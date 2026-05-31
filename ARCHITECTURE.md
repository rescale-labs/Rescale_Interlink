# Architecture - Rescale Interlink

**Version**: 4.9.8
**Last Updated**: May 31, 2026

For verified feature details and source code references, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

---

## Table of Contents

- [System Overview](#system-overview)
- [Package Structure](#package-structure)
- [Key Components](#key-components)
- [CLI Compatibility Mode](#cli-compatibility-mode)
- [Jobs Watch Engine](#jobs-watch-engine)
- [GUI Architecture (Wails)](#gui-architecture-wails)
- [Encryption & Security](#encryption--security)
- [Storage Backends](#storage-backends)
- [Performance Optimizations](#performance-optimizations)
- [Threading Model](#threading-model)
- [Data Flow](#data-flow)
- [Design Principles](#design-principles)
- [Constants Management](#constants-management)

---

## System Overview

Rescale Interlink is a unified CLI and GUI application for managing Rescale computational jobs. The architecture follows a layered design with clear separation of concerns.

```
+-------------------------------------------------------------+
|                 Rescale Interlink v4.9.8                     |
|              Unified CLI + GUI Architecture                  |
+-------------------------------------------------------------+
|                                                              |
|  +------------------+             +----------------------+   |
|  |   CLI Mode       |             |   GUI Mode (Wails)   |   |
|  |   (default)      |             |   (rescale-int-gui)  |   |
|  +------------------+             +----------------------+   |
|  | * Cobra commands |             | * React/TS Frontend  |   |
|  | * Compat mode    |             | * Wails Go Bindings  |   |
|  | * mpb progress   |             | * Event Bridge       |   |
|  +--------+---------+             +----------+-----------+   |
|           |                                  |               |
|           +---------------+------------------+               |
|                           |                                  |
|                  +--------v--------+                         |
|                  |  Services Layer |                         |
|                  +-----------------+                         |
|                  | * TransferSvc   |                         |
|                  | * FileService   |                         |
|                  | * EventBus      |                         |
|                  +--------+--------+                         |
|                           |                                  |
|                  +--------v--------+                         |
|                  |  Core Engine    |                         |
|                  +-----------------+                         |
|                  | * Config        |                         |
|                  | * API Client    |                         |
|                  | * Cloud I/O     |                         |
|                  +-----------------+                         |
+-------------------------------------------------------------+
          |                    |                    |
          v                    v                    v
    +---------+          +---------+        +----------+
    | Rescale |          | Local   |        | User     |
    | API     |          | Files   |        | Terminal |
    +---------+          +---------+        +----------+
```

**Binaries:**
- `rescale-int` (from `cmd/rescale-int/`): CLI-only. Rejects `--gui` with an error directing users to `rescale-int-gui`. Also serves as the compat-mode entry point when invoked as `rescale-cli`.
- `rescale-int-gui` (from root `main.go`): Unified GUI+CLI. The `--gui` flag launches the Wails GUI.
- `rescale-int-tray` (from `cmd/rescale-int-tray/`): Windows system tray companion for daemon status. **Windows MSI installs only** — not shipped with the portable Windows distribution, macOS, or Linux builds.

---

## Package Structure

### Top-Level Organization

```
rescale-int/
├── cmd/
│   ├── rescale-int/               # CLI-only binary entry point
│   └── rescale-int-tray/          # Windows system tray companion (MSI install only)
│
├── frontend/                      # Wails React frontend
│   ├── src/
│   │   ├── App.tsx                # Main app with tab navigation
│   │   ├── components/
│   │   │   ├── tabs/              # 6 tab implementations
│   │   │   ├── widgets/           # Shared widgets (JobsTable, StatsBar, etc.)
│   │   │   └── common/            # Common components (ErrorBoundary)
│   │   ├── stores/                # Zustand state management
│   │   │   ├── jobStore.ts        # PUR workflow configuration
│   │   │   ├── runStore.ts        # Active run monitoring + queue
│   │   │   ├── singleJobStore.ts  # Single Job form state
│   │   │   ├── configStore.ts     # Configuration state
│   │   │   ├── transferStore.ts   # Transfer tracking + batch grouping
│   │   │   └── logStore.ts        # Activity log state
│   │   ├── types/                 # TypeScript type definitions
│   │   └── utils/                 # Shared utilities
│   ├── wailsjs/                   # Auto-generated Go bindings
│   └── package.json
│
├── internal/
│   │
│   │  ── CLI & Commands ──
│   ├── cli/                       # Native CLI commands (Cobra)
│   │   └── compat/                # rescale-cli compatibility mode (25 files)
│   ├── watch/                     # Job watch engine (shared by native + compat)
│   │
│   │  ── Core ──
│   ├── api/                       # Rescale API client (v3 + v2)
│   ├── config/                    # Configuration, CSV parsing, API key resolution
│   ├── constants/                 # Application-wide constants
│   ├── core/                      # Core engine (job pipeline orchestration)
│   ├── events/                    # Event bus system (pub/sub + ring buffer)
│   ├── models/                    # Data models (jobs, files, credentials)
│   ├── version/                   # Version constant
│   │
│   │  ── Cloud Storage ──
│   ├── cloud/                     # Cloud storage (unified backend)
│   │   ├── credentials/           # Credential management + warming
│   │   ├── download/              # Download entry point
│   │   ├── providers/             # Provider implementations
│   │   │   ├── s3/                # S3 provider (5 files)
│   │   │   └── azure/             # Azure provider (5 files)
│   │   ├── state/                 # Resume state management
│   │   ├── storage/               # Storage interfaces and errors
│   │   ├── transfer/              # Upload/download orchestration
│   │   └── upload/                # Upload entry point
│   │
│   │  ── Transfer Infrastructure ──
│   ├── transfer/                  # Transfer coordination and batch abstraction
│   │   ├── folder/                # Folder creation and orchestration
│   │   └── scan/                  # Remote folder scanning
│   ├── localfs/                   # Local filesystem browser (WalkStream)
│   ├── resources/                 # Resource management (threads, memory)
│   ├── progress/                  # Progress bar UI (mpb wrapper)
│   │
│   │  ── Security & Crypto ──
│   ├── crypto/                    # AES-256-CBC encryption (streaming + legacy)
│   ├── fips/                      # FIPS 140-3 initialization
│   ├── reporting/                 # Error reporting (classify → redact → report)
│   │
│   │  ── GUI ──
│   ├── wailsapp/                  # Wails v2 Go bindings
│   ├── services/                  # GUI-agnostic services (TransferService, FileService)
│   │
│   │  ── Background Service ──
│   ├── daemon/                    # Auto-download daemon
│   ├── service/                   # Windows service mode (multi-user)
│   ├── ipc/                       # Cross-process IPC (daemon ↔ GUI)
│   │
│   │  ── Rate Limiting ──
│   ├── ratelimit/                 # Token bucket rate limiting
│   │   └── coordinator/           # Cross-process rate limit coordinator
│   │
│   │  ── PUR ──
│   ├── pur/                       # PUR (Parallel Upload and Run)
│   │   ├── filescan/              # File scanning
│   │   ├── parser/                # SGE script parsing
│   │   ├── pattern/               # Pattern detection for batch jobs
│   │   ├── pipeline/              # Pipeline orchestration
│   │   ├── state/                 # PUR state management
│   │   └── validation/            # Core type validation
│   │
│   │  ── Networking ──
│   ├── http/                      # HTTP client, proxy, and retry logic
│   │
│   │  ── Platform ──
│   ├── diskspace/                 # Cross-platform disk space checking
│   ├── elevation/                 # Windows UAC / Unix privilege elevation
│   ├── logging/                   # Logger and TeeWriter
│   ├── mesa/                      # Mesa/OpenGL setup (Windows/Linux GPU)
│   ├── mesainit/                  # Mesa early initialization
│   ├── pathutil/                  # Path resolution
│   ├── platform/                  # Cross-platform sleep prevention
│   │
│   │  ── Utilities ──
│   ├── util/
│   │   ├── analysis/              # Analysis utilities
│   │   ├── buffers/               # Buffer pooling
│   │   ├── filter/                # File filtering
│   │   ├── glob/                  # Glob pattern matching
│   │   ├── multipart/             # Multipart upload and scan
│   │   ├── paths/                 # Path collision detection
│   │   ├── sanitize/              # String sanitization
│   │   ├── tags/                  # File tag utilities
│   │   └── tar/                   # TAR archive creation
│   └── validation/                # Path validation
│
├── build/                         # Wails build assets (icons, manifests)
└── testdata/                      # Test fixtures
```

### Import Dependencies

```
cmd/rescale-int
    ├─→ internal/cli
    ├─→ internal/cli/compat
    ├─→ internal/fips
    └─→ internal/version

internal/cli
    ├─→ internal/core
    ├─→ internal/progress
    ├─→ internal/api
    ├─→ internal/watch
    └─→ internal/models

internal/cli/compat
    ├─→ internal/api
    ├─→ internal/config
    ├─→ internal/watch
    ├─→ internal/models
    └─→ internal/version

internal/wailsapp
    ├─→ internal/core
    ├─→ internal/services
    ├─→ internal/events
    ├─→ internal/api
    └─→ internal/models

internal/services (GUI-agnostic)
    ├─→ internal/core
    ├─→ internal/events
    └─→ internal/cloud

internal/core
    ├─→ internal/events
    ├─→ internal/api
    ├─→ internal/config
    ├─→ internal/pur/state
    └─→ internal/models

internal/watch (zero imports from cli or compat)
    ├─→ internal/constants
    └─→ (all dependencies injected via function types)
```

**Key Principle**: No circular dependencies. Clear layering with dependencies flowing downward. The `watch` package is deliberately import-free from `cli` and `compat` — all behavior is injected.

---

## Key Components

### 1. Core Engine (`internal/core/`)

**Purpose**: Orchestrates the PUR job submission pipeline (tar → upload → create → submit).

The `Engine` struct holds configuration, API client, event bus, state manager, pipeline instance, transfer/file services, and job monitoring infrastructure. See `internal/core/engine.go` for the full definition.

**Responsibilities**:
- Configuration validation
- Job specification parsing
- Pipeline execution (tar → upload → create → submit; or skip tar/upload when input files are pre-specified)
- State persistence
- Event emission for UI updates

**Thread Safety**: All public methods are thread-safe using RWMutex.

### 2. API Client (`internal/api/`)

**Purpose**: Interface to Rescale Platform REST API v3 and v2.

The `Client` struct manages HTTP transport with connection pooling, API token, base URL, rate limiter, and folder cache. See `internal/api/client.go` for the full definition.

**Key Features**:
- HTTP client with connection pooling (512 idle connections total, 100 per host, 90s idle timeout)
- Automatic retry with exponential backoff
- Rate limiting (three-scope token bucket)
- Folder content caching via `ListFolderContentsPage` enrichment
- Structured error handling

**Selected client methods**: file/folder/job CRUD (`ListFiles`, `DeleteFile`, `CreateFolder`, `ListFolderContents`, `DeleteFolder`, `GetJob`, `GetJobStatuses`, `SubmitJob`, `StopJob`, etc.). Streaming upload and download primitives are **not** methods on `api.Client` — they live as free functions in `internal/cloud/upload/` and `internal/cloud/download/` and run on top of provider-specific transfer handles. The API client only handles metadata-level REST calls.

### 3. Event Bus (`internal/events/`)

**Purpose**: Decouple UI updates from business logic via publish-subscribe.

The `EventBus` struct manages per-type subscriber channels, an "all events" subscriber list, a ring buffer for timeline capture in error reports, and a dropped-event counter. See `internal/events/events.go` for the full definition.

**Event Types** (19 total):
- Core pipeline: `EventProgress`, `EventLog`, `EventStateChange`, `EventError`, `EventComplete`
- Transfer queue: `EventTransferQueued`, `EventTransferInitializing`, `EventTransferStarted`, `EventTransferProgress`, `EventTransferCompleted`, `EventTransferFailed`, `EventTransferCancelled`
- Configuration: `EventConfigChanged`
- Enumeration: `EventEnumerationStarted`, `EventEnumerationProgress`, `EventEnumerationCompleted`
- Catalog scan: `EventScanProgress`
- Batch display: `EventBatchProgress`
- Error reporting: `EventReportableError`

**Key Features**:
- Buffered channels (configurable, default 1000) prevent blocking
- Non-blocking publish (drops if subscriber slow, counted via atomic counter)
- Thread-safe subscription management
- Ring buffer (capacity 50) captures recent events for error report timelines

**Transfer Batch Events:**
- `EventBatchProgress` — aggregate progress for batched transfers (1/sec per active batch)
- Individual `EventTransferProgress` suppressed at source for batched tasks
- Terminal events (completed, failed, cancelled) always published individually for accuracy

### 4. Folder Cache (`internal/transfer/folder/`)

**Purpose**: Reduce API calls for folder operations during directory uploads.

The `FolderCache` struct in `internal/transfer/folder/folder.go` uses a map keyed by folder ID with RWMutex for thread safety. Double-checked locking prevents duplicate API calls.

**Cache methods**:
- `Get(ctx, apiClient, folderID)`: Returns cached contents or fetches from API
- `Invalidate(folderID)`: Removes cached entry

**Related package helper**: `folder.CheckFolderExists(ctx, apiClient, cache, parentID, name)` — a free function that probes the cache before creating folders. Not a method on `*FolderCache`.

### 5. Rate Limiter (`internal/ratelimit/`)

**Purpose**: Prevent API throttling (429 errors) with cross-process coordination.

**Architecture**: Four-layer system:

1. **Token Bucket** (`limiter.go`): Per-scope rate limiter with configurable rate/burst. Supports cooldown periods (from 429 responses) and coordinator delegation hooks.

2. **Singleton Store** (`store.go`): Process-level store keyed by `{baseURL, hash(apiKey), scope}`. All `api.Client` instances sharing the same Rescale account share the same limiters. Also integrates sleep prevention via `platform.InhibitSleep()`.

3. **Unified Registry** (`registry.go`): Single source of truth for endpoint-to-scope mapping. `ResolveScope(method, path)` returns the correct scope using specificity-based rule matching.

4. **Cross-Process Coordinator** (`coordinator/`): Standalone process owning authoritative token buckets. GUI, daemon, and CLI all acquire tokens through it via Unix socket or Windows named pipe. Auto-starts on first API call, auto-exits on idle timeout.

**Configured Scopes** (from `internal/ratelimit/constants.go`):
- User Scope (all v3 API endpoints): 7200/hour = 2 req/sec, target 85%, burst 150
- Job Submission Scope: 1000/hour = 0.278 req/sec, target 85%, burst 50
- Jobs-Usage Scope (v2 job queries): 90000/hour = 25 req/sec, target 85%, burst 300

**429 Feedback Loop**:
- `CheckRetry` callback in `api/client.go` detects every 429 response
- Calls `limiter.Drain()` + `limiter.SetCooldown()` through coordinator hooks
- Propagates drain/cooldown across all processes via coordinator

**Visibility**: Utilization-based notifications with hysteresis — silent when utilization < 50%, warns when >= 60%, throttled to 1 notification per 10 seconds.

**Fallback Behavior** (when coordinator is unreachable):
- Emergency cap: `(hardLimit/4) * 0.5` per process
- Lease-based: valid leases honored until expiry
- Auto-retry: store retries coordinator connection every 30 seconds

### 6. Transfer Batch Abstraction (`internal/transfer/batch.go`)

**Purpose**: Unified execution model for batched file transfers across all entry points.

**Key Types**:
- `WorkItem` interface: requires `FileSize() int64` for adaptive concurrency
- `RunBatch[T WorkItem]`: Executes a known set of items with adaptive concurrency from `ComputeBatchConcurrency()`
- `RunBatchFromChannel[T WorkItem]`: Streaming mode for items arriving incrementally (e.g., folder scan → download). Dynamic worker scaling: samples first 20 items, resamples every 50, scales workers up to 2x per interval.

**Usage**: All transfer paths — CLI folder upload/download, GUI streaming transfers, daemon auto-download — use `RunBatch` or `RunBatchFromChannel`. This replaced 10+ inline worker pool implementations.

### 7. Error Reporting (`internal/reporting/`)

**Purpose**: Safe reporting of genuine server-side failures, with redaction of sensitive data.

**Pipeline**: classify → redact → build → report

- **Classifier** (`classifier.go`): `IsReportable()` filters errors — only server errors (5xx) and unclassified internal errors generate reports. User-fixable errors (auth, network, timeout, disk space, client 4xx) are suppressed.
- **Redactor** (`redactor.go`): Strips hex tokens, URL params, emails, auth tokens, home paths. Job names replaced with `job-N` placeholders.
- **Builder** (`builder.go`): Assembles report from classified error + redacted timeline snapshot.
- **Reporter** (`reporter.go`): GUI wrapper for classify → publish flow.
- **CLI Helper** (`cli_helper.go`): `HandleCLIError()` at CLI `ExecuteC()` error seam — auto-saves reports to disk.

### 8. Sleep Prevention (`internal/platform/`)

**Purpose**: Prevent OS sleep/suspend during file transfers.

Cross-platform via build tags:
- **macOS**: `IOPMAssertionCreateWithName` via CGO (IOKit framework)
- **Windows**: `SetThreadExecutionState`
- **Linux**: `systemd-inhibit`

Integration: ref-counted in `ratelimit/store.go` — acquired when a transfer starts, released when complete. Each platform's release function is idempotent via `sync.Once`.

### 9. Disk Space Checker (`internal/diskspace/`)

**Purpose**: Prevent out-of-disk failures mid-operation.

Cross-platform: `syscall.Statfs` on Unix, `windows.GetDiskFreeSpaceEx` on Windows. Safety margin: 15% additional space required.

### 10. Progress Tracking (`internal/progress/`)

**Purpose**: Abstract progress reporting for CLI and GUI.

CLI uses `mpb` (multi-progress bars) with per-file bars showing speed and ETA. GUI uses EventBus events forwarded through the Wails event bridge.

---

## CLI Compatibility Mode

**Package**: `internal/cli/compat/` (25 files)

Provides drop-in compatibility with `rescale-cli` (the legacy Java-based Rescale CLI). Existing scripts and automation workflows can migrate to Interlink without modification.

### Detection and Activation

`IsCompatMode()` in `compat.go` activates when:
1. `--compat` flag is present in args
2. Binary name ends with `rescale-cli` (symlink or rename)

When active, `cmd/rescale-int/main.go` dispatches to `compat.ExecuteCompat()` instead of the native CLI.

### Architecture

Compat mode builds a **separate Cobra command tree** (`NewCompatRootCmd()` in `root.go`) that mirrors rescale-cli's flag syntax. It imports `config`, `api`, `models`, and `version` directly — it does NOT import the `cli` package, avoiding import cycles.

**Credential resolution chain** (independent from native CLI):
1. `-p/--api-token` flag
2. `RESCALE_API_KEY` env var
3. `apiconfig` INI profile (`--profile` section or `[default]`)

**Argument normalization** (`NormalizeCompatArgs()` in `compat.go`):
- Multi-char short flags: `-fid` → `--file-id`, `-lh` → `--load-hours`
- Multi-value `-f`: `upload -f a b c` → `upload -f a -f b -f c`

### Implemented Commands (10)

`status`, `stop`, `delete`, `check-for-update`, `list-info`, `upload`, `download-file`, `submit`, `list-files`, `sync`

### Behavioral Fidelity

- Exit code 33 on error (matches rescale-cli convention)
- SLF4J-style timestamp format
- JSON output modes (`-e` flag)
- Quiet mode (`-q`) suppresses informational output but not data/errors

---

## Jobs Watch Engine

**Package**: `internal/watch/` (2 files)

Polling engine for monitoring job status and incrementally downloading output files. Imported by both native CLI (`internal/cli`) and compat layer (`internal/cli/compat`), so it has **zero imports from those packages** — all dependencies are injected via function types.

### Design

All behavior is injected:
- `StatusFunc`: fetches current job status
- `DownloadFunc`: runs one download pass (skip-existing semantics)
- `JobLister`: discovers jobs newer than a reference ID
- `DownloadFuncFactory`: creates per-job download closures
- `Callbacks`: optional notification hooks (status change, download pass, terminal, error)

### Two Modes

- **`WatchJob()`**: Polls a single job until terminal status, running download passes each tick.
- **`WatchNewerThan()`**: Discovers all jobs newer than a reference job and watches them until all reach terminal status. Re-discovers newly-created jobs each polling tick.

### Terminal Statuses

`Completed`, `Failed`, `Stopped`, `Force Stopped`, `Terminated` — unified superset used by both native and compat watch paths.

---

## GUI Architecture (Wails)

### Backend Bindings (`internal/wailsapp/`)

1. **App** (`app.go`): Main Wails application struct with lifecycle hooks
2. **Transfer Bindings** (`transfer_bindings.go`): `StartTransfers()`, `CancelTransfer()`, `GetTransferBatches()`, `CancelBatch()`, `RetryFailedInBatch()`, DTOs
3. **File Bindings** (`file_bindings.go`): `ListLocalDirectory()`, `ListRemoteFolder()`, `StartFolderDownload()`, `StartFolderUpload()`
4. **Job Bindings** (`job_bindings.go`): `ScanDirectory()`, `StartBulkRun()`, `StartSingleJob()`, `GetRunHistory()`, `GetHistoricalJobRows()`
5. **Config Bindings** (`config_bindings.go`): Configuration management
6. **Daemon Bindings** (`daemon_bindings.go`): Daemon IPC
7. **Event Bridge** (`event_bridge.go`): Forwards EventBus events to Wails runtime, throttles progress updates (100ms interval)
8. **Version Bindings** (`version_bindings.go`): GitHub update check
9. **Reporting Bindings** (`reporting_bindings.go`): Error report display
10. **API Key Source Bindings** (`api_key_source_bindings.go`): Reports back to the GUI which credential source the runtime resolved (token file vs. env vs. config) and any source conflicts

### Frontend Stores (`frontend/src/stores/`)

1. **jobStore** — PUR workflow configuration state machine
2. **runStore** — Active run monitoring, event subscriptions, polling, queue, restart recovery
3. **singleJobStore** — Single Job form state persisted across tab navigation
4. **configStore** — API configuration and connection state
5. **transferStore** — Transfer queue tracking with batch grouping and disk space error classification
6. **logStore** — Activity log entries with level-aware trimming
7. **fileBrowserStore** — File Browser state, including the four remote browse modes (My Library, My Jobs, Legacy, Trash) and selection bookkeeping
8. **errorReportStore** — Pending error-report dialog state (current report, redacted details, modal visibility)

### Frontend Components (`frontend/src/components/tabs/`)

1. **FileBrowserTab** — Two-pane local/remote file browser. Remote pane has four browse modes: My Library, My Jobs, Legacy, and Trash (soft-deleted entries with restore/purge actions). Upload is disabled in Trash and My Jobs modes with an explicit reason.
2. **TransfersTab** — Transfer progress with batch grouping, cancel/retry, disk space error banner
3. **SingleJobTab** — Job template builder with three input modes (directory, local files, remote files)
4. **PURTab** — Batch job pipeline with view modes (choice screen, monitoring, configuration)
5. **SetupTab** — API settings, proxy configuration, logging, auto-download daemon
6. **ActivityTab** — Logs with level filtering, run history with expandable job tables

### Frontend Shared Widgets (`frontend/src/components/widgets/`)

`JobsTable`, `StatsBar`, `PipelineStageSummary`, `PipelineLogPanel`, `ErrorSummary`, `StatusBadge`, `FileList`, `LocalBrowser`, `RemoteBrowser`, `RemoteFilePicker`, `TemplateBuilder`

---

## Encryption & Security

### AES-256-CBC Encryption (`internal/crypto/`)

**Specifications**:
- **Algorithm**: AES-256-CBC (Cipher Block Chaining)
- **Key Size**: 256-bit (32 bytes)
- **IV Size**: 128-bit (16 bytes)
- **Padding**: PKCS7 (adds 1-16 bytes)
- **Chunk Size**: 16KB for streaming operations
- **Hash Function**: SHA-512 for file integrity

**Streaming implementation** processes files in 16KB chunks with constant ~16KB memory regardless of file size, preventing memory exhaustion on large files (60GB+). See `internal/crypto/encryption.go` and `internal/crypto/streaming.go`.

**Encryption Modes**:
- **Default (streaming)**: Per-part AES-256-CBC encryption during upload. No temporary encrypted file. HKDF-SHA256 key derivation per part.
- **Legacy (`--pre-encrypt`)**: Full-file encryption before upload. Compatible with older Rescale clients.

### File Permissions Security

State files containing sensitive data (encryption keys, IVs, master keys) are created with `0600` permissions:
- Upload/download resume files
- Daemon state
- Token file

### Windows IPC Security

Named pipe authorization with per-user SID matching. See SECURITY.md for details.

### Daemon Transfer Visibility

The daemon auto-download process routes all downloads through the same `TransferService` the GUI uses; there is no parallel transfer implementation inside `internal/daemon/`. GUI visibility is via IPC-based observation:
- `Daemon.TransferService()` + `Daemon.Queue()` expose the shared machinery. IPC polling reads live task and batch state via `MsgGetTransferStatus` → `DaemonTransferSnapshot{Tasks, Batches}`.
- The main Transfers tab renders daemon rows alongside GUI rows with a `Daemon` badge; per-row Cancel/Retry routes by `sourceLabel` through IPC commands (`MsgCancelDaemonBatch`, `MsgCancelDaemonTransfer`, `MsgRetryFailedInDaemonBatch`).
- Works in both subprocess mode (macOS/Linux) and Windows service mode; service-mode routing goes through `MultiUserDaemon.userDaemon(...)` to the correct per-user daemon.

---

## Storage Backends

### Unified Backend Architecture

All transfer operations (uploads and downloads) from both CLI and GUI converge to a single shared backend:

```
┌──────────────────────────────────────────────────────────┐
│                       ENTRY POINTS                        │
│  CLI: upload, download, folders upload-dir/download-dir,  │
│       jobs download, daemon auto-download                 │
│  GUI: File Browser, Single Job, PUR Pipeline              │
└───────────────────────┬──────────────────────────────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────┐
│               UNIFIED ENTRY POINTS                        │
│  upload.UploadFile()         download.DownloadFile()      │
│  internal/cloud/upload/      internal/cloud/download/     │
└───────────────────────┬──────────────────────────────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────┐
│                   PROVIDER FACTORY                         │
│     providers.NewFactory().NewTransferFromStorageInfo()    │
│                 providers/factory.go                       │
└──────────────┬─────────────────────────┬─────────────────┘
               │                         │
               ▼                         ▼
┌──────────────────────┐  ┌──────────────────────┐
│    S3 Provider       │  │    Azure Provider     │
│  (providers/s3/)     │  │  (providers/azure/)   │
│  5 files, 6 ifaces   │  │  5 files, 6 ifaces    │
└──────────┬───────────┘  └──────────┬────────────┘
           └──────────┬──────────────┘
                      ▼
┌──────────────────────────────────────────────────────────┐
│               SHARED ORCHESTRATION                        │
│  transfer/downloader.go  - Download orchestration         │
│  transfer/uploader.go    - Upload orchestration           │
│  transfer/streaming.go   - Streaming encryption           │
│  state/upload.go         - Upload resume state            │
│  state/download.go       - Download resume state          │
└──────────────────────────────────────────────────────────┘
```

**Key Files:**
- Entry points: `internal/cloud/upload/upload.go`, `internal/cloud/download/download.go`
- Providers: `internal/cloud/providers/s3/`, `internal/cloud/providers/azure/`
- Orchestration: `internal/cloud/transfer/`
- State: `internal/cloud/state/`

**Provider Interfaces (6)**: `CloudTransfer`, `StreamingConcurrentUploader`, `StreamingConcurrentDownloader`, `StreamingPartDownloader`, `LegacyDownloader`, `PreEncryptUploader`

**Storage Backend Parity**:
- Both S3 and Azure implement identical 6 interfaces
- Same chunk/part size (32MB via `constants.ChunkSize`)
- Same concurrency model via orchestration layer
- Same resume capability via `state/` package
- Transparent to user (auto-detected via provider factory)

### S3 Backend (`internal/cloud/providers/s3/`)

Multi-part upload API for files ≥100MB, 32MB parts, concurrent part uploads, credential caching via `EnsureFreshCredentials()`, automatic retry with exponential backoff, seekable upload streams for SDK retry.

### Azure Backend (`internal/cloud/providers/azure/`)

Block blob API, 32MB blocks, concurrent block upload, automatic credential refresh, same interface as S3 for consistency.

---

## Performance Optimizations

### Connection Reuse

Single HTTP client with connection pooling (512 idle connections total, 100 per host, 90s idle timeout). All operations in a batch reuse the same client.

### Rate Limiting

Token bucket algorithm with cross-process coordinator. See [Rate Limiter](#5-rate-limiter-internalratelimit) section for details.

### Adaptive Concurrency

`ComputeBatchConcurrency()` in the resource manager dynamically scales concurrent transfers based on median file size:

| Median File Size | Concurrent Transfers | Threads/File |
|-----------------|---------------------|--------------|
| < 100MB (small) | Up to 20 | 1 |
| 100MB – 1GB (medium) | Up to 10 | 4 |
| > 1GB (large) | Up to 5 | 8–16 |

Validated against thread pool capacity and 75% of available memory. Applied symmetrically in GUI and CLI.

**Source:** `internal/resources/manager.go`, `internal/constants/app.go`

### FileInfo Enrichment

`ListFolderContentsPage()` parses full metadata from folder listings (encryption keys, storage info, checksums). Downloads skip the per-file `GetFileInfo()` call. For 13,000 files: eliminates ~2+ hours of rate-limited API overhead.

**Source:** `internal/api/client.go`

### Streaming Scan-to-Download (GUI)

`ScanRemoteFolderStreaming()` uses 8 concurrent workers scanning subfolders, emitting files to a channel. Downloads begin within seconds of scan initiation rather than waiting for full recursive scan.

---

## Threading Model

### CLI Mode

**Main Thread**: Command parsing (Cobra), synchronous execution, progress bar rendering.

**Background Goroutines**: Concurrent uploads/downloads (controlled by `RunBatch` semaphore, adaptive 5–20 based on file sizes), per-file multi-threaded transfers via `TransferHandle`, API calls with timeouts, progress updates.

**Synchronization**: WaitGroups for concurrent operations, mutexes for shared state (minimal), channels for coordination.

### GUI Mode (Wails v2)

**Architecture**: Wails v2 with React/TypeScript frontend.
- **Main Process** (Go): Runs the Wails app, handles API calls, file I/O
- **Renderer Process** (Chromium): Runs the React UI
- **IPC**: Automatic method binding via Wails runtime

**Event Bridge Pattern** (`internal/wailsapp/event_bridge.go`):
Go backend forwards internal EventBus events to Wails runtime events. Frontend subscribes via `EventsOn()`. Progress events throttled to 100ms intervals.

### Two-Layer Concurrency Model

Transfer concurrency uses two layers sharing a single global thread pool (`resources.Manager`):

**Layer 1 — Batch Concurrency** (`RunBatch` / `RunBatchFromChannel` in `internal/transfer/batch.go`):
- Determines how many files transfer simultaneously
- `ComputeBatchConcurrency()` computes median file size → picks tier
- All transfer paths (CLI, GUI, daemon) use this shared abstraction

**Layer 2 — Per-File Multi-Threading** (`AllocateForTransfer` in `resources/manager.go`):
- When each file starts, allocates threads from the shared pool
- Thread count based on file size tiers (500MB-1GB: 4, 1-5GB: 8, 5-10GB: 12, 10GB+: 16)
- Dynamic rebalancing: as files complete, freed threads become available

```
                        ┌─────────────────────┐
                        │  resources.Manager   │
                        │  (Global Thread Pool)│
                        └──────────┬──────────┘
                                   │
              ┌────────────────────┼────────────────────┐
              │                    │                     │
    ┌─────────▼─────────┐  ┌──────▼──────────────┐
    │   RunBatch         │  │ StartStreamingDownloadBatch│
    │   (known items)    │  │ (streaming — GUI folder    │
    │   adaptive workers │  │  download AND daemon)      │
    └─────────┬─────────┘  └──────┬──────────────┘
              │                    │
              └────────────────────┘
                                   │ per file
                        ┌──────────▼──────────┐
                        │  AllocateTransfer    │
                        │  (per-file threads)  │
                        └─────────────────────┘
```

### Conflict Resolution

File conflict handling (skip/overwrite/rename) uses a shared `ConflictResolver[A comparable]` generic type (`internal/cli/conflict.go`). Thread-safe with automatic escalation from "Once" (prompt per conflict) to "All" (apply automatically).

---

## Configuration & Settings Flow

### Settings Persistence Architecture

`config.csv` is the single source of truth for all persistent settings. The GUI reads from and writes to `config.csv` via the Go backend's `ConfigDTO`:

```
┌─────────────────────┐    updateConfig()     ┌──────────────────┐    SaveConfigCSV()    ┌────────────┐
│  PUR Tab            │──────────────────────→│  config_bindings │───────────────────────→│ config.csv │
│  (Pipeline Settings)│    saveConfig()       │  (Go backend)    │    LoadConfigCSV()    │            │
│  SingleJob Tab      │←──────────────────────│                  │←──────────────────────│            │
│  (Tar Options)      │    GetConfig()        │  GetConfig()     │                       │            │
└─────────────────────┘                       └──────────────────┘                       └────────────┘
```

**Settings location by tab:**
- **Setup Tab**: API key, proxy configuration, detailed logging, auto-download daemon
- **PUR Tab**: Pipeline Settings (tar/upload/job workers, tar options), scan prefix, validation pattern
- **SingleJob Tab**: Tar options (directory mode only: exclude/include patterns, compression, flatten)

---

## Data Flow

### Upload Pipeline

```
User Command
    │
    ▼
CLI/GUI Interface
    │
    ▼
Core Engine
    │
    ├─→ 1. Disk Space Check ──→ [FAIL FAST if insufficient]
    ├─→ 2. Create Tar Archive ─→ /tmp/job-xxxx.tar.gz
    ├─→ 3. Encrypt Archive ────→ Encrypted chunks
    ├─→ 4. Upload to Storage ──→ S3/Azure (with progress)
    ├─→ 5. Register File ──────→ API: POST /api/v3/files/
    ├─→ 6. Create Job ─────────→ API: POST /api/v3/jobs/
    └─→ 7. Submit Job ─────────→ API: POST /api/v3/jobs/{id}/submit/
            │
            ▼
         Rescale Platform
```

### Download Pipeline

```
User Command
    │
    ▼
CLI/GUI Interface
    │
    ▼
Core Engine
    │
    ├─→ 1. Get File Metadata ──→ API (or from enriched listing)
    ├─→ 2. Download File ──────→ S3/Azure (with progress)
    ├─→ 3. Decrypt File ───────→ Decrypted chunks
    └─→ 4. Save to Disk ───────→ Local filesystem
```

---

## Design Principles

### 1. Separation of Concerns
- CLI and GUI share core logic
- UI code doesn't contain business logic
- API client is independent of delivery mechanism

### 2. Event-Driven Updates
- UI updates via event bus (decoupled)
- Non-blocking event publish
- Subscribers control their own update rate

### 3. Thread Safety
- Minimal locking (prefer channels)
- Clear lock acquisition order (prevents deadlocks)
- Release locks before calling into other components

### 4. Fail Fast
- Validate early (disk space, config, etc.)
- Clear error messages
- Don't waste time on operations that will fail

### 5. Performance by Default
- Connection reuse automatic
- Folder caching transparent
- Rate limiting prevents problems before they occur (cross-process coordinator ensures global budget sharing)

### 6. Cross-Platform Compatibility
- Abstract platform differences (disk space, file paths, sleep prevention)
- Build tags for platform-specific code
- Consistent user experience across platforms

### 7. Dependency Injection
- Watch engine has zero imports from CLI packages
- All behavior injected via function types
- Enables sharing between native and compat modes without import cycles

---

## Constants Management

### Centralized Configuration

**Purpose**: Single source of truth for all configuration values.

**Implementation**: `internal/constants/app.go`

All configuration constants centralized in one file with named constants, inline documentation, logical grouping, and type safety.

**Categories:**

1. **Storage Operations**: `MultipartThreshold` (100MB), `ChunkSize` (32MB), `MinPartSize` (5MB)
2. **Credential Refresh**: `GlobalCredentialRefreshInterval` (10min), `AzurePeriodicRefreshInterval` (8min)
3. **Retry Logic**: `MaxRetries` (10), `RetryInitialDelay` (200ms), `RetryMaxDelay` (15s)
4. **Disk Space Safety**: `DiskSpaceBufferPercent` (0.15)
5. **Event System**: `EventBusDefaultBuffer` (1000), `EventBusMaxBuffer` (5000)
6. **Pipeline Queues**: `DefaultQueueMultiplier` (2), `MaxQueueSize` (1000)
7. **UI Updates**: `TableRefreshMinInterval` (100ms), `ProgressUpdateInterval` (500ms)
8. **Thread Pool**: `AbsoluteMaxThreads` (32), `MemoryPerThreadMB` (128)
9. **Resource Management**: File size thresholds and adaptive thread allocation
10. **Adaptive Concurrency**: `DefaultMaxConcurrent` (5), `MaxMaxConcurrent` (20), tier-specific values
11. **Channel Buffer Sizes**: `DispatchChannelBuffer` (256), `WorkChannelBuffer` (100)

**Best Practice**: When adding new configurable behavior, add constants to `constants/app.go` with documentation and use them throughout code.

---
