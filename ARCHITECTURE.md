# Architecture - Rescale Interlink

**Version**: 2.4.8
**Last Updated**: November 20, 2025

For verified feature details and source code references, see [FEATURE_SUMMARY.md](FEATURE_SUMMARY.md).

---

## Table of Contents

- [System Overview](#system-overview)
- [Package Structure](#package-structure)
- [Key Components](#key-components)
- [Encryption & Security](#encryption--security)
- [Storage Backends](#storage-backends)
- [Performance Optimizations](#performance-optimizations)
- [Threading Model](#threading-model)
- [Data Flow](#data-flow)
- [v2.3.0 Bug Fixes](#v230-bug-fixes)

---

## System Overview

Rescale Interlink is a unified CLI and GUI application for managing Rescale computational jobs. The architecture follows a layered design with clear separation of concerns:

```
┌─────────────────────────────────────────────────────────┐
│                 Rescale Interlink v2.0                   │
│              Unified CLI + GUI Architecture              │
├─────────────────────────────────────────────────────────┤
│                                                          │
│  ┌──────────────────┐         ┌──────────────────┐     │
│  │   CLI Mode       │         │   GUI Mode       │     │
│  │   (default)      │         │   (--gui flag)   │     │
│  ├──────────────────┤         ├──────────────────┤     │
│  │ • Commands       │         │ • Setup Tab      │     │
│  │ • Flags          │         │ • Jobs Tab       │     │
│  │ • Progress Bars  │         │ • Activity Tab   │     │
│  │ • Shell Output   │         │ • Event Display  │     │
│  └────────┬─────────┘         └─────────┬────────┘     │
│           │                             │              │
│           └──────────┬──────────────────┘              │
│                      │                                  │
│               ┌──────▼───────┐                          │
│               │  Core Engine │                          │
│               │  (Shared)    │                          │
│               ├──────────────┤                          │
│               │ • Config     │                          │
│               │ • State      │                          │
│               │ • Pipeline   │                          │
│               └──────┬───────┘                          │
│                      │                                  │
│        ┌─────────────┼─────────────┐                   │
│        │             │             │                    │
│   ┌────▼────┐   ┌───▼────┐   ┌───▼────┐               │
│   │ API     │   │ Events │   │ Cache  │               │
│   │ Client  │   │ Bus    │   │ Layer  │               │
│   └─────────┘   └────────┘   └────────┘               │
│                                                          │
└─────────────────────────────────────────────────────────┘
          │                    │                    │
          ▼                    ▼                    ▼
    ┌─────────┐          ┌─────────┐        ┌──────────┐
    │ Rescale │          │ Local   │        │ User     │
    │ API     │          │ Files   │        │ Terminal │
    └─────────┘          └─────────┘        └──────────┘
```

---

## Package Structure

### Top-Level Organization (Reorganized November 2025)

```
rescale-int/
├── cmd/
│   └── rescale-int/           # Main entry point
│       ├── main.go            # CLI/GUI router
│       ├── setup_tab.go       # GUI: Configuration
│       ├── jobs_tab.go        # GUI: Job management
│       └── activity_tab.go    # GUI: Logs/activity
│
├── internal/
│   ├── api/                   # Rescale API client
│   ├── cli/                   # CLI commands and helpers
│   ├── cloud/                 # Cloud storage abstraction
│   │   ├── credentials/       # Credential management (S3/Azure)
│   │   ├── upload/            # Upload implementation (S3/Azure)
│   │   ├── download/          # Download implementation (S3/Azure)
│   │   └── storage/           # Storage utilities and errors
│   ├── config/                # Configuration and CSV parsing
│   ├── constants/             # Application-wide constants
│   ├── core/                  # Core engine
│   ├── crypto/                # AES-256-CBC encryption
│   ├── events/                # Event bus system
│   ├── gui/                   # GUI implementation (Fyne)
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
│   ├── ratelimit/             # Rate limiting (token bucket)
│   ├── state/                 # State manager
│   ├── trace/                 # Tracing
│   └── validation/            # Path validation
│
├── bin/                       # Pre-built binaries (organized by version/platform)
├── testdata/                  # Test fixtures
└── assets/                    # Application assets
```

### Import Dependencies

```
cmd/rescale-int
    ├─→ internal/cli
    ├─→ internal/gui
    ├─→ internal/core
    └─→ internal/events

internal/cli
    ├─→ internal/core
    ├─→ internal/progress
    ├─→ internal/pur/api
    └─→ internal/pur/models

internal/gui
    ├─→ internal/core
    ├─→ internal/events
    └─→ fyne.io/fyne/v2

internal/core
    ├─→ internal/events
    ├─→ internal/pur/api
    ├─→ internal/pur/config
    ├─→ internal/pur/state
    └─→ internal/pur/models
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
- Pipeline execution (tar → upload → create → submit)
- State persistence
- Event emission for UI updates

**Thread Safety**: All public methods are thread-safe using RWMutex

### 2. API Client (`internal/pur/api/`)

**Purpose**: Interface to Rescale Platform REST API v3

**Key Features**:
- HTTP client with connection pooling
- Automatic retry with exponential backoff
- Rate limiting (dual token bucket)
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

### 4. Folder Cache (`internal/pur/api/folders.go`)

**Purpose**: Reduce API calls by 99.8% for folder operations

**Implementation**:
```go
type FolderCache struct {
    cache map[string]*CacheEntry
    mu    sync.RWMutex
    ttl   time.Duration
}

type CacheEntry struct {
    folderID  string
    timestamp time.Time
}
```

**Cache Operations**:
- `Get(path)`: Retrieve cached folder ID
- `Set(path, id)`: Store folder ID with timestamp
- `Invalidate(path)`: Remove from cache
- `Clear()`: Remove all entries

**TTL Management**: Entries expire after 5 minutes by default

**Cache Invalidation**:
- Folder creation: Invalidate parent path
- Folder deletion: Invalidate exact path
- Folder move: Invalidate both old and new paths

### 5. Rate Limiter (`internal/pur/api/ratelimit.go`)

**Purpose**: Prevent API throttling (429 errors)

**Algorithm**: Dual token bucket

**Token Buckets**:
```go
type TokenBucket struct {
    tokens       int
    maxTokens    int
    refillRate   time.Duration
    lastRefill   time.Time
    mu           sync.Mutex
}
```

**Configuration**:
```go
// Bucket 1: General operations (files, folders)
generalLimiter := NewTokenBucket(
    capacity: 10,           // Burst capacity
    refillRate: 120ms,      // 500/min = 8.3/sec
)

// Bucket 2: Job submissions
jobLimiter := NewTokenBucket(
    capacity: 2,            // Burst capacity
    refillRate: 12s,        // 5/min = 0.083/sec
)
```

**Backoff Strategy**:
- On 429 response: Wait for Retry-After header (if present)
- Otherwise: Exponential backoff with jitter
- Base delay: 1s, doubles each retry (1s, 2s, 4s, 8s, 16s)
- Max delay: 32s
- Jitter: ±20% to prevent thundering herd

### 6. Disk Space Checker (`internal/pur/diskspace/`)

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

### AES-256-CBC Encryption (`internal/pur/encryption/`)

**Implementation**: `internal/pur/encryption/encryption.go`

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

**Streaming Implementation (v2.3.0)**:
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

**Decryption (v2.3.0)**:
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

**v2.3.0 Improvements**:
- Rewrote encryption/decryption to use streaming (16KB chunks)
- Prevents memory exhaustion on large files (60GB+)
- Maintains low memory footprint throughout operation

---

## Storage Backends

### S3 Backend (`internal/pur/upload/s3.go`, `internal/pur/download/s3.go`)

**Supported**: AWS S3 and S3-compatible services

**Upload Features**:
- Multi-part upload API for files >100MB
- Part size: 16MB chunks
- Concurrent part uploads (configurable threads)
- Credential caching (10-minute TTL)
- Automatic retry with exponential backoff

**Download Features**:
- Concurrent chunk download using range requests
- Chunk size: 16MB
- Resume capability with range validation
- Automatic credential refresh

**Implementation**:
```go
// S3 multi-part upload
type S3Uploader struct {
    s3Client  *s3.Client
    bucket    string
    key       string
    uploadID  string
}

// Concurrent chunk download
func (d *S3Downloader) DownloadConcurrent(fileID string, chunks int) error {
    // Split file into chunks
    // Download chunks in parallel with semaphore
    // Merge chunks into final file
}
```

### Azure Backend (`internal/pur/upload/azure.go`, `internal/pur/download/azure.go`)

**Supported**: Azure Blob Storage

**Upload Features**:
- Block blob API
- Block size: 16MB
- Concurrent block upload
- Automatic credential refresh before expiration
- Same interface as S3 for consistency

**Download Features**:
- Concurrent block download
- Block size: 16MB
- Resume capability
- Identical user experience to S3

**Implementation**:
```go
// Azure block blob upload
type AzureUploader struct {
    blobClient *azblob.BlockBlobClient
    containerURL string
}

// Same concurrency model as S3
func (u *AzureUploader) UploadConcurrent(file string, threads int) error {
    // Split into blocks
    // Upload blocks in parallel
    // Commit block list
}
```

**Storage Backend Parity**:
- Both S3 and Azure support identical features
- Same chunk/part size (16MB)
- Same concurrency model
- Same resume capability
- Same progress tracking
- Transparent to user (auto-detected)

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

**Problem**: Bursts of API calls trigger 429 throttling

**Solution**: Token bucket algorithm with predictable pacing

**Benefits**:
- Eliminates 429 errors
- Reduces total execution time (no retries)
- Predictable, smooth API usage

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

---

## Threading Model

### CLI Mode

**Main Thread**:
- Command parsing (Cobra)
- Synchronous execution of most commands
- Progress bar rendering

**Background Goroutines**:
- Concurrent uploads (controlled by semaphore)
- API calls with timeouts
- Progress updates

**Synchronization**:
- WaitGroups for concurrent operations
- Mutexes for shared state (minimal)
- Channels for coordination

### GUI Mode

**Main Thread** (Fyne Requirement):
- All UI operations MUST run on main thread
- Widget creation and updates
- Event loop

**Background Goroutines**:
- API calls
- File I/O operations
- Job status polling (every 30 seconds)
- Event emission

**UI Updates from Goroutines**:
```go
// WRONG: Direct UI update from goroutine
func (jt *JobsTab) updateFromBackground() {
    jt.table.Refresh()  // CRASHES: not on main thread
}

// CORRECT: Queue on main thread
func (jt *JobsTab) updateFromBackground() {
    fyne.CurrentApp().Driver().CanvasForObject(jt.table).
        QueueMain(func() {
            jt.table.Refresh()  // Safe: on main thread
        })
}
```

### Deadlock Prevention

**Problem** (Fixed in v1.1.0): Holding write lock during UI refresh

**Pattern**:
```go
// WRONG: Deadlock-prone
func update() {
    mu.Lock()
    defer mu.Unlock()
    data = newData
    table.Refresh()  // DEADLOCK: Refresh acquires another lock
}

// CORRECT: Release before refresh
func update() {
    mu.Lock()
    data = newData
    mu.Unlock()  // ✓ Released BEFORE refresh

    table.Refresh()  // ✓ Safe: no locks held
}
```

**Validation**: Static analysis + unit tests confirm all code follows safe pattern

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
- Rate limiting prevents problems before they occur

### 6. Cross-Platform Compatibility
- Abstract platform differences (disk space, file paths)
- Test on all target platforms
- Provide consistent user experience

---

## Constants Management (v2.4.3)

### Centralized Configuration

**Purpose**: Single source of truth for all configuration values, reducing errors and improving maintainability.

**Implementation**: `internal/pur/constants/constants.go`

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
   - `ChunkSize = 16MB` - Part size for S3, block size for Azure, range size for downloads
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

## v2.3.0 Bug Fixes

### 1. Resume Logic Fix (November 17, 2025)

**Problem**: Resume validation failed for complete encrypted files due to PKCS7 padding

**Root Cause**: Code checked for exact file size match, but PKCS7 padding adds 1-16 bytes

**Solution**: Updated size validation to accept range
```go
// Before (incorrect)
if encryptedSize != expectedSize {
    return fmt.Errorf("size mismatch: got %d, expected %d", encryptedSize, expectedSize)
}

// After (correct) - internal/cli/download_helper.go:163-186
minExpected := expectedSize + 1   // Minimum padding (1 byte)
maxExpected := expectedSize + 16  // Maximum padding (16 bytes)
if encryptedSize < minExpected || encryptedSize > maxExpected {
    return fmt.Errorf("size mismatch: got %d, expected %d-%d",
        encryptedSize, minExpected, maxExpected)
}
```

**Impact**: Prevents unnecessary re-downloads of complete files

### 2. Decryption Progress Feedback (November 17, 2025)

**Problem**: Users confused during long decryption operations (40+ minutes for 60GB files)

**Solution**: Added progress message before decryption starts
```go
// internal/pur/download/s3_concurrent.go:458
fmt.Fprintf(out, "Decrypting %s (this may take several minutes for large files)...\n",
    filepath.Base(outputPath))
```

**Impact**:
- Clear communication during long operations
- Prevents users from thinking process has hung
- Applied to both S3 and Azure backends

### 3. Progress Bar Corruption Fix (November 17, 2025)

**Problem**: Print statements bypassed mpb output writer, causing corrupted progress bars

**Root Cause**: Direct use of `fmt.Printf` instead of mpb's io.Writer

**Solution**: Routed all output through mpb container's io.Writer
```go
// Before (incorrect)
fmt.Printf("Uploading file...\n")  // Bypasses mpb

// After (correct)
out := progressContainer.GetWriter()
fmt.Fprintf(out, "Uploading file...\n")  // Goes through mpb
```

**Files Updated**: 17 files across internal/cli/ and internal/pur/

**Impact**:
- Clean progress bar display
- No "ghost bars" or corruption
- Professional terminal output

---

**Last Updated**: November 20, 2025
**Version**: 2.4.8
**Status**: Production ready with all critical bugs fixed
