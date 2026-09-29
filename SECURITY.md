# Security Documentation - Rescale Interlink

**Version:** 4.9.9
**Last Updated:** 2026-09-26

## Overview

Rescale Interlink is designed with security as a core priority, especially for FedRAMP compliance. This document outlines the security architecture, FIPS 140-3 compliance requirements, and important security considerations for deployment.

---

## FIPS 140-3 Compliance

Rescale Interlink REQUIRES FIPS 140-3 compliant builds for production use. This is mandatory for FedRAMP environments.

### Build Requirements

All production builds must be compiled with:

```bash
# GUI binary
GOFIPS140=certified wails build -tags fips

# CLI binary (this is what `make build` runs)
make build
```

`GOFIPS140=certified` selects the latest Go Cryptographic Module version holding a CMVP
validation certificate. `GOFIPS140=latest` is **not** equivalent — it tracks the
toolchain-bundled module, which may not yet be CMVP-validated, and must not be used for
release builds.

### Runtime Verification

The application verifies FIPS compliance at startup. Non-FIPS builds will:
- Display a critical error message naming the correct rebuild command
- Exit with code 2

For development only, bypass with:
```bash
RESCALE_ALLOW_NON_FIPS=true ./rescale-int
```

**Warning:** Never use non-FIPS builds in production or with FedRAMP platforms.

### Build and Release Integrity

Release artifacts are produced by `.github/workflows/release.yml`, on a `v*` tag push or
a maintainer's manual run from any branch, with the following controls:

- **No release is replaced.** A first job stops a manual run whose version already has
  a tag or a release, draft or published, and on a tag push leaves an already published
  release untouched; the release job checks again before it creates the draft. Every run
  makes a draft, and a manual run's tag is created only when its draft is published.
- **Gated on verification.** A `verify` job runs before any build: the FIPS-tagged Go test suite
  (`make test`), `GOFIPS140=certified go vet -tags fips ./...`, and the frontend test,
  lint, and build. The Windows and macOS build jobs declare `needs: [verify]`, so a
  failing check blocks the release rather than shipping beside it.
- **Pinned, checksum-verified toolchain.** Go 1.26.7 is downloaded and checked against
  its published SHA-256 before install. Node.js is pinned to 24.21.0 in every job
  (checksum-verified in the Linux build), and the WebView2 runtime bundled into the MSI
  is pinned by version and SHA-256; the installer build stops if it was not bundled
  completely.
- **Deterministic dependency installs.** `npm ci` installs exactly what
  `package-lock.json` pins. This is also what `wails.json`'s `frontend:install` runs, so
  local and CI builds resolve the same tree.
- **Code signing.** Windows: the executable and the MSI are both signed via Azure
  Trusted Signing with RFC-3161 timestamps. macOS: the `.app` bundle is deep-signed and
  the standalone CLI is signed with the hardened runtime and a timestamp, signatures are
  verified in-job, then the bundle is submitted to `notarytool` and the notarization
  ticket is stapled.
- **Checksums and build paths.** Every release asset (macOS zip, Windows MSI and zip,
  Linux tarball) is published with a `.sha256` that `shasum -a 256 -c` accepts, and
  every binary is built with `-trimpath`, so it records no build-machine paths.
- **Linux AppImage self-check.** The Linux build runs in
  `.github/workflows/release-linux.yml`, in an `almalinux:8` container, with every
  download checked against a pinned SHA-256. Its packaging step bundles WebKit's helper
  executables into the AppDir with `$ORIGIN`-relative RPATHs, and `build/linux/verify-appimage.sh` then inspects the
  finished AppImage — deliberately the built image rather than the AppDir, so a missing
  RPATH cannot hide behind a matching host WebKit — and fails the release before the
  artifact ships if the helpers are missing, not executable, or resolve their WebKit/GTK
  dependencies from the host instead of the bundle. Without this, WebKit forks the host's
  `WebKitWebProcess` from a compiled-in path and a mismatched build breaks its IPC.

### S3 FIPS Endpoints (ITAR Platforms)

For ITAR platforms (`itar.rescale.com` and `itar.rescale-gov.com`), Interlink automatically routes S3 API traffic through AWS FIPS-validated endpoints. This is detected at runtime by `shouldUseFIPSEndpoint()` in `internal/cloud/providers/s3/client.go` and applied by `newSDKClient()`, which sets `EndpointOptions.UseFIPSEndpoint` to `aws.FIPSEndpointStateEnabled`.

- **Scope**: All S3 operations (upload, download, multipart, credential refresh)
- **Trigger**: Platform URL contains `itar.rescale-gov.com` or `itar.rescale.com`
- **Effect**: AWS SDK resolves to FIPS 140-2 validated S3 endpoints (e.g., `s3-fips.us-east-1.amazonaws.com`)
- **No user configuration required**: Enabled automatically based on the configured platform URL

This satisfies FedRAMP Moderate requirements for FIPS-validated data-in-transit to S3 storage.

The S3 client is built only from what the platform supplies: region, temporary
credentials, the HTTP client and the FIPS switch. AWS environment variables and shared
config or credentials files do not affect it, and CRC32 request checksums and response
validation are on.

---

## Proxy Authentication

### Supported Modes

| Mode | Description | FIPS Compliant |
|------|-------------|----------------|
| `no-proxy` | Direct connection | Yes |
| `system` | Use system proxy settings | Depends on system |
| `basic` | Basic authentication over TLS | Yes |
| `ntlm` | NTLM authentication | **No** |

### NTLM and FIPS Compliance

NTLM proxy authentication (`proxy_mode=ntlm`) uses the Azure/go-ntlmssp library which requires MD4 and MD5 hashing algorithms. These algorithms are **not FIPS 140-3 approved**.

#### For FIPS-Tagged Builds

- **NTLM proxy mode is disabled by build policy** on all platforms
- The NTLM transport implementation is excluded from FIPS-tagged builds
- Backend config validation and HTTP client setup reject `proxy_mode=ntlm`
- The GUI hides/disables NTLM when the running build reports that it is unavailable

#### For FedRAMP Environments (`rescale-gov.com`)

- **NTLM proxy mode is automatically disabled** in the GUI when a FedRAMP platform is selected
- If NTLM is configured when switching to a FedRAMP platform, it automatically switches to `basic`
- A warning is displayed explaining the restriction

#### For Non-FIPS Commercial Builds

- NTLM proxy mode is available and supported
- The FIPS boundary is the Rescale API communication, not the proxy
- Use NTLM if your corporate proxy requires it

### Recommendations

1. **Always prefer `basic` authentication over TLS** for maximum compatibility
2. If your proxy requires NTLM and you're using a FIPS-tagged or FedRAMP build:
   - Contact your IT team for proxy alternatives
   - Consider using a FIPS-compliant proxy gateway
3. A FIPS-tagged build rejects `proxy_mode=ntlm` before creating an API client

---

## Log Security

### Directory Permissions

All log directories are created with `0700` permissions (Unix) to restrict access to the owner only:

| Platform | Log Location | Permissions |
|----------|-------------|-------------|
| Unix | `~/.config/rescale/logs/` | `drwx------` (0700) |
| Windows | `%LOCALAPPDATA%\Rescale\Interlink\logs\` | Inherits from `%LOCALAPPDATA%`, which is per-user owner-restricted by default. Unlike the token file, the log directory does not get an explicit DACL applied by Interlink. |

### Log Contents

Logs may contain:
- Operation timestamps
- Job and file identifiers
- Error messages
- Transfer progress

Logs do **not** contain:
- API keys (full or partial)
- Storage access signatures, storage keys or `Authorization` values: every log line and
  error passes through the redactor described under [Redaction](#redaction)
- Proxy passwords
- File contents
- Encryption keys

### Best Practices

1. Regularly rotate and archive logs
2. Do not share log files without reviewing contents
3. Use the built-in log rotation (keeps 5 backups, 30-day retention)

---

## API Key Security

### Storage

API keys should be stored securely:

| Method | Recommended | Notes |
|--------|-------------|-------|
| Token file (Unix: `~/.config/rescale/token`; Windows: `%LOCALAPPDATA%\Rescale\Interlink\token`, with the legacy `%APPDATA%\Rescale\Interlink\token` still read) | Yes | 0600 on Unix; explicit DACL on Windows (owner + Administrators + SYSTEM, no inheritance) |
| Environment variable (`RESCALE_API_KEY`) | Yes | Cleared after session |
| Config file (`config.csv`) | **No** | Keys are ignored from config.csv |

### Transmission

- API keys are transmitted only over HTTPS
- Keys are never logged (not even partially)
- `config init` does not echo the key while you type it
- The GUI allows viewing the key (toggle) but never exposes it externally
- **Auth scheme selected by key shape**: API tokens use `Authorization: Token <key>`; short-lived JWT-shaped credentials (three dot-separated `ey…` segments) automatically switch to `Authorization: Bearer <key>`. Selection is per request, based on the credential value at the time of the call

---

## Platform URL Allowlist

Rescale Interlink restricts API communication to a fixed set of known Rescale platform URLs.
This prevents credential exfiltration to arbitrary endpoints via `config.csv api_base_url`, the compat-mode `-X/--api-base-url` flag, or `RESCALE_API_URL`.

- **Allowlist enforcement**: `ValidatePlatformURL()` in `internal/config/platforms.go` checks URLs against 6 known Rescale platform hostnames
- **Strict origin validation**: HTTPS-only, no custom ports, no userinfo, no path/query/fragment components accepted
- **Primary enforcement point**: `api.NewClient()` — all client creation paths pass through here, including engine startup, GUI test-connection, PUR, CLI, and daemon
- **Defense-in-depth**: `config.Validate()` also checks the platform URL
- **CLI protection**: `config init` uses a numbered menu instead of free-text URL input
- **GUI**: Already restricted to dropdown selection since v4.3.0

**Allowed platforms:**
- `https://platform.rescale.com` (North America)
- `https://kr.rescale.com` (Korea)
- `https://platform.rescale.jp` (Japan)
- `https://eu.rescale.com` (Europe)
- `https://itar.rescale.com` (US ITAR)
- `https://itar.rescale-gov.com` (US ITAR FRM)

---

## Update Checks

Rescale Interlink can check GitHub for newer releases on GUI startup. This makes a single
unauthenticated HTTPS request to `api.github.com`.

- **Disabled by default on FedRAMP platforms** (`rescale-gov.com` domains)
- **Environment variable kill switch**: Set `RESCALE_DISABLE_UPDATE_CHECK=1` to disable
- **No credentials sent**: Request is unauthenticated (GitHub public API)
- **Rate limited**: Results cached for 24 hours (errors cached 1 hour)
- **Trusted URLs only**: The "open in browser" action opens a hardcoded GitHub URL, not API-provided URLs
- **Proxy aware**: Respects configured proxy settings (without warmup side effects)

---

## IPC Security (Windows)

### Authorization Model

Each user's daemon listens on a named pipe of its own, `\\.\pipe\rescale-interlink-<SID>`,
named with that user's SID. Access is checked twice:

1. **Connection level**: the pipe admits only that user and LocalSystem, and is owned by
   the user. The daemon never joins a pipe that already exists, and the app, tray and
   CLI use a pipe only if the current user owns it. If another account holds the name,
   auto-download does not start, and the tray, the app and `daemon run` say so.
2. **Operation level**: modify operations also require the caller's SID to match the
   daemon owner's.

Without the user's SID the IPC server does not start and clients refuse; nothing falls
back to a pipe name shared between users. The rate-limit coordinator's pipe,
`\\.\pipe\rescale-ratelimit-coordinator-<SID>`, follows the same rules. On macOS and
Linux both sockets are in the user's `~/.config/rescale`, with mode `0600`, and without
a home folder neither starts.

### Modify Operations (Protected)

These require the caller's SID to match the SID of the user who started the daemon:
- `PauseUser`
- `ResumeUser`
- `TriggerScan`
- `ReloadConfig`
- `Shutdown`
- `CancelDaemonBatch`
- `CancelDaemonTransfer`
- `RetryFailedInDaemonBatch`

### Fail-Closed Authorization

If the daemon cannot capture the owner SID at startup, the IPC server does not start, so
`daemon run --ipc` stops with an error. A modify request whose caller SID cannot be
captured at the pipe layer is denied (`authorizeModifyRequest` in `internal/ipc/server.go`).

---

## Encryption

### File Encryption

Interlink uses mandatory AES-256 encryption for all file transfers:
- AES-256-CBC with PKCS7 padding
- Random 256-bit keys and 128-bit IVs for each encryption operation
- Legacy uploads (v3.1.x) used per-part keys derived via HKDF-SHA256; current uploads use CBC chaining with a single key/IV pair

### TLS

All API communication uses TLS 1.2+ with FIPS-approved cipher suites when FIPS mode is active.

---

## API Key Resolution Priority

Rescale Interlink resolves API credentials through a priority chain that differs between native and compat modes.

### Native CLI / GUI

1. `--api-key` command-line flag (highest priority)
2. Per-user token file in the resolved user-profile directory
3. `apiconfig` INI file in the resolved user-profile directory (legacy)
4. Default token file (Unix: `~/.config/rescale/token`; Windows: `%LOCALAPPDATA%\Rescale\Interlink\token`, then the legacy `%APPDATA%\Rescale\Interlink\token`)
5. `RESCALE_API_KEY` environment variable (lowest priority)

**Source:** `internal/config/apikey.go`

### Compat Mode (rescale-cli compatibility)

1. `-p/--api-token` flag (highest priority)
2. `RESCALE_API_KEY` environment variable
3. `apiconfig` INI profile (`--profile` section, or `[default]`)

**Source:** `internal/cli/compat/compat.go`

### Base URL Resolution (compat mode)

1. `-X/--api-base-url` flag
2. `RESCALE_API_URL` environment variable
3. Profile URL from apiconfig
4. Default: `https://platform.rescale.com`

---

## Error Reporting Privacy Model

The `internal/reporting/` package provides safe serious-error reporting. Reports are generated only for genuine server-side failures — never for user-fixable problems.

### What Gets Reported

Only errors where the user cannot self-diagnose are reportable:
- **Server errors** (HTTP 5xx) — the server broke
- **Unclassified internal errors** — something unexpected happened

The following are **not reportable** (users can fix these themselves, or nothing broke):
- Authentication errors (401/403)
- Network/DNS errors
- Timeout errors
- Disk space errors
- Client errors (400/404)
- Local filesystem refusals (permissions, missing path, read-only volume), including an
  upload refused by another transfer's upload lock
- User cancellation, and daemon-stopped
- Rate limit responses (429)

**Source:** `internal/reporting/classifier.go` — `IsReportable()` and
`ClassifyErrorClass()`

Two further filters run ahead of `IsReportable()` on the CLI and daemon path, in
`internal/reporting/cli_helper.go`:

- **Usage errors** — a missing required flag, an unknown flag, conflicting flags, a
  refusal to prompt without a terminal, a user abort, an output file that already
  exists, a refused `daemon run` or a `daemon stop` that timed out, or a pre-flight
  validation failure ("no valid files to upload"). These are user mistakes or local
  conditions, not system failures.
- **Aggregate roll-ups** — the batch summary a command returns after it has already
  reported each failure item by item (`N file(s) failed to upload`,
  `N of M job(s) failed`, and similar). Reporting the roll-up as well would duplicate
  diagnostics that carry no additional detail.

When a report *is* generated, the diagnostic summary is printed to stderr first and
unconditionally. Whether the report file could also be written is secondary: a save
failure is a debug-only note rather than a warning that competes with the real failure.

### Redaction

Error text is redacted wherever it is shown or written — the terminal, the GUI, logs,
the daemon's records and error reports. The first layer (`RedactSecrets`) removes
credentials and keeps the surrounding text:
- Signed-URL parameters (Azure SAS `sig`, `se`, `sp`, `sv` and the like; S3 `X-Amz-*`),
  plain or HTML-, percent- or JSON-escaped → `name=REDACTED`
- Storage and AWS keys (`AccountKey=`, `accessKey`, `secretKey`, `sessionToken`, also as
  JSON fields) → `REDACTED`
- `Authorization` and `Proxy-Authorization` values of any scheme → the scheme is kept,
  the value becomes `REDACTED`
- AWS access key IDs (`AKIA…`) → `[REDACTED_AWS_KEY]`

Reports then apply these rules as well:
- Hex tokens of **20 or more** characters → `[REDACTED]`
- URL query parameters → `?[REDACTED]`
- Email addresses → `[EMAIL]`
- Strings of the form `(bearer|token|key|authorization)[=:\s]+VALUE` → the keyword is preserved and the value becomes `[REDACTED]` (e.g., `bearer abc...` → `bearer=[REDACTED]`)
- Home directory paths → `[HOME]`

In addition, when the report includes the recent **timeline snapshot** (transfer events, job state changes), each timeline entry has its file paths reduced to basename only and job names replaced with `job-N` placeholders. These two transformations apply to timeline entries only, not to the generic redacted error message.

**Source:** `internal/reporting/redactor.go`

### Report Delivery

- **GUI:** Modal dialog with "Copy to Clipboard" and "Save Report" options. Duplicate suppression while modal is open.
- **CLI/Daemon:** Auto-saved to the report directory with path printed to stderr.
- **Retention:** The report directory keeps the newest 500 files and deletes the rest on
  each write. Pruning is best-effort, so it can never fail the report it just wrote. This
  bounds a repeating failure — a daemon retrying the same broken scan every poll used to
  write one file per occurrence, forever (`internal/reporting/transport.go`).
- Reports include workspace name, workspace ID, and platform URL for support context.
- Reports **do not** contain API keys, storage credentials, passwords or file contents.
  Home-directory paths are shortened to `[HOME]`, and timeline entries keep only file
  names.

---

## Sleep Prevention

During file transfers, Rescale Interlink prevents the operating system from sleeping or suspending, which would interrupt active transfers.

### Cross-Platform Implementation

| Platform | Mechanism |
|----------|-----------|
| macOS | `IOPMAssertionCreateWithName` (IOKit, via CGO) |
| Windows | `SetThreadExecutionState` |
| Linux | `systemd-inhibit` |

### Integration

Sleep prevention is ref-counted in the rate limiter store (`internal/ratelimit/store.go`). An assertion is acquired when a transfer batch starts and released when the batch completes, covering all transfer paths (CLI, GUI, and daemon).

Each platform's release function is idempotent (safe to call multiple times) via `sync.Once`.

**Source:** `internal/platform/sleep.go`, `internal/platform/sleep_darwin.go`, `internal/platform/sleep_linux.go`, `internal/platform/sleep_windows.go`

---

## Reporting Security Issues

If you discover a security vulnerability, please report it to:
- Email: security@rescale.com
- Include "Interlink Security" in the subject line
- Provide steps to reproduce if possible

Do not disclose security issues publicly until a fix is available.

---

## Version History

| Version | Date | Security-Relevant Changes |
|---------|------|---------------------------|
| 4.9.9 | Unreleased | Security hardening in credential handling, file handling, Windows auto-download and the release process |
| 4.9.8 | 2026-05-31 | FIPS 140-3 build path tightening; security dependency refresh to clear advisories; cleaner credential source DTO across GUI and CLI |
| 4.9.4 | 2026-04-19 | Explicit Windows DACL on token file (owner + Administrators + SYSTEM, no inheritance); IPC caller-SID scoping consolidated behind one helper with catalog-wide fail-closed enforcement |
| 4.9.3 | 2026-04-15 | AWS SDK security bump (eventstream DoS fix); CodeQL quality cleanup |
| 4.9.2 | 2026-04-13 | S3 FIPS endpoints for ITAR platforms; Windows service credential isolation |
| 4.9.1 | 2026-04-12 | CLI compat mode with independent credential chain; `jobs watch` command |
| 4.9.0 | 2026-04-03 | Error reporting privacy model (redaction, reportability filtering); sleep prevention |
| 4.8.7 | 2026-03-11 | Platform URL allowlist — strict origin enforcement, credential exfiltration prevention |
| 4.8.2 | 2026-03-02 | Automatic update check with policy gate (FedRAMP, env var), trusted URL enforcement |
| 4.7.5 | 2026-02-25 | Empty file upload fix |
| 4.7.3 | 2026-02-22 | Path traversal sanitization in GetHistoricalJobRows, event listener isolation |
| 4.5.1 | 2026-01-28 | Log permissions hardened (0700), NTLM/FIPS safeguards, fail-closed IPC auth |
| 4.4.2 | 2026-01-19 | Centralized log directory, file permissions security (0600 for sensitive state) |
| 4.0.0 | 2025-12-27 | Initial FIPS 140-3 compliance |
