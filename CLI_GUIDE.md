# Rescale Interlink CLI Guide

Complete command-line interface reference for `rescale-int` v4.9.9.

**Version:** 4.9.9
**Build Date:** August 25, 2026
**Status:** Production Ready, FIPS 140-3 Compliant (Mandatory)

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
- [Shell Completion](#shell-completion)
- [Compatibility Mode](#compatibility-mode)
- [Compatibility Reference](#compatibility-reference)
- [Examples](#examples)
- [Performance Tips](#performance-tips)
- [Troubleshooting](#troubleshooting)

## System Requirements

**Operating System:**
- **macOS**: 10.15 (Catalina) or later (Apple Silicon only)
- **Windows**: Windows 10 or later (64-bit)
- **Linux**: GLIBC 2.27+ required
  - RHEL/CentOS/Rocky/Alma 8+
  - Ubuntu 18.04+
  - Debian 10+
  - **NOT supported**: CentOS/RHEL 7 or older (end-of-life, GLIBC too old)

If you see an error like `GLIBC_2.27 not found`, your Linux distribution is too old and not supported.

## Installation

Download the archive for your platform from the releases page. Each release
attaches these assets, where `<tag>` is the release tag (for example `v4.9.9`):

- **macOS (Apple Silicon)**: `rescale-interlink-<tag>-macos_aarch64.zip`
- **Linux**: `rescale-interlink-<tag>-linux-amd64.tar.gz`
- **Windows**: `rescale-interlink-<tag>-win_amd64.zip` (portable) or
  `rescale-interlink-<tag>-win_amd64.msi` (installer)

Unpack the archive. The CLI binary inside is named `rescale-int` (`rescale-int.exe`
on Windows) on every platform; each archive also carries the GUI application
alongside it.

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

Configuration is saved to `~/.config/rescale/config.csv`

**Note:** If you have an existing configuration at the old location (`~/.config/rescale-int/`), it will be detected and used automatically. A migration message will suggest moving to the new location.

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

**Note:** API keys and proxy passwords are NOT stored in config files for security reasons.

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

The match is on scheme and host only: `https` is required, and a port, userinfo,
path, query, or fragment is rejected. Anything else fails with a message listing
the valid platforms. This is deliberate — it stops a mistyped or injected URL
from sending your API key somewhere that is not Rescale.

### API Key Configuration

**Option 1: Environment Variable**
```bash
export RESCALE_API_KEY="your-api-key"
```

**Option 2: Token File (recommended for scripts)**
```bash
# Create token file with restricted permissions
echo "your-api-key" > ~/.config/rescale/token
chmod 600 ~/.config/rescale/token

# Use token file
rescale-int --token-file ~/.config/rescale/token <command>
```

**Option 3: Command-Line Flag (not recommended)**
```bash
rescale-int --api-key "your-api-key" <command>
```

### Priority Order

Configuration values are merged with this priority:
1. `--api-key` command-line flag (highest)
2. `RESCALE_API_KEY` environment variable
3. `--token-file` flag
4. Default token file (`~/.config/rescale/token`)
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
- `system` - Use system proxy settings (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` environment variables)
- `basic` - HTTP Basic authentication
- `ntlm` - NTLM authentication for corporate proxies, available only in builds that support NTLM

**Notes:**
- Proxy passwords are prompted at runtime for security (not stored in config files)
- FIPS-tagged builds reject `proxy_mode=ntlm` because NTLM requires non-FIPS MD4/MD5 algorithms
- All traffic (API calls + S3/Azure storage) routes through the configured proxy
- Use `no_proxy` config key for bypass rules (comma-separated hostnames, wildcards, CIDRs). `no_proxy` is fully wired to the HTTP transport and configurable from the GUI Setup tab.

### Advanced Configuration Options

Additional configuration options for specialized use cases:

| Key | Description | Default |
|-----|-------------|---------|
| `tar_workers` | Number of concurrent tar operations | 4 |
| `upload_workers` | Number of concurrent upload workers | 4 |
| `job_workers` | Number of concurrent job submission workers | 4 |
| `exclude_pattern` | Patterns to exclude from tarballs (semicolon-separated, e.g., `*.log;*.tmp`) | (none) |
| `include_pattern` | Include-only patterns (mutually exclusive with exclude) | (none) |
| `flatten_tar` | Remove subdirectory structure in tarballs (`true`/`false`) | false |
| `run_subpath` | Scan prefix: subpath to navigate into before scanning for run directories (e.g., `Simcodes/Powerflow`) | (none) |
| `validation_pattern` | Pattern to validate runs (e.g., `*.avg.fnc`), opt-in | (none) |
| `tar_compression` | Compression type: `none` or `gzip`. Only the exact value `none` disables compression; anything else (including the legacy `gz`) produces a gzip archive. The GUI displays legacy `gz` as `gzip`, and saving from the GUI writes that normalized `gzip` back to `config.csv`; editing the file by hand leaves whatever you typed. `--tar-compression` is not validated, so a typo silently means gzip | none |
| `max_retries` | Maximum upload retry attempts | 1 |

**Note:** In the GUI, worker and tar settings are configured via the **PUR tab's Pipeline Settings** section (visible in both the scan step and the jobs-validated step). Tar options are also available in the **SingleJob tab** when using directory input mode. The `run_subpath` and `validation_pattern` are configured on the **PUR tab** scan step and persist to `config.csv` automatically. These settings are no longer in the Setup tab's Advanced Settings.

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
- Displays the transfer path's `[BATCH]`, `[SLOT]`, `[CRED]` and `[TIMING]` diagnostics
- Useful for diagnosing upload/download issues

At default verbosity those diagnostics are suppressed entirely. When you ask for
them they are written through the progress display rather than around it, so they
appear as whole lines above the progress bars instead of tearing them. Setting
`RESCALE_DEBUG` to any non-empty value has the same effect as `--verbose`.

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

### GUI Mode

`rescale-int` is the CLI-only binary; it rejects `--gui` and tells you to run
`rescale-int-gui` instead. To get debug output in the GUI, set `RESCALE_DEBUG`
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

Transient storage and API failures are retried automatically. From the second
attempt onward each retry prints a one-line notice naming the operation, the
attempt number, the cause class, and the next backoff:

```
⟳ Retrying PutObject (attempt 3/10, network): dial tcp: i/o timeout — waiting 4s
```

A single retry is routine and is not reported. Notices go through the progress
display, so they land above the bars rather than through them.

Every retried operation has its own 90-second wall-clock budget. The two layers
report running out of it differently:

- **Storage transfers.** `retries exhausted after 1m28s (limit 1m30s, 6
  attempt(s)):` then the failing operation's own error.
- **API calls.** `retry budget (1m30s) spent after 1m52s elapsed:` then the
  status and up to 200 bytes of the server's own response.

The budget is checked between attempts, so it cannot cut short an attempt already
in flight: one slow attempt can push the elapsed time it reports well past 90
seconds. This bounds a single stalled call, not a whole multi-part transfer.

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
say so. A detached daemon (`daemon run --background`) writes them to its log file and
IPC log buffer instead of stderr.

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
- Multi-part upload for every file, regardless of size. Part size scales with the
  file: 16MB below 100MB, 32MB from 100MB to 1GB, 48MB from 1GB to 5GB, and 64MB
  above that. Files large enough that 64MB parts would exceed the backend's part
  ceiling — 10,000 parts on S3, 50,000 blocks on Azure — get proportionally
  larger parts
- Automatic retry of individual parts on transient network errors. An upload
  killed outright restarts from the beginning — see the note on interrupted
  uploads below
- Progress bars with transfer speed and ETA
- Support for both S3 and Azure storage backends
- Duplicate detection with configurable handling modes

**Flags:**
- `-d, --folder-id string` - Target folder ID
- `--max-concurrent int` - Maximum concurrent uploads, 1-20 (default 5). Actual concurrency adapts to file size within this cap
- `--tags string` - Comma-separated tags to apply to each uploaded file (e.g. `"simulation,cfd,v2"`)
- `--check-duplicates` - Check for existing files before uploading (prompts for each duplicate)
- `--no-check-duplicates` - Skip duplicate checking (fast mode, may create duplicates)
- `--skip-duplicates` - Check and automatically skip files that already exist
- `--allow-duplicates` - Check but upload anyway (explicitly allows duplicates)
- `--dry-run` - Preview what would be uploaded without actually uploading
- `--pre-encrypt` - Use legacy pre-encryption mode (pre-encrypts entire file to temp file before upload, for compatibility with older Rescale clients)

**Duplicate Detection Modes:**
- **Interactive mode (no flags)**: Prompts for duplicate handling mode at start
- **Non-interactive mode**: Defaults to no-check with warning; use explicit flags for other behavior

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

# Preview what would be uploaded
rescale-int files upload *.dat --dry-run --check-duplicates

# Upload large file - multi-part, with part size scaled to the file
rescale-int files upload large_dataset.tar.gz
```

**Note:** Files are encrypted locally using AES-256-CBC before upload. Decryption happens automatically on download. See [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md#encryption) for encryption details.

**Note on interrupted uploads:** Uploads do not resume. Individual parts are
retried automatically when the network hiccups, but an upload that dies outright
— Interlink killed, machine rebooted — restarts from the first byte when you rerun
the command.

The `--pre-encrypt` path writes a `<file>.upload.resume` sidecar as it goes, but it
does not reuse it: every attempt encrypts with a fresh key and IV under a fresh
object key, so the parts already sent describe different ciphertext and cannot be
continued. On the next run the stale state is discarded and the upload starts over
from the beginning; on S3 the orphaned multipart upload is aborted as well.
Resuming it would silently produce a corrupt file, so it is thrown away on purpose.

#### files download
Download files from Rescale

```bash
rescale-int files download <file-id> [file-id...] [flags]
```

**Features:**
- Automatic decryption after download
- Chunked download for files larger than 100MB (32MB chunks by default)
- Progress bars during download and decryption
- Resume capability for interrupted downloads (state saved to a `<file>.download.resume` sidecar)
- Streaming decryption using 16KB chunks (prevents memory exhaustion)
- Concurrent chunk downloads for large files

**Flags:**
- `-o, --outdir string` - Output directory (default: current directory)
- `-m, --max-concurrent int` - Maximum concurrent downloads, 1-20 (default 5). Actual concurrency adapts to file size within this cap
- `-w, --overwrite` - Overwrite existing files without prompting
- `-S, --skip` - Skip existing files without prompting
- `-r, --resume` - Resume interrupted downloads without prompting
- `--skip-checksum` - Skip post-download checksum verification (not recommended)

**Examples:**
```bash
# Download single file (automatically decrypted)
rescale-int files download abc123 -o ./results

# Download multiple files
rescale-int files download abc123 def456 ghi789 -o ./downloads

# Download large file - shows "Decrypting..." message for large files
rescale-int files download large-file-id -o output.dat

# Rerun an interrupted download; --resume answers the "partial file exists"
# prompt with "continue" instead of asking
rescale-int files download abc123 -o result.tar.gz --resume
```

**Note on Resume:** `--resume` is an answer to a prompt, not a transfer mode. When a
partial download is already on disk, Interlink asks what to do with it; `--resume`
answers "continue" up front so the command can run unattended.

What "continue" can actually reuse depends on how the file was stored:

- **Legacy (v0) encrypted files** keep a `.download.resume` JSON sidecar recording
  which chunks finished, and a rerun re-requests only the missing ones over HTTP
  Range. This applies only to the concurrent chunked path — the file must be over
  100MB *and* have been allocated more than one thread, which in practice means
  500MB and up. Granularity is the 32MB chunk, not the exact byte: a chunk
  interrupted halfway is fetched again in full.
- **Files in the current (v2 CBC-streaming) format** — everything uploaded by a
  current client — restart from zero. The output file is recreated on each attempt
  because AES-CBC decryption is chained from the start of the stream, so a partial
  plaintext cannot be extended safely.

Either way decryption starts from the beginning. In the current format it happens
inline as parts arrive. The legacy path is the one with a visible pause: it writes
the whole ciphertext to `<file>.encrypted` first, then prints `Decrypting ...` and
decrypts to the final file as a separate step.

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

**Example:**
```bash
rescale-int files list --limit 50
rescale-int files list --include "*.csv" --exclude "*backup*"
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

Positional arguments are rejected — `files delete --fileid A B C` used to delete `A`
and silently discard `B` and `C`, so every ID must carry its own `-i/--fileid`.

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

**Conflict Handling Modes:**
- **Skip** (`-S`): Skip subfolders that already exist on Rescale. The root folder cannot be skipped — if it already exists, the upload is cancelled with an error
- **Merge** (`-m`): Use existing folders and skip files that already exist
- **Interactive mode (no flags)**: Prompts for conflict handling mode when a folder exists
- **Non-interactive**: With no terminal to prompt on, the command fails and names `--skip-folder-conflicts` / `--merge-folder-conflicts`

**Performance Note:** Files upload concurrently with connection reuse. Folder creation runs concurrently too (`--folder-concurrency`, default 15).

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
- `--skip-checksum` - Skip checksum verification (not recommended)

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
- `-n, --limit int` - Maximum number of jobs to list (`0` returns all; default `0`)

**Examples:**
```bash
# List all jobs
rescale-int jobs list

# Cap the listing to 50 most recent
rescale-int jobs list --limit 50
```

There is no server-side status filter — Rescale's `/jobs/` endpoint accepts the parameter but ignores it. Filter client-side with `grep`/`jq` against the listing output if needed.

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

Polls the job's status history and prints each new entry as it appears. It does
not stream the job's log or console output — use `jobs listfiles` and
`jobs download` for the job's own output files.

**Flags:**
- `-j, --job-id string` - Job ID (required)
- `-i, --interval int` - Polling interval in seconds (default: 10)

**Note:** `jobs tail` stops on its own only at `Completed` or `Failed`. A job that
ends as `Stopped`, `Force Stopped`, or `Terminated` prints that status and then
keeps polling until you press Ctrl+C. `jobs watch` treats all five as terminal
and exits on any of them.

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
- `--file-id string` - Specific file ID to download (optional)
- `-d, --outdir string` - Output directory for batch download
- `-o, --output string` - Output file path (for single file)
- `-m, --max-concurrent int` - Maximum concurrent downloads, 1-20 (default 5). Actual concurrency adapts to file size within this cap
- `-w, --overwrite` - Overwrite existing files
- `-S, --skip` - Skip existing files
- `-r, --resume` - Resume interrupted downloads
- `-s, --search string` - Include only files whose name contains one of these terms (comma-separated, case-insensitive)
- `-x, --exclude string` - Exclude files matching these glob patterns (comma-separated)
- `--filter string` - Include only files matching these glob patterns; comma-separated. Matched against the filename
- `--path-filter string` - Include only files matching these path patterns. Matched against the file's path within the job, and supports `**` for recursive matching (e.g. `"run_1/*.dat"`, `"**/results/*.csv"`)
- `--skip-checksum` - Skip post-download checksum verification (not recommended)

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
- **Newer-than** (`--newer-than`): Watch all jobs created after a reference job. Downloads each job's files into per-job subdirectories (`OUTDIR/job_ID/`). Re-discovers newly-created jobs each polling tick.

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

Downloads use skip-existing semantics — files already present in the output directory are not re-downloaded. Press Ctrl+C to stop watching.

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
- `-E, --end-to-end` - Full workflow: upload, create, submit, monitor, download
- `--download` - Auto-download results after job completes (requires `--end-to-end`)
- `--no-tar` - Skip tarball creation for single file uploads
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

**SSH access to the running job:** A job specification can request inbound SSH.
Three fields carry it, and all three reach the Rescale API:

| JSON key (`--job-file`) | SGE directive (`--script`) | Meaning |
|---|---|---|
| `cidrRule` | `#RESCALE_INBOUND_SSH_CIDR` | CIDR range allowed to connect |
| `publicKey` | `#RESCALE_PUBLIC_KEY` | Public key authorized for the job user |
| `sshPort` | (JSON only) | Inbound SSH port |

All three are omitted from the request when unset, so a specification that does not
use them produces the same payload as before.

**Unknown keys in `--job-file`:** Top-level JSON keys that Interlink does not model
are not sent to Rescale. They are now named in a warning and the submit continues, so
a typo'd or unsupported field is visible instead of vanishing silently.

**Job tags:** Tags are applied to the created job from the `tags` column of a
jobs CSV or the `#RESCALE_TAGS` directive of an SGE script. In both cases tags
are **comma-separated** (e.g. `simulation, cfd, v2`); surrounding whitespace is
trimmed. A space alone is not a separator — `cfd run` is a single tag named
`cfd run`. Each tag is applied to the job individually after it is created.

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

**Config File:** `~/.config/rescale/daemon.conf` (Unix) or `%APPDATA%\Rescale\Interlink\daemon.conf` (Windows)

The daemon automatically loads settings from the config file. CLI flags override config file values, allowing you to test different settings without modifying the config file.

**Flags:**
- `-d, --download-dir string` - Directory to download job outputs to (default: value from `daemon.conf` `download_folder`, falling back to the platform default at `~/Downloads/rescale-jobs` on Unix or `%USERPROFILE%\Downloads\rescale-jobs` on Windows)
- `--poll-interval string` - How often to check for completed jobs (default "5m")
- `--name-prefix string` - Only download jobs with names starting with this prefix
- `--name-contains string` - Only download jobs with names containing this string
- `--exclude stringArray` - Exclude jobs with names starting with these prefixes
- `--max-concurrent int` - Maximum concurrent file downloads per job (default 5)
- `--state-file string` - Path to daemon state file
- `--use-job-id` - Use job ID instead of job name for output directory names
- `--once` - Run once and exit (useful for cron jobs)
- `--log-file string` - Path to log file (empty = stdout)
- `--background` - Run in background mode (Unix only)
- `--ipc` - Enable IPC server for GUI/CLI control

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

Requires the daemon to have been started with `--ipc`. Returns once the daemon has
acknowledged and exited, waiting up to 5 seconds. If no daemon is running it prints
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

If the file does not exist yet, the defaults are shown and labelled as such.

**Example output:**
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

The eligibility model was simplified in v4.3.0 to a single `auto_download_tag`; the older `correctness_tag` / `auto_download_value` / `downloaded_tag` keys are no longer settable.

##### daemon config path

Show the path to the daemon configuration file.

```bash
rescale-int daemon config path
```

**Example:**
```bash
rescale-int daemon config path
# Output: ~/.config/rescale/daemon.conf
```

##### daemon config edit

Open the daemon configuration file in your default editor.

```bash
rescale-int daemon config edit
```

Uses `$EDITOR` environment variable (falls back to `vi` on Unix, `notepad` on Windows).

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
is still accepted as a deprecated alias for `auto_download_tag`. `mode`,
`auto_download_value`, and `downloaded_tag` are no longer settable and print a note
explaining that mode moved to the per-job custom field. Any other key is an error.

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
concurrent, and whether auto-download is enabled — it is off by default, so set
`enabled true` to turn it on).

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
3. Set the field per job: `Enabled` opts the job into auto-download, `Disabled` (or unset) skips it. A job set to `Conditional` is downloaded only if it also carries the tag named by `auto_download_tag` in `daemon.conf` (default `autoDownload`). Individual jobs need not use `Conditional`, but the option still has to exist on the field.

The three values are matched case-insensitively, but the words themselves are fixed and
cannot be changed in Interlink. A job whose field is unset, or set to anything the daemon
does not recognize, is skipped — including near-misses such as `Enable` and `Disable`,
which produce no per-job skip line in the output.

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
- `--state-file string` - Path to daemon state file

**Live view** (a daemon is running with `--ipc`):
- Running / paused state, version, uptime
- Active downloads
- Last scan time, with how long ago it was
- **Last error**, with its age and an actionable hint — a scan that failed on an expired
  key, a dead network, or proxy trouble now says so instead of leaving a last-scan
  timestamp that quietly stops advancing. The error is cleared by the next scan that
  completes
- Per-user detail (download folder, jobs downloaded) where the daemon reports it

**State-file view** (no daemon answering):
- Whether a daemon process was found at all, and whether it is likely missing `--ipc`
- Last poll time
- Number of downloaded jobs and failed downloads
- Recent download history, and failed downloads with their error text

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
- `--state-file string` - Path to daemon state file
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

#### daemon retry

Mark failed jobs for retry on the next poll cycle.

```bash
rescale-int daemon retry [flags]
```

**Flags:**
- `--state-file string` - Path to daemon state file
- `--all` - Retry all failed jobs
- `-j, --job-id stringArray` - Job ID to retry (can be specified multiple times)

**Examples:**
```bash
# Retry all failed jobs
rescale-int daemon retry --all

# Retry specific job
rescale-int daemon retry --job-id XxYyZz
```

---

### Service Commands (Windows only)

Manage the Rescale Interlink Windows service. The service is the multi-user auto-download daemon used in MSI-installer deployments. These commands are no-ops on macOS and Linux — auto-download on those platforms uses the subprocess daemon (`daemon run`).

All `service` commands require an elevated (Administrator) command prompt.

#### service install

Register the Interlink service with Windows Service Control Manager. After install, use `service start` to bring it up.

```bash
rescale-int service install
```

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
- `--part-dirs strings` - Project directories for multi-part mode

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

# Multi-part mode: scan multiple project directories
rescale-int pur make-dirs-csv \
  --template template.csv \
  --output jobs.csv \
  --pattern "Run_*" \
  --part-dirs /data/DOE_1 /data/DOE_2 /data/DOE_3 \
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
- `--secondary strings` - Secondary file pattern; repeat for multiple. Each entry may end with `:required` (default) or `:optional`. Wildcard `*` is replaced with the primary file's basename.
- `-t, --template string` - Template CSV used as the row prototype when generating jobs CSV
- `-o, --output string` - Output jobs CSV path (must be combined with `--template`)
- `--overwrite` - Overwrite an existing output file
- `--json` - Emit the scan result as JSON instead of a printed summary

**Examples:**
```bash
# Print a summary of matched primary/secondary files
rescale-int pur scan-files --root /data --primary "*.inp" --secondary "*.mesh"

# Optional secondary from a sibling directory
rescale-int pur scan-files --root /data --primary "inputs/*.inp" \
  --secondary "*.mesh:required" --secondary "../common.cfg:optional"

# Generate jobs.csv from a template
rescale-int pur scan-files --root /data --primary "*.inp" \
  --template template.csv --output jobs.csv
```

#### pur plan
Validate job pipeline without executing

```bash
rescale-int pur plan --jobs-csv FILE [--validate-coretype]
```

**Flags:**
- `-j, --jobs-csv string` - Jobs CSV file (required)
- `--validate-coretype` - Validate core type with Rescale API

**Example:**
```bash
rescale-int pur plan --jobs-csv jobs.csv --validate-coretype
```

#### pur run
Execute complete job pipeline

```bash
rescale-int pur run --jobs-csv FILE [--state FILE] [--multipart]
```

**Pipeline stages:**
1. Create tar archives from run directories
2. Upload files to Rescale
3. Submit jobs to Rescale
4. Save state for resume capability

**Flags:**
- `-j, --jobs-csv string` - Jobs CSV file (required)
- `-s, --state string` - State file for resume capability
- `--multipart` - Enable multi-part mode
- `--common-input-files string` - Comma-separated local paths and/or `id:<fileId>` to share across all jobs
- `--decompress-common` - Decompress common input files on cluster (default: false)
- `--folder string` - Remote folder path for this batch's uploads, created if missing (e.g. `"sweeps/alpha-beta"`)
- `--folder-parent string` - Folder ID that `--folder` is resolved beneath (default: My Library)
- `--file-tags string` - Comma-separated tags applied to every file this batch uploads
- `--include-pattern strings` - Only tar files matching glob (repeatable)
- `--exclude-pattern strings` - Exclude files matching glob from tar (repeatable)
- `--flatten-tar` - Remove subdirectory structure in tarball
- `--tar-compression string` - Tar compression: "none" or "gzip"
- `--tar-workers int` - Parallel tar workers (default from config)
- `--upload-workers int` - Parallel upload workers (default from config)
- `--job-workers int` - Parallel job creation workers (default from config)
- `--rm-tar-on-success` - Delete local tar after successful upload
- `--dry-run` - Validate and show plan without executing

> **Deprecated:** `--extra-input-files` and `--decompress-extras` are hidden aliases for
> `--common-input-files` and `--decompress-common`. They still work but emit a warning;
> passing a flag together with its alias is an error. Use the `common` names.

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

# Dry-run: validate and preview without executing
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
- `--multipart` - Enable multi-part mode
- `--common-input-files string` - Comma-separated local paths and/or `id:<fileId>`
- `--decompress-common` - Decompress common input files on cluster
- `--folder string` - Remote folder path for this batch's uploads, created if missing
- `--folder-parent string` - Folder ID that `--folder` is resolved beneath (default: My Library)
- `--file-tags string` - Comma-separated tags applied to every file this batch uploads
- `--include-pattern strings` - Only tar files matching glob (repeatable)
- `--exclude-pattern strings` - Exclude files matching glob from tar (repeatable)
- `--flatten-tar` - Remove subdirectory structure in tarball
- `--tar-compression string` - Tar compression: "none" or "gzip"
- `--tar-workers int` - Parallel tar workers
- `--upload-workers int` - Parallel upload workers
- `--job-workers int` - Parallel job creation workers
- `--rm-tar-on-success` - Delete local tar after successful upload
- `--dry-run` - Show what would be resumed without executing

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
- `--state string` - State file (default `"submit_existing_state.csv"`)
- `--ids string` - Comma-separated job IDs to submit directly (mutually exclusive with --jobs-csv)

**Example:**
```bash
# Submit from CSV (existing behavior):
rescale-int pur submit-existing --jobs-csv jobs_with_fileids.csv

# Submit specific job IDs directly:
rescale-int pur submit-existing --ids "abc123,def456,ghi789"
```

**Example:**
```bash
rescale-int pur submit-existing --jobs-csv jobs_with_fileids.csv --state state.csv
```

### Shortcuts

Convenient aliases for commonly-used commands.

#### upload
Shortcut for `files upload`

```bash
rescale-int upload <file> [file...] [flags]
```

**Example:**
```bash
rescale-int upload input.txt data.csv
```

#### download
Shortcut for `files download`

```bash
rescale-int download <file-id> [file-id...] [flags]
```

**Example:**
```bash
rescale-int download abc123 --outdir ./downloads
```

#### ls
Shortcut for `jobs list`. Default limit is `10`; pass `--limit 0` to list all jobs.

```bash
rescale-int ls [--limit N]
```

**Example:**
```bash
rescale-int ls --limit 50
```

## Shell Completion

Enable shell completion for tab-completion of commands and flags.

### Bash

**Linux:**
```bash
rescale-int completion bash > /etc/bash_completion.d/rescale-int
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

```powershell
rescale-int completion powershell > rescale-int.ps1
```

## Compatibility Mode

Rescale Interlink includes a compatibility layer that provides drop-in replacement for `rescale-cli`, the legacy Java-based Rescale CLI. Existing scripts and automation workflows can migrate to Interlink without modification.

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
| `--version` | `-v` | Print version and exit |
| `--enableErrorTracking` | | Accepted and ignored (hidden) |
| `--no-ssl-verify` | | Accepted and ignored (hidden) |

### Credential Resolution

Credentials are resolved in this order:
1. `-p` flag (explicit)
2. `RESCALE_API_KEY` environment variable
3. apiconfig INI file (`--profile` section or `[default]`)

Base URL resolution: `-X` flag > `RESCALE_API_URL` env > profile > `https://platform.rescale.com`

### Exit Codes

- `0` — Success
- `33` — Error (matches rescale-cli convention)

### Commands

**`status`** — Check job status
```
rescale-cli status -j JOB_ID [-e] [--load-hours N]
```

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
rescale-cli upload -f file1.txt -f file2.txt [-d FOLDER_ID] [-e] [-r REPORT]
```

**`download-file`** — Download job output files
```
rescale-cli download-file -j JOB_ID -f FILENAME [-o OUTPUT]
rescale-cli download-file --file-id FILE_ID [-o OUTPUT]
rescale-cli download-file -j JOB_ID -r RUN_ID [-f FILENAME] [-o OUTPUT]
```

**`submit`** — Parse SGE script, upload inputs, create and submit job
```
rescale-cli submit -i SCRIPT [FILE...] [-E] [-f GLOB] [--p-cluster ID] [--waive-sla]
```

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

1. **Symlink approach** (recommended): Create a symlink so existing scripts work unchanged:
   ```bash
   ln -s /usr/local/bin/rescale-int /usr/local/bin/rescale-cli
   ```

2. **Flag approach**: Add `--compat` to your Interlink invocations:
   ```bash
   rescale-int --compat status -j JOB_ID
   ```

3. **Credential setup**: Compat mode reads the same `apiconfig` INI file as rescale-cli. If you have an existing `~/.config/rescale/apiconfig`, it will work automatically.

4. **Known differences**: `spub` commands are not yet supported. The `list-info -d` (desktops) and `check-for-update -i` (install) flags return "not yet implemented" errors.

## Compatibility Reference

This section documents the compatibility status between Interlink's compat mode and `rescale-cli`. This is a living document — as capabilities are added, these tables will be updated.

**Tested against**: rescale-cli versions 1.1.271 and 1.1.349, on both S3 and Azure storage backends.

### Input Compatibility

Compat mode accepts the same arguments as rescale-cli. The table below tallies 56
flag registrations; some flags repeat across commands, so the number of distinct
flags is lower:

| Category | Count | Details |
|----------|------:|---------|
| Fully implemented | 45 | Flag accepted and behavior matches rescale-cli |
| Accepted, ignored | 7 | `--enableErrorTracking`, `--no-ssl-verify`, `--no-prompt`, `-t`/`--type`, `--verify` (submit), `--verify` (sync), `--max-concurrent` (submit/sync) |
| Deferred (clean error, exit 33) | 4 | `-T`/`--Target`, `--copy-to-cfs`, `-d`/`--desktops`, `-i`/`--install-available` |

**Argument normalization**: Compat mode automatically normalizes rescale-cli's non-standard argument patterns:
- Multi-char short flags: `-fid VALUE` → `--file-id VALUE`, `-lh VALUE` → `--load-hours VALUE`
- Multi-value `-f`, `--files`, and `--file-matcher` on `upload` and `submit`:
  `-f a b c` → `-f a -f b -f c`

**Credential resolution chain** (independent from native CLI):
1. `-p/--api-token` flag (highest priority)
2. `RESCALE_API_KEY` environment variable
3. `apiconfig` INI profile (`--profile` section or `[default]`)

**Base URL resolution**: `-X` flag > `RESCALE_API_URL` env > profile > `https://platform.rescale.com`

No unknown-flag errors are possible — every rescale-cli flag is registered in Interlink. Unrecognized flags produce a standard Cobra error with exit code 33.

### Behavioral Compatibility

All 10 user-facing commands are implemented. Behavior was verified via head-to-head comparison across 30 parity items:

| Status | Count | Description |
|--------|------:|-------------|
| Pass/Fixed | 25 | Behavior matches rescale-cli or is strictly better |
| Intentional divergence | 5 | Interlink behavior is correct where rescale-cli crashes or is wrong |

**Intentional divergences** (Interlink is better in all 5 cases):
- `status -j BAD_ID`: Interlink shows a clean error; rescale-cli shows a Java stack trace.
- `download-file -j -f` on completed job: Interlink works correctly; rescale-cli crashes (Java NPE).
- `sync` metadata: Same files downloaded with same exit code. Different internal bookkeeping mechanism (file-existence vs `.rescale` metadata).
- `check-for-update`: Different tools checking for their own updates — matching would be incorrect.
- Help text: Custom argparse4j-style renderer in Interlink provides structural match with minor formatting differences.

**Per-command status**:

| Command | Status | Notes |
|---------|--------|-------|
| `status` | Implemented | Text and JSON (`-e`) modes, `--load-hours` (see Known Gaps) |
| `stop` | Implemented | Output matches including `-q` quirk |
| `delete` | Implemented | |
| `submit` | Implemented | SGE parsing, tarball flow, `-E` end-to-end, `-e` JSON transformation |
| `upload` | Implemented | Multi-file, `-e` JSON, `-r` report |
| `download-file` | Implemented | By job+filename, by file-id, by run-id, `-e` metadata |
| `list-info` | Implemented | Core types (`-c`) and analyses (`-a`) as JSON |
| `list-files` | Implemented | Run-specific listing supported |
| `sync` | Implemented | Single-job, polling (`-d`), newer-than (`-n`), file filtering |
| `check-for-update` | Implemented | Prints Interlink version and releases URL |
| `spub` | Deferred | Returns clear error indicating deferral to v5.0.0 |

### Output Compatibility

Compat mode reproduces rescale-cli's output format:

- **Exit codes**: 0 on success, 33 on error (matches rescale-cli convention).
- **Timestamps**: SLF4J-style format (`2006-01-02 15:04:05,000`).
- **JSON output** (`-e` flag): Field sets verified head-to-head for `status`, `upload`, `download-file`, `submit`, `list-info`.
- **`submit -e` JSON**: `transformSubmitJSON` reshapes the v3 API response to match rescale-cli's client-side JSON structure (26 top-level, 22 jobanalysis, 11 input-file keys).
- **`download-file -e`**: Filtered to 9-field set matching rescale-cli (`decryptedSize`, `encodedEncryptionKey`, `fileChecksums`, `id`, `isUploaded`, `name`, `pathParts`, `storage`, `typeId`).
- **Quiet mode** (`-q`): Suppresses informational output but preserves data output and errors. Matches rescale-cli's behavior including the `-q stop` quirk (unconditional status message).
- **Debug suppression**: `log.Printf` output is discarded in compat mode unless `RESCALE_DEBUG` is set.

### Testing Status

164 end-to-end tests across all commands, both S3 and Azure backends, flag combinations, edge cases, help text, and spub placeholders:

| Disposition | Count | Description |
|-------------|------:|-------------|
| PASS | 136 | Behavior matches or is strictly better than rescale-cli |
| FAIL | 0 | All resolved prior to release |
| KNOWN-GAP | 9 | Documented, not release-blocking (see below) |
| CLI-BUG | 5 | rescale-cli crashes; Interlink works correctly |
| SKIP | 14 | Long-running E2E or long-form aliases verified elsewhere |

**Known gaps** (9 items, none release-blocking):
- `--load-hours` returns empty data — Interlink's v2 `cluster-load-measurements` endpoint returns 404; rescale-cli uses an undiscovered endpoint. Interlink correctly returns `[]`.
- `upload -d` with invalid folder ID cannot be end-to-end tested (no folder creation API in compat mode); flag wiring verified in code.
- 4 hidden deferred flags not shown in help text (by design — they produce clean errors).
- `spub` subcommand tree is flat (5 placeholders) vs rescale-cli's hierarchical `tile`/`sandbox` tree (9 subcommands). All produce deferral messages.

**CLI bugs found in rescale-cli** (5 items where Interlink works correctly):
- `--enableErrorTracking` without `=true` breaks rescale-cli's argparse
- `download-file -j -f` on completed job: Java NPE
- `download-file -r RUN_ID`: Java NPE
- `list-files -r RUN_ID`: Java NPE
- `sync -d` on completed job: hangs indefinitely (never exits)

Full audit details are maintained in the repository's `old-reference/` directory.

## Examples

### Basic File Operations

```bash
# Upload files
rescale-int upload model.tar.gz input.dat

# List files
rescale-int files list --limit 50

# Download file
rescale-int download abc123 -o model_output.tar.gz

# Delete old files (moved to Trash; add --permanent to delete irreversibly)
rescale-int files delete -i old_file_id1 -i old_file_id2
```

### Folder Management

```bash
# Create project folder
rescale-int folders create --name "CFD Project Q1 2025"

# Upload entire simulation directory (significantly faster than individual uploads)
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
# Rescale's API doesn't filter by status server-side, so we filter client-side.
# Each job prints as a block with ID, Name, then Status, so the ID is two lines
# above the status line — hence `grep -B2`.
rescale-int jobs list --limit 100 | \
  grep -B2 "Status: Completed" | \
  grep "ID:" | \
  awk '{print $2}' | \
  while read job_id; do
    rescale-int jobs download -j "$job_id" -d "./job_$job_id"
  done
```

**Monitor job until completion:**
```bash
job_id="WfbQa"
while true; do
  status=$(rescale-int jobs get -j "$job_id" | grep "Status:" | awk '{print $2}')
  echo "Job $job_id status: $status"
  if [[ "$status" == "Completed" || "$status" == "Failed" ]]; then
    break
  fi
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

**Performance expectations**:
- Small files (<500MB): No change — these transfer on a single thread
- Medium files (500MB-1GB): 1.5-2x speedup
- Large files (1-10GB): 2-4x speedup
- Very large files (>10GB): 3-5x speedup

**Global flags for thread control**:
- `--max-threads N`: Total thread pool size (0=auto, 1-32)
- `--no-auto-scale`: Disable adaptive thread allocation
- `--max-concurrent N`: Override adaptive file-level concurrency with a fixed value

**Adaptive concurrency:**
All batch transfers scale their concurrency to the file size distribution, within the
`--max-concurrent` cap:
- Many small files (<100MB): up to 20 concurrent transfers
- Medium files (100MB–1GB): up to 10 concurrent transfers
- Large files (>1GB): up to 5 concurrent transfers (more threads per file)

The cap differs by command. `folders upload-dir` and `folders download-dir` are the only
two that raise it to 20 when you do not set `--max-concurrent`, so adaptive scaling can
reach the top of that range there. Every other command that takes the flag defaults to a
cap of 5 — `files upload`, `files download`, `jobs submit`, `jobs download`, `jobs watch`,
and the `upload` and `download` shortcuts. Raise it explicitly (up to 20) for a directory
full of small files.

The adaptive count is validated against available system memory and thread pool capacity.

### General Tips

1. **Use folders upload-dir for bulk uploads**: Connection reuse makes this significantly faster than uploading files one at a time
2. **Batch operations**: Upload/download multiple files in one command
3. **PUR pipeline**: Efficiently manage dozens or hundreds of jobs
4. **State files**: Resume interrupted operations without starting over
5. **Thread tuning**: Use `--max-threads` for large files on fast connections
6. **Adaptive concurrency**: For folders with many small files, the default adaptive mode provides the best throughput automatically

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

# All uploads are multipart; part size scales with the file
```

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

Retries are bounded and reported. If a transfer or API call stalls, watch for the
`⟳ Retrying ...` lines: they name the operation, the attempt, and the backoff. Each
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
`--no-check-duplicates` to choose deliberately.

## Support

For issues and feature requests:
- GitHub Issues: https://github.com/rescale-labs/Rescale_Interlink/issues
- Documentation: https://docs.rescale.com

## Version

```bash
rescale-int --version
```

See [RELEASE_NOTES.md](RELEASE_NOTES.md) for complete version history and [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md) for comprehensive feature details.
