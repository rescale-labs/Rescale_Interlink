# Rescale Interlink - Complete Feature Summary

**Version:** 4.7.7
**Build Date:** February 27, 2026
**Status:** Production Ready, FIPS 140-3 Compliant (Mandatory)

This document provides a comprehensive, verified list of all features available in Rescale Interlink.

---

## Table of Contents

- [Core Capabilities](#core-capabilities)
- [File Operations](#file-operations)
- [Folder Operations](#folder-operations)
- [Job Operations](#job-operations)
- [Background Service (Daemon)](#background-service-daemon)
- [PUR (Parallel Upload and Run)](#pur-parallel-upload-and-run)
- [Configuration Management](#configuration-management)
- [Hardware & Software Discovery](#hardware--software-discovery)
- [Security & Encryption](#security--encryption)
- [Performance Features](#performance-features)
- [User Experience](#user-experience)
- [Technical Infrastructure](#technical-infrastructure)

---

## Core Capabilities

### Dual Interface
- **CLI Mode** (default): Command-line interface for automation and scripting
- **GUI Mode** (`--gui` flag): Graphical interface for interactive job monitoring
- **Source:** `cmd/rescale-int/main.go:24-34`

### Supported Platforms
- macOS (darwin/arm64, darwin/amd64)
- Linux (amd64)
- Windows (amd64)
- **Source:** Build targets in Makefile/build scripts

---

## File Operations

**Command:** `rescale-int files [subcommand]`
**Source:** `internal/cli/files.go`

### Upload Files
```bash
rescale-int files upload <file1> [file2 ...] [--folder-id ID]
# Shortcut: rescale-int upload <files>

# Legacy mode (compatible with Python client):
rescale-int files upload <file> --pre-encrypt
```

**Features:**
- Single or multiple file upload
- Upload to specific folder with `--folder-id`
- **Streaming encryption** (v3.0.0): Encrypts on-the-fly during upload, no temp file needed
- Multi-part upload for files >100MB
- Automatic resume on interruption (Ctrl+C)
- Progress bars with transfer speed and ETA
- Support for both S3 and Azure storage backends
- **Seekable upload streams** (v4.6.4): S3 upload progress reader implements `io.ReadSeeker` so AWS SDK can rewind on transient network errors; reader created fresh per retry attempt
- **Source:** `internal/cli/files.go:45-160`, `internal/cloud/upload/`, `internal/cloud/providers/s3/streaming_concurrent.go`

**Encryption Modes (v3.0.0):**
- **Default (streaming)**: Per-part AES-256-CBC encryption during upload
  - No temporary encrypted file (saves disk space for large files)
  - HKDF-SHA256 key derivation per part (FIPS 140-3 compliant)
  - Cloud metadata: `formatVersion=1`, `fileId`, `partSize`
- **Legacy (`--pre-encrypt`)**: Full-file encryption before upload
  - Creates temporary `.encrypted` file
  - Compatible with older Rescale clients
  - Cloud metadata: `formatVersion=0` or absent

**Verified Technical Details:**
- Multipart upload threshold: Files ≥100MB use multipart/block upload for better performance and resume capability
- Single-part upload: Files <100MB use simpler single-upload for efficiency
- **No file size limit** - Rescale Interlink supports files of any size
- Source: `internal/constants/app.go`
- Part size: 32MB chunks (`internal/cloud/upload/`)
- Resume state saved to `.upload.resume` files
- Encryption: AES-256-CBC with PKCS7 padding (`internal/crypto/encryption.go`, `internal/crypto/streaming.go`)

### Download Files
```bash
rescale-int files download <file-id1> [file-id2 ...] [-o output-dir]
# Shortcut: rescale-int download <file-ids>
```

**Features:**
- Single or multiple file download
- Automatic decryption after download
- Chunked/concurrent download for files ≥100MB (better performance)
- **No file size limit** - supports files of any size
- Progress bars during download and decryption
- Download interruption recovery: full byte-offset resume via HTTP Range requests, state tracked via JSON sidecar files (`.download.resume`). Decryption starts from beginning (AES-CBC constraint) but happens automatically once encrypted file is complete.
- Concurrent chunk downloads for large files
- **Source:** `internal/cli/files.go:162-286`, `internal/cloud/download/`

**Verified Technical Details:**
- Download threshold: 100MB (`internal/cloud/download/constants.go`)
- Chunk size: 32MB (`internal/cloud/download/s3_concurrent.go:44`)
- Resume state:  `.encrypted` file detection with size validation
- Decryption message added in v2.3.0 (`internal/cloud/download/s3_concurrent.go:458`)
- Resume logic fixed in v2.3.0: accounts for 1-16 byte PKCS7 padding (`internal/cli/download_helper.go:163-186`)

### List Files
```bash
rescale-int files list
```

**Features:**
- List all files in library with metadata
- Shows file ID, name, size, upload date
- **Source:** `internal/cli/files.go:288-338`

### Delete Files
```bash
rescale-int files delete <file-id1> [file-id2 ...]
```

**Features:**
- Delete one or more files from Rescale
- Confirmation prompt (unless using force flag)
- **Source:** `internal/cli/files.go:340-410`

---

## Folder Operations

**Command:** `rescale-int folders [subcommand]`
**Source:** `internal/cli/folders.go`

### Create Folder
```bash
rescale-int folders create <folder-name>
```

**Features:**
- Create new folder in library
- Returns folder ID for use in other commands
- **Source:** `internal/cli/folders.go:45-95`

### List Folders
```bash
rescale-int folders list
```

**Features:**
- List all folders with metadata
- Shows folder ID, name, parent, creation date
- **Source:** `internal/cli/folders.go:97-145`

### Upload Directory
```bash
rescale-int folders upload-dir <local-path> [--folder-id ID] [--exclude pattern]
```

**Features:**
- Recursive directory upload
- Preserve directory structure
- Exclude patterns (glob-style)
- Concurrent file uploads with progress tracking
- Smart conflict handling (skip/overwrite/rename options)
- Resume capability for interrupted uploads
- **Source:** `internal/cli/folders.go:147-305`, `internal/cli/folder_upload_helper.go`

**Verified Technical Details:**
- Uses file-level concurrency (multiple files uploaded in parallel)
- Each file gets its own progress bar
- Conflict detection: checks existing files by name before upload
- Pattern matching: Uses filepath.Match for exclude patterns

### Download Directory
```bash
rescale-int folders download-dir <folder-id> [-o output-dir] [--include pattern]
```

**Features:**
- Recursive folder download
- Recreate directory structure locally
- Include patterns for selective download
- Concurrent file downloads
- Progress tracking per file
- **Source:** `internal/cli/folders.go:307-450`, `internal/cli/folder_download_helper.go`

### Delete Folder
```bash
rescale-int folders delete <folder-id>
```

**Features:**
- Delete folder and all contents
- Confirmation prompt
- **Source:** `internal/cli/folders.go:452-510`

---

## Job Operations

**Command:** `rescale-int jobs [subcommand]`
**Source:** `internal/cli/jobs.go`

### List Jobs
```bash
rescale-int jobs list [--status STATUS] [--limit N]
# Shortcut: rescale-int ls
```

**Features:**
- List all jobs with filtering
- Filter by status (pending, executing, completed, etc.)
- Limit number of results
- Shows job ID, name, status, created date
- **Source:** `internal/cli/jobs.go:45-130`

### Get Job Details
```bash
rescale-int jobs get <job-id>
```

**Features:**
- Get detailed job information
- Shows status, command, compute resources, timing
- JSON output option with `--verbose`
- **Source:** `internal/cli/jobs.go:132-195`

### Submit Job
```bash
rescale-int jobs submit --script <path> --files <file-ids> [flags]
```

**Features:**
- Submit job from SGE-style script
- Automatic file upload with encryption (-E flag)
- Specify core type, walltime, and other job parameters
- **Source:** `internal/cli/jobs.go:197-380`

**Verified Flags:**
- `--script`: Path to job script
- `--files`: Input file IDs (comma-separated)
- `-E, --encrypt-files`: Auto-upload and encrypt files
- `--core-type`: Core type code
- `--cores-per-slot`: Number of cores per slot
- `--slots`: Number of slots
- `--walltime`: Maximum runtime (hours)
- `--automation`: Automation ID(s) to attach (can specify multiple)

### GUI Single Job Tab (v4.0.0+ Wails)

The GUI Single Job tab supports three input modes:
- **Directory mode**: Tar and upload a local directory as job input. Includes inline tar options (exclude/include patterns, compression, flatten) added in v4.7.1.
- **Local Files mode** (v4.6.8): Upload individual local files, then create the job with those file IDs
- **Remote Files mode** (v4.6.8): Use already-uploaded Rescale file IDs directly (skips tar/upload)

All three modes support the full job configuration: software, hardware, command, walltime, automations, license settings, extra input files, and submit mode (create-only or create-and-submit).

**v4.7.3 Enhancements:**
- Form state persists across tab navigation via `singleJobStore` (Zustand store)
- Submit becomes "Queue Job" when another run is active
- Job status displayed from `runStore.activeRun` after submission
- Cancel and "Start Over" actions integrated with `runStore`

**Source:** `internal/wailsapp/job_bindings.go:612-681`, `frontend/src/components/tabs/SingleJobTab.tsx`, `frontend/src/stores/singleJobStore.ts`

### Stop Job
```bash
rescale-int jobs stop <job-id>
```

**Features:**
- Stop a running or queued job
- Graceful termination
- **Source:** `internal/cli/jobs.go:382-430`

### Tail Job Logs
```bash
rescale-int jobs tail <job-id> [--interval SECONDS]
```

**Features:**
- Real-time log streaming
- Configurable polling interval
- Follows job status changes
- **Source:** `internal/cli/jobs.go:432-520`

### List Job Output Files
```bash
rescale-int jobs listfiles <job-id>
```

**Features:**
- List all output files from completed job
- Shows file IDs for download
- Uses optimized v2 API endpoint (v2.4.7+)
- **12.5x faster** than v3 endpoint (20 req/sec vs 1.6 req/sec)
- **Source:** `internal/cli/jobs.go:522-580`, `internal/api/client.go`

### Download Job Outputs
```bash
rescale-int jobs download <job-id> [-o output-dir] [--include pattern]
```

**Features:**
- Download all job output files
- Automatic decryption
- Progress tracking
- Selective download with include patterns
- **Highly Optimized (v2.4.7-2.4.8):**
  - Uses v2 API for file listing (12.5x faster)
  - Zero GetFileInfo calls (v2.4.8) - saves ~3 min per 289 files
  - Downloads now limited by S3/Azure speed, not API calls
- **Source:** `internal/cli/jobs.go:582-720`, `internal/cli/download_helper.go`

**Performance Improvement Details:**
- **v2.4.6**: ~188 seconds API overhead (289 GetFileInfo calls @ 1.6 req/sec)
- **v2.4.7**: ~181 seconds API overhead (faster listing, still GetFileInfo calls)
- **v2.4.8**: <1 second API overhead (v2 listing + zero GetFileInfo)
- **Improvement**: 99% reduction in API overhead

### Delete Jobs
```bash
rescale-int jobs delete <job-id1> [job-id2 ...]
```

**Features:**
- Delete one or more completed jobs
- Confirmation prompt
- **Source:** `internal/cli/jobs.go:722-790`

---

## Background Service (Daemon)

**Command:** `rescale-int daemon [subcommand]`
**Source:** `internal/cli/daemon.go`, `internal/daemon/`
**Added:** v3.4.0

Background service for automatically downloading completed jobs. Useful for automated workflows where results need to be downloaded without manual intervention.

### Run Daemon
```bash
rescale-int daemon run --download-dir ./results [flags]
```

**Flags:**
- `--download-dir` - Directory to download job outputs to
- `--poll-interval` - How often to check for completed jobs (default: 5m)
- `--name-prefix` - Only download jobs with names starting with this prefix
- `--name-contains` - Only download jobs containing this string
- `--exclude` - Exclude jobs matching these prefixes (can specify multiple)
- `--max-concurrent` - Maximum concurrent file downloads per job (default: 5)
- `--once` - Run once and exit (useful for cron jobs)

**Features:**
- Automatic polling for completed jobs
- Persistent state tracking (downloaded/failed jobs)
- Job name filtering (prefix, contains, exclude)
- Output directories include job ID suffix to prevent collisions
- Graceful shutdown on Ctrl+C
- Integration with existing download infrastructure (checksums enabled)
- **Source:** `internal/daemon/daemon.go`, `internal/daemon/monitor.go`, `internal/daemon/state.go`

### Daemon Status
```bash
rescale-int daemon status
```

Shows daemon state: last poll time, downloaded count, failed count.

### List Downloads
```bash
rescale-int daemon list [--failed] [--limit N]
```

Lists downloaded or failed jobs from the state file.

### Retry Failed
```bash
rescale-int daemon retry --all
rescale-int daemon retry --job-id <id>
```

Marks failed jobs for retry on the next poll cycle.

**State File Location:** `~/.config/rescale/daemon-state.json`

---

## PUR (Parallel Upload and Run)

**Command:** `rescale-int pur [subcommand]`
**Source:** `internal/cli/pur.go`, `internal/pur/pipeline/`

### Run Pipeline
```bash
rescale-int pur run --config <csv> --jobs-csv <csv> --state <csv>
# Shortcut: rescale-int run [args]
```

**Features:**
- Batch job submission from CSV files
- Multi-part directory support with pattern matching
- Automatic file upload with encryption
- Job submission with parameterization
- State management for resume capability
- Concurrent job processing
- **Submit mode normalization** (v4.6.0): `NormalizeSubmitMode()` maps all GUI/CLI mode strings (`"create_and_submit"`, `"yes"`, `"true"`, `"submit"`, `"draft"`, `"create_only"`) to canonical `"submit"` or `"create_only"` values
- **Context-aware cancellation** (v4.6.0): All pipeline workers (tar, upload, job) use `select` on `ctx.Done()` for responsive cancel
- **Tar Subpath** (v4.6.0): Tar only a subdirectory within each matched `Run_*` directory, with path traversal guard
- **Scan Prefix** (v4.6.0): Navigate into a subdirectory before scanning for `Run_*` patterns (renamed from "Run Subpath" for clarity)
- **Readable tar naming** (v4.6.0): Archive filenames use last 1-2 path components plus FNV-32a hash suffix
- **Source:** `internal/cli/pur.go:45-230`, `internal/pur/pipeline/pipeline.go`

**Verified Configuration:**
- `--config`: Pipeline configuration CSV (core types, walltime, etc.)
- `--jobs-csv`: Jobs to submit (paths, patterns, parameters)
- `--state`: State file for tracking progress
- Pattern support: `Run_*`, `Project_*/Run_*`, custom glob patterns

### Make Directories CSV
```bash
rescale-int pur make-dirs-csv --template <csv> --output <csv> --pattern "Run_*"
```

**Features:**
- Auto-generate jobs CSV from directory structure
- Pattern-based run directory discovery
- Template-based job creation
- **Source:** `internal/cli/pur.go:232-340`

**Verified Patterns:**
- Simple patterns: `Run_*`, `Sim_*`
- Nested patterns: `Project*/Run_*`
- Subpath support: Access files in subdirectories

### Plan Pipeline
```bash
rescale-int pur plan --config <csv> --jobs-csv <csv>
```

**Features:**
- Validate pipeline before execution
- Dry-run mode: shows what would be uploaded/submitted
- Directory validation
- File discovery preview
- **Source:** `internal/cli/pur.go:342-450`

### Resume Pipeline
```bash
rescale-int pur resume --state <csv>
```

**Features:**
- Resume interrupted pipeline from state file
- Skip completed jobs
- Continue from last checkpoint
- **Source:** `internal/cli/pur.go:452-520`

### Submit with Existing Files
```bash
rescale-int pur submit-existing --config <csv> --jobs-csv <csv> --state <csv>
```

**Features:**
- Submit jobs using previously uploaded files
- Skip re-upload step
- Useful for resubmitting with different parameters
- **Source:** `internal/cli/pur.go:522-620`

---

## Configuration Management

**Command:** `rescale-int config [subcommand]`
**Source:** `internal/cli/config_commands.go`

### Set Configuration
```bash
rescale-int config set <key> <value>
```

**Features:**
- Set API key, API URL, default settings
- Persistent configuration storage
- **Source:** `internal/cli/config_commands.go:45-110`

**Verified Keys:**
- `api_key`: Rescale API key
- `api_url`: API base URL (default: https://platform.rescale.com)
- `max_threads`: Default max threads
- `parts_per_file`: Default parts per file

### Get Configuration
```bash
rescale-int config get [key]
```

**Features:**
- View current configuration
- Show all settings or specific key
- **Source:** `internal/cli/config_commands.go:112-160`

### List Configuration
```bash
rescale-int config list
```

**Features:**
- List all configuration keys and values
- **Source:** `internal/cli/config_commands.go:162-200`

---

## Hardware & Software Discovery

### Hardware Discovery
```bash
rescale-int hardware list [--search TERM]
rescale-int hardware get <code>
```

**Features:**
- List available core types
- Search by name or code
- Get core type details (specs, pricing)
- **Source:** `internal/cli/hardware.go`

### Software Discovery
```bash
rescale-int software list [--search TERM]
rescale-int software get <code>
```

**Features:**
- List available software packages
- Search software catalog
- Get software version details
- **Source:** `internal/cli/software.go`

---

## Security & Encryption

### Proxy Support (v2.4.2, enhanced v4.5.9+)
**Implementation:** `internal/http/proxy.go`

**Enterprise-Ready Network Configuration:**
- **All traffic** (API calls + S3/Azure storage) routes through configured proxy
- Achieves feature parity with Python PUR implementation
- Critical for enterprise environments with strict network policies

**Supported Proxy Modes:**
- **no-proxy**: Direct connection (default)
- **system**: Use system environment proxy settings (HTTP_PROXY, HTTPS_PROXY, NO_PROXY)
- **basic**: Basic authentication with username/password
- **ntlm**: NTLM authentication for corporate proxies (Windows integrated auth)

**Key Features:**
- Proxy warmup for NTLM/Basic modes to establish authentication
- Automatic retry on proxy timeout with fresh authentication
- **NO_PROXY bypass rules** (v4.5.9): Fully wired to HTTP transport via `proxyFuncWithBypass()` using Go's `httpproxy` package. Supports wildcard domains, CIDR ranges, exact hostnames, and comma-separated multi-pattern lists. Configurable from the GUI Setup tab.
- Works for all operations: file upload/download, folder operations, job submission, API calls
- **Source:** `internal/http/proxy.go`

**Configuration:**
- GUI: Setup Tab → Proxy Configuration section
- CLI: Config file (`proxy_mode`, `proxy_host`, `proxy_port`, `proxy_user`, `proxy_password`)

**Benefits:**
- Network policy compliance (all traffic auditable via proxy logs)
- Security monitoring by IT teams
- Works in environments blocking direct S3/Azure access
- Matches Python PUR proxy behavior exactly

### Encryption Details
**Implementation:** `internal/crypto/encryption.go`

**Verified Specifications:**
- **Algorithm:** AES-256-CBC (line 15-18)
- **Key Size:** 256-bit (32 bytes)
- **IV Size:** 128-bit (16 bytes)
- **Padding:** PKCS7 (line 69-90)
- **Chunk Size:** 16KB for streaming (line 16)
- **Hash Function:** SHA-512 for file integrity (line 53-67)

**Process:**
1. Generate random 256-bit key and 128-bit IV
2. Encrypt file locally using AES-256-CBC
3. Upload encrypted file to S3/Azure (via proxy if configured)
4. Store encryption key with file metadata
5. Download encrypted file (via proxy if configured)
6. Decrypt locally using stored key

**Streaming Implementation (v2.3.0):**
- Encryption: Processes file in 16KB chunks, writes encrypted data immediately
- Decryption: Streams decryption, only unpads final chunk
- Memory usage: Constant ~16KB regardless of file size
- **Source:** `internal/crypto/encryption.go:93-173, 177-264`

### Token File Security (v2.5.0)
**Implementation:** `internal/config/csv_config.go`

**Permission Validation:**
- Warns when token files have overly permissive permissions (readable by group or others)
- Recommends `chmod 600` for token files containing API keys
- Checks file mode on Unix systems before reading token content
- **Source:** `internal/config/csv_config.go:364-377`

**Warning Message:**
```
Warning: Token file <path> has insecure permissions <mode>. Consider using 'chmod 600 <path>'
```

### Buffer Pool Security (v2.5.0)
**Implementation:** `internal/util/buffers/pool.go`

**Memory Clearing:**
- All buffer pools (32MB chunk buffers, 16KB encryption buffers) clear data before returning to pool
- Prevents sensitive data from persisting in memory between operations
- Uses Go's `clear()` builtin for efficient zeroing
- **Source:** `internal/util/buffers/pool.go:50-56, 72-80`

---

## Performance Features

### Rate Limiting (Token Bucket + Cross-Process Coordinator)

**What it is**: Intelligent API throttling with cross-process coordination that prevents hitting Rescale's hard rate limits while maximizing throughput — even when GUI, daemon, and CLI run simultaneously.

**How it works:**
- Token bucket algorithm with per-scope rate limiters and configurable burst capacity
- **Cross-process coordinator** (Unix socket / named pipe) ensures GUI + daemon + CLI share a single budget
- **429 feedback loop**: CheckRetry callback drains + cools down across all processes instantly
- **Utilization-based visibility**: warns when operating above 60% of capacity (with hysteresis to prevent flickering)
- Three rate limiters for different API scopes:
  - **User Scope**: 1.7 req/sec (85% of 2 req/sec hard limit) - all v3 endpoints
  - **Job Submission**: 0.236 req/sec (85% of 0.278 req/sec limit) - POST /api/v2/jobs/{id}/submit/
  - **Jobs Usage**: 21.25 req/sec (85% of 25 req/sec limit) - v2 job query endpoints (v2.4.7+)

**Burst Capacity:**
- User scope: 150 tokens (~88 seconds of rapid operations)
- Job submission: 50 tokens (~212 seconds)
- Jobs usage: 300 tokens (~14 seconds)

**Key Benefits:**
- 15% safety margin prevents throttle lockouts; 429 feedback provides additional safety net
- Cross-process coordination prevents independent processes from exceeding limits
- Allows rapid burst operations at startup
- Automatic rate adjustment based on endpoint type
- Utilization-based notifications in CLI output and GUI Activity Logs

**Configuration:**
```bash
# Global flags (applied to all commands)
--max-threads 16        # Thread pool size for transfers (0=auto, max 32)
--parts-per-file 5      # Concurrent chunks per file (0=auto, max 10)
--no-auto-scale         # Disable dynamic thread adjustment
```

**Source:** `internal/ratelimit/`, `internal/ratelimit/coordinator/`, `internal/api/client.go`

### Transfer Grouping (v4.7.7)

**What it is**: Bulk file transfers (folder uploads/downloads, PUR pipeline uploads, Single-Job uploads) are collapsed into a single aggregate batch row in the GUI Transfers tab, replacing 10k+ individual rows with one collapsible summary.

**How it works:**
- Each bulk operation generates a `BatchID` that propagates to all individual transfer tasks
  - Folder uploads/downloads: use the enumeration ID as BatchID (natural group identifier)
  - PUR pipeline: generates `pur_<timestamp>` batch ID with "PUR: N jobs" label
  - Single-Job: generates `job_<timestamp>` batch ID with "Job: <name>" label
  - Individual file uploads from File Browser: ungrouped (shown as individual rows)
- Backend computes aggregate stats per batch in a single O(tasks) pass: total/queued/active/completed/failed, byte-weighted progress, aggregate speed
- Batch rows are collapsible — expand to show paginated individual tasks (50 per page)

**Event Optimization:**
- Individual `EventTransferProgress` events **suppressed at source** for batched tasks (queue layer skip)
- 1/sec aggregate `BatchProgressEvent` published per active batch (replaces 20k events/sec flood)
- Terminal events (completed, failed, cancelled) still published individually for accuracy
- Batch progress ticker auto-starts on first batch task, auto-stops when all tasks are terminal

**Polling Optimization:**
- Frontend polls: `GetTransferBatches()` (~200 bytes/batch) + `GetUngroupedTransferTasks()` + `GetTransferStats()`
- Replaces previous `GetTransferTasks()` which serialized all 10k+ tasks (~2MB) per 500ms poll cycle
- Expanded batch tasks fetched on-demand only when user clicks expand

**Batch Actions:**
- `CancelBatch(batchID)` — cancels all non-terminal tasks including queued tasks (standard `Cancel()` only handles active/initializing)
- `RetryFailedInBatch(batchID)` — retries all failed tasks in batch
- Source-label gating: cancel/retry only shown for FileBrowser-sourced batches (PUR/SingleJob manage their own lifecycle)

**Source:** `internal/transfer/queue.go`, `internal/wailsapp/transfer_bindings.go`, `frontend/src/stores/transferStore.ts`, `frontend/src/components/tabs/TransfersTab.tsx`

### Concurrent Uploads

**Default Behavior**: Uploads 5 files simultaneously with automatic multi-part chunking for large files.

**How it works:**
1. **File-level Concurrency**: Process multiple files in parallel (default: 5, max: 10)
2. **Chunk-level Concurrency**: Large files split into parts (default: auto, max: 10 parts/file)
3. **Dynamic Thread Allocation**: Thread pool shared across all active transfers
4. **Resource Manager**: Auto-scales threads based on file sizes and system resources

**Performance:**
- Small files (<10MB): Sequential upload within concurrent batch
- Large files (>100MB): Multi-part upload with concurrent chunks
- Typical speedup: **5-10x faster** than sequential uploads

**Conflict Handling:**
- **Fast Mode (default)**: Upload first, handle conflicts on error (1 API call/file)
- **Safe Mode** (`--check-conflicts`): Check existence before upload (1-2 API calls/file)

**Example:**
```bash
# Upload 10 files concurrently (default 5 workers)
rescale-int files upload *.dat

# Maximum concurrency (10 workers)
rescale-int files upload *.dat --max-concurrent 10

# Sequential uploads (1 at a time)
rescale-int files upload *.dat --max-concurrent 1

# Upload with conflict checking
rescale-int files upload *.dat --check-conflicts

# Control chunk-level parallelism for large files
rescale-int files upload large.tar.gz --parts-per-file 8
```

**Source:** `internal/cli/files.go`, `internal/cloud/upload/`, `internal/transfer/manager.go`

### Concurrent Downloads

**Default Behavior**: Downloads 5 files simultaneously with resume support for interrupted transfers.

**How it works:**
1. **Worker Pool**: 5 concurrent download workers (default, max: 10)
2. **Multi-part Downloads**: Large files downloaded in concurrent chunks
3. **Credential Caching**: Storage credentials cached for 10 minutes
4. **Resume State**: Saves progress for each file, can resume interrupted downloads
5. **Checksum Verification**: SHA-512 validation after download (can skip with `--skip-checksum`)

**Job Download Optimization (v2.4.7-2.4.8):**
- Uses v2 API for file listing (20 req/sec vs 1.6 req/sec = **12.5x faster**)
- Zero GetFileInfo calls (v2.4.8: uses metadata from listing = **~3 min saved per 289 files**)
- Direct metadata-to-download pipeline (no intermediate API calls)

**Performance:**
- Typical speedup: **10-100x faster** than sequential downloads
- 289-file job: ~1-2 minutes (vs ~10-20 minutes sequential)
- Limited by S3/Azure transfer speed, not API rate limits

**Resume Support:**
```bash
# Download with resume capability
rescale-int jobs download --id JOB_ID --resume

# Skip checksum verification (faster but not recommended)
rescale-int jobs download --id JOB_ID --skip-checksum

# Control concurrency
rescale-int jobs download --id JOB_ID --max-concurrent 10
```

**Example:**
```bash
# Download job outputs (optimized v2 API)
rescale-int jobs download --id abc123 --outdir ./results

# Download with filters
rescale-int jobs download --id abc123 --filter "*.dat,*.log"

# Exclude patterns
rescale-int jobs download --id abc123 --exclude "debug*,temp*"

# Resume interrupted download
rescale-int jobs download --id abc123 --resume

# Download folder preserving structure
rescale-int folders download-dir --folder-id xyz789 --outdir ./data
```

**Source:** `internal/cli/download_helper.go`, `internal/cloud/download/`, `internal/api/client.go`

### Multi-Threaded Transfers

**Auto-Scaling Algorithm:**
- Small files (<100MB): 1 thread (sequential)
- Medium files (100MB-1GB): 2-4 threads based on available bandwidth
- Large files (>1GB): Up to max-threads (default: auto-detect from CPU count)
- Maximum: 32 threads (configurable with `--max-threads`)

**Verified Settings:**
- `--max-threads N`: Set maximum concurrent threads (0 = auto-detect)
- `--no-auto-scale`: Disable auto-scaling, use fixed thread count
- `--parts-per-file N`: Parts per file for multi-part (0 = auto, max 10)

**Source:** `internal/transfer/manager.go`

### Progress Tracking
**Source:** `internal/progress/`, uses `github.com/vbauerster/mpb/v8`

**Features:**
- Real-time progress bars for uploads/downloads
- Transfer speed calculation (MB/s)
- ETA estimation
- Multi-file progress tracking (one bar per file)
- Proper terminal control (no ghost bars as of v2.3.0)

**v2.3.0 Fix:**
- All output routed through `io.Writer` to avoid bypassing mpb
- **Source:** Progress bar corruption fix across 17 files

### Resume Capability

**Upload Resume:**
- State file: `.rescale-upload-state` (JSON)
- Tracks: Uploaded parts, encryption key, IV, file hash
- Resume on: Ctrl+C, network failure, crash
- **Source:** `internal/cloud/upload/resume.go`

**Download Resume:**
- State file: `.rescale-download-state` (JSON)
- Tracks: Downloaded chunks, total size, encryption metadata
- Resume detection: Checks `.encrypted` file size against expected range
- **v2.3.0 Fix:** Accounts for 1-16 byte PKCS7 padding in size check
- **Source:** `internal/cloud/download/resume.go`, `internal/cli/download_helper.go:163-186`

---

## User Experience

### Progress Messages (v2.3.0)
- "Decrypting {file} (this may take several minutes for large files)..."
- Shown before decryption of large files to avoid perceived hang
- **Source:** `internal/cloud/download/s3_concurrent.go:458`, `azure_concurrent.go:483`

### Conflict Handling
- Interactive prompts for file conflicts
- Options: Skip, Overwrite, Rename, All (apply to all)
- Smart detection: Checks both encrypted and decrypted files
- **Source:** `internal/cli/download_helper.go`, `folder_upload_helper.go`

### Error Handling
- Clear error messages with context
- Disk space checks before operations (5% safety buffer)
- **Disk space error UX (v4.7.1)**: Amber banner in Transfers tab when downloads fail due to insufficient space, showing available/needed amounts. Short "No disk space" labels with full hover tooltips. Error classification via `classifyError()`/`extractDiskSpaceInfo()`.
- Resume state validation and cleanup
- Graceful degradation on network issues
- **Source:** `internal/diskspace/`, `internal/pur/storage/errors.go`, `frontend/src/stores/transferStore.ts`, `frontend/src/components/tabs/TransfersTab.tsx`

### GUI Enhancements (v2.6.0)

**File Browser Tab:**
- Two-pane design: local files (left) / remote Rescale files (right)
- Delete functionality for both local and remote files/folders
- Search/filter by filename (case-insensitive, real-time filtering)
- Confirmation dialogs before delete operations
- Transfer rate display for uploads/downloads (e.g., "2.5 MB/s")
- Proper button spacing/padding around navigation and action buttons
- **Source (v4.0.0+ Wails):** `frontend/src/components/tabs/FileBrowserTab.tsx`, `internal/wailsapp/file_bindings.go`

**File List Components (v4.0.0+ Wails):**
- React components with selection support
- Pagination support
- Filename truncation for long names
- **Source:** `frontend/src/components/` (React), `internal/wailsapp/file_bindings.go` (Go bindings)

**Status Display (v4.0.0+ Wails):**
- Unified status display via React components
- Activity indicators for operations in progress
- Thread-safe via Wails event bridge
- **Source:** `frontend/src/components/`, `internal/wailsapp/event_bridge.go`

**Dialog/Modal System (v4.0.0+ Wails):**
- React-based modals and dialogs
- Error display with details
- Confirmation dialogs for destructive operations
- **Source:** `frontend/src/components/` (React components)

### Run Session Persistence (v4.7.3)

**Central Run State Management:**
- `runStore` tracks active run, completed runs (max 20), queued job, and queue status
- App-level event listeners (`interlink:state_change`, `interlink:log`, `interlink:complete`) established in App.tsx
- Reconciliation polling supplements event-driven updates
- Active run metadata persisted to localStorage (`rescale-int-active-run`) for restart recovery
- **Source:** `frontend/src/stores/runStore.ts`

**PUR Tab View Modes:**
- `purViewMode`: `'auto' | 'monitor' | 'configure'` with computed `effectiveView`
- Choice screen when returning during active PUR run
- Read-only monitoring dashboard with live progress from `runStore.activeRun`
- Collapsible monitoring banner when configuring a new run
- Completed results view with success/failure summary and job table snapshot
- **Source:** `frontend/src/components/tabs/PURTab.tsx`

**Job Queue:**
- Submit button becomes "Queue Run"/"Queue Job" when a run is active
- Configuration deep-copied at queue time via `structuredClone()` to prevent mutation
- Auto-start with retry/backoff after current run completes (5 attempts, 500ms increments)
- Inline queue status banners with color-coded feedback
- **Source:** `frontend/src/stores/runStore.ts` (startQueuedJobWithRetry)

**Restart Recovery:**
- Reads `localStorage.getItem('rescale-int-active-run')` on app mount
- Checks engine state via `GetRunStatus()` — if running, re-establishes monitoring
- If idle: loads historical rows from disk via `GetHistoricalJobRows(runId)`
- Classifies final status from rows: completed, failed, or interrupted
- **Source:** `frontend/src/stores/runStore.ts` (recoverFromRestart)

**Activity Tab Run History:**
- Session-level completed runs with expandable job tables
- Historical runs loaded from disk via `GetRunHistory()` + `GetHistoricalJobRows()`
- Lazy-loading of historical job rows on click
- **Source:** `frontend/src/components/tabs/ActivityTab.tsx`

**Shared Widgets:**
- `JobsTable` — Reusable job rows table extracted from PURTab
- `StatsBar` — Run progress statistics
- `PipelineStageSummary` — Per-stage success/fail counts
- `PipelineLogPanel` — Scrollable pipeline log display
- `ErrorSummary` — Failed job error display
- `StatusBadge` — Color-coded status indicator
- **Source:** `frontend/src/components/widgets/`

---

## Technical Infrastructure

### Storage Backends
**Supported:** S3 (AWS), Azure Blob Storage

**Unified Backend Architecture (v3.1.0):**
- **Entry Points:** `internal/cloud/upload/upload.go`, `internal/cloud/download/download.go`
- **Provider Factory:** `internal/cloud/providers/factory.go`
- **Orchestration:** `internal/cloud/transfer/uploader.go`, `internal/cloud/transfer/downloader.go`

**S3 Implementation:**
- **Source:** `internal/cloud/providers/s3/` (5 files)
- Multi-part upload API
- Concurrent chunk download (range requests)
- Credential refresh via `EnsureFreshCredentials()`

**Azure Implementation:**
- **Source:** `internal/cloud/providers/azure/` (5 files)
- Block blob API
- Concurrent block upload/download
- Credential refresh via `EnsureFreshCredentials()`

### API Client
**Source:** `internal/api/client.go`

**Features:**
- RESTful API wrapper for Rescale platform API v3 and v2
- Automatic retry with exponential backoff
- Request/response logging in verbose mode
- Credential caching (5-minute TTL for user profile, 10-minute for storage creds)
- **Smart API Routing (v2.4.7):**
  - v3 endpoints: user scope (1.6 req/sec)
  - v2 job submission: job-submission scope (0.139 req/sec)
  - v2 job query: jobs-usage scope (20 req/sec)
- **Source:** `internal/cloud/credentials/manager.go`, `internal/ratelimit/`

### Proxy Support
**Source:** `internal/http/proxy.go`

**Features:**
- HTTP/HTTPS proxy configuration
- Environment variable support (HTTP_PROXY, HTTPS_PROXY)
- Proxy authentication (Basic and NTLM)
- NO_PROXY bypass rules (fully wired to HTTP transport in v4.5.9)

### Package Structure
```
internal/
├── api/          # Rescale API client
├── cli/          # CLI commands and helpers
├── cloud/        # Cloud storage (v3.1.0 unified backend)
│   ├── interfaces.go      # CloudTransfer, UploadParams, DownloadParams
│   ├── state/             # Resume state management
│   ├── transfer/          # Upload/download orchestration (8 files)
│   ├── providers/         # Provider implementations
│   │   ├── factory.go     # Provider factory
│   │   ├── s3/            # S3 provider (5 files)
│   │   └── azure/         # Azure provider (5 files)
│   ├── upload/            # Single entry point (upload.go)
│   ├── download/          # Single entry point (download.go)
│   ├── credentials/       # Credential management
│   └── storage/           # Storage utilities and errors
├── config/       # Configuration management
├── constants/    # Shared constants
├── core/         # Core utilities
├── crypto/       # AES-256 encryption (streaming + legacy)
├── diskspace/    # Disk space checking
├── events/       # Event-driven architecture
├── wailsapp/     # Wails v2 GUI bindings (v4.0.0+)
├── services/     # Framework-agnostic services (TransferService, FileService)
├── http/         # HTTP client and retry logic
├── logging/      # Logging utilities
├── models/       # Data structures
├── progress/     # Progress bar UI (mpb wrapper)
├── pur/          # PUR-specific functionality
│   ├── parser/   # CSV/config parsing
│   ├── pattern/  # File pattern matching
│   └── pipeline/ # PUR pipeline execution
├── ratelimit/    # Token bucket rate limiting + cross-process coordinator
├── transfer/     # Concurrent transfer manager
├── util/         # Buffer pools and utilities
└── validation/   # Input validation
```

---

## Version History

### v3.2.0 (December 1, 2025)
**Concurrent Streaming Download:**
- ✅ **Full concurrent streaming download** - Multi-threaded downloads for streaming (v1) format files
- ✅ **StreamingPartDownloader interface** - `GetEncryptedSize()` and `DownloadEncryptedRange()` methods
- ✅ **Parallel part decryption** - Each part independently decrypted with HKDF-derived keys
- ✅ **Worker goroutines** - Configurable thread count based on file size and system resources

**GUI Thread Safety:**
- ✅ **Upload progress UI fix** - `initProgressUIWithFiles()` now uses `fyne.Do()` for thread safety
- ✅ **Consistent with download UI** - Matches thread-safe pattern in `initProgressUIForDownloads()`

**Code Quality:**
- ✅ **Shared download helpers** - New `download_helpers.go` with reusable download functions
- ✅ **Version consistency** - All files updated to 3.2.0 headers
- ✅ **Dead code removal** - Removed commented debug code from `jobs_tab.go`

**GUI Improvements & Bug Fixes (November 30):**
- ✅ **JSON Job Template Support** - Load from JSON and Save as JSON buttons in Single Job Tab
- ✅ **SearchableSelect Fix** - Dropdown no longer appears when value is set programmatically
- ✅ **Fyne Thread Safety** - Fixed "Error in Fyne call thread" warnings in Activity Tab
- ✅ **Hardware Scan UX** - Scan button enables when any valid software code is entered
- ✅ **Dialog Sizing** - Configure New Job dialog enlarged (900×800) with text wrapping

**Source:** `internal/cloud/transfer/downloader.go`, `internal/cloud/transfer/download_helpers.go`, `internal/cloud/providers/s3/streaming_concurrent.go`, `internal/cloud/providers/azure/streaming_concurrent.go`, `internal/gui/file_browser_tab.go`

### v3.1.0 (November 29, 2025)
**Unified Backend Architecture:**
- Complete refactor of `internal/cloud/` package
- Single entry points: `upload/upload.go` and `download/download.go`
- Provider factory pattern: `providers.NewFactory().NewTransferFromStorageInfo()`
- Symmetric S3/Azure implementations with 5 identical interfaces:
  - `cloud.CloudTransfer`
  - `transfer.StreamingConcurrentUploader`
  - `transfer.StreamingConcurrentDownloader`
  - `transfer.LegacyDownloader`
  - `transfer.PreEncryptUploader`
- Shared orchestration layer in `transfer/` package (8 files)
- Centralized resume state in `state/` package

**Code Quality:**
- Eliminated 6,375 lines of duplicated code from old upload/download
- Proper separation: providers/, transfer/, state/ packages
- All `*_concurrent.go` files removed from upload/ and download/
- Provider independence verified (no imports from old packages)

**Source:** `internal/cloud/providers/`, `internal/cloud/transfer/`, `internal/cloud/state/`

### v2.6.0 (November 26, 2025)
**GUI Usability Improvements:**
- ✅ **File Browser pagination** - Default 40 items/page, configurable range 20-200
- ✅ **Transfer rate display** - Real-time transfer speed shown during uploads/downloads (e.g., "2.5 MB/s")
- ✅ **Button spacing/padding** - Proper spacing around navigation and action buttons
- ✅ **Filename truncation** - Long filenames truncate with ellipsis instead of overlapping size column
- ✅ **White list backgrounds** - File list panes now have white background instead of grey
- ✅ **Compact pagination UI** - Page controls use `< 1/1 >` format to minimize window width

**Source:** `internal/gui/file_browser_tab.go`, `internal/gui/file_list_widget.go`, `internal/gui/local_browser.go`, `internal/gui/remote_browser.go`

### v2.5.0 (November 22, 2025)
**CLI Usability Improvements:**
- ✅ **Short flags for all CLI commands** - All commands support single-letter short flags (e.g., `-j` for `--job-id`, `-s` for `--search`)
- ✅ **Hardware list default behavior change** - Shows only active hardware by default, use `-a/--all` for inactive types
- ✅ Aligned short flags with `rescale-cli` conventions where applicable

**Short Flag Examples:**
```bash
# Concise commands with short flags
rescale-int hardware list -s emerald -J     # Search, JSON output
rescale-int jobs download -j WfbQa -d ./out -w  # Job ID, output dir, overwrite
rescale-int files upload model.tar.gz -d abc123  # Folder ID
```

**Source:** `internal/cli/hardware.go`, `internal/cli/software.go`, `internal/cli/jobs.go`, `internal/cli/files.go`

### v2.4.9 (November 22, 2025)
**Security Improvements:**
- ✅ Removed credential persistence from config files (API keys, proxy passwords)
- ✅ Added `--token-file` flag for secure API key storage
- ✅ Added secure password prompting for proxy authentication

**Bug Fixes:**
- ✅ Fixed pipeline resource leak (defer-in-loop in uploadWorker)
- ✅ Fixed S3 context leak (defer-in-loop in uploadMultipart)
- ✅ Enhanced PKCS7 padding verification

**Source:** `internal/config/csv_config.go`, `internal/cli/root.go`, `internal/pur/pipeline/pipeline.go`

### v2.4.8 (November 20, 2025)
**Massive Download Performance Improvement:**
- ✅ **Eliminated GetFileInfo API calls for job downloads** - saves ~3 minutes per 289-file job
- ✅ Uses cached metadata from v2 ListJobFiles endpoint directly
- ✅ 99% reduction in API overhead (from ~188s to <1s for 289 files)
- ✅ Downloads now limited by S3/Azure transfer speed, not API calls

**Technical Changes:**
- Enhanced JobFile model to capture all metadata from v2 endpoint
- Created ToCloudFile() conversion method for clean abstraction
- Added DownloadFileWithMetadata() function that accepts CloudFile directly
- Modified job download flow to use cached metadata

**Source:** `internal/models/job.go`, `internal/cloud/download/download.go`, `internal/cli/download_helper.go`

### v2.4.7 (November 20, 2025)
**v2 API Support for Job Operations:**
- ✅ Switched ListJobFiles to v2 endpoint (12.5x faster rate limit)
- ✅ Added jobs-usage rate limiter (20 req/sec vs 1.6 req/sec)
- ✅ Smart API routing based on endpoint type

**Technical Changes:**
- Added jobs-usage scope constants and rate limiter
- Updated API client routing logic to select appropriate limiter
- Changed ListJobFiles from v3 to v2 endpoint

**Source:** `internal/ratelimit/constants.go`, `internal/ratelimit/limiter.go`, `internal/api/client.go`

### v2.4.6 (November 20, 2025)
**Rate Limiting and Upload Fixes:**
- ✅ Corrected rate limits to 80% of hard limits (better safety margin)
- ✅ Dual-mode upload (fast/safe) with conflict detection
- ✅ Fixed upload concurrency configuration

**Source:** `internal/ratelimit/`, `internal/cli/files.go`

### v4.7.7 (February 27, 2026)
**GUI Performance for Bulk Transfers + Rate Limit Validation:**
- ✅ Transfer grouping: folder uploads/downloads, PUR pipelines, Single-Job uploads collapse into aggregate batch rows (10k individual rows → 1 collapsible row)
- ✅ `BatchID`/`BatchLabel` fields added to `TransferRequest`, `TransferTask`, DTOs — propagated through all transfer paths
- ✅ `GetAllBatchStats()` — single-pass O(tasks) aggregate computation with byte-weighted progress
- ✅ `GetBatchTasks(batchID, offset, limit)` — paginated expansion of individual tasks within a batch
- ✅ `CancelBatch(batchID)` — cancels queued + active tasks (standard Cancel only handles active/initializing)
- ✅ `RetryFailedInBatch(batchID)` — retries all failed tasks in a batch
- ✅ `GetUngroupedTransferTasks()` — returns only tasks without a BatchID (avoids 10k-task IPC payload)
- ✅ Event suppression: `publishTransferEvent()` skips `EventTransferProgress` for batched tasks; terminal events still published
- ✅ Batch progress ticker: 1/sec per active batch, auto-starts/auto-stops based on task lifecycle
- ✅ `BatchProgressEvent` type + event bridge forwarding as `interlink:batch_progress`
- ✅ Frontend `BatchRow` component with `React.memo()`: collapsible aggregate progress, file count summary, cancel/retry (FileBrowser only)
- ✅ Frontend polling optimization: `fetchBatches()` + `fetchUngroupedTasks()` (~1KB/cycle) replaces `fetchTasks()` (~2MB/cycle)
- ✅ Rate limit end-to-end validation: 7 test scenarios pass (coordinator auto-spawn, no spawn on --version, concurrent sharing, sustained load enforcement, visibility messages, zero 429s, bucket state inspection)
- ✅ 8 new unit tests: TrackTransferWithBatch, GetAllBatchStats, GetBatchTasksPaginated, GetUngroupedTasks, CancelBatch, RetryFailedInBatch, BatchProgressSuppressesIndividual, BatchProgressTickerPublishes

**Source:** `internal/transfer/queue.go`, `internal/transfer/queue_test.go`, `internal/events/events.go`, `internal/wailsapp/transfer_bindings.go`, `internal/wailsapp/event_bridge.go`, `internal/wailsapp/file_bindings.go`, `internal/services/transfer_service.go`, `internal/pur/pipeline/pipeline.go`, `internal/core/engine.go`, `internal/wailsapp/job_bindings.go`, `frontend/src/stores/transferStore.ts`, `frontend/src/components/tabs/TransfersTab.tsx`, `frontend/src/types/events.ts`

### v4.7.6 (February 26, 2026)
**Auto-Download Reliability Fixes:**
- ✅ Service-mode API key resolution: checks per-user token path before falling back to SYSTEM's AppData (fixes silent "no API key" failures on Windows Service)
- ✅ Token persistence: GUI writes API key to token file before every daemon/service start
- ✅ Config propagation: every config save triggers `ReloadDaemonConfig()` — restarts subprocess daemon or triggers service rescan
- ✅ IPC ReloadConfig protocol: new `MsgReloadConfig` with active-download awareness (defers restart during active downloads)
- ✅ Pre-flight validation: enable toggle validates API key and download folder first, specific error messages on failure
- ✅ Progressive pending state: "Activating..." shows time-based messages (0-10s, 10-30s, 30s+) with Open Logs and Retry buttons
- ✅ Install & Start Service: combined idempotent CLI subcommand and GUI button — single UAC prompt
- ✅ Lookback fix: `getJobCompletionTime()` retries once; jobs with unknown completion time included
- ✅ IPC lifecycle: IPC server starts before daemon on Windows; timeout increased from 2s to 5s
- ✅ Skip-and-retry: users skipped for missing API key retried on each service rescan
- ✅ Tray auto-launch: GUI automatically launches tray companion on startup (Windows only)
- ✅ Daemon stderr surfacing: IPC timeout errors include last 3 lines from daemon-stderr.log

**Source:** `internal/wailsapp/daemon_bindings.go`, `internal/daemon/`, `internal/service/`, `internal/config/apikey.go`, `internal/ipc/`, `frontend/src/components/tabs/SetupTab.tsx`

### v4.7.5 (February 25, 2026)
**Empty File Fix + Cleanup:**
- ✅ Empty file upload: fixed crash when uploading 0-byte files through streaming encryption (zero parts → one empty encrypted part)
- ✅ Empty file download: fixed download validation rejecting legitimate 0-byte files (allows 0-byte results when DecryptedSize is 0)
- ✅ Dead code cleanup: removed unused ThroughputMonitor infrastructure from resource manager (collected at 5 sites, never read)
- ✅ Build artifact cleanup: excluded `.wixpdb` files from release workflow artifacts

### v4.7.4 (February 23, 2026)
**Unified Transfer Architecture:**
- ✅ PUR uploads visible in Transfers tab: pipeline workers delegate to `TransferService.UploadFileSync()` via new `SyncUploader` interface
- ✅ Single-Job uploads visible in Transfers tab: local file uploads route through TransferService
- ✅ Unified concurrency: all uploads share TransferService's semaphore regardless of entry point
- ✅ Source label badges: "PUR" (blue) and "Job" (green) in Transfers tab; cancel/retry hidden for pipeline-managed transfers
- ✅ Cancel fix: execution paths now create derived `context.WithCancel()` and register cancel function (previously only updated state)
- ✅ Cancel-state race: `context.Canceled` detected to prevent cancelled state being overwritten with "failed"
- ✅ TransferHandle leak: all paths now call `defer transferHandle.Complete()`
- ✅ Tags on upload: File Browser upload dialog includes optional tags input with live chip preview; also available via CLI
- ✅ Tarball deletion safety: `safeRemoveTar()` with 5 guardrails (canonical path, under tempDir, regular file, tar extension, FNV hash pattern)

**Source:** `internal/services/transfer_service.go`, `internal/core/engine.go`, `internal/pur/pipeline/pipeline.go`, `internal/wailsapp/job_bindings.go`, `internal/wailsapp/file_bindings.go`, `frontend/src/components/tabs/TransfersTab.tsx`

### v4.7.3 (February 22, 2026)
**Run Session Persistence and Monitoring:**
- ✅ New `runStore.ts`: Central run state manager with app-level event listeners, active run tracking, queue, restart recovery
- ✅ New `singleJobStore.ts`: Single Job form state persisted across tab navigation via Zustand
- ✅ PUR view modes: choice screen on return during active run, monitoring dashboard, progress banner, completed results view
- ✅ Job queue: "Queue Run"/"Queue Job" when a run is active, with retry/backoff auto-start (500ms, 1s, 1.5s, 2s, 2.5s)
- ✅ App restart recovery: localStorage persistence + historical state file loading from disk
- ✅ Active run indicator in footer status bar
- ✅ Activity tab run history: session-level completed runs + historical state file loading via `GetRunHistory()`/`GetHistoricalJobRows()`
- ✅ Shared widget extraction: `JobsTable`, `StatsBar`, `PipelineStageSummary`, `PipelineLogPanel`, `ErrorSummary`, `StatusBadge` in `widgets/`
- ✅ Shared utility extraction: `computeStageStats`, `formatDuration`/`formatDurationMs` in `utils/`
- ✅ Shared type extraction: `types/jobs.ts` (breaks import cycle), `types/run.ts` (ActiveRun, CompletedRun, QueuedJob)
- ✅ jobStore refactored: event subscriptions/polling/runtime state moved to runStore
- ✅ Log field mapping fix: `data.jobName`/`data.stage` instead of `data.detail`/`data.category` (C5)
- ✅ Frontend-optimistic cancellation with 5-second reconciliation timeout (C1)
- ✅ Path traversal sanitization in `GetHistoricalJobRows` (C8)
- ✅ Event listener isolation: unsub callbacks instead of `EventsOff` (C9)
- ✅ PUR monitor view state sync: `workflowState` synced from `activeRun.status` via useEffect, status-aware executing branch (header/cancel/view-results), all 3 "Prepare New Run" buttons call `reset()` (C10)
- ✅ SingleJob executing view state sync: `singleJobStore.state` synced from `activeRun.status`, status-aware header/buttons, `handleCancel` race fix, `handleStartOver` guarded `clearActiveRun()` (C11)
- ✅ Backend: `GetRunHistory()` lists `~/.rescale-int/states/` files, `GetHistoricalJobRows()` loads with sanitization
- ✅ 3 new Go tests: path traversal, missing file, empty directory

**Source:** `frontend/src/stores/runStore.ts`, `frontend/src/stores/singleJobStore.ts`, `frontend/src/stores/jobStore.ts`, `frontend/src/components/tabs/PURTab.tsx`, `frontend/src/components/tabs/SingleJobTab.tsx`, `frontend/src/components/tabs/ActivityTab.tsx`, `frontend/src/App.tsx`, `internal/wailsapp/job_bindings.go`

### v4.7.2 (February 21, 2026)
**Consistent Load/Save UI + Label Improvements:**
- ✅ PUR "Load Existing Base Job Settings" dropdown with CSV, JSON, SGE options (was single "Load Settings" button)
- ✅ PUR "Save As..." dropdown with CSV, JSON, SGE options for template saving
- ✅ SingleJob "Load From..." renamed to "Load Existing Job Settings"
- ✅ PUR subtitle "Parallel Upload and Run" added to progress bar header
- ✅ PUR label improvements: "Configure Base Job Settings", "Scan to Create Jobs"
- ✅ SetupTab inner "Advanced Settings" renamed to "Logging Settings"
- ✅ Fixed orgCode dropped in `loadJobFromJSON` and `loadJobFromSGE` (jobStore.ts)

### v4.7.1 (February 21, 2026)
**Disk Space Error UX & Settings Reorganization:**
- ✅ Disk space error banner in Transfers tab with available/needed space info
- ✅ Short "No disk space" error labels with full tooltip on hover
- ✅ Error classification (classifyError/extractDiskSpaceInfo) in transferStore
- ✅ Moved Worker Configuration and Tar Options from Setup tab to PUR tab Pipeline Settings
- ✅ Tar options added to SingleJob directory mode
- ✅ Scan prefix and validation pattern persist to config.csv from PUR tab
- ✅ Compression value normalized (legacy "gz" → "gzip")
- ✅ Removed engine validation pattern fallback to config

### v4.7.0 (February 21, 2026)
**PUR Performance & Reliability:**
- ✅ Fixed relative path generation: all scan output now uses absolute paths via `pathutil.ResolveAbsolutePath()`, preventing CWD-dependent failures in GUI mode
- ✅ Pipeline ingress normalization: `NewPipeline()` normalizes relative paths at ingress, with belt-and-suspenders check in `tarWorker` for legacy CSV/state files
- ✅ Replaced `filepath.Walk` with `filepath.WalkDir` in engine.go and multipart.go (no per-entry `os.Stat` syscalls)
- ✅ Added `filepath.SkipDir` after matched directories to avoid descending into run directory contents
- ✅ Concurrent version resolution: `resolveAnalysisVersions()` runs in goroutine; tar/upload workers start immediately instead of waiting for paginated API call
- ✅ Fixed `p.logf()` duplicate logging: no longer calls both callback AND `log.Printf` when callback is set
- ✅ Converted ~35 `log.Printf` calls in pipeline.go to `p.logf()` for GUI Activity Log visibility
- ✅ Added `AnalysisResolver` interface for testable version resolution
- ✅ Added phase timing logs: pipelineStart, first tarball, completion timing
- ✅ New `pipeline_test.go` with 5 tests; 3 new engine tests for absolute paths and SkipDir behavior

**Source:** `internal/pur/pipeline/pipeline.go`, `internal/core/engine.go`, `internal/util/multipart/multipart.go`

### v4.6.8 (February 18, 2026)
**Bug Fixes & Terminology:**
- ✅ Fixed automation JSON format: (1) nested `{"automation": {"id": "..."}}` object, not flat string; (2) `"environmentVariables": {}` must be present (API returns HTTP 500 if omitted). Added `NormalizeAutomations()` choke point + initialized at all construction sites
- ✅ Retry safety: job creation/submission POST no longer retries on 5xx; custom `ErrorHandler` preserves actual API error messages
- ✅ Fixed single job submission for all three input modes (directory, localFiles, remoteFiles)
- ✅ Added pipeline support for pre-specified InputFiles (skip tar/upload when Directory is empty)
- ✅ Suppressed GTK ibus input method warnings on Linux GUI
- ✅ Renamed "template" → "settings" in PUR tab headings/buttons
- ✅ Renamed "Run Folders Subpath and Validation Configuration" → "Directory Scan Settings"
- ✅ Updated PUR terminology: "Parallel Upload and Run" throughout CLI help and docs
- ✅ Fixed default job template directory from `./Run_${index}` to empty string

**Source:** `internal/models/job.go`, `internal/api/client.go`, `internal/wailsapp/job_bindings.go`, `internal/pur/pipeline/pipeline.go`, `main.go`, `frontend/src/`

### v4.6.7 (February 17, 2026)
**Audit Remediation (Security, Code Quality, Dead Code, Documentation):**
- ✅ Corrected SECURITY.md crypto claims (AES-256-GCM → AES-256-CBC with PKCS7 padding)
- ✅ URL sanitization: replaced substring-based FedRAMP URL detection with hostname parsing (frontend + backend) + 9 tests
- ✅ Race condition fix in queue retry test (mutex synchronization)
- ✅ Context leak fix in daemon.go (`defer cancel()`)
- ✅ Shared FIPS init extracted to `internal/fips/init.go`; stdlib `slices.Contains` consolidation
- ✅ Dead code removal: `ValidateForConnection`, `UpdateJobRow`, backward-compat aliases, `equalIgnoreCase`
- ✅ Makefile `make build` fixed to output to versioned platform directory
- ✅ Fixed stale 16MB → 32MB chunk size references; version consistency across all docs

**Source:** `internal/fips/init.go`, `internal/config/csv_config.go`, `frontend/src/components/tabs/SetupTab.tsx`, `frontend/src/components/tabs/PURTab.tsx`, `internal/services/daemon.go`

### v4.6.6 (February 17, 2026)
**Shared Job Download Fix (Azure):**
- ✅ Fixed `AzureCredentials.Paths` type mismatch — API returns `[]object`, struct now uses `[]AzureCredentialPath` (was `[]string`)
- ✅ Removed dead code branch in `buildSASURL()` (Paths-as-URL was never used by API)
- ✅ Added `GetPerFileSASToken()` helper for per-file blob-level SAS token lookup with container-level fallback
- ✅ Structured download error messages with step classification, root cause extraction, secret sanitization
- ✅ 26 new unit tests across credentials, Azure client, error formatting, and API mocks

**Source:** `internal/models/credentials.go`, `internal/cloud/providers/azure/client.go`, `internal/cli/download_helper.go`, `internal/cli/jobs.go`

### v4.6.5 (February 16, 2026)
**PUR Parity: Close All Gaps vs Old Python PUR:**
- ✅ Per-upload proxy warmup — prevents Basic proxy session expiry during long batch runs
- ✅ Multi-part `make-dirs-csv` with `--part-dirs` flag for scanning multiple project directories
- ✅ OrgCode project assignment — per-job org code with API-based project assignment
- ✅ `--dry-run` on `pur run` and `pur resume` — preview job summary without executing
- ✅ `submit-existing --ids` — direct job submission by ID without CSV
- ✅ Shared `multipart.ScanDirectories()` helper with 8 unit tests

**Source:** `internal/http/proxy.go`, `internal/pur/pipeline/pipeline.go`, `internal/util/multipart/scan.go`, `internal/cli/pur.go`

### v4.6.4 (February 12, 2026)
**PUR Feature Parity, Bug Fixes, and Enhancements:**
- ✅ Fixed pattern regex missing filenames with number-separator-text (e.g., `Run_335_Fluid_Meas.avg.snc`)
- ✅ Fixed GUI template crash on null/missing fields (panic recovery, atomic writes)
- ✅ Fixed Azure proxy timeout on block 0 (context-aware retry, deadline checks)
- ✅ `--extra-input-files` — upload local files once, attach to every PUR job
- ✅ `--iterate-command-patterns` — vary commands across runs with preview mode
- ✅ Missing CLI flags exposed: `--include-pattern`, `--exclude-pattern`, `--flatten-tar`, `--tar-compression`

**Source:** `internal/pur/pattern/pattern.go`, `internal/wailsapp/job_bindings.go`, `internal/cli/pur.go`, `internal/http/retry.go`

### v2.4.5 (November 19, 2025)
**Cross-Storage Download & Signal Handling Fixes:**
- ✅ Fixed job output downloads for cross-storage scenarios (Azure users can download S3 job outputs)
- ✅ Fixed spurious cancellation messages after successful operations
- ✅ Enhanced tab-completion documentation

**Source:** `internal/cloud/download/download.go`, `internal/cli/root.go`

### v2.4.3 (November 18, 2025)
**Security & Quality Improvements:**
- ✅ Path traversal protection with comprehensive input validation
- ✅ Strict checksum verification by default
- ✅ Graceful cancellation support (Ctrl+C)
- ✅ Constants centralization for better maintainability

**Source:** `internal/validation/paths.go`, `internal/constants/app.go`

### v2.3.0 (November 17, 2025)
**Critical Bug Fixes:**
1. **Resume Logic Fix** - Range check for PKCS7 padding (1-16 bytes)
2. **Decryption Progress** - Message before long-running decryption
3. **Progress Bar Corruption** - Routed all output through mpb io.Writer

**Previous in v2.3.0:**
- Streaming decryption (16KB chunks)
- Disk space checks before decryption
- Reduced safety buffer to 5%

---

## Compatibility

### API Compatibility
- **Rescale Platform API:** v3 (default) and v2 (optimized for job operations)
- **Minimum Go Version:** 1.24

### Storage Compatibility
- **S3:** AWS S3 API (compatible with S3-compatible services)
- **Azure:** Azure Blob Storage API v2

---

## Performance Summary

| Feature | Key Benefit | Performance Impact |
|---------|-------------|-------------------|
| **Transfer Grouping** | Collapse 10k+ transfers into aggregate batch rows | 2000x reduction in IPC payload (2MB → 1KB/cycle), eliminates DOM flooding |
| **Rate Limiting** | Prevents API lockouts while maximizing throughput | 15% safety margin, cross-process coordinator, 429 feedback, utilization-based visibility |
| **Concurrent Uploads** | Process multiple files simultaneously | 5-10x faster than sequential |
| **Concurrent Downloads** | Parallel downloads with resume support | 10-100x faster, limited by storage not API |
| **Folder Operations** | Preserve directory structure | Eliminates manual folder management |
| **Job Downloads** | v2 API + zero GetFileInfo optimization | 12.5x faster listing + ~3 min saved per job |
| **Multi-threading** | Dynamic thread allocation across transfers | Optimizes for file size and system resources |

**Overall**: rescale-int is designed for **high-performance, large-scale operations** with intelligent resource management and automatic optimization.

---

## Documentation References

For more details, see:
- **README.md** - Quick start guide
- **CLI_GUIDE.md** - Complete command reference with examples
- **ARCHITECTURE.md** - System design and technical architecture
- **RELEASE_NOTES.md** - Detailed version history
- **TODO_AND_PROJECT_STATUS.md** - Current status and roadmap

---

*Last Updated: February 25, 2026*
*Version: 4.7.5*
