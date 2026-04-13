# Security Documentation - Rescale Interlink

**Version:** 4.9.1
**Last Updated:** 2026-04-12

## Overview

Rescale Interlink is designed with security as a core priority, especially for FedRAMP compliance. This document outlines the security architecture, FIPS 140-3 compliance requirements, and important security considerations for deployment.

---

## FIPS 140-3 Compliance

Rescale Interlink REQUIRES FIPS 140-3 compliant builds for production use. This is mandatory for FedRAMP environments.

### Build Requirements

All production builds must be compiled with:

```bash
GOFIPS140=latest wails build
# or
make build
```

### Runtime Verification

The application verifies FIPS compliance at startup. Non-FIPS builds will:
- Display a critical error message
- Exit with code 2

For development only, bypass with:
```bash
RESCALE_ALLOW_NON_FIPS=true ./rescale-int
```

**Warning:** Never use non-FIPS builds in production or with FedRAMP platforms.

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

#### For FedRAMP Environments (`rescale-gov.com`)

- **NTLM proxy mode is automatically disabled** in the GUI when a FedRAMP platform is selected
- If NTLM is configured when switching to a FedRAMP platform, it automatically switches to `basic`
- A warning is displayed explaining the restriction

#### For Commercial Rescale Platforms

- NTLM proxy mode is available and supported
- The FIPS boundary is the Rescale API communication, not the proxy
- Use NTLM if your corporate proxy requires it

### Recommendations

1. **Always prefer `basic` authentication over TLS** for maximum compatibility
2. If your proxy requires NTLM and you're targeting FedRAMP:
   - Contact your IT team for proxy alternatives
   - Consider using a FIPS-compliant proxy gateway
3. The startup warning will alert you if NTLM is configured in a FIPS build

---

## Log Security

### Directory Permissions

All log directories are created with `0700` permissions (Unix) to restrict access to the owner only:

| Platform | Log Location | Permissions |
|----------|-------------|-------------|
| Unix | `~/.config/rescale/logs/` | `drwx------` (0700) |
| Windows | `%LOCALAPPDATA%\Rescale\Interlink\logs\` | Owner-only via ACL |

### Log Contents

Logs may contain:
- Operation timestamps
- Job and file identifiers
- Error messages
- Transfer progress

Logs do **not** contain:
- API keys (full or partial)
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
| Token file (`~/.config/rescale/token`) | Yes | 0600 permissions |
| Environment variable (`RESCALE_API_KEY`) | Yes | Cleared after session |
| Config file (`config.csv`) | **No** | Keys are ignored from config.csv |

### Transmission

- API keys are transmitted only over HTTPS
- Keys are never logged (not even partially)
- The GUI allows viewing the key (toggle) but never exposes it externally

---

## Platform URL Allowlist

Rescale Interlink restricts API communication to a fixed set of known Rescale platform URLs.
This prevents credential exfiltration to arbitrary endpoints via `--api-url` or configuration files.

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

The Windows daemon uses named pipes for IPC with a two-tier security model:

1. **Connection Level**: Any authenticated user can connect
2. **Operation Level**: Modify operations require owner authorization

### Modify Operations (Protected)

These require the caller's SID to match the daemon owner:
- `PauseUser`
- `ResumeUser`
- `TriggerScan`
- `Shutdown`

### Read Operations (Open)

These are available to any authenticated user:
- `GetStatus`
- `GetUserList`
- `GetRecentLogs`
- `OpenLogs`

### Fail-Closed Authorization

If the daemon cannot capture the owner SID at startup, all modify operations are denied. This prevents potential authorization bypass.

---

## Windows Service Mode

When running as a Windows Service:

1. **User isolation**: Each user's daemon instance is scoped to their profile
2. **Elevated controls**: Starting/stopping the service requires UAC approval
3. **Per-user state**: Downloads, pauses, and scans are user-specific

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
2. Per-user token file (service mode: `<userProfile>/.config/rescale/token`)
3. `apiconfig` INI file (service mode: legacy compatibility)
4. Default token file (`~/.config/rescale/token`)
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

The following are **not reportable** (users can fix these themselves):
- Authentication errors (401/403)
- Network/DNS errors
- Timeout errors
- Disk space errors
- Client errors (400/404)
- User cancellation
- Rate limit responses (429)

**Source:** `internal/reporting/classifier.go` — `IsReportable()` function

### Redaction

All error messages are redacted before inclusion in reports:
- Hex tokens >20 characters → `[REDACTED]`
- URL query parameters → `?[REDACTED]`
- Email addresses → `[EMAIL]`
- Bearer/authorization tokens → `[REDACTED]`
- Home directory paths → `[HOME]`
- File paths reduced to basename only
- Job names replaced with `job-N` placeholders

**Source:** `internal/reporting/redactor.go`

### Report Delivery

- **GUI:** Modal dialog with "Copy to Clipboard" and "Save Report" options. Duplicate suppression while modal is open.
- **CLI/Daemon:** Auto-saved to the report directory with path printed to stderr.
- Reports include workspace name, workspace ID, and platform URL for support context.
- Reports **do not** contain API keys, passwords, file contents, or full file paths.

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
| 4.9.1 | 2026-04-12 | CLI compat mode with independent credential chain; `jobs watch` command |
| 4.9.0 | 2026-03-25 | Error reporting privacy model (redaction, reportability filtering); sleep prevention |
| 4.8.7 | 2026-03-11 | Platform URL allowlist — strict origin enforcement, credential exfiltration prevention |
| 4.8.2 | 2026-03-02 | Automatic update check with policy gate (FedRAMP, env var), trusted URL enforcement |
| 4.7.5 | 2026-02-25 | Empty file upload fix |
| 4.7.3 | 2026-02-22 | Path traversal sanitization in GetHistoricalJobRows, event listener isolation |
| 4.5.1 | 2026-01-28 | Log permissions hardened (0700), NTLM/FIPS safeguards, fail-closed IPC auth |
| 4.4.2 | 2025-12-XX | Centralized log directory, file permissions security (0600 for sensitive state) |
| 4.0.0 | 2025-XX-XX | Initial FIPS 140-3 compliance |
