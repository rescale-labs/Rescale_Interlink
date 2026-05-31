# Rescale Interlink — Feature Summary

**Version:** 4.9.8
**Last Updated:** May 31, 2026
**Status:** Production Ready, FIPS 140-3 Compliant (Mandatory)

This document catalogs what Rescale Interlink can do. For full command syntax, see [CLI_GUIDE.md](CLI_GUIDE.md). For architecture internals, see [ARCHITECTURE.md](ARCHITECTURE.md). For version history, see [RELEASE_NOTES.md](RELEASE_NOTES.md).

---

## Table of Contents

- [Core Platform](#core-platform)
- [File Operations](#file-operations)
- [Folder Operations](#folder-operations)
- [Job Operations](#job-operations)
- [CLI Compatibility Mode](#cli-compatibility-mode)
- [Background Service (Daemon)](#background-service-daemon)
- [PUR (Parallel Upload and Run)](#pur-parallel-upload-and-run)
- [Configuration Management](#configuration-management)
- [Hardware & Software Discovery](#hardware--software-discovery)
- [GUI Features](#gui-features)
- [Transfer Architecture](#transfer-architecture)
- [Security](#security)
- [Performance](#performance)

---

## Core Platform

### Dual Interface
- **CLI Mode** (default): Command-line interface for automation and scripting. Entry point: `cmd/rescale-int/`
- **GUI Mode**: Graphical interface with Wails v2 + React/TypeScript frontend. Entry point: root `main.go` with `--gui` flag via `rescale-int-gui` binary

### Supported Platforms
- macOS (darwin/arm64, darwin/amd64)
- Linux (amd64)
- Windows (amd64)

### FIPS 140-3 Compliance
All production builds are compiled with `GOFIPS140=certified` (the CMVP-validated Go Cryptographic Module) and the `fips` build tag. Non-FIPS builds refuse to run (exit code 2) unless `RESCALE_ALLOW_NON_FIPS=true` is set. Mandatory for FedRAMP environments.

---

## File Operations

**Command:** `rescale-int files [subcommand]`

### Upload
- Single or multiple file upload
- Upload to specific folder with `--folder-id`
- **Streaming encryption** (default): encrypts on-the-fly during upload, no temp file needed
- **Legacy mode** (`--pre-encrypt`): full-file encryption before upload, compatible with older clients
- Multi-part upload for files ≥100MB (32MB parts)
- Automatic resume on interruption
- Progress bars with transfer speed and ETA
- S3 and Azure backends with seekable upload streams for retry

### Download
- Single or multiple file download
- Automatic decryption after download
- Chunked/concurrent download for files ≥100MB
- Full byte-offset resume via HTTP Range requests
- Progress bars during download and decryption
- No file size limit

### List
- List all files in library with ID, name, size, upload date

### Delete
- Move one or more files to Trash (recoverable) with a confirmation prompt; use `--permanent` to delete irreversibly

---

## Folder Operations

**Command:** `rescale-int folders [subcommand]`

### Create Folder
- Create new folder in library, returns folder ID

### List Folders
- List all folders with metadata

### Upload Directory
- Recursive directory upload preserving structure
- Exclude patterns (glob-style)
- Concurrent file uploads with adaptive concurrency
- Conflict handling (skip/overwrite/rename)
- Resume capability
- Streaming folder creation (creates remote folders as parent becomes ready)

### Download Directory
- Recursive folder download recreating local structure
- Include patterns for selective download
- Concurrent downloads with adaptive concurrency
- Streaming scan-to-download (downloads begin within seconds of scan start)

### Delete Folder
- Move a folder (and its contents) to Trash (recoverable) with confirmation; use `--permanent` to delete irreversibly

---

## Job Operations

**Command:** `rescale-int jobs [subcommand]`

### List Jobs
- List all jobs with optional status filtering and limit

### Get Job Details
- Detailed job info: status, command, compute resources, timing
- JSON output with `--verbose`

### Submit Job
- Submit from SGE-style script
- Automatic file upload with encryption
- Core type, walltime, slots, and automation parameters

### Stop Job
- Graceful termination of running or queued jobs

### Tail Job Logs
- Real-time log streaming with configurable polling interval

### List Job Output Files
- Optimized v2 API endpoint for fast file listing

### Download Job Outputs
- Download all output files with automatic decryption
- Selective download with include/exclude patterns
- Optimized: zero per-file `GetFileInfo` calls (metadata from listing)

### Delete Jobs
- Delete one or more completed jobs

### Watch Jobs
- **Single-job mode** (`-j`): Watch one job, incrementally download files as they appear
- **Newer-than mode** (`--newer-than`): Watch all jobs created after a reference job, download each into per-job subdirectories
- File filtering with `--filter`, `--exclude`, `--search`
- Configurable polling interval (default 30s, minimum 5s)
- Shared watch engine used by both native CLI and compat mode

---

## CLI Compatibility Mode

Drop-in replacement for `rescale-cli` (the legacy Java-based Rescale CLI). Existing scripts and automation workflows can migrate to Interlink without modification.

### Activation
- `--compat` flag: `rescale-int --compat status -j JOB_ID`
- Binary name detection: symlink or rename as `rescale-cli`

### Implemented Commands (10)
`status`, `stop`, `delete`, `check-for-update`, `list-info`, `upload`, `download-file`, `submit`, `list-files`, `sync`

### Key Features
- Independent credential chain: `-p` flag > `RESCALE_API_KEY` env > `apiconfig` INI profile
- Exit code 33 on error (matches rescale-cli)
- SLF4J-style timestamp format
- Argument normalization (`-fid` → `--file-id`, multi-value `-f` expansion)
- JSON output modes (`-e` flag)
- Quiet mode (`-q`)
- `sync` command: watch and incrementally download job outputs (wraps shared watch engine)

### Deferred Commands
`spub` (software publisher) subcommands: clear error indicating deferral to v5.0.0.

See [CLI_GUIDE.md](CLI_GUIDE.md) for full command reference.

---

## Background Service (Daemon)

**Command:** `rescale-int daemon [subcommand]`

Background service for automatically downloading completed jobs.

### Features
- Automatic polling for completed jobs (configurable interval, default 5m)
- Job name filtering (prefix, contains, exclude patterns)
- Persistent state tracking (downloaded/failed jobs)
- Output directories include job ID suffix to prevent collisions
- Graceful shutdown on Ctrl+C
- **Tag-based source of truth**: The `downloaded` tag on the Rescale platform is authoritative. Removing the tag via the Rescale web UI triggers a re-download on the next poll; a tag-apply failure after a successful download is retried without re-downloading the files.
- **Shared transfer engine**: Daemon downloads route through the same `TransferService` the GUI uses. Multi-file jobs download in parallel with adaptive concurrency; there is no parallel transfer implementation inside the daemon.
- **Unified Transfers tab**: Daemon transfers appear alongside GUI transfers with a `Daemon` badge. Per-row Cancel/Retry works on daemon rows, routed via IPC; `Cancel All` cancels both engines.

### Subcommands
- `run` — Start the daemon (foreground or `--background`, optional `--ipc`)
- `stop` — Send a clean shutdown request to a running daemon
- `status` — Show daemon state and statistics
- `list [--failed]` — List downloaded or failed jobs
- `retry [--all | -j ID...]` — Mark failed jobs for retry on the next poll
- `config show` / `config path` / `config edit` / `config set <key> <value>` / `config init` / `config validate` — Manage `daemon.conf`

On Windows MSI installs, the daemon is fronted by the Windows Service. See the **Service Commands** section in [CLI_GUIDE.md](CLI_GUIDE.md) for `service install`, `start`, `stop`, `install-and-start`, and `status`.

### Platform Support
- macOS/Linux: subprocess mode with Unix domain socket IPC
- Windows: native service mode with named pipe IPC, multi-user support, UAC elevation

---

## PUR (Parallel Upload and Run)

**Command:** `rescale-int pur [subcommand]`

Batch job submission pipeline for parallel computational studies.

### Run Pipeline
- Batch job submission from CSV files
- Multi-part directory support with pattern matching (`Run_*`, `Sim_*`, nested patterns)
- Automatic file upload with streaming encryption
- Job submission with parameterization
- State management for resume capability
- Concurrent tar/upload/submit workers
- Context-aware cancellation
- Tar subpath and scan prefix support
- Extra input files (upload once, attach to every job)
- Iterate command patterns (vary commands across runs)

### Additional Commands
- `make-dirs-csv` — Auto-generate jobs CSV from directory structure
- `scan-files` — Scan a tree for primary input files plus optional secondary attachments, summarize the matches, and optionally generate a jobs CSV from a template
- `plan` — Validate pipeline (dry-run)
- `resume` — Resume interrupted pipeline from state file
- `submit-existing` — Submit jobs using previously uploaded files

### GUI PUR Tab
- Three-step workflow: configure → scan → execute
- Load/Save settings (CSV, JSON, SGE formats)
- Pipeline Settings (workers, tar options)
- Real-time monitoring dashboard with live progress
- Run queue: "Queue Run" when another run is active, auto-start on completion

---

## Configuration Management

**Command:** `rescale-int config [subcommand]`

### Commands
- `config init` — Interactive setup with numbered platform menu
- `config show` — Display current configuration
- `config test` — Test API connection
- `config path` — Show the configuration file path

### Storage
`config.csv` is the single source of truth for all persistent settings. API keys are stored in a separate token file (`~/.config/rescale/token`) with `0600` permissions. Keys are never written to `config.csv`.

---

## Hardware & Software Discovery

### Hardware
- `rescale-int hardware list [--search TERM]` — List available core types

### Software
- `rescale-int software list [--search TERM]` — List available software packages

---

## GUI Features

### Tabs

1. **Setup Tab**: API configuration, proxy settings, logging configuration, auto-download daemon management
2. **Single Job Tab**: Job template builder with three input modes (directory, local files, remote files). Tar options for directory mode. Form state persists across tab navigation.
3. **PUR Tab**: Batch job pipeline with view modes (choice screen, monitoring, configuration), pipeline settings, run queue
4. **File Browser Tab**: Two-pane local/remote browser with upload, download, and delete operations. The remote pane offers four browse modes — My Library, My Jobs, Legacy, and Trash. Trash shows soft-deleted entries with restore/purge actions; Upload is disabled in Trash and My Jobs with an explicit "N/A in this view" reason.
5. **Transfers Tab**: Transfer progress with batch grouping (folder ops, PUR, single-job collapse into single rows), cancel/retry, filter chips, disk space error banner. Daemon auto-download rows appear inline with a `Daemon` badge and support per-row Cancel/Retry via IPC.
6. **Activity Tab**: Logs with level filtering (DEBUG/INFO/WARN/ERROR), run history with expandable job tables

### Transfer Grouping
Bulk operations collapse into single aggregate batch rows instead of showing thousands of individual rows:
- Folder uploads/downloads use enumeration ID as batch ID
- PUR pipeline generates `pur_<timestamp>` batch ID
- Single-Job generates `job_<timestamp>` batch ID
- File Browser multi-file selections generate `fb_upload_<timestamp>` / `fb_download_<timestamp>` batch IDs
- Expand to see paginated individual tasks (50 per page)
- Batch-level cancel and retry

### Run Session Persistence
- Active runs tracked across tab navigation via `runStore`
- Job queue: submit becomes "Queue Run"/"Queue Job" when a run is active
- Restart recovery: localStorage persistence + historical state file loading
- Activity tab shows completed runs with expandable job tables

### Error Reporting
- Modal dialog for genuine server-side failures (not user-fixable errors)
- Shows redacted technical details, operation context, optional user notes
- "Copy to Clipboard" / "Save Report" buttons
- Privacy note: no API keys, passwords, or file contents included

### Update Notification
- GUI checks GitHub for newer releases on startup
- Yellow badge with "Update available" when newer version exists
- Disabled on FedRAMP platforms; env var kill switch available

---

## Transfer Architecture

### Unified Backend
All transfers (CLI, GUI, daemon) converge to single entry points (`upload.UploadFile()`, `download.DownloadFile()`) and share the same provider factory, orchestration layer, and resume system.

### Streaming Encryption
Default mode encrypts on-the-fly during upload (AES-256-CBC, HKDF-SHA256 key derivation per part). No temporary encrypted file needed. Constant ~16KB memory regardless of file size.

### Batch Abstraction
`RunBatch[T]` and `RunBatchFromChannel[T]` provide unified execution for all transfer paths. Adaptive concurrency computed from median file size.

### Two-Layer Concurrency
- **Layer 1**: Batch concurrency — how many files transfer simultaneously (5–20, adaptive)
- **Layer 2**: Per-file multi-threading — each file gets threads from a shared pool based on size

### Resume Support
- **Upload**: State saved to `.upload.resume` JSON files (parts, encryption key, IV)
- **Download**: State saved to `.download.resume` JSON files with byte-offset HTTP Range resume

### Conflict Handling
Thread-safe `ConflictResolver[A comparable]` generic type with automatic escalation from "prompt each" to "apply all".

### Progress Tracking
- CLI: `mpb` multi-progress bars with per-file speed and ETA
- GUI: EventBus events forwarded through Wails event bridge, 100ms throttling

---

## Security

### Encryption
- **Algorithm**: AES-256-CBC with PKCS7 padding
- **Key**: 256-bit random per operation
- **IV**: 128-bit random per operation
- **Streaming**: 16KB chunks, constant memory footprint
- **TLS**: 1.2+ with FIPS-approved cipher suites

### Proxy Support
Modes: `no-proxy`, `system`, `basic`, and `ntlm` where supported. FIPS-tagged builds disable NTLM at build and backend-validation time; FedRAMP platforms also disable NTLM in the GUI. Proxy warmup for authentication. `NO_PROXY` bypass rules fully wired.

### S3 FIPS Endpoints
ITAR platforms (`itar.rescale.com`, `itar.rescale-gov.com`) automatically route S3 traffic through AWS FIPS-validated endpoints. No user configuration required.

### Platform URL Allowlist
API communication restricted to 6 known Rescale platform URLs. Prevents credential exfiltration via `--api-url`.

### Error Report Privacy
Reports redact hex tokens, URL params, emails, auth tokens, home paths, and file paths. Only server errors (5xx) and unclassified internal errors generate reports.

### API Key Security
Token file with `0600` permissions. Keys never logged or written to config.csv. State files with sensitive data use `0600` permissions.

### Sleep Prevention
OS sleep/suspend inhibited during transfers: IOPMAssertion (macOS), SetThreadExecutionState (Windows), systemd-inhibit (Linux).

See [SECURITY.md](SECURITY.md) for complete security documentation.

---

## Performance

### Rate Limiting
Token bucket algorithm with cross-process coordinator (Unix socket / named pipe). Three scopes: User (1.7 req/sec), Job Submission (0.236 req/sec), Jobs Usage (21.25 req/sec). 429 feedback loop propagates cooldowns across all processes.

### Adaptive Concurrency
Dynamic scaling based on file size distribution: <100MB → 20 workers, 100MB–1GB → 10, >1GB → 5. Validated against thread pool and memory constraints.

### FileInfo Enrichment
Folder listings parse full metadata. Downloads skip per-file `GetFileInfo()` API calls. Eliminates hours of overhead for large folders.

### Connection Reuse
HTTP connection pooling (100 idle, 20 per host) across all operations in a batch.

### Streaming Scan-to-Download
GUI folder downloads start downloading within seconds of scan initiation. 8 concurrent subfolder workers emit files to a channel consumed by the download pipeline.

### Page Size Enforcement
All folder listing pagination uses `page_size=1000` (API maximum), reducing pagination calls ~40x.

### Folder Caching
In-memory cache for folder contents during directory uploads, reducing duplicate API calls.

---

## Documentation References

- **[README.md](README.md)** — Quick start guide
- **[CLI_GUIDE.md](CLI_GUIDE.md)** — Complete command reference with examples
- **[ARCHITECTURE.md](ARCHITECTURE.md)** — System design and technical architecture
- **[RELEASE_NOTES.md](RELEASE_NOTES.md)** — Detailed version history
- **[SECURITY.md](SECURITY.md)** — Security architecture and policies
- **[TESTING.md](TESTING.md)** — Test guide and coverage
- **[CONTRIBUTING.md](CONTRIBUTING.md)** — Contributing guidelines
