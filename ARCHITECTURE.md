# Architecture - Rescale Interlink

**Version**: 4.9.9
**Last Updated**: September 7, 2026

For feature details and source code references, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

Sizes in this document are binary and match the constants in `internal/constants/app.go`: 1 KB = 1,024 bytes, 1 MB = 1,048,576 bytes, 1 GB = 1,073,741,824 bytes, 1 TB = 1,099,511,627,776 bytes.

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
- [Transfer Integrity](#transfer-integrity)
- [Performance Optimizations](#performance-optimizations)
- [Threading Model](#threading-model)
- [Configuration & Settings Flow](#configuration--settings-flow)
- [Data Flow](#data-flow)
- [Design Principles](#design-principles)
- [Constants Management](#constants-management)

---

## System Overview

Rescale Interlink is a unified CLI and GUI application for managing Rescale computational jobs. The architecture follows a layered design with clear separation of concerns.

```
+--------------------------------------------------------------+
|                 Rescale Interlink v4.9.9                     |
|              Unified CLI + GUI Architecture                  |
+--------------------------------------------------------------+
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
|           |                       +----------v-----------+   |
|           |                       |     Core Engine      |   |
|           |                       +----------------------+   |
|           |                       | * PUR orchestration  |   |
|           |                       | * Config, API client |   |
|           |                       +----------+-----------+   |
|           |                                  |               |
|           |                       +----------v-----------+   |
|           |                       |    Services Layer    |   |
|           |                       +----------------------+   |
|           |                       | * TransferService    |   |
|           |                       | * FileService        |   |
|           |                       | * EventBus           |   |
|           |                       +----------+-----------+   |
|           |                                  |               |
|           +---------------+------------------+               |
|                           |                                  |
|                  +--------v--------+                         |
|                  | Transfer + I/O  |                         |
|                  +-----------------+                         |
|                  | * Transfer queue|                         |
|                  | * Cloud I/O     |                         |
|                  | * API Client    |                         |
|                  +-----------------+                         |
+--------------------------------------------------------------+
          |                    |                    |
          v                    v                    v
    +---------+          +---------+        +----------+
    | Rescale |          | Local   |        | User     |
    | API     |          | Files   |        | Terminal |
    +---------+          +---------+        +----------+
```

The arrow between the two middle boxes runs **core → services**: `internal/core` imports `internal/services` and `internal/services` does not import `internal/core`. The Core Engine is the PUR orchestrator, so the GUI reaches it for pipeline runs and reaches `TransferService`/`FileService` directly for file transfers and browsing. The CLI goes through neither — it builds its own Cobra tree and reaches `internal/transfer`, `internal/cloud` and `internal/pur/pipeline` itself.

**Startup and mode selection.** Both binaries call `fips.Init` from their `init()`: when the Go runtime does not report FIPS 140-3 mode, startup prints a critical error and exits with code **2**, unless `RESCALE_ALLOW_NON_FIPS=true` is set (a development override, `internal/fips/init.go`). `rescale-int-gui` embeds the built frontend with `//go:embed all:frontend/dist` and chooses its mode in `isCLIMode()`: `--cli` forces CLI and `--gui` forces GUI; otherwise the arguments are matched against a fixed list of ten subcommand names (`jobs`, `files`, `folders`, `upload`, `download`, `hardware`, `software`, `config`, `pur`, `completion`) plus `--help`/`-h`/`--version`/`-v`, each compared as `arg == pattern || strings.HasPrefix(arg, pattern+" ")`, and a match selects CLI. Anything left over falls through a catch-all that also selects CLI, so the GUI is reached only by `--gui` or by no arguments at all — and with no arguments it still falls back to the CLI on Linux with neither `DISPLAY` nor `WAYLAND_DISPLAY` set. On Linux the same `init()` sets `GTK_IM_MODULE`, `GIO_USE_VFS` and `WEBKIT_DISABLE_DMABUF_RENDERER` before GTK/WebKit initialise, each skippable through an environment variable (`RESCALE_ENABLE_GVFS`, `RESCALE_GPU_ACCEL`).

**Binaries:**
- `rescale-int` (from `cmd/rescale-int/`): CLI-only. Rejects `--gui` with an error directing users to `rescale-int-gui`. Also serves as the compat-mode entry point when invoked as `rescale-cli`.
- `rescale-int-gui` (from root `main.go`): Unified GUI+CLI. The `--gui` flag launches the Wails GUI.
- `rescale-int-tray` (from `cmd/rescale-int-tray/`): Windows system tray companion for daemon status. Windows-only, and shipped in both Windows artifacts — `build_dist.ps1` builds it into `_build\bin\`, which is what the portable zip packs. What the zip lacks is the MSI's wiring: the per-user `HKCU\...\CurrentVersion\Run` value that starts the tray at logon, the Start-menu and desktop shortcuts, and the tray launch that follows a successful install. The bundled WebView2 fixed-version runtime is *not* one of those differences — `build_dist.ps1` puts it in `_build\bin\webview2\`, so the zip carries it too. Neither is the Windows Service: the MSI does not install or start it. The service is installed by `rescale-int service install` (`internal/cli/service_commands.go`), which the GUI and tray trigger through UAC via `elevation.InstallServiceElevated`; the MSI only best-effort *uninstalls* it on removal.

---

## Package Structure

### Top-Level Organization

```
rescale-int/
├── main.go                        # GUI+CLI binary entry point (rescale-int-gui)
├── cmd/
│   ├── rescale-int/               # CLI-only binary entry point
│   └── rescale-int-tray/          # Windows system tray companion (zip + MSI; autostart is MSI-only)
│
├── frontend/                      # Wails React frontend
│   ├── src/
│   │   ├── App.tsx                # Main app with tab navigation
│   │   ├── components/
│   │   │   ├── tabs/                # 7 tab implementations
│   │   │   ├── widgets/             # Shared widgets (JobsTable, StatsBar, etc.)
│   │   │   ├── common/              # Common components (ErrorBoundary)
│   │   │   └── ErrorReportModal.tsx # Error-report dialog
│   │   ├── stores/                  # Zustand state management
│   │   │   ├── jobStore.ts          # PUR workflow configuration
│   │   │   ├── runStore.ts          # Active run monitoring + queue
│   │   │   ├── singleJobStore.ts    # Single Job form state
│   │   │   ├── configStore.ts       # Configuration state
│   │   │   ├── transferStore.ts     # Transfer tracking + batch grouping
│   │   │   ├── fileBrowserStore.ts  # File Browser modes, filters, selection
│   │   │   ├── rateLimitStore.ts    # Footer rate-limit indicator
│   │   │   ├── errorReportStore.ts  # Pending error-report dialog
│   │   │   └── logStore.ts          # Activity log state
│   │   ├── types/                 # TypeScript type definitions
│   │   └── utils/                 # Shared utilities
│   ├── wailsjs/                   # Auto-generated Go bindings
│   └── package.json
│
├── internal/
│   │
│   │  ── CLI & Commands ──
│   ├── cli/                       # Native CLI commands (Cobra)
│   │   └── compat/                # rescale-cli compatibility mode (17 source files)
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
│   │   │   ├── s3/                # S3 provider (5 source files)
│   │   │   ├── azure/             # Azure provider (5 source files)
│   │   │   └── testsupport/       # Fixtures shared by both providers' tests
│   │   ├── state/                 # Resume state, upload locking, process liveness
│   │   ├── storage/               # Shared storage error helpers
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
│   │   ├── doe/                   # Design of experiments (parameter sweeps)
│   │   ├── filescan/              # Per-file job scanning and command rendering
│   │   ├── parser/                # SGE script parsing
│   │   ├── pattern/               # Pattern detection and {{token}} substitution
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
│   ├── mesa/                      # Mesa/OpenGL software rendering (Windows only;
│   │                              # the non-Windows files are no-op stubs)
│   │   └── dlls/                  # Mesa DLLs embedded by `-tags mesa`
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
│   │   ├── multipart/             # Multi-part run-directory scan and validation
│   │   ├── paths/                 # Path collision detection
│   │   ├── sanitize/              # String sanitization
│   │   ├── tags/                  # File tag utilities
│   │   └── tar/                   # TAR archive creation
│   └── validation/                # Path validation
│
├── build/                         # appicon.png, plus build_dist.ps1 and
│   │                              # build_installer.ps1 (Windows driver scripts)
│   ├── darwin/                    # Info.plist / Info.dev.plist for the .app bundle
│   ├── linux/                     # AppImage WebKit bundling, release verification,
│   │                              # and the .desktop entry
│   └── windows/                   # icon.ico and wails.exe.manifest
│
├── installer/                     # Windows MSI: WiX source, licence, icon, build
│                                  # script, plus a Go test that checks the WiX source
├── packaging/                     # macOS install helper, Linux .desktop entry and icon
└── .github/                       # Release workflow
```

That is every tracked top-level directory. The tracked top-level files are `main.go`, `wails.json`, `Makefile`, `go.mod`/`go.sum`, `LICENSE`, `logo.png`, `.gitignore`, `.gitattributes`, and the documentation `.md` files.

### Import Dependencies

Selected edges, not the full graph. `go list -f '{{join .Imports "\n"}}' ./internal/<pkg>` is authoritative.

```
cmd/rescale-int                     (complete — this package imports nothing else internal)
    ├─→ internal/cli
    ├─→ internal/cli/compat
    └─→ internal/fips

internal/cli
    ├─→ internal/api
    ├─→ internal/config
    ├─→ internal/watch
    ├─→ internal/models
    ├─→ internal/progress
    ├─→ internal/transfer  (+ /folder, /scan)
    ├─→ internal/cloud (+ /upload, /download, /credentials, /state)
    ├─→ internal/pur/{pipeline,parser,pattern,state,filescan,doe,validation}
    ├─→ internal/daemon, internal/service, internal/ipc
    ├─→ internal/reporting, internal/resources, internal/localfs, internal/diskspace
    └─→ internal/ratelimit (+ /coordinator)
        NOT internal/core — the PUR engine is reached through internal/pur/pipeline

internal/cli/compat                 (complete, and does NOT import internal/cli —
                                     that is what avoids the cycle)
    ├─→ internal/api, internal/config, internal/models, internal/version, internal/watch
    ├─→ internal/cloud/{credentials,download,upload}
    ├─→ internal/transfer, internal/resources, internal/progress, internal/http
    ├─→ internal/constants, internal/validation
    ├─→ internal/pur/parser
    └─→ internal/util/{analysis,glob}

internal/wailsapp
    ├─→ internal/core
    ├─→ internal/services
    ├─→ internal/events
    ├─→ internal/api
    ├─→ internal/models
    └─→ internal/cli          (reuses CLI-side helpers; the dependency runs GUI → CLI, never back)

internal/core
    ├─→ internal/services     (the engine is a consumer of the services layer)
    ├─→ internal/events
    ├─→ internal/api
    ├─→ internal/config
    ├─→ internal/pur/{pipeline,state,pattern}
    ├─→ internal/transfer/folder
    └─→ internal/models

internal/services (GUI-agnostic)
    ├─→ internal/cloud (+ /upload, /download, /credentials)
    ├─→ internal/transfer     (the batch abstraction and the queue, not /folder or /scan)
    ├─→ internal/events
    ├─→ internal/resources
    ├─→ internal/ratelimit
    ├─→ internal/reporting
    └─→ internal/api
        NOT internal/core — the arrow runs core → services

internal/watch                      (complete)
    ├─→ internal/constants
    └─→ (all other dependencies injected via function types)
```

**Key Principle**: No circular dependencies. Dependencies flow downward: `wailsapp` → `core` → `services` → `cloud`/`transfer`. `internal/cli` sits *below* `wailsapp`, which imports it, and above `transfer`/`cloud`, which it reaches directly rather than through `core`. Two packages are deliberately kept clean so they can be shared:

- `internal/watch` imports only `internal/constants`, so both `internal/cli` and `internal/cli/compat` can use it. Everything else is injected via function types.
- `internal/cli/compat` does not import `internal/cli`. That is the whole of the rule: it builds its own Cobra tree, and reaches the transfer stack (`cloud/{credentials,download,upload}`, `transfer`, `resources`, `progress`, `http`) directly, the same way `internal/cli` does.

`internal/daemon` is a consumer of `TransferService`, not a parallel transfer implementation: it routes downloads through `TransferService.StartStreamingDownloadBatch` and touches `internal/transfer` only for the observation types (`transfer.Queue`, `transfer.BatchStats`). It must not reach for `transfer.RunBatch`, `transfer.Manager` or `resources.Manager` directly.

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

**Thread Safety**: an `RWMutex` guards the configuration and the API client, and the accessors that swap or read them take it — `UpdateConfig` builds the replacement client *outside* the lock, because `api.NewClient` can spend seconds on proxy warmup and holding the lock through that freezes every `GetConfig()` caller. It is not a blanket guarantee: `GetAnalyses` reads `e.apiClient` without taking the lock, and `GetConfig` hands back the shared `*config.Config` rather than a copy (`internal/core/engine.go`).

### 2. API Client (`internal/api/`)

**Purpose**: Interface to Rescale Platform REST API v3 and v2.

The `Client` struct holds its HTTP client, the resolved config, base URL, API key, the process-level rate-limiter store, API usage metrics, and the organization code resolved from the key's own user profile on first use and reused for the rest of the run. There is no folder cache on it. See `internal/api/client.go` for the full definition.

**Key Features**:
- HTTP client from `http.ConfigureHTTPClient` — connection pooling at 100 idle connections total, 100 per host, 90s idle timeout. The larger 512-idle pool is the transfer client's, not this one's
- Automatic retry with exponential backoff
- Rate limiting (three-scope token bucket)
- Folder listing *enrichment* via `ListFolderContentsPage` — not caching. The cache is `folder.FolderCache` in `internal/transfer/folder/`, created per operation by the caller
- Structured error handling

**Selected client methods**: file/folder/job CRUD (`ListFiles`, `DeleteFile`, `CreateFolder`, `ListFolderContents`, `DeleteFolder`, `GetJob`, `GetJobStatuses`, `SubmitJob`, `StopJob`, etc.). Streaming upload and download primitives are **not** methods on `api.Client` — they live as free functions in `internal/cloud/upload/` and `internal/cloud/download/` and run on top of provider-specific transfer handles. The API client only handles metadata-level REST calls.

**Pagination**: the jobs endpoint is read by **page number**. `ListJobsPage(ctx, page, pageSize)` sends `page` and `page_size` and nothing else, because the code assumes `limit`/`offset` are accepted and ignored there, so an offset-based reader would silently re-read page 1 (`internal/api/client.go`). File and folder listings page by following the server's `next` link, and `page_size` is re-applied to every `next` URL because the code assumes the server's own link carries its 25-item default, which would otherwise reintroduce slow pagination mid-walk.

**Filtered listings**: `ListFilesPageWithOptions` accepts a `FileListOptions{OwnerFilter, SearchQuery, Ordering}`, and `SearchFolderContents` searches within a folder. These back the File Browser's search, owner filter and sort controls.

### 3. Event Bus (`internal/events/`)

**Purpose**: Decouple UI updates from business logic via publish-subscribe.

The `EventBus` struct manages per-type subscriber channels, an "all events" subscriber list, a ring buffer for timeline capture in error reports, and a dropped-event counter. See `internal/events/events.go` for the full definition.

**Event Types** (18 total):
- Core pipeline: `EventProgress`, `EventLog`, `EventStateChange`, `EventComplete`
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
- `BatchProgressEvent` carries `Cancelled` and `CancelRequested` alongside `Completed`/`Failed`, so a batch cancelled while the Transfers tab is in the background is still reported as cancelled rather than frozen at zero. It also carries `DiscoveredTotal`/`DiscoveredBytes` — the scan's running count, which leads `Total` (registered tasks) during a streaming folder transfer, so completion is measured against `max(discoveredTotal, total)` — plus `Skipped` (entries the walker could not follow), `FilesPerSec` from a 10-second sliding window, and `ETASeconds`, which is `-1` while `TotalKnown` is false rather than a guess made from an unfinished scan.

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
- The `checkRetry` callback in `api/client.go` runs after every attempt. A cancelled context returns before any feedback, and the 429 branch needs a response that carries its own request and no transport error
- A 429 that reaches it calls `limiter.Drain()` unconditionally, before the retry budget is consulted, so the limiter learns of a 429 that exhausted its retries as well as one that did not. `limiter.SetCooldown()` follows only when `Retry-After` parses as a positive number of seconds or as an HTTP date still in the future
- Propagates drain/cooldown across all processes via coordinator

**Visibility**: Utilization-based notifications with hysteresis — silent when utilization < 50% (`UtilizationSuppressThreshold`), warns at >= 60% (`UtilizationWarnThreshold`), throttled to 1 notification per 10 seconds (`NotifyMinInterval`).

Notices reach a surface through `ratelimit.SetGlobalNotifyFunc`, a single process-level callback:
- The CLI registers it in root's `PersistentPreRun`, pointing at stderr **through** `progress.SinkWriter` so a notice lands above the progress bars rather than inside them.
- `daemon run` re-registers it onto the daemon's own logger in `RunE`, which runs after `PersistentPreRun`. It has to: `daemonize` sets a detached child's stderr to nil, and these notices deliberately bypass the standard logger, so without re-registration a daemon's throttling, cooldown and retry notices would reach nothing.
- The GUI publishes to the event bus, which feeds the Activity log and the footer indicator (`rateLimitStore`).

Entering and leaving degraded mode is announced exactly once per transition (`setDegraded`), not on every check — degraded mode cuts the refill rate, which by itself would drive utilization notices.

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

**Usage**: CLI folder upload/download, GUI streaming transfers and daemon auto-download all run through `RunBatch` or `RunBatchFromChannel`. Two paths deliberately do not: the PUR pipeline runs its own three stage worker pools (see [PUR Pipeline](#13-pur-pipeline-internalpurpipeline)), and `TransferService.UploadFileSync` transfers a single file directly — which is why it signals transfer activity to the rate-limit store itself, work `RunBatch` would otherwise do for it (`internal/services/transfer_service.go`).

**Cancellation**: a cancel must remain distinguishable from an empty or successful run all the way to the surface that reports it, which takes three cooperating pieces:

- The folder orchestrator (`internal/transfer/folder/orchestrator.go`) returns a `Cancelled` flag and the counts discovery reached. Cancellation usually leaves the merge loop through the closed-channel path rather than `ctx.Done()`, because `WalkStream` closes its file channel on cancel and a receive from a closed channel is always ready — so both exits set the flag.
- An empty batch anchors a placeholder task so the transfer still leaves a record. That placeholder is a **completed** task, so both registration sites are guarded on the scan not having been cancelled, and `CancelBatch` anchors a *cancelled* placeholder instead.
- The Transfers tab treats a batch row as "Complete" only when its total is known, its total is non-zero, nothing failed, nothing was cancelled, no cancel was requested, and `completed` equals `max(discoveredTotal, total)` — the same denominator the row's progress text uses, since `total` counts registered tasks and lags the scan (`frontend/src/components/tabs/TransfersTab.tsx`). On the Go side `CancelRequested` is what suppresses a cancelled batch's error report (`internal/services/transfer_service.go`).

`TransferService.CancelAll()` is the queue-wide sweep. The GUI's "Cancel All" is not that call — it iterates the *active* batches (`queued > 0 || active > 0 || !totalKnown`), regardless of whether a row is expanded, then makes a second pass over the still-active ungrouped tasks one by one. Batches and tasks whose `sourceLabel` is `Daemon` are routed to `App.CancelDaemonBatch` / `App.CancelDaemonTransfer` instead (`frontend/src/stores/transferStore.ts`).

### 7. Error Reporting (`internal/reporting/`)

**Purpose**: Safe reporting of genuine server-side failures, with redaction of sensitive data.

**Pipeline**: classify → redact → build → report

- **Classifier** (`classifier.go`): `IsReportable()` filters errors — only server errors (5xx) and unclassified internal errors generate reports. User-fixable errors (auth, network, timeout, disk space, client 4xx, local filesystem) are suppressed. `ClassifyErrorClass` checks the local-filesystem patterns *before* the 4xx/5xx digit matches, whose substring tests would otherwise claim any message containing a bare number.
- **Redactor** (`redactor.go`): `RedactError` strips hex tokens, URL query strings, email addresses, `bearer`/`token`/`key`/`authorization` values and home-directory prefixes from any message. Timeline entries built from *structured* events — state changes and job progress — identify the job as `job-N` by its batch index rather than by name. Log and error text is only pattern-redacted, so an arbitrary job name inside a message is not recognised, and a batch label is copied into a batch-progress entry as it stands.
- **Builder** (`builder.go`): Assembles report from classified error + redacted timeline snapshot.
- **Reporter** (`reporter.go`): GUI wrapper for classify → publish flow.
- **CLI Helper** (`cli_helper.go`): `HandleCLIError()` at CLI `ExecuteC()` error seam — auto-saves reports to disk.
- **Transport** (`transport.go`): writes the report JSON `0600` and, for auto-saved reports, prunes the report directory to the newest 500 (`maxRetainedReports`). Nothing else prunes it, so a repeating failure would otherwise write one file per occurrence forever.

### 8. Sleep Prevention (`internal/platform/`)

**Purpose**: Prevent OS sleep/suspend during file transfers.

Cross-platform via build tags:
- **macOS**: `IOPMAssertionCreateWithName` via CGO (IOKit framework)
- **Windows**: `SetThreadExecutionState`
- **Linux**: `systemd-inhibit`

Integration: ref-counted in `ratelimit/store.go` — acquired when a transfer starts, released when complete. Each platform's release function is idempotent via `sync.Once`.

### 9. Disk Space Checker (`internal/diskspace/`)

**Purpose**: Prevent out-of-disk failures mid-operation.

Cross-platform: `syscall.Statfs` on Unix, `GetDiskFreeSpaceExW` via `kernel32.dll` on Windows. `CheckAvailableSpace(targetPath, requiredBytes, safetyMargin)` takes the margin as a parameter; call sites pass 1.15, i.e. the 15% of `constants.DiskSpaceBufferPercent`.

Two things about the requirement are easy to get wrong and are worth stating:

- The legacy (pre-encrypted) download path requires **2x** the file size, because it holds the encrypted and the decrypted copy at once. That doubling and the margin are part of the decision, so the pre-flight sites return `CheckAvailableSpace`'s own error verbatim rather than rebuilding a message from their own figures: a rebuilt message states a requirement the check never enforced, and stats whichever volume the call site happens to name.
- Free space is measured on the filesystem of the directory being written to. `CheckAvailableSpace` takes the *target file* path and applies `filepath.Dir` to it. `GetAvailableSpace` stats the directory it is handed, as handed, and its callers must apply `filepath.Dir` themselves — stat'ing the parent inside it would report the wrong volume whenever the download directory is itself a mount point.

A mid-transfer ENOSPC is reported by the write that hits it, with that site's own figures. It is not confined to the paths that skip the pre-flight: the v0 legacy download and the sequential HKDF download both pre-check and can still fail on a later write, since the check is a point-in-time reading of a filesystem other processes share. `IsDiskFullError` and `ClassifyErrorClass` match both the Linux ("disk quota exceeded") and macOS/BSD ("disc quota exceeded") spellings of `EDQUOT`.

### 10. Progress Tracking (`internal/progress/`)

**Purpose**: Abstract progress reporting for CLI and GUI.

CLI uses `mpb` (multi-progress bars) with per-file bars showing speed and ETA. GUI uses EventBus events forwarded through the Wails event bridge.

**Log routing during CLI transfers**: while `mpb` is drawing it owns the terminal, redrawing its frame on a timer, so anything written to the same terminal in between lands *inside* that frame — one bar becomes a screenful of half-drawn ones. The routed logging and notification paths therefore go through mpb's own writer, which interleaves whole lines above the bars. Not every byte a transfer emits is routed: a few sites write to stdout or stderr directly, among them the chunked download's resume announcement (`internal/cloud/transfer/provider_download.go`) and the `--skip-checksum` warning (`internal/cloud/download/download.go`).

- `progress.SetLogSink` lets the active UI register itself and `SinkWriter` resolves the sink per write, so log setup happens once at startup and follows the bars as they come and go. Only a UI attached to a terminal claims the sink — with bars off, mpb writes to `io.Discard`, and routing logs there would swallow them.
- The standard logger, which carries the transfer path's `[BATCH]`, `[SLOT]` and `[CRED]` diagnostics and the transfer service's `[TIMING]` lines (the cloud package's own `[TIMING]` lines go to the transfer's output writer, `cloud.TimingLog`), is discarded unless the user asked for it via `--verbose`, `--debug` or `RESCALE_DEBUG`, and routed through `progress.SinkWriter` when they did (`internal/cli/root.go`). The `[TIMING]` lines are emitted at all only where timing is switched on, which `cloud.TimingEnabled` answers from `RESCALE_TIMING=1` or the GUI's detailed-logging toggle. The root command's own `--timing` sets that environment variable in `PersistentPreRun` rather than mirroring it in memory, so a subprocess — the rate-limit coordinator, a daemon — inherits it exactly as it does when the user exports the variable.
- Rate-limit visibility and credential-source warnings deliberately bypass the standard logger so they survive that discard. A crawling transfer must still be able to say it is waiting on a rate limit.

### 11. Design of Experiments (`internal/pur/doe/`)

**Purpose**: Expand one base `JobSpec` into a parameter sweep, one job per design point.

`doe.Generate(Options) Result` is pure — no API client, no filesystem, no engine — so the same call serves the CLI's `--preview`, the GUI's live preview, and generation itself. Its output is `[]models.JobSpec`, which is what `pipeline.NewPipeline` already takes as its job ingress, so a sweep inherits tar/upload/create/submit, state and resume, progress events, and both front ends without pipeline changes.

**Structure**:
- `doe.go` — `Options`, `Parameter`, `Case`, `Result`, `Generate`
- `methods.go` — `Methods()`, the single source of each design's label, description and which options it reads; consumed by CLI flag help and the GUI's method menu
- `design.go` / `sobol.go` — the samplers, which produce points in unit coordinates that `render.go` then maps onto each parameter's range or category list
- `validate.go` — `validateOptions`: option and parameter shape and the bidirectional parameter/token comparison, checked before anything is sampled. The projected case count is a separate gate, `checkCaseCount` in `design.go`, which `Generate` calls next
- `cases_csv.go` — `ParseCasesCSV(io.Reader)`, the one parser behind both the CLI's `--cases-csv` and the GUI's pasted-cases box, so identical text yields an identical sweep on either surface. It carries a limit of its own: `maxCasesCSVBytes` caps the input at 4 MB

**Values reach the command line**: they are rendered into the job's command through `pur/pattern`'s `{{name}}` substitution rather than passed as environment variables, so each case's configuration is visible on its Rescale job page. Parameters and command tokens are validated against each other in both directions — an unused parameter and an unfilled token are both errors, not silently wrong jobs — and every rendered surface (command, job name, tag) is asserted free of residual tokens afterwards.

**One rejection boundary**: `Generate` is the single gate both surfaces call, so the CLI and the GUI bindings surface the same errors instead of each carrying their own policy. It rejects in two phases. Before sampling, `validateOptions` checks the options and parameters and `checkCaseCount` checks the projected size — a full factorial grows as the product of its level counts, so an oversized sweep has to be caught by arithmetic rather than by allocating it. After sampling, `render.go` checks each case's values, residual tokens, rendered lengths and job-name uniqueness, which means earlier cases may already have been rendered when a later one is rejected. Nothing is clamped silently, and a run with any error returns no cases and no job specs at all.

**Shared inputs**: a case never carries a `Directory`, so it always takes the pipeline's skip-tar-and-upload path. `BaseFileIDs` points every case at an already-uploaded deck; left empty, the deck arrives as batch-level Common Files, which the pipeline uploads once and attaches to every job. Either way one deck serves the whole sweep instead of being re-uploaded per case.

### 12. Per-File Job Scanning (`internal/pur/filescan/`)

**Purpose**: Turn each file matching a primary pattern into its own job, with its own command and its own upload.

`filescan` is the single backend behind both the GUI's **Job Source → Files** and the CLI's `pur scan-files`, so the two cannot drift. `scanner.go` globs the primary pattern under the scan root and resolves each match's secondary attachments into a `JobFiles`; `render.go` turns that into the job's command and name.

**Command rendering**: `render.go` reuses `pur/pattern`'s `{{name}}` substitution with five built-in tokens derived from the primary file — `{{file}}`, `{{base}}`, `{{ext}}`, `{{dir}}`, `{{index}}`. A token outside that set is a fatal scan error in either the command or the job name, rather than a literal `{{bse}}` on every rendered command line; a command with no tokens at all is only a warning, since an identical command for every file is occasionally intended. A filename whose value would be unsafe on a command line skips that one file instead of failing the batch. Rendered names are checked for uniqueness across the scan: the state file keeps its rows apart by index, but two jobs sharing a name are indistinguishable in the logs, in the progress reporting keyed by job name, and in the platform's own job list, so the scan fails and names both files rather than producing them.

**Upload model**: each job's `LocalInputFiles` holds exactly its own files — primary plus resolved secondaries — and the pipeline archives that list with `tar.CreateTarGzFromFiles`, flattened into the job's working directory, rather than walking `Directory`. Flattening is what lets a secondary pattern reach outside the primary's folder, and `tar.GenerateTarPathForFiles` names the archive from the job's row index and a hash over every member's path, so neither jobs scanned out of one folder nor two jobs running the same deck collide on a single tarball. Data genuinely shared by every job belongs in Common Files, which uploads once and attaches to all of them.

### 13. PUR Pipeline (`internal/pur/pipeline/`)

**Purpose**: Run a batch of jobs through tar → upload → create → submit, with resumable per-job state. Submission is optional per job (`submitMode`), so a run may legitimately end at "create".

`NewPipeline(cfg, apiClient, jobs []models.JobSpec, opts PipelineOptions)` is the single ingress: the CLI passes a `StateFile` and the GUI passes its own `state.Manager` through `ExistingState`, so both surfaces share one state file per run rather than keeping two. Job directories **and** each job's `LocalInputFiles` are made absolute at ingress, since a path generated under a different working directory is otherwise resolved against this one — and it is what makes an explicit file list's archive name stable across working directories. `SkipTarUpload` is what `submit-existing` sets to go straight to job creation from pre-uploaded file IDs. Persisted state is keyed by the job's **1-based row index**, not by its name; the name is a stored field on the row.

**Stage scheduling.** The pipeline is not a serial loop. `Run` starts three worker pools — `cfg.TarWorkers` tar workers, `cfg.UploadWorkers` upload workers, `cfg.JobWorkers` job workers — over three buffered channels each sized `workers * constants.DefaultQueueMultiplier` (2), plus a feeder goroutine that walks the job list and closes the tar queue behind it. Jobs are therefore in different stages at the same time, and a stage's queue is the back-pressure on the one before it. Two things run alongside: `ResolveSharedFiles` resolves batch-level Common Files synchronously before any worker starts, because every job attaches them; software-version resolution runs in its own goroutine and job workers wait on the `versionsResolved` channel, since only they need it while tar and upload do not. Uploads go through an injected `pipeline.SyncUploader` when the GUI supplies one (`core.Engine` adapts `TransferService.UploadFileSync`, which is what puts PUR's uploads in the Transfers tab under one batch ID); with no uploader injected the pipeline uploads directly.

`checkJobHasInputs` runs in the feeder for any job with no archive of its own to build, and refuses a job that would be created with nothing attached — no directory, no local file list, no per-job file IDs, no batch Common Files. `submit-existing` (`SkipTarUpload`) bypasses it, since that mode's premise is that the caller placed the inputs on Rescale.

**Archive staging.** Archives are staged under `<common parent>/.rescale-int-<hash>/`, where the hash is FNV-1a over the batch's absolute state-file path. That separates two concurrent batches over the same deck and gives a resumed batch the directory it had before, but it is not an unconditional guarantee of uniqueness: two runs pointed at the same state-file path resolve to the same directory, which is the point on a resume, and with no state file at all a per-process seed (PID plus nanosecond timestamp) stands in — that case has no other protection, since two runs without a state file over one file list share every input the name is built from. The common parent is the jobs' shared ancestor, made absolute — a relative directory would be recorded into the state file's archive paths, which a resume from another working directory could not find — and it falls back to the working directory when the only shared ancestor is a volume root. The directory is created in `NewPipeline`, unconditionally, even for a `submit-existing` run that never writes an archive.

Archive filenames differ by job source, and neither shape says which batch it belongs to — which is why the directory rather than the filename separates batches. `tar.GenerateTarPath` names a **directory** archive from the last one or two path components of the directory plus an FNV hash of its absolute path; `tar.GenerateTarPathForFiles` names an **explicit file list** archive from the job's 1-based row index, the first file's stem and a hash over every member's absolute path — the index leads because two jobs may legitimately run the same deck with different commands.

Cleanup is guarded rather than trusting the filename. `safeRemoveTar` resolves the path through symlinks, requires it to sit **directly** inside this batch's own archive directory (also symlink-resolved, and itself required to carry the `.rescale-int-` prefix), requires a regular file with a `.tar.gz`/`.tar` extension, and requires the FNV hash suffix that Interlink's own archive names carry (`pathutil.HasFNVSuffix`). At the end of a run the archive directory is removed with `os.Remove`, which refuses a directory still holding anything — exactly the archives a run without `--rm-tar-on-success` is meant to keep.

**Unconfirmed job creation.** A create call carries no idempotency key, and the code assumes the platform enforces no unique name a lost creation could be looked up by — which is why it has a reconciliation search for a lost *file registration* and none for a lost job creation. A request whose answer was lost therefore cannot be retried safely. Three pieces cover it:

- `api.CreateJob` returns `api.ErrJobMayExist` when the request **may** have been delivered and the answer did not come back. `isAmbiguousDelivery` decides that: a call that started on a dead context sent nothing, and from there only a dial that never connected and a name that never resolved are proof of non-delivery. A live starting context plus any other transport failure is not proof of delivery either — it is the absence of proof of non-delivery, which is what the sentinel records. A cancellation or deadline arriving mid-flight is **not** proof of non-delivery, because it cannot be told from one that arrives after the platform already acted. A 2xx whose body could not be decoded is the same case — the job exists and nothing names it.
- The pipeline checkpoints `SubmitStatusCreating` **before** the request goes out (`recordCreateIntent`), and replaces it with the outcome: a job ID, a failure, or `SubmitStatusIndeterminate`. An intent held only in memory is one a restart cannot see, so a death between delivery and the answer would otherwise leave an ordinary pending job that the next resume creates a second time — which is the position a run given no state file is in, since its whole table is in memory and dies with the process. A checkpoint that cannot be written fails the item *without sending the request*. For a fresh job that means the job certainly does not exist and the next resume simply retries it; for a job this run was authorized to **recreate**, `failBeforeCreate` instead leaves the previous unconfirmed record standing, because nothing was sent and that earlier creation is still the one someone has to check the platform for — writing a plain failure over it would produce a record the next resume creates from with no flag at all. Both statuses are `SubmitStatus` values rather than a new column, so a state file written by an older binary still loads.
- `state.MayAlreadyExist` is the one classification: no job ID, and a submit status of `creating` or `indeterminate`. Such a job is **never** created again on its own. `--recreate-indeterminate` (`PipelineOptions.RecreateIndeterminate`) is the only thing that does, and it is batch-wide by design: it creates every unconfirmed job in the batch, so the caller is asserting it has checked the platform for all of them. Nothing else sets it, and nothing in the GUI does — `core.RunOptions` has no field for it, so the PUR tab observes unconfirmed jobs and prints the flag as guidance, while the recovery itself is a CLI action (`internal/cli/pur.go`).

**State persistence and what it does not promise.** There is persistence at all only where the run named a state file. An empty path — what `pur run` without `--state` constructs — makes an in-memory manager: it holds the same table and answers every read from it, but `Load` reads nothing, both write paths (`Save` and the `UpdateState` checkpoints) touch no file and report success, and `FilePath()` answers `""`, which is what sends the archive directory to the per-process seed above. Such a run completes and leaves nothing behind: no file records which jobs were created, `pur resume` has nothing to read, and the CLI says so on stderr before the pipeline starts. Everything that follows describes a manager that was given a path. `state.Manager` hands out **snapshots**: `GetState` copies the stored row, and `UpdateState` copies the caller's snapshot back in under the write mutex, preserving the manager-owned `UploadProgress` field when the snapshot carries zero. Persistence happens inside that same lock — `saveUnlocked` writes the whole table to `<state>.tmp` and renames it over the state file — so two workers checkpointing through `UpdateState` cannot interleave a write. That is the scope of the guarantee: the public `Manager.Save` takes only a read lock, so two concurrent `Save` calls can both be writing the one `<state>.tmp` at once. No caller does that today — the two are the pipeline's feeder and the GUI's state pre-population, and they do not overlap — but the mutex is not what prevents it. What the mechanism is not, either way, is a transaction: the in-memory map is updated *before* the save is attempted, so a save that fails leaves the run holding a state the file does not have, and nothing rolls it back. Neither the temporary file nor its directory is synced before the rename, so the checkpoints below bound recovery from a crash or a restart, not from power loss.

**Durability beyond the create intent.** Two more checkpoints bound recovery, and each one's failure has a defined meaning. The upload worker checkpoints the registered file ID **before** the archive is cleaned up and before the job is created: an unrecorded file ID means a restart re-uploads, which it cannot do once `--rm-tar-on-success` has removed the archive. The job worker checkpoints the returned job ID **before** submission, because that ID is the only thing that names the created job; a row that loses it still carries the `creating` intent written before the request, which is what stops the next resume creating the job a second time. A submission that succeeds but whose state cannot be written is logged with the job ID and *not* counted done. What the file ends up holding is not settled by that failure: `checkpoint` marks the row `failed` and attempts a second write, and any later whole-table save persists the same in-memory row, so the state file may carry `failed` or may still carry `pending`, from which a resume submits the running job again. Even the `failed` outcome is only left alone on the ordinary branch — tar and upload both `success` with a job ID present, where the feeder re-queues nothing unless the submit is still pending. It is not inert everywhere: `submit-existing` (`SkipTarUpload`) and a job with no archive of its own are sent to the job queue by their own branch whatever the submit status says, and `clearStaleFailures` resets a `failed` submit marker to `pending` whenever tar or upload is not `success`. The job ID is in the log line because only the platform can settle which.

**Resume and reporting.** The feeder makes two decisions about each row before it reaches any queue branch, and both bypasses are downstream of them. First `clearStaleFailures`: every tar and upload failure path also stamps `SubmitStatus="failed"`, so a row whose earlier stage is about to run again has those markers reset to `pending`. A row whose tar and upload both succeeded is the one exception: there the marker stands, and with a job ID present the feeder re-queues nothing unless the submit is still pending, so a submit failure is that row's real, unretried outcome. Second `mayAlreadyExist`: a row the previous run could not confirm the creation of is taken out of this one entirely — logged with what to check on the platform and skipped — and only `--recreate-indeterminate` sends it on, carrying its authorization on the work item rather than clearing the status on disk. A missing job ID is therefore not read as evidence that no job was created. Past those two, the same row *without* a job ID goes to the job queue whatever its submit status says. For a row that was not explicitly authorized, having passed `mayAlreadyExist` means its status is neither `creating` nor `indeterminate`, so the creation it is queued for is one that either never went out or came back as a definite failure; a row `--recreate-indeterminate` authorized reaches the same queue still carrying its unconfirmed status, so reaching the job queue is not on its own evidence that no job was created. Rows that reach neither stage take the queue their state names — the upload queue after a successful tar, the tar queue otherwise — and the two bypasses skip both: `submit-existing` (`SkipTarUpload`) marks tar and upload `skipped` and goes straight to the job queue, as does a job with no archive of its own to build.

`SubmitMode` is a per-job field, so the pipeline's chain does not always end at "submit": `NormalizeSubmitMode` maps the mode to `submit` or `create_only`, `shouldSubmit` skips the submission for the latter, and the job worker records that row's submit status as `skipped`. `Engine.getJobStats` therefore counts `skipped` as **Completed** — a created-but-unsubmitted job is that run's intended outcome, not a failure.

Where the workers were started and have drained, the unconfirmed jobs are named in a warning of their own, unconditionally, and the run is not reported as clean — unless the run was cancelled, in which case `Run` returns nil, because a cancellation is the user's own doing. Only the returned error is suppressed there: the warning naming the unconfirmed jobs is emitted before that check, and any failure a worker already logged stands. That is not the only way out of `Run`, though. `ResolveSharedFiles` runs synchronously before any worker starts, and a failure there returns a wrapped error straight away — ahead of the archive-directory cleanup and of the unconfirmed warning alike — which is also what a cancellation arriving during a Common Files upload produces. `Engine.getJobStats` puts unconfirmed jobs in an `Unconfirmed` bucket that is neither `Completed` nor `Failed`, and `publishComplete` carries that count on the completion event, which is what the PUR tab renders.

**Upload destination and metadata.** `PipelineOptions.UploadFolderID` is the destination for **every** file the pipeline uploads — batch Common Files and each job's archive alike — and an empty value means My Library; callers resolve a folder path to an ID with `folder.ResolveOrCreatePath` before constructing the pipeline. `PipelineOptions.FileTags` are applied to every one of those uploads, by `TransferService` on the injected-uploader path and by `applyFileTags` on the CLI fallback, and a tagging failure is logged rather than fatal. Local Common Files are deduplicated by absolute path within a single `ResolveSharedFiles` call, which is per run: it is not a record that the file was uploaded once and never will be again, so a resumed batch uploads them again. Job **tags** are separate and later: the code does not rely on a `tags` field in the creation body being honoured, and instead POSTs each tag to the per-job tags endpoint once the job exists, non-fatally. Project assignment follows the same after-creation, non-fatal shape: an explicit organization code from the job spec wins over the config's, an empty one is resolved from the API key, and `AssignProjectToJob` is retried up to three times with a doubling wait capped at 60s, stopping early on `api.ErrOrgCodeUnavailable`.

**Job ingress validation.** One shared function backs the two surfaces that offer a validation step, so `pur plan` (`internal/cli/pur.go`) and the GUI's `App.ValidateJobSpec` binding refuse the same specs. It is not a gate on the pipeline: neither the CLI's `runPipeline` nor the Core Engine's `RunFromSpecsWithOptions` calls it, so a run started without passing through one of those two surfaces reaches `NewPipeline` without that check. Two narrower checks do gate the CLI's pipeline commands (`internal/cli/pur.go`). `validateWorkerCounts` refuses a `tar_workers`, `upload_workers` or `job_workers` below 1, because the count sizes both the stage's worker pool and its queue: zero starts no workers to drain a queue of zero, and a negative count panics in `make`. `validateSubmitModes` refuses a row whose `Submit` value `NormalizeSubmitMode` cannot read, calling `NormalizeSubmitMode` itself rather than restating the accepted set, since `shouldSubmit` reads a normalization error as "do not submit" and the run would otherwise create every job and submit none of them without saying so. Both run inside `loadInputs` — after the `--tar-workers`/`--upload-workers`/`--job-workers` overrides are applied, so they judge the values the pipeline would be built with, and ahead of `pur run` and `pur resume`'s `--dry-run` branch — and again in `submit-existing`, which loads its own config and CSV. The shared validator itself, `validation.ValidateJobSpec` (`internal/pur/validation/`), checks the required fields — job name, analysis code, core type, command, and positive cores-per-slot, slots and walltime — the submit mode against the same vocabulary `NormalizeSubmitMode` accepts, and the license pair: a feature name needs a positive licenses-per-job count and a count needs a feature name. That pair is checked here rather than left to `BuildJobRequest`, which refuses the same thing but runs per job after that job's archive has been built and uploaded, so a whole CSV would otherwise validate cleanly and then fail every job at the last step. A negative count is reported rather than read as "unset", since the CSV parser accepts one. `validation.CoreTypeValidator` is the API-backed half, holding the fetched core types behind a `constants.ValidationCacheTTL` (5min) cache so a batch validates against one listing. Directory-shaped ingress has its own scanner: `internal/util/multipart/` collects run directories out of one or several project directories (`CollectAllRunDirectories`, `ScanDirectories`), keeps those that contain a file matching the validation pattern where one is given (`ValidateRunDirectory`; an empty pattern accepts everything), and generates the job names, suffixing the project name where several project directories contribute. It has nothing to do with cloud multipart upload.

### 14. Local Filesystem Discovery (`internal/localfs/`)

**Purpose**: one implementation of listing and walking the local filesystem, so no caller grows its own idea of what a directory contains.

`ListDirectoryEx(ctx, path, opts)` is the single-directory listing behind the GUI's `App.ListLocalDirectoryEx(path, includeHidden)`. `WalkStream(ctx, root, opts)` and `WalkCollect(root, opts)` are the recursive walks in `internal/transfer/folder`, which is what both the CLI's folder upload and the GUI's build their batches from — so a folder upload enumerates the same entries whichever surface started it. Four behaviours are worth stating because they decide what a transfer sees:

- **Hidden entries** are excluded by default. `WalkOptions.SkipHiddenDirs` additionally stops the walk descending into a hidden directory at all, rather than descending and filtering.
- **Symlinks** are skipped entirely unless `FollowSymlinks` is set. When it is, a symlinked **file** is followed and its target's size and modification time are used, while a symlinked **directory** is descended only after a cycle check. That check is by **ancestry**, not by a global visited set: each directory's device+inode identity is tracked against its own chain of ancestors, so a link back into an ancestor is caught while the same directory reached down two different branches is not treated as a cycle. `getDirIdentity` reports no identity on Windows, so there the directory case has no cycle check to pass and is never descended — the entry is reported as skipped instead. File symlinks are still followed there.
- **The initial directory read runs under a timeout** when one is configured, in a goroutine, so an unresponsive mount cannot wedge `ListDirectoryEx` at its `os.ReadDir`. The bound stops there: the per-entry `entry.Info()` and the `os.Stat` each parallel symlink resolution makes can still block, and the resolver waits on its workers without a timeout of its own. `ListDirectoryEx` resolves a listing's symlinks in parallel when asked to, since each resolution is an independent stat.
- **Some skipped entries are propagated**, rather than all of them: `WalkStream` returns a fourth `skipped` channel alongside directories, files and errors, and that count is what surfaces as `Skipped` on the batch progress event, so a folder transfer can report entries the walker could not follow instead of quietly transferring fewer files than the user selected. What it carries is the cases a caller could not otherwise diagnose — a directory symlink whose identity the platform will not report, and a Windows reparse point that `Lstat` did not call a symlink but `Stat` resolves to a directory. A broken symlink, a cycle and an entry that could not be stat'd are skipped without a notification. Delivery is also non-blocking, so a full channel drops the notification rather than stalling the walk.

---

## CLI Compatibility Mode

**Package**: `internal/cli/compat/` (17 source files, plus 9 of tests)

Aims at drop-in compatibility with `rescale-cli` (the legacy Java-based Rescale CLI), so that existing scripts keep their flag syntax for the commands compat mode implements. That aim is the package's own — `NewCompatRootCmd` (`root.go`) documents the tree as mirroring rescale-cli's flag interface — and what the source establishes is the flag surface Interlink itself implements, not a verified equivalence with the other tool. It is deliberately partial, not a full replacement. The `spub` command and its five subcommands are registered and then fail (see below), and two root flags are accepted and ignored — `--no-ssl-verify` and `--enableErrorTracking`, both hidden (`internal/cli/compat/root.go`).

### Detection and Activation

`IsCompatMode()` in `compat.go` activates when:
1. `--compat` appears anywhere after the program name
2. The program name's base is exactly `rescale-cli` or `rescale-cli.exe` (symlink or rename)

When active, `cmd/rescale-int/main.go` dispatches to `compat.ExecuteCompat()` instead of the native CLI.

### Architecture

Compat mode builds a **separate Cobra command tree** (`NewCompatRootCmd()` in `root.go`), written to mirror rescale-cli's flag syntax. It imports `config`, `api`, `models`, `watch` and `version` directly, plus the shared transfer stack (`cloud/{credentials,download,upload}`, `transfer`, `resources`, `progress`, `http`, `pur/parser`, `util/{analysis,glob}`, `validation`, `constants`) — it does NOT import the `cli` package, which is what avoids the import cycle.

**Credential resolution chain** (independent from native CLI):
1. `-p/--api-token` flag
2. `RESCALE_API_KEY` env var
3. `apiconfig` INI profile (`--profile` section or `[default]`)

**Argument normalization** (`NormalizeCompatArgs()` in `compat.go`):
- Multi-char short flags: `-fid` → `--file-id`, `-lh` → `--load-hours`
- Multi-value expansion of `-f`, `--files` and `--file-matcher`, for `upload` and `submit` only: `upload -f a b c` → `upload -f a -f b -f c`. The subcommand is detected by skipping the root flags that consume a value (`-p`/`--api-token`, `-X`/`--api-base-url`, `--profile`)

### Implemented Commands (10)

`status`, `stop`, `delete`, `check-for-update`, `list-info`, `upload`, `download-file`, `submit`, `list-files`, `sync`

A `spub` command and its five subcommands (`register`, `upload`, `validate`, `list`, `status`) are registered as placeholders in `placeholder.go`. They parse whatever they are given and then fail through the standard error path, so a script that reaches one gets exit 33 and a message naming the command rather than "unknown command".

### Submission Staging

`compat submit` does not use the PUR tar flow. It stages the job's inputs into a temporary directory — the script as `run.sh`, every other input file zipped into `input.zip` — uploads exactly those two artifacts, and then rewrites the first job analysis to run `./run.sh` with both file IDs attached and `Decompress` set on each, before creating and submitting the job (`internal/cli/compat/submit.go`). The command the job runs is therefore the staged script rather than anything the caller passed, and that `Decompress` flag is the request for both artifacts to be unpacked into the job's working directory — the platform's own step rather than anything this code performs.

### Behavioral Fidelity

- Exit code 33 on error (`ExitCodeCompatError`, which the constant's own comment gives as matching the rescale-cli convention)
- SLF4J-style timestamp format (`FormatSLF4JTimestamp`), used for the authentication line and for error messages
- Extended JSON output via `-e`/`--extended-output`, on `status`, `upload`, `download-file` and `submit`
- Quiet mode (`-q`, persistent on the root command) suppresses informational output but not data/errors

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

`Completed`, `Failed`, `Stopped`, `Force Stopped`, `Terminated` — the `TerminalStatuses` map, a unified superset used by every poll loop in the CLI, the compat layer and the watch engine, so a status is terminal in all of them or in none.

Terminal is not success. `StatusCompleted` is the only one of the five that means the job produced the result it was asked for; the other four end the job without one. A caller that polls until a job finishes and then reports success would call a stopped job successful, which is why the distinction is a named constant rather than a convention.

---

## GUI Architecture (Wails)

### Backend Bindings (`internal/wailsapp/`)

The groups below are the binding surface by area, not an exhaustive method list.

1. **App** (`app.go`): Main Wails application struct with lifecycle hooks, plus stand-alone bindings such as `ClearCatalogCache()`
2. **Transfer Bindings** (`transfer_bindings.go`): `StartTransfers()`, `CancelTransfer()`, `RetryTransfer()`, `GetTransferStats()`, `GetTransferTasks()`, `GetUngroupedTransferTasks()`, `ClearCompletedTransfers()`, `GetTransferBatches()`, `CancelBatch()`, `RetryFailedInBatch()`, `GetBatchTasks(batchID, offset, limit, stateFilter)` — the paged reader the Transfers tab uses so rendering a batch's rows does not copy the whole batch, DTOs. There is deliberately no queue-wide cancel binding; see [Transfer Batch Abstraction](#6-transfer-batch-abstraction-internaltransferbatchgo)
3. **File Bindings** (`file_bindings.go`): `ListLocalDirectoryEx(path, includeHidden)`, `ListRemoteFolder()`, `ListRemoteFolderPage()`, `SearchRemoteFolderContents()`, `ListRemoteLegacy()`, `ListRemoteLegacyWithFilters(cursor, pageSize, ownerFilter, searchQuery, sortField, sortDirection)`, `ListRemoteTrash()`, `RecoverTrashItems()`, `PurgeTrashItems()`, `CreateRemoteFolder()`, `DeleteRemoteItems()`, `CheckFoldersExistForUpload()`, `StartFolderDownload()`, `StartFolderUpload()`
4. **Job Bindings** (`job_bindings.go`): `ScanDirectory()` — one entry point for both scan modes, dispatching on `ScanOptionsDTO.ScanMode == "files"` — plus `StartBulkRunWithOptions()`, `StartSingleJob()`, `CancelRun()`, `GetRunStatus()`, `GetJobRows()`, `GetRunHistory()`, `GetHistoricalJobRows()`, the catalog readers (`GetCoreTypes()`, `GetAnalysisCodes()`, `GetAutomations()`, `GetProjects()`), and the template load/save surface (CSV, JSON and SGE)
5. **Job Status Bindings** (`job_status_bindings.go`): `ListJobStatuses()` for the first page and `ListJobStatusesPage(offset)` for subsequent pages, backing the Job Status tab
6. **Config Bindings** (`config_bindings.go`): Configuration management
7. **Daemon Bindings** (`daemon_bindings.go`, `daemon_bindings_common.go`, `daemon_bindings_windows.go`): daemon IPC and configuration. `daemon_bindings_common.go` is always compiled and holds the pre-flight and configuration readers (`ValidateAutoDownloadPreFlight`, `ValidateAutoDownloadSetup`, `GetDaemonConfig`, `GetDefaultDownloadFolder`, the file-logging pair) and the three daemon transfer controls the Transfers tab routes to — `CancelDaemonBatch`, `CancelDaemonTransfer`, `RetryFailedInDaemonBatch`. `daemon_bindings.go` (`//go:build !windows`) and `daemon_bindings_windows.go` (`//go:build windows`) are the two implementations of the same lifecycle, service and snapshot surface — `StartDaemon`, `StopDaemon`, `PauseDaemon`, `ResumeDaemon`, `TriggerDaemonScan`, `TriggerProfileRescan`, `ReloadDaemonConfig`, `SaveDaemonConfig`, `GetDaemonStatus`, `GetDaemonTransferSnapshot`, `GetDaemonLogs`, `GetServiceStatus` and the elevated service actions — over a subprocess daemon and over the Windows service respectively
8. **Event Bridge** (`event_bridge.go`): Forwards EventBus events to Wails runtime, throttles progress updates (100ms interval)
9. **Version Bindings** (`version_bindings.go`): GitHub update check
10. **Reporting Bindings** (`reporting_bindings.go`): Error report display
11. **API Key Source Bindings** (`api_key_source_bindings.go`): Reports back to the GUI which credential source the runtime resolved (token file vs. env vs. config) and any source conflicts
12. **DOE Bindings** (`doe_bindings.go`): `GetDOEMethods()` for the method menu, `PreviewDOECases()` for the debounced live preview (truncated, no job specs), `GenerateDOE()` for the full sweep plus its job specs, `ParseDOECasesCSV()` for pasted explicit cases, `DefaultDOEMaxCases()` for the cap the form shows

**Deleting is two different operations.** `FileService` (`internal/services/file_service.go`) backs the file bindings, and the remote pane's delete is a **soft** one: `ArchiveItems` moves the selection to the user's Trash through the folder-scoped archive endpoint, which is why it takes the parent folder the items currently live in. What the client itself checks is only that a parent was supplied; rejecting a mismatched one and applying the batch atomically are contracts of the endpoint that the code is written against rather than anything it verifies. What the service guarantees is its own report: an API error is reported as zero moved and every item failed, without any reconciliation of what the platform did with the request. `DeleteFile` and `DeleteFolder` are the permanent path and are not what a user-initiated delete reaches.

Trash then addresses its contents by a different identity. A trashed **file** is listed as a filesymlink, and `RecoverTrashItems` and `PurgeTrashItems` send that `SymlinkID` rather than the file ID — an item without one is refused before the call rather than sent under the wrong identifier — while folders are sent as ordinary folder IDs. Both are single bulk POSTs, and both rest on the same assumed endpoint contract: an error is reported as nothing recovered or purged.

### Frontend Stores (`frontend/src/stores/`)

1. **jobStore** — PUR workflow configuration state machine
2. **runStore** — Active run monitoring, event subscriptions, polling, queue, restart recovery
3. **singleJobStore** — Single Job form state persisted across tab navigation
4. **configStore** — API configuration and connection state
5. **transferStore** — Transfer queue tracking with batch grouping and disk space error classification
6. **logStore** — Activity log entries with level-aware trimming
7. **fileBrowserStore** — File Browser state, including the four remote browse modes (My Library, My Jobs, Legacy, Trash), the search/owner/sort controls, and selection bookkeeping. A search debounce is scoped to its browse mode so a pending one cannot land on the other mode's listing.
8. **errorReportStore** — Pending error-report dialog state (current report, redacted details, modal visibility). Drops events whose class the backend already treats as user-fixable (`disk_space`, `auth`, `client_error`, `network`, `timeout`, `local_fs`) and will not reopen for the same `errorID` within a minute, so a duplicated event cannot interrupt repeatedly.
9. **rateLimitStore** — Footer rate-limit indicator, driven by `stage: 'rate-limit'` log events with a lingering window so it stays steady across a burst instead of flickering

### Frontend Components (`frontend/src/components/tabs/`)

1. **FileBrowserTab** — Two-pane local/remote file browser. Remote pane has four browse modes: My Library, My Jobs, Legacy, and Trash (soft-deleted entries with restore/purge actions). Search by name, owner filter (own files / shared with me), and sort by name, size or upload date, with the pagination cursor carried through search. Upload is disabled in Trash and My Jobs modes with an explicit reason.
2. **TransfersTab** — Transfer progress with batch grouping, cancel/retry, disk space error banner. Batch rows are read a page at a time via `GetBatchTasks()`. Polling is gated on the tab being active.
3. **SingleJobTab** — Job template builder with three input modes (directory, local files, remote files). A three-step state machine (`initial` → `jobConfigured` → `inputsReady`) with Back navigation; the form lives in `singleJobStore`, so stepping back or leaving the tab preserves what was entered.
4. **PURTab** — Batch job pipeline with three job sources (folder scan, file scan, or a parameter sweep) and its own `goBack`/`canGoBack` workflow navigation. `effectiveView` resolves to one of four views — `configure`, `choice`, `monitor`, `results` — from the store's three-valued `purViewMode` (`auto`, `monitor`, `configure`) and the active run's state. A finished run's headline is decided in that order: cancellation first, then failures, and only then "Pipeline Finished with Unconfirmed Creations" for a run whose sole problem is jobs whose creation could not be confirmed. Those jobs are counted separately and kept out of the failure panel, because only someone who checks the platform can say which they are; a dedicated panel gives the count and the recovery guidance, while the names appear in the shared `JobsTable` alongside every other row. The tab observes unconfirmed creations — it does not expose `--recreate-indeterminate`, which is a CLI action. See [PUR Pipeline](#13-pur-pipeline-internalpurpipeline)
5. **JobStatusTab** — Paged listing of the user's most recent jobs (50 per page) with status badges, dates, and a name/ID filter. Fetches are generation-counted so a stale response cannot overwrite a newer one or wedge the loading state, and jobs whose status could not be fetched are surfaced as a warning rather than silently omitted.
6. **SetupTab** — API settings, proxy configuration, logging, auto-download daemon
7. **ActivityTab** — Logs with level filtering, run history with expandable job tables

### Frontend Shared Widgets (`frontend/src/components/widgets/`)

`JobsTable`, `StatsBar`, `PipelineStageSummary`, `PipelineLogPanel`, `ErrorSummary`, `StatusBadge`, `FileList`, `LocalBrowser`, `RemoteBrowser`, `RemoteFilePicker`, `TemplateBuilder`, `DOEBuilder`

---

## Encryption & Security

### AES-256-CBC Encryption (`internal/crypto/`)

**Specifications**:
- **Algorithm**: AES-256-CBC (Cipher Block Chaining)
- **Key Size**: 256-bit (32 bytes, `encryption.KeySize`)
- **IV Size**: 128-bit (16 bytes, `encryption.IVSize`)
- **Padding**: PKCS7 (adds 1-16 bytes)
- **Hash Function**: SHA-512 for file integrity

Two things are called "streaming" here and they bound memory differently:

- **Whole-file encryption and decryption** (`EncryptFile`, `DecryptFileWithHash` in `internal/crypto/encryption.go`) read and write in 16KB chunks from a pooled buffer (`constants.EncryptionChunkSize`, `internal/util/buffers/`), so memory is constant regardless of file size. This is the `--pre-encrypt` upload path and the legacy (v0) download path.
- **The streaming upload and download default** (`internal/crypto/streaming.go`) works a *part* at a time — `CBCStreamingEncryptor.EncryptPart` takes one whole plaintext part — so its footprint is the part size the upload plan chose (16-64MB, or larger for very large files), multiplied by the workers and queue slots that plan reserved. Bounding it is the planner's job, not the cipher's: see [Storage Backends](#storage-backends).

**Encryption Modes**:
- **Default (streaming)**: AES-256-CBC chained across parts during upload. One key and one initial IV cover the whole object: part N's IV is the last ciphertext block of part N-1, and PKCS7 padding is applied only to the final part — so the combined ciphertext is identical to whole-file AES-256-CBC, which is the property the format is built to preserve for whatever decrypts it. No temporary encrypted file. The two halves of that live in different places: the **object's** metadata carries `iv`, `streamingformat=cbc` and `partsize` (`{s3,azure}/streaming_concurrent.go`), while the AES key travels in the Rescale file-registration record as `EncodedEncryptionKey` (`internal/cloud/upload/upload.go`) — it is never written to object metadata.
- **Legacy (`--pre-encrypt`)**: Full-file encryption before upload, writing only `iv` to the object's metadata. The mode exists for readers that predate the streaming format; which of those it is compatible with is not something this repository establishes.
- **Legacy read path (v3.1.x)**: per-part HKDF-SHA256 key and IV derivation. Still readable on download (format version 1); never written by new uploads.

### File Permissions Security

State files containing sensitive data (encryption keys, IVs, master keys) are created with `0600` permissions:
- Upload and download resume files (`<file>.upload.resume`, `<file>.download.resume`), written to a temporary name and renamed into place
- The upload lock (`<file>.upload.lock`) and the reclamation claim file beside it
- Daemon state, the daemon's PID file, and the Unix IPC socket
- Token file, `apiconfig`, and `daemon.conf`

`WriteTokenFile` re-applies the mode with `Chmod` after writing, because `os.WriteFile` only applies its mode when it creates the file — an existing token file would otherwise keep whatever permissions it already had. On Windows a POSIX mode means little, so the same call site additionally replaces the file's DACL with a protected one granting full control to the current user's SID, `BUILTIN\Administrators` and `NT AUTHORITY\SYSTEM` — not owner-only, and inheritance from the parent directory is switched off so a misconfigured parent cannot widen it. That tightening is **best effort**: a SID lookup that fails and an ACL that cannot be applied each print a warning to stderr and let the write succeed, leaving the file with whatever DACL it already carried or inherited from its parent directory — the failed tightening does not reset it to any known default — and a `Chmod` error on Windows is non-fatal for the same reason (`internal/config/csv_config.go`, `internal/config/token_acl_windows.go`).

### Windows IPC Security

Named pipe authorization with per-user SID matching. See SECURITY.md for details.

### Daemon Transfer Visibility

The daemon auto-download process routes all downloads through the same `TransferService` the GUI uses; there is no parallel transfer implementation inside `internal/daemon/`. GUI visibility is via IPC-based observation:
- `Daemon.TransferService()` + `Daemon.Queue()` expose the shared machinery. IPC polling reads live task and batch state via `MsgGetTransferStatus` → `DaemonTransferSnapshot{Tasks, Batches}`.
- The main Transfers tab renders daemon rows alongside GUI rows with a `Daemon` badge; per-row Cancel/Retry routes by `sourceLabel` through IPC commands (`MsgCancelDaemonBatch`, `MsgCancelDaemonTransfer`, `MsgRetryFailedInDaemonBatch`).
- Works in both subprocess mode (macOS/Linux) and Windows service mode; service-mode routing goes through `MultiUserDaemon.userDaemon(...)` to the correct per-user daemon.

### Daemon Status Reporting

A background process the user cannot see needs a way to say it is broken, otherwise a failing scan (expired key, dead network, proxy trouble) shows only as a last-scan timestamp that stops advancing:

- The status snapshot carries the most recent scan failure, its code and when it happened, cleared by the next scan that completes. `daemon status` prints it with the age and an actionable hint; the Setup tab renders the same error text and how stale it is.
- Actions that could not happen do not report success. `TriggerPoll` returns *why* no scan started — stopped, paused, or a poll already running, which is also what a wedged poll looks like — and the IPC handlers and the Windows multi-daemon path propagate that rather than swallowing it, so "Scan triggered" in the GUI means a scan started.
- "Save all settings" asks the running daemon to reload and reports what happened, rather than reporting the file write alone: writing `daemon.conf` leaves a running daemon on its old settings until it reloads.
- Pre-flight validates the download folder with the same write probe `SaveDaemonConfig` gates on, rather than a stat: a folder that exists but cannot be written to would otherwise pass setup and fail every download.

### Daemon Eligibility and Completion

A job reaches the eligibility check only after `Monitor.FindCompletedJobs` has already filtered for it. That pass keeps jobs whose status is exactly `Completed`; suppresses jobs whose files are on disk but whose downloaded-marker tag has not yet been applied, so the poll loop's separate tag-retry pass cannot see them re-enqueued; drops jobs created before a cutoff of lookback plus 30 days, which is an API-call optimisation rather than the lookback itself; and applies the configured name filters. Every drop is bucketed by a stable `SkipReasonCode` so the per-poll scan summary can decide which are worth a log line.

What is then downloaded is decided per job by `Monitor.CheckEligibility`, tag first (`internal/daemon/monitor.go`):

1. The downloaded-marker tag is authoritative and checked first — it is the common answer on every poll, and revoking it in the Rescale web UI is what triggers a re-download on the next one. Its literal value is `autoDownloaded:true` (`config.DownloadedTag`), which is the name it appears under in the web UI.
2. The job's **Auto Download** custom field: disabled or empty is a silent skip, and so is an unrecognised value. Field-lookup failures are silent too, because a workspace without that field would otherwise log a line per job per poll.
3. **Enabled** is eligible outright.
4. **Conditional** with no conditional tag configured is also eligible; with one configured (`AutoDownloadTag`, default `autoDownload`), that tag decides.

The lookback window (`LookbackDays`, default 7) narrows what the scan considers, but it is not unconditional: a lookback of zero or less disables the window entirely, and where it is on, the filter runs against the job's **completion** time rather than its creation time. A completion time that cannot be read is not a reason to drop the job — the scan admits it, since it has already passed the creation prefilter, and a long-running job would otherwise be skipped by an argument about when it started.

Completion is recorded on both sides. Locally the job is recorded as downloaded; remotely the downloaded-marker tag is applied, and that tag update is retried on its own so a failure to tag does not cost the download. A job whose file tasks did not all succeed is not recorded as *downloaded*, but it is recorded: any failed or cancelled task sends it through `MarkFailed` instead, which stores the job's name, the error, an incremented retry count and the time of the attempt, so the next poll picks it up again. Finished download batches keep their tasks in the shared transfer queue only up to `daemonBatchHistoryLimit` (20) — the queue never removes a terminal task, so without that bound a daemon polling for weeks would accumulate one per file it ever downloaded.

**What a poll is bounded by.** The scan phase — listing jobs and checking their eligibility — has a `scanBudget` of ten minutes, deliberately longer than the HTTP client's own 300s timeout so a slow call can still be retried inside it. Downloading is *not* charged to that budget: the elapsed download time is subtracted before the check, and each transfer is given the daemon's lifecycle context rather than a scan context, because a large file legitimately outlasts the scan and a budget-killed download would interrupt a legitimate long transfer and can force retransmission. Each eligibility check gets a two-minute context of its own, parented on the lifecycle context for the same reason. The two ways a poll can end short leave different records. The budget check runs inside the candidate loop, before each candidate rather than after the last one: when it fires it records the overrun as a scan error, emits its summary and persists the progress it made, so the last-scan timestamp advances with the failure attached rather than freezing. A timeout or an error returned by `FindCompletedJobs` is the other exit — it records the error and emits the same summary line with every count at zero, but returns *without* persisting, since that poll never got past listing jobs and has no progress to record.

State is bounded too. The state file is read once, when the daemon is constructed; from there the daemon works on the in-memory state and writes it back — after a poll saves its progress, after each download outcome, and on shutdown. Where a positive lookback is configured, the persisted download record is retained for the lookback plus the same 30-day buffer `FindCompletedJobs` uses for its creation pre-filter; a non-positive retention disables pruning altogether and keeps everything. Pruning runs inside `State.Save`, and the cutoff is on the local `DownloadedAt` — which `MarkFailed` also resets to the time of the failed attempt, so a job that keeps failing keeps its entry. An entry still owing a tag call is exempt, since losing the pending-tag flag would let the job be downloaded a second time. What pruning bounds is the file's growth, not what a scan can select: candidate filtering runs on the platform's own creation and completion times, and a job whose creation timestamp is missing or unparseable and whose completion time cannot be read passes both filters, so a dropped entry is not a guarantee that no later scan can pick the job up again.

`alreadyDownloaded` decides whether an existing file counts as finished, and size alone is not enough: an interrupted download leaves a full-size file, and adopting one is permanent because every later poll adopts it too. So when the remote file carries a SHA-512, the local file must hash to it — or match a verification this daemon already made, cached by path, size, modification time and expected checksum. Only a file whose remote record carries no checksum is adopted on length alone, which is the same test every other presence check in the product makes.

---

## Storage Backends

### Unified Backend Architecture

All transfer operations (uploads and downloads) from both CLI and GUI converge to a single shared backend:

```
┌───────────────────────────────────────────────────────────┐
│                       ENTRY POINTS                        │
│  CLI: upload, download, folders upload-dir/download-dir,  │
│       jobs download, daemon auto-download                 │
│  GUI: File Browser, Single Job, PUR Pipeline              │
└───────────────────────┬───────────────────────────────────┘
                        │
                        ▼
┌───────────────────────────────────────────────────────────┐
│               UNIFIED ENTRY POINTS                        │
│  upload.UploadFile()         download.DownloadFile()      │
│  internal/cloud/upload/      internal/cloud/download/     │
└───────────────────────┬───────────────────────────────────┘
                        │
                        ▼
┌───────────────────────────────────────────────────────────┐
│                   PROVIDER FACTORY                        │
│     providers.NewFactory().NewTransferFromStorageInfo()   │
│                 providers/factory.go                      │
└──────────────┬─────────────────────────┬──────────────────┘
               │                         │
               ▼                         ▼
┌──────────────────────┐  ┌───────────────────────┐
│    S3 Provider       │  │    Azure Provider     │
│  (providers/s3/)     │  │  (providers/azure/)   │
│  5 src, 7 ifaces     │  │  5 src, 7 ifaces      │
└──────────┬───────────┘  └──────────┬────────────┘
           └──────────┬──────────────┘
                      ▼
┌───────────────────────────────────────────────────────────┐
│               SHARED ORCHESTRATION                        │
│  transfer/downloader.go       - Download orchestration    │
│  transfer/uploader.go         - Upload interfaces/plans   │
│  transfer/streaming.go        - Streaming upload handle   │
│  transfer/part_pipeline.go    - Concurrent part staging   │
│  transfer/provider_download.go- Range fetch, version pin  │
│  transfer/retry.go            - Retry, refresh, ranges    │
│  transfer/progress.go         - Progress aggregation      │
│  transfer/format.go           - Object-metadata decoding  │
│  state/upload.go              - Upload resume + locking   │
│  state/download.go            - Download resume state     │
│  state/liveness*, piddomain*  - Lock-owner probing        │
└───────────────────────────────────────────────────────────┘
```

**Key Files:**
- Entry points: `internal/cloud/upload/upload.go`, `internal/cloud/download/download.go`
- Providers: `internal/cloud/providers/s3/`, `internal/cloud/providers/azure/`
- Orchestration: `internal/cloud/transfer/`
- State: `internal/cloud/state/`

**Provider Interfaces (7)**: each `Provider` carries a compile-time assertion for all seven, so the two backends are structurally identical:

| Interface | Declared in | Asserted at |
|---|---|---|
| `cloud.CloudTransfer` | `internal/cloud/interfaces.go` | `s3/provider.go`, `azure/provider.go` |
| `cloud.RetryObserverSetter` | `internal/cloud/notice.go` | `s3/provider.go`, `azure/provider.go` |
| `transfer.StreamingConcurrentUploader` | `internal/cloud/transfer/uploader.go` | `{s3,azure}/streaming_concurrent.go` |
| `transfer.PreEncryptUploader` | `internal/cloud/transfer/uploader.go` | `{s3,azure}/pre_encrypt.go` |
| `transfer.StreamingConcurrentDownloader` | `internal/cloud/transfer/downloader.go` | `{s3,azure}/streaming_concurrent.go` |
| `transfer.StreamingPartDownloader` | `internal/cloud/transfer/downloader.go` | `{s3,azure}/streaming_concurrent.go` |
| `transfer.LegacyDownloader` | `internal/cloud/transfer/downloader.go` | `{s3,azure}/download.go` |

An eighth capability, `AbortUploadByID(ctx, uploadID, storagePath)`, is resolved by runtime type assertion rather than declared in that table (`backendUploadAborter` in `internal/cloud/upload/upload.go`). Both providers implement it; the assertion is what lets a provider that cannot discard an upload by recorded identity simply not offer it, instead of having to.

`internal/cloud/storage/` is a different thing despite the name: it holds only the shared storage error types (`IsDiskFullError` and friends). `internal/cloud/transfer/downloader.go` is its single importer.

**Storage Backend Parity**:
- Both S3 and Azure assert the same seven interfaces above
- Same part sizing: neither provider picks its own. `resources.PlanUpload` sizes every upload, streaming or `--pre-encrypt`, and returns an `UploadPlan{PartSize, WorkerCap, QueueDepth}` (`internal/cloud/transfer/uploader.go`, `internal/cloud/upload/upload.go`). Planning always precedes the *network* transfer, but not all local I/O: the streaming path plans before it reads anything, while `--pre-encrypt` writes the whole ciphertext to a local temp file first and plans against that file's actual size, so an oversize file is refused after a full local encryption pass. Part size starts at the file-size tier (16MB under 100MB, 32MB to 1GB, 48MB to 5GB, 64MB above — 48MB is a literal in `resources`, the others are `constants.MinChunkSize`/`ChunkSize`/`MaxChunkSize`), is clamped so `chunk * threads * 2` fits in 75% of the memory budget, and is then raised to the **part-count floor** — `ceil(fileSize / MaxParts)` rounded up to a whole MB — whenever the tier would need more parts than the backend accepts. The floor overrides both the memory clamp and `MaxChunkSize`, so the ceiling on file size is the backend's own per-part limit rather than the 64MB tier's 625 GB (S3, 10,000 parts) and 3,125 GB (Azure, 50,000 blocks). When even the floor exceeds the backend's per-part limit, the upload is refused up front and names the largest file that backend can take, rather than failing after every byte has moved. The **streaming** paths stamp the part size into the object's `partsize` metadata and CBC chains through it, so it cannot be renegotiated partway; `--pre-encrypt` writes `iv` alone, because the platform decrypts that object as one whole-file ciphertext
- Same memory budget: `(*Manager).PlanUpload` reserves each plan's peak part-buffer memory against one shared budget keyed by transfer ID, so concurrent uploads narrow each other's pipeline (queue depth first, then worker count) instead of each claiming the whole machine. The worker floor is `min(requestedThreads, UploadMinThrottledWorkers)`, and it collapses to 1 when even that will not fit — with parts of several hundred megabytes, insisting on four workers would not make an upload slow, it would make it impossible. Because each contended transfer is bounded by that minimum rather than by what is left, concurrent minimum reservations can together exceed the budget; losing a race for memory is meant to narrow an upload, not fail it. A call made without a transfer handle has no pool to share and takes the non-reserving `resources.PlanUpload`, whose plan is advisory. `resources.CalculateDynamicChunkSize` is the tier-plus-memory step inside the planner; nothing in the transfer path calls it directly. Downloads size parts with `resources.ChunkSizeFromFileSize`, the same tier table without the memory clamp, for objects whose metadata carries no `partsize`
- Same concurrency model via orchestration layer
- Same resume behaviour: both upload modes checkpoint and both continue an interrupted attempt, on either backend. Which artifacts they keep and what disqualifies them is [Transfer Integrity](#transfer-integrity)
- Transparent to user (auto-detected via provider factory)

### S3 Backend (`internal/cloud/providers/s3/`)

Multi-part upload API. The streaming default always creates a multipart upload, whatever the file size — `constants.MultipartThreshold` (100MB) never reaches it; it only splits a `--pre-encrypt` upload between multipart and a single `PutObject`, and decides whether a legacy download is chunked. Part size from the upload plan as above, concurrent part uploads, credential caching via `EnsureFreshCredentials()`, automatic retry with exponential backoff, seekable upload streams for SDK retry.

`UploadLimits` reports 10,000 parts and a per-part ceiling of `MaxS3PlaintextPartSize` — S3's 5GB limit less one MiB. The margin is CBC's: the final part's ciphertext is up to one AES block longer than its plaintext, and the planner sizes *plaintext* parts, so a plan that used the raw 5GB would produce a final part the backend rejects.

### Azure Backend (`internal/cloud/providers/azure/`)

Block blob API, block size from the same upload plan, concurrent block upload, automatic credential refresh, same seven interfaces as S3 for consistency. `UploadLimits` reports 50,000 blocks and `MaxAzurePlaintextBlockSize` — the 4000MB large-block ceiling less the same one-MiB CBC margin. The code assumes the two backends refuse a too-long list at different moments — S3 rejecting part number 10,001 as it arrives, Azure accepting every block and failing at `CommitBlockList` once the whole file has been staged (`internal/constants/app.go`, `{s3,azure}/streaming_concurrent.go`). Neither is exercised here, because the planner refuses such a part count up front; that up-front refusal is the reason the part-count floor is computed before the transfer starts rather than being discovered by either backend.

---

## Transfer Integrity

A transfer that is interrupted, cancelled, retried or run twice must not be able to produce a registered file that nobody uploaded. The mechanisms below all answer the same question — what evidence establishes that these bytes belong to this object — and they refuse when the evidence is missing rather than assuming.

### Upload Locking (`internal/cloud/state/upload.go`)

An upload records its progress in a sidecar beside the source file, and that sidecar names a multipart upload on the backend. Two invocations that both read it would fill the same object and each abort the other's upload on the way out. One invocation at a time therefore owns a source's resume lifecycle, and the lock is taken before anything is loaded, restored or abandoned. Its extent is that lifecycle and no more: it is acquired inside `uploadStreaming`/`uploadPreEncrypt` and released when those return, so the SHA-512 pass over the source and the file registration that follow — both in the outer `UploadFile` — run with no lock held.

**Ownership is established by creating `<file>.upload.lock` with `O_CREATE|O_EXCL`.** That is the only step of an acquisition two acquirers cannot both win. Writing a temporary file and renaming it over the pathname cannot exclude anyone, because rename replaces whatever is there and both acquirers would believe they had won.

The record inside the lock carries the owning process's PID, a random per-run **owner token**, the **host** and **user** it runs as, the **PID domain** it is numbered in, when it was acquired, and the source path. The token is what a PID cannot do on its own: the OS hands a dead process's PID to a new one, so a lock left by a crashed run can name the PID of the run that finds it.

The lock path is canonicalised — made absolute and resolved through symlinks — so two spellings of one file name one lock, and an in-process `heldLocks` map (case-folded on Windows) refuses a second transfer of the same source inside this process, which the file alone cannot do since our own PID is by definition alive. The canonicalisation is best effort: `Abs` and `EvalSymlinks` are each used only if they succeed, and it resolves *names*, not file identities, so two hard links to one inode are two sources with two locks.

**An existing lock is taken over only on positive evidence that its owner is gone, never because it is old.** A multi-hour upload is still an owner. Three things have to hold:

1. **The record must name this PID domain.** A PID means something only inside the set of processes it is numbered among, and nothing this program writes down can name that set — a home directory mounted on two machines would hand both the same identifier, after which each reads the other's live PIDs as dead. The name comes from the operating system: on Linux the machine-id (validated as systemd defines it, so an empty or `uninitialized` one is refused) plus the PID namespace at `/proc/self/ns/pid`; on macOS the `kern.bootsessionuuid` sysctl, which names the boot rather than the machine; on Windows the `MachineGuid` from the 64-bit registry view. On any other platform there is no such identifier, so locks are created and released but none is ever reclaimed. A record naming no domain — written before this version, or by a process whose system would not say — is never reclaimed either.
2. **The owner's process must be provably gone.** Liveness is three-valued: alive, dead, or unknown, with unknown as the zero value so a probe that says nothing refuses the reclamation rather than authorising it. On Unix the null signal answers, and `EPERM` counts as alive — the refusal is itself evidence the process is there. On Windows `OpenProcess` with `PROCESS_QUERY_LIMITED_INFORMATION` is followed by reading the exit code, because a handle outlives the process it names; access denied reads as **alive**, since a live process of another login or an elevated one answers exactly that, and reading it as absence is what clears a running upload's lock. An exit code of 259 — the value of `STILL_ACTIVE` — also reads as alive, because a process that genuinely exited with that code cannot be told from one that has not exited; refusing is the conservative side.

   One record is reclaimed with **no liveness probe at all**: one naming *this* PID with a *different* owner token. This run's own record — same PID and same token — is refused earlier, on disk, with its own message, so what is left under that PID is a released lock of ours or a PID the OS has since handed us. Probing it would always answer "alive" and wedge every retry after a crash.
3. **The reclaimer must win a claim on the record it judged.** Judging is a read and replacing is a write, and the pathname alone keeps no second reclaimer out in between. The claim is an `O_EXCL` create of `<file>.upload.lock.stale-<hex>`, where the hex is a digest of the judged record: exactly one reclaimer of that record can hold it, it is taken before the lock file is touched, and a reclaimer that loses has changed nothing. Holding it, the reclaimer re-reads the lock; anything other than the record it judged means another acquirer has already been through, and the pathname is judged again from scratch. `lockTakeoverAttempts` bounds that loop at 8.

Refusals name what to do, with whatever the record actually holds. A lock in another PID domain, and one whose owner cannot be probed, name the source file plus the process, host, user and acquisition time recorded in it, and the path of the lock file to delete if that upload is not running. A **live owner** is the one refusal that names neither the source nor the lock file: it carries the owner's PID and when the lock was acquired, and nothing else. A lock file holding **no decodable record** names no process, host or user at all, so it is reported with the file's own modification time and the lock path to delete. A claim file left by a reclaimer that died mid-reclamation names the claim path. Two refusals name no owner: a lock that could not be read or examined, which carries the underlying filesystem error and so usually the lock pathname with it, and a takeover that kept losing its race through all `lockTakeoverAttempts` (8) rounds, which names only the source.

Nothing is cleared on a timer, and a lock is reclaimed only by the three-part test above — so a crashed run's lock *is* taken over automatically when its record names this PID domain and the OS says that PID is gone. What costs a manual delete is a lock the test cannot clear: one naming another PID domain, one naming no domain at all (which is what a record written before v4.9.9 looks like), one whose liveness probe would not answer, one holding no decodable record, and — on macOS, where the domain is the boot session rather than the machine — any lock left by a crash before the current boot.

Release re-reads the lock and checks the token as well as the PID, so a lock retaken by a later process that happens to have our PID is not ours to delete. That protection is exactly as strong as the read: if the file cannot be read, or its contents cannot be decoded, release still attempts the removal.

### Stateless Uploads

An upload lock cannot always be created: the source directory may be read-only, full, or gone. That case is separated from contention by `ErrUploadLockUnavailable`, which is returned only for a named set of filesystem refusals — `fs.ErrPermission`, `fs.ErrNotExist`, `EROFS`, `ENOSPC`, `EDQUOT` — raised by the `O_CREATE|O_EXCL` create or by the record write that follows a create this process won. It is a **classification of the error**, not a probe and not a finding that the pathname is free: an existing lock is reported as such by the create's own "already exists" answer, which is handled separately, and none of the five refusals establishes that no such lock is there. The write-failure branch is the one that does know the create was won, and there the newly created lock file is removed on a best-effort basis with the result unchecked, so an empty lock file can survive. A lock file that exists but cannot be **read** is deliberately not this case — `inspectionError` is returned instead, because something may be behind it. A lock file that reads but does not **decode** — malformed JSON, or a non-positive PID — is a third answer again: `unidentifiedLockError`, the modification-time refusal described above.

When there is nowhere to record the lock, the upload runs **stateless**: it holds no lock, reads no checkpoint and writes none, and it neither consumes nor deletes another attempt's saved checkpoint — the sidecar it would write to belongs to whoever holds the lock it could not take. It can still abort the fresh backend upload *it* opened, which is its own to discard. That is what makes running without the lock safe — not the directories, but the attempt. Such an upload cannot be resumed, which is logged once. Refusing outright would instead fail every upload whose source sits on a filesystem it cannot write to, which is a case the transfer itself has no need of.

### Interrupted Uploads

**Streaming (the default).** The checkpoint records the **contiguous prefix** of parts the backend has accepted, in index order, together with the CBC chain position at that boundary. A prefix is the resume point rather than a set of completed parts, because parts finish out of order under concurrency while the chain IV names exactly one boundary and the source is re-read from it. The record also carries the part size, the object key and upload ID, the source file's size and modification time, the storage ID and container, and a `CreatedAt` — which for a fresh upload is stamped when the first non-empty prefix is checkpointed, not when the upload was opened. Writing the checkpoint can fail without failing the upload: it sits beside the source, which may be on a read-only or full filesystem, and a streaming upload does not need to write there to succeed. What is lost is only the ability to resume, so that is reported once rather than per part.

A saved state is resumed only if every one of these holds; otherwise it is abandoned and a fresh object is started. The reason is printed only where the caller supplied an `OutputWriter` — PUR's CLI fallback passes `io.Discard`, so there it goes nowhere — and a state file that could not be loaded or decoded at all starts fresh with no diagnostic of any kind:

- it is a streaming (v1) state. A recorded backend must match this one, but an *empty* recorded backend is tolerated
- it is going to this destination. That comparison is skipped entirely when the current run was not told its storage ID or container; where the state records either, they must both match and no path comparison is made; and where the state records neither — which is what a state written before v4.9.9 looks like — the object key must sit under this destination's own path prefix, and a destination without a prefix cannot be told apart at all, so the state is refused
- it describes this path, at this size, with this modification time — a state without a recorded modification time is refused, since size alone cannot tell an edited file from the one those parts were cut from
- it is younger than `MaxResumeAge` (7 days). The code takes that as both backends' own expiry for an unfinished upload (`internal/cloud/state/upload.go`)
- it names the object, states a part size, and carries a master key, initial IV and chain IV that each *decode to the right number of bytes* — not merely present, because base64 of the wrong length gets all the way to the provider and then fails identically on every later attempt
- it has at least one completed part, and its recorded parts are an unbroken run from index 0, each with a handle, and no more of them than the file has

Resuming means running with the interrupted upload's part size, whatever this run would have chosen, so the plan is made again with that size fixed. Planning again for the same transfer replaces its own reservation rather than adding to it. A part size this machine cannot plan for at all is the one case where a resume costs more than it saves, so the state is abandoned and only the bytes already sent are lost.

**Pre-encrypt (`--pre-encrypt`).** The encrypted copy on disk is the artifact, and a resume continues against it whatever order the parts completed in. The same destination, path, size, modification time and age checks apply, plus three of its own: the state must carry an encryption key, IV and random suffix; it must name a path that *looks like* a ciphertext this package made — `isEncryptedTempFile` requires a non-empty pathname, different from the source's, ending in `.encrypted`, which is a name test rather than a proof of provenance and does not resolve aliases to the source by file identity; and that ciphertext must still be exactly the size the state recorded, since `ValidateUploadState` only checks that it exists and a truncated copy would upload as a short object registered under the whole file's checksum.

**Backend-side validation.** Local eligibility is not the whole resume decision — the backend has to still be holding what the checkpoint names, and "gone" has to be told from "could not be asked". The streaming default asks through `ValidateStreamingUploadExists`, one method with two implementations: S3 calls `ListParts` on the recorded upload ID, where `NoSuchUpload` means gone and starts a fresh object while any other error is returned so the caller retries rather than throwing a resume away over a credential blip; Azure has no upload ID, so it calls `GetBlockList` for uncommitted blocks, where `BlobNotFound` or an empty list means gone. The `--pre-encrypt` paths run the same two tests through their own helpers, `multipartUploadExists` and `stagedBlocksExist`. That case is real rather than theoretical — a commit that succeeded while its response was lost leaves a checkpoint naming blocks that are no longer uncommitted.

These checks establish **existence, not correspondence**. S3 discards the part list `ListParts` returned and Azure only counts the uncommitted blocks; neither compares the checkpoint's parts against the backend's inventory by number, size or ETag, so a resume proceeds on the strength of the upload still being open rather than on the backend holding exactly the prefix the checkpoint claims.

Retiring a checkpoint over a rejected **commit** is a `--pre-encrypt` behaviour only. There, `commitFailure` retires the checkpoint when Azure answers `InvalidBlockList`, because the list names blocks Azure does not hold and would name them again on every later attempt, and keeps it for any other rejection, since the blocks are still staged and the checkpoint is what lets the retry ask again. The streaming path makes no such distinction: `CompleteStreamingUpload` returns the commit error as it stands, and what happens to the checkpoint is decided by whether a usable one stands at all (see **Completion and source consistency**, below).

**Retirement.** A state that cannot be resumed still names parts the backend may be holding until its own expiry. `retireBackendUpload` tries to discard that upload before deleting the record, addressing it by the identity the state recorded rather than by a rebuilt handle — a rebuild needs the encryption parameters a damaged state may not have, and the identity is all a state from the *other* mode carries. It is **best effort**, and the record's deletion is attempted either way: an abort that fails is logged and nothing else, and the deletion that follows can itself fail without changing the outcome the caller sees, leaving the checkpoint — and, for a `--pre-encrypt` state, the ciphertext beside it — on disk. Several cases skip the abort entirely — a provider that does not implement `AbortUploadByID`, a state with no object key, a state naming a different backend, or one naming a different destination of this backend (an upload ID a destination never held is simply absent there, an answer that would read as retirement while the upload stays open and its only local record is deleted; such a state is left to its own destination's expiry, and said to be). Azure's `AbortUploadByID` is itself a no-op: a staged, uncommitted block belongs to no blob and cannot be deleted on its own, so Azure retirement is the service's own sweep. Where an abort does run, it runs on a context detached from the caller's, bounded by `constants.AbortOperationTimeout` (60s), because retirement usually runs on the way out of a cancellation and an abort issued on a cancelled context never reaches the backend.

**Completion and source consistency.** An upload is checked for completeness immediately before the call that assembles it. The code's premise is that neither backend will catch the problem for it: it assumes both assemble whatever subset of parts they are handed and report success, that S3 assembles in part-number order so a gap, a duplicate or a reordering changes the stored bytes without failing, and that an unfilled slot in Azure's ordered block list silently drops that block's bytes (`internal/cloud/transfer/uploader.go`, `internal/cloud/upload/upload.go`). What check runs depends on the mode:

- The **streaming default** compares part counts inline, in `internal/cloud/upload/upload.go`: it requires the map of completed parts — this run's plus any it resumed — to hold exactly the planned number, then builds the ordered list by index lookup so what is committed is in index order by construction. There is no byte-count comparison on this path. On Azure the provider adds `transfer.VerifyBlockList` inside `CompleteStreamingUpload`.
- The **`--pre-encrypt` paths** verify per path, and their single-request paths not at all: a ciphertext no larger than `constants.MultipartThreshold` (100MB) on S3, or smaller than it on Azure, goes up in one PUT or one blob write with none of these checks. Both of S3's multipart paths call `transfer.VerifyUploadComplete` — the uploaded byte count *and* part count equal to what was planned — followed by `VerifyPartSequence`, which requires S3's part numbers to be exactly `1..n` in order. Azure's sequential block upload calls `VerifyUploadComplete` alone; its concurrent block upload adds `VerifyBlockList`, which requires every slot of the ordered block list to be filled.

Failing a check refuses the commit in every mode. It does not universally discard the backend upload: on the streaming path that is decided by whether a usable checkpoint stands, which is not the same as this attempt having written one. `checkpoint.recorded` starts **true** when the attempt resumed a non-empty prefix, and is set by the first checkpoint the attempt writes, so an attempt that resumed and then failed before checkpointing anything of its own still keeps the upload. Where one stands, both the parts and the checkpoint are kept for the next attempt to carry on from; only an attempt that neither inherited nor wrote one aborts, since its parts are already unreachable. Underneath, the part pipeline reads with `io.ReadFull` so a short read is `ErrUnexpectedEOF` — a real final part — rather than a silently truncated one.

Registration then has to describe the bytes that were actually sent. The SHA-512 is computed by re-reading the source *after* the transfer — the ordering is what the code does; the reason given for it, that the file is still in the page cache and so cheaper to read than to hash up front, is an assumption about the host rather than something the source establishes. `checkSourceUnchanged` then re-stats the source and fails the upload if its size or modification time moved: otherwise the recorded size and checksum would describe neither the uploaded bytes nor each other, and every later download would fail verification with nothing to point at. The limitation is stated in the code and is real — a rewrite that restores both the size and the modification time is not detected, because catching that would need the hash computed from the bytes as they are uploaded, which this path does not do.

### Download Object Identity

A download that fetches its object as many independent ranges is one download only if the ranges all came from one object. A provider can compare the ranges of a single call and no more: with nothing pinned up front, each call adopts whatever the first response it saw reported, so one part can adopt the version of an object another part never read.

Two layers close that. `PinObjectVersion` wraps a provider's ranged open so every range reports the backend's version (the S3 or Azure ETag) and is compared against the pin; `objectVersionEvidence` on `DownloadPrep` does the same across the parts of a concurrent download. The rule in both is that **missing evidence is not evidence of sameness**: once a download has settled on a version, a range reporting none is refused as firmly as one reporting a different version, and a version arriving after ranges that reported none is refused too, because adopting it now would say nothing about the bytes already written. Only a download whose ranges all report no version runs unpinned, and that is logged. A version that changes mid-download fails with `ErrObjectReplaced` rather than writing a file stitched from two objects.

The concurrent CBC path also bounds how far it reads ahead. Decryption is sequential because CBC chains, so a part that arrives early waits in a buffer, and queueing every part up front would let a stalled part 0 accumulate every other range in memory while slowing nothing down. Parts are dispatched against a fixed number of slots — twice the concurrency — returned once a part has been decrypted and dropped, and workers the scaler adds later share that window rather than widening it.

The concurrent v1 path has the opposite shape — its parts decrypt independently and are written at their own offsets — so it is the **one** path that stages through `<file>.partial`, renaming only once every part is written, the size is final, the hash is computed and the bytes are synced. It pre-allocates the whole part span as its first act, so without the scratch file a part that never arrived would leave a full-size file holed with zeros exactly where the finished download belongs, and every later size check would accept it. The v2 CBC default writes straight to the destination and attempts to remove it on failure, after closing the handle because Windows refuses to remove a file anything still holds open; that removal, like the scratch file's, is unchecked, so a removal the filesystem refuses leaves the bytes where they are. That removal matters even though a failed CBC download is usually short: when the plaintext is a whole number of parts the final encrypted part is padding only, so every plaintext byte can be on disk before that part fails — exactly the file the daemon would adopt by size where no checksum is registered. The sequential v1 driver also writes straight to the destination, but leaves what it wrote in place on failure.

The finished file is then checked twice by `download.DownloadFile`, in this order. **Size first**, against what the API reported, because it is the only completeness check that does not need the file to carry a checksum — and it is the check the CLI's skip-existing modes already make, taking a file of the right length for a finished download. (The auto-download daemon is stricter: it adopts on length alone only when the remote record carries no SHA-512. See [Daemon Eligibility and Completion](#daemon-eligibility-and-completion).) **Then the checksum**, preferring the SHA-512 the download computed over a re-read of the file it has just written; the CBC path hashes as it writes, while the concurrent v1 path reads the completed file back through its still-open handle (its parts are written with `WriteAt`, so they cannot be hashed in order as they land), and `DownloadFile` falls back to re-reading the file when the driver supplied no computed hash.

Neither check is unconditional, and one failure is reported without quarantine:

- an expected size of zero or less means the API reported none, so the size check is skipped; an absent SHA-512 means there is nothing to compare, so the checksum check is skipped
- a file that came out **zero bytes** where a positive size was expected is reported as its own diagnosis and is *not* moved aside — an empty file is not worth preserving. That branch is inside the size check, so it needs the expected size: an empty result the API reported no size for passes the size check untouched, as does a file that is legitimately empty and expected to be. Either can still be quarantined by the checksum check that follows
- any other size mismatch, and a checksum mismatch, move the file aside to `<file>.corrupt`; if the rename fails the file is deleted instead, and if the deletion also fails the returned error says so explicitly and names the file to delete before retrying
- `--skip-checksum` downgrades the checksum failure to a warning; it does not affect the size check

**Download resume by format.** Only some paths keep a resume artifact. The v0 legacy path keeps its partial **ciphertext**, and the chunked driver records which chunks it holds in a `.download.resume` sidecar beside it, so a rerun fetches only the missing ranges; the ciphertext is removed only once the plaintext has been written from it or this attempt has decided the bytes are not worth returning to. `ValidateDownloadState` checks the state's age against `MaxResumeAge`, the destination path, and — for a chunked state — that the encrypted file is no larger than the recorded total *and* no shorter than the last range the state claims: a file too short to hold its own claims was truncated or the state outlived its data, and resuming would skip exactly the missing ranges while the resumed download's pre-allocation refilled them with zeros, a hole no size check notices and only a checksum would catch. Around that, `chunkedResumeUsable` requires the chunk size and total size to match this run's, and requires the recorded ETag to match the object's current one when the backend reported one. That is the limit of the cross-restart identity check: the state also records the remote path and the storage type, and neither is compared on resume, while the ETag comparison is skipped entirely where this operation has no ETag to offer — so the within-attempt version evidence described above does not extend to a resumed download's claim that these bytes came from this object. A state that fails any of these has itself and its partial file removed, rather than left where a later size check could mistake it for a finished download — an attempted removal, like the others here, so a filesystem that refuses it leaves the ciphertext behind. The v2 CBC path keeps **no** checkpoint at all, and the concurrent v1 path discards its scratch file on failure, because every attempt re-fetches all of the parts — so for those two, a failed download always restarts from the beginning.

### Cancellation, Retry and Attempt Tokens (`internal/transfer/queue.go`)

Attempt tokens guard the two things a superseded executor must not do: cancel the run that replaced it, and write a terminal state onto someone else's run. `BeginAttempt(taskID, token)` hands out that ownership — a dispatch the queue scheduled presents the `AttemptToken` it was given, and one the queue did not schedule (`NoAttempt`) takes a fresh attempt only while the task is unheld — and `Attempt`'s methods (`SetCancel`, `ClearCancel`, `Complete`, `Fail`, `FailIfNotTerminal`) are refused unless the token still owns the task. Non-terminal bookkeeping is not gated this way: `UpdateSize`, `StartTransfer` and `UpdateProgress` are called on the queue directly.

The ownership record appears when the attempt is *reserved*, before its executor starts, so a retry requested in that gap waits instead of starting a second run. It is normally released when the owning executor reaches a terminal call, which is later than the task's own terminal state, because a cancelled task's executor keeps unwinding after the cancel; `Attempt.ClearCancel` also releases it, without any terminal transition, for the early-return paths where the task is already terminal. Retries requested during that unwinding are held in `claimedRetries` and start when the task is released.

A cancelled batch is remembered in `cancelledBatches`, because a streaming batch's registration goroutine can still be mid-flight when the cancel sweep runs: tasks registered afterwards are entered directly as cancelled rather than queued, so they cannot sit queued forever with no worker left to run them.

### Credential Refresh

Storage credentials expire mid-transfer, and a rejected credential is not a reason to fail a whole file. `http.ExecuteWithRetry` calls its configured `CredentialRefresh` before **each** attempt, and `transfer.RetryWithBackoff` supplies each provider's `EnsureFreshCredentials` as that hook, so every retrying storage operation on either backend renews credentials on the way into the next attempt. A refresh that itself fails ends the operation rather than being retried around.

Underneath that, `credentials.GetManager` returns one process-wide manager, replaced only when the API client itself changes. It caches four things with two different clocks: the account's default S3 and Azure credential pair, and a credential per storage it has been asked about, both on `constants.GlobalCredentialRefreshInterval` (10min); and the user profile and the root folders, each on a five-minute window of its own. Every getter serves its cache unless the entry is older than its window **or empty**, so retries across a wide batch do not each fetch a new credential, while a credential a backend has just rejected is refetched at once. The per-storage entries are keyed by storage ID, and on Azure by storage ID **and** blob path where the file record carries one. That second key follows from the premise the cache is designed around and states in `GetAzureCredentialsForStorage`: that the API answers a shared-file request with a SAS token scoped to that single blob, so a cached response for one file would not authorise another. The key is what the code establishes; the scope a token actually carries is the platform's to grant. Three things bypass that clock: `InvalidateS3Credentials`/`InvalidateAzureCredentials` drop a rejected credential by **identity**, so a burst of parts failing on one credential invalidates it once and a late rejection cannot throw away the replacement that has already arrived; `EnsureFresh` refreshes proactively at the shorter `constants.CredentialFreshnessThreshold` (8min), measured on the wall clock so a laptop sleeping through the window is still detected; and `ForceRefresh` fetches unconditionally. Those last two are narrower than the cache they sit in front of: both fetch with a nil storage, so they replace only the account's default S3 and Azure pair and its timestamp, and the per-storage entries are left untouched for their own getters to refill. The sequential HKDF download driver runs a ticker at `constants.PeriodicCredentialRefreshInterval` (8min) for the life of the download, since a single long-running range can outlive a credential without ever retrying; a tick that fails is logged and the download continues.

Retries are reported rather than hidden: `RetryWithBackoff` publishes a `cloud.RetryObserver` notification through the retry config's `OnRetry` hook, carrying the operation, attempt number, `constants.MaxRetries`, the classified cause and the next delay. It fires when a retry is about to be *scheduled* — so an initial attempt, a successful attempt and a fatal failure do not each produce one, and neither does an exhausted elapsed budget, which is checked before the notification, because announcing a wait that is not going to happen is worse than silence. One check does follow it: whether the context's remaining deadline is long enough for the backoff. A deadline too short to wait in therefore produces the notification and then the refusal. Bytes reported by a failed attempt are rolled back with a negative delta so the retry can report them again without double-counting.

**Retry boundaries.** `constants.MaxRetries` (10) is the total number of *attempts* `http.ExecuteWithRetry` makes, not ten retries after a first try. `constants.RetryMaxElapsed` (90s) is a budget checked when deciding whether to *wait* for another attempt, not a deadline that interrupts an operation already in flight — an in-flight range is bounded instead by `constants.PartOperationTimeout` (10min) on its own context. Errors are classified before the decision: fatal ones return immediately, credential ones pause one second and retry, network and retryable ones back off exponentially with full jitter. `CredentialRefresh` runs before **each** attempt, and a refresh that itself fails ends the operation rather than being retried around. A ranged read is retried as a whole — open, read and close inside one attempt — so a mid-transfer proxy failure restarts the range instead of returning a short read, and a body shorter than the range asked for is reported as an EOF-shaped error, which the classifier reads as a network failure and retries, rather than being written out as a short part.

**API retries are a separate policy.** The Rescale API client does not use `http.ExecuteWithRetry`; it wraps its transport in `go-retryablehttp` with bounds of its own — `apiRetryMax` 10, which is retries *after* the first attempt and so **11 total**, and its own 1–30 second wait window (`internal/api/client.go`). The one bound it shares with the storage path is the wall clock: it is configured with the same `constants.RetryMaxElapsed` (90s), consulted only between attempts, so a single attempt that hangs overshoots it by however long it hangs, and even between attempts the check is on elapsed time alone rather than elapsed plus the next backoff.

Its policy also suppresses retries the storage path would make. A 4xx other than 429 is never retried. A transport failure on a non-idempotent create is not retried unless the request provably never left, because there is no idempotency key to make a second create harmless and the caller keeps only the last response's ID; the failure is handed back to be reconciled or reported instead. A 5xx answering a POST is passed through rather than retried, so the caller can read the platform's own error out of the body — the test is a substring of the request path (`/api/v3/jobs/` or `/submit/`) rather than an endpoint list, so it covers a POST to any job **subresource** as well, the per-job tags endpoint among them.

---

## Performance Optimizations

### Connection Reuse

The S3/Azure transfer client (`http.CreateOptimizedClient`) pools 512 idle connections total, 100 per host, 90s idle timeout. One is built per provider client (`s3.NewS3Client`, `azure.NewAzureClient`), and that client is reused across every operation *and every credential refresh* of that provider — which is the point, since rebuilding it on each refresh would throw the pool away mid-transfer. It is not one pool shared by providers the factory built separately, so a batch that constructs several providers has several pools. Two transports keep the smaller pool `http.ConfigureHTTPClient` sets — 100 idle total (`internal/http/proxy.go`): the API client, which uses that function directly, and any transport an NTLM proxy has wrapped, because the optimisation pass only rewrites a plain `*http.Transport`.

### Networking Policy

Two functions in `internal/http/` build the API client and the transfer clients, and they are not the same policy. `ConfigureHTTPClient` builds the transport and applies the proxy: it is what the API client uses directly, and the base `CreateOptimizedClient` starts from. `CreateOptimizedClient` then rewrites that transport for transfers — the larger idle pool (512 against 100), the HTTP/2 and compression rules below, and the clearing of the 300s whole-request timeout `ConfigureHTTPClient` sets, since a transfer bounds itself per operation instead. What it does *not* change is the per-connection timeouts: the TLS handshake, expect-continue and idle-connection timeouts it assigns are the same constants `ConfigureHTTPClient` already assigned. Everything below therefore describes the configured, plain-transport branch. Two other branches leave that function early with the overall timeout cleared and nothing else applied: a transport an NTLM proxy has wrapped, which is returned as `ConfigureHTTPClient` left it, and a call made with no configuration at all, which gets a bare `http.Client` on Go's own default transport. Two clients are built outside both functions entirely — `WarmupProxyConnection` constructs its own transport for the basic-mode warmup, and the GUI's version check falls back to a default `http.Client` when the engine has none to lend it.

- **Proxy modes** come from the `ProxyMode` field of `config.Config`: `no-proxy`, `system` (environment variables), `basic`, `ntlm`. `WarmupProxyIfNeeded` runs before credential refreshes, because the first request through some proxies pays a one-time cost that would otherwise land inside a transfer.
- **NTLM depends on the build.** `internal/config/proxy_features_fips.go` (`//go:build fips || fips3`) reports NTLM unsupported and `ValidateProxyModeForBuild` rejects it, because NTLM needs MD4/MD5; `proxy_features_nonfips.go` permits it. `ConfigureHTTPClient` calls that validator first, so in a FIPS build a configured NTLM proxy fails at client construction — while the GUI's startup path only *warns* about the same configuration, leaving the refusal to whichever client is built next.
- **HTTP/2 is a transfer-transport rule**, set in `CreateOptimizedClient` and nowhere else. It is enabled by default there (`ForceAttemptHTTP2`, `http2.ConfigureTransport`) and turned off in two cases: `DISABLE_HTTP2=true` in the environment, which is unconditional; and a proxy being active, decided by the configured proxy mode, with environment variables consulted only in `system` mode. `FORCE_HTTP2=true` is the escape hatch from the second of those and only from it — it keeps HTTP/2 on through a proxy and does not override `DISABLE_HTTP2`. The API client's own transport sets none of this.
- Compression is disabled in the same place and with the same scope: the transfer payloads are already-compressed archives and ciphertext.

### Rate Limiting

Token bucket algorithm with cross-process coordinator. See [Rate Limiter](#5-rate-limiter-internalratelimit) section for details.

### Adaptive Concurrency

`ComputeBatchConcurrency()` in the resource manager dynamically scales concurrent transfers based on median file size:

| Median File Size | Concurrent Transfers | Threads/File |
|-----------------|---------------------|--------------|
| < 100MB (small) | Up to 20 | 1 |
| 100MB – 500MB (medium) | Up to 10 | 1 |
| 500MB – 1GB (medium) | Up to 10 | 4 |
| >= 1GB (large) | Up to 5 | 8–16 |

The concurrency tier turns at 100MB and 1GB, but threads-per-file turns at 500MB (`constants.MediumFileThreshold`), which is why the medium band splits. Each turn is a `<` comparison against the threshold, so a median of exactly 1GB is in the large tier, not the medium one. Thread counts are the base tiers, before aggressive mode. Validated against thread pool capacity and 75% of the memory budget, then capped by the caller's maximum and the batch size, with a floor of 1 (`constants.MinMaxConcurrent`). That budget is `getAvailableMemory()`, and it is not a real reading everywhere: on Windows it is free physical memory from `GlobalMemoryStatusEx`, while on macOS and Linux it is 75% of *(a hardcoded 4GB model less the current Go heap)*, clamped to 512MB–8GB — and if the Go heap has reached or passed that 4GB model, a flat 2GB fallback is returned instead (`internal/resources/memory_unix.go`). Applied symmetrically in GUI and CLI.

**Source:** `internal/resources/manager.go`, `internal/constants/app.go`

### FileInfo Enrichment

`ListFolderContentsPage()` parses full metadata from folder listings (encryption keys, storage info, checksums). Downloads therefore skip the per-file `GetFileInfo()` call, turning one API call per file into one per page. Since all v3 endpoints share a 2 req/sec budget, this is the difference between a large folder download being rate-limited by metadata and being limited by the transfer itself.

**Source:** `internal/api/client.go`

### Streaming Scan-to-Download (GUI)

`ScanRemoteFolderStreaming()` uses 8 concurrent workers scanning subfolders (`numScanWorkers`, `internal/transfer/scan/scan.go`), emitting files to a channel. Downloads are started from that channel as entries arrive, so the first transfers begin while the recursive scan is still running rather than after it finishes.

---

## Threading Model

### CLI Mode

**Main Thread**: Command parsing (Cobra), synchronous execution, progress bar rendering.

**Background Goroutines**: Concurrent uploads/downloads — `RunBatch` runs a fixed worker pool over a buffered channel, sized by `ComputeBatchConcurrency()` at 20 / 10 / 5 by median file size, then clamped by the thread pool, the memory budget, `--max-concurrent` and the batch size, with a floor of `constants.MinMaxConcurrent` (1). `constants.DefaultMaxConcurrent` (5) is both the largest tier's value and the *declared* default of `--max-concurrent` on every transfer command. Two commands override the declaration when the flag was not actually given: `folders upload-dir` and `folders download-dir` substitute `constants.MaxMaxConcurrent` (20), so that adaptive concurrency can reach the small-file tier instead of being capped at 5 before it starts, and `daemon run` substitutes the daemon config's own `max_concurrent` when it is set. An explicitly supplied `--max-concurrent` is taken as given, within the `constants.MinMaxConcurrent`–`constants.MaxMaxConcurrent` (1–20) range the transfer commands check it against. `daemon run` does neither: it applies no range check of its own and passes the flag value straight into the daemon config, and the 1–10 range in `DaemonConfig.Validate` bounds the `max_concurrent` written in `daemon.conf` rather than that flag. Alongside that: per-file multi-threaded transfers via `TransferHandle`, API calls with timeouts, progress updates.

**Synchronization**: WaitGroups for concurrent operations, mutexes for shared state (minimal), channels for coordination.

### GUI Mode (Wails v2)

**Architecture**: Wails v2 with React/TypeScript frontend.
- **Main Process** (Go): Runs the Wails app, handles API calls, file I/O
- **Renderer** (the platform's own webview): Runs the React UI — WebView2 on Windows, where `WebviewBrowserPath` points at the bundled fixed-version runtime when one is present; WebKitGTK on Linux, bundled into the AppImage with its helper processes; WKWebView on macOS
- **IPC**: Automatic method binding via Wails runtime

**Event Bridge Pattern** (`internal/wailsapp/event_bridge.go`):
Go backend forwards internal EventBus events to Wails runtime events. Frontend subscribes via `EventsOn()`. Progress events throttled to 100ms intervals.

### Two-Layer Concurrency Model

Transfer concurrency uses two layers sharing one `resources.Manager`. That manager is per-owner, not process-wide: `NewTransferService` constructs one, and so does each `pipeline.NewPipeline`. Sharing is within the owner — every transfer a `TransferService` runs draws on its manager — and never across processes, so a CLI run, the GUI and the daemon each budget threads and memory independently.

**Layer 1 — Batch Concurrency** (`RunBatch` / `RunBatchFromChannel` in `internal/transfer/batch.go`):
- Determines how many files transfer simultaneously
- `ComputeBatchConcurrency()` computes median file size → picks tier
- CLI, GUI and daemon transfers use this shared abstraction; PUR runs its own stage pools instead

**Layer 2 — Per-File Multi-Threading** (`transfer.Manager.AllocateTransfer` in `internal/transfer/manager.go`, which is what every batch caller invokes; it draws the thread count from `resources.Manager.AllocateForTransfer` underneath):
- When each file starts, allocates threads from the shared pool
- Thread count based on file size tiers: under 500MB gets `constants.MinThreadsPerFile` (1) — no branch matches below `MediumFileThreshold`, so the 100MB–500MB band is 1 as well — then 500MB–1GB: 4, 1–5GB: 8, 5–10GB: 12, 10GB+: 16. `--no-auto-scale` replaces the ladder with 1 / 2 / 3
- Aggressive mode is on unless the caller configures it, and multiplies those tiers by 1.5x (1-5GB), 1.75x (5-10GB) or 2x (10GB+) *before* the caps apply — so on a machine with enough cores the effective counts are 12 / 16 / 16, not 8 / 12 / 16
- Three caps then apply in order: the file's share of the pool (`totalThreads / totalFiles`, floor 1), `constants.MaxThreadsPerFile` (16), and the logical core count
- Dynamic rebalancing: as files complete, freed threads become available

```
                     ┌──────────────────────────┐
                     │    resources.Manager     │
                     │ (per-owner thread pool)  │
                     └────────────┬─────────────┘
                                  │
                 ┌────────────────┴────────────────┐
                 │                                 │
      ┌──────────▼───────────┐   ┌─────────────────▼──────────────┐
      │      RunBatch        │   │  StartStreamingDownloadBatch   │
      │    (known items)     │   │  (streaming — GUI folder       │
      │   adaptive workers   │   │   download AND daemon)         │
      └──────────┬───────────┘   └─────────────────┬──────────────┘
                 │                                 │
                 └────────────────┬────────────────┘
                                  │ per file
                     ┌────────────▼─────────────┐
                     │     AllocateTransfer     │
                     │   (per-file threads)     │
                     └──────────────────────────┘
```

### Conflict Resolution

Conflict handling uses a shared `ConflictResolver[A comparable]` generic type (`internal/cli/conflict.go`), instantiated four times with the action vocabulary each site actually offers: download file conflicts (skip / overwrite / resume), folder download conflicts (skip / merge), file upload conflicts (skip / overwrite) and error handling (continue). There is no rename action anywhere in it. It is thread-safe — the prompt runs under the mutex, so prompts are serialised — and it escalates from a "Once" action to an "All" one when the prompt returns an All choice: the user's own answer promotes the mode, after which conflicts of that kind resolve without prompting. Nothing escalates on its own after a number of conflicts.

---

## Configuration & Settings Flow

### Settings Persistence Architecture

`config.csv` holds the settings the GUI's tabs write through `ConfigDTO`, and is the single source of truth for those. Two of the DTO's fields are deliberately not among them. The API key is written to a separate token file. The **proxy password** is not persisted at all: `updateConfig` puts it into the in-memory config so the session can use it, `SaveConfigCSV` leaves it out of the file, and `LoadConfigCSV` ignores a `proxy_password` value left in an older file, warning on stderr that it did — so a proxy password is a per-run value, entered through the GUI's field or the CLI's secure prompt. The rest of the GUI's persistence is elsewhere again: the auto-download daemon's settings go to `daemon.conf`, and part of the workflow state the tabs keep lives in the browser rather than on the Go side (see [Frontend persistence](#frontend-persistence-versus-backend-state) below).

```
┌─────────────────────┐    updateConfig()     ┌──────────────────┐    SaveConfigCSV()    ┌────────────┐
│  PUR Tab            │──────────────────────→│  config_bindings │──────────────────────→│ config.csv │
│  (Pipeline Settings)│    saveConfig()       │  (Go backend)    │    LoadConfigCSV()    │            │
│  SingleJob Tab      │←──────────────────────│                  │←──────────────────────│            │
│  (Tar Options)      │    GetConfig()        │  GetConfig()     │                       │            │
└─────────────────────┘                       └──────────────────┘                       └────────────┘
```

**Settings location by tab:**
- **Setup Tab**: API key, proxy configuration, detailed logging, auto-download daemon
- **PUR Tab**: Pipeline Settings (tar/upload/job workers, tar options), scan prefix, validation pattern
- **SingleJob Tab**: Tar options (directory mode only: exclude/include patterns, compression, flatten)

**Other persistent files.** `config.csv` is not the only thing written, and on Windows the four files do not all live together:

| File | Windows | Unix |
|---|---|---|
| `config.csv` | `%LOCALAPPDATA%\Rescale\Interlink` | `~/.config/rescale` |
| `token` (where `config init` puts an API key) | `%LOCALAPPDATA%\Rescale\Interlink` | `~/.config/rescale` |
| `apiconfig` (the INI compat mode's `--profile` reads) | `%APPDATA%\Rescale\Interlink` (Roaming) | `~/.config/rescale` |
| `daemon.conf` (owned by the auto-download daemon) | `%APPDATA%\Rescale\Interlink` (Roaming) | `~/.config/rescale` |

`config.csv` and `token` use Local so credentials do not roam between machines; each location has a read-side fallback to the pre-rename directory so an existing installation keeps working. Writing `daemon.conf` leaves a running daemon on its old settings, which is why "Save all settings" asks it to reload and reports what happened.

`internal/wailsapp/persistence.go` holds one function, `ensureAllConfigPersisted`, which flushes the in-memory config to `config.csv` and the API key to the token file (removing that file when the key is cleared, so a service booting later cannot resurrect a stale credential) before any handoff to a different-identity process — the Windows service via UAC, or a subprocess daemon. Saved GUI job **templates** are a different thing: `App.SaveTemplate` (`internal/wailsapp/job_bindings.go`) writes them as JSON under `<home>/.config/rescale/templates`, built from `os.UserHomeDir()` on every platform, so on Windows they land under `%USERPROFILE%\.config\rescale\templates` rather than in either app-data directory.

**Credential precedence, by surface.** Three chains exist, and they are not the same one.

- The **native CLI** loads its key in `Config.MergeWithFlagsAndTokenFile`: `--api-key` > `RESCALE_API_KEY` > `--token-file` > the default token file. There is no per-user `apiconfig` step in it. When several of those hold *different* values the command warns and names the precedence; several holding the same value is the documented setup `config init` produces and is not warned about.
- **`config.ResolveAPIKey`** is the profile-directed chain the daemon surfaces use: an explicitly supplied key; the per-user token file under the given profile directory; the per-user `apiconfig` INI; then the default token file; then the environment variable. In **service mode** the fallback stops after the per-user sources — the default token file and the environment would resolve to SYSTEM's own on a Windows service, which is a different identity from the user whose jobs are being downloaded. The GUI's daemon bindings call it through `ResolveAPIKeyForCurrentUser` with the current home directory and service mode off; the Windows service calls `ResolveAPIKeySource` per profile with service mode on.
- The **GUI's own** startup selection is narrower still (`resolveGUIAPIKeySource`): the default token file, then `RESCALE_API_KEY`. A legacy `apiconfig` holding a key is reported to the user as present but *not* used.

Compat mode has a fourth, separate chain (see [CLI Compatibility Mode](#cli-compatibility-mode)).

**Windows service isolation.** `MultiUserDaemon` runs one daemon per user profile, keyed by profile path: it enumerates profiles at startup, runs startup migrations against them, and rescans every five minutes so a profile added later is picked up without a restart. A discovered profile does not automatically get a running daemon — `daemon.conf` has to load, be enabled and validate, and a per-user API key has to resolve. Only the last of those produces a recorded skip: a profile with no resolvable key is entered in the table with the reason `no_api_key`, while a configuration that will not load is returned as an error and one that is disabled or fails validation is logged at debug level and left out of the table entirely. The service itself runs as SYSTEM, and drive mappings belong to a user's session rather than to SYSTEM — but what the code does about that is warn, not skip. A download folder naming a drive letter other than C: that cannot be stat'd produces a warning naming the folder and suggesting a local or UNC path, and the daemon is then constructed and started regardless; whatever the unreachable folder breaks, it breaks later.

Over the named pipe the caller is authenticated to a SID, and the **user-scoped** requests take their scope from that SID rather than from anything the request supplies — a supplied user ID is disregarded, and a caller the pipe could not identify is refused. That is what keeps users apart, which is why `authorizeModifyRequest` relaxes to "any authenticated caller" in service mode (`internal/ipc/server.go`). Not every message is user-scoped, though: `GetStatus` is a read with no authorization at all and describes the service; `OpenLogs` has an explicit `service` scope that opens the service's own log directory; a `Shutdown` an authenticated caller is allowed to make stops the **service**, not just that caller's daemon; and `ReloadConfig`, although routed per user, is handled by triggering the service-wide profile rescan (`internal/service/ipc_handler.go`). Credential resolution is the other half of the isolation: in service mode `ResolveAPIKey` refuses to fall back to the default token file or the environment, both of which would resolve to SYSTEM's rather than the user's.

### Frontend Persistence versus Backend State

Two stores write to the browser's `localStorage`, and neither is backed by `config.csv` or the PUR state file: `runStore` records a descriptor of the active run — its ID, type, start time and job count — and `jobStore` keeps a "workflow memory" of values carried between sessions. Every read and write is wrapped so a failure is ignored rather than breaking the tab. Other in-tab state — `singleJobStore`'s form, for instance — is Zustand state that survives tab navigation but not a reload. Backend state is the other kind: `config.csv`, the token file, `daemon.conf` and the PUR state CSV are what another process reads, and they are files on disk rather than browser storage — whether any of them outlives an uninstall is a property of the installer and of what the user chooses there, not of the code.

What that descriptor buys is **reconstruction, not resumption**. On startup `recoverFromRestart` reads it and asks the engine what it is doing. If a run is still going — the app was reconnected rather than restarted — it reloads the job rows and reattaches its polling. If the engine is idle, it loads the run's rows from the state file on disk, classifies the outcome from them (`interrupted` when rows are still pending, otherwise failed, unconfirmed or completed), files the result among the completed runs, and clears the saved descriptor. It never restarts the pipeline. The completed-run list it files into is in-memory and capped at 20; the durable record of any run remains its state CSV.

For compatibility output the API client keeps a parallel **raw** reader (`internal/api/client_raw.go`), returning `json.RawMessage` so that fields the typed models drop (`price`, `lowPriorityPrice`, `walltimeRequired`, `supportDesks`, `notify`, `preventDuplicates`) survive to the output. Most read v2 endpoints — `GetCoreTypesRaw`, `GetAnalysesRaw`, `GetJobConnectionDetailsRaw`, `GetJobLoadMeasurementsRaw` — while `GetJobStatusesRaw`, `GetJobRaw` and `GetFileInfoRaw` read v3. Compat mode is the only consumer, across three commands: `list-info` takes the core types and analyses, `status` takes the statuses, connection details and load measurements, and `submit` takes `GetJobRaw` for its extended output. `GetFileInfoRaw` has no caller outside the tests.

---

## Data Flow

### Upload Pipeline

```
User Command
    │
    ▼
CLI/GUI Interface
    │
    ├─→ file upload only: steps 2-3
    └─→ PUR / Single Job (through the pipeline): steps 1-5
    │
    ├─→ 1. Create Tar Archive ─→ <common parent>/.rescale-int-<hash>/job-xxxx.tar.gz
    │                            (pipeline only; a plain file upload starts at 2)
    ├─→ 2. Encrypt + Upload ───→ S3/Azure, encrypted per part in flight
    │                            (--pre-encrypt instead writes a temp
    │                             encrypted file, then uploads it)
    ├─→ 3. Register File ──────→ API: POST /api/v3/files/
    ├─→ 4. Create Job ─────────→ API: POST /api/v3/jobs/
    └─→ 5. Submit Job ─────────→ API: POST /api/v2/jobs/{id}/submit/
            │                    (skipped for submitMode=create_only)
            ▼
         Rescale Platform
```

Steps 1, 4 and 5 belong to a job run: `upload.UploadFile` on its own does steps 2 and 3 and stops. For a plain file upload the CLI reaches `upload.UploadFile` directly and the GUI reaches it through `TransferService`. The whole chain is `internal/pur/pipeline`'s, and how it is reached depends on the surface: the native CLI's `pur` commands construct the pipeline themselves (`internal/cli/pur.go`), while the GUI's PUR and Single Job tabs go through the Core Engine, which constructs it and injects `TransferService` as the uploader. Step 1 is the pipeline's for both, so a Single Job in directory mode archives exactly as a PUR job does; a Single Job pointed at remote file IDs has no archive to build and starts at step 4.

Single Job has a third input mode, and it does not reach the pipeline the same way. In **local files** mode (`App.StartSingleJob`, `internal/wailsapp/job_bindings.go`) the binding expands each selected path — walking a directory into its individual files — and uploads them one at a time through `TransferService.UploadFileSync` under a batch of the job's own, *before* `RunFromSpecs` invokes the pipeline. The engine run has already started by then — `StartRun` is called before the expansion, and the engine's state and progress methods are what those uploads report through. Only once they have all finished is the job spec handed to `RunFromSpecs`, with the returned file IDs as its `InputFiles`, so the pipeline sees the same shape as remote-IDs mode and also starts at step 4. The uploads are steps 2 and 3 done outside the pipeline, which is what puts them in the Transfers tab as their own batch; the state row is pre-created so the progress they report has somewhere to land, and a single failed file fails the job before any of it is created.

Steps 3 and 4 are the two calls whose loss cannot be told from their success, and each is reconciled rather than blindly repeated — but they resolve it differently. A registration whose response never arrived is reconciled by searching the platform for the record it would have created, matched on **the stored object the request named** rather than on name and size, which two uploads of different content may share. Name, size, folder and recency narrow the search; the object comparison decides it. That comparison is asymmetric on purpose: the storage path is mandatory, so a candidate whose path is unknown is fetched in full rather than guessed at, and a path that differs rules the candidate out — while container, storage ID, storage type and checksums are compared only where both sides carry them, and agree by default where one does not. Recency is bounded by `registerReconcileWindow` (10 minutes) as a lower bound on the upload date, wide enough for a request that stalled before the connection dropped and narrow enough that an unrelated earlier file of the same name and size is not adopted; a candidate carrying no upload date is not excluded by it. A negative answer has to be complete, so pages are read up to `registerLookupMaxPages` (10) before reporting that nothing was created; an exhausted bound, a search that fails, or two records that cannot be told apart are each an error rather than a "no". Given a **complete** negative answer the implemented rule is to send the registration again, up to `registerFileAttempts` (3) rounds — the decision rests on that listing being complete and current, which is the platform's answer rather than something this code can establish. Two things stop the loop regardless: a response the platform accepted but whose body could not be read (`errRecordUnconfirmed`), since the record exists and a second attempt would create another; and a context the *caller* cancelled after the request went out, because confirming what the platform did with it needs the context that just died, so the call reports that it could not find out. A job creation in the same position has no such search available and produces `api.ErrJobMayExist` instead; see [PUR Pipeline](#13-pur-pipeline-internalpurpipeline).

### Job Request Construction

A job payload is assembled at three independent sites, and a field added to only some of them is silently dropped rather than rejected — the platform cannot complain about a key it never received. All three must stay in sync:

- typed decode of a `--job-file` (`internal/cli/jobs.go`), which needs only the struct field on `models.JobRequest`
- `pipeline.BuildJobRequest` (`internal/pur/pipeline/pipeline.go`), which copies from `models.JobSpec`
- `SGEMetadata.ToJobRequest` (`internal/pur/parser/sge.go`), plus the two `JobSpec` converters, so a script loaded into the GUI and saved back out keeps what it came in with

The SSH access fields show the shape of that: the SGE parser reads `#RESCALE_INBOUND_SSH_CIDR` and `#RESCALE_PUBLIC_KEY`, `models.JobRequest` carries all three (`cidrRule`, `publicKey`, `sshPort`), and each of the three construction paths propagates them. All three are tagged `omitempty`, so a submit that leaves them unset omits the keys from the JSON rather than sending empty ones. `--job-file` also warns about top-level keys the decode ignored, so a typo is named instead of vanishing.

### Download Pipeline

```
User Command
    │
    ▼
CLI / GUI / daemon
    │
    ▼
download.DownloadFile
    │
    ├─→ 1. Get File Metadata ──→ API (or from enriched listing)
    ├─→ 2. Detect Format ──────→ v2 CBC / v1 HKDF / v0 legacy
    ├─→ 3. Download + Decrypt ─→ S3/Azure, decrypted per part in flight,
    │                            ranges pinned to one object version unless
    │                            every range reports none
    │                            (v0 legacy instead downloads the whole
    │                             encrypted file, then decrypts it;
    │                             concurrent v1 stages via <file>.partial
    │                             and renames before step 4)
    └─→ 4. Verify size, then checksum ─→ keep <file>, or → <file>.corrupt
```

`ParseObjectFormat` (`internal/cloud/transfer/format.go`) decides the version from the object's own metadata, normalized to lowercase keys first because S3 lowercases them and Azure preserves whatever the writer used. `formatversion=1` is HKDF; `streamingformat=cbc` is v2; anything else is v0 — including an object carrying no metadata at all, which is the case the v0 branch is there to cover.

Disk space is pre-checked on two of those paths and not the others. The v0 legacy path requires **2x** the file size, because it holds the encrypted and the decrypted copy at once; the v1 HKDF path, when it runs sequentially, requires the estimated plaintext size. The concurrent v1 path and the v2 CBC default do not pre-check — they write in place as parts decrypt, and a mid-transfer ENOSPC is reported by the write that hits it.

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
- Prefer releasing locks before calling into other components. This is a guideline, not an invariant: `Engine.UpdateConfig` calls into the transfer and file services while holding its mutex, and `FolderCache.Get` calls the API under its write lock so a double-checked miss cannot become two listings of one folder

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
- A shipped bundle must not depend on the host's copy of a library it already bundles. `libwebkit2gtk` forks its helper executables (`WebKitWebProcess`, `WebKitNetworkProcess`) from a path compiled into the library, so bundling the library without the helpers makes the Linux AppImage fork the *host's* helpers — and a mismatched host WebKit fails the IPC handshake, leaving a window that paints but never renders. `build/linux/bundle-webkit.sh` copies the helpers in and gives them `$ORIGIN`-relative RPATHs; `build/linux/verify-appimage.sh` checks the result by extracting a finished AppImage and resolving every helper's libraries with `LD_LIBRARY_PATH` deliberately unset, because setting it would hide exactly the missing RPATH the bundling exists to supply. Nothing in this repository invokes that script — the release workflow in `.github/workflows/release.yml` has no Linux build job at all (its jobs build on macOS and Windows, and the final `release` job only assembles what they produced), so where it runs is a property of the out-of-tree Linux build

### 7. Dependency Injection
- Watch engine has zero imports from CLI packages
- All behavior injected via function types
- Enables sharing between native and compat modes without import cycles

### 8. Truthful Status Reporting

Every surface that reports an outcome must be able to distinguish success, failure, cancellation and "not yet known". This is a correctness property, not a UI nicety — a wrong answer here is worse than no answer, because it is acted on.

- **Exit codes**: a command that prints a failure summary must not also return nil, or scripts and CI read a failed run as a success. `folders upload-dir` and `pur run` return an error when any item failed; an aborted prompt cancels the batch rather than continuing through the remaining files; a prompt that cannot run (no terminal) records the file as failed instead of dropping it silently. A cancelled run is deliberately *not* a failure.
- **Cancellation**: see [Transfer Batch Abstraction](#6-transfer-batch-abstraction-internaltransferbatchgo). A cancelled transfer must not be able to render as a completion.
- **"Not yet known" is a fourth outcome, not a rounding of the other three.** A job whose creation the platform never answered is neither done nor failed; it is counted and named on its own, and the run is not reported as finished cleanly. See [PUR Pipeline](#13-pur-pipeline-internalpurpipeline).
- **Retries and throttling**: a silent retry loop is indistinguishable from a hang. Storage retries and rate-limit waits are published on every surface — progress-bar-safe stderr in the CLI, the daemon's own logger and IPC log buffer, the event bus and footer indicator in the GUI.
- **Evidence, not assumption.** Where an answer decides whether to overwrite, reclaim or discard something, its absence is refused rather than read as consent: a lock whose owner cannot be probed is left alone, and a download range that reports no object version cannot join one that did. See [Transfer Integrity](#transfer-integrity).
- **Error messages must agree with the decision that produced them.** A refusal reports the figures the check itself enforced: the disk-space pre-flights return `CheckAvailableSpace`'s own error verbatim rather than rebuilding one at the call site, since a rebuilt message states a requirement nothing actually applied.

---

## Constants Management

### Centralized Configuration

**Purpose**: one home for the constants more than one package has to agree on.

**Implementation**: `internal/constants/app.go`

Shared application constants are collected in that one file, with named constants, inline documentation, logical grouping and type safety. It is not the only place constants live: values used by a single component stay with it — the rate-limit scopes in `internal/ratelimit/constants.go`, `MaxResumeAge` in `internal/cloud/state/upload.go`, the DOE limits in `internal/pur/doe/`, and so on.

**Categories:**

1. **Storage Operations**: `MultipartThreshold` (100MB), `ChunkSize` (32MB), `MinChunkSize` (16MB), `MaxChunkSize` (64MB), `MinPartSize` (5MB), `PartSizeAlignment` (1MB), and the backend ceilings `MaxS3UploadParts` (10,000), `MaxAzureUploadBlocks` (50,000), `MaxS3PartSize` (5GB), `MaxAzureBlockSize` (4000MB)
2. **Credential Refresh**: `GlobalCredentialRefreshInterval` (10min), `CredentialFreshnessThreshold` (8min, `EnsureFresh`'s proactive window), `PeriodicCredentialRefreshInterval` (8min, used by Azure's provider ticker and by the shared sequential HKDF download driver)
3. **Retry and Timeouts**: `MaxRetries` (10 total attempts), `RetryInitialDelay` (200ms), `RetryMaxDelay` (15s), `RetryMaxElapsed` (90s, a pre-retry budget check rather than an interrupting deadline), `PartOperationTimeout` (10min per attempt), `AbortOperationTimeout` (60s)
4. **Disk Space Safety**: `DiskSpaceBufferPercent` (0.15)
5. **Event System**: `EventBusDefaultBuffer` (1000), `EventBusMaxBuffer` (5000)
6. **Pipeline Queues**: `DefaultQueueMultiplier` (2), `MaxQueueSize` (1000)
7. **UI Updates**: `TableRefreshMinInterval` (100ms), `ProgressUpdateInterval` (500ms)
8. **Thread Pool**: `AbsoluteMaxThreads` (32), `MaxThreadsPerFile` (16), `MemoryPerThreadMB` (128)
9. **Resource Management**: File size thresholds (`SmallFileThreshold` 100MB, `MediumFileThreshold` 500MB, `LargeFile1GB`/`5GB`/`10GB`) and the per-tier thread counts (4 / 8 / 12 / 16), plus the memory model bounds `MinSystemMemory` (512MB) and `MaxSystemMemory` (8GB)
10. **Adaptive Concurrency**: `DefaultMaxConcurrent` (5), `MaxMaxConcurrent` (20), `MinMaxConcurrent` (1), tier-specific values (20 / 10 / 5)
11. **Upload Pipeline**: `UploadQueueDepthPerWorker` (3), `UploadPipelineTransientParts` (4), `UploadMinThrottledWorkers` (4)
12. **Channel Buffer Sizes**: `DispatchChannelBuffer` (256), `WorkChannelBuffer` (100)

**Best Practice**: When adding new configurable behavior, add constants to `constants/app.go` with documentation and use them throughout code.

---
