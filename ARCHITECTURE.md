# Architecture - Rescale Interlink

**Version**: 4.8.8
**Last Updated**: March 1, 2026

For verified feature details and source code references, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

---

## Table of Contents

- [System Overview](#system-overview)
- [Package Structure](#package-structure)
- [Key Components](#key-components)
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

**v4.0.0 Note**: The GUI has been completely rewritten using Wails with a React/TypeScript frontend, replacing the previous Fyne-based implementation. The backend (CLI, cloud, API, core) remains unchanged.

```
+-------------------------------------------------------------+
|                 Rescale Interlink v4.8.8                 |
|              Unified CLI + GUI Architecture                  |
+-------------------------------------------------------------+
|                                                              |
|  +------------------+             +----------------------+   |
|  |   CLI Mode       |             |   GUI Mode (Wails)   |   |
|  |   (default)      |             |   (--gui flag)       |   |
|  +------------------+             +----------------------+   |
|  | * Cobra commands |             | * React/TS Frontend  |   |
|  | * mpb progress   |             | * Wails Go Bindings  |   |
|  | * Shell output   |             | * Event Bridge       |   |
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

**Binaries**: `rescale-int` (CLI-only, from `cmd/rescale-int/`) and `rescale-int-gui` (unified GUI+CLI, from root `main.go`). The `--gui` flag shown above applies only to `rescale-int-gui`.

---

## Package Structure

### Top-Level Organization

```
rescale-int/
├── cmd/
│   └── rescale-int/           # Main entry point
│       └── main.go            # CLI/GUI router
│
├── frontend/                  # Wails React frontend (v4.0.0)
│   ├── src/
│   │   ├── App.tsx            # Main app with tab navigation + runStore wiring
│   │   ├── components/
│   │   │   ├── tabs/          # 6 tab implementations
│   │   │   ├── widgets/       # Shared widgets (JobsTable, StatsBar, etc.)
│   │   │   └── common/        # Common components (ErrorBoundary)
│   │   ├── stores/            # Zustand state management
│   │   │   ├── jobStore.ts    # PUR workflow configuration state
│   │   │   ├── runStore.ts    # Active run monitoring + queue (v4.7.3)
│   │   │   ├── singleJobStore.ts # Single Job form state (v4.7.3)
│   │   │   ├── configStore.ts # Configuration state
│   │   │   ├── transferStore.ts # Transfer tracking
│   │   │   └── logStore.ts    # Activity log state
│   │   ├── types/             # TypeScript type definitions
│   │   │   ├── jobs.ts        # Shared domain types (v4.7.3)
│   │   │   ├── run.ts         # Run session types (v4.7.3)
│   │   │   └── events.ts      # Event DTOs
│   │   └── utils/             # Shared utilities (v4.7.3)
│   │       ├── stageStats.ts  # Pipeline stage stats computation
│   │       └── formatDuration.ts # Duration formatting
│   ├── wailsjs/               # Auto-generated Go bindings
│   └── package.json           # Node.js dependencies
│
├── internal/
│   ├── api/                   # Rescale API client
│   ├── cli/                   # CLI commands and helpers
│   ├── wailsapp/              # Wails Go bindings (v4.0.0)
│   │   ├── app.go             # Main Wails application
│   │   ├── config_bindings.go # Configuration methods
│   │   ├── transfer_bindings.go # Upload/download methods
│   │   ├── file_bindings.go   # File browser methods
│   │   ├── job_bindings.go    # Job submission methods
│   │   ├── daemon_bindings.go # Daemon IPC bindings (v4.7.8)
│   │   └── event_bridge.go    # EventBus to Wails events
│   ├── services/              # GUI-agnostic services
│   │   ├── transfer_service.go
│   │   └── file_service.go
│   ├── cloud/                 # Cloud storage (unified backend)
│   │   ├── interfaces.go      # CloudTransfer, UploadParams, DownloadParams
│   │   ├── state/             # Resume state management
│   │   ├── transfer/          # Upload/download orchestration (8 files)
│   │   ├── providers/         # Provider implementations
│   │   │   ├── factory.go     # Provider factory
│   │   │   ├── s3/            # S3 provider (5 files)
│   │   │   └── azure/         # Azure provider (5 files)
│   │   ├── upload/            # Single entry point (upload.go)
│   │   ├── download/          # Single entry point (download.go)
│   │   ├── credentials/       # Credential management
│   │   └── storage/           # Storage utilities and errors
│   ├── config/                # Configuration and CSV parsing
│   ├── constants/             # Application-wide constants
│   ├── core/                  # Core engine
│   ├── crypto/                # AES-256-CBC encryption
│   ├── events/                # Event bus system
│   ├── http/                  # HTTP client, proxy, retry logic
│   ├── models/                # Data models (jobs, files, credentials)
│   ├── progress/              # Progress bar UI (mpb wrapper)
│   ├── pur/                   # PUR-specific functionality
│   │   ├── parser/            # SGE script parsing
│   │   ├── pattern/           # Pattern detection for batch jobs
│   │   ├── pipeline/          # Pipeline orchestration
│   │   ├── state/             # PUR state management
│   │   └── validation/        # Core type validation
│   ├── resources/             # Resource management (threads, memory)
│   ├── transfer/              # Transfer coordination
│   ├── util/                  # General utilities
│   │   ├── buffers/           # Buffer pooling
│   │   ├── filter/            # File filtering
│   │   ├── multipart/         # Multipart upload utilities
│   │   ├── sanitize/          # String sanitization
│   │   └── tar/               # TAR archive creation
│   ├── diskspace/             # Disk space checking
│   ├── logging/               # Logger
│   ├── daemon/                # Auto-download daemon (background service)
│   │   ├── daemon.go          # Daemon lifecycle, job polling, downloadJob()
│   │   ├── monitor.go         # Job eligibility checking
│   │   ├── state.go           # Persistent daemon state (downloaded/failed)
│   │   ├── transfer_tracker.go # In-memory batch tracker for GUI visibility (v4.7.8)
│   │   ├── ipc_handler.go     # Subprocess IPC handler (Unix)
│   │   └── ipc_handler_windows.go # Subprocess IPC handler (Windows)
│   ├── ipc/                   # Cross-process IPC (daemon ↔ GUI communication)
│   │   ├── messages.go        # Message types and structs
│   │   ├── client.go          # IPC client (Windows named pipe)
│   │   ├── client_unix.go     # IPC client (Unix domain socket)
│   │   ├── server.go          # IPC server + ServiceHandler interface (Windows)
│   │   └── server_unix.go     # IPC server + ServiceHandler interface (Unix)
│   ├── service/               # Windows service mode (multi-user daemon)
│   │   ├── service.go         # MultiUserService wrapper
│   │   ├── multi_daemon.go    # MultiUserDaemon (per-user daemon management)
│   │   └── ipc_handler.go     # Service-mode IPC handler
│   ├── ratelimit/             # Rate limiting (token bucket + cross-process coordinator)
│   │   └── coordinator/      # Cross-process rate limit coordinator (Unix socket / named pipe)
│   └── validation/            # Path validation
│
├── bin/                       # Pre-built binaries (organized by version/platform)
├── build/                     # Wails build assets (icons, manifests)
└── testdata/                  # Test fixtures
```

### Import Dependencies

```
cmd/rescale-int
    ├─→ internal/cli
    ├─→ internal/wailsapp  # Wails GUI (v4.0.0)
    ├─→ internal/core
    └─→ internal/events

internal/cli
    ├─→ internal/core
    ├─→ internal/progress
    ├─→ internal/api
    └─→ internal/models

internal/wailsapp (v4.0.0)
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
    ├─→ internal/state
    └─→ internal/models
```

**Key Principle**: No circular dependencies. Clear layering with dependencies flowing downward.

---

## Key Components

### 1. Core Engine (`internal/core/`)

**Purpose**: Orchestrates the job submission pipeline

**Key Types**:
```go
type Engine struct {
    config      *config.Config
    apiClient   *api.Client
    stateManager *state.Manager
    eventBus    *events.EventBus
    mu          sync.RWMutex
}
```

**Responsibilities**:
- Configuration validation
- Job specification parsing
- Pipeline execution (tar → upload → create → submit; or skip tar/upload when input files are pre-specified)
- State persistence
- Event emission for UI updates

**Thread Safety**: All public methods are thread-safe using RWMutex

### 2. API Client (`internal/api/`)

**Purpose**: Interface to Rescale Platform REST API v3

**Key Features**:
- HTTP client with connection pooling
- Automatic retry with exponential backoff
- Rate limiting (three-scope token bucket)
- Folder caching
- Structured error handling

**Client Structure**:
```go
type Client struct {
    httpClient    *http.Client
    transport     *http.Transport
    apiToken      string
    baseURL       string
    rateLimiter   *RateLimiter
    folderCache   *FolderCache
}
```

**Connection Pooling**:
```go
transport := &http.Transport{
    MaxIdleConns:        100,
    MaxIdleConnsPerHost: 20,
    IdleConnTimeout:     90 * time.Second,
    DisableKeepAlives:   false,  // Critical for reuse
}
```

**Key Methods**:
- File operations: `UploadFile()`, `DownloadFile()`, `ListFiles()`, `DeleteFile()`
- Folder operations: `CreateFolder()`, `ListFolderContents()`, `DeleteFolder()`
- Job operations: `CreateJob()`, `SubmitJob()`, `GetJobStatus()`, `StopJob()`

### 3. Event Bus (`internal/events/`)

**Purpose**: Decouple UI updates from business logic

**Architecture**: Publish-Subscribe pattern

**Event Types**:
```go
type EventType string

const (
    ProgressEvent    EventType = "progress"
    LogEvent         EventType = "log"
    StateChangeEvent EventType = "state_change"
)
```

**Implementation**:
```go
type EventBus struct {
    subscribers map[EventType][]chan Event
    mu          sync.RWMutex
}

func (eb *EventBus) Publish(event Event) {
    eb.mu.RLock()
    defer eb.mu.RUnlock()

    for _, ch := range eb.subscribers[event.Type()] {
        select {
        case ch <- event:
        default:
            // Drop event if subscriber is slow (non-blocking)
        }
    }
}
```

**Key Features**:
- Buffered channels (size 100) prevent blocking
- Non-blocking publish (drops if subscriber slow)
- Thread-safe subscription management
- Type-safe event dispatch

**Transfer Batch Events (v4.7.7):**
- `EventBatchProgress` — aggregate progress for batched transfers (1/sec per active batch)
- Individual `EventTransferProgress` suppressed at source for batched tasks (queue-layer skip)
- Terminal events (completed, failed, cancelled) always published individually for accuracy
- Batch progress ticker auto-starts on first `TrackTransferWithBatch()`, auto-stops when all terminal

### 4. Folder Cache (`internal/cli/folder_upload_helper.go`)

**Purpose**: Reduce API calls by 99.8% for folder operations

**Implementation**:
```go
type FolderCache struct {
    cache map[string]*api.FolderContents // folderID -> contents
    mu    sync.RWMutex
}
```

**Cache Operations**:
- `Get(ctx, apiClient, folderID)`: Retrieve cached folder contents or fetch from API
- Double-checked locking pattern prevents duplicate API calls
- Automatic caching of API responses

**Cache Behavior**:
- Cache hit: Returns cached contents immediately
- Cache miss: Fetches from API, caches result, returns
- Thread-safe with RWMutex for concurrent access

### 5. Rate Limiter (`internal/ratelimit/`)

**Purpose**: Prevent API throttling (429 errors) with cross-process coordination

**Architecture**: Four-layer system:

1. **Token Bucket** (`limiter.go`): Per-scope rate limiter with configurable rate/burst. Supports cooldown periods (from 429 responses) and coordinator delegation hooks.

2. **Singleton Store** (`store.go`): Process-level store keyed by `{baseURL, hash(apiKey), scope}`. All `api.Client` instances sharing the same Rescale account share the same limiters. Wires coordinator hooks and visibility callbacks when creating new limiters.

3. **Unified Registry** (`registry.go`): Single source of truth for endpoint-to-scope mapping. `ResolveScope(method, path)` returns the correct scope using specificity-based rule matching.

4. **Cross-Process Coordinator** (`coordinator/`): Standalone process owning authoritative token buckets. GUI, daemon, and CLI all acquire tokens through it via Unix socket (`~/.config/rescale/ratelimit-coordinator.sock`) or Windows named pipe. Auto-starts on first API call, auto-exits on idle timeout.

**Configuration** (from `internal/ratelimit/constants.go`):
```go
// User Scope (all v3 API endpoints): 7200/hour = 2 req/sec
// Target: 85% = 1.7 req/sec, Burst: 150 tokens
userScopeLimiter := NewUserScopeRateLimiter()

// Job Submission Scope: 1000/hour = 0.278 req/sec
// Target: 85% = 0.236 req/sec, Burst: 50 tokens
jobSubmitLimiter := NewJobSubmissionRateLimiter()

// Jobs-Usage Scope (v2 job queries): 90000/hour = 25 req/sec
// Target: 85% = 21.25 req/sec, Burst: 300 tokens
jobsUsageLimiter := NewJobsUsageRateLimiter()
```

**429 Feedback Loop**:
- `CheckRetry` callback in `api/client.go` detects every 429 response
- Calls `limiter.Drain()` + `limiter.SetCooldown()` through coordinator hooks
- Propagates drain/cooldown across all processes via coordinator
- Parses both delta-seconds and HTTP-date `Retry-After` formats

**Visibility**: Utilization-based notifications with hysteresis:
- Silent when utilization < 50% (e.g., emergency cap at 12.5%)
- Warns when utilization >= 60% (e.g., normal operation at 85%)
- Hysteresis: once active, warnings persist until utilization drops below 50%
- Throttled to 1 notification per 10 seconds
- CLI: `log.Printf()`; GUI: Activity Logs tab via EventBus

**Coordinator Startup Wiring**:
- CLI/daemon: `root.go` PersistentPreRun calls `SetCoordinatorEnsurer(coordinator.EnsureCoordinatorClient)`
- GUI: `app.go` startup() calls same (GUI bypasses CLI's PersistentPreRun entirely)
- Lazy: coordinator only spawns when first `GetLimiter()` is called (not on `--version`/`--help`)

**Fallback Behavior** (when coordinator is unreachable):
- Emergency cap: `(hardLimit/4) * 0.5` per process (assumes max 4 concurrent processes)
- Lease-based: if a valid lease exists, use its granted rate until lease expires
- Auto-retry: store retries coordinator connection every 30 seconds

### 6. Disk Space Checker (`internal/diskspace/`)

**Purpose**: Prevent out-of-disk failures mid-operation

**Cross-Platform Implementation**:
```go
// Unix (macOS, Linux)
func GetAvailableSpace(path string) (uint64, error) {
    var stat syscall.Statfs_t
    if err := syscall.Statfs(path, &stat); err != nil {
        return 0, err
    }
    return stat.Bavail * uint64(stat.Bsize), nil
}

// Windows
func GetAvailableSpace(path string) (uint64, error) {
    var freeBytes uint64
    err := windows.GetDiskFreeSpaceEx(
        windows.StringToUTF16Ptr(path),
        &freeBytes,
        nil,
        nil,
    )
    return freeBytes, err
}
```

**Safety Margin**: 15% additional space required

**Usage**:
```go
required := estimatedFileSize * 1.15
available := diskspace.GetAvailableSpace(tempDir)
if available < required {
    return fmt.Errorf("Insufficient disk space: need %s, have %s",
        humanize(required), humanize(available))
}
```

### 7. Progress Tracking (`internal/progress/`)

**Purpose**: Abstract progress reporting for CLI and GUI

**Interface**:
```go
type ProgressReporter interface {
    Start(total int64, description string)
    Update(current int64)
    Finish()
    Error(err error)
}
```

**CLI Implementation** (Multi-Progress):
```go
type MultiProgress struct {
    container *mpb.Progress
    bars      map[string]*mpb.Bar
    mu        sync.Mutex
}

func (mp *MultiProgress) AddBar(name string, total int64) {
    bar := mp.container.AddBar(total,
        mpb.PrependDecorators(
            decor.Name(name),
            decor.CountersKibiByte("% .2f / % .2f"),
        ),
        mpb.AppendDecorators(
            decor.EwmaSpeed(decor.UnitKiB, "% .2f", 60),
            decor.Percentage(),
        ),
    )
    mp.bars[name] = bar
}
```

**GUI Implementation** (Event-Based):
```go
type GUIProgress struct {
    eventBus *events.EventBus
    jobName  string
}

func (gp *GUIProgress) Update(current int64) {
    gp.eventBus.Publish(&events.ProgressEvent{
        JobName: gp.jobName,
        Current: current,
    })
}
```

---

## Encryption & Security

### AES-256-CBC Encryption (`internal/crypto/`)

**Implementation**: `internal/crypto/encryption.go`

**Verified Specifications** (from FEATURE_SUMMARY.md):
- **Algorithm**: AES-256-CBC (Cipher Block Chaining)
- **Key Size**: 256-bit (32 bytes)
- **IV Size**: 128-bit (16 bytes)
- **Padding**: PKCS7 (adds 1-16 bytes)
- **Chunk Size**: 16KB for streaming operations
- **Hash Function**: SHA-512 for file integrity

**Process**:
1. Generate random 256-bit key and 128-bit IV
2. Encrypt file locally using AES-256-CBC with streaming
3. Upload encrypted file to S3/Azure
4. Store encryption key with file metadata in Rescale API
5. Download encrypted file
6. Decrypt locally using stored key with streaming

**Streaming Implementation**:
```go
// Encryption: Processes file in 16KB chunks
func EncryptFileStreaming(src, dst string, key, iv []byte) error {
    cipher := aes.NewCipher(key)
    stream := cipher.NewCBCEncrypter(iv)

    buffer := make([]byte, 16384) // 16KB chunks
    for {
        n, err := src.Read(buffer)
        if n > 0 {
            encrypted := make([]byte, n)
            stream.CryptBlocks(encrypted, buffer[:n])
            dst.Write(encrypted)
        }
        if err == io.EOF {
            break
        }
    }
    // Add PKCS7 padding to final chunk
}
```

**Decryption**:
```go
// Streaming decryption - only unpads final chunk
func DecryptFileStreaming(src, dst string, key, iv []byte) error {
    cipher := aes.NewCipher(key)
    stream := cipher.NewCBCDecrypter(iv)

    buffer := make([]byte, 16384) // 16KB chunks
    // Process all chunks except last
    // Only unpad the final chunk (removes 1-16 bytes)
}
```

**Memory Usage**: Constant ~16KB regardless of file size

**Key Properties**:
- Streaming encryption/decryption using 16KB chunks
- Prevents memory exhaustion on large files (60GB+)
- Constant ~16KB memory footprint regardless of file size

### File Permissions Security (v4.4.2)

**State files** containing sensitive data (encryption keys, IVs, master keys) are created with secure permissions:

| File Type | Location | Permissions | Contains |
|-----------|----------|-------------|----------|
| Upload resume | `<file>.upload.resume` | 0600 | EncryptionKey, IV, MasterKey |
| Upload lock | `<file>.upload.lock` | 0600 | Process metadata |
| Download resume | `<file>.download.resume` | 0600 | MasterKey, StreamingFileId |
| Daemon state | `~/.config/rescale-int/daemon-state.json` | 0600 | Job metadata |
| Token file | `~/.config/rescale-int/token` | 0600 | API key |

**Implementation**: All `os.WriteFile()` calls for sensitive data use mode `0600`.

### Windows IPC Security (v4.4.2)

**Named Pipe Authorization**: The Windows IPC server (`internal/ipc/server.go`) implements per-user authorization:

1. **Owner SID Capture**: Daemon captures current user's SID at startup
2. **Caller SID Extraction**: Each connection extracts caller's SID via `GetNamedPipeClientProcessId`
3. **Authorization Check**: Modify operations require SID match

| Operation | Authorization |
|-----------|---------------|
| GetStatus, GetUserList, GetRecentLogs, OpenLogs, GetTransferStatus | Open (read-only) |
| PauseUser, ResumeUser, TriggerScan, Shutdown | Owner SID required |

**Rationale**: Prevents User A from controlling User B's daemon on multi-user Windows systems.

### Daemon Transfer Visibility (v4.7.8)

The daemon auto-download process is decoupled from the GUI's TransferService. To provide GUI visibility into daemon downloads without coupling the two systems, v4.7.8 introduces an IPC-based observation pattern:

```
Daemon Process                          GUI Process (Wails)
┌─────────────────────┐                ┌──────────────────────────┐
│ downloadJob()       │                │ transferStore.ts         │
│   ├─ StartBatch()   │                │   ├─ fetchDaemonBatches()│
│   ├─ downloads files│   IPC poll     │   └─ daemonBatches state │
│   └─ FinalizeBatch()│◄──────────────►│                          │
│                     │ GetTransfer    │ TransfersTab.tsx          │
│ DaemonTransferTracker│    Status     │   └─ DaemonBatchRow (RO) │
│   ├─ active batches │                │       (purple "Auto" badge│
│   └─ recent history │                │        no cancel/retry)  │
└─────────────────────┘                └──────────────────────────┘
```

**DaemonTransferTracker** (`internal/daemon/transfer_tracker.go`): In-memory tracker with per-file accounting (Complete/Fail/Skip) and partial-file-bytes progress for smooth progress bars. Speed computed internally from byte deltas. Recent history capped at 10 completed batches.

**IPC Flow**: `MsgGetTransferStatus` → `ServiceHandler.GetTransferStatus(userID)` → `DaemonTransferTracker.GetStatus()` → `TransferStatusData` (IPC-native struct, distinct from Wails DTOs to avoid import cycles).

**Service-mode routing** (3-layer delegation): `ServiceIPCHandler` → `MultiUserService.GetUserTransferStatus()` → `MultiUserDaemon.GetUserTransferStatus()` with SID/username matching (same pattern as PauseUser/GetUserLogs).

---

## Storage Backends

### Unified Backend Architecture (v3.2.0)

All transfer operations (uploads and downloads) from both CLI and GUI converge to a single shared backend, ensuring maximum code reuse and consistent behavior:

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           ENTRY POINTS                                   │
├────────────────────────────────────┬────────────────────────────────────┤
│              CLI                   │              GUI                    │
│  ┌────────────────────────────┐   │   ┌────────────────────────────┐   │
│  │ upload command             │   │   │ File Browser Tab           │   │
│  │   upload_helper.go:361     │   │   │   file_browser_tab.go:585  │   │
│  ├────────────────────────────┤   │   │   file_browser_tab.go:831  │   │
│  │ download command           │   │   ├────────────────────────────┤   │
│  │   download_helper.go:230   │   │   │ Single Job Tab             │   │
│  │   download_helper.go:558   │   │   │   single_job_tab.go:1456   │   │
│  ├────────────────────────────┤   │   ├────────────────────────────┤   │
│  │ folders upload-dir         │   │   │ PUR Pipeline               │   │
│  │   folder_upload_helper.go  │   │   │   pipeline.go:409          │   │
│  ├────────────────────────────┤   │   └────────────────────────────┘   │
│  │ folders download-dir       │   │                                    │
│  │   folder_download_helper.go│   │                                    │
│  ├────────────────────────────┤   │                                    │
│  │ jobs download              │   │                                    │
│  │   jobs.go:918              │   │                                    │
│  └────────────────────────────┘   │                                    │
└────────────────────┬───────────────┴──────────────────┬─────────────────┘
                     │                                  │
                     ▼                                  ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                    UNIFIED ENTRY POINTS                                  │
│                     (internal/cloud/)                                    │
│  ┌─────────────────────────────┐    ┌─────────────────────────────┐    │
│  │    upload.UploadFile()      │    │   download.DownloadFile()   │    │
│  │    upload/upload.go         │    │   download/download.go      │    │
│  └──────────────┬──────────────┘    └──────────────┬──────────────┘    │
└─────────────────┼───────────────────────────────────┼───────────────────┘
                  │                                   │
                  ▼                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                      PROVIDER FACTORY                                    │
│           providers.NewFactory().NewTransferFromStorageInfo()            │
│                       providers/factory.go                               │
└────────────────────────────────┬────────────────────────────────────────┘
                                 │
              ┌──────────────────┴──────────────────┐
              ▼                                      ▼
┌───────────────────────────────┐  ┌───────────────────────────────┐
│        S3 Provider            │  │       Azure Provider          │
│     (providers/s3/)           │  │     (providers/azure/)        │
├───────────────────────────────┤  ├───────────────────────────────┤
│ client.go                     │  │ client.go                     │
│ streaming_concurrent.go       │  │ streaming_concurrent.go       │
│ pre_encrypt.go                │  │ pre_encrypt.go                │
│ download.go                   │  │ download.go                   │
├───────────────────────────────┤  ├───────────────────────────────┤
│ Implements 6 interfaces:      │  │ Implements 6 interfaces:      │
│ • CloudTransfer               │  │ • CloudTransfer               │
│ • StreamingConcurrentUploader │  │ • StreamingConcurrentUploader │
│ • StreamingConcurrentDownloader│ │ • StreamingConcurrentDownloader│
│ • StreamingPartDownloader     │  │ • StreamingPartDownloader     │
│ • LegacyDownloader            │  │ • LegacyDownloader            │
│ • PreEncryptUploader          │  │ • PreEncryptUploader          │
└───────────────────────────────┘  └───────────────────────────────┘
              │                                      │
              └──────────────────┬───────────────────┘
                                 ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                    SHARED ORCHESTRATION                                  │
│                      (transfer/ + state/)                                │
├─────────────────────────────────────────────────────────────────────────┤
│  transfer/downloader.go      - Download orchestration                   │
│  transfer/uploader.go        - Upload orchestration                     │
│  transfer/streaming.go       - Streaming encryption/decryption          │
│  transfer/download_helpers.go - Shared download utilities (v3.2.0)      │
│  state/upload.go             - Upload resume state                      │
│  state/download.go           - Download resume state                    │
└─────────────────────────────────────────────────────────────────────────┘
```

**Key Files:**
- Entry points: `internal/cloud/upload/upload.go`, `internal/cloud/download/download.go`
- Providers: `internal/cloud/providers/s3/`, `internal/cloud/providers/azure/`
- Orchestration: `internal/cloud/transfer/uploader.go`, `internal/cloud/transfer/downloader.go`
- State: `internal/cloud/state/upload.go`, `internal/cloud/state/download.go`

**Provider Factory Pattern:**
```go
// Both S3 and Azure implement identical interfaces
provider := providers.NewFactory().NewTransferFromStorageInfo(storageType, creds)
// Uses: cloud.CloudTransfer, transfer.StreamingConcurrentUploader, etc.
```

**Code Reuse Verification:**
| Entry Point | File:Line | Backend Function |
|-------------|-----------|------------------|
| CLI upload | `upload_helper.go:361` | `upload.UploadFile()` |
| CLI download | `download_helper.go:230,558` | `download.DownloadFile()` |
| CLI folders upload | `folder_upload_helper.go:592,818,918` | `upload.UploadFile()` |
| CLI folders download | `folder_download_helper.go:345` | `download.DownloadFile()` |
| CLI jobs download | `jobs.go:918` | `download.DownloadFile()` |
| GUI File Browser upload | `file_browser_tab.go:585` | `upload.UploadFile()` |
| GUI File Browser download | `file_browser_tab.go:831` | `download.DownloadFile()` |
| GUI Single Job upload | `single_job_tab.go:1456` | `upload.UploadFile()` |
| PUR Pipeline upload | `pipeline.go:409` | `upload.UploadFile()` |

### S3 Backend (`internal/cloud/providers/s3/`)

**Supported**: AWS S3 and S3-compatible services

**Implementation Files**:
- `client.go`: S3 client with credential refresh
- `upload.go`: Upload implementation
- `download.go`: Download implementation
- `streaming_concurrent.go`: Concurrent streaming uploads
- `streaming_concurrent_test.go`: Upload progress reader tests
- `pre_encrypt.go`: Legacy pre-encrypt mode

**Features**:
- Multi-part upload API for files ≥100MB
- Part size: 32MB chunks
- Concurrent part uploads (configurable threads)
- Credential caching via `EnsureFreshCredentials()`
- Automatic retry with exponential backoff (`RetryWithBackoff`)
- Seekable upload streams via `uploadProgressReader` (v4.6.4): `io.ReadSeeker` support so AWS SDK can rewind on transient errors; fresh reader per retry attempt

### Azure Backend (`internal/cloud/providers/azure/`)

**Supported**: Azure Blob Storage

**Implementation Files**:
- `client.go`: Azure client with credential refresh
- `upload.go`: Upload implementation
- `download.go`: Download implementation
- `streaming_concurrent.go`: Concurrent streaming uploads
- `pre_encrypt.go`: Legacy pre-encrypt mode

**Features**:
- Block blob API
- Block size: 32MB
- Concurrent block upload
- Automatic credential refresh via `EnsureFreshCredentials()`
- Same interface as S3 for consistency

**Storage Backend Parity**:
- Both S3 and Azure implement identical 6 interfaces
- Same chunk/part size (32MB via `constants.ChunkSize`)
- Same concurrency model via orchestration layer
- Same resume capability via `state/` package
- Same progress tracking
- Transparent to user (auto-detected via provider factory)

---

## Performance Optimizations

### Connection Reuse

**Problem**: Creating new HTTP connection for each file upload is slow

**Solution**: Single HTTP client with connection pooling

**Implementation**:
```go
// Single client for entire batch
apiClient := api.NewClient(config)

// All uploads reuse same client
for _, file := range files {
    apiClient.UploadFile(file)  // Reuses connections
}
```

**Performance Gain**: 5-10x speedup for multi-file operations

### Folder Caching

**Problem**: Repeated folder lookups cause excessive API calls

**Solution**: In-memory cache with TTL expiration

**Before**:
```
500 folder lookups = 500 API calls
Time: ~250 seconds (0.5s per call)
```

**After**:
```
500 folder lookups = 1 API call + 499 cache hits
Time: ~0.5 seconds
Speedup: 500x
```

**Cache Hit Rate**: 99.8% for typical workflows

### Rate Limiting

**Problem**: Bursts of API calls trigger 429 throttling; multiple processes (GUI + daemon + CLI) can independently exceed limits

**Solution**: Token bucket algorithm with cross-process coordinator and 429 feedback

**Architecture**:
- Process-level singleton store shares limiters across all `api.Client` instances
- Cross-process coordinator (Unix socket / named pipe) ensures GUI + daemon + CLI share a single budget
- 429 feedback: `CheckRetry` callback drains + cools down across all processes instantly
- Utilization-based visibility with hysteresis (silent < 50%, warn >= 60%)
- Targets at 85% of hard limit with 15% safety margin

**Benefits**:
- Eliminates 429 errors (even across multiple processes)
- Reduces total execution time (no retries)
- Predictable, smooth API usage
- Automatic failover to emergency cap if coordinator unreachable

**Effectiveness**:
- Before: 37% of requests returned 429
- After: 0% errors, 60% time reduction

### Concurrent Uploads

**Problem**: Sequential uploads are slow for multiple files

**Solution**: Semaphore pattern for controlled concurrency

**Implementation**:
```go
semaphore := make(chan struct{}, maxConcurrent)
var wg sync.WaitGroup

for _, file := range files {
    wg.Add(1)
    go func(f string) {
        defer wg.Done()
        semaphore <- struct{}{}        // Acquire
        defer func() { <-semaphore }() // Release

        uploadFile(f)
    }(file)
}

wg.Wait()
```

**Performance**: 4.5x-9x faster depending on concurrency level

### Multi-Progress Bars

**Problem**: No visibility into concurrent operations

**Solution**: Individual progress bars per operation

**Benefits**:
- Clear visibility into what's happening
- Real-time bandwidth and ETA per file
- Total progress summary
- No output overlap or corruption

### Adaptive Concurrency (v4.8.0)

**Problem**: Fixed concurrent transfer count (5) is conservative for batches of many small files, where per-file overhead dominates and each file needs only 1 thread.

**Solution**: `ComputeBatchConcurrency()` in the resource manager dynamically scales concurrent transfers based on the median file size in the batch:

| Median File Size | Concurrent Transfers | Threads/File |
|-----------------|---------------------|--------------|
| < 100MB (small) | Up to 20 | 1 |
| 100MB – 1GB (medium) | Up to 10 | 4 |
| > 1GB (large) | Up to 5 | 8–16 |

**Validation**: The adaptive count is validated against:
1. Thread pool capacity (`totalThreads / desiredThreadsPerFile`)
2. Available memory (75% of system memory, at `MemoryPerThreadMB` per thread)
3. User-specified `--max-concurrent` cap (when explicitly set)
4. File count (never more workers than files)
5. Minimum guarantee (`MinMaxConcurrent = 1`)

**Implementation**: `internal/resources/manager.go` — method on the resource manager, sharing mutex access with thread allocation. Applied symmetrically in GUI (`transfer_service.go`) and CLI (`folder_download_helper.go`, `folder_upload_helper.go`).

### FileInfo Enrichment (v4.8.0)

**Problem**: Every file download required a separate `GetFileInfo()` API call at 1.7 req/sec — ~2.2 hours for 13,000 files.

**Solution**: `ListFolderContentsPage()` now parses full metadata from the folder listing response (encryption keys, storage info, checksums, path parts). `FileInfo.ToCloudFile()` converts to the format `DownloadFile()` needs. Returns `nil` if required fields are missing, triggering graceful fallback to `GetFileInfo()`.

**Impact**: Eliminates ~13,000 rate-limited API calls for complete metadata.

### Streaming Scan-to-Download (v4.8.0, GUI)

**Problem**: Folder downloads blocked for the full recursive scan (~40 minutes for 13k files) before any download began.

**Solution**: `ScanRemoteFolderStreaming()` uses 8 concurrent workers scanning subfolders, emitting files to a channel. `StartStreamingDownloadBatch()` consumes the channel and starts downloads as files are discovered. Downloads begin within seconds of scan initiation.

**Key design**:
- Bounded subfolder workers (8 goroutines) via work channel
- Files emitted to `scanEventCh` immediately as discovered
- `requestCh` bridges scanner → batch registrar (closed exactly once via `defer`)
- Context cancellation stops all three layers: scan workers, registration, in-flight downloads
- `batchScanInProgress` map tracks scan state for `TotalKnown` semantics
- `CleanupBatch()` removes map entries on all terminal paths (success, error, cancel)

---

## Threading Model

### CLI Mode

**Main Thread**:
- Command parsing (Cobra)
- Synchronous execution of most commands
- Progress bar rendering

**Background Goroutines**:
- Concurrent uploads/downloads (controlled by semaphore, adaptive count 5–20 based on file sizes in v4.8.0)
- Per-file multi-threaded transfers via `TransferHandle` from resource manager (v4.8.0)
- API calls with timeouts
- Progress updates

**Synchronization**:
- WaitGroups for concurrent operations
- Mutexes for shared state (minimal)
- Channels for coordination

### GUI Mode (Wails v2 - v4.0.0+)

**Architecture**: Wails v2 with React/TypeScript frontend

- **Main Process** (Go): Runs the Wails app, handles API calls, file I/O
- **Renderer Process** (Chromium): Runs the React UI
- **IPC**: Automatic method binding via Wails runtime

**Background Goroutines**:
- API calls
- File I/O operations
- Job status polling (every 30 seconds)
- Event emission via EventBus

**UI Updates from Go**:
```go
// Event Bridge Pattern - internal/wailsapp/event_bridge.go
type EventBridge struct {
    ctx           context.Context
    eventBus      *events.EventBus
    subscription  <-chan events.Event
}

// Forward internal events to Wails runtime
func (eb *EventBridge) handleEvent(e events.Event) {
    switch e.Type() {
    case events.EventTransferProgress:
        dto := ProgressEventDTO{...}
        runtime.EventsEmit(eb.ctx, "transfer:progress", dto)
    case events.EventTransferCompleted:
        dto := CompleteEventDTO{...}
        runtime.EventsEmit(eb.ctx, "transfer:complete", dto)
    }
}
```

**Frontend Event Handling** (React):
```typescript
// Frontend subscribes to Wails events
EventsOn("transfer:progress", (event: ProgressEvent) => {
    useTransferStore.getState().updateProgress(event);
});
```

### Thread Safety (Wails)

**Pattern**: Goroutines can call methods that emit Wails events safely.

```go
// Go methods bound to frontend are automatically thread-safe
func (a *App) StartTransfers(reqs []TransferRequestDTO) error {
    // Run transfer in background
    go func() {
        // Event bridge handles thread-safe emission
        a.eventBus.PublishProgress(taskID, percent, speed)
    }()
    return nil
}
```

**Event Throttling**: Progress events are throttled to 100ms intervals to prevent UI flooding:
```go
progressInterval: 100 * time.Millisecond
if time.Since(eb.lastProgress[taskID]) < eb.progressInterval {
    return // Skip this update
}
```

**Resume Capabilities**: Both upload and download resume are fully implemented. Download resume uses byte-offset HTTP Range requests to continue interrupted downloads from exact positions.

### Two-Layer Concurrency Model (v4.8.1)

Transfer concurrency uses two layers sharing a single global thread pool (`resources.Manager`):

**Layer 1 — Batch Concurrency** (`RunBatch` / `RunBatchFromChannel` in `internal/transfer/batch.go`):
- Determines how many files transfer simultaneously
- `ComputeBatchConcurrency()` computes median file size → picks tier (small=20, medium=10, large=5)
- Validates tier against thread pool capacity and 75% of available memory
- All transfer paths (CLI, GUI, daemon) use this shared abstraction

**Layer 2 — Per-File Multi-Threading** (`AllocateForTransfer` in `resources/manager.go`):
- When each file starts, allocates threads from the shared pool
- Thread count based on file size tiers (500MB-1GB: 4, 1-5GB: 8, 5-10GB: 12, 10GB+: 16)
- Dynamic rebalancing: as files complete, freed threads become available for active transfers

**Key invariant**: Layer 1 uses `ComputeBatchConcurrency` to ensure the batch concurrency never oversubscribes the thread pool. Layer 2's `AllocateForTransfer(fileSize, totalFiles)` uses the adaptive worker count from Layer 1 to compute each file's fair share of the pool.

```
                        ┌─────────────────────┐
                        │  resources.Manager   │
                        │  (Global Thread Pool)│
                        └──────────┬──────────┘
                                   │
              ┌────────────────────┼────────────────────┐
              │                    │                     │
    ┌─────────▼─────────┐  ┌──────▼──────┐  ┌──────────▼─────────┐
    │   RunBatch         │  │RunBatchFrom-│  │  ForceSequential   │
    │   (known items)    │  │ Channel     │  │  (daemon mode)     │
    │   adaptive workers │  │ (streaming) │  │  1 worker          │
    └─────────┬─────────┘  └──────┬──────┘  └──────────┬─────────┘
              │                    │                     │
              └────────────────────┼────────────────────┘
                                   │ per file
                        ┌──────────▼──────────┐
                        │  AllocateTransfer    │
                        │  (per-file threads)  │
                        └─────────────────────┘
```

### Conflict Resolution (v4.8.1)

File conflict handling (skip/overwrite/rename) uses a shared `ConflictResolver[A comparable]` generic type (`internal/cli/conflict.go`). The resolver is thread-safe and handles automatic escalation from "Once" (prompt per conflict) to "All" (apply automatically). Used by:
- Download helpers: `NewDownloadConflictResolver`, `NewFolderDownloadConflictResolver`
- Upload helpers: `NewFileConflictResolver`, `NewErrorActionResolver`

### GUI Components (v4.0.0+ Wails)

**Backend Bindings** (`internal/wailsapp/`):

1. **App** (`app.go`):
   - Main Wails application struct
   - Lifecycle hooks: startup, domReady, beforeClose, shutdown
   - Service injection via Engine

2. **Transfer Bindings** (`transfer_bindings.go`):
   - `StartTransfers()` - Initiate upload/download batch
   - `CancelTransfer()` - Cancel single transfer
   - `GetTransferStats()` - Current stats
   - `GetTransferBatches()` - Aggregate batch stats (v4.7.7)
   - `GetUngroupedTransferTasks()` - Tasks with no batch ID (v4.7.7)
   - `GetBatchTasks()` - Paginated tasks within a batch (v4.7.7)
   - `CancelBatch()` / `RetryFailedInBatch()` - Batch-level actions (v4.7.7)
   - DTOs for cross-boundary data

3. **File Bindings** (`file_bindings.go`):
   - `ListLocalDirectory()` - Browse local filesystem
   - `ListRemoteFolder()` - Browse Rescale files
   - `StartFolderDownload()` - Recursive download

4. **Job Bindings** (`job_bindings.go`):
   - `ScanDirectory()` - Pattern-based job discovery
   - `StartBulkRun()` - PUR batch submission
   - `StartSingleJob()` - Single job workflow
   - `GetCoreTypes()`, `GetAnalysisCodes()` - Metadata
   - `GetRunHistory()` - List historical state files (v4.7.3)
   - `GetHistoricalJobRows()` - Load job rows from state file with path-traversal sanitization (v4.7.3)

5. **Event Bridge** (`event_bridge.go`):
   - Forwards EventBus events to Wails runtime
   - Handles Progress, Log, StateChange, Error, Complete, BatchProgress events
   - Throttles progress updates (100ms interval)
   - BatchProgressEvent forwarded as `interlink:batch_progress` (v4.7.7)

**Frontend Stores** (`frontend/src/stores/`):

1. **jobStore** - PUR workflow configuration state machine (template, scan, validation, execution start)
2. **runStore** - Active run monitoring, event subscriptions, polling, queue, restart recovery (v4.7.3)
3. **singleJobStore** - Single Job form state persisted across tab navigation (v4.7.3)
4. **configStore** - API configuration and connection state
5. **transferStore** - Transfer queue tracking with disk space error classification, batch grouping (v4.7.7)
6. **logStore** - Activity log entries and filtering

**Frontend Components** (`frontend/src/components/tabs/`):

1. **FileBrowserTab** - Two-pane local/remote file browser
2. **TransfersTab** - Active upload/download progress tracking, disk space error banner (v4.7.1), transfer grouping with BatchRow component and paginated expansion (v4.7.7)
3. **SingleJobTab** - Job template builder and submission, tar options for directory mode (v4.7.1), uses singleJobStore (v4.7.3)
4. **PURTab** - Batch job pipeline with Pipeline Settings (workers + tar options, v4.7.1), view modes and run monitoring (v4.7.3)
5. **SetupTab** - API settings, proxy configuration, logging, auto-download daemon
6. **ActivityTab** - Logs, event monitoring, and run history panel (v4.7.3)

**Frontend Shared Widgets** (`frontend/src/components/widgets/` - v4.7.3):

1. **JobsTable** - Reusable job rows table with status badges
2. **StatsBar** - Run progress statistics bar
3. **PipelineStageSummary** - Per-stage success/fail counts
4. **PipelineLogPanel** - Scrollable pipeline log display
5. **ErrorSummary** - Failed job error display
6. **StatusBadge** - Color-coded status indicator

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

**Event Emissions** (at each stage):
- ProgressEvent (status updates)
- LogEvent (detailed logs)
- StateChangeEvent (job status changes)

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
    ├─→ 1. Get File Metadata ──→ API: GET /api/v3/files/{id}/
    ├─→ 2. Download File ──────→ S3/Azure (with progress)
    ├─→ 3. Decrypt File ───────→ Decrypted chunks
    └─→ 4. Save to Disk ───────→ Local filesystem
            │
            ▼
         Local File
```

### Folder Operations with Caching

```
User Command: folders list --folder-id abc123
    │
    ▼
CLI/GUI Interface
    │
    ▼
API Client
    │
    ├─→ Check Cache ─────→ [HIT: Return cached ID]
    │                      [MISS: Continue below]
    ▼
Rate Limiter (wait if needed)
    │
    ▼
HTTP Request
    │
    ▼
API: GET /api/v3/folders/abc123/contents/
    │
    ▼
Cache Result (for future use)
    │
    ▼
Return to User
```

**Cache Hit Rate**: 99.8% after initial population

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
- Abstract platform differences (disk space, file paths)
- Test on all target platforms
- Provide consistent user experience

---

## Configuration & Settings Flow (v4.7.1)

### Settings Persistence Architecture

`config.csv` is the single source of truth for all persistent settings. The GUI reads from and writes to `config.csv` via the Go backend's `ConfigDTO`:

```
┌─────────────────────┐    updateConfig()     ┌──────────────────┐    SaveConfigCSV()    ┌────────────┐
│  PUR Tab            │──────────────────────→│  config_bindings │───────────────────────→│ config.csv │
│  (Pipeline Settings)│    saveConfig()       │  (Go backend)    │    LoadConfigCSV()    │            │
│  SingleJob Tab      │←──────────────────────│                  │←──────────────────────│            │
│  (Tar Options)      │    GetConfig()        │  GetConfig()     │                       │            │
└─────────────────────┘                       │  normalizes gz→  │                       └────────────┘
                                              │  gzip (v4.7.1)   │
                                              └──────────────────┘
```

**Settings location by tab (v4.7.1):**
- **Setup Tab**: API key, proxy configuration, detailed logging, auto-download daemon
- **PUR Tab**: Pipeline Settings (tar/upload/job workers, tar options), scan prefix, validation pattern
- **SingleJob Tab**: Tar options (directory mode only: exclude/include patterns, compression, flatten)

**Scan options persistence (v4.7.1):**
The PUR tab's scan prefix (`runSubpath`) and validation pattern (`validationPattern`) are persisted to `config.csv` on change. The engine's `Scan()` and `ScanToSpecs()` use only the value from scan options (no fallback to global config), making the PUR tab the single source of truth.

---

## Constants Management

### Centralized Configuration

**Purpose**: Single source of truth for all configuration values, reducing errors and improving maintainability.

**Implementation**: `internal/constants/app.go`

**Motivation**:
- Magic numbers scattered across codebase made changes error-prone
- Inconsistent values between different parts of system
- No central documentation of why specific values were chosen
- Hard to discover what values control system behavior

**Solution**:
All configuration constants centralized in one file with:
- Named constants instead of magic numbers
- Comprehensive inline documentation
- Logical grouping by category
- Type safety via Go's const system

**Categories of Constants**:

1. **Storage Operations** (`lines 6-22`)
   - `MultipartThreshold = 100MB` - When to use multipart/block upload
   - `ChunkSize = 32MB` - Part size for S3, block size for Azure, range size for downloads
   - `MinPartSize = 5MB` - AWS S3 minimum (Azure has no minimum)

2. **Credential Refresh** (`lines 24-39`)
   - `GlobalCredentialRefreshInterval = 10min` - AWS/Azure credentials (~15min expiry, 5min buffer)
   - `AzurePeriodicRefreshInterval = 8min` - For Azure large files (>1GB)
   - `LargeFileThreshold = 1GB` - Triggers periodic refresh

3. **Retry Logic** (`lines 41-52`)
   - `MaxRetries = 10` - Exponential backoff attempts
   - `RetryInitialDelay = 200ms` - First retry delay
   - `RetryMaxDelay = 15s` - Cap for exponential backoff

4. **Disk Space Safety** (`lines 54-59`)
   - `DiskSpaceBufferPercent = 0.15` - 15% extra space required

5. **Event System** (`lines 61-70`)
   - `EventBusDefaultBuffer = 1000` - Reduced from 10,000 for memory
   - `EventBusMaxBuffer = 5000` - High-throughput scenarios

6. **Pipeline Queues** (`lines 72-80`)
   - `DefaultQueueMultiplier = 2` - Queue = workers × multiplier
   - `MaxQueueSize = 1000` - Prevent unbounded growth

7. **UI Updates** (`lines 82-95`)
   - `TableRefreshMinInterval = 100ms` - Prevent excessive redraws
   - `ProgressUpdateInterval = 250ms` - Balance responsiveness/performance

8. **Thread Pool** (`lines 106-114`)
   - `AbsoluteMaxThreads = 32` - Hard cap on concurrency
   - `MemoryPerThreadMB = 128` - Memory allocation estimate

9. **Resource Management** (`lines 149-192`)
   - File size thresholds (100MB, 500MB, 1GB, 5GB, 10GB)
   - Thread allocation strategies (auto-scale vs fixed)
   - Adaptive allocation based on file size

10. **Adaptive Concurrency** (v4.8.0, `lines 158-181`)
    - `DefaultMaxConcurrent = 5` — default for non-adaptive paths
    - `MaxMaxConcurrent = 20` — upper bound for adaptive scaling
    - `AdaptiveSmallFileConcurrency = 20` — files < 100MB
    - `AdaptiveMediumFileConcurrency = 10` — files 100MB–1GB
    - `AdaptiveLargeFileConcurrency = 5` — files > 1GB

11. **Channel Buffer Sizes** (v4.8.1)
    - `DispatchChannelBuffer = 256` — high-throughput dispatch channels (streaming downloads, GUI requests)
    - `WorkChannelBuffer = 100` — bounded worker pool channels (pipelined uploads, log subscribers)

**Usage Example**:
```go
// Before (magic number)
if fileSize > 100*1024*1024 {
    // Use multipart upload
}

// After (named constant)
if fileSize > constants.MultipartThreshold {
    // Use multipart upload
}
```

**Benefits**:
- **Discoverability**: One place to find all values
- **Maintainability**: Change once, applies everywhere
- **Documentation**: Each constant explains WHY that value
- **Consistency**: No mismatched values between modules
- **Type Safety**: Compile-time validation

**Best Practice**: When adding new configurable behavior:
1. Add constant to `constants.go` with documentation
2. Use constant throughout code
3. Document rationale in comments
4. Group with related constants

---
