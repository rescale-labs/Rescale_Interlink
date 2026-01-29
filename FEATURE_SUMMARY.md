# Rescale Interlink - Complete Feature Summary

**Version:** 4.5.1
**Build Date:** January 28, 2026
**Status:** Production Ready, FIPS 140-3 Compliant (Mandatory)

This document provides a comprehensive, verified list of all features available in Rescale Interlink.

---

## Table of Contents

- [Core Capabilities](#core-capabilities)
- [File Operations](#file-operations)
- [Folder Operations](#folder-operations)
- [Job Operations](#job-operations)
- [Background Service (Daemon)](#background-service-daemon)
- [PUR - Parallel Uploader and Runner](#pur---parallel-uploader-and-runner)
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
- **Source:** `internal/cli/files.go:45-160`, `internal/cloud/upload/`

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
- Part size: 16MB chunks (`internal/cloud/upload/`)
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
- Chunk size: 16MB (`internal/cloud/download/s3_concurrent.go:44`)
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

## PUR - Parallel Uploader and Runner

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
- **Source:** `internal/cli/pur.go:45-230`

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

### Proxy Support (v2.4.2)
**Implementation:** `internal/pur/proxy/proxy.go`, `internal/pur/httpclient/client.go`

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
- NO_PROXY support for bypass rules
- Works for all operations: file upload/download, folder operations, job submission, API calls
- **Source:** `internal/pur/proxy/proxy.go:17-227`, `internal/pur/httpclient/client.go:30-88`

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
- All buffer pools (16MB chunk buffers, 16KB encryption buffers) clear data before returning to pool
- Prevents sensitive data from persisting in memory between operations
- Uses Go's `clear()` builtin for efficient zeroing
- **Source:** `internal/util/buffers/pool.go:50-56, 72-80`

---

## Performance Features

### Rate Limiting (Token Bucket Algorithm)

**What it is**: Intelligent API throttling that prevents hitting Rescale's hard rate limits while maximizing throughput.

**How it works:**
- Uses token bucket algorithm with configurable burst capacity
- Three separate rate limiters for different API scopes:
  - **User Scope**: 1.6 req/sec (80% of 2 req/sec hard limit) - all v3 endpoints
  - **Job Submission**: 0.139 req/sec (50% of 0.278 req/sec limit) - POST /api/v2/jobs/{id}/submit/
  - **Jobs Usage**: 20 req/sec (80% of 25 req/sec limit) - v2 job query endpoints (v2.4.7+)

**Burst Capacity:**
- User scope: 150 tokens (~93 seconds of rapid operations)
- Job submission: 50 tokens (~360 seconds)
- Jobs usage: 300 tokens (~15 seconds)

**Key Benefits:**
- 20% safety margin prevents throttle lockouts
- Allows rapid burst operations at startup
- Automatic rate adjustment based on endpoint type
- Real-time usage monitoring (logs every 30 seconds)

**Configuration:**
```bash
# Global flags (applied to all commands)
--max-threads 16        # Thread pool size for transfers (0=auto, max 32)
--parts-per-file 5      # Concurrent chunks per file (0=auto, max 10)
--no-auto-scale         # Disable dynamic thread adjustment
```

**Source:** `internal/ratelimit/limiter.go`, `internal/ratelimit/constants.go`, `internal/api/client.go`

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
- Resume state validation and cleanup
- Graceful degradation on network issues
- **Source:** `internal/diskspace/`, `internal/pur/storage/errors.go`

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
**Source:** `internal/pur/proxy/`

**Features:**
- HTTP/HTTPS proxy configuration
- Environment variable support (HTTP_PROXY, HTTPS_PROXY)
- Proxy authentication (Basic and NTLM)
- NO_PROXY bypass rules

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
├── ratelimit/    # Token bucket rate limiting
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
| **Rate Limiting** | Prevents API lockouts while maximizing throughput | 20% safety margin, smart burst handling |
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

*Last Updated: January 9, 2026*
*Version: 4.2.1*
