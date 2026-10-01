# Rescale Interlink — Feature Summary

**Version:** 4.9.9
**Last Updated:** September 26, 2026
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
- [Discovery Commands](#discovery-commands)
- [CLI Behavior](#cli-behavior)
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

**Release artifacts** (what a tagged release publishes):
- macOS (darwin/arm64) — signed and notarized `.zip`
- Linux (amd64) — `.tar.gz` holding the CLI and an AppImage GUI. The AppImage bundles WebKit's helper executables with `$ORIGIN`-relative RPATHs, so the window renders on hosts whose own WebKit build differs. The Linux build gates the AppImage on `build/linux/verify-appimage.sh` before packaging the tarball, so helpers that are missing or resolve against the host fail the build. It is built by `.github/workflows/release-linux.yml` in an `almalinux:8` container, and fails if a binary needs a GLIBC newer than 2.28
- Windows (amd64) — portable `.zip` and `.msi`; the executables are signed before zipping and the MSI is signed after it is built
- Every asset is published with a `.sha256` beside it

**Additional build targets** (`make build-all`, not published with a release):
- macOS (darwin/amd64)
- Windows (amd64) Mesa software-rendering variant, for VMs and RDP sessions without usable GPU acceleration

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
- Every streaming upload is multipart, whatever the file size. The 100MB threshold (`constants.MultipartThreshold`) applies only to `--pre-encrypt`, which sends smaller files in a single request, and to legacy download chunking. Part size is planned per file, not fixed: 16MB / 32MB / 48MB / 64MB by file size, clamped to the transfer memory budget, then raised as far as needed to keep the part count inside the backend's limit (10,000 for S3, 50,000 for Azure). A file too large for even the largest legal part is refused up front, naming the biggest file that backend can take
- An interrupted upload resumes. A `<file>.upload.resume` record beside the source names the backend upload and the parts it has accepted, and a rerun continues from it while the source file is unchanged and the record is less than seven days old. The streaming default continues from the last contiguous part and rewrites the record at most every 10 seconds and once when it stops; `--pre-encrypt` continues from its encrypted copy. An upload from a folder that cannot hold the record runs without it and cannot resume
- An exclusive `<file>.upload.lock` keeps two transfers off one source file; symbolic links are followed, and a named pipe, device or socket is refused as a source
- Every upload path checks completeness before committing and aborts rather than registering a partial object: the streaming default requires every part to be present, and `--pre-encrypt` additionally verifies the byte count and that the part or block sequence has no gaps (v4.9.9)
- Progress bars with transfer speed and ETA, including a retry label when a part is retried
- S3 and Azure backends with seekable upload streams for retry
- Per-file tags applied after upload with `--tags`

### Download
- Single or multiple file download
- Automatic decryption after download
- Chunked/concurrent download for files larger than 100MB
- Byte-offset resume via HTTP Range requests (`<file>.encrypted.download.resume` sidecar, beside the downloaded ciphertext) — legacy-format objects only. The current v2 CBC format decrypts sequentially into the output file, so an interrupted download restarts from zero
- Progress bars during download and decryption
- Names from the platform are checked before they become local paths: an empty or dot-only name, a reserved Windows device name, or one with a colon, a Windows-forbidden or control character, or a trailing dot or space is refused for that file, or for that folder and everything in it, and the command exits non-zero. A download never writes through a symbolic link or special file at its destination
- SHA-512 verification, with the algorithm name matched in any spelling; a file whose listed checksums include no SHA-512 is reported as not verified
- Pre-flight disk space check that reports the requirement it actually enforced, measured on the filesystem holding the output directory — on the legacy path, which stages an encrypted temp file, and on the Azure v1 path. The v2 CBC format writes plaintext straight out and has no pre-flight check
- No file size limit

### List
- List all files in library with ID, name, size, upload date
- Client-side include/exclude glob filters and a filename search term

### Tags
- `files tags list | add | remove | set` — read, extend, prune, or replace a file's tags. `set` with no tags clears them

### Delete
- Move one or more files to Trash (recoverable) with a confirmation prompt; use `--permanent` to delete irreversibly
- IDs come from repeated `-i/--fileid` flags; positional arguments are rejected so extra IDs cannot be silently dropped

---

## Folder Operations

**Command:** `rescale-int folders [subcommand]`

### Create Folder
- Create new folder in library, returns folder ID

### List Folders
- List all folders with metadata

### Upload Directory
- Recursive directory upload preserving structure
- Optional hidden-file inclusion (`--include-hidden`)
- Concurrent file uploads with adaptive concurrency, plus concurrent folder creation (`--folder-concurrency`)
- Folder conflict handling: skip existing subfolders, or merge into them. The root folder cannot be skipped
- Each unfinished file resumes on a re-run, as a single-file upload does
- Streaming folder creation (creates remote folders as parent becomes ready)
- Symbolic links are followed wherever they point; a link that leads back into the folder being uploaded (or a folder containing it), a broken link, and on Windows a link to a folder are skipped, named with the reason, and counted
- Exits non-zero when any file, directory walk, or folder creation failed

### Download Directory
- Recursive folder download recreating local structure
- Conflict handling: skip, overwrite, or merge; `--dry-run` previews the plan. Without a terminal, a destination that already holds files needs one of the three
- Concurrent downloads with adaptive concurrency
- Streaming scan-to-download (downloads begin within seconds of scan start)
- Checksum verification after download, waivable with `--skip-checksum`
- A file conflict that cannot be resolved counts as a failed file; without `--continue-on-error` the first failure stops the download, and the command exits non-zero when any file failed
- `--overwrite` replaces existing files even when the size matches

### Delete Folder
- Move a folder (and its contents) to Trash (recoverable) with confirmation; use `--permanent` to delete irreversibly

---

## Job Operations

**Command:** `rescale-int jobs [subcommand]`

### List Jobs
- List all jobs, newest first, with an optional display limit (`0` = all)
- No server-side status filter: Rescale's `/jobs/` endpoint accepts the parameter and ignores it, so filtering is client-side

### Get Job Details
- Detailed job info: status and status reason, command, compute resources, timing, owner

### Submit Job
- Submit from a JSON job specification (`--job-file`) or an SGE-style script (`--script`), whose directives may be written Interlink's way (`#RESCALE_NAME job1`) or rescale-cli's (`#RESCALE_NAME=job1`), or submit an already-created job by ID
- Three workflow modes: `--create` (create only), `--submit` (default), `-E/--end-to-end` (upload → create → submit → monitor, with `--download` for results)
- Automatic file upload with encryption
- Core type, walltime, slots, tags, project, environment variables, license settings, and automation parameters
- **SSH access fields** reach the API: `cidrRule`, `publicKey`, and `sshPort` in a `--job-file`; `#RESCALE_INBOUND_SSH_CIDR` and `#RESCALE_PUBLIC_KEY` in a script. All are omitted when unset, so specs that do not use them produce an unchanged payload
- Top-level `--job-file` keys Interlink does not model are named in a warning and the submit continues, instead of vanishing silently

### Stop Job
- Graceful termination of running or queued jobs

### Tail Job Logs
- Real-time log streaming with configurable polling interval

### List Job Output Files
- Optimized v2 API endpoint for fast file listing

### Download Job Outputs
- Download all output files with automatic decryption, or one file by ID. `--skip`, `--overwrite`, `--resume` and `--skip-checksum` work in both modes; `--output` names the one file's path and `--outdir` the directory for a whole job, and `--max-concurrent` must be 1–20
- Selective download with filename include/exclude globs, a search term, and `--path-filter` for path patterns (supports `**`)
- Optimized: zero per-file `GetFileInfo` calls (metadata from listing)

### Delete Jobs
- Delete one or more completed jobs
- `--job-id` accepts `--id` as an alias; passing both is rejected rather than acting on whichever was parsed last (same on `jobs get` and `jobs download`)

### Watch Jobs
- **Single-job mode** (`-j`): Watch one job, incrementally download files as they appear
- **Newer-than mode** (`--newer-than`): Watch all jobs created after a reference job, download each into per-job subdirectories
- File filtering with `--filter`, `--exclude`, `--search`
- Configurable polling interval (default 30s, minimum 5s)
- Shared watch engine used by both native CLI and compat mode

---

## CLI Compatibility Mode

Compatibility mode implements ten commands from `rescale-cli`, the legacy Java-based Rescale CLI. Check the defaults and script-handling differences in [CLI_GUIDE.md](CLI_GUIDE.md#compatibility-mode) before migrating an existing workflow.

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
- `submit` reads a job script's directives as rescale-cli writes them (`#RESCALE_NAME=job1`), with rescale-cli's meanings, and refuses one Interlink cannot carry out, naming its line

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
- Output directories are named after the sanitized job name; the job ID is recorded in a `.jobid` file inside the folder (rather than appended to the folder name). If a same-named folder already exists for a different job, the job ID is appended to avoid collisions
- Graceful shutdown on Ctrl+C
- **Workspace folders (opt-in)**: with `include_workspace_folders`, jobs in the workspace's shared folders, including other users' jobs, are downloaded too, into a mirror of the folder tree under the download folder, or into the download folder itself with `flatten_folder_structure`. Archived folders are skipped, and `daemon status` counts the folders and out-of-window jobs the last poll left out
- **Tag-based source of truth**: The `autodownload:done` tag on the Rescale platform is authoritative (`autoDownloaded:true`, applied by earlier versions, counts too). Removing the tag via the Rescale web UI triggers a re-download on the next poll; a tag-apply failure after a successful download is retried without re-downloading the files.
- **Multi-client coordination**: When several clients poll the same workspace folders, each puts an `autodownload:started:<client>:<time>` tag on a job before downloading it and reads the job's tags back a few seconds later; only the client whose tag is the earliest downloads the job, provided the computers' clocks agree to within a couple of seconds, as automatic time synchronization normally keeps them. The others skip it, logging which tag holds it and until when, and `daemon status` counts such jobs. A client removes its tag when the attempt fails or the daemon stops (so the job is retryable), retrying a removal that fails at the next poll, and replaces it with `autodownload:done` on success. A tag holds its job for 24 hours at most, so a client that crashed does not hold a job for ever.
- **Shared transfer engine**: Daemon downloads route through the same `TransferService` the GUI uses. Multi-file jobs download in parallel with adaptive concurrency; there is no parallel transfer implementation inside the daemon.
- **Unified Transfers tab**: Daemon transfers appear alongside GUI transfers with a `Daemon` badge. Per-row Cancel/Retry works on daemon rows, routed via IPC; `Cancel All` cancels both engines.
- **Failures are reported, not just absent**: a scan that throws (expired key, dead network, proxy trouble) records the error, its code, and when it happened. `daemon status` and the Setup tab show it with its age and an actionable hint; the next successful scan clears it. Previously the only outward symptom was a last-scan timestamp that stopped advancing.
- **Actions that did not happen say so**: a scan trigger that could not start (stopped, paused, or a poll already running) reports why instead of returning success, and "Save all settings" asks the running daemon to reload and reports the result — writing `daemon.conf` alone leaves a running daemon on its old settings, and a daemon that is downloading keeps them until auto-download is restarted, which the message says. Switching **Enable Auto-Download** off stops a running daemon, as **Stop Auto-Download** does.
- **Pre-flight validates the download folder** with the same write probe the config save uses, so a read-only folder is caught before every download fails.
- **Bounded growth**: the report directory keeps the newest 500 files; state entries are dropped once no scan could select them again (jobs still owed a tag call are exempt); and terminal transfer tasks beyond the 20 most recent batches are cleared, keeping recent auto-downloads visible without accumulating one task per file forever.
- **The downloaded count is a trailing window, not a lifetime total**: `daemon status` and the Setup tab count the state entries still retained, and retention is `lookback_days` + 30 days — about 37 days at the default. Jobs downloaded before that fall out of the count.
- **Rate limit and retry notices** reach a detached daemon's log file and IPC log buffer, not the closed stderr it was started with.

### Subcommands
- `run` — Start the daemon (foreground or `--background`, optional `--ipc`). Only one runs at a time: every mode claims the PID file under an OS lock (`daemon.pid.lock`) first, and a second launch refuses
- `stop` — Send a clean shutdown request to a running daemon, wait up to 10 seconds for it to exit, and exit 1 if it has not, or if it does not answer and `--force` is not given
- `status` — Show daemon state and statistics
- `list [--failed]` — List downloaded or failed jobs; `--failed` shows when each will be tried next
- `retry [--all | -j ID...]` — Release failed jobs for another attempt, also while a daemon is running. A failed job is otherwise retried after 5, 10, 20 and then 30 minutes and held after five failed attempts
- `config show` / `config path` / `config edit` / `config set <key> <value>` / `config init` / `config validate` — Manage `daemon.conf`

On Windows MSI installs, the system tray app starts the daemon at login when auto-download is enabled. The daemon runs as the logged-in user (no Windows service, no admin), so it can reach the user's mapped network drives. A Windows service installed by an earlier version removes itself the next time Windows starts it, and `rescale-int service uninstall`, run as administrator, removes it at once. See [ARCHITECTURE.md → Auto-Download Process Model](ARCHITECTURE.md#auto-download-process-model).

### Platform Support
- macOS/Linux: subprocess in the user's session, Unix domain socket IPC
- Windows: subprocess in the logged-in user's session, with a named pipe of each user's own, so several signed-in users can each run auto-download; tray auto-start at login

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
- Common input files (upload once, attach to every job) — `--common-input-files`, with `--extra-input-files` kept as a deprecated hidden alias. Takes local files, folders (every file under one, leaving out files and folders whose names start with `.`) and `id:<fileId>` references, all checked before the run starts
- Iterate command patterns (vary commands across runs)
- Optional remote folder and file tags for a batch's uploads (`--folder`, `--folder-parent`, `--file-tags`)

### Design of Experiments
- `doe` — Expand one base job into a parameter sweep, one job per case
- Eight designs: full factorial, one-factor-at-a-time, Latin hypercube, Sobol, Monte Carlo, central composite, Box-Behnken, explicit cases from a CSV
- Values are rendered into each case's **command line**, not environment variables, so the configuration is visible on the Rescale job page
- Parameters and `{{token}}`s in the command are checked against each other in both directions; unknown tokens in the job-name and tag templates are errors too
- Per-case job name and Rescale job tag templates, with `{{__base}}` and `{{__index}}` built in
- A sweep never zips a working directory: the shared deck comes either from `--base-file-ids` (already-uploaded IDs referenced by every case) or from `--common-input-files` at run time
- `--preview` prints the design, the first case's rendered command and its tags without writing anything

### Additional Commands
- `make-dirs-csv` — Auto-generate jobs CSV from directory structure
- `scan-files` — Scan a tree for primary input files plus optional secondary attachments, render each job's command from its own file (`{{file}}`, `{{base}}`, `{{ext}}`, `{{dir}}`, `{{index}}`), and optionally generate a jobs CSV from a template. Each job uploads only its own primary plus secondary files. `--recursive` searches subfolders too, as `**/` in front of the pattern does
- `plan` — Validate pipeline (dry-run)
- `resume` — Resume interrupted pipeline from state file. Failure markers belonging to stages being retried are cleared, so a run that failed at tar and then resumed cleanly reports success instead of the previous run's failures. A job whose tar and upload both succeeded keeps its submit failure, which is a real unretried outcome
- `submit-existing` — Submit jobs using previously uploaded files

### GUI PUR Tab
- Three-step workflow: configure → scan → execute
- Jobs come from a folder scan, a file scan, or a parameter sweep built in the tab with a live case preview
- Load/Save settings (CSV, JSON, SGE formats)
- Common input files from **Add Files**, **Add Folder** or **Browse Rescale Library**, listed as in Single Job
- Pipeline Settings (workers, tar options)
- Real-time monitoring dashboard with live progress
- Run queue: "Queue Run" when another run is active, auto-start on completion

---

## Configuration Management

**Command:** `rescale-int config [subcommand]`

### Commands
- `config init` — Interactive setup with a numbered platform menu (free-text URLs are not accepted). Refuses up front when stdin is not a terminal, rather than looping on the required API-key prompt. Writes to the `--config` file when one is given, does not echo the key, keeps an existing token file unless `--force`, and creates its directory `0700`
- `config show` — Display the merged configuration (file, environment, flags) and its precedence
- `config test` — Test API connection
- `config path` — Show the configuration file path

### Storage
`config.csv` is the single source of truth for all persistent settings. API keys are stored in a separate token file — `~/.config/rescale/token` on macOS and Linux, `%LOCALAPPDATA%\Rescale\Interlink\token` on Windows — with `0600` permissions. Keys are never written to `config.csv`.

---

## Discovery Commands

### Hardware
- `rescale-int hardware list [--search TERM] [--all] [--json]` — List available core types. Active types only by default; `--all` includes inactive/deprecated ones

### Software
- `rescale-int software list [--search TERM] [--versions] [--json]` — List available software packages (analyses), optionally with their versions

### Automations
- `rescale-int automations list [--json]` — List automations available to attach to jobs
- `rescale-int automations get --id ID [--json]` — Details for one automation

---

## CLI Behavior

Behavior that applies across the native CLI rather than to one command.

### Truthful Exit Codes
`0` on success, `1` on failure, `2` when the binary was not built with FIPS support. Code
`1` covers partial outcomes: a batch that transferred some files and failed others exits
non-zero, `pur run` fails when any job in the pipeline failed, and choosing **Abort** at a
prompt stops the batch rather than continuing through the rest of the files. A cancelled
PUR run is not a failure; a cancelled download or `files upload` is.

### Visible, Bounded Retries
Transient storage and API failures are retried automatically and reported from the second
attempt onward, naming the operation, attempt number, cause class, and next backoff.
Progress bars carry a matching `(retry N)` label. Each operation has a 90-second
wall-clock retry budget, reported per layer: storage transfers fail with `retries
exhausted after 1m28s (limit 1m30s, 6 attempt(s))` and the failing operation's own error;
API calls fail with `retry budget (1m30s) spent after 1m52s elapsed`, quoting the status
and up to 200 bytes of the server's own response so the platform's explanation survives.
The budget is checked between attempts and cannot cut short an attempt already in flight,
so one slow attempt can push the reported elapsed time past the budget. `Retry-After`
values longer than the client's cap are clamped and the discrepancy is reported.

### Bar-Safe Diagnostic Output
While `mpb` is drawing it owns the terminal, so anything written alongside it lands inside
the frame. Everything with something to say during a transfer now goes through `mpb`'s own
writer, which interleaves whole lines above the bars: the standard logger's transfer
diagnostics, retry and rate limit notices, resume-state hints from batch workers, and
interactive prompts. `--verbose` / `--debug` / `RESCALE_DEBUG` decide whether the
diagnostics are shown at all; rate limit and API-key notices bypass the discard so they
always survive. Piped output is unchanged.

### Prompts That Cannot Lie
Every interactive prompt refuses up front when stdin is not a terminal, naming the flag
that answers the question instead of surfacing an `EOF` from a worker goroutine.
Destructive confirmations that read `EOF` used to print "Cancelled" and exit `0`, which
was indistinguishable from a completed delete.

### Rate Limit Degraded Mode
When the cross-process coordinator is unreachable, each scope drops to an emergency cap
and says so once per transition, with a matching notice on reconnect.

---

## GUI Features

### Tabs

Seven tabs, in order:

1. **Setup Tab**: API configuration, proxy settings, logging configuration, auto-download daemon management. Surfaces the daemon's most recent error with its age and an actionable hint.
2. **Single Job Tab**: Job template builder with three input modes (directory, local files, remote files). Tar options for directory mode. A Back button between step one and step two returns to the first step without losing what was entered. The tag field accepts multiple comma-separated tags, and values typed without blurring are still captured on save and on template reload. Form state persists across tab navigation.
3. **PUR Tab**: Batch job pipeline with view modes (choice screen, monitoring, configuration), pipeline settings, run queue. Jobs come from a folder scan, a file scan, or a parameter sweep built in the tab with a live case preview
4. **Job Status Tab**: Paginated listing of your jobs with status badges, a name filter, and manual refresh. Pages forward with "Load next"; Refresh is disabled while a page load is in flight, and a tab-switch refresh that supersedes an in-flight page does not wedge the control.
5. **File Browser Tab**: Two-pane local/remote browser with upload, download, and delete operations. The remote pane offers four browse modes — My Library, My Jobs, Legacy, and Trash. My Library and My Jobs support server-side search within a folder; Legacy adds an owner filter (any / mine / shared with me), server-side sorting by name, size, or created date, and Type / Owner / Created columns. Filters are scoped per mode and cleared on mode switch. A search or listing failure is reported rather than rendered as an empty library. Trash shows soft-deleted entries with restore/purge actions; Upload is disabled in Trash and My Jobs with an explicit "N/A in this view" reason. An optional setting, **Download jobs without Input/Output split** (`flatten_job_download`), puts a downloaded job folder's files directly under the job folder, as auto-download lays them out.
6. **Transfers Tab**: Transfer progress with batch grouping (folder ops, PUR, single-job collapse into single rows), cancel/retry, filter chips, disk space error banner. A cancelled batch reads as cancelled end to end rather than as a clean completion, and storage retries surface on the rows and in the Activity log. Daemon auto-download rows appear inline with a `Daemon` badge and support per-row Cancel/Retry via IPC.
7. **Activity Logs Tab**: Logs with level filtering (DEBUG/INFO/WARN/ERROR), run history with expandable job tables

### Transfer Grouping
Bulk operations collapse into single aggregate batch rows instead of showing thousands of individual rows:
- Folder uploads/downloads use enumeration ID as batch ID
- PUR pipeline generates `pur_<timestamp>` batch ID
- Single-Job generates `job_<timestamp>` batch ID
- File Browser selections of 50 files or more generate `fb_upload_<timestamp>` / `fb_download_<timestamp>` batch IDs; smaller selections stay as individual rows
- Expand to see paginated individual tasks (50 per page). Paging filters and slices in one pass, cloning only the rows on the requested page, so asking for 50 rows of a 20,000-file batch does not stall progress updates behind the query
- Batch-level cancel and retry, with the cancelled count carried through to the row even when the tab was in the background while the batch was cancelled

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
All transfers (CLI, GUI, daemon) converge to single entry points (`upload.UploadFile()`, `download.DownloadFile()`) and share the same provider factory, orchestration layer, and upload planner.

### Streaming Encryption
Default mode encrypts on-the-fly during upload with AES-256-CBC chained across parts: a
single key and IV are stored in cloud metadata, part N's IV is the last ciphertext block
of part N-1, and PKCS7 padding is applied only to the final part. The combined ciphertext
is identical to whole-file AES-256-CBC, which is what lets the Rescale platform decrypt
it. No temporary encrypted file needed; constant ~16KB memory regardless of file size.

The v3.1.x per-part HKDF-SHA256 format is still readable for backward compatibility but is
not used for new uploads.

### Batch Abstraction
`RunBatch[T]` and `RunBatchFromChannel[T]` provide unified execution for all transfer paths. Adaptive concurrency computed from median file size.

### Two-Layer Concurrency
- **Layer 1**: Batch concurrency — how many files transfer simultaneously, adaptive within the `--max-concurrent` cap (1–20). `folders upload-dir` / `download-dir` raise the cap to 20 when it is unset; per-file commands default to 5
- **Layer 2**: Per-file multi-threading — each file gets threads from a shared pool based on size

### Retry Visibility
`cloud.RetryObserver` carries the caller's retry-visibility hooks down to the provider
clients, where retries actually happen several layers below the caller. Each entry point
renders them where the user is looking: the CLI through the progress writer, PUR through
the pipeline log, and the GUI onto the event bus for the Activity log and the batch rows.

### Resume Support
- **Upload**: `<file>.upload.resume` records the backend upload and its accepted parts; a rerun resumes while the source is unchanged and the record is under seven days old (see [Upload](#upload))
- **Download**: `.download.resume` JSON files with byte-offset HTTP Range resume, for legacy-format objects only. The current v2 CBC format restarts from zero

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
Reports redact storage signatures and keys, authorization values, hex tokens, URL params, emails and home paths, and timeline entries keep file names only; the same credential redaction applies to every error, log line and GUI message. Only server errors (5xx) and unclassified internal errors generate reports — auth, network, timeout, disk space, client-error, local filesystem, cancellation, and rate-limit failures are excluded, as are CLI usage errors and batch roll-ups whose individual failures were already reported. The report directory keeps the newest 500 files.

### API Key Security
Token file with `0600` permissions. Keys never logged or written to config.csv. State files with sensitive data use `0600` permissions.

### Sleep Prevention
OS sleep/suspend inhibited during transfers: IOPMAssertion (macOS), SetThreadExecutionState (Windows), systemd-inhibit (Linux).

See [SECURITY.md](SECURITY.md) for complete security documentation.

---

## Performance

### Rate Limiting
Token bucket algorithm with a cross-process coordinator per user (Unix socket / named pipe). Four scopes, each 85% of the platform's hard limit: `user` (1.7 req/sec), `job-submission` (0.236 req/sec), `jobs-usage` (21.25 req/sec) and `credential-access` (21.25 req/sec, storage credential requests). 429 feedback loop propagates cooldowns across all processes. When the coordinator is unreachable, each scope falls back to an emergency cap of one-eighth its hard rate and announces the transition.

### Adaptive Concurrency
Dynamic scaling based on file size distribution: <100MB → up to 20 workers, 100MB–1GB → up to 10, >1GB → up to 5. Bounded by the command's `--max-concurrent` cap, and validated against thread pool and memory constraints.

### FileInfo Enrichment
Folder listings parse full metadata. Downloads skip per-file `GetFileInfo()` API calls. Eliminates hours of overhead for large folders.

### Connection Reuse
HTTP connection pooling across all operations in a batch: 512 total idle connections, 100 idle and 100 total per host.

### Streaming Scan-to-Download
GUI folder downloads start downloading within seconds of scan initiation. 8 concurrent subfolder workers emit files to a channel consumed by the download pipeline.

### Page Size Enforcement
Recursive folder enumeration requests `page_size=1000` (API maximum), reducing pagination calls ~40x. Job listings page by page number — the API accepts `limit`/`offset` and ignores them.

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
