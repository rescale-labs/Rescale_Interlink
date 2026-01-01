package constants

import (
	"time"
)

// Storage operation thresholds
const (
	// MultipartThreshold - files larger than this use multipart/block upload (100 MB)
	// Used by both S3 multipart and Azure block blob uploads
	MultipartThreshold = 100 * 1024 * 1024

	// ChunkSize - base size of each chunk for uploads and downloads (32 MB)
	// This is used as the default for:
	// - PartSize for S3 multipart uploads
	// - BlockSize for Azure block blob uploads
	// - RangeChunkSize for S3/Azure downloads
	//
	// See CalculateDynamicChunkSize() for adaptive sizing based on file size
	// and available memory. Dynamic range: 16 MB - 64 MB.
	//
	// Trade-offs:
	// - Smaller chunks = more HTTP requests but better progress granularity
	// - Larger chunks = better throughput but coarser progress updates
	ChunkSize = 32 * 1024 * 1024

	// MinChunkSize - minimum chunk size for transfers (16 MB)
	// Used by dynamic chunk sizing to prevent excessive HTTP overhead.
	MinChunkSize = 16 * 1024 * 1024

	// MaxChunkSize - maximum chunk size for transfers (64 MB)
	// Used by dynamic chunk sizing to cap memory usage per chunk.
	MaxChunkSize = 64 * 1024 * 1024

	// MinPartSize - AWS S3 minimum part size (5 MB, except last part)
	// Azure has no equivalent minimum (can use any size)
	MinPartSize = 5 * 1024 * 1024

	// MaxS3PartSize - AWS S3 maximum part size (5 GB)
	MaxS3PartSize = 5 * 1024 * 1024 * 1024

	// MaxAzureBlockSize - Azure maximum block size (4000 MB with large block support)
	// Standard accounts support 100 MB max, but we use 4 GB for premium/large block accounts
	MaxAzureBlockSize = 4000 * 1024 * 1024

	// MinAzureBlockSize - Azure minimum block size (1 byte, but we use 1 MB for practical purposes)
	MinAzureBlockSize = 1 * 1024 * 1024
)

// Credential refresh intervals
const (
	// GlobalCredentialRefreshInterval - interval for global credential manager refresh (10 minutes)
	// AWS credentials expire at ~15 minutes, refresh with 5 min buffer
	// Azure SAS tokens also expire at ~15 minutes
	GlobalCredentialRefreshInterval = 10 * time.Minute

	// AzurePeriodicRefreshInterval - periodic refresh for Azure long-running operations (8 minutes)
	// Only used for Azure uploads/downloads of files >1GB
	// Provides additional safety layer since Azure SDK has no automatic refresh
	AzurePeriodicRefreshInterval = 8 * time.Minute

	// LargeFileThreshold - files larger than this trigger periodic credential refresh (1 GB)
	// Only applies to Azure backend (S3 uses AWS SDK automatic refresh)
	LargeFileThreshold = 1 * 1024 * 1024 * 1024
)

// Retry configuration
const (
	// MaxRetries - maximum number of retries for transient errors
	MaxRetries = 10

	// RetryInitialDelay - initial delay before first retry (200ms)
	RetryInitialDelay = 200 * time.Millisecond

	// RetryMaxDelay - maximum delay between retries (15s)
	// Exponential backoff with jitter caps at this value
	RetryMaxDelay = 15 * time.Second
)

// Disk space safety margin
const (
	// DiskSpaceBufferPercent - additional space to require beyond file size (15%)
	// Accounts for temporary files, encryption overhead, etc.
	DiskSpaceBufferPercent = 0.15
)

// Event System
const (
	// EventBusDefaultBuffer - default buffer size for event channels (1000)
	// Reduced from 10,000 to decrease memory footprint per subscriber
	// 1000 events is still generous for typical event throughput
	EventBusDefaultBuffer = 1000

	// EventBusMaxBuffer - maximum buffer size for high-throughput scenarios (5000)
	EventBusMaxBuffer = 5000
)

// Pipeline Queues
const (
	// DefaultQueueMultiplier - multiplier for queue size based on worker count
	// Queue size = workers * multiplier for optimal throughput
	DefaultQueueMultiplier = 2

	// MaxQueueSize - absolute maximum queue size to prevent unbounded growth
	MaxQueueSize = 1000
)

// UI Updates
const (
	// TableRefreshMinInterval - minimum time between table refreshes (100ms)
	// Prevents excessive UI updates during rapid state changes
	TableRefreshMinInterval = 100 * time.Millisecond

	// TableRefreshBatchInterval - interval for batched table updates (1 second)
	// Used during bulk operations to improve performance
	TableRefreshBatchInterval = 1 * time.Second

	// ProgressUpdateInterval - interval for progress bar updates (250ms)
	// Balances responsiveness with performance
	ProgressUpdateInterval = 250 * time.Millisecond
)

// Activity Log
const (
	// ActivityLogInitialCapacity - initial capacity for activity log entries
	ActivityLogInitialCapacity = 100

	// ActivityLogMaxEntries - maximum number of log entries to retain
	ActivityLogMaxEntries = 10000
)

// Thread Pool
const (
	// AbsoluteMaxThreads - absolute maximum threads allowed
	AbsoluteMaxThreads = 32

	// MemoryPerThreadMB - estimated memory usage per thread (128 MB)
	// Accounts for: 64MB part buffer + 64MB encryption/decryption + overhead
	MemoryPerThreadMB = 128
)

// Monitoring
const (
	// JobPollInterval - interval for polling job status updates (30 seconds)
	JobPollInterval = 30 * time.Second

	// HealthCheckInterval - interval for system health checks (60 seconds)
	HealthCheckInterval = 60 * time.Second
)

// CLI Concurrency Limits
const (
	// DefaultMaxConcurrent - default concurrent file operations
	DefaultMaxConcurrent = 5

	// MinMaxConcurrent - minimum concurrent operations (sequential mode)
	MinMaxConcurrent = 1

	// MaxMaxConcurrent - maximum concurrent operations allowed
	MaxMaxConcurrent = 10
)

// Resource Manager - Thread Limits
const (
	// MaxBaselineThreads - maximum baseline threads from CPU cores
	MaxBaselineThreads = 16

	// MinThreadsPerFile - minimum threads per file transfer
	MinThreadsPerFile = 1

	// MaxThreadsPerFile - maximum threads per file transfer
	// Increased to 16 for better utilization of high-bandwidth connections
	MaxThreadsPerFile = 16
)

// Resource Manager - File Size Thresholds
const (
	// SmallFileThreshold - files below this use sequential transfer (100 MB)
	SmallFileThreshold = 100 * 1024 * 1024

	// MediumFileThreshold - threshold for medium-sized files (500 MB)
	MediumFileThreshold = 500 * 1024 * 1024

	// LargeFile1GB - 1 GB threshold for thread allocation
	LargeFile1GB = 1 * 1024 * 1024 * 1024

	// LargeFile5GB - 5 GB threshold for thread allocation
	LargeFile5GB = 5 * 1024 * 1024 * 1024

	// LargeFile10GB - 10 GB threshold for thread allocation
	LargeFile10GB = 10 * 1024 * 1024 * 1024
)

// Resource Manager - Thread Allocation (Non-AutoScale)
const (
	// ThreadsForSmallFiles - threads for files < 500MB
	ThreadsForSmallFiles = 1

	// ThreadsForMediumFiles - threads for files 500MB - 1GB
	ThreadsForMediumFiles = 2

	// ThreadsForLargeFiles - threads for files > 1GB
	ThreadsForLargeFiles = 3
)

// Resource Manager - Thread Allocation (AutoScale)
// Increased thread counts for better performance on high-bandwidth connections
const (
	// ThreadsFor500MBto1GB - threads for 500MB - 1GB range
	ThreadsFor500MBto1GB = 4

	// ThreadsFor1GBto5GB - threads for 1GB - 5GB range
	ThreadsFor1GBto5GB = 8

	// ThreadsFor5GBto10GB - threads for 5GB - 10GB range
	ThreadsFor5GBto10GB = 12

	// ThreadsFor10GBPlus - threads for files 10GB+
	ThreadsFor10GBPlus = 16
)

// Resource Manager - Throughput Monitoring
const (
	// MaxThroughputSamples - keep last N samples for throughput analysis
	MaxThroughputSamples = 10

	// MinScaleUpThroughputMBps - minimum MB/s to consider scaling up
	MinScaleUpThroughputMBps = 10.0

	// MaxScaleUpVarianceMBps - maximum variance MB/s for scale-up eligibility
	MaxScaleUpVarianceMBps = 2.0

	// ScaleDownThresholdPercent - throughput drop percentage that triggers scale-down
	ScaleDownThresholdPercent = 0.8
)

// System Memory Limits
const (
	// MinSystemMemory - minimum available memory (512 MB)
	MinSystemMemory = 512 * 1024 * 1024

	// MaxSystemMemory - maximum memory cap (8 GB)
	MaxSystemMemory = 8 * 1024 * 1024 * 1024
)

// Encryption
const (
	// EncryptionChunkSize - chunk size for AES encryption operations (16 KB)
	// Different from upload/download ChunkSize which is for network transfers
	EncryptionChunkSize = 16 * 1024
)

// GUI Operation Timeouts
const (
	// GUITransferTimeout - timeout for upload/download operations (30 minutes)
	// Applies to bulk file transfer operations in the GUI
	GUITransferTimeout = 30 * time.Minute

	// GUIOperationTimeout - timeout for individual operations (5 minutes)
	// Applies to single-file operations and folder listing
	GUIOperationTimeout = 5 * time.Minute
)

// Overall Operation Timeouts
const (
	// MaxOperationTimeout - absolute maximum time for any single file transfer (4 hours)
	// This is a safety limit to prevent zombie operations consuming resources indefinitely.
	// Even the largest files (100GB+) should complete within 4 hours on reasonable connections.
	// Individual parts have their own timeouts (10 minutes), but this caps the overall operation.
	MaxOperationTimeout = 4 * time.Hour

	// LargeFileOperationTimeout - timeout for large file transfers (2 hours)
	// Applied to files > 10GB that use concurrent multipart transfers
	LargeFileOperationTimeout = 2 * time.Hour

	// SmallFileOperationTimeout - timeout for small file transfers (30 minutes)
	// Applied to files < 100MB that use single-part transfers
	SmallFileOperationTimeout = 30 * time.Minute
)

// API and Context Timeouts
const (
	// APIContextTimeout - default timeout for API operations (30 seconds)
	APIContextTimeout = 30 * time.Second

	// APIConnectionTestTimeout - timeout for testing API connectivity (10 seconds)
	APIConnectionTestTimeout = 10 * time.Second

	// ValidationCacheTTL - time-to-live for cached validation results (5 minutes)
	ValidationCacheTTL = 5 * time.Minute
)

// HTTP Client Timeouts
const (
	// HTTPIdleConnTimeout - how long to keep idle connections open (90 seconds)
	HTTPIdleConnTimeout = 90 * time.Second

	// HTTPTLSHandshakeTimeout - timeout for TLS handshake (60 seconds)
	HTTPTLSHandshakeTimeout = 60 * time.Second

	// HTTPExpectContinueTimeout - timeout for 100-continue response (1 second)
	HTTPExpectContinueTimeout = 1 * time.Second

	// HTTPDialTimeout - timeout for establishing connection (30 seconds)
	HTTPDialTimeout = 30 * time.Second

	// HTTPDialKeepAlive - keep-alive period for dialer (30 seconds)
	HTTPDialKeepAlive = 30 * time.Second
)

// Pipeline and Job Timeouts
const (
	// PipelineTickerInterval - interval for pipeline progress updates (2 seconds)
	PipelineTickerInterval = 2 * time.Second

	// PipelineStateCheckInterval - interval for checking pipeline state (10 seconds)
	PipelineStateCheckInterval = 10 * time.Second

	// JobTailTickerInterval - interval for job tail updates (5 seconds)
	JobTailTickerInterval = 5 * time.Second

	// JobProgressLogInterval - interval for job progress log messages (30 seconds)
	JobProgressLogInterval = 30 * time.Second
)

// Rate Limiter Timeouts
const (
	// RateLimitWarningThreshold - delay threshold to show warning (2 seconds)
	RateLimitWarningThreshold = 2 * time.Second

	// RateLimitWarningInterval - minimum interval between warnings (10 seconds)
	RateLimitWarningInterval = 10 * time.Second

	// RateLimitLogThreshold - delay threshold for logging (5 seconds)
	RateLimitLogThreshold = 5 * time.Second
)

// Pagination Safety Limits
const (
	// MaxPaginationPages - maximum pages to fetch before stopping (prevents infinite loops)
	// At 100 items/page, this allows up to 100,000 items which should cover any reasonable use case
	MaxPaginationPages = 1000

	// PaginationWarningThreshold - log warning when approaching limit (90% of max)
	PaginationWarningThreshold = 900
)

// Local File Browser (v4.0.3)
const (
	// DirectoryReadTimeout - timeout for reading a local directory (30 seconds)
	// Prevents UI freeze on hung network mounts (NFS/SMB)
	DirectoryReadTimeout = 30 * time.Second

	// SlowPathWarningThreshold - threshold for showing "slow path" warning (5 seconds)
	// If directory listing takes longer than this, we warn the user
	SlowPathWarningThreshold = 5 * time.Second

	// SymlinkWorkerCount - number of parallel workers for symlink resolution (8)
	// Used when a directory contains many symlinks that need os.Stat() calls
	SymlinkWorkerCount = 8
)
