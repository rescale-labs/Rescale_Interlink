# Architecture - Rescale Interlink

**Version**: 4.9.9
**Last Updated**: August 25, 2026

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
- [Configuration & Settings Flow](#configuration--settings-flow)
- [Data Flow](#data-flow)
- [Design Principles](#design-principles)
- [Constants Management](#constants-management)

---

## System Overview

Rescale Interlink is a unified CLI and GUI application for managing Rescale computational jobs. The architecture follows a layered design with clear separation of concerns.

```
+-------------------------------------------------------------+
|                 Rescale Interlink v4.9.9                     |
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
- `rescale-int-tray` (from `cmd/rescale-int-tray/`): Windows system tray companion for daemon status. Windows-only, and shipped in both Windows artifacts — `build_dist.ps1` builds it into `_build\bin\`, which is what the portable zip packs. What the zip lacks is the MSI's wiring: the per-user `Run` key that starts the tray at logon, and the installable Windows Service it fronts.

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
│   │   ├── doe/                   # Design of experiments (parameter sweeps)
│   │   ├── filescan/              # File scanning
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
│   │   ├── multipart/             # Multipart upload and scan
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
├── installer/                     # Windows MSI: WiX source, licence, icon, build script
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
    ├─→ internal/fips
    └─→ internal/version

internal/cli
    ├─→ internal/api
    ├─→ internal/config
    ├─→ internal/watch
    ├─→ internal/models
    ├─→ internal/progress
    ├─→ internal/transfer  (+ /folder, /scan)
    ├─→ internal/cloud/{upload,download,credentials,state}
    ├─→ internal/pur/{pipeline,parser,pattern,state,filescan,validation}
    ├─→ internal/daemon, internal/service, internal/ipc
    └─→ internal/ratelimit (+ /coordinator)
        NOT internal/core — the PUR engine is reached through internal/pur/pipeline

internal/cli/compat                 (does NOT import internal/cli — that is what avoids the cycle)
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
    ├─→ internal/pur/{pipeline,state,pattern,validation}
    └─→ internal/models

internal/services (GUI-agnostic)
    ├─→ internal/cloud (+ /upload, /download, /credentials)
    ├─→ internal/transfer (+ /folder, /scan)
    ├─→ internal/events
    ├─→ internal/resources
    ├─→ internal/ratelimit
    └─→ internal/api
        NOT internal/core — the arrow runs core → services

internal/watch                      (complete — verified with go list -deps)
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

**Thread Safety**: All public methods are thread-safe using RWMutex.

### 2. API Client (`internal/api/`)

**Purpose**: Interface to Rescale Platform REST API v3 and v2.

The `Client` struct holds its HTTP client, the resolved config, base URL, API key, the process-level rate-limiter store, and API usage metrics. There is no folder cache on it. See `internal/api/client.go` for the full definition.

**Key Features**:
- HTTP client from `http.ConfigureHTTPClient` — connection pooling at 100 idle connections total, 100 per host, 90s idle timeout. The larger 512-idle pool is the transfer client's, not this one's
- Automatic retry with exponential backoff
- Rate limiting (three-scope token bucket)
- Folder listing *enrichment* via `ListFolderContentsPage` — not caching. The cache is `folder.FolderCache` in `internal/transfer/folder/`, created per operation by the caller
- Structured error handling

**Selected client methods**: file/folder/job CRUD (`ListFiles`, `DeleteFile`, `CreateFolder`, `ListFolderContents`, `DeleteFolder`, `GetJob`, `GetJobStatuses`, `SubmitJob`, `StopJob`, etc.). Streaming upload and download primitives are **not** methods on `api.Client` — they live as free functions in `internal/cloud/upload/` and `internal/cloud/download/` and run on top of provider-specific transfer handles. The API client only handles metadata-level REST calls.

**Pagination**: the jobs endpoint paginates by **page number**, not by offset — `limit`/`offset` are accepted and ignored, so an offset-based reader silently re-reads page 1. `ListJobsPage(ctx, page, pageSize)` sends `page` and `page_size` (`internal/api/client.go`). File and folder listings page by following the server's `next` link, and `page_size` is re-applied to every `next` URL because the server's own link carries its 25-item default, which would otherwise reintroduce slow pagination mid-walk.

**Filtered listings**: `ListFilesPageWithOptions` accepts a `FileListOptions{OwnerFilter, SearchQuery, Ordering}`, and `SearchFolderContents` searches within a folder. These back the File Browser's search, owner filter and sort controls.

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
- `BatchProgressEvent` carries `Cancelled` and `CancelRequested` alongside `Completed`/`Failed`, so a batch cancelled while the Transfers tab is in the background is still reported as cancelled rather than frozen at zero. It also carries `DiscoveredTotal`/`DiscoveredBytes` — the scan's running count, which leads `Total` (registered tasks) during a streaming folder transfer, so completion is measured against `max(discoveredTotal, total)`.

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

**Visibility**: Utilization-based notifications with hysteresis — silent when utilization < 50% (`UtilizationSuppressThreshold`), warns at >= 60% (`UtilizationWarnThreshold`), throttled to 1 notification per 10 seconds (`NotifyMinInterval`).

Notices reach a surface through `ratelimit.SetGlobalNotifyFunc`, a single process-level callback:
- The CLI registers it in root's `PersistentPreRun`, pointing at stderr **through** `progress.SinkWriter` so a notice lands above the progress bars rather than inside them.
- `daemon run` re-registers it onto the daemon's own logger in `RunE`, which runs after `PersistentPreRun`. It has to: `daemonize` sets a detached child's stderr to nil, and these notices deliberately bypass the standard logger, so without re-registration every throttling, cooldown and retry notice in the daemon went nowhere.
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

**Usage**: All transfer paths — CLI folder upload/download, GUI streaming transfers, daemon auto-download — use `RunBatch` or `RunBatchFromChannel`. This replaced 10+ inline worker pool implementations.

**Cancellation**: a cancel must remain distinguishable from an empty or successful run all the way to the surface that reports it, which takes three cooperating pieces:

- The folder orchestrator (`internal/transfer/folder/orchestrator.go`) returns a `Cancelled` flag and the counts discovery reached. Cancellation usually leaves the merge loop through the closed-channel path rather than `ctx.Done()`, because `WalkStream` closes its file channel on cancel and a receive from a closed channel is always ready — so both exits set the flag.
- An empty batch normally anchors a placeholder task so the transfer still leaves a record. That placeholder is a **completed** task, so registering one for a cancelled scan is what made a cancelled transfer render as finished; both registration sites are now guarded on the scan not having been cancelled, and `CancelBatch` anchors a *cancelled* placeholder instead.
- The batch row treats "Complete" as requiring nothing failed, nothing cancelled, and no cancel requested.

`TransferService.CancelAll()` is the queue-wide sweep. The GUI's "Cancel All" is not that call — it iterates the visible batches and calls `CancelBatch()` on each.

### 7. Error Reporting (`internal/reporting/`)

**Purpose**: Safe reporting of genuine server-side failures, with redaction of sensitive data.

**Pipeline**: classify → redact → build → report

- **Classifier** (`classifier.go`): `IsReportable()` filters errors — only server errors (5xx) and unclassified internal errors generate reports. User-fixable errors (auth, network, timeout, disk space, client 4xx) are suppressed.
- **Redactor** (`redactor.go`): Strips hex tokens, URL params, emails, auth tokens, home paths. Job names replaced with `job-N` placeholders.
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

- The legacy (pre-encrypted) download path requires **2x** the file size, because it holds the encrypted and the decrypted copy at once. That doubling and the margin are part of the decision, so the pre-flight sites return `CheckAvailableSpace`'s own error verbatim rather than rebuilding a message — a rebuilt message that dropped the margin reported a requirement the check had not enforced, and could read as "need 292366 MB, have 312832 MB available".
- Free space is measured on the filesystem of the directory being written to. Applying `filepath.Dir` to a directory argument measures the *parent* volume, which is wrong whenever the download directory is itself a mount point.

Mid-transfer ENOSPC is a separate path: no pre-flight ran, so those sites report their own figures. `IsDiskFullError` and `ClassifyErrorClass` match both the Linux ("disk quota exceeded") and macOS/BSD ("disc quota exceeded") spellings of `EDQUOT`.

### 10. Progress Tracking (`internal/progress/`)

**Purpose**: Abstract progress reporting for CLI and GUI.

CLI uses `mpb` (multi-progress bars) with per-file bars showing speed and ETA. GUI uses EventBus events forwarded through the Wails event bridge.

**Log routing during CLI transfers**: while `mpb` is drawing it owns the terminal, redrawing its frame on a timer, so anything written to the same terminal in between lands *inside* that frame — one bar becomes a screenful of half-drawn ones. Everything with something to say during a transfer therefore goes through mpb's own writer, which interleaves whole lines above the bars:

- `progress.SetLogSink` lets the active UI register itself and `SinkWriter` resolves the sink per write, so log setup happens once at startup and follows the bars as they come and go. Only a UI attached to a terminal claims the sink — with bars off, mpb writes to `io.Discard`, and routing logs there would swallow them.
- The standard logger, which carries the transfer path's `[BATCH]`, `[SLOT]`, `[CRED]` and `[TIMING]` diagnostics, is discarded unless the user asked for it via `--verbose`, `--debug` or `RESCALE_DEBUG` — mirroring what compat mode already did.
- Rate-limit visibility and credential-source warnings deliberately bypass the standard logger so they survive that discard. A crawling transfer must still be able to say it is waiting on a rate limit.

### 11. Design of Experiments (`internal/pur/doe/`)

**Purpose**: Expand one base `JobSpec` into a parameter sweep, one job per design point.

`doe.Generate(Options) Result` is pure — no API client, no filesystem, no engine — so the same call serves the CLI's `--preview`, the GUI's live preview, and generation itself. Its output is `[]models.JobSpec`, which is what `pipeline.NewPipeline` already takes as its job ingress, so a sweep inherits tar/upload/create/submit, state and resume, progress events, and both front ends without pipeline changes.

**Structure**:
- `doe.go` — `Options`, `Parameter`, `Case`, `Result`, `Generate`
- `methods.go` — `Methods()`, the single source of each design's label, description and which options it reads; consumed by CLI flag help and the GUI's method menu
- `design.go` / `sobol.go` — the samplers, which produce points in unit coordinates that `render.go` then maps onto each parameter's range or category list
- `validate.go` — every rejection rule, run before anything is sampled
- `cases_csv.go` — `ParseCasesCSV(io.Reader)`, the one parser behind both the CLI's `--cases-csv` and the GUI's pasted-cases box, so identical text yields an identical sweep on either surface

**Values reach the command line**: they are rendered into the job's command through `pur/pattern`'s `{{name}}` substitution rather than passed as environment variables, so each case's configuration is visible on its Rescale job page. Parameters and command tokens are validated against each other in both directions — an unused parameter and an unfilled token are both errors, not silently wrong jobs — and every rendered surface (command, job name, tag) is asserted free of residual tokens afterwards.

**One rejection boundary**: `Generate` is where every limit lives — case count, dimensionality, format syntax, value safety and rendered length — so the CLI and the GUI bindings surface the same errors instead of each carrying their own policy. Nothing is clamped silently; an oversized or malformed sweep is reported before any case is built.

**Shared inputs**: a case never carries a `Directory`, so it always takes the pipeline's skip-tar-and-upload path. `BaseFileIDs` points every case at an already-uploaded deck; left empty, the deck arrives as batch-level Common Files, which the pipeline uploads once and attaches to every job. Either way one deck serves the whole sweep instead of being re-uploaded per case.

---

## CLI Compatibility Mode

**Package**: `internal/cli/compat/` (17 source files, plus 8 of tests)

Provides drop-in compatibility with `rescale-cli` (the legacy Java-based Rescale CLI). Existing scripts and automation workflows can migrate to Interlink without modification.

### Detection and Activation

`IsCompatMode()` in `compat.go` activates when:
1. `--compat` flag is present in args
2. Binary name ends with `rescale-cli` (symlink or rename)

When active, `cmd/rescale-int/main.go` dispatches to `compat.ExecuteCompat()` instead of the native CLI.

### Architecture

Compat mode builds a **separate Cobra command tree** (`NewCompatRootCmd()` in `root.go`) that mirrors rescale-cli's flag syntax. It imports `config`, `api`, `models`, `watch` and `version` directly, plus the shared transfer stack (`cloud/{credentials,download,upload}`, `transfer`, `resources`, `progress`, `http`, `pur/parser`, `util/{analysis,glob}`, `validation`, `constants`) — it does NOT import the `cli` package, which is what avoids the import cycle.

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
2. **Transfer Bindings** (`transfer_bindings.go`): `StartTransfers()`, `CancelTransfer()`, `CancelAllTransfers()`, `GetTransferBatches()`, `CancelBatch()`, `RetryFailedInBatch()`, `GetBatchTasks(batchID, offset, limit, stateFilter)` — the paged reader the Transfers tab uses so rendering a batch's rows does not copy the whole batch, DTOs
3. **File Bindings** (`file_bindings.go`): `ListLocalDirectory()`, `ListRemoteFolder()`, `ListRemoteFolderPage()`, `SearchRemoteFolderContents()`, `ListRemoteLegacy()`, `ListRemoteLegacyWithFilters(cursor, pageSize, ownerFilter, searchQuery, sortField, sortDirection)`, `ListRemoteTrash()`, `RecoverTrashItems()`, `PurgeTrashItems()`, `StartFolderDownload()`, `StartFolderUpload()`
4. **Job Bindings** (`job_bindings.go`): `ScanDirectory()`, `StartBulkRunWithOptions()`, `StartSingleJob()`, `GetRunHistory()`, `GetHistoricalJobRows()`
5. **Job Status Bindings** (`job_status_bindings.go`): `ListJobStatuses()` for the first page and `ListJobStatusesPage(offset)` for subsequent pages, backing the Job Status tab
6. **Config Bindings** (`config_bindings.go`): Configuration management
7. **Daemon Bindings** (`daemon_bindings.go`): Daemon IPC
8. **Event Bridge** (`event_bridge.go`): Forwards EventBus events to Wails runtime, throttles progress updates (100ms interval)
9. **Version Bindings** (`version_bindings.go`): GitHub update check
10. **Reporting Bindings** (`reporting_bindings.go`): Error report display
11. **API Key Source Bindings** (`api_key_source_bindings.go`): Reports back to the GUI which credential source the runtime resolved (token file vs. env vs. config) and any source conflicts
12. **DOE Bindings** (`doe_bindings.go`): `GetDOEMethods()` for the method menu, `PreviewDOECases()` for the debounced live preview (truncated, no job specs), `GenerateDOE()` for the full sweep plus its job specs, `ParseDOECasesCSV()` for pasted explicit cases, `DefaultDOEMaxCases()` for the cap the form shows

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
4. **PURTab** — Batch job pipeline with view modes (choice screen, monitoring, configuration), three job sources (folder scan, file scan, or a parameter sweep), and its own `goBack`/`canGoBack` workflow navigation
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
- **Key Size**: 256-bit (32 bytes)
- **IV Size**: 128-bit (16 bytes)
- **Padding**: PKCS7 (adds 1-16 bytes)
- **Chunk Size**: 16KB for streaming operations
- **Hash Function**: SHA-512 for file integrity

**Streaming implementation** processes files in 16KB chunks with constant ~16KB memory regardless of file size, preventing memory exhaustion on large files (60GB+). See `internal/crypto/encryption.go` and `internal/crypto/streaming.go`.

**Encryption Modes**:
- **Default (streaming)**: AES-256-CBC chained across parts during upload. A single key and IV are stored in cloud metadata, part N's IV is the last ciphertext block of part N-1, and PKCS7 padding is applied only to the final part — so the combined ciphertext is identical to whole-file AES-256-CBC, which is what lets the platform decrypt it. No temporary encrypted file.
- **Legacy (`--pre-encrypt`)**: Full-file encryption before upload. Compatible with older Rescale clients.
- **Legacy read path (v3.1.x)**: per-part HKDF-SHA256 key and IV derivation. Still readable on download (format version 1); never written by new uploads.

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

### Daemon Status Reporting

A background process the user cannot see needs a way to say it is broken, otherwise a failing scan (expired key, dead network, proxy trouble) shows only as a last-scan timestamp that stops advancing:

- The status snapshot carries the most recent scan failure, its code and when it happened, cleared by the next scan that completes. `daemon status` prints it with the age and an actionable hint; the Setup tab renders the same error text and how stale it is.
- Actions that could not happen do not report success. `TriggerPoll` returns *why* no scan started — stopped, paused, or a poll already running, which is also what a wedged poll looks like — and the IPC handlers and the Windows multi-daemon path propagate that rather than swallowing it, so "Scan triggered" in the GUI means a scan started.
- "Save all settings" asks the running daemon to reload and reports what happened. Writing `daemon.conf` alone leaves a running daemon on its old settings, which used to be reported as a plain success.
- Pre-flight validates the download folder with the same write probe `SaveDaemonConfig` gates on. A stat-only check green-lit read-only folders, and every download then failed after the user had been told setup was fine.

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
│  5 files, 7 ifaces   │  │  5 files, 7 ifaces    │
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

`internal/cloud/storage/` is a different thing despite the name: it holds the shared storage error types (`IsDiskFullError` and friends) and an `interfaces.go` declaring four cloud-client interfaces that nothing imports. The error types are all the rest of the tree takes from it — `internal/cloud/transfer/downloader.go` is the only importer.

**Storage Backend Parity**:
- Both S3 and Azure assert the same seven interfaces above
- Same part sizing: neither provider picks its own. `resources.PlanUpload` sizes every upload, streaming or `--pre-encrypt`, before any bytes move and returns an `UploadPlan{PartSize, WorkerCap, QueueDepth}` (`internal/cloud/transfer/uploader.go`, `internal/cloud/upload/upload.go`). Part size starts at the file-size tier (16MB / 32MB / 48MB / 64MB from `constants.ChunkSize` and friends), is clamped so `chunk * threads * 2` fits in 75% of the memory budget, and is then raised to the **part-count floor** — `ceil(fileSize / MaxParts)` rounded up to a whole MB — whenever the tier would need more parts than the backend accepts. The floor overrides both the memory clamp and `MaxChunkSize`, which is what lifted the old 640GB (S3, 10,000 parts) and 3.2TB (Azure, 50,000 blocks) ceilings. When even the floor exceeds the backend's per-part limit, the upload is refused up front and names the largest file that backend can take, rather than failing after every byte has moved. Part size is stamped into the object's `partsize` metadata and CBC chains through it, so it cannot be renegotiated partway
- Same memory budget: `(*Manager).PlanUpload` reserves each plan's peak part-buffer memory against one shared budget keyed by transfer ID, so concurrent uploads narrow each other's pipeline (queue depth first, then worker count, floor `UploadMinThrottledWorkers`) instead of each claiming the whole machine. `resources.CalculateDynamicChunkSize` is the tier-plus-memory step inside the planner; nothing in the transfer path calls it directly any more. Downloads size parts with `resources.ChunkSizeFromFileSize`, which is the same tier table without the memory clamp, for objects whose metadata carries no `partsize`
- Same concurrency model via orchestration layer
- Same restart behaviour: an interrupted upload restarts from the beginning. Every attempt generates a fresh key, IV and object-key suffix, so a `state/` sidecar left by an earlier attempt describes an upload of *different* ciphertext; it is never resumed and the upload restarts from scratch, rather than splicing two encryptions into one object. The mechanism differs: S3 fails the object-key check, aborts the orphaned multipart upload and deletes the stale sidecar; Azure fails the same check and simply starts fresh, leaving the sidecar until a successful upload clears it (`{s3,azure}/pre_encrypt.go`). The streaming default never writes a sidecar at all. Only downloads in the legacy (v0) format resume, by chunk offset
- Transparent to user (auto-detected via provider factory)

### S3 Backend (`internal/cloud/providers/s3/`)

Multi-part upload API. The streaming default always creates a multipart upload, whatever the file size — `constants.MultipartThreshold` (100MB) never reaches it; it only splits a `--pre-encrypt` upload between multipart and a single `PutObject`, and decides whether a legacy download is chunked. Part size from the upload plan as above (`UploadLimits`: 10,000 parts, 5GB per part), concurrent part uploads, credential caching via `EnsureFreshCredentials()`, automatic retry with exponential backoff, seekable upload streams for SDK retry.

### Azure Backend (`internal/cloud/providers/azure/`)

Block blob API, block size from the same upload plan (`UploadLimits`: 50,000 blocks, 4000MB per block), concurrent block upload, automatic credential refresh, same seven interfaces as S3 for consistency.

---

## Performance Optimizations

### Connection Reuse

The S3/Azure transfer client (`http.CreateOptimizedClient`) pools 512 idle connections total, 100 per host, 90s idle timeout, and every operation in a batch reuses it. Two transports keep the smaller pool `http.ConfigureHTTPClient` sets — 100 idle total (`internal/http/proxy.go`): the API client, which uses that function directly, and any transport an NTLM proxy has wrapped, because the optimisation pass only rewrites a plain `*http.Transport`.

### Rate Limiting

Token bucket algorithm with cross-process coordinator. See [Rate Limiter](#5-rate-limiter-internalratelimit) section for details.

### Adaptive Concurrency

`ComputeBatchConcurrency()` in the resource manager dynamically scales concurrent transfers based on median file size:

| Median File Size | Concurrent Transfers | Threads/File |
|-----------------|---------------------|--------------|
| < 100MB (small) | Up to 20 | 1 |
| 100MB – 500MB (medium) | Up to 10 | 1 |
| 500MB – 1GB (medium) | Up to 10 | 4 |
| > 1GB (large) | Up to 5 | 8–16 |

The concurrency tier turns at 100MB and 1GB, but threads-per-file turns at 500MB (`constants.MediumFileThreshold`), which is why the medium band splits. Thread counts are the base tiers, before aggressive mode. Validated against thread pool capacity and 75% of the memory budget. That budget is `getAvailableMemory()`, and it is not a real reading everywhere: on Windows it is free physical memory from `GlobalMemoryStatusEx`, while on macOS and Linux it is 75% of a hardcoded 4GB model minus the current Go heap, clamped to 512MB–8GB (`internal/resources/memory_unix.go`). Applied symmetrically in GUI and CLI.

**Source:** `internal/resources/manager.go`, `internal/constants/app.go`

### FileInfo Enrichment

`ListFolderContentsPage()` parses full metadata from folder listings (encryption keys, storage info, checksums). Downloads therefore skip the per-file `GetFileInfo()` call, turning one API call per file into one per page. Since all v3 endpoints share a 2 req/sec budget, this is the difference between a large folder download being rate-limited by metadata and being limited by the transfer itself.

**Source:** `internal/api/client.go`

### Streaming Scan-to-Download (GUI)

`ScanRemoteFolderStreaming()` uses 8 concurrent workers scanning subfolders, emitting files to a channel. Downloads begin within seconds of scan initiation rather than waiting for full recursive scan.

---

## Threading Model

### CLI Mode

**Main Thread**: Command parsing (Cobra), synchronous execution, progress bar rendering.

**Background Goroutines**: Concurrent uploads/downloads (controlled by `RunBatch` semaphore — 20 / 10 / 5 by median file size, then clamped by the thread pool, the memory budget, `--max-concurrent` and the batch size, with a floor of 1; 5 is the default only for an empty batch), per-file multi-threaded transfers via `TransferHandle`, API calls with timeouts, progress updates.

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

**Layer 2 — Per-File Multi-Threading** (`transfer.Manager.AllocateTransfer` in `internal/transfer/manager.go`, which is what every batch caller invokes; it draws the thread count from `resources.Manager.AllocateForTransfer` underneath):
- When each file starts, allocates threads from the shared pool
- Thread count based on file size tiers (500MB-1GB: 4, 1-5GB: 8, 5-10GB: 12, 10GB+: 16)
- Aggressive mode is on unless the caller configures it, and multiplies those tiers by 1.5x (1-5GB), 1.75x (5-10GB) or 2x (10GB+) *before* the 16-thread and CPU-core caps apply — so on a machine with enough cores the effective counts are 12 / 16 / 16, not 8 / 12 / 16
- Dynamic rebalancing: as files complete, freed threads become available

```
                     ┌──────────────────────────┐
                     │    resources.Manager     │
                     │   (Global Thread Pool)   │
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
    ├─→ 1. Create Tar Archive ─→ /tmp/job-xxxx.tar.gz
    ├─→ 2. Encrypt + Upload ───→ S3/Azure, encrypted per part in flight
    │                            (--pre-encrypt instead writes a temp
    │                             encrypted file, then uploads it)
    ├─→ 3. Register File ──────→ API: POST /api/v3/files/
    ├─→ 4. Create Job ─────────→ API: POST /api/v3/jobs/
    └─→ 5. Submit Job ─────────→ API: POST /api/v2/jobs/{id}/submit/
            │
            ▼
         Rescale Platform
```

### Job Request Construction

A job payload is assembled at three independent sites, and a field added to only some of them is silently dropped rather than rejected — the platform cannot complain about a key it never received. All three must stay in sync:

- typed decode of a `--job-file` (`internal/cli/jobs.go`), which needs only the struct field on `models.JobRequest`
- `pipeline.BuildJobRequest` (`internal/pur/pipeline/pipeline.go`), which copies from `models.JobSpec`
- `SGEMetadata.ToJobRequest` (`internal/pur/parser/sge.go`), plus the two `JobSpec` converters, so a script loaded into the GUI and saved back out keeps what it came in with

The SSH access fields (`cidrRule`, `publicKey`, `sshPort`) are the worked example: the parser read `#RESCALE_INBOUND_SSH_CIDR` and `#RESCALE_PUBLIC_KEY` and then threw them away, and `JobRequest` had nowhere to put them. All three are `omitempty`, so a submit that does not set them produces a byte-identical payload. `--job-file` also warns about top-level keys the decode ignored, so a typo is named instead of vanishing.

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
    ├─→ 2. Detect Format ──────→ v2 CBC / v1 HKDF / v0 legacy
    ├─→ 3. Download + Decrypt ─→ S3/Azure, decrypted per part in flight
    │                            (v0 legacy instead downloads the whole
    │                             encrypted file, then decrypts it. v0, and
    │                             v1 on Azure, are the only paths that
    │                             pre-check disk space)
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
- A shipped bundle must not depend on the host's copy of a library it already bundles. `libwebkit2gtk` forks its helper executables (`WebKitWebProcess`, `WebKitNetworkProcess`) from a path compiled into the library, so bundling the library without the helpers makes the Linux AppImage fork the *host's* helpers — and a mismatched host WebKit fails the IPC handshake, leaving a window that paints but never renders. `build/linux/bundle-webkit.sh` copies the helpers in and gives them `$ORIGIN`-relative RPATHs; `build/linux/verify-appimage.sh` gates the Linux build — the HPC build script runs it on the finished AppImage before packaging the tarball, not GitHub Actions, which has no Linux job — by resolving every helper's libraries with `LD_LIBRARY_PATH` deliberately unset, because setting it would hide exactly the missing RPATH that caused the original bug

### 7. Dependency Injection
- Watch engine has zero imports from CLI packages
- All behavior injected via function types
- Enables sharing between native and compat modes without import cycles

### 8. Truthful Status Reporting

Every surface that reports an outcome must be able to distinguish success, failure, cancellation and "not yet known". This is a correctness property, not a UI nicety — a wrong answer here is worse than no answer, because it is acted on.

- **Exit codes**: a command that printed a failure summary while returning nil taught scripts and CI to treat a failed run as a success. `folders upload-dir` and `pur run` now return an error when any item failed; an aborted prompt cancels the batch rather than continuing through the remaining files; a prompt that cannot run (no terminal) records the file as failed instead of dropping it silently. A cancelled run is deliberately *not* a failure.
- **Cancellation**: see [Transfer Batch Abstraction](#6-transfer-batch-abstraction-internaltransferbatchgo). A cancelled transfer must not be able to render as a completion.
- **Retries and throttling**: a silent retry loop is indistinguishable from a hang. Storage retries and rate-limit waits are published on every surface — progress-bar-safe stderr in the CLI, the daemon's own logger and IPC log buffer, the event bus and footer indicator in the GUI.
- **Error messages must agree with the decision that produced them.** Rebuilding an error message at the call site from different figures than the check used is how a refusal came to read "need 292366 MB, have 312832 MB available".

---

## Constants Management

### Centralized Configuration

**Purpose**: Single source of truth for all configuration values.

**Implementation**: `internal/constants/app.go`

All configuration constants centralized in one file with named constants, inline documentation, logical grouping, and type safety.

**Categories:**

1. **Storage Operations**: `MultipartThreshold` (100MB), `ChunkSize` (32MB), `MinPartSize` (5MB)
2. **Credential Refresh**: `GlobalCredentialRefreshInterval` (10min), `PeriodicCredentialRefreshInterval` (8min, both providers)
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
