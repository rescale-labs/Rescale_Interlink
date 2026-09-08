# Rescale Interlink CLI Guide

Complete command-line interface reference for `rescale-int` v4.9.9.

**Version:** 4.9.9
**Cryptography:** FIPS 140-3. At startup the binary checks that Go's FIPS 140-3
module is active; one built without it refuses to start and exits `2`.

For a comprehensive list of all features with source code references, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

## Table of Contents

- [System Requirements](#system-requirements)
- [Installation](#installation)
- [Configuration](#configuration)
- [Global Flags](#global-flags)
- [Exit Codes](#exit-codes)
- [Output, Retries, and Rate Limits](#output-retries-and-rate-limits)
- [Quick Start](#quick-start)
- [Command Reference](#command-reference)
  - [Config Commands](#config-commands)
  - [File Commands](#file-commands)
  - [Folder Commands](#folder-commands)
  - [Job Commands](#job-commands)
  - [Daemon Commands](#daemon-commands)
  - [Service Commands (Windows only)](#service-commands-windows-only)
  - [Hardware Commands](#hardware-commands)
  - [Software Commands](#software-commands)
  - [Automations Commands](#automations-commands)
  - [PUR (Parallel Upload and Run) Commands](#pur-parallel-upload-and-run-commands)
  - [Shortcuts](#shortcuts)
  - [Internal Commands](#internal-commands)
- [Shell Completion](#shell-completion)
- [Compatibility Mode](#compatibility-mode)
- [Compatibility Reference](#compatibility-reference)
- [Examples](#examples)
- [Performance Tips](#performance-tips)
- [Troubleshooting](#troubleshooting)
- [Support](#support)
- [Version](#version-1)

## System Requirements

**Operating System:**
- **macOS**: 10.15 (Catalina) or later. The published archive is an Apple Silicon
  (`aarch64`) build; an Intel build can be produced from a checkout with
  `make build-darwin-amd64`
- **Windows**: Windows 10 or later (64-bit)
- **Linux**: `x86_64`, built as `linux/amd64`

The Linux distribution floors below describe where the `linux/amd64` build is
supported. Nothing enforces them: the build records no GLIBC minimum and the
binary makes no such check at startup, so it may well start on an older system
— and may equally fail in the dynamic linker instead.

- GLIBC 2.27+
- RHEL/CentOS/Rocky/Alma 8+
- Ubuntu 18.04+
- Debian 10+
- CentOS/RHEL 7 and older are not supported (end-of-life, GLIBC too old)

If you see an error like `GLIBC_2.27 not found`, your Linux distribution is too old and not supported.

## Installation

Download the archive for your platform from the releases page. A release is built
to carry these assets, where `<tag>` is the release tag (for example `v4.9.9`):

- **macOS (Apple Silicon)**: `rescale-interlink-<tag>-macos_aarch64.zip`
- **Windows**: `rescale-interlink-<tag>-win_amd64.zip` (portable) or
  `rescale-interlink-<tag>-win_amd64.msi` (installer)

No Linux asset is built, so check the release page itself for what a given
release actually carries.

Unpack the archive. The CLI binary inside is named `rescale-int` (`rescale-int.exe`
on Windows); each archive also carries the GUI application alongside it.

On Linux, build the CLI from a checkout instead. The module requires Go 1.26.7 or
newer:

```bash
make build-linux-amd64          # writes bin/<version>/linux-amd64/rescale-int
```

`<version>` is the version string including its leading `v`, so for v4.9.9 the
binary is at `bin/v4.9.9/linux-amd64/rescale-int`. `make package` writes
`dist/rescale-int-<version>-linux-amd64.tar.gz`, but it also packages macOS and
Windows, so it fails unless those binaries have been built too — run
`make build-all` first, or just take the binary from the path above.

Make the binary executable (macOS/Linux):
```bash
chmod +x rescale-int
sudo mv rescale-int /usr/local/bin/
```

## Configuration

### Interactive Setup

Run the interactive configuration wizard:

```bash
rescale-int config init
```

This will prompt you for:
- API key (required)
- API base URL (default: https://platform.rescale.com)
- Worker settings (tar, upload, job workers)
- Proxy configuration (optional)

**Note:** Worker and tar settings are also configurable from the GUI PUR tab's Pipeline Settings section. Settings in config.csv are shared between CLI and GUI modes.

Configuration is saved to `~/.config/rescale/config.csv` on macOS and Linux, and
to `%LOCALAPPDATA%\Rescale\Interlink\config.csv` on Windows. The API key goes to
a `token` file beside it, not into `config.csv`.

**Note:** If the new location holds no `config.csv`, an existing one at the old
location is detected and used automatically — `~/.config/rescale-int/` on
macOS and Linux, `%APPDATA%\Rescale\Interlink\` (Roaming) on Windows. A message
suggesting the new location is written to the diagnostic log, so it is visible
only with `--verbose` or `--debug`.

### Manual Configuration

Create a CSV file with key-value pairs:

```csv
key,value
api_base_url,https://platform.rescale.com
tar_workers,4
upload_workers,4
job_workers,4
proxy_mode,no-proxy
```

**Note:** API keys and proxy passwords are NOT stored in config files for
security reasons. An `api_key` or `proxy_password` row left in an old config file
is ignored, and the CLI prints a warning to stderr naming the row and what to use
instead.

**Note:** `api_base_url` (and the `--api-url` flag) accept only these six Rescale
platform origins:

| URL | Region |
|-----|--------|
| `https://platform.rescale.com` | North America |
| `https://kr.rescale.com` | Korea |
| `https://platform.rescale.jp` | Japan |
| `https://eu.rescale.com` | Europe |
| `https://itar.rescale.com` | US ITAR |
| `https://itar.rescale-gov.com` | US ITAR FRM |

The match is on scheme and host only. A value with no scheme has `https://`
prepended and a trailing `/` is ignored, so `platform.rescale.com` and
`https://platform.rescale.com/` both resolve to the North America entry. An
`http://` scheme, a port, userinfo, a path, a query, or a fragment is rejected,
each with a one-line reason (`platform URL must use HTTPS (got "http")`,
`platform URL must not specify a port`, and so on); an unrecognized host is
rejected with the list of valid platforms. This is deliberate: it stops a
mistyped or injected URL from sending your API key somewhere that is not
Rescale.

### API Key Configuration

**Option 1: Environment Variable**
```bash
export RESCALE_API_KEY="your-api-key"
```

**Option 2: Token File (recommended for scripts)**
```bash
# Create token file with restricted permissions
mkdir -p ~/.config/rescale
echo "your-api-key" > ~/.config/rescale/token
chmod 600 ~/.config/rescale/token

# Use token file
rescale-int --token-file ~/.config/rescale/token <command>
```

The file holds the raw token and nothing else; surrounding whitespace is
trimmed. Permissions looser than `0600` produce a warning on stderr but do not
stop the read. A `--token-file` that is missing, unreadable or empty is skipped
silently rather than failing the command, so a lower-priority source — the
default token file — can still supply a key. Run `rescale-int config test` if
you need to know which key a setup actually resolves to.

**Option 3: Command-Line Flag (not recommended)**
```bash
rescale-int --api-key "your-api-key" <command>
```

### Priority Order

Configuration values are merged with this priority:
1. `--api-key` command-line flag (highest)
2. `RESCALE_API_KEY` environment variable
3. `--token-file` flag
4. Default token file (`~/.config/rescale/token`, or
   `%LOCALAPPDATA%\Rescale\Interlink\token` on Windows)
5. Configuration file (non-credential settings only)
6. Default values (lowest)

If several of these are set to *different* values, the CLI prints one warning
naming the sources it found and the one it used.

### Proxy Configuration

For enterprise environments requiring proxy access, configure proxy settings in your config file:

```csv
key,value
proxy_mode,basic
proxy_host,proxy.company.com
proxy_port,8080
proxy_user,username
```

**Supported Proxy Modes:**
- `no-proxy` - Direct connection (default)
- `system` - Use system proxy settings (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` environment variables, in either case — Go's own environment proxy selection reads the lowercase spellings too)
- `basic` - HTTP Basic authentication
- `ntlm` - NTLM authentication for corporate proxies, available only in builds that support NTLM

**Notes:**
- Proxy passwords are prompted at runtime for security (not stored in config files)
- FIPS-tagged builds reject `proxy_mode=ntlm` because NTLM requires non-FIPS MD4/MD5 algorithms
- All traffic (API calls + S3/Azure storage) routes through the configured proxy
- The `no_proxy` config key supplies bypass rules (comma-separated hostnames,
  wildcards, CIDRs) and is wired to the HTTP transport in `basic` and `ntlm`
  modes; it is also configurable from the GUI Setup tab. In `system` mode the
  bypass rules come from the environment's `NO_PROXY` instead, because that mode
  hands proxy selection to Go's own `ProxyFromEnvironment`
- `proxy_port` defaults to `8080` when a host is configured without one
- Setting `HTTPS_PROXY` while no `proxy_host` is configured fills in the host and
  port from that URL, and switches `proxy_mode` from `no-proxy` to `system`. That
  import is a plain colon split of the value with its scheme removed, not a URL
  parse, so it handles only `host:port` (and bare `host`). A proxy URL carrying
  credentials imports the username as the host, and an IPv6 literal imports as
  nonsense. What such a run actually uses is `system` mode, where Go reads the
  environment variable itself and handles both forms correctly — the imported
  `proxy_host`/`proxy_port` are what `config show` will display, and are wrong.
  Configure `proxy_host` and `proxy_port` explicitly if those fields matter
- `basic` and `ntlm` fall back to a direct connection, with a warning, when
  `proxy_host` is empty
- A configured `proxy_user` with no password makes non-interactive commands fail
  with `proxy authentication required but no password provided and not running in
  interactive terminal`. There is no flag that supplies the password, so
  unattended runs need a proxy that does not require one, or `system` mode, where
  the proxy URL in the environment carries whatever the proxy needs.
  `config init` prompts only for proxy mode, host and port — set `proxy_user` in
  `config.csv` or from the GUI
- `config test` is not a check of that: it loads, merges and validates the
  configuration directly rather than going through the path that prompts for the
  password, so it can reach the network with incomplete proxy credentials and
  report a failure an ordinary command would have refused up front
- These settings apply to the native CLI only. [Compatibility mode](#compatibility-mode)
  builds its own configuration from the API key and base URL alone: it reads no
  proxy settings from `config.csv` and does no environment merging, so compat
  traffic always goes direct

### Advanced Configuration Options

Additional configuration options for specialized use cases:

| Key | Description | Default |
|-----|-------------|---------|
| `tar_workers` | Number of concurrent tar operations | 4 |
| `upload_workers` | Number of concurrent upload workers | 4 |
| `job_workers` | Number of concurrent job submission workers | 4 |
| `exclude_pattern` | Patterns to exclude from tarballs (semicolon-separated, e.g., `*.log;*.tmp`) | (none) |
| `include_pattern` | Include-only patterns. Setting both this and `exclude_pattern` makes the file fail to load, rather than one silently taking precedence | (none) |
| `flatten_tar` | Remove subdirectory structure in tarballs (`true`/`false`) | false |
| `run_subpath` | Scan prefix: subpath to navigate into before scanning for run directories (e.g., `Simcodes/Powerflow`) | (none) |
| `validation_pattern` | Pattern to validate runs (e.g., `*.avg.fnc`), opt-in | (none) |
| `tar_compression` | Compression type: `none` or `gzip`. Only the exact value `none` disables compression; anything else (including the legacy `gz`) produces a gzip archive. The GUI displays legacy `gz` as `gzip`, and saving from the GUI writes that normalized `gzip` back to `config.csv`; editing the file by hand leaves whatever you typed. `--tar-compression` is not validated, so a typo silently means gzip | none |
| `max_retries` | How many *attempts* a PUR upload worker makes at a whole archive, not how many retries it adds: the default of `1` means one attempt and no retry, and a value below 1 is treated as 1. A second attempt is made only when the first failure looks like a proxy or connection timeout — its text contains `timeout`, `SocketTimeoutException`, `connection reset` or `EOF`. Separate from the storage and API retry layers described under [Output, Retries, and Rate Limits](#output-retries-and-rate-limits), which run inside each attempt | 1 |
| `proxy_warmup` | Send a warm-up request through the proxy before credential calls (`true`/`false`) | false |
| `tenant_url` | Legacy alias for `api_base_url`; the two are kept in sync on load and on save | (none) |
| `org_code` | Organization code used for org-scoped project assignment | (none) |
| `detailed_logging` | GUI toggle for timing and metrics diagnostics. The CLI reads the key but does not act on it — pass `--timing` (or set `RESCALE_TIMING=1`) for the same output from a CLI run | false |
| `sort_field` | GUI file-browser sort column | `name` |
| `sort_ascending` | GUI file-browser sort direction (`true`/`false`) | true |

`tar_workers`, `upload_workers` and `job_workers` must each be at least 1.
`rescale-int config test` checks that and reports `tar_workers must be at least
1`. `pur run`, `pur resume` and `pur submit-existing` check it too, after any
`--tar-workers` / `--upload-workers` / `--job-workers` override has been applied,
so they judge the value the pipeline would actually be built with. A `0` or
negative count stops the command before any archive is built, with the key and
the value named:

```
Error: tar_workers must be at least 1 (got 0)
```

Loading the file still does not apply the check, so commands that build no
pipeline — `config show` among them — read a bad value without complaint. Note
also that a zero or negative value passed on the command line is *ignored* rather
than rejected: the flag handlers apply an override only when it is greater than
zero, so `--tar-workers 0` leaves the configured count in place and the run
proceeds on that.

Keys not listed here are ignored. `api_key` and `proxy_password` are read and
discarded with a warning, as described above.

A `config.csv` the loader rejects outright — the include/exclude combination
above, or a malformed CSV — does not stop an ordinary command. The failure is
recorded in the diagnostic log (visible with `--verbose`) and the command
continues with an empty configuration plus whatever the flags and environment
supply, so every other setting in the file is silently absent. `config test`
reports the failure instead.

**Note:** In the GUI, worker and tar settings are configured via the **PUR tab's Pipeline Settings** section (visible in both the scan step and the jobs-validated step). Tar options are also available in the **SingleJob tab** when using directory input mode. The `run_subpath` and `validation_pattern` are configured on the **PUR tab** scan step and persist to `config.csv` automatically.

### Environment Variables

| Variable | Effect |
|---|---|
| `RESCALE_API_KEY` | API key. Priority as listed above |
| `RESCALE_API_URL` | Overrides `api_base_url`; `--api-url` still wins. Validated against the same six platform origins |
| `RESCALE_DEBUG` | Any non-empty value enables verbose output, like `--verbose`. A few API and proxy diagnostics test this variable directly and so are not reached by `--verbose` alone |
| `RESCALE_TIMING` | `1` enables the `[TIMING]` diagnostics. The native `--timing` flag sets this variable and is equivalent; in compatibility mode the variable is the only way |
| `RESCALE_ALLOW_NON_FIPS` | `true` downgrades the FIPS startup refusal to a warning. Development only |
| `RESCALE_CONFIG_FILE` | Path to the `apiconfig` INI file used by compatibility mode |
| `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` | Read when `proxy_mode` is `system`. `HTTPS_PROXY` also fills in the proxy host and port when no `proxy_host` is configured, and switches `no-proxy` to `system` |
| `EDITOR` | Editor launched by `daemon config edit` |
| `DEBUG_HTTP`, `DEBUG_RETRY`, `DISABLE_HTTP2`, `FORCE_HTTP2` | `true` on any of these switches on a storage-client diagnostic or forces an HTTP/2 decision. Troubleshooting aids, not part of normal operation |

## Global Flags

These flags are available on all commands:

### Debug and Logging

**`--verbose, -v`** - Enable verbose/debug output
```bash
rescale-int files upload myfile.txt --verbose
rescale-int pur run --config config.csv --jobs-csv jobs.csv --state state.csv -v
```

**`--debug`** - Enable debug output (same as `--verbose`)
```bash
rescale-int files upload myfile.txt --debug
```

When debug mode is enabled:
- Shows detailed operation logs
- Displays the transfer path's `[BATCH]`, `[SLOT]` and `[CRED]` diagnostics
- Useful for diagnosing upload/download issues

At default verbosity those diagnostics are suppressed entirely. When you ask for
them they are written through the progress display rather than around it, so they
appear as whole lines above the progress bars instead of tearing them. Setting
`RESCALE_DEBUG` to any non-empty value enables verbose output the same way; a few
API and proxy diagnostics read that variable directly, so they appear with
`RESCALE_DEBUG` but not with `--verbose` alone.

**`--timing`** - Print `[TIMING]` transfer diagnostics. These are on a switch of
their own: `--verbose` and `--debug` do not turn them on. The flag is equivalent
to setting `RESCALE_TIMING=1` — it sets that variable for the process, so a
subprocess Interlink starts (the rate limit coordinator, a background daemon)
inherits it as it would an exported one. It is a persistent flag on the root
command, so it is accepted anywhere on any native command line.
```bash
rescale-int files upload large_file.dat --timing
RESCALE_TIMING=1 rescale-int files upload large_file.dat
```

The flag belongs to the native command tree. Compatibility mode does not register
it, so `rescale-int --compat --timing <command>` still fails with
`unknown flag: --timing` and exit `33` — use `RESCALE_TIMING=1` there.

### Performance Tuning

**`--max-threads N`** - Set maximum concurrent threads (0 = auto-detect, range: 1-32).
A value outside 0-32 prints a warning and falls back to auto-detect; it is not an error.
```bash
rescale-int files upload large_file.dat --max-threads 10
```

**`--no-auto-scale`** - Disable automatic thread scaling
```bash
rescale-int files upload large_file.dat --no-auto-scale --max-threads 4
```

### Configuration Overrides

**`--config, -c PATH`** - Use specific configuration file
```bash
rescale-int pur run --config myconfig.csv --jobs-csv jobs.csv --state state.csv
```

**`--api-key KEY`** - Override API key from all other sources
```bash
rescale-int files list --api-key your-api-key-here
```

**`--token-file PATH`** - Read API key from file
```bash
rescale-int files list --token-file ~/.config/rescale/token
```

**`--api-url URL`** - Override API base URL. Only the six approved Rescale
platform origins are accepted (see [Manual Configuration](#manual-configuration));
any other URL is rejected.
```bash
rescale-int files list --api-url https://platform.rescale.com
```

### Version

**`--version`** - Print the version and exit. Available on the root command only.
```bash
rescale-int --version
```

### GUI Mode

`rescale-int` is the CLI-only binary. It rejects `--gui` with

```
Error: --gui is not available in the CLI-only binary.
Use rescale-int-gui for the graphical interface.
```

and exits `1`. To get debug output in the GUI, set `RESCALE_DEBUG`
before launching it:
```bash
export RESCALE_DEBUG=1
./rescale-int-gui
```

## Exit Codes

Native CLI commands use these exit codes:

| Code | Meaning |
|-----:|---------|
| `0` | The command did what it was asked to do |
| `1` | The command failed, or completed with failures |
| `2` | The binary was not built with FIPS 140-3 support (startup refusal) |

Code `1` covers partial failures, not just outright errors. A batch command that
transfers some files and fails others exits non-zero, so scripts and CI see the
failure:

- `files upload`, `files download`, and `jobs download` fail when any file in the batch
  failed. Files you chose to skip are reported separately and are not failures.
- `folders upload-dir` fails when any file, directory walk, or folder creation failed.
- `folders download-dir` fails when any file failed.
- `pur run` and `pur resume` fail when any job in the pipeline failed. A run you
  cancelled is not a failure.
- `jobs watch -j` fails when the job it was watching ends as anything other than
  `Completed` — `Failed`, `Stopped`, `Force Stopped` and `Terminated` all exit
  non-zero, because none of them produced the result the job was asked for.
  `jobs watch --newer-than` does not classify that way: it exits `0` once every
  job it discovered is terminal, whatever those statuses are, and also when the
  reference job has no newer jobs at all. In both modes a failed download pass is
  reported as a warning and does not change the exit code.
  `jobs tail` only reports and exits `0` on every terminal status. Interrupting
  either command with Ctrl+C ends it as a cancellation and exits `1`, with no
  diagnostic report written.
- `jobs submit --end-to-end` exits `0` once the job is created and submitted. A
  job that then ends as `Failed`, or monitoring that stops early, is reported on
  stderr but does not change the exit code — the job is still on the platform.
  Check the outcome with `jobs get` or `jobs watch -j`.
- `daemon run --once` exits `0` whenever it managed to write its state file. A
  poll that listed no jobs, failed against the API, or failed a download is
  visible in the log and in `daemon status`, not in the exit code.
- Choosing **Abort** at a conflict or error prompt stops the batch and exits non-zero.
  Remaining files are not uploaded.
- A prompt that cannot run — no terminal, so the read fails immediately — is
  recorded as a failure rather than silently skipping the file. Pass the flag that
  answers the question (`--overwrite`, `--skip`, `--merge`, `--confirm`, or
  `--continue-on-error`) when running non-interactively. Duplicate handling is the
  exception: `files upload` does not fail without one of the duplicate-handling
  flags, it warns and proceeds with checking disabled (see below).

Destructive confirmations (`files delete`, `folders delete`, `jobs delete`,
`jobs stop`) fail and name `--confirm` when there is no terminal to prompt on. An
explicit "no" at an interactive prompt still cancels quietly with exit code `0`.

Compat mode uses rescale-cli's convention instead: `0` on success, `33` on error.
See [Compatibility Mode](#compatibility-mode).

## Output, Retries, and Rate Limits

### Retries are visible and bounded

Transient storage and API failures are retried automatically, by two separate
layers that report themselves differently.

**Storage transfers** print a one-line notice naming the operation, the retry
number, the cause class, and the next backoff:

```
⟳ Retrying PutObject (attempt 3/10, network): dial tcp: i/o timeout — waiting 600ms
```

The first retry is routine and is not reported; reporting starts with the second,
so the first line you see is `attempt 2/10`. The number before the slash is the
retry; the number after it is the total number of tries allowed. A storage
operation is tried at most 10 times in all — up to nine retries — so `attempt
9/10` is the last line it can print. Notices go through the progress display, so
they land above the bars rather than through them.

The waits are not uniform. The **first** network or server retry does not wait at
all; from the second, the wait is a random value between zero and
`200ms × 2^(retry − 1)`, capped at 15 seconds. So the upper bound doubles each
time — 400ms at retry 2, 3.2 seconds at retry 5, and the 15-second cap from retry
8 — while the actual wait, being drawn from that range, is usually shorter. A
**credential** retry is different again: it pauses a fixed one second and
refreshes the credentials rather than backing off.

Where they appear is not uniform. Ordinary transfer commands render them beside
the progress bars as above. `pur run`, `pur resume` and `pur submit-existing`
route the same notices through the pipeline's own logger, which is discarded at
default verbosity — add `--verbose` to see storage retries during a PUR batch.

**API calls** report every retry, including the first, in a different shape —
`Retrying POST /api/v3/jobs/ in 2s (8 attempt(s) left)` — counting down the
attempts left rather than up. A call is retried at most 10 times after the first
attempt (11 tries in all) and each backoff is capped at 30 seconds. A backoff of
5 seconds or more is announced by the longer rate-limit notice described below
instead.

Not everything is retried. A 4xx response other than 429 is returned to the
caller unchanged, and so is a 5xx on a job creation or submission `POST`: those
requests are not idempotent, so retrying one could create the job twice. The
server's own error is reported instead.

Every retried operation has its own 90-second wall-clock budget. The two layers
report running out of it differently:

- **Storage transfers.** `retries exhausted after 1m28s (limit 1m30s, 6
  attempt(s)):` then the failing operation's own error.
- **API calls.** `retry budget (1m30s) spent after 1m52s elapsed:` then the
  status and up to 200 bytes of the server's own response.

The budget is checked between attempts, so it cannot cut short an attempt already
in flight: one slow attempt can push the elapsed time it reports well past 90
seconds. This bounds a single stalled call, not a whole multi-part transfer.

A create request that reaches the platform but never answers is a case neither
layer can retry safely. It is reported as `job may have been created: "<name>"`
with the instruction to check the platform for a job of that name before creating
it again. This applies to `jobs submit` as much as to PUR; PUR additionally
records the job so a later resume will not recreate it on its own (see
[`pur run`](#pur-run)).

### Rate limit notices

Interlink rate-limits itself with a token bucket shared across processes by a
coordinator. Three separate mechanisms produce output, and they mean different
things:

- **A long retry backoff.** When a *retry* is about to sleep 5 seconds or more, the
  CLI names the operation and the wait, for example
  `Waiting 12s before retrying POST /api/v3/jobs/ (HTTP 429)`. A server-supplied
  `Retry-After` longer than the client's 30-second cap is reported as
  asked-for-versus-applied. This is the retry layer talking, not the limiter.
- **Waiting on the limiter.** When the limiter itself throttles a call, it reports
  how much of the API budget is in use and how long the call waited, for example
  `Rate limiting: 74% of API capacity, waited 1.8s`. This one is gated mainly on
  utilization rather than on the length of the wait: it starts once utilization
  reaches 60%, stops once it falls below 50%, and repeats at most once every 10
  seconds. Waits under 100ms never report at all. Short waits at low utilization
  are normal and stay silent.
- **Degraded mode.** If the coordinator is unreachable, each scope falls back to an
  emergency cap and says so once per transition, for example
  `Rate limit coordinator unavailable — user API calls capped at 0.25 req/s until it
  reconnects`. A matching notice is printed when it reconnects.

These notices deliberately bypass the standard logger so they survive at default
verbosity — a transfer that is crawling because the server is refusing calls should
say so. `daemon run` re-points them at its own logger — every invocation, not only
`--background` — so in a daemon they land wherever the rest of its log goes,
rather than on stderr: the in-memory IPC log buffer always, the console when the
daemon runs in the foreground, and a file only when `--log-file` names one.
`--log-file` is empty by default, so a daemon started without it writes no log
file at all.

## Quick Start

### 1. Configure API credentials
```bash
rescale-int config init
```

### 2. Test connection
```bash
rescale-int config test
```

### 3. Upload a file
```bash
rescale-int upload input.txt
```

### 4. List your jobs
```bash
rescale-int ls
```

### 5. Run a job pipeline
```bash
rescale-int pur run --jobs-csv jobs.csv --state state.csv
```

## Command Reference

### Config Commands

#### config init
Initialize configuration interactively

```bash
rescale-int config init [--force]
```

**Flags:**
- `-f, --force` - Overwrite existing configuration

Every answer comes from the terminal, so a run with no terminal fails rather than
hanging on an unanswerable prompt.

**`config init` always writes to the default configuration path.** It ignores the
global `-c/--config`, with or without `--force`, so `rescale-int --config
./my.csv config init` still reads and writes `~/.config/rescale/config.csv` (or
its Windows equivalent). Use `config path` to see where that is, and write a
configuration file of your own by hand when you want it somewhere else.

The API key it collects goes to the token file beside that configuration —
`~/.config/rescale/token`, or `%LOCALAPPDATA%\Rescale\Interlink\token` on
Windows — rather than into `config.csv`. The default token lookup is likewise
independent of `--config`, and it also has a legacy fallback: when the current
path does not exist it reads `~/.config/rescale-int/token`, or
`%APPDATA%\Rescale\Interlink\token` on Windows.

**Example:**
```bash
rescale-int config init
rescale-int config init -f  # Force overwrite
```

#### config show
Display current configuration

```bash
rescale-int config show
```

Shows merged configuration from file, environment, and flags.

#### config test
Test API connection

```bash
rescale-int config test
```

Verifies:
- Configuration is valid
- API credentials work
- Network connectivity
- Returns user information

#### config path
Show configuration file path

```bash
rescale-int config path
```

Displays the path to the config file and whether it exists.

### File Commands

#### files upload
Upload files to Rescale

```bash
rescale-int files upload <file> [file...] [flags]
```

**Features:**
- Automatic encryption (AES-256-CBC) before upload
- Multi-part upload for every file in the default streaming mode, regardless of
  size. (`--pre-encrypt` is the exception: an encrypted file under 100MiB goes up
  in a single request.) Part size starts from the file size — 16MiB below
  100MiB, 32MiB from 100MiB to 1GiB, 48MiB from 1GiB to 5GiB, and 64MiB above
  that — and is then adjusted in both directions: a machine short of memory gets
  smaller parts, down to a floor of 16MiB, and a file large enough that 64MiB
  parts would exceed the backend's part ceiling — 10,000 parts on S3, 50,000
  blocks on Azure — gets proportionally larger ones. The part-count floor wins
  over the memory limit, because a part size that fits memory but not the part
  count fails partway through a transfer that has already moved
- Automatic retry of individual parts on transient network errors, and resume of
  an upload that died outright — see the note on interrupted uploads below
- Progress bars with transfer speed and ETA
- Support for both S3 and Azure storage backends
- Duplicate detection with configurable handling modes
- An exclusive per-file upload lock, so two transfers cannot race on one source

**Flags:**
- `-d, --folder-id string` - Target folder ID
- `--max-concurrent int` - Maximum concurrent uploads, 1-20 (default 5). Actual concurrency adapts to file size within this cap
- `--tags string` - Comma-separated tags to apply to each uploaded file (e.g. `"simulation,cfd,v2"`)
- `--check-duplicates` - Check for existing files before uploading (prompts for each duplicate)
- `--no-check-duplicates` - Skip duplicate checking (fast mode, may create duplicates)
- `--skip-duplicates` - Check and automatically skip files that already exist
- `--allow-duplicates` - Check but upload anyway (explicitly allows duplicates)
- `--dry-run` - Preview what would be uploaded and exit without uploading anything, in every duplicate mode — see the note below
- `--pre-encrypt` - Use legacy pre-encryption mode: the whole file is encrypted to a temporary copy beside it first, and that copy is uploaded. Its own help gives the reason as compatibility with older Rescale clients

**Duplicate Detection Modes:**
- **Interactive mode (no flags)**: Prompts for duplicate handling mode at start
- **Non-interactive mode**: Defaults to no-check with warning; use explicit flags for other behavior

The four duplicate flags are mutually exclusive: passing more than one fails with
an error naming all four.

The duplicate check compares filenames against the contents of the destination
folder — the folder given by `-d/--folder-id`, or My Library's root when none is
given. It is not proof that the same content does not exist elsewhere in your
account.

**`--dry-run` never uploads.** Every duplicate mode answers the flag before any
transfer starts, so a preview moves no bytes — including the two cases that skip
the duplicate check: `--no-check-duplicates`, and a run with no terminal (CI, a
pipe, a cron job), which falls back to no-check after printing `⚠️  Warning:
Duplicate checking disabled (non-interactive mode). Use --check-duplicates to
enable.` A no-check dry run makes no API call at all; the checking modes still
list the destination folder first, because that is what decides which files the
preview would skip. The paths are validated in every mode, so a missing file
still fails the command.

The preview names the destination and each file with its size, then summarizes:

```
🔍 DRY-RUN MODE - Analyzing what would happen...

📁 Destination: root (My Library)

✓ Would upload: /data/run_1/payload.dat (3.34 MB)
✓ Would upload: /data/run_1/notes.txt (0.00 MB)

============================================================
📊 Dry-Run Summary
============================================================

📄 Files:
  Would upload:     2

💾 Total data to upload: 3.34 MB

🔧 Duplicate mode: NO-CHECK (no duplicate checking)
============================================================

✅ Dry-run complete. No files were uploaded.
   Remove --dry-run to perform the actual upload.
```

With `-d/--folder-id` the destination line reads `📁 Destination: folder ID <id>`
instead, and in a checking mode a `Would skip: N (duplicates)` line is added to
the counts. Unattended, pair `--dry-run` with `--skip-duplicates` or
`--allow-duplicates` if you want the duplicate check without a prompt.

**Examples:**
```bash
# Upload single file (automatically encrypted)
rescale-int files upload input.txt

# Upload multiple files
rescale-int files upload data1.csv data2.csv results.tar.gz

# Upload to specific folder
rescale-int files upload model.tar.gz -d abc123

# Upload with duplicate checking (skip existing files)
rescale-int files upload *.dat --skip-duplicates

# Upload with duplicate checking (prompt for each conflict)
rescale-int files upload *.dat --check-duplicates

# Upload without duplicate checking (fast mode)
rescale-int files upload *.dat --no-check-duplicates

# Preview what would be uploaded, without uploading anything
rescale-int files upload *.dat --dry-run

# The same preview with the duplicate check on, so it also reports what it
# would skip
rescale-int files upload *.dat --dry-run --check-duplicates

# Upload large file - multi-part, with part size scaled to the file
rescale-int files upload large_dataset.tar.gz
```

**Note:** Files are encrypted locally using AES-256-CBC before upload. Decryption happens automatically on download. See [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md#encryption) for encryption details.

**Note on interrupted uploads:** A multi-part upload resumes. Individual parts
are retried automatically when the network hiccups, and an upload that dies
outright — Interlink killed, machine rebooted, transfer cancelled — continues
from where it stopped when you rerun the same command, provided it got far
enough to leave a usable checkpoint and the parts it had already sent are still
on the backend. Three cases fall outside that: a transfer interrupted before its
first part completed, a small `--pre-encrypt` upload that went in a single
request, and an interruption after the backend upload finished but before the
file was registered with Rescale — each of those starts over.

Both encryption modes keep a record beside the source file, named
`<file>.upload.resume`, and the parts already accepted stay on the backend:

- **Streaming (the default).** Each completed part is recorded as it lands,
  together with the encryption chain position at that boundary. The next attempt
  continues from the last contiguous part and produces exactly the object an
  uninterrupted upload would have produced.
- **`--pre-encrypt`.** The upload resumes from the encrypted copy it already
  wrote, in whatever order the parts completed. A resume is planned against the
  part size it was interrupted with, so a retry that runs alongside other
  transfers continues with fewer workers rather than starting over.

A rerun starts a fresh upload instead, and says which of these it found, when the
saved record no longer describes the object it is about to register. Both modes
reject a record whose source file has changed size or modification time, that is
more than seven days old (the age at which Interlink stops trusting a
checkpoint), whose destination storage differs, that names a different file, or
that belongs to the other encryption mode. Each mode then adds its own:

- **Streaming** additionally needs completed parts that form an unbroken run from
  the start, and encryption material that is present and the right length. AES-CBC
  is chained from the beginning of the stream, so a gap in the run describes a
  different upload than the saved chain position does.
- **`--pre-encrypt`** has no such ordering rule — the parts of an
  already-encrypted file can be resumed in any order — but the encrypted copy
  must still exist and be exactly the size the record says.

"Names a different file" is the *normalized source path*, compared literally.
`files upload` turns each argument and each glob match into a cleaned absolute
path before the transfer, so `file`, `./file`, the equivalent absolute path, and
a relative path from another directory that resolves to the same location all
carry the same identity and all resume. What is not resolved is symlinks and
mount aliases: reaching the same bytes through a symlink, or through a second
mount of the same filesystem, produces a different pathname and starts over.

When an upload fails with a resume record on disk, the command says so. A single
file prints

```
💡 Re-running the same command can resume this upload using its completed parts if the file is unchanged, the interrupted upload is less than 7 days old, and its saved resume data is still usable.
```

and `folders upload-dir` prints the same sentence for each such file, prefixed
with `💡 <filename> left a resume record.` The line appears only when a record
actually exists.

When a record is retired this way, the stale record and any scratch ciphertext
are deleted and the orphaned backend upload is aborted, all on a best-effort
basis: a record too damaged to parse is passed over silently, a record whose
destination is on another backend is deliberately not aborted against the current
one, and an abort that fails leaves the parts to the backend's own expiry.

If the source directory cannot hold the lock and resume files — it is read-only or
full — the upload runs anyway; the notice saying so goes to the diagnostic log, so
it is visible only with `--verbose` or `--debug`. Such an attempt writes no state,
so it cannot be resumed and it holds no cross-process lock either. `--pre-encrypt`
still needs somewhere to write the encrypted copy.

**Upload locks.** Before an upload starts, Interlink takes an exclusive
`<file>.upload.lock` beside the source file, so two transfers of one file cannot
run against the same upload. A second transfer of the file inside one process is
refused outright, and so is one whose owner is another process still running on
this machine. A lock left by a crashed transfer on this same machine is cleared
automatically once its owner's process is gone. An upload that could not write
its state at all (above) takes no lock, so it is not protected against a
concurrent transfer of the same file.

A lock is never cleared automatically when Interlink cannot establish that its
owner is gone. Each refusal names the file to delete, and there are four of them:

- **Another machine, or a release older than this one.** `upload of <file> is
  locked by PID N on host H as user U since T; if that upload is not running,
  delete <file>.upload.lock to release it`. A process id recorded elsewhere means
  nothing here, so the decision is left to you. After upgrading, a lock left by an
  earlier release needs this one manual delete — and on macOS, so does a lock left
  by a crash before a reboot.
- **An owner this system will not answer about.** `... and this system will not
  say whether that process is still running (...)`. Windows reports access denied
  for a live process belonging to another login, and treating that as "no such
  process" would clear a running upload's lock.
- **A record naming no owner.** `upload of <file> is locked by a record that names
  no owner, written T; if no upload of that file is running, delete
  <file>.upload.lock to release it`.
- **A reclamation that did not finish.** `the upload lock of <file> is being
  reclaimed by another transfer; if none is running, delete
  <file>.upload.lock.stale-<hex> to release it`. That claim file is normally held
  for microseconds; one left behind by a transfer that died mid-reclamation is
  named in the message and is safe to delete.

What counts as "this machine" is decided per platform, and it is worth knowing
which case you are in before deleting a lock:

- **Linux** combines the machine identifier (`/etc/machine-id`, falling back to
  `/var/lib/dbus/machine-id`) with the exact value of the `/proc/self/ns/pid`
  link. Two containers with PID namespaces of their own are therefore separate
  domains — but two that *share* a PID namespace read the same link and are one.
  Likewise, two Linux systems restored from an image that ships a fixed,
  already-committed `/etc/machine-id` are one domain whenever their namespace
  links also read alike, and so cannot protect a shared source file against each
  other.
- **macOS** uses the current boot session (`kern.bootsessionuuid`), not a machine
  identifier. Two Macs cloned from one image are therefore separate domains — but
  a lock left by a crash before a reboot is refused afterwards and needs the one
  manual delete described above.
- **Windows** uses the installation's `MachineGuid`, since processes are numbered
  machine-wide. Two Windows systems imaged from one installation share it. This
  is also the platform where a live process belonging to another login answers
  "access denied" rather than "no such process", which produces the second
  refusal above.

#### files download
Download files from Rescale

```bash
rescale-int files download <file-id> [file-id...] [flags]
```

**Features:**
- Automatic decryption after download
- Concurrent transfer for large files, in units that depend on how the object was
  stored: a current-format object is fetched and decrypted a stored part at a
  time, at whatever part size the upload recorded in the object's metadata —
  usually one of the 16MiB–64MiB bands below, but larger for a file big enough
  that the backend's part ceiling forced bigger parts. A legacy object over 100MB
  is fetched in 32MB chunks over HTTP Range instead
- Progress bars during the download itself. In the current format decryption is
  inline, so the same bar covers it; the legacy path's separate decryption step
  has no progress indication of its own (see the note below)
- Resume capability for interrupted downloads of legacy objects, recorded in a
  sidecar beside the encrypted copy (`<file>.encrypted.download.resume`)
- Whole-file decryption — the legacy path and `--pre-encrypt` — reads in 16KB
  chunks so a large file does not have to fit in memory
- Skip and overwrite handling for files already on disk

**Flags:**
- `-o, --outdir string` - Output **directory**, created if missing (default: current directory). The remote filename is used inside it; this is not a way to rename the file — `jobs download --file-id ... --output` is the flag that takes a file path
- `-m, --max-concurrent int` - Maximum concurrent downloads, 1-20 (default 5). Actual concurrency adapts to file size within this cap
- `-w, --overwrite` - Overwrite existing files without prompting
- `-S, --skip` - Skip existing files without prompting
- `-r, --resume` - Resume interrupted downloads without prompting
- `--skip-checksum` - Do not fail on a checksum mismatch (not recommended). Verification still runs and a mismatch is reported as a warning; the file is kept instead of being moved aside. The file-size check is unaffected and still fails

**Examples:**
```bash
# Download single file (automatically decrypted)
rescale-int files download abc123 -o ./results

# Download multiple files
rescale-int files download abc123 def456 ghi789 -o ./downloads

# Download a large file
rescale-int files download large-file-id -o ./results

# Rerun an interrupted download; --resume answers the "file exists" prompt with
# "continue" instead of asking
rescale-int files download abc123 -o ./results --resume
```

**Note on Resume:** `--resume` is an answer to a prompt, not a transfer mode. The
prompt is raised by the presence of the *final destination file*, and that is the
only thing that raises it: a run interrupted before any plaintext was written
leaves only `<file>.encrypted` and its sidecar, meets no prompt at all, and
continues on its own from whatever the sidecar records. `--resume` answers
"continue" up front so a command that does meet the prompt can run unattended.

What "continue" can actually reuse depends on how the file was stored. The format
is read from the object's own metadata: `formatversion=1` marks per-part HKDF
streaming and `streamingformat=cbc` the current CBC streaming format. An object
carrying neither is the legacy whole-file format — including an object with no
such metadata at all.

- **Legacy encrypted files** are fetched into `<file>.encrypted` first, and the
  chunk driver keeps a JSON sidecar beside *that* file —
  `<file>.encrypted.download.resume` — recording which chunks finished, so a
  rerun re-requests only the missing ones over HTTP Range. This applies only to
  the concurrent chunked path — the file must be over 100MB *and* have been
  allocated more than one thread, which in practice means 500MB and up.
  Granularity is the 32MB chunk, not the exact byte: a chunk interrupted halfway
  is fetched again in full. A stored record is discarded, along with the partial
  file, when it no longer describes this download: a different chunk size or
  total size, or an object whose ETag has changed.
- **Files in the current CBC-streaming format** — anything a default streaming
  upload produced; `--pre-encrypt` writes the legacy format instead — restart
  from zero. This path keeps no resume state, and a failed attempt deletes its
  partial output rather than leaving it to be mistaken for a finished file:
  AES-CBC decryption is chained from the start of the stream, so a partial
  plaintext cannot be extended safely.

Either way decryption starts from the beginning. In the current format it happens
inline as parts arrive. The legacy path is the one with a visible pause: it writes
the whole ciphertext to `<file>.encrypted` first, then decrypts to the final file
as a separate step, with the progress bars showing no movement in between. There
is no announcement of that step — the `Decrypting ...` line the transfer layer can
emit is printed only for callers that supply an output writer, which none of the
CLI's download commands do.

Two edges are worth knowing before answering "continue" on a legacy download. The
prompt's resume branch looks for a resume record beside the *final* file, while
the legacy chunk driver keeps its record beside `<file>.encrypted`; when it finds
neither that record nor a complete-looking `<file>.encrypted`, it announces a
fresh start and deletes the ciphertext already on disk. And a destination whose
name collides with an existing *directory* is not prompted about for that reason:
the file is redirected to `<name>.file`, with `⚠️  File '<name>' conflicts with
directory, downloading as '<name>.file'`. Redirection is not an exemption from
conflict handling, though — the alternate destination is then checked like any
other, so an existing `<name>.file` prompts, or is skipped or overwritten
according to the flags.

`--overwrite`, `--skip` and `--resume` are mutually exclusive here too: passing
more than one fails with `only one of --overwrite, --skip, or --resume can be
specified`. `folders download-dir` has the same rule over its own trio,
`--overwrite`, `--skip` and `--merge`.

**A download that finishes wrong is moved aside.** A file whose SHA-512 does not
match, or whose length is not the size the API reported, is renamed to
`<file>.corrupt` and the command fails naming what became of it; deletion is the
fallback when the rename fails, and if neither works the error says the bad file
is still in place and must be deleted before retrying. This matters because the
CLI's "already downloaded" tests — `--skip` and `jobs watch` — compare sizes, so
a full-length bad file left in place would be taken for a finished download.
Three cases behave differently: an expected size of zero (the API reported none)
disables the size comparison, an object with no SHA-512 in its metadata cannot be
checksum-verified, and a download that produced **zero bytes** against a positive
expected size is not quarantined at all — it fails with `download failed: file is
empty (0 bytes) - possible write error or filesystem issue` and the empty file
stays where it is.

**Skipping an existing file** compares its size against the expected size rather
than testing for existence. A file of the wrong length is removed and downloaded
again, with `⚠️  Existing file ... is N bytes, expected M — re-downloading`; a
file of the right length is skipped without reading it, so a same-size but
corrupted file is not detected. Where the expected size is unknown, existence is
all there is to go on. The auto-download daemon is the exception: where the API
reports a SHA-512 it hashes the existing file before adopting it, and re-fetches
one that is the right length but fails its checksum. Each such verification is
remembered for the rest of that daemon's run, keyed by path, size, modification
time and expected checksum, so the file is not re-hashed on every poll.

#### files list
List files

```bash
rescale-int files list [flags]
```

**Flags:**
- `-n, --limit int` - Maximum number of files to list (default 20)
- `--include string` - Include only files matching these glob patterns (comma-separated, e.g. `"*.dat,*.log"`)
- `-x, --exclude string` - Exclude files matching these glob patterns (comma-separated, e.g. `"debug*,temp*"`)
- `-s, --search string` - Include only files whose name contains one of these terms (comma-separated, case-insensitive)
- `--filter string` - Deprecated single-pattern form of `--include`; prints a deprecation notice, takes one pattern, and is overridden by `--include` when both are given

`--limit` bounds the request, and the filters are applied to what comes back. So
a pattern that matches nothing in the first 20 files reports nothing even when
matching files exist further down the list — raise `--limit` when filtering.
A run that filtered anything out prints `Filtered: N of M files match filters`.

**Example:**
```bash
rescale-int files list --limit 50
rescale-int files list --limit 500 --include "*.csv" --exclude "*backup*"
```

#### files delete
Move files to the Trash (recoverable) or, with `--permanent`, delete them irreversibly. IDs are passed via repeated `-i/--fileid` flags (not positional arguments). By default files go to Trash, matching the web UI; recover them from the GUI Trash view.

```bash
rescale-int files delete -i <file-id> [-i <file-id>...] [-y] [--permanent]
```

**Flags:**
- `-i, --fileid stringArray` - File ID to delete; repeat the flag for multiple files (required)
- `-y, --confirm` - Skip confirmation prompt
- `--permanent` - Permanently delete instead of moving to Trash (irreversible)

Positional arguments are rejected, so every ID must carry its own `-i/--fileid`:
`files delete --fileid A B C` fails rather than deleting `A` and discarding the
rest.

**Example:**
```bash
rescale-int files delete -i abc123 -i def456            # move to Trash (recoverable)
rescale-int files delete -i abc123 --permanent          # permanent delete
rescale-int files delete -i abc123 --confirm
```

Note: moving to Trash first confirms the file exists, then looks up its parent folder
automatically. An ID that does not exist fails immediately with a 404 rather than
scanning your library for it. If a file exists but cannot be located under your library
(e.g. it lives in a job folder), use `--permanent` to delete it by ID. Either failure
stops the batch at that file — IDs listed after it are not processed.

#### files tags

Manage file-level tags. Tags are arbitrary strings attached to a file's metadata, useful for organization, filtering, and downstream automation.

```bash
rescale-int files tags list <file-id>
rescale-int files tags add <file-id> <tag> [tag...]
rescale-int files tags remove <file-id> <tag> [tag...]
rescale-int files tags set <file-id> [tag...]
```

**Subcommands:**
- `list` — List the current tags on a file
- `add` — Add one or more tags (existing tags preserved)
- `remove` — Remove specific tags
- `set` — Replace all tags with the supplied list (pass no tags to clear)

**Examples:**
```bash
rescale-int files tags list abc123
rescale-int files tags add abc123 production validated
rescale-int files tags remove abc123 draft
rescale-int files tags set abc123 final v2  # replaces all existing tags
rescale-int files tags set abc123            # clears all tags
```

### Folder Commands

#### folders create
Create a new folder. The folder name is supplied via `-n/--name` (not as a positional argument).

```bash
rescale-int folders create -n <name> [--parent-id ID]
```

**Flags:**
- `-n, --name string` - Folder name (required)
- `--parent-id string` - Parent folder ID (optional; omit for root)

**Examples:**
```bash
# Create root-level folder
rescale-int folders create --name "My Simulations"

# Create subfolder
rescale-int folders create --name "CFD Cases" --parent-id abc123
```

#### folders list
List folder contents

```bash
rescale-int folders list [--folder-id ID]
```

**Flags:**
- `--folder-id string` - Folder ID (omit for root folders)

**Examples:**
```bash
# List root folders
rescale-int folders list

# List folder contents
rescale-int folders list --folder-id abc123
```

#### folders upload-dir
Upload entire directory to a folder

```bash
rescale-int folders upload-dir <directory> [flags]
```

**Flags:**
- `--parent-id string` - Parent folder ID (default: My Library root)
- `--max-concurrent int` - Maximum concurrent file uploads, 1-20. Left unset, the cap is raised to 20 so adaptive concurrency can scale up for small files; set it explicitly to pin a fixed cap
- `--folder-concurrency int` - Maximum concurrent folder-creation API calls (default 15, range 1-30)
- `--include-hidden` - Include hidden files (starting with .)
- `--tags string` - Comma-separated tags to apply to each uploaded file (e.g. `"simulation,cfd,v2"`)
- `--sequential` - Use sequential mode (create all folders, then upload all files)
- `--continue-on-error` - Continue uploading on errors without prompting
- `-S, --skip-folder-conflicts` - Skip folders that already exist on Rescale
- `-m, --merge-folder-conflicts` - Merge into existing folders (skip existing files)
- `--check-conflicts` - Check for existing files before upload (slower but shows conflicts upfront)
- `--skip-existing` - Hidden, deprecated alias for `--merge-folder-conflicts`. Still parses, and prints no deprecation warning of its own; it participates in the exclusivity check below, whose error names all three

**Conflict Handling Modes:**
- **Skip** (`-S`): Skip subfolders that already exist on Rescale. The root folder cannot be skipped — if it already exists, the upload is cancelled with an error
- **Merge** (`-m`): Use existing folders and skip files that already exist
- **Interactive mode (no flags)**: Prompts for conflict handling mode when a folder exists
- **Non-interactive**: With no terminal to prompt on, the command fails and names `--skip-folder-conflicts` / `--merge-folder-conflicts`

Only one of `--skip-folder-conflicts`, `--merge-folder-conflicts` and
`--skip-existing` may be given; passing two fails with an error naming all three.

**Performance Note:** Files upload concurrently with connection reuse. Folder creation runs concurrently too (`--folder-concurrency`, default 15).

`--help` reports `--max-concurrent`'s default as `5`, which is the flag's declared
value; `upload-dir` and `download-dir` raise it to 20 when you do not set it, so
adaptive concurrency can scale up for a directory of small files. Setting the flag
explicitly pins the cap to what you asked for.

**Examples:**
```bash
# Upload directory to My Library root
rescale-int folders upload-dir ./simulation_data

# Upload to specific parent folder
rescale-int folders upload-dir ./project --parent-id abc123

# Upload and merge into existing folder (skip existing files)
rescale-int folders upload-dir ./project --merge-folder-conflicts

# Upload and abort if folder already exists
rescale-int folders upload-dir ./project --skip-folder-conflicts

# Upload with high concurrency
rescale-int folders upload-dir ./project --max-concurrent 10

# Include hidden files
rescale-int folders upload-dir ./project --include-hidden

# Example: Folder caching in action
# First run: 1 API call to resolve folder
# Later lookups in the same run: served from the in-memory cache, no API call
# (the cache lives for one operation and is gone when the process exits)
```

#### folders download-dir
Download entire folder recursively from Rescale

```bash
rescale-int folders download-dir <folder-id> [flags]
```

**Features:**
- Recursive folder structure download
- Concurrent file downloads for improved performance
- Conflict handling for existing local files/folders
- Dry-run mode for previewing downloads
- Checksum verification after download

**Flags:**
- `-o, --outdir string` - Output directory for downloaded files (default: current directory)
- `--max-concurrent int` - Maximum concurrent downloads, 1-20. Left unset, the cap is raised to 20 so adaptive concurrency can scale up for small files; set it explicitly to pin a fixed cap
- `-S, --skip` - Skip existing files/folders without prompting
- `-w, --overwrite` - Overwrite existing files without prompting
- `-m, --merge` - Merge into existing folders, skip existing files
- `--dry-run` - Preview what would be downloaded without actually downloading
- `--continue-on-error` - Continue downloading other files if one fails
- `--skip-checksum` - Do not fail on a checksum mismatch (not recommended); verification still runs and a mismatch becomes a warning. The file-size check is unaffected

**Conflict Handling Modes:**
- **Skip** (`-S`): Skip the entire folder if it already exists locally
- **Overwrite** (`-w`): Download into existing folders, overwrite existing files
- **Merge** (`-m`): Download into existing folders, skip existing files
- **Interactive mode (no flags)**: Prompts for conflict handling mode when folder exists
- **Non-interactive mode**: Requires explicit flag (`--skip`, `--overwrite`, or `--merge`)

**Examples:**
```bash
# Download folder to current directory
rescale-int folders download-dir abc123

# Download to specific directory
rescale-int folders download-dir abc123 -o ./downloads

# Download with merge (skip existing files)
rescale-int folders download-dir abc123 --merge -o ./data

# Download with overwrite (replace existing files)
rescale-int folders download-dir abc123 --overwrite -o ./data

# Preview what would be downloaded
rescale-int folders download-dir abc123 --dry-run --merge -o ./data

# Download with skip (abort if folder exists)
rescale-int folders download-dir abc123 --skip -o ./data

# Download with high concurrency
rescale-int folders download-dir abc123 --max-concurrent 10 --merge -o ./data

# Continue downloading even if some files fail
rescale-int folders download-dir abc123 --continue-on-error --merge
```

#### folders delete
Move a folder to the Trash (recoverable) or, with `--permanent`, delete it irreversibly. The folder ID is supplied via `--folder-id` (not a positional argument). By default the folder goes to Trash, matching the web UI.

```bash
rescale-int folders delete --folder-id <folder-id> [--confirm] [--permanent]
```

**Flags:**
- `--folder-id string` - Folder ID to delete (required)
- `--confirm` - Skip confirmation prompt
- `--permanent` - Permanently delete instead of moving to Trash (irreversible)

**Example:**
```bash
rescale-int folders delete --folder-id abc123              # move to Trash (recoverable)
rescale-int folders delete --folder-id abc123 --permanent  # permanent delete
rescale-int folders delete --folder-id abc123 --confirm
```

### Job Commands

#### jobs list
List jobs

```bash
rescale-int jobs list [flags]
```

**Flags:**
- `-n, --limit int` - Maximum number of jobs to display (`0` shows all; default `0`)

**Examples:**
```bash
# List all jobs
rescale-int jobs list

# Show only the first 50
rescale-int jobs list --limit 50
```

`--limit` truncates the display, not the request: the whole job list is fetched
either way, and a truncated listing ends with `(Showing N of M jobs. Use --limit
to change)`.

Each job prints as a block:

```
Job #1:
  ID: WfbQa
  Name: cfd-run-1
  Status: Completed
  Created: 2026-09-01T12:00:00Z
  Owner: you@example.com
  Status Reason: ...        # only when the status carries one
```

There is no status flag. Filter client-side with `grep`/`awk` against that block,
as in [Scripting Examples](#scripting-examples).

#### jobs get
Get job details

```bash
rescale-int jobs get -j <job-id>
```

**Flags:**
- `-j, --job-id string` - Job ID (required; `--id` is accepted as an alias)

**Example:**
```bash
rescale-int jobs get -j WfbQa
```

`--job-id` and `--id` write the same value, so passing both is rejected rather than
silently acting on whichever came last. This applies to `jobs get`, `jobs delete`,
and `jobs download`.

#### jobs stop
Stop a running job

```bash
rescale-int jobs stop -j <job-id>
```

**Flags:**
- `-j, --job-id string` - Job ID (required)
- `-y, --confirm` - Skip confirmation prompt

**Example:**
```bash
rescale-int jobs stop -j WfbQa
rescale-int jobs stop -j WfbQa -y  # Skip confirmation
```

#### jobs tail
Follow a job's status transitions

```bash
rescale-int jobs tail -j <job-id> [flags]
```

Polls the job's status history and prints the newest entry whenever it differs
from the one printed before. It reports the job's current status at each poll,
not a complete history: a transition that happens and is superseded between two
polls is never printed. It does not stream the job's log or console output —
use `jobs listfiles` and `jobs download` for the job's own output files.

**Flags:**
- `-j, --job-id string` - Job ID (required)
- `-i, --interval int` - Polling interval in seconds, minimum 1 (default: 10). A value below 1 is refused before the API client is built, with `--interval must be at least 1 second (got 0)`

**Note:** `jobs tail` stops on its own at any of the five terminal statuses —
`Completed`, `Failed`, `Stopped`, `Force Stopped`, `Terminated` — printing
`Job reached terminal state: <status>` and exiting `0`. A job that is already
finished when you start tailing it is reported and exits at once rather than
waiting for a transition that will never come. `jobs watch` treats the same five
as terminal but exits non-zero for any of them except `Completed`.

**Note:** Ctrl+C ends `jobs tail`. The signal cancels the shared context, and the
polling loop waits on that context alongside its ticker, so it returns at once
with the cancellation rather than continuing — the same ending as an interrupted
`jobs watch`, exiting `1` with no diagnostic report written.

Other API failures are a different matter: the loop reports each one and keeps
polling, so a run that has lost its credentials prints one failure per interval
rather than stopping.

**Examples:**
```bash
# Follow status changes with default 10-second polling
rescale-int jobs tail -j WfbQa

# Monitor job with 5-second polling interval
rescale-int jobs tail -j WfbQa -i 5

# Using long flags
rescale-int jobs tail --job-id WfbQa --interval 30
```

#### jobs listfiles
List files in a job

```bash
rescale-int jobs listfiles -j <job-id>
```

**Flags:**
- `-j, --job-id string` - Job ID (required)

**Example:**
```bash
rescale-int jobs listfiles -j WfbQa
```

#### jobs download
Download job output files

```bash
rescale-int jobs download -j <job-id> [flags]
```

**Modes:**
1. **Batch download** (no `--file-id`): Download all job output files
2. **Single file** (with `--file-id`): Download specific file

**Flags:**
- `-j, --job-id string` - Job ID (required) (alias: `--id`)
- `--file-id string` - Specific file ID to download (optional). Giving it selects single-file mode, which ignores most of the flags below
- `-o, --output string` - Output file path (single-file mode only)

Batch mode only — every one of these is ignored when `--file-id` is given:

- `-d, --outdir string` - Output directory for batch download
- `-m, --max-concurrent int` - Maximum concurrent downloads, 1-20 (default 5). Actual concurrency adapts to file size within this cap
- `-w, --overwrite` - Overwrite existing files
- `-S, --skip` - Skip existing files
- `-r, --resume` - Resume interrupted downloads
- `-s, --search string` - Include only files whose name contains one of these terms (comma-separated, case-insensitive)
- `-x, --exclude string` - Exclude files matching these glob patterns (comma-separated)
- `--filter string` - Include only files matching these glob patterns; comma-separated. Matched against the filename
- `--path-filter string` - Include only files matching these path patterns. Matched against the file's path within the job, and supports `**` for recursive matching (e.g. `"run_1/*.dat"`, `"**/results/*.csv"`)
- `--skip-checksum` - Do not fail on a checksum mismatch (not recommended); verification still runs and a mismatch becomes a warning. The file-size check is unaffected

`--overwrite`, `--skip` and `--resume` are mutually exclusive: passing more than
one fails with `only one of --overwrite, --skip, or --resume can be specified`.
That check runs in batch mode only.

**Single-file mode takes its own path, and it always overwrites.** With
`--file-id` the command bypasses the batch machinery entirely: it fetches that one
file to `--output` (or `./<remote name>` when `--output` is not given), creating
the parent directory if needed. There is no conflict handling on that path — the
destination is created or truncated, so **`--skip` does not protect a file
already sitting there**, and `--resume` has nothing to act on. Checksum
verification is always strict here: `--skip-checksum` is ignored, so a mismatch
fails the download and the file is moved aside exactly as described under
[`files download`](#files-download). Concurrency is one file, so
`--max-concurrent` has no meaning, and the filters have no list to filter.

**Batch mode never prompts.** With no conflict flag it behaves as `--skip`: a
file already on disk at the full expected size is left alone and announced with
`⊘ Skipping existing file: <name>`, and one of the wrong size is removed and
fetched again. This is deliberate — job downloads also run unattended from
`jobs watch` and the end-to-end workflow — and it differs from `files download`,
which does prompt in the same situation.

**Two files with the same name.** A job's outputs are laid out under `--outdir`
by each file's path within the job, but a path that would escape the output
directory falls back to the filename alone, which can put two files at one
destination. Any such collision is resolved by inserting the file ID before the
extension — `results.dat` becomes `results_AbCdEf.dat` — and the command reports
`⚠️  Found N files with duplicate names. File IDs will be appended to ensure
unique downloads.` Separately, a file whose server-supplied name is not a plain
filename is refused: a warning names it and it is dropped from the batch. If the
files that remain all succeed the command still exits `0`, so read the warnings
rather than the exit code to know the batch was complete.

**Examples:**
```bash
# Download all job files to current directory
rescale-int jobs download -j WfbQa

# Download all job files to specific directory
rescale-int jobs download -j WfbQa -d ./results

# Download specific file
rescale-int jobs download -j WfbQa --file-id xyz789 -o result.tar.gz
```

#### jobs watch
Watch a job and incrementally download output files

```bash
rescale-int jobs watch -j <job-id> [flags]
rescale-int jobs watch --newer-than <ref-job-id> [flags]
```

Monitor a running job's status and incrementally download output files as they become available. Exits when the job reaches a terminal state (Completed, Failed, Stopped, Force Stopped, Terminated).

**Two modes:**
- **Single-job** (`-j`): Watch one job, downloading files into the output directory. Supports file filtering.
- **Newer-than** (`--newer-than`): Watch the jobs created no earlier than a reference job. Downloads each job's files into per-job subdirectories (`OUTDIR/job_ID/`). Re-discovers newly-created jobs each polling tick, for as long as it keeps running — it exits as soon as every job it knows about is terminal, and immediately if the reference job has no newer jobs at all, so it is not a standing listener for jobs created later. The reference job itself is excluded by ID; a job created in the same second passes the filter, as does one whose creation date is missing or unparseable.

**Flags:**
- `-j, --job-id string` - Job ID to watch (mutually exclusive with `--newer-than`)
- `-n, --newer-than string` - Reference job ID — watch all jobs created after this one
- `-i, --interval int` - Polling interval in seconds (default 30, minimum 5)
- `-d, --outdir string` - Output directory (default `.`)
- `--filter string` - Include globs, comma-separated (single-job mode only)
- `-x, --exclude string` - Exclude globs, comma-separated (single-job mode only)
- `-s, --search string` - Search terms, comma-separated (single-job mode only)
- `-m, --max-concurrent int` - Maximum concurrent downloads, 1-20 (default 5)

**Examples:**
```bash
# Watch a single job and download output files
rescale-int jobs watch -j XxYyZz -d ./results

# Watch with faster polling (every 10 seconds)
rescale-int jobs watch -j XxYyZz -i 10

# Watch and download only specific file types
rescale-int jobs watch -j XxYyZz --filter "*.dat,*.log"

# Exclude large files
rescale-int jobs watch -j XxYyZz -x "*.tar.gz,*.zip"

# Watch all jobs newer than a reference job
rescale-int jobs watch --newer-than OlDjOb -d ./results
```

Downloads use skip-existing semantics — a file already in the output directory at
its expected size is not fetched again, and one of the wrong size is removed and
fetched again. Press Ctrl+C to stop watching.

In single-job mode (`-j`), `jobs watch` exits `0` only when the job ends as
`Completed`. `Failed`, `Stopped`, `Force Stopped` and `Terminated` are terminal
too, but none of them produced the result the job was asked for, so each exits
non-zero with `job reached terminal status: <status>`.

`--newer-than` does not work that way. It exits `0` once every job it discovered
has reached a terminal status, whatever those statuses are, and also when no job
newer than the reference exists. In both modes a download that fails is logged as
a warning and does not affect the exit code, so a script that needs to know
whether the outputs arrived has to check them.

#### jobs delete
Delete jobs

```bash
rescale-int jobs delete -j <job-id> [-j <job-id>...] [-y]
```

**Flags:**
- `-j, --job-id stringArray` - Job ID to delete (repeat the flag for multiple jobs) (alias: `--id`)
- `-y, --confirm` - Skip confirmation prompt

**Examples:**
```bash
# Delete single job (with confirmation)
rescale-int jobs delete --job-id WfbQa

# Delete multiple jobs (short form)
rescale-int jobs delete -j WfbQa -j XyzBb -j AbcCc

# Delete without confirmation
rescale-int jobs delete --job-id WfbQa --confirm
```

#### jobs submit
Create and/or submit jobs from JSON, SGE script, or existing job ID

```bash
rescale-int jobs submit --job-file <file> [--create]
rescale-int jobs submit --script <file> [--submit]
rescale-int jobs submit --job-id <id>
```

**Flags:**
- `-f, --job-file string` - Path to job specification JSON file
- `-s, --script string` - Path to SGE-style script with `#RESCALE_*` metadata
- `-j, --job-id string` - Existing job ID to submit (use with `--submit` only)
- `--files strings` - Input files to upload (comma-separated, supports glob patterns)
- `--create` - Create job only (don't submit)
- `--submit` - Create and submit job (default behavior)
- `-E, --end-to-end` - Full workflow: upload, create, submit, then monitor until the job finishes. Results are downloaded only if `--download` is also given
- `--download` - Auto-download results after job completes (requires `--end-to-end`)
- `--no-tar` - Registered, but has no effect on this command at this release: `--files` are uploaded individually and no archive is created either way
- `-m, --max-concurrent int` - Maximum concurrent file uploads, 1-20 (default 5)
- `--automation strings` - Automation ID(s) to attach (comma-separated or repeated)

**SGE script directives** (`--script`): `#RESCALE_NAME`, `#RESCALE_COMMAND`,
`#RESCALE_ANALYSIS`, `#RESCALE_ANALYSIS_VERSION`, `#RESCALE_CORES`,
`#RESCALE_CORES_PER_SLOT`, `#RESCALE_SLOTS`, `#RESCALE_WALLTIME`, `#RESCALE_TAGS`,
`#RESCALE_PROJECT_ID`, `#RESCALE_INBOUND_SSH_CIDR`, `#RESCALE_PUBLIC_KEY`,
`#RESCALE_USER_DEFINED_LICENSE_SETTINGS`, `#RESCALE_AUTOMATION`,
`#RESCALE_ENV_<NAME>`, and `#USE_RESCALE_LICENSE`. The qsub forms `#$ -l key=value`,
`#$ -N NAME`, and `#$ -pe smp N` are read as fallbacks — `#RESCALE_*` directives take
precedence. If `#RESCALE_COMMAND` is absent the script body is used as the command.

Six values are required, and a script that leaves one unset is rejected naming
the directive that would have supplied it: `RESCALE_NAME`, `RESCALE_COMMAND`,
`RESCALE_ANALYSIS`, `RESCALE_CORES` (the core type), `RESCALE_CORES_PER_SLOT`
(must be > 0) and `RESCALE_WALLTIME` (must be > 0). The check is on the values,
not on the literal directives, so the fallbacks can satisfy it: `#$ -N` or
`#$ -l rescale_name=` supplies the name, `#$ -l rescale_code=`,
`rescale_coretype=`, `rescale_cores=` and `rescale_walltime=` the rest, and the
script body stands in for `#RESCALE_COMMAND`.

`#RESCALE_WALLTIME` is in **hours**. A value above 336 (two weeks) is rejected
with a message saying so, because it is almost always seconds by mistake — 3600,
7200 and 86400 all land there.

**SSH access to the running job:** A job specification can request inbound SSH.
Three fields carry it, and all three reach the Rescale API:

| JSON key (`--job-file`) | SGE directive (`--script`) | Meaning |
|---|---|---|
| `cidrRule` | `#RESCALE_INBOUND_SSH_CIDR` | CIDR range allowed to connect |
| `publicKey` | `#RESCALE_PUBLIC_KEY` | Public key authorized for the job user |
| `sshPort` | (JSON only) | Inbound SSH port |

All three are omitted from the request when unset, so a specification that does not
use them adds nothing to the payload.

**Unknown keys in `--job-file`:** Top-level JSON keys that Interlink does not model
are not sent to Rescale. They are named in a warning and the submit continues, so
a typo'd or unsupported field is visible instead of vanishing silently.

**Job tags:** Tags come from the `Tags` column of a jobs CSV or the
`#RESCALE_TAGS` directive of an SGE script. In both cases tags are
**comma-separated** (e.g. `simulation, cfd, v2`); surrounding whitespace is
trimmed. A space alone is not a separator — `cfd run` is a single tag named
`cfd run`.

The two paths apply them differently, and the results are not guaranteed to
match. PUR creates the job and then posts each tag on its own to the job's tags
endpoint, logging a warning and carrying on if one fails. `jobs submit` puts the
list in the create request and makes no per-tag calls. If tags matter to a
downstream workflow, confirm them on the job's page after creating it.

**A create request whose answer never arrived** is reported as
`job may have been created: "<name>"`, with the instruction to look for a job of
that name before running the command again. There is no idempotency key on job
creation, so a blind retry can produce a duplicate.

**Examples:**
```bash
# Submit job from JSON spec
rescale-int jobs submit --job-file job_spec.json

# Create job without submitting (create-only mode)
rescale-int jobs submit --job-file job_spec.json --create

# Submit job with automations attached
rescale-int jobs submit --job-file job_spec.json --automation aB1cD2 --automation eF3gH4
```

### Daemon Commands

Background service for automatically downloading completed jobs.

The daemon reads settings from `daemon.conf` by default. CLI flags override config file values. See [daemon config](#daemon-config) commands below.

#### daemon run

Start the daemon to poll for completed jobs and download their output files.

```bash
rescale-int daemon run [flags]
```

**Config File:** `~/.config/rescale/daemon.conf` (macOS/Linux) or `%APPDATA%\Rescale\Interlink\daemon.conf` (Windows — Roaming, unlike `config.csv` and the state file, which live under Local)

The daemon automatically loads settings from the config file. CLI flags override config file values, allowing you to test different settings without modifying the config file. `daemon run` does not consult `daemon.conf`'s `enabled` setting: that gates the per-user daemons the Windows service starts, not a daemon you start yourself.

**Flags:**
- `-d, --download-dir string` - Directory to download job outputs to (default: value from `daemon.conf` `download_folder`, falling back to the platform default at `~/Downloads/rescale-jobs` on Unix or `%USERPROFILE%\Downloads\rescale-jobs` on Windows)
- `--poll-interval string` - How often to check for completed jobs, as a Go duration (default "5m"). Must be between 30 seconds and 24 hours; anything outside that fails with `poll interval must be at least 30 seconds` / `at most 24 hours`
- `--name-prefix string` - Only download jobs with names starting with this prefix
- `--name-contains string` - Only download jobs with names containing this string
- `--exclude stringArray` - Exclude jobs with names starting with these prefixes
- `--max-concurrent int` - Maximum concurrent file downloads per job (default 5)
- `--state-file string` - Path to daemon state file (default `~/.config/rescale/daemon-state.json` on macOS/Linux, `%LOCALAPPDATA%\Rescale\Interlink\state\daemon-state.json` on Windows)
- `--use-job-id` - Use job ID instead of job name for output directory names
- `--once` - Run once and exit (useful for cron jobs)
- `--log-file string` - Path to log file (empty = stdout)
- `--background` - Run in background mode (macOS/Linux only; on Windows it fails and points at the Windows service)
- `--ipc` - Enable IPC server for GUI/CLI control

`--background` re-launches the process with a rebuilt argument list that carries
the daemon's own flags — download directory, poll interval, filters, max
concurrent, state file, `--use-job-id`, log file, `--ipc` — and nothing else. Global credential
and configuration flags (`--api-key`, `--token-file`, `--config`, `--api-url`)
and `--once` are not passed on to the child, so a backgrounded daemon has to get
its credentials from the environment or the default token file. `--background`
and `--ipc` both refuse to start when a daemon is already running, naming its PID.

Those two modes are also the only ones that write the PID file
(`~/.config/rescale/daemon.pid`, or
`%LOCALAPPDATA%\Rescale\Interlink\daemon.pid` on Windows), and they remove it on
exit. A plain foreground `daemon run` writes none, which is why `daemon status`
cannot see it — see [daemon status](#daemon-status).

**Examples:**
```bash
# Start daemon using daemon.conf settings
rescale-int daemon run

# Start daemon with IPC for GUI control
rescale-int daemon run --background --ipc

# Override download-dir from config file
rescale-int daemon run --download-dir ./override

# With job name filtering (overrides config)
rescale-int daemon run --name-prefix "MyProject"
rescale-int daemon run --name-contains "simulation"
rescale-int daemon run --exclude "Debug" --exclude "Test"

# Configure poll interval (overrides config)
rescale-int daemon run --poll-interval 2m

# Run once and exit (for cron jobs)
rescale-int daemon run --once
```

#### daemon stop

Send a clean shutdown request to a running daemon over IPC.

```bash
rescale-int daemon stop
```

Requires the daemon to have been started with `--ipc`. After sending the shutdown
request it checks ten times, half a second apart, whether IPC has stopped
answering, and prints `Daemon stopped successfully.` on the first check that
finds it gone. IPC going quiet is the signal — not the process actually exiting —
so cleanup can still be in progress when the command returns. If IPC is still
answering after those checks it prints `Shutdown command sent. Daemon may still
be cleaning up.` and still exits `0`. If no daemon is running it prints
"No running daemon detected." and exits `0`. If a daemon process exists but IPC is not
responding, it says so and tells you how to terminate the process by PID.

To stop all per-user daemons in Windows service mode, use `rescale-int service stop`
from an elevated prompt instead — `daemon stop` there only pauses your own daemon.

#### daemon config

Manage daemon configuration file (`daemon.conf`).

##### daemon config show

Display current daemon configuration.

```bash
rescale-int daemon config show
```

Shows all settings from the config file with current values.

If the file does not exist yet, the defaults are shown and labelled as such. The
shipped defaults are `enabled = false`, `poll_interval_minutes = 5`,
`use_job_name_dir = true`, `max_concurrent = 5`, `lookback_days = 7`,
`auto_download_tag = autoDownload`, and all three notification settings on.

**Example output** (a configured file, not the defaults):
```
Config file: /Users/you/.config/rescale/daemon.conf

[daemon]
enabled = true
download_folder = /Users/you/Downloads/rescale-jobs
poll_interval_minutes = 5
use_job_name_dir = true
max_concurrent = 5
lookback_days = 7

[filters]
name_prefix =
name_contains =
exclude = test,debug

[eligibility]
auto_download_tag = autoDownload

# Note: Mode (Enabled/Conditional/Disabled) is set per-job via the
# 'Auto Download' custom field in Rescale workspace, not here.
# Downloaded tag (hardcoded): autoDownloaded:true

[notifications]
enabled = true
show_download_complete = true
show_download_failed = true
```

Eligibility is configured by a single key, `auto_download_tag`. `correctness_tag`
is still accepted as a deprecated alias for it; `auto_download_value` and
`downloaded_tag` are not settable and print a note explaining where their
behaviour moved to.

##### daemon config path

Show the path to the daemon configuration file.

```bash
rescale-int daemon config path
```

The path is printed resolved, not abbreviated:

```bash
rescale-int daemon config path
# Output: /Users/you/.config/rescale/daemon.conf
```

##### daemon config edit

Open the daemon configuration file in your default editor. If the file does not
exist yet, it is created with the defaults first.

```bash
rescale-int daemon config edit
```

Uses the `$EDITOR` environment variable. With `$EDITOR` unset it looks for `vim`,
then `vi`, then `nano` on macOS and Linux and fails with
`no editor found; set $EDITOR environment variable` if none of them is on `PATH`;
on Windows it uses `notepad`.

##### daemon config set

Set a configuration value. Keys are bare names (no `section.` prefix).

```bash
rescale-int daemon config set <key> <value>
```

**Available keys:**
- `enabled` - Enable/disable daemon (true/false)
- `download_folder` - Download directory path (resolved to an absolute path)
- `poll_interval_minutes` - Poll interval in minutes (1-1440)
- `use_job_name_dir` - Use job name for subdirectories (true/false)
- `max_concurrent` - Max concurrent downloads (1-10)
- `lookback_days` - How many days back to check for jobs (1-365)
- `name_prefix` - Job name prefix filter
- `name_contains` - Job name contains filter
- `exclude` - Comma-separated exclude patterns
- `auto_download_tag` - Job tag that opts a job into auto-download
- `notifications_enabled` - Enable notifications (true/false)
- `show_download_complete` - Notify on successful download (true/false)
- `show_download_failed` - Notify on failed download (true/false)

Booleans accept `true`, `1`, or `yes`; anything else reads as false. `correctness_tag`
is accepted as a deprecated alias for `auto_download_tag`. `mode`,
`auto_download_value` and `downloaded_tag` are not settable: each prints a note
saying that the mode lives on the per-job custom field instead. Any other key is
an error.

**Examples:**
```bash
# Set download folder
rescale-int daemon config set download_folder ~/Downloads/rescale-jobs

# Set poll interval to 10 minutes
rescale-int daemon config set poll_interval_minutes 10

# Set exclude patterns
rescale-int daemon config set exclude "test,debug,scratch"

# Enable the daemon
rescale-int daemon config set enabled true
```

##### daemon config init

Create a fresh `daemon.conf` with default values. Not interactive. Refuses to
overwrite an existing file — use `daemon config edit` or `daemon config set` to modify
one in place.

```bash
rescale-int daemon config init
```

Prints the created path and the defaults it wrote (download folder, poll interval, max
concurrent, and whether auto-download is enabled — it is off by default). Set
`enabled true` for the Windows service, which will not start a per-user daemon
without it, and for the GUI, which reads the key and presents it. `daemon run`
starts regardless.

##### daemon config validate

Validate that your Rescale workspace is configured for auto-download.

```bash
rescale-int daemon config validate
```

This command checks if the required "Auto Download" custom field exists in your workspace.

**Example output:**
```
Validating auto-download workspace configuration...

Custom Fields Enabled: true
'Auto Download' Field: true
  - Type: select
  - Section: Context
  - Values: [Enabled Conditional Disabled]
'Auto Download Path' Field: false (optional)

✓ Workspace is properly configured for auto-download.
```

**Setting up your workspace for auto-download:**

1. Go to Rescale Platform → Workspace Settings → Custom Fields
2. Create a new Job custom field:
   - **Name**: `Auto Download` (exact spelling required)
   - **Type**: Select (Option List) — `daemon config validate` reports an error for any other type
   - **Options**: all three of `Enabled`, `Conditional`, `Disabled`. All three are required on the field. `daemon config validate` reports an error for each one missing and exits non-zero, so a field carrying only `Enabled` and `Disabled` does not pass. Extra options are reported as warnings
3. Set the field per job: `Enabled` opts the job into auto-download, `Disabled` (or unset) skips it. A job set to `Conditional` is downloaded only if it also carries the tag named by `auto_download_tag` in `daemon.conf` (default `autoDownload`) — unless that key is configured empty, in which case `Conditional` is treated as eligible with no tag at all. Individual jobs need not use `Conditional`, but the option still has to exist on the field.

The three values are matched case-insensitively, but the words themselves are fixed and
cannot be changed in Interlink. A job whose field is unset, or set to anything the daemon
does not recognize, is skipped — including near-misses such as `Enable` and `Disable`.
Neither produces a per-job skip line, but an unrecognized value does log
`Unrecognized Auto Download value` with the value it saw, which is how a typo on
the field shows up at all; an unset field is skipped in silence.

**Why a completed job was not downloaded.** Any one of these accounts for it. The
first three are applied while the job list is being scanned; the last two are the
per-job tag and field checks that follow, so a job dropped during the scan never
reaches them:

1. It was created longer ago than `lookback_days` plus 30 days. This is a
   creation-date pre-filter meant as an optimization on the listing, but it is
   applied to every job, so a long-running job created before that cutoff is
   dropped even though it completed today. With the default `lookback_days` of 7
   the cutoff is 37 days back.
2. Its name is excluded by `name_prefix`, `name_contains` or `exclude`.
3. It completed longer ago than `lookback_days` (7 by default). This window is
   measured on completion time. A job whose completion time cannot be read is
   *kept* rather than dropped, since it already passed the creation pre-filter.
4. The job already carries the `autoDownloaded:true` tag. This is authoritative
   over the daemon's own record, so removing it on the platform lets the daemon
   consider the job again — provided the job still passes the filters above, and
   bearing in mind that files already on disk and verified are skipped rather
   than fetched again.
5. Its `Auto Download` field is `Disabled` or unset, or `Conditional` without the
   tag named by `auto_download_tag` — unless that key is configured empty, in
   which case `Conditional` is eligible with no tag at all.

One more suppression sits ahead of all of these and is not a reason to
investigate: a job whose files are already on disk but whose `autoDownloaded:true`
tag has not yet been accepted by the platform is skipped silently until that tag
call succeeds, so the retry pass cannot download it twice.

A previous download that failed is *not* on this list. The daemon keeps no
backoff and gives up on nothing: a failed job that is still eligible is attempted
again on the next poll like any other. `daemon list --failed` shows what failed
last time and `daemon retry` clears those records, but neither is a precondition
for another attempt.

An optional `Auto Download Path` custom field on the job redirects that job's
output, within limits. A relative value is resolved beneath the configured
download directory; an absolute one is taken as given. Either way the result must
stay inside the download directory once symlinks are resolved — a value that
escapes it is refused with a warning and the job falls back to the ordinary
download directory. The per-job subdirectory (job name, or job ID with
`--use-job-id`) is still appended underneath whichever base was chosen.

#### Auto-Start on Login

On **Windows with MSI installer**, the service must be started from the GUI Setup tab ("Install & Start Service") or via `rescale-int service install-and-start` from an elevated command prompt.

On **Mac and Linux**, configure auto-start using the system's init system. Interlink does not ship a built-in provisioning flow for launchd or systemd-user; the instructions below are for users who want to wire this up themselves:

<details>
<summary><b>macOS (launchd)</b></summary>

Create `~/Library/LaunchAgents/com.rescale.interlink.daemon.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.rescale.interlink.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/rescale-int</string>
        <string>daemon</string>
        <string>run</string>
        <string>--download-dir</string>
        <string>/Users/USERNAME/Downloads/rescale-jobs</string>
        <string>--ipc</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/USERNAME/Library/Logs/rescale-interlink.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/USERNAME/Library/Logs/rescale-interlink.error.log</string>
</dict>
</plist>
```

**Note:** Do NOT use `--background` with launchd. Launchd expects the process to stay in the foreground;
`--background` forks and exits, causing launchd to think the daemon crashed.

**Commands:**
```bash
# Replace USERNAME with your actual username in the plist file

# Install (enable auto-start)
launchctl load ~/Library/LaunchAgents/com.rescale.interlink.daemon.plist

# Uninstall (disable auto-start)
launchctl unload ~/Library/LaunchAgents/com.rescale.interlink.daemon.plist

# Check status
launchctl list | grep rescale
```
</details>

<details>
<summary><b>Linux (systemd)</b></summary>

Create `~/.config/systemd/user/rescale-interlink.service`:

```ini
[Unit]
Description=Rescale Interlink Auto-Download Daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/rescale-int daemon run --download-dir %h/Downloads/rescale-jobs --ipc
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
```

**Note:** Do NOT use `--background` with systemd. Systemd expects `Type=simple` services to stay in the foreground;
`--background` forks and exits, causing systemd to think the daemon crashed.

**Commands:**
```bash
# Install (enable auto-start)
systemctl --user daemon-reload
systemctl --user enable rescale-interlink
systemctl --user start rescale-interlink

# Check status
systemctl --user status rescale-interlink

# View logs
journalctl --user -u rescale-interlink -f

# Disable auto-start
systemctl --user disable rescale-interlink
```
</details>

#### daemon status

Show daemon state and statistics. There are two views, chosen by whether a running
daemon answers over IPC.

```bash
rescale-int daemon status [flags]
```

**Flags:**
- `--state-file string` - Path to daemon state file (default `~/.config/rescale/daemon-state.json` on macOS/Linux, `%LOCALAPPDATA%\Rescale\Interlink\state\daemon-state.json` on Windows)

**Live view** (a daemon is running with `--ipc`):
- Running / paused state, version, uptime
- Active downloads
- Last scan time, with how long ago it was
- **Last error**, with its age and an actionable hint — a scan that failed on an expired
  key, a dead network, or proxy trouble is named here rather than showing only a
  last-scan timestamp that stops advancing. The error is cleared by the next scan that
  completes
- Per-user detail (download folder, jobs downloaded) where the daemon reports it

**State-file view** (no daemon answering):
- Whether a daemon process was found at all, and whether it is likely missing `--ipc`
- Last poll time
- Number of downloaded jobs and failed downloads
- Recent download history, and failed downloads with their error text

"Whether a daemon process was found" is decided by the PID file, which only
`daemon run --background` and `daemon run --ipc` write. A foreground `daemon run`
writes none, so this view reports `No running daemon detected.` while that daemon
is alive and polling. On Windows the two halves are independent: the command
prints `Note: Windows Service is running but IPC not responding.` when the
service is up, and then still falls through to the PID-file line, which can read
`No running daemon detected.` in the same output. Treat the message as
conclusive only when it names a PID.

**Example:**
```bash
rescale-int daemon status
```

#### daemon list

List downloaded or failed jobs.

```bash
rescale-int daemon list [flags]
```

**Flags:**
- `--state-file string` - Path to daemon state file (default `~/.config/rescale/daemon-state.json` on macOS/Linux, `%LOCALAPPDATA%\Rescale\Interlink\state\daemon-state.json` on Windows)
- `--failed` - Show failed downloads instead of successful ones
- `--limit int` - Limit number of entries shown (0 = all)

**Examples:**
```bash
# List downloaded jobs
rescale-int daemon list

# List failed downloads
rescale-int daemon list --failed

# Limit to 10 most recent
rescale-int daemon list --limit 10
```

**This is not permanent history.** A running daemon prunes its state on every
save: a record whose download timestamp is older than `lookback_days` plus a
30-day buffer is dropped, because no scan can select that job again. Records
still waiting for their `autoDownloaded:true` tag to be applied are kept
regardless of age. With the default `lookback_days` of 7, `daemon list` therefore
reaches back 37 days, not forever.

**A state file that will not parse is set aside, not reported.** If the JSON is
malformed, it is renamed to `<state-file>.corrupt.<unix-timestamp>` on a
best-effort basis and the daemon carries on with empty state — no error, no
warning at the command line. So an unexplained empty `daemon list` is worth
checking against the state directory for a `.corrupt.*` file.

#### daemon retry

Mark failed jobs for retry on the next poll cycle.

```bash
rescale-int daemon retry [flags]
```

**Flags:**
- `--state-file string` - Path to daemon state file (default `~/.config/rescale/daemon-state.json` on macOS/Linux, `%LOCALAPPDATA%\Rescale\Interlink\state\daemon-state.json` on Windows)
- `--all` - Retry all failed jobs
- `-j, --job-id stringArray` - Job ID to retry (can be specified multiple times)

**Examples:**
```bash
# Retry all failed jobs
rescale-int daemon retry --all

# Retry specific job
rescale-int daemon retry --job-id XxYyZz
```

**Stop the daemon before running this, and check that it has actually stopped.**
`daemon retry` edits the state file on disk. A running daemon read that file once
at startup and keeps its state in memory, so it does not see the edit — and the
next time it saves, it writes its own copy back over yours.

`daemon stop` is not proof of that. It returns as soon as IPC stops answering,
which is not the same as the process exiting, and it returns straight away —
still exiting `0` — when no daemon is detected or when a daemon is running
without `--ipc`. On Windows with the service installed and running it does
something else again: it *pauses* your user's daemon inside the service and tells
you so, leaving the service itself running. Stopping that needs
`rescale-int service stop` from an elevated prompt.

`daemon status` is only a partial check, and it is worth knowing why before
trusting it. It looks for a PID file, which is written only by `daemon run
--background` and `daemon run --ipc`; a plain foreground `daemon run` writes
none. So `No running daemon detected.` means "no PID file and no answer over
IPC", which a live foreground daemon also produces. When there *is* a PID file it
is conclusive the other way: `Daemon process found (PID N) but IPC not
responding.` means the process is still there. The PID file lives at
`~/.config/rescale/daemon.pid` on macOS and Linux and
`%LOCALAPPDATA%\Rescale\Interlink\daemon.pid` on Windows.

So confirm against the process itself — the terminal you started a foreground
daemon in, your platform's process list, or Task Manager — and use `daemon
status` as the quick check only for a daemon you started with `--background` or
`--ipc`:

```bash
rescale-int daemon stop
rescale-int daemon status       # for a --background/--ipc daemon: repeat until
                                # "No running daemon detected."
                                # otherwise: confirm the process itself has exited
rescale-int daemon retry --all
rescale-int daemon run          # or daemon run --once to retry immediately
```

---

### Service Commands (Windows only)

Manage the Rescale Interlink Windows service. The service is the multi-user auto-download daemon used in MSI-installer deployments. On macOS and Linux every one of these commands fails immediately with `service installation is only supported on Windows` or `service management is only supported on Windows` and exits `1` — they are not no-ops. Auto-download on those platforms uses the subprocess daemon (`daemon run`).

All `service` commands require an elevated (Administrator) command prompt.

#### service install

Register the Interlink service with Windows Service Control Manager. After install, use `service start` to bring it up.

```bash
rescale-int service install [--config PATH]
```

**Flags:**
- `--config string` - Path to the configuration file the installed service should use (optional)

This is the one command with a local `--config`, and it shadows the global
`-c/--config` — `rescale-int service install --help` shows no `-c` under Global
Flags. The value is recorded for the service rather than used to load
configuration for this invocation.

#### service uninstall

Stop and unregister the Interlink service.

```bash
rescale-int service uninstall
```

#### service start

Start the registered service.

```bash
rescale-int service start
```

#### service stop

Stop the running service. This stops every per-user daemon under it.

```bash
rescale-int service stop
```

#### service install-and-start

Idempotent install + start in a single invocation. Used by the GUI Setup tab's "Install & Start Service" button. Safe to re-run if the service is already installed and/or running.

```bash
rescale-int service install-and-start
```

#### service status

Show whether the service is installed and currently running.

```bash
rescale-int service status
```

---

### Hardware Commands

Commands for discovering available hardware types (core types) on the Rescale platform.

#### hardware list
List available hardware types (core types). By default, only active hardware types are shown.

```bash
rescale-int hardware list [flags]
```

**Flags:**
- `-s, --search string` - Search for hardware by code or name
- `-J, --json` - Output as JSON
- `-a, --all` - Include inactive/deprecated hardware types

**Examples:**
```bash
# List active hardware types (default)
rescale-int hardware list

# Include inactive/deprecated hardware types
rescale-int hardware list -a

# Search for specific hardware
rescale-int hardware list -s emerald

# Get JSON output
rescale-int hardware list -J
```

Active hardware is shown by default; use `-a/--all` to include inactive types.

### Software Commands

Commands for discovering available software applications (analyses) on the Rescale platform.

#### software list
List available software applications (analyses)

```bash
rescale-int software list [flags]
```

**Flags:**
- `-s, --search string` - Search for software by code, name, or description
- `-J, --json` - Output as JSON
- `-V, --versions` - Show available versions for each software

**Examples:**
```bash
# List all software
rescale-int software list

# Search for specific software
rescale-int software list --search openfoam

# Get JSON output with versions
rescale-int software list --json --versions
```

### Automations Commands

Commands for discovering available automations on the Rescale platform. Automations are pre-configured scripts that run before (pre) or after (post) job execution.

#### automations list
List available automations

```bash
rescale-int automations list [flags]
```

**Flags:**
- `-J, --json` - Output as JSON

**Examples:**
```bash
# List all automations (table format)
rescale-int automations list

# Get JSON output
rescale-int automations list --json
```

#### automations get
Get details about a specific automation

```bash
rescale-int automations get --id <automation-id> [flags]
```

**Flags:**
- `--id string` - Automation ID (required)
- `-J, --json` - Output as JSON

**Examples:**
```bash
# Get automation details
rescale-int automations get --id YYnVk

# Get JSON output
rescale-int automations get --id YYnVk --json
```

### PUR (Parallel Upload and Run) Commands

PUR (Parallel Upload and Run) provides batch job submission with pipeline management.

#### The jobs CSV

`pur make-dirs-csv`, `pur scan-files` and `pur doe` write this CSV, and
`pur plan`, `pur run`, `pur resume` and `pur submit-existing --jobs-csv` read it:
one row per job, with a header row naming the columns. Column names are matched
case-insensitively. (`pur submit-existing --ids` reads no CSV, and
`pur scan-files` without both `--template` and `--output` only prints a summary.)

**Required columns** — these headers must be present, though a cell may be empty:

| Column | Meaning |
|---|---|
| `Directory` | Local directory this job's archive is made from. Empty for a job that supplies `LocalInputFiles`, `InputFiles` or `ExtraInputFileIDs` instead |
| `JobName` | Name of the job on Rescale. Keep it unique in the batch: it is the label progress lines and skip messages are reported under, and `pur scan-files` refuses to generate two rows with the same rendered name |
| `AnalysisCode` | Rescale analysis code, e.g. `openfoam` |
| `Command` | Command the job runs |
| `CoreType` | Rescale core type code, e.g. `emerald` |
| `CoresPerSlot` | Cores per slot (integer) |
| `WalltimeHours` | Walltime in hours |
| `Slots` | Number of slots (integer) |
| `LicenseSettings` | License **environment variables**, as a JSON object — each key/value pair becomes an environment variable on the job. When non-empty it must parse as a non-empty JSON object, checked when the file is loaded. This is not the user-defined license feature payload; that comes from `LicenseFeatureName`/`LicensesPerJob` below |

**Optional columns:**

| Column | Meaning |
|---|---|
| `AnalysisVersion` | Analysis version. Version names are resolved to version codes at submit time |
| `ExtraInputFileIDs` | IDs of files already on Rescale, **comma**-separated. Required by `pur submit-existing --jobs-csv`. These are always requested with decompression on the cluster |
| `InputFiles` | File IDs, `;`-separated. Read **only** for a job that builds no archive of its own — one with neither `Directory` nor `LocalInputFiles`. Where an archive is built, the job's primary input is that archive's file ID and this column is ignored |
| `LocalInputFiles` | Local paths that together form this job's archive, `;`-separated (written by `pur scan-files`) |
| `TarSubpath` | Subdirectory inside `Directory` to archive instead of the whole directory |
| `Tags` | Job tags, **comma**-separated |
| `Automations` | Automation IDs, `;`-separated |
| `ProjectID`, `OrgCode` | Project and organization the job is charged to |
| `OnDemandLicenseSeller` | On-demand license seller |
| `LicenseFeatureName`, `LicensesPerJob` | User-defined license feature and how many seats the job takes. `LicensesPerJob` is an integer and fails the load as `row N: invalid LicensesPerJob: <value>` if it is not one. The two columns go together: a name with no positive count, or a non-zero count with no name, fails the job with a message naming which half is missing. Together they produce a single `featureSets` entry, `USER_SPECIFIED_0`, in the job request |
| `CIDRRule`, `PublicKey`, `SSHPort` | Inbound SSH access. `CIDRRule` and `PublicKey` are passed through verbatim; `SSHPort` must parse as an integer, and a row that fails fails the load with `row N: invalid SSHPort: <value>` |
| `NoDecompress`, `IsLowPriority` | Booleans; `true`, `yes` or `1` mean true, anything else false |
| `Submit` | Whether the created job is also submitted. `yes`, `true`, `submit` or `create_and_submit` submit it; `no`, `false`, `create_only` or `draft` create it and stop. Empty or absent means submit. Matching ignores case and surrounding spaces. Anything else is rejected — see below |

An unrecognized `Submit` value stops the command before any work is done.
`pur run`, `pur resume` and `pur submit-existing` refuse it, naming the row and
the value:

```
Error: job 1 (run_1): Invalid submit mode: unrecognized submitMode: "maybe"
```

`pur plan` reports the same problem in its per-job form —
`- Invalid submit mode: unrecognized submitMode: "maybe"` under that job's line,
then `validation failed: one or more jobs have errors`.

For `pur run` and `pur resume` the check runs while the inputs are being loaded,
ahead of the `--dry-run` branch and ahead of the pipeline, so nothing is archived,
uploaded or created; `pur submit-existing` checks straight after its CSV load,
before its `ExtraInputFileIDs` preflight and before it builds an API client.

The three list columns — `LocalInputFiles`, `InputFiles` and `Automations` — use
`;` as their separator, because a path may legitimately contain a comma. `Tags`
and `ExtraInputFileIDs` are comma-separated. Every other column, `Directory` and
`TarSubpath` included, is a single scalar value and is not split at all, so a
path in one of those may hold either character. Ordinary CSV quoting is what lets
a cell hold its own comma, and any cell holding a comma needs it — including a
whole `;`-separated list, and a scalar column such as `Command` or
`LicenseSettings`.

Quoting does **not** buy you a literal `;` inside a member of a `;`-separated
list. Quotes protect the CSV field; the loader then splits that field on `;`
regardless, so `"a;b.inp"` comes back as two entries. There is no escape for it —
rename the file or leave it out of the job. Interlink refuses to write such a
value at all: `pur scan-files` and the other CSV writers stop before creating the
file, naming the job, the column and the value.

The two license columns are unrelated and are written differently. `LicenseSettings`
is a JSON object, so its commas and quotes need CSV quoting (doubling the inner
quotes); `LicenseFeatureName` is a bare string and `LicensesPerJob` a bare
integer:

```csv
Directory,JobName,AnalysisCode,Command,CoreType,CoresPerSlot,WalltimeHours,Slots,LicenseSettings,LicenseFeatureName,LicensesPerJob
Run_001,run_001,openfoam,./solve.sh,emerald,4,2,1,"{""ANSYSLMD_LICENSE_FILE"":""1055@lic.example.com"",""LM_PROJECT"":""alpha""}",ansys_hpc,8
```

Here the JSON becomes two environment variables on the job, while
`ansys_hpc`/`8` becomes the job's user-defined license feature.

A CSV written by Interlink carries every column above; one you write by hand needs
only the required headers. Columns added in later releases are optional, so an
older CSV still loads.

**Row order is part of the state.** Resume state is keyed by the row's 1-based
position in the CSV, not by `JobName`. Reordering rows, inserting one in the
middle, or deleting one between a run and its resume therefore points existing
state records at different jobs. Add new work at the end of the file, and keep
the rows a state file was written against exactly where they were.

#### pur make-dirs-csv
Generate jobs CSV from directory pattern

```bash
rescale-int pur make-dirs-csv --template TEMPLATE --output OUTPUT --pattern PATTERN [--overwrite]
```

**Flags:**
- `-t, --template string` - Template CSV file (required)
- `-o, --output string` - Output jobs CSV file (required unless `--command-pattern-test`)
- `-p, --pattern string` - Directory pattern, e.g., 'Run_*' (required)
- `--overwrite` - Overwrite existing output file
- `--iterate-command-patterns` - Vary command across runs by iterating numeric patterns
- `--command-pattern-test` - Preview pattern detection without generating CSV
- `--cwd string` - Working directory (default: current directory)
- `--run-subpath string` - Subdirectory path to navigate before finding runs
- `--validation-pattern string` - File pattern to validate directories
- `--start-index int` - Starting index for job numbering (default: 1)
- `--part-dirs strings` - Project directories for multi-part mode. This takes **one** argument: give the directories comma-separated, or repeat the flag. Space-separating them — which the command's own usage example does — supplies only the first; the rest are consumed as positional arguments and dropped without an error, so the run reports `partDirs=1` and generates jobs from that one directory alone

**Example:**
```bash
rescale-int pur make-dirs-csv \
  --template template.csv \
  --output jobs.csv \
  --pattern "Run_*"

# Preview how command patterns would vary:
rescale-int pur make-dirs-csv \
  --template template.csv \
  --pattern "Run_*" \
  --command-pattern-test

# Generate with pattern iteration:
rescale-int pur make-dirs-csv \
  --template template.csv \
  --output jobs.csv \
  --pattern "Run_*" \
  --iterate-command-patterns

# Multi-part mode: scan multiple project directories.
# --part-dirs takes one comma-separated value (or repeat the flag); the
# directories must not be space-separated.
rescale-int pur make-dirs-csv \
  --template template.csv \
  --output jobs.csv \
  --pattern "Run_*" \
  --part-dirs /data/DOE_1,/data/DOE_2,/data/DOE_3 \
  --validation-pattern "*.avg.fnc"
```

#### pur scan-files
Scan a directory tree for primary input files, optionally attaching secondary files to each, and emit either a printed summary or a generated jobs CSV. Useful for setting up a PUR pipeline when each job is keyed off a single solver input file with an associated mesh, config, etc.

```bash
rescale-int pur scan-files --primary <pattern> [flags]
```

**Flags:**
- `-r, --root string` - Root directory to scan (default: current directory)
- `--primary string` - Primary file pattern, e.g., `*.inp` (required)
- `--secondary stringArray` - Secondary file pattern; repeat for multiple. Each entry may end with `:required` (default) or `:optional`. Wildcard `*` is replaced with the primary file's basename. Unlike `--part-dirs`, this is a repeat-only flag: a comma inside one value is part of the pattern, not a separator
- `-t, --template string` - Template CSV used as the row prototype when generating jobs CSV
- `-o, --output string` - Output jobs CSV path. A CSV is written only when both `--template` and `--output` are given; `--output` on its own prints the summary and writes nothing
- `--overwrite` - Overwrite an existing output file
- `--json` - Emit the scan result as JSON instead of a printed summary. This returns before any CSV is generated, so `--json` together with `--template` and `--output` writes no CSV

**How `--primary` matches.** The pattern is a path relative to `--root`, matched
with ordinary shell globbing: `*` does not cross a `/`, and `**` is not special.
So `--primary "*.inp"` finds only the `.inp` files sitting directly in the root,
and `--primary "inputs/*.inp"` finds those one level down in `inputs/`. An absolute
pattern, or one that climbs out of the root with `..`, is rejected. A match that is
not a regular file — a directory named `model.inp`, say — is skipped and reported
rather than failing the job later at tar time.

**Command tokens:**
The template's command and job name are rendered per file, so each job runs
against its own input rather than every job repeating one fixed command. For a
primary file of `inputs/case1.inp`:

| Token | Value |
| --- | --- |
| `{{file}}` | `case1.inp` |
| `{{base}}` | `case1` |
| `{{ext}}` | `inp` (no leading dot) |
| `{{dir}}` | `inputs` — the containing folder, which is what tells apart a `case1/model.inp`, `case2/model.inp` layout |
| `{{index}}` | `1` (1-based) |

```
template:  abaqus job={{base}} input={{file}} cpus=8
case1.inp: abaqus job=case1 input=case1.inp cpus=8
case2.inp: abaqus job=case2 input=case2.inp cpus=8
```

No token resolves to a path on the submitting machine. Every job's archive holds
exactly its own files — the primary plus its resolved secondaries — flattened
into the working directory, so `{{file}}` is the bare name the job will find
there. A secondary pattern may therefore point outside the primary file's folder
(`--secondary "../meshes/*.cfg"`) and still be uploaded; `{{dir}}` names the
folder the primary was scanned from, which is a label for telling jobs apart
rather than a folder the job has. Data genuinely shared by every job belongs in
`--common-input-files`, which uploads once and attaches to all of them.

An unknown token is an error rather than a warning, since substitution leaves
what it cannot resolve in place: a typo like `{{bse}}` would otherwise submit
every job with a literal `{{bse}}` on its command line. A command with no tokens
is accepted with a warning — every job then runs the same command, which is
occasionally intended. A value that would restructure the command (a space, or a
shell metacharacter such as `$`) skips that file and reports why; the rest of the
batch is unaffected. The check covers only the tokens the command and job-name
templates actually use, so a space in a folder name matters when `{{dir}}` is
being substituted and not otherwise.

A job name containing tokens is rendered the same way, and an unknown token there
is an error too. Two files that render to one job name are an error as well,
naming both files and suggesting `{{index}}` or `{{dir}}`: progress and skip
messages are reported by job name, so a duplicate would make two jobs
indistinguishable in the output. A job name with no tokens keeps the `Name_1`,
`Name_2` numbering. A generation in which every matched file was skipped fails
rather than writing a header-only CSV, so an existing output file survives even
under `--overwrite`.

Rendered output is bounded, as it is for `pur doe`: a command that renders longer
than 32KiB, or a job name longer than 128 bytes, is rejected rather than
submitted.

Generated jobs carry their file list in the `LocalInputFiles` column of the jobs
CSV, semicolon-separated, so `scan-files` → `jobs.csv` → `pur run` round-trips.
The column is optional on load, so older CSVs still work.

**Examples:**
```bash
# Print a summary of matched primary/secondary files
rescale-int pur scan-files --root /data --primary "*.inp" --secondary "*.mesh"

# Optional secondary from a sibling directory
rescale-int pur scan-files --root /data --primary "inputs/*.inp" \
  --secondary "*.mesh:required" --secondary "../common.cfg:optional"

# Generate jobs.csv from a template, one rendered command per file
# (template.csv's Command column: abaqus job={{base}} input={{file}} cpus=8)
rescale-int pur scan-files --root /data --primary "*.inp" \
  --secondary "*.mesh:required" \
  --template template.csv --output jobs.csv
```

#### pur doe
Expand one base job into a design of experiments (parameter sweep)

```bash
rescale-int pur doe --template TEMPLATE --param SPEC... [--output OUTPUT | --preview]
```

The base job's command must contain a `{{name}}` token for each swept parameter.
Each case renders its own values into that command, so every case's configuration
is visible in the command line on its Rescale job page:

```
template:  starccm+ -param alpha {{alpha}} -param beta {{beta}} -load input.sim
case 1:    starccm+ -param alpha 10 -param beta 15 -load input.sim
```

Parameters and command tokens are checked against each other in both directions:
a swept parameter with no matching token, or a token with no matching parameter,
is an error rather than a silently wrong job. The two built-in tokens are the
exception in one direction: `{{__base}}` and `{{__index}}` may appear in the
command without being declared as parameters, because the generator supplies
them. An unknown `{{token}}` in the job-name or tag template is an error too, and
a token that survives substitution in any of the three is fatal rather than
shipped as literal text.

**Flags:**
- `-t, --template string` - Template jobs CSV whose first row is the base job (required)
- `-o, --output string` - Output jobs CSV file (required unless `--preview`)
- `--overwrite` - Overwrite existing output file
- `-m, --method string` - Sampling method (default `full-factorial`)
- `--param stringArray` - Swept parameter, e.g. `"alpha=10:20:5"` or `"model=a,b,c"` (repeatable)
- `--param-format stringArray` - Numeric rendering format, e.g. `"alpha=%.3f"` (repeatable; values render with `%g` when unset)
- `--samples int` - Design points for `latin-hypercube`, `sobol` and `monte-carlo`. Those three require it: the registered default of `0` fails their validation with `method "<name>" needs Samples >= 1, got 0`, so a sampled design has to name a count. The grid designs ignore it
- `--seed uint` - Seed for the randomized samplers (`latin-hypercube` and `monte-carlo`); `sobol` is deterministic and ignores it
- `--center-points int` - Center point repeats for `central-composite` and `box-behnken` (default 1)
- `--max-cases int` - Maximum cases; 0 uses the default of 1000. A negative value is an error
- `--job-name-template string` - Case name template (default `"{{__base}}_{{__index}}"`)
- `--tag-template stringArray` - Per-case Rescale job tag, e.g. `"alpha={{alpha}}"` (repeatable)
- `--base-file-ids string` - Comma-separated IDs of already-uploaded input files shared by every case
- `--cases-csv string` - CSV of explicit cases, one column per parameter (selects `--method explicit` unless `--method` is given)
- `--preview` - Show the generated cases without writing a CSV
- `--preview-limit int` - How many cases to list; 0 lists all of them (default 20)

Three of those defaults are stated above as the value you get, which is not the
value the flag is registered with. `--center-points`, `--max-cases` and
`--job-name-template` are all registered as their zero values — `0`, `0` and `""`
— and the generator substitutes `1`, `1000` and `{{__base}}_{{__index}}` when it
sees them. It matters in one direction only: passing `--center-points 0` or
`--max-cases 0` explicitly is the same as leaving them out, so neither can be used
to ask for no center points or no ceiling. `--samples` and `--seed` are registered
at `0` and mean it: zero samples fails the sampled methods, and seed `0` is a
seed like any other.

**Parameter syntax:**

| Spec | Meaning |
|------|---------|
| `alpha=10:20:5` | Numeric range, 5 levels from 10 to 20 |
| `alpha=10:20` | Numeric range, the two endpoints |
| `model=kepsilon,komega,les` | Categorical, one case per value |

Level counts apply to the designs that sample on a grid (`full-factorial` and
`ofat`); the continuous samplers draw from the range directly and ignore them.
A `--param-format` may name only a parameter that is actually being swept, and
its width and precision are each capped at 32.

**Methods:**

| Method | Design |
|--------|--------|
| `full-factorial` | Every combination of every level |
| `ofat` | One factor at a time from a baseline |
| `latin-hypercube` | `--samples` points, every parameter evenly covered |
| `sobol` | `--samples` low-discrepancy points (up to 6 parameters) |
| `monte-carlo` | `--samples` uniform random points |
| `central-composite` | Corners, axial points and center, for a quadratic fit |
| `box-behnken` | Quadratic design avoiding the corners (3+ parameters) |
| `explicit` | Cases read from `--cases-csv` |

`central-composite` and `box-behnken` fit a quadratic response surface, so they
take numeric parameters only; a categorical parameter is rejected rather than
mapped onto coded coordinates.

**Reproducibility:** `latin-hypercube` and `monte-carlo` draw from a seeded
generator, so re-running with the same `--seed` on the same build reproduces the
same sweep. `sobol` is a deterministic low-discrepancy sequence and is unaffected
by `--seed` — the same `--samples` always yields the same points. The exact
values a seed produces are a property of the build's Go runtime, not a
cross-version guarantee; pin the generated CSV if you need the sweep itself to
be an artifact.

**Name and tag templates** may use any parameter token plus `{{__base}}` (the
template's job name with any trailing `_<n>` removed, so sweeping a job called
`sim_1` yields `sim_1`, `sim_2`, …) and `{{__index}}` (the 1-based case number).
Both built-in names are reserved and cannot be used as parameter names. Rendered
tags are applied to that case's Rescale job, one API call per tag per job — a
sweep whose tagging would add more than about 500 calls says so as a warning.

**Limits.** A sweep is capped at `--max-cases` (default 1000) and, whatever that
is raised to, at an absolute ceiling of 100,000 cases; at most 128 parameters may
be swept. A rendered command is bounded at 32 KiB, a job name at 128 bytes and a
tag at 64 bytes. Each parameter value must be a single command-line argument: a
value carrying whitespace, a quote or a shell metacharacter is rejected rather
than rendered, and a categorical or explicit value additionally may not contain
glob characters, which numeric output never produces. Negative numbers are fine.
Every one of these is reported before anything is generated or written.

**Shared input files:** every case in a sweep uses the same input deck, and a
sweep never zips a working directory — generated cases carry no directory of
their own. There are two ways to supply the deck, and the deck transfers once for
the whole sweep either way:

- Pass `--base-file-ids` with the IDs of an already-uploaded deck. Each case
  references those files directly, and the generated CSV is run with
  `pur submit-existing`.
- Leave it unset and supply the deck once at run time with
  `pur run --common-input-files`, which uploads it once and attaches it to every
  job. A job that reaches `pur run` with no directory, no file IDs and no common
  input files is rejected rather than submitted with no inputs at all.

**Examples:**
```bash
# 3x3 full factorial written to a jobs CSV
rescale-int pur doe --template base.csv --output sweep.csv \
  --param "alpha=10:20:3" --param "beta=15:25:3"

# Preview a 20-point Latin hypercube without writing anything
rescale-int pur doe --template base.csv --preview \
  --method latin-hypercube --samples 20 --seed 7 \
  --param "alpha=10:20" --param "beta=15:25"

# Share one uploaded deck across the sweep and tag each job
rescale-int pur doe --template base.csv --output sweep.csv \
  --base-file-ids abcde,fghij --tag-template "alpha={{alpha}}" \
  --param "alpha=10:20:5"
rescale-int pur submit-existing --jobs-csv sweep.csv

# Cases from a hand-written CSV
rescale-int pur doe --template base.csv --output sweep.csv --cases-csv cases.csv
```

The `--cases-csv` file is read as ordinary CSV: its header names the parameters,
values may be quoted to carry embedded commas, and a row whose field count does
not match the header is an error naming the line rather than a silently dropped
case. The file is bounded at 4 MiB.

#### pur plan
Validate job pipeline without executing

```bash
rescale-int pur plan --jobs-csv FILE [--validate-coretype]
```

**Flags:**
- `-j, --jobs-csv string` - Jobs CSV file (required)
- `--validate-coretype` - Validate core type with Rescale API

`pur plan` checks each row against the jobs-CSV rules — required fields, value
ranges, the `Submit` values, the license-feature pairing — and, with
`--validate-coretype`, each row's core type against the platform. A `Directory`
that does not exist is a warning, not a failure. It does not build the job
requests, resolve `--common-input-files`, resolve a `--folder` target or look at
the contents of any run directory, so a plan that passes is not a promise that
every job will create.

`pur plan`, `pur run --dry-run` and `pur resume --dry-run` all load the
configuration before doing anything, so an API key must be resolvable even when
the command reaches no network. Without one they stop with
`failed to load config: API key is required`.

**Example:**
```bash
rescale-int pur plan --jobs-csv jobs.csv --validate-coretype
```

#### pur run
Execute complete job pipeline

```bash
rescale-int pur run --jobs-csv FILE [--state FILE] [--multipart]
```

**Pipeline stages:** each job moves through three stages, and the stages run
concurrently across the batch — a job can be uploading while another is still
being archived.

1. Create tar archives from run directories
2. Upload files to Rescale
3. Create and submit jobs on Rescale

State is not a fourth stage at the end: the run checkpoints throughout, including
immediately before each irreversible job creation, so a run given `--state` and
then interrupted leaves a state file describing exactly where it stopped.

**Name a state file unless you are certain you will not want one.** `--state` is
optional and a run without it works: the run keeps its state in memory, so every
checkpoint succeeds and the batch completes normally. What it does not do is
leave anything behind. Nothing on disk records which jobs were created, `pur
resume` has nothing to read, and a run that dies mid-flight leaves no way to tell
which jobs the platform already accepted. `pur run` says so once, on stderr,
before the pipeline starts:

```
No --state file given: this run cannot be resumed and its progress is not recorded.
```

The line is not printed for `--dry-run`, which starts no pipeline, and not
printed when `--state` is given.

**Flags:**
- `-j, --jobs-csv string` - Jobs CSV file (required)
- `-s, --state string` - State file. Optional; without it the run's state lives in memory for the life of the process (see above)
- `--multipart` - Archive a run directory by its absolute path (`tar -P`) instead of relative to its parent, so runs of the same name coming from different project trees stay distinct inside their archives. It applies to directory archives only, and only when `--flatten-tar` is off — flattening wins, and a row with `LocalInputFiles` is archived under bare filenames whatever this flag says. This is about tar layout only and has nothing to do with multi-part uploads to storage, which happen anyway
- `--common-input-files string` - Comma-separated local paths and/or `id:<fileId>` to share across all jobs. Because the separator is a comma, a path containing one cannot be expressed here
- `--decompress-common` - Decompress common input files on cluster (default: false). This governs the common files only; a job's own inputs follow its `NoDecompress` column, and `ExtraInputFileIDs` are always decompressed. A common file ID that is *already* among a job's own inputs is not attached twice, and the setting that stands is the one the earlier entry carried — so for such a job this flag has no effect
- `--folder string` - Remote folder path for this batch's uploads, created if missing (e.g. `"sweeps/alpha-beta"`)
- `--folder-parent string` - Folder ID that `--folder` is resolved beneath (default: My Library). Given on its own, it is the upload target
- `--file-tags string` - Comma-separated tags applied to every file this batch uploads
- `--include-pattern stringArray` - Only tar files matching glob (repeatable; a comma in a value is part of the pattern, not a separator)
- `--exclude-pattern stringArray` - Exclude files matching glob from tar (repeatable; a comma in a value is part of the pattern, not a separator)
- `--flatten-tar` - Remove subdirectory structure in tarball
- `--tar-compression string` - Tar compression: "none" or "gzip"
- `--tar-workers int` - Parallel tar workers (default from config). A value of zero or less is ignored and the configured count stands; see [Advanced Configuration Options](#advanced-configuration-options)
- `--upload-workers int` - Parallel upload workers (default from config), same rule
- `--job-workers int` - Parallel job creation workers (default from config), same rule
- `--rm-tar-on-success` - Delete local tar after successful upload
- `--recreate-indeterminate` - Create again every job in the batch whose creation a previous run could not confirm. Use it only after checking the platform for each of them: the flag is batch-wide, and any job that already exists is created a second time
- `--dry-run` - List the jobs that would run and stop. It loads the configuration and the jobs CSV and prints one line per job — index, name, run directory, core type, walltime and a truncated command — without creating or submitting anything. Its own help calls this "validate and show plan"; what it validates is what loading the inputs enforces anyway — the CSV's own rules, the worker counts and the `Submit` values. It does not run `pur plan`'s remaining per-row checks, so use [`pur plan`](#pur-plan) for those

**What gets archived.** A row with `LocalInputFiles` archives exactly the files
that column names, under their bare filenames, and `Directory` is not walked for
it. `TarSubpath` and the include/exclude and flatten settings narrow a directory
walk, so they have nothing to act on for such a row and are ignored. Only the
include/exclude and flatten case is announced — once per run, at `--verbose`,
`Include/exclude/flatten settings do not apply to jobs with an explicit file
list; archiving exactly the listed files`. A `TarSubpath` on such a row is dropped
silently. Two rules apply to an explicit list and to nothing else: every entry
must be a regular file (a directory, a device or a socket fails the row), and no
two entries may share a filename, since they would collide in the flat archive.

A row with `Directory` and no `LocalInputFiles` is archived by walking that
directory. Two things about that walk are easy to get wrong:

- **`--include-pattern` and `--exclude-pattern` match the file's *basename*, not
  its path inside the directory.** `--exclude-pattern "*.log"` works;
  `--exclude-pattern "logs/*"` matches nothing, because no basename contains a
  slash. `config.csv` refuses to load with both `include_pattern` and
  `exclude_pattern` set, but the flags are not re-checked against each other, so
  passing both on the command line is accepted and the includes win. A pattern
  the matcher cannot parse (an unclosed `[`, say) is not a validation error
  either — it simply never matches, which for `--include-pattern` means an
  archive with none of the files in it.
- **`--flatten-tar` drops the directory structure and therefore rejects duplicate
  basenames**, failing the row with `duplicate filename '<name>' found in '<a>'
  and '<b>'`. It also skips directory entries entirely.

The plain walk is also the one case with an external dependency: an unfiltered,
unflattened directory archive is built by running the system's `tar`, so `tar`
has to be on `PATH`. Everything else — an explicit file list, or a directory with
include/exclude patterns or `--flatten-tar` — is archived in-process and needs
nothing installed.

**Walltime is rounded up.** `WalltimeHours` is submitted as a whole number of
hours: a fractional value rounds up and anything at or below 1 becomes 1, so
`0.25` and `1.5` are submitted as 1 and 2.

A CSV that Interlink writes formats the column to **one decimal place**, and that
is a rounding of its own — enough to change what a later run submits. `1.01`
submits as 2 hours; written back out it becomes `1.0`, which submits as 1. Keep
the hand-written CSV as the source of truth when the fractions are finer than a
tenth, rather than a copy some command has rewritten.

**Common input files transfer once per invocation, not once per batch.** Local
paths given to `--common-input-files` are uploaded before any row is processed,
and their file IDs are not recorded in the state file. A resumed run therefore
uploads them again, even when every job it would attach them to is already done.
Entries given as `id:<fileId>` are referenced as they are: not uploaded, not
moved into `--folder`, and not tagged by `--file-tags`.

**Where the archives go.** A batch writes its tar archives to
`<common parent>/.rescale-int-<hash>/`, where the common parent is the directory
containing the jobs' run directories. With `--state` the hash comes from the
absolute state-file path, so a resumed batch gets the directory it had before —
and two batches given the same state-file path share one. With no `--state` the
hash comes from the process id and the current time instead, so each such run
gets a directory of its own and reuses nothing — including two stateless runs
over the same job list.
`--rm-tar-on-success` deletes only an archive that sits directly inside such a
directory, resolved through symlinks and carrying the name Interlink gave it, and
the directory itself is removed when the run leaves it empty.

**Metadata failures do not fail a job.** A file tag, a job tag or a project
assignment that the platform refuses is logged as a warning while the job goes on
being created and submitted, so check the job page if those matter. The one
failure worth acting on is a job that was submitted but whose state could not be
written: the run warns that a resume could submit it again, and that job should be
reconciled against the platform before resuming.

**Run PUR with `--verbose` when you need these warnings.** The pipeline's own
per-job messages — the metadata warnings above, the skip notice below, the
storage retry notices — all go through the standard logger, which is discarded at
default verbosity. What a default run shows is the progress display and the
end-of-run failure count, plus the final error, which names unconfirmed jobs
without reproducing the detailed text.

**Jobs a previous run could not confirm.** If a create request went out and no
answer came back, the job may be running on the platform under a name this run
cannot look it up by. Such a job is never created again on its own. `pur run` and
`pur resume` skip it, and with `--verbose` say so:

```
Skipped: a previous run could not confirm whether "job_7" was created (...).
Check the platform for a job of that name; once every unconfirmed job in this
batch has been checked, resume with --recreate-indeterminate, which creates all
of them.
```

Check the platform for each name the run lists, and only once none of them are
there, rerun with `--recreate-indeterminate`. `pur resume --dry-run` lists the
same names at default verbosity, under `Could not be confirmed as created`.

> **Deprecated:** `--extra-input-files` and `--decompress-extras` are hidden aliases for
> `--common-input-files` and `--decompress-common`. They still work but emit a warning;
> passing a flag together with its alias is an error. Use the `common` names.
> Both aliases, and the same exclusivity rule, are registered on `pur resume` as
> well as on `pur run`.

**Example:**
```bash
rescale-int pur run --jobs-csv jobs.csv --state state.csv

# With common input files shared by every job:
rescale-int pur run --jobs-csv jobs.csv --state state.csv \
  --common-input-files "/path/to/shared_script.py,id:AbCdEf123"

# Collect the batch's uploads in a folder and tag every file:
rescale-int pur run --jobs-csv jobs.csv --state state.csv \
  --folder "sweeps/alpha-beta" --file-tags "sweep-2026-q3,cfd"

# With tar filtering:
rescale-int pur run --jobs-csv jobs.csv --state state.csv \
  --exclude-pattern "*.log" --exclude-pattern "*.tmp"

# Dry-run: list the loaded jobs without executing
rescale-int pur run --jobs-csv jobs.csv --dry-run
```

#### pur resume
Resume interrupted pipeline

```bash
rescale-int pur resume --jobs-csv FILE --state FILE [--multipart]
```

**Flags:**
- `-j, --jobs-csv string` - Jobs CSV file (required)
- `-s, --state string` - State file (required)
- `--multipart` - Archive run directories by absolute path, as for [`pur run`](#pur-run)
- `--common-input-files string` - Comma-separated local paths and/or `id:<fileId>`
- `--decompress-common` - Decompress common input files on cluster
- `--folder string` - Remote folder path for this batch's uploads, created if missing
- `--folder-parent string` - Folder ID that `--folder` is resolved beneath (default: My Library)
- `--file-tags string` - Comma-separated tags applied to every file this batch uploads
- `--include-pattern stringArray` - Only tar files matching glob (repeatable; a comma in a value is part of the pattern, not a separator)
- `--exclude-pattern stringArray` - Exclude files matching glob from tar (repeatable; a comma in a value is part of the pattern, not a separator)
- `--flatten-tar` - Remove subdirectory structure in tarball
- `--tar-compression string` - Tar compression: "none" or "gzip"
- `--tar-workers int` - Parallel tar workers
- `--upload-workers int` - Parallel upload workers
- `--job-workers int` - Parallel job creation workers
- `--rm-tar-on-success` - Delete local tar after successful upload
- `--recreate-indeterminate` - Create again every job in the batch whose creation a previous run could not confirm. Use it only after checking the platform for each of them: the flag is batch-wide, and any job that already exists is created a second time
- `--dry-run` - Show what would be resumed without executing

`--dry-run` reads the state file and reports how many jobs are already complete
and how many still need tarring, uploading, creating or submitting. Jobs whose
creation could not be confirmed are listed by name under their own heading,
`Could not be confirmed as created: N`, and are not counted as work unless
`--recreate-indeterminate` is given.

The counts recognise a stage only where the state file records it as `success`.
A stage that was legitimately skipped is recorded as `skipped`, and the counts
sort such rows by the first stage they are missing rather than by how much work
is really left:

- A row with no `Directory`, whose tar and upload never ran, is counted under
  `Need tar` — even when its job was created and submitted.
- A `create_only` row that archived, uploaded and was created is counted under
  `Need submit`, because its submit is recorded as `skipped` rather than
  `success`. It will not be submitted by a resume either.

Read the counts as "not recorded as done", not as "work the resume will actually
redo".

Archives, the archive directory and the treatment of unconfirmed jobs are the same
as for [`pur run`](#pur-run).

**A resume trusts what the state file says about the archive.** A row recorded as
`tar` `success` goes straight to the upload stage; the archive is not rebuilt and
its contents are not re-checked. If that file has been deleted or moved in the
meantime — a cleaned temporary directory, a `--rm-tar-on-success` from a run whose
upload was never recorded — the resume fails that row with a `Failed to stat
file` error rather than archiving it again. Rerunning the batch as a `pur run`
against a fresh state file is what builds a new archive.

**Example:**
```bash
rescale-int pur resume --jobs-csv jobs.csv --state state.csv

# Dry-run: analyze state and show remaining work
rescale-int pur resume --jobs-csv jobs.csv --state state.csv --dry-run
```

#### pur submit-existing
Submit jobs using existing uploaded file IDs

```bash
rescale-int pur submit-existing --jobs-csv FILE [--state FILE]
rescale-int pur submit-existing --ids JOB1,JOB2,JOB3
```

Skips tar and upload phases. Use when files are already uploaded to Rescale.

**Flags:**
- `--jobs-csv string` - Jobs CSV file with extrainputfileids column (default `"jobs.csv"`)
- `--state string` - State file (default `"submit_existing_state.csv"`). Passing it explicitly empty (`--state ""`) keeps the run's state in memory, as a `pur run` without `--state` does; this command prints no notice when you do
- `--ids string` - Comma-separated IDs of jobs that already exist, to submit directly (mutually exclusive with `--jobs-csv`)
- `--recreate-indeterminate` - Create again every job in the batch whose creation a previous run could not confirm. Use it only after checking the platform for each of them: the flag is batch-wide, and any job that already exists is created a second time

Two different things share this command. With `--jobs-csv` it creates jobs from
rows whose input files are already on Rescale, then submits them; every row must
carry a non-empty `extrainputfileids` value, and one that does not fails the
command up front, naming the row. It runs the same pipeline as `pur run` with the
tar and upload stages skipped, so it honours the `Submit` column — a
`create_only` row is created and not submitted — and it honours the state file,
so a row a previous run already created or submitted is not done again. The same
two preflight checks as `pur run` apply: a worker count below 1 in the
configuration and an unrecognized `Submit` value are both refused before the API
client is built. With `--ids` it creates nothing — it submits jobs that already
exist, printing `[SUBMIT] <id> -> OK` or `-> FAILED` per ID and exiting non-zero
if any of them failed.

**Examples:**
```bash
# Create and submit from a CSV of pre-uploaded file IDs
rescale-int pur submit-existing --jobs-csv jobs_with_fileids.csv

# ... with an explicit state file
rescale-int pur submit-existing --jobs-csv jobs_with_fileids.csv --state state.csv

# Submit jobs that already exist, by ID
rescale-int pur submit-existing --ids "abc123,def456,ghi789"
```

### Shortcuts

Convenient aliases for commonly-used commands. Each carries a smaller flag set
than the command it stands for — use the full command when you need a flag the
shortcut does not have.

#### upload
Shortcut for `files upload`

```bash
rescale-int upload <file> [file...] [flags]
```

**Flags:**
- `-d, --folder-id string` - Upload to a specific folder (default: root)
- `-m, --max-concurrent int` - Maximum concurrent file uploads, 1-20 (default 5)
- `--pre-encrypt` - Use legacy pre-encryption

Duplicate handling, `--tags` and `--dry-run` are not available here; use
`files upload` for those.

**Example:**
```bash
rescale-int upload input.txt data.csv
```

#### download
Shortcut for `files download`

```bash
rescale-int download <file-id> [file-id...] [flags]
```

**Flags:**
- `-m, --max-concurrent int` - Maximum concurrent downloads, 1-20 (default 5)
- `-o, --outdir string` - Output directory (default `.`)

Conflict handling (`--overwrite`, `--skip`, `--resume`) and `--skip-checksum` are
not available here; use `files download` for those.

**Example:**
```bash
rescale-int download abc123 --outdir ./downloads
```

#### ls
Shortcut for `jobs list`. Default limit is `10`; pass `--limit 0` to list all jobs.

```bash
rescale-int ls [--limit N]
```

**Flags:**
- `-n, --limit int` - Maximum number of jobs to display (default 10)

**Example:**
```bash
rescale-int ls --limit 50
```

### Internal Commands

These exist on the command line but are not part of the documented surface. They
are listed here so that seeing one in a process list or a completion script is
not a mystery; there is no reason to run them by hand.

- **`ratelimit-coordinator run`** and **`ratelimit-coordinator status`** — the
  cross-process rate limiter described under
  [Rate limit notices](#rate-limit-notices). The group is hidden, so it does not
  appear in `--help`. Interlink starts `run` as a subprocess of its own accord
  when a transfer needs a coordinator and none is listening; `status` reports what
  that process is doing. Stopping or starting one by hand only interferes with the
  transfers already relying on it.
- **`help [command]`**, the `-h/--help` flag on every command, and the hidden
  `__complete` / `__completeNoDesc` pair are generated by the command framework.
  `__complete` is the protocol the installed completion scripts call; it is not
  meant to be typed.
- Compatibility mode carries its own generated **`completion`** group —
  `bash`, `zsh`, `fish`, `powershell` — each of which takes `--no-descriptions`
  (default false) to emit a script without completion descriptions. The native
  `completion` commands documented below take no flags of their own.

## Shell Completion

Enable shell completion for tab-completion of commands and flags.

Each `completion` subcommand writes a script to standard output and nothing else.
Installing it — into a completion directory, or into the shell's startup files —
is a separate step, and the target directory has to exist and be writable.

### Bash

**Linux** (`/etc` needs root):
```bash
rescale-int completion bash | sudo tee /etc/bash_completion.d/rescale-int > /dev/null
```

**macOS:**
```bash
rescale-int completion bash > $(brew --prefix)/etc/bash_completion.d/rescale-int
```

**Current session:**
```bash
source <(rescale-int completion bash)
```

### Zsh

**macOS** (a directory of your own, then add it to `fpath`):
```bash
mkdir -p ~/.zsh/completions
rescale-int completion zsh > ~/.zsh/completions/_rescale-int
# then in ~/.zshrc:
#   fpath=(~/.zsh/completions $fpath)
#   autoload -Uz compinit && compinit
```

**Linux:**
```bash
rescale-int completion zsh > "${fpath[1]}/_rescale-int"
```

**Current session:**
```bash
source <(rescale-int completion zsh)
```

### Fish

```bash
rescale-int completion fish > ~/.config/fish/completions/rescale-int.fish
```

### PowerShell

**Persistent** (appends to your PowerShell profile):
```powershell
rescale-int completion powershell >> $PROFILE
```

**Current session:**
```powershell
rescale-int completion powershell | Out-String | Invoke-Expression
```

Writing the script to a file of its own (`rescale-int completion powershell >
rescale-int.ps1`) does not enable anything until that file is dot-sourced or
added to the profile.

## Compatibility Mode

Rescale Interlink includes a compatibility layer that stands in for `rescale-cli`, the legacy Java-based Rescale CLI. It aims to run a script that uses the supported commands and options unchanged. What is documented here is what this layer accepts and does; how closely that matches a particular `rescale-cli` version is not something this reference can settle, so test a script against the layer before relying on it. `spub`, the deferred flags, and the behavioural differences listed under [Compatibility Reference](#compatibility-reference) are the known exceptions to check for first.

### Activation

**Flag activation:**
```bash
rescale-int --compat status -j JOB_ID
```

**Symlink activation:** Name or symlink the binary as `rescale-cli` and it activates automatically:
```bash
ln -s /usr/local/bin/rescale-int /usr/local/bin/rescale-cli
rescale-cli status -j JOB_ID
```

### Global Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--api-token` | `-p` | API token for authentication |
| `--api-base-url` | `-X` | Rescale API base URL |
| `--quiet` | `-q` | Suppress informational output |
| `--no-prompt` | | Disable interactive prompts (default behavior) |
| `--profile` | | CLI configuration profile name (apiconfig INI section) |
| `--enableErrorTracking` | | Accepted and ignored (hidden) |
| `--no-ssl-verify` | | Accepted and ignored (hidden) |

`--version/-v` prints the version and exits. Like the native `--version`, it is a
root-command flag rather than a global one, so it is used *without* a subcommand:
`rescale-cli --version`. Putting it before or after a subcommand fails with
`unknown flag: --version` and exit `33`, because the subcommand parses its own
flags and does not inherit this one.

### Credential Resolution

Credentials are resolved in this order:
1. `-p` flag (explicit)
2. `RESCALE_API_KEY` environment variable
3. apiconfig INI file (`--profile` section or `[default]`)

Base URL resolution: `-X` flag > `RESCALE_API_URL` env > profile > `https://platform.rescale.com`

The profile is read **only when steps 1 and 2 produced no key**. Supplying a key
with `-p` or `RESCALE_API_KEY` therefore skips the profile entirely, including
its base URL: without `-X` or `RESCALE_API_URL`, such a run goes to
`https://platform.rescale.com` even when the profile names another region. Pass
`-X` alongside the key, or let the profile supply both.

An explicit `--profile` fails the run with `failed to load profile "<name>"` when
the profile is actually consulted and reading it goes wrong — the section is
missing from an INI file that does exist, or the file cannot be read or parsed at
all. Two cases are not errors: a **missing** INI file yields no profile and no
error, so the run fails later with the generic `no API key provided` instead, and
a `--profile` passed alongside `-p` or `RESCALE_API_KEY` is never looked at,
whether its section exists or not.

### Exit Codes

- `0` — Success
- `33` — Error (matches rescale-cli convention)

### Commands

Every short flag below has a long form; both are accepted. The mapping is
`-j/--job-id`, `-e/--extended-output`, `-p/--api-token`, `-d/--directory-id` on
`upload` and `-d/--sync-interval` on `sync`, `-f/--files` on `upload` but
`-f/--file-name` on `download-file` and `-f/--file-matcher` on `submit`/`sync`,
`-r/--report` on `upload` but `-r/--run-id` on `download-file`/`list-files`,
`-o/--output`, `-s/--search`, `-i/--input-file`, `-n/--newer-than-job-id`,
`-E/--end-to-end`, `-c/--core-types` and `-a/--analyses`.

**`status`** — Check job status
```
rescale-cli status -j JOB_ID [-e] [--load-hours N]
```
`--load-hours` is read only in extended output: it takes effect with `-e` and a
value above zero, and is ignored otherwise.

**`stop`** — Stop a running job
```
rescale-cli stop -j JOB_ID
```

**`delete`** — Delete a job
```
rescale-cli delete -j JOB_ID
```

**`check-for-update`** — Print current version and releases URL (skips authentication)
```
rescale-cli check-for-update
```

**`list-info`** — List hardware or software as JSON
```
rescale-cli list-info -c    # core types
rescale-cli list-info -a    # analyses
```

**`upload`** — Upload files
```
rescale-cli upload -f file1.txt -f file2.txt [-d DIRECTORY_ID] [-e] [-r REPORT]
```
`-r/--report` writes an upload report to a file; `-` writes it to stdout.

**`download-file`** — Download job output files
```
rescale-cli download-file -j JOB_ID -f FILENAME [-o OUTPUT]
rescale-cli download-file --file-id FILE_ID [-o OUTPUT]
rescale-cli download-file -j JOB_ID -r RUN_ID [-f FILENAME] [-o OUTPUT]
```

**`submit`** — Parse SGE script, upload inputs, create and submit job
```
rescale-cli submit -i SCRIPT [FILE...] [-E] [-e] [-f GLOB] [-s SEARCH]
                   [--exclude TERM] [--p-cluster ID] [--waive-sla]
```
`-f`, `-s` and `--exclude` filter the download in `-E` (end-to-end) mode.

**`list-files`** — List files from a running job's cluster
```
rescale-cli list-files -j JOB_ID [-r RUN_ID]
```

**`sync`** — Download job output files with optional polling
```
rescale-cli sync -j JOB_ID [-d INTERVAL] [-o DIR] [-f GLOB] [--exclude TERM] [-s SEARCH]
rescale-cli sync -n NEWER_THAN_JOB_ID [-d INTERVAL] [-o DIR]
```

### Argument Normalization

Compat mode normalizes rescale-cli's argument conventions for Cobra compatibility:
- `-fid VALUE` → `--file-id VALUE`
- `-lh VALUE` → `--load-hours VALUE`
- Multi-value flags on `upload` and `submit`: `-f a b c` → `-f a -f b -f c`. The
  same expansion applies to `--files` and `--file-matcher`.

### Deferred Commands

Software publisher (`spub`) commands are not yet supported and return a clear error:
- `spub register`, `spub upload`, `spub validate`, `spub list`, `spub status`

### Migration from rescale-cli

Check a script against the known differences below before switching it over, and
run it once against the layer: the supported commands and options are meant to
need no changes, but that equivalence is not something this document can
demonstrate.

1. **Symlink approach** (recommended): Create a symlink so existing scripts call the compatibility layer:
   ```bash
   ln -s /usr/local/bin/rescale-int /usr/local/bin/rescale-cli
   ```

2. **Flag approach**: Add `--compat` to your Interlink invocations:
   ```bash
   rescale-int --compat status -j JOB_ID
   ```

3. **Credential setup**: Compat mode reads the same `apiconfig` INI file as
   rescale-cli, so an existing one works as it is. It looks for it at
   `~/.config/rescale/apiconfig` on macOS and Linux and at
   `%APPDATA%\Rescale\Interlink\apiconfig` (Roaming) on Windows, falling back to
   `%USERPROFILE%\AppData\Roaming\Rescale\Interlink\apiconfig` when `APPDATA` is
   unset. `RESCALE_CONFIG_FILE` overrides the path on every platform.

4. **Known differences**: `spub` commands are not yet supported. The
   `list-info -d` (desktops), `check-for-update -i` (install available),
   `upload -T` (target) and `upload --copy-to-cfs` flags return "not yet
   implemented" errors.

## Compatibility Reference

This section documents what compat mode accepts and how it behaves, so you can
tell before running a script whether it will work unchanged.

### Input Compatibility

Every flag this layer knows about is registered rather than silently rejected,
including the ones it does not act on. Most are fully implemented; the exceptions
are these.

**Accepted and ignored** — the flag parses, the value has no effect:

| Flag | Type and registered default | Where |
|---|---|---|
| `--enableErrorTracking` | bool, `false` | global |
| `--no-ssl-verify` | bool, `false` | global |
| `--no-prompt` | bool, `false` | global (compat mode is already non-interactive) |
| `-t`, `--type` | string, `"script"` | `submit` |
| `--verify` | **string**, `"true"` | `submit`, `sync` |
| `--max-concurrent` | int, `5` | `submit`, `sync` |

`--verify` takes a value even though it reads like a switch: pass
`--verify true`, not a bare `--verify`, or the next argument is consumed as its
value.

**Deferred** — the flag parses and then fails with a clear message and exit 33:

| Flag | Type and registered default | Where |
|---|---|---|
| `-T`, `--Target` | string list, empty | `upload` |
| `--copy-to-cfs` | bool, `false` | `upload` |
| `-d`, `--desktops` | bool, `false` | `list-info` |
| `-i`, `--install-available` | bool, `false` | `check-for-update` |

Every flag in both tables except `--no-prompt` is hidden from help output by
design. Hidden is not unregistered: each still parses, so a script that passes one
does not fail on an unknown flag.

**Argument normalization**: Compat mode automatically normalizes rescale-cli's non-standard argument patterns:
- Multi-char short flags: `-fid VALUE` → `--file-id VALUE`, `-lh VALUE` → `--load-hours VALUE`
- Multi-value `-f`, `--files`, and `--file-matcher` on `upload` and `submit`:
  `-f a b c` → `-f a -f b -f c`

**Credential resolution chain** (independent from native CLI):
1. `-p/--api-token` flag (highest priority)
2. `RESCALE_API_KEY` environment variable
3. `apiconfig` INI profile (`--profile` section or `[default]`)

**Base URL resolution**: `-X` flag > `RESCALE_API_URL` env > profile > `https://platform.rescale.com`

An unrecognized flag produces a standard Cobra error with exit code 33. The
`spub` tree is the exception: `spub` and its five subcommands accept unknown
flags rather than rejecting them, so such a call proceeds into credential
resolution and then into the deferral message — an unknown-flag typo there is
reported as whatever it reaches next, not as an unknown flag.

### Behavioral Compatibility

Every user-facing command but `spub` is implemented.

| Command | Status | Notes |
|---------|--------|-------|
| `status` | Implemented | Text and JSON (`-e`) modes, `--load-hours` |
| `stop` | Implemented | Prints `Job <id> is stopping.` to stdout even under `-q`, matching rescale-cli |
| `delete` | Implemented | |
| `submit` | Implemented | SGE parsing; the script is staged as `run.sh` and any additional input files are zipped into `input.zip`, and those two files are what is uploaded. `-E` end-to-end, `-e` JSON transformation |
| `upload` | Implemented | Multi-file, `-e` JSON, `-r` report |
| `download-file` | Implemented | By job+filename, by file-id, by run-id, `-e` metadata |
| `list-info` | Implemented | Core types (`-c`) and analyses (`-a`) as JSON |
| `list-files` | Implemented | Run-specific listing supported |
| `sync` | Implemented | Single-job, polling (`-d`), newer-than (`-n`), file filtering |
| `check-for-update` | Implemented | Prints the Interlink version and its releases URL |
| `spub` | Deferred | `spub` and its five subcommands (`register`, `upload`, `validate`, `list`, `status`) fail with `compat command 'spub ...' is deferred to v5.0.0`. That message is the error text this release prints, not a commitment about a future release |

Two details of `submit` are worth knowing before scripting against it.
`input.zip` is **flat**: every additional input file goes in under its basename,
with no directory structure and no duplicate check, so two inputs sharing a
basename become two entries of the same name in the archive. The job's command is
overridden to `./run.sh` whatever the script was called. And `-e` and `-E` do not
combine:
`-e` prints the transformed job JSON as soon as the job is submitted and returns
there, so a run given both gets the JSON and no monitoring and no download.

`status -e --load-hours N` fetches
`/api/v2/jobs/<id>/cluster-load-measurements/`; without `-e`, or with `N` of zero
or less, nothing is fetched. Where that endpoint is not available the
measurements come back as `[]` rather than as an error; an authentication,
network or server failure is still surfaced.

`sync` decides what to re-download from what is already on disk, rather than from
the `.rescale` metadata directory rescale-cli keeps, and writes no metadata
directory of its own.

Help text uses an argparse4j-style renderer so the structure matches rescale-cli's,
with minor formatting differences.

### Output Compatibility

Compat mode reproduces rescale-cli's output format:

- **Exit codes**: 0 on success, 33 on error (matches rescale-cli convention).
- **Timestamps**: SLF4J-style format (`2006-01-02 15:04:05,000`).
- **`submit -e` JSON**: the v3 API response is reshaped into rescale-cli's
  client-side structure rather than passed through.
- **`upload -e` and `download-file -e`**: each file is emitted as exactly nine
  fields, in rescale-cli's order — `name`, `pathParts`, `storage`,
  `encodedEncryptionKey`, `isUploaded`, `decryptedSize`, `typeId`,
  `fileChecksums`, `id`.
- **Quiet mode** (`-q`): suppresses informational output and, at the top level,
  the error message itself, so a script that uses `-q` has to work from the exit
  code. It is not silence, though: only quiet-gated output is suppressed. Data
  output (job status lines, JSON, reports) is always printed, `stop` prints its
  acknowledgement regardless, and a failing `upload -e` writes its failure JSON
  envelope to stdout before returning the error that `-q` then hides.
- **Debug suppression**: `log.Printf` output is discarded in compat mode unless
  `RESCALE_DEBUG` is set.

### Known limitations

- The four deferred flags above are accepted and then fail; there is no
  equivalent behavior.
- The `spub` tree is flat: `spub` plus five placeholder subcommands, all of which
  report the deferral rather than doing the work.
- `sync` writes no `.rescale` metadata directory (see above).
- Ctrl+C does not cancel a compat command. The signal handler prints
  `Received signal interrupt, cancelling...` but leaves the command's context
  alone, so the work in flight runs to completion.
- Proxy settings do not apply at all. Compat mode builds its own configuration
  from the API key and base URL only, and a configuration with no proxy mode
  gives a transport with proxying switched off — so its traffic goes direct
  regardless of `proxy_mode` in `config.csv` *and* regardless of `HTTP_PROXY` or
  `HTTPS_PROXY` in the environment, which that transport never consults. Where a
  proxy is required, use the native commands.

## Examples

### Basic File Operations

```bash
# Upload files
rescale-int upload model.tar.gz input.dat

# List files
rescale-int files list --limit 50

# Download file into a directory (-o is a directory, not a filename)
rescale-int download abc123 -o ./results

# Delete old files (moved to Trash; add --permanent to delete irreversibly)
rescale-int files delete -i old_file_id1 -i old_file_id2
```

### Folder Management

```bash
# Create project folder
rescale-int folders create --name "CFD Project Q1 2025"

# Upload an entire simulation directory in one command, with concurrent
# transfers and connection reuse
rescale-int folders upload-dir ./simulation_cases --parent-id abc123

# List folder contents
rescale-int folders list --folder-id abc123
```

### Job Management

```bash
# List recent jobs
rescale-int ls --limit 20

# Get job details
rescale-int jobs get -j WfbQa

# Follow status changes in real-time
rescale-int jobs tail -j WfbQa

# Download all job outputs
rescale-int jobs download -j WfbQa -d ./results

# Stop job
rescale-int jobs stop -j WfbQa

# Delete old jobs
rescale-int jobs delete -j job1 -j job2 --confirm
```

### Batch Job Pipeline (PUR)

```bash
# 1. Generate jobs CSV from Run_* directories
rescale-int pur make-dirs-csv \
  --template template.csv \
  --output jobs.csv \
  --pattern "Run_*"

# 2. Validate pipeline
rescale-int pur plan \
  --jobs-csv jobs.csv \
  --validate-coretype

# 3. Execute pipeline
rescale-int pur run --jobs-csv jobs.csv --state state.csv

# 4. If interrupted, resume from where it left off
rescale-int pur resume \
  --jobs-csv jobs.csv \
  --state state.csv
```

### Parameter Sweep (DOE)

```bash
# 1. Upload the input deck once; every case will reference these files
rescale-int upload input.sim
# note the file ID(s) reported, e.g. AbCdEf

# 2. Preview the design before committing to it. The template's command must
#    contain a {{token}} per swept parameter, e.g.
#      starccm+ -param alpha {{alpha}} -param beta {{beta}} -load input.sim
rescale-int pur doe \
  --template base.csv \
  --method latin-hypercube --samples 24 --seed 7 \
  --param "alpha=10:20" --param "beta=15:25" \
  --preview

# 3. Generate the sweep, tagging each job with its own values
rescale-int pur doe \
  --template base.csv \
  --output sweep.csv \
  --method latin-hypercube --samples 24 --seed 7 \
  --param "alpha=10:20" --param "beta=15:25" \
  --param-format "alpha=%.2f" \
  --tag-template "alpha={{alpha}}" --tag-template "beta={{beta}}" \
  --base-file-ids AbCdEf

# 4. Submit. Tar and upload are skipped because the deck is already on Rescale.
rescale-int pur submit-existing --jobs-csv sweep.csv --state sweep.state

# The alternative to step 1: generate the sweep with no --base-file-ids, so the
# cases carry no input files, and supply the shared deck once at run time.
# It needs its own CSV and its own state file — reusing sweep.csv and
# sweep.state above would find every case already created and submit nothing.
rescale-int pur doe \
  --template base.csv \
  --output sweep_nofiles.csv \
  --method latin-hypercube --samples 24 --seed 7 \
  --param "alpha=10:20" --param "beta=15:25"

rescale-int pur run --jobs-csv sweep_nofiles.csv --state sweep_nofiles.state \
  --common-input-files ./input.sim \
  --folder "sweeps/alpha-beta" --file-tags "sweep-2026-q3"
```

### Configuration Management

```bash
# Interactive setup
rescale-int config init

# Test connection
rescale-int config test

# View current configuration
rescale-int config show

# Find config file location
rescale-int config path
```

### Using Environment Variables

```bash
# Set API key via environment
export RESCALE_API_KEY="your-api-key-here"
export RESCALE_API_URL="https://platform.rescale.com"

# Now commands work without config file
rescale-int ls
rescale-int upload input.txt
```

### Scripting Examples

**Upload all CSV files in directory:**
```bash
for file in *.csv; do
  rescale-int upload "$file"
done
```

**Download all completed jobs:**
```bash
# There is no status flag, so filter client-side. Each job prints as a block with
# ID, Name, then Status, so the ID is two lines above the status line — hence
# `grep -B2`. --limit 0 lists every job; a smaller limit searches only that many.
rescale-int jobs list --limit 0 | \
  grep -B2 "Status: Completed" | \
  grep "ID:" | \
  awk '{print $2}' | \
  while read job_id; do
    rescale-int jobs download -j "$job_id" -d "./job_$job_id" --skip
  done
```

**Monitor job until completion:** `jobs tail -j <id>` already does this and stops
at every terminal status, so prefer it. Where a loop is wanted, it has to handle
all five terminal statuses and a failing command, or it spins forever:

```bash
job_id="WfbQa"
while true; do
  if ! out=$(rescale-int jobs get -j "$job_id"); then
    echo "status check failed" >&2
    exit 1
  fi
  # Status values can contain a space ("Force Stopped"), so take the whole field.
  status=$(printf '%s\n' "$out" | sed -n 's/^  Status: //p')
  echo "Job $job_id status: $status"
  case "$status" in
    Completed|Failed|Stopped|"Force Stopped"|Terminated) break ;;
  esac
  sleep 30
done
```

## Performance Tips

### Multi-Threaded Transfers

**Automatic (recommended for most users)**:
```bash
# Auto-detects system resources and optimizes transfer speed
rescale-int files upload largefile.tar.gz
rescale-int files download <file-id>
```

**Manual control for specific scenarios**:
```bash
# High-bandwidth connection (>500 Mbps): increase threads
rescale-int files upload bigfile.tar.gz --max-threads 16

# Low-memory system (< 4GB RAM): reduce threads
rescale-int files download <id> --max-threads 4

# Many small files: spread threads across files
rescale-int files upload *.log --max-concurrent 10 --max-threads 10

# Few large files: concentrate threads per file
rescale-int files upload huge1.tar.gz huge2.tar.gz --max-threads 16

# Conservative allocation (disable auto-scaling)
rescale-int files upload file.tar.gz --no-auto-scale
```

**Threads allocated per file**. A file is asked for threads by size, then capped:

| File size | Auto-scale (default) | `--no-auto-scale` |
|---|---:|---:|
| < 500MB | 1 | 1 |
| 500MB - 1GB | 4 | 2 |
| 1GB - 5GB | 12 | 3 |
| 5GB - 10GB | 21, capped to 16 | 3 |
| 10GB and up | 32, capped to 16 | 3 |

The auto-scale column already includes the aggressive multiplier, which is on by
default for every file of 100MB or more: the base allocation of 1/4/8/12/16 is
multiplied by 1.5x in the 1–5GB band, 1.75x in 5–10GB and 2x above 10GB. The
result is then capped three ways — by this file's share of the pool, by a hard
ceiling of 16 threads per file, and by the machine's CPU core count — which is
what brings the last two rows down to 16 on a machine with enough cores, and
lower on one without them. So a file under 500MB transfers on one thread whatever
you pass — *per file*. The pool still matters for a batch of them, because it
also caps how many run at once: see the note under adaptive concurrency below.

The pool share is the thread pool (`--max-threads`, or the auto-detected size when
it is unset) divided by the number of transfers running **concurrently** — which
is the adaptive worker count described below, itself never more than
`--max-concurrent` or the number of files. It is not divided by the batch length,
so a hundred small files at the default cap divide the pool by at most 20, and a
single file does not divide it at all. The share never falls below 1.

The `--no-auto-scale` column is returned as it stands: that branch is not subject
to the pool-share or CPU caps.

**Global flags for thread control**:
- `--max-threads N`: Total thread pool size (0=auto, 1-32)
- `--no-auto-scale`: Disable adaptive thread allocation

Both are read by the ordinary transfer commands. `pur run`, `pur resume` and
`pur submit-existing` build their own resource manager with auto-detected threads
and scaling on, so those two flags do not change a PUR batch; tune it with
`--tar-workers`, `--upload-workers` and `--job-workers` instead.

**Adaptive concurrency:**
`--max-concurrent` is not a global flag — it is registered per command, and it
sets an upper bound rather than a fixed number of transfers. Within that bound,
batch transfers scale their concurrency to the file size distribution:
- Many small files (<100MB): up to 20 concurrent transfers
- Medium files (100MB–1GB): up to 10 concurrent transfers
- Large files (>1GB): up to 5 concurrent transfers (more threads per file)

The default cap differs by command. `folders upload-dir` and `folders download-dir`
are the only two that raise it to 20 when you do not set `--max-concurrent`, so
adaptive scaling can reach the top of that range there. Every other command that
takes the flag defaults to a cap of 5 — `files upload`, `files download`,
`jobs submit`, `jobs download`, `jobs watch`, `daemon run`, and the `upload` and
`download` shortcuts. Raise it explicitly (up to 20) for a directory full of
small files.

The adaptive count is then validated against available system memory and thread
pool capacity, and this is the second thing `--max-threads` controls. The batch
asks for its tier's worth of concurrent transfers, each at that tier's threads per
file; if the total exceeds the pool, the concurrency is scaled down to fit. Small
files ask for one thread each, so a pool of 4 holds a batch of them to 4
simultaneous transfers however high `--max-concurrent` is set. Raising
`--max-threads` therefore does raise throughput on many small files — not by
giving any one file more threads, but by letting more of them move at once.

### General Tips

1. **Use folders upload-dir for bulk uploads**: one command transfers files concurrently over reused connections, instead of paying setup per invocation
2. **Batch operations**: Upload/download multiple files in one command
3. **PUR pipeline**: Manage dozens or hundreds of jobs from one CSV
4. **State files**: Resume interrupted operations without starting over
5. **Thread tuning**: `--max-threads` raises the pool that both a large file's
   per-file allocation and a batch's concurrency are drawn from. It gives no
   extra threads to a file under 500MB, which gets one regardless, but it does
   let more such files transfer at the same time
6. **Adaptive concurrency**: For folders with many small files, leave `--max-concurrent` unset so the cap rises to 20 and adaptive scaling can use it

## Troubleshooting

### Connection Issues

```bash
# Test your connection
rescale-int config test

# Check configuration
rescale-int config show

# Verify API key is set
echo $RESCALE_API_KEY
```

### File Upload Failures

```bash
# Check file exists and is readable
ls -lh input.txt

# Try with verbose logging
rescale-int upload input.txt --verbose

# In the default streaming mode every upload is multipart and the part size
# scales with the file; --pre-encrypt sends a small file in one request
```

If a command fails in a way Interlink can report, it writes a diagnostic report
file. Look for it under `%LOCALAPPDATA%\Rescale\Interlink\reports` on Windows,
`~/Library/Application Support/rescale/reports` on macOS and
`~/.config/rescale/reports` on Linux — where, unlike the config file, the
directory follows `XDG_CONFIG_HOME` when that is set, so it is
`$XDG_CONFIG_HOME/rescale/reports` on such a system. Note also that logs use a
different macOS location, `~/.config/rescale/logs`.

### Job Issues

```bash
# Check job status
rescale-int jobs get --job-id WfbQa

# Follow status changes (polls every 10 seconds by default)
rescale-int jobs tail --job-id WfbQa

# List job files to verify outputs
rescale-int jobs listfiles --job-id WfbQa
```

### A Command Looks Hung

Retries are bounded and reported, in two shapes. A storage transfer prints
`⟳ Retrying <operation> (attempt N/10, <cause>): <error> — waiting <delay>`,
starting from its second retry and stopping at the ninth (ten tries in all). A
PUR batch routes those same lines through its own logger, so add `--verbose` to
see them there. An API call prints
`Retrying <request> in <delay> (N attempt(s) left)`, from its first. Each
operation gives up after 90 seconds of retrying: storage transfers say `retries
exhausted after ... (limit ..., N attempt(s))`, API calls say `retry budget (1m30s)
spent after ... elapsed` and quote the server's own response. An elapsed time well past
90 seconds is not a broken budget — it is only checked between attempts, so a single
hung attempt is never cut short. If you instead see rate limit notices, the server or
the local limiter is throttling you and the wait is expected. See
[Output, Retries, and Rate Limits](#output-retries-and-rate-limits).

### Download Refused for Disk Space

```
insufficient disk space for <file>: need N MB, have M MB available
```

The legacy download path holds the encrypted and decrypted copies at once, so it
requires roughly 2x the file size plus a 15% margin. The reported `need` figure is the
requirement that was actually enforced, and the available figure is measured on the
filesystem holding the output directory — including when that directory is its own mount
point. If `need` looks larger than the file, that is the 2x-plus-margin rule, not an
error in the message.

### Non-Interactive Runs

In CI or over a pipe there is no terminal to prompt on. Commands that would ask a
question fail and name the flag that answers it rather than guessing. Supply the
relevant flag up front: `--overwrite` / `--skip` / `--resume` / `--merge` for conflicts,
`--confirm` for destructive operations, and `--continue-on-error` for error prompts.

`files upload` is the one exception. Without a duplicate-handling flag it does not
fail: it warns that duplicate checking is disabled and uploads everything. Pass
`--check-duplicates`, `--skip-duplicates`, `--allow-duplicates`, or
`--no-check-duplicates` to choose deliberately. `--dry-run` is safe either way —
it previews and uploads nothing in every mode, including the no-check fallback —
but pairing it with `--skip-duplicates` or `--allow-duplicates` is what makes the
preview report which files already exist.

## Support

For issues and feature requests:
- GitHub Issues: https://github.com/rescale-labs/Rescale_Interlink/issues
- Documentation: https://docs.rescale.com

## Version

```bash
rescale-int --version
```

See [RELEASE_NOTES.md](RELEASE_NOTES.md) for complete version history and [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md) for comprehensive feature details.
