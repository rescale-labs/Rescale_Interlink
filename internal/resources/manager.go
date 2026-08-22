package resources

import (
	"fmt"
	"runtime"
	"sort"
	"sync"

	"github.com/rescale/rescale-int/internal/constants"
)

// Manager manages a shared pool of threads/goroutines for file transfers
// It allocates threads between concurrent files and concurrent parts within files
type Manager struct {
	totalThreads        int              // Total threads in the pool
	availableThreads    int              // Currently available (not allocated)
	baselineThreads     int              // Baseline calculated from CPU cores
	cpuCores            int              // Logical cores this manager plans against
	memoryBudget        uint64           // Bytes this manager plans against
	availableMemory     int64            // Budget not yet reserved by an upload plan
	memoryLimit         int              // Max threads based on memory
	autoScale           bool             // Whether auto-scaling is enabled
	aggressiveMode      bool             // Use more threads for large files
	aggressiveThreshold int64            // File size threshold for aggressive mode
	allocations         map[string]int   // Track allocations per transfer ID
	memoryReservations  map[string]int64 // Track upload-plan byte reservations per transfer ID
	mu                  sync.Mutex       // Protects all fields
}

// Config holds configuration for the resource manager
type Config struct {
	MaxThreads          int   // User-specified max threads (0 = auto-detect)
	AutoScale           bool  // Enable auto-scaling
	AggressiveMode      bool  // More aggressive thread allocation for large files
	AggressiveThreshold int64 // File size threshold for aggressive mode (default 100MB)

	// CPUCores overrides the logical core count used for thread math
	// (0 = detect with runtime.NumCPU()). Per-file allocation is capped at the
	// core count, so tests set this to pin allocation to a synthetic machine
	// rather than to whatever host they happen to run on.
	CPUCores int

	// MemoryBudget overrides the byte budget used for chunk sizing, batch
	// concurrency and upload plans (0 = detect with getAvailableMemory()).
	// Follows the CPUCores convention: resolved once in NewManager so every
	// reader sees one consistent machine, and set by tests that would otherwise
	// assert against whatever host they run on.
	MemoryBudget uint64
}

// NewManager creates a new resource manager
func NewManager(config Config) *Manager {
	// Calculate baseline from CPU cores
	cores := config.CPUCores
	if cores < 1 {
		cores = runtime.NumCPU()
	}
	baselineThreads := cores * 2
	if baselineThreads > constants.MaxBaselineThreads {
		baselineThreads = constants.MaxBaselineThreads
	}

	// Calculate memory constraint
	availableMemory := config.MemoryBudget
	if availableMemory == 0 {
		availableMemory = getAvailableMemory()
	}
	memoryThreads := int(availableMemory / (constants.MemoryPerThreadMB * 1024 * 1024))

	// Determine total threads
	totalThreads := baselineThreads
	if memoryThreads < totalThreads {
		totalThreads = memoryThreads
	}
	if totalThreads > constants.AbsoluteMaxThreads {
		totalThreads = constants.AbsoluteMaxThreads
	}
	if totalThreads < constants.MinThreadsPerFile {
		totalThreads = constants.MinThreadsPerFile
	}

	// User override
	if config.MaxThreads > 0 {
		totalThreads = config.MaxThreads
		if totalThreads > constants.AbsoluteMaxThreads {
			totalThreads = constants.AbsoluteMaxThreads
		}
		if totalThreads < constants.MinThreadsPerFile {
			totalThreads = constants.MinThreadsPerFile
		}
	}

	// Set default aggressive mode settings
	aggressiveMode := config.AggressiveMode
	aggressiveThreshold := config.AggressiveThreshold
	if aggressiveThreshold == 0 {
		aggressiveThreshold = constants.SmallFileThreshold // 100MB default
	}

	// Enable aggressive mode by default for better performance
	// This is safe because we cap at CPU cores
	if !config.AggressiveMode && config.AggressiveThreshold == 0 {
		aggressiveMode = true
	}

	return &Manager{
		totalThreads:        totalThreads,
		availableThreads:    totalThreads,
		baselineThreads:     baselineThreads,
		cpuCores:            cores,
		memoryBudget:        availableMemory,
		availableMemory:     int64(availableMemory),
		memoryLimit:         memoryThreads,
		autoScale:           config.AutoScale,
		aggressiveMode:      aggressiveMode,
		aggressiveThreshold: aggressiveThreshold,
		allocations:         make(map[string]int),
		memoryReservations:  make(map[string]int64),
	}
}

// AllocateForTransfer allocates threads for a specific transfer
// Returns the number of threads allocated
func (m *Manager) AllocateForTransfer(transferID string, fileSize int64, totalFiles int) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Calculate desired threads based on file size and auto-scaling settings
	desired := m.calculateDesiredThreads(fileSize, totalFiles)

	// Allocate what we can from available pool
	allocated := desired
	if allocated > m.availableThreads {
		allocated = m.availableThreads
	}
	if allocated < constants.MinThreadsPerFile {
		allocated = constants.MinThreadsPerFile
	}

	m.availableThreads -= allocated
	m.allocations[transferID] = allocated

	return allocated
}

// ReleaseTransfer releases threads allocated to a transfer, plus any upload-plan
// memory the transfer still holds. Releasing memory here is a backstop: callers
// that plan an upload should release the plan as soon as the pipeline drains,
// rather than holding the budget for the rest of the transfer.
func (m *Manager) ReleaseTransfer(transferID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if allocated, exists := m.allocations[transferID]; exists {
		m.availableThreads += allocated
		delete(m.allocations, transferID)
	}
	m.releaseUploadPlanLocked(transferID)
}

// TryAcquire attempts to acquire up to `count` additional threads for an existing transfer.
// Returns the number of threads actually acquired (0 if none available).
// This is used for dynamic scaling - when other transfers complete, their threads
// become available and can be claimed by remaining active transfers.
func (m *Manager) TryAcquire(transferID string, count int) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Only allow acquiring for existing transfers
	if _, exists := m.allocations[transferID]; !exists {
		return 0
	}

	if count > m.availableThreads {
		count = m.availableThreads
	}
	if count <= 0 {
		return 0
	}

	m.availableThreads -= count
	m.allocations[transferID] += count
	return count
}

// GetMaxForFileSize returns the maximum threads recommended for a file of the given size.
// This is used for dynamic scaling to determine the upper bound for thread acquisition.
func (m *Manager) GetMaxForFileSize(fileSize int64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calculateDesiredThreads(fileSize, 1) // Assume single file for max
}

// GetAvailableThreads returns the current number of available threads
func (m *Manager) GetAvailableThreads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.availableThreads
}

// GetAvailableUploadMemory returns the bytes of the memory budget that no upload
// plan currently holds.
func (m *Manager) GetAvailableUploadMemory() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.availableMemory
}

// GetTotalThreads returns the total thread pool size
func (m *Manager) GetTotalThreads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totalThreads
}

// GetStats returns current resource manager statistics
func (m *Manager) GetStats() ManagerStats {
	m.mu.Lock()
	defer m.mu.Unlock()

	activeTransfers := len(m.allocations)
	activeThreads := m.totalThreads - m.availableThreads

	return ManagerStats{
		TotalThreads:     m.totalThreads,
		AvailableThreads: m.availableThreads,
		ActiveThreads:    activeThreads,
		ActiveTransfers:  activeTransfers,
		BaselineThreads:  m.baselineThreads,
		MemoryLimit:      m.memoryLimit,
		AutoScaleEnabled: m.autoScale,
	}
}

// ManagerStats holds statistics about the resource manager
type ManagerStats struct {
	TotalThreads     int
	AvailableThreads int
	ActiveThreads    int
	ActiveTransfers  int
	BaselineThreads  int
	MemoryLimit      int
	AutoScaleEnabled bool
}

// calculateDesiredThreads determines how many threads a transfer should get
// This is called with the lock already held
func (m *Manager) calculateDesiredThreads(fileSize int64, totalFiles int) int {
	cpuCores := m.cpuCores

	// For small files, use sequential
	if fileSize < constants.SmallFileThreshold {
		return constants.MinThreadsPerFile
	}

	// If auto-scaling is disabled, use conservative defaults
	if !m.autoScale {
		if fileSize < constants.MediumFileThreshold {
			return constants.ThreadsForSmallFiles
		}
		if fileSize < constants.LargeFile1GB {
			return constants.ThreadsForMediumFiles
		}
		return constants.ThreadsForLargeFiles
	}

	// Auto-scaling logic

	// Calculate per-file share of total pool
	poolShare := m.totalThreads
	if totalFiles > 1 {
		poolShare = m.totalThreads / totalFiles
		if poolShare < constants.MinThreadsPerFile {
			poolShare = constants.MinThreadsPerFile
		}
	}

	// Determine desired threads based on file size
	desired := constants.MinThreadsPerFile
	if fileSize >= constants.MediumFileThreshold && fileSize < constants.LargeFile1GB {
		desired = constants.ThreadsFor500MBto1GB
	} else if fileSize >= constants.LargeFile1GB && fileSize < constants.LargeFile5GB {
		desired = constants.ThreadsFor1GBto5GB
	} else if fileSize >= constants.LargeFile5GB && fileSize < constants.LargeFile10GB {
		desired = constants.ThreadsFor5GBto10GB
	} else if fileSize >= constants.LargeFile10GB {
		desired = constants.ThreadsFor10GBPlus
	}

	// Aggressive mode: double threads for large files, capped at CPU cores
	// This improves throughput for multi-GB files where network/disk can handle more parallelism
	if m.aggressiveMode && fileSize >= m.aggressiveThreshold {
		// Scale factor based on file size
		if fileSize >= constants.LargeFile10GB {
			// 10GB+: use up to 2x threads
			desired = desired * 2
		} else if fileSize >= constants.LargeFile5GB {
			// 5-10GB: use up to 1.75x threads
			desired = desired * 7 / 4
		} else if fileSize >= constants.LargeFile1GB {
			// 1-5GB: use up to 1.5x threads
			desired = desired * 3 / 2
		}
		// else: 100MB-1GB uses base allocation
	}

	// Cap at pool share
	if desired > poolShare {
		desired = poolShare
	}

	// Never exceed max threads per file
	if desired > constants.MaxThreadsPerFile {
		desired = constants.MaxThreadsPerFile
	}

	// Never exceed CPU cores (hard limit for aggressive mode)
	if desired > cpuCores {
		desired = cpuCores
	}

	return desired
}

// ComputeBatchConcurrency determines optimal concurrent transfer count for a batch
// based on the median file size, validated against thread pool capacity and memory.
// maxAllowed is the upper cap (typically cap(semaphore)).
func (m *Manager) ComputeBatchConcurrency(fileSizes []int64, maxAllowed int) int {
	if len(fileSizes) == 0 {
		return constants.DefaultMaxConcurrent
	}

	// Compute median file size
	sorted := make([]int64, len(fileSizes))
	copy(sorted, fileSizes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	median := sorted[len(sorted)/2]

	// Determine tier from file size distribution
	var tier int
	switch {
	case median < int64(constants.SmallFileThreshold):
		tier = constants.AdaptiveSmallFileConcurrency
	case median < int64(constants.LargeFile1GB):
		tier = constants.AdaptiveMediumFileConcurrency
	default:
		tier = constants.AdaptiveLargeFileConcurrency
	}

	// Validate against resource constraints
	m.mu.Lock()
	desiredThreadsPerFile := m.calculateDesiredThreads(median, tier)
	totalThreadsNeeded := tier * desiredThreadsPerFile
	if totalThreadsNeeded > m.totalThreads {
		// Scale down concurrency to fit within thread pool
		if desiredThreadsPerFile > 0 {
			tier = m.totalThreads / desiredThreadsPerFile
		}
	}
	memoryNeeded := int64(tier) * int64(desiredThreadsPerFile) * int64(constants.MemoryPerThreadMB) * 1024 * 1024
	availMem := m.memoryBudget
	if memoryNeeded > int64(float64(availMem)*0.75) && desiredThreadsPerFile > 0 {
		tier = int(float64(availMem) * 0.75 / float64(int64(desiredThreadsPerFile)*int64(constants.MemoryPerThreadMB)*1024*1024))
	}
	m.mu.Unlock()

	// Apply caps
	if tier > maxAllowed {
		tier = maxAllowed
	}
	if tier > len(fileSizes) {
		tier = len(fileSizes)
	}
	if tier < constants.MinMaxConcurrent {
		tier = constants.MinMaxConcurrent
	}
	return tier
}

// String returns a human-readable representation of the manager state
func (m *Manager) String() string {
	stats := m.GetStats()
	return fmt.Sprintf("ResourceManager[total=%d available=%d active=%d transfers=%d autoscale=%v]",
		stats.TotalThreads, stats.AvailableThreads, stats.ActiveThreads,
		stats.ActiveTransfers, stats.AutoScaleEnabled)
}

// =============================================================================
// Dynamic Chunk Sizing
// =============================================================================

// ChunkSizeFromFileSize returns the expected chunk size for a file of given plaintext size.
// This is a deterministic calculation based purely on file size - no memory or thread
// constraints are applied.
//
// This function is used during download to infer the chunk size for files
// that were uploaded without partsize metadata. Since we can't know the
// uploader's memory constraints, we use the file-size-based defaults which work
// for the vast majority of cases (machines with 1GB+ available memory).
//
// The returned values match the CalculateDynamicChunkSize defaults:
//   - <100 MB files: 16 MB chunks
//   - 100 MB - 1 GB files: 32 MB chunks
//   - 1 GB - 5 GB files: 48 MB chunks
//   - >=5 GB files: 64 MB chunks
func ChunkSizeFromFileSize(plaintextSize int64) int64 {
	switch {
	case plaintextSize < constants.SmallFileThreshold: // < 100 MB
		return constants.MinChunkSize // 16 MB
	case plaintextSize < constants.LargeFile1GB: // 100 MB - 1 GB
		return constants.ChunkSize // 32 MB
	case plaintextSize < constants.LargeFile5GB: // 1 GB - 5 GB
		return 48 * 1024 * 1024 // 48 MB
	default: // >= 5 GB
		return constants.MaxChunkSize // 64 MB
	}
}

// CalculateDynamicChunkSize returns the optimal chunk size for a transfer.
// Takes into account:
//   - File size (larger files benefit from larger chunks)
//   - Available memory (chunk * threads * 2 must fit in available RAM)
//   - Number of threads (more threads = need smaller chunks per thread)
//
// Returns a value between MinChunkSize (16 MB) and MaxChunkSize (64 MB).
//
// Usage:
//
//	chunkSize := resources.CalculateDynamicChunkSize(fileSize, threads)
//	// Use chunkSize for upload/download parts
func CalculateDynamicChunkSize(fileSize int64, numThreads int) int64 {
	return calculateDynamicChunkSize(fileSize, numThreads, getAvailableMemory())
}

// calculateDynamicChunkSize is CalculateDynamicChunkSize with the memory reading
// supplied by the caller, so a Manager can size chunks against the same budget it
// plans everything else against.
func calculateDynamicChunkSize(fileSize int64, numThreads int, availableMemory uint64) int64 {
	if numThreads < 1 {
		numThreads = 1
	}

	// Step 1: Determine base chunk size from file size
	var baseChunk int64

	switch {
	case fileSize < constants.SmallFileThreshold: // < 100 MB
		// Small files: use minimum chunk size
		baseChunk = constants.MinChunkSize // 16 MB

	case fileSize < constants.MediumFileThreshold: // 100 MB - 500 MB
		// Medium-small files: use base chunk size
		baseChunk = constants.ChunkSize // 32 MB

	case fileSize < constants.LargeFile1GB: // 500 MB - 1 GB
		// Medium files: use base chunk size
		baseChunk = constants.ChunkSize // 32 MB

	case fileSize < constants.LargeFile5GB: // 1 GB - 5 GB
		// Large files: use 48 MB for better throughput
		baseChunk = 48 * 1024 * 1024 // 48 MB

	default: // >= 5 GB
		// Very large files: use maximum chunk size
		baseChunk = constants.MaxChunkSize // 64 MB
	}

	// Step 2: Apply memory constraint
	// Rule: chunkSize * numThreads * 2 (double buffer) <= availableMemory * 0.75
	maxFromMemory := int64(float64(availableMemory) * 0.75 / float64(numThreads*2))

	// Apply memory cap
	if baseChunk > maxFromMemory {
		baseChunk = maxFromMemory
	}

	// Step 3: Enforce bounds
	if baseChunk < constants.MinChunkSize {
		baseChunk = constants.MinChunkSize
	}
	if baseChunk > constants.MaxChunkSize {
		baseChunk = constants.MaxChunkSize
	}

	// Step 4: Ensure chunk size is a multiple of 16 bytes (AES block size)
	// and ideally a power of 2 MB for efficient memory alignment
	// Round down to nearest MB
	baseChunk = (baseChunk / (1024 * 1024)) * (1024 * 1024)
	if baseChunk < constants.MinChunkSize {
		baseChunk = constants.MinChunkSize
	}

	return baseChunk
}

// =============================================================================
// Streaming Upload Planning
// =============================================================================

// UploadLimits are one storage backend's hard multipart ceilings.
type UploadLimits struct {
	StorageType string // Backend name, used when reporting a file it cannot store
	MaxParts    int64  // Highest part/block count the backend accepts
	MaxPartSize int64  // Largest plaintext part the planner may choose
}

// UploadPlanRequest describes one streaming upload to be planned.
type UploadPlanRequest struct {
	FileSize int64        // Plaintext size of the file
	Threads  int          // Upload workers the caller intends to start
	Limits   UploadLimits // Ceilings of the backend the file is going to
}

// UploadPlan is the geometry a streaming upload must run with.
type UploadPlan struct {
	// PartSize is the plaintext bytes per part. It is stamped into the object's
	// partsize metadata and CBC chains sequentially through it, so it is fixed
	// for the whole upload and cannot be renegotiated partway.
	PartSize int64

	// WorkerCap is the upper bound on live upload workers, including any the
	// dynamic scaler adds later.
	WorkerCap int

	// QueueDepth is the capacity of the encrypted-part channel between the
	// encryption stage and the upload workers.
	QueueDepth int
}

// inFlightBytes is the peak part-buffer memory this plan permits.
func (p UploadPlan) inFlightBytes() int64 {
	return (int64(p.QueueDepth) + int64(p.WorkerCap) + constants.UploadPipelineTransientParts) * p.PartSize
}

// PlanUpload sizes a streaming upload before any bytes move and reserves the
// memory it will occupy from this manager's budget, so concurrent uploads share
// one machine instead of each claiming all of it. The reservation is keyed by
// transferID; release it with ReleaseUploadPlan (ReleaseTransfer also does, as a
// backstop). Planning twice for the same transferID replaces the reservation.
//
// Part size is max(the memory-derived dynamic chunk size, the part-count floor),
// where the floor is ceil(fileSize/MaxParts) rounded up to a whole MiB so CBC
// still gets block-aligned parts. The floor wins over the memory clamp: a part
// size that satisfies the memory rule but not the part count produces an upload
// that fails partway through with everything already transferred, which is worse
// than one that runs with a narrower pipeline.
//
// Memory invariant: (QueueDepth + WorkerCap + transients) * PartSize <= budget.
// The transient count is UploadPipelineTransientParts (4), derived from the
// part-sized buffers the pipeline in internal/cloud/upload/upload.go holds
// outside the queue and the workers:
//
//  1. the read buffer, allocated once and live for the whole encrypt goroutine;
//  2. the plaintext copy taken from it for each part, because the buffer is reused;
//  3. the padded copy pkcs7Pad allocates for the final part — crypto appends to a
//     slice with no spare capacity, so it reallocates rather than padding in place;
//  4. the ciphertext EncryptPart returns, between the point it is allocated and
//     the point it lands in a queue slot.
//
// Four is deliberately more than the pipeline holds at any single instant: (3)
// exists only on the last part, and (4) is the same allocation a queue slot then
// counts again. Further headroom comes from the budget itself, which on Unix is
// already discounted to a fraction of system memory.
//
// When part size grows past what the budget affords, QueueDepth shrinks first
// (floor 1), then WorkerCap; part size never shrinks below the part-count floor.
// How far WorkerCap may fall matters more than it looks, because the cap is
// fixed here and the scaler only reads it — a transfer squeezed now stays
// squeezed even after the transfers that crowded it out have finished. So the
// worker floor is min(Threads, UploadMinThrottledWorkers) whenever the machine
// can hold that many parts, and 1 when it cannot. Both halves are load-bearing:
// dropping a batch's later files to a single worker for their whole transfer
// costs far more than the memory it saves, while insisting on four workers once
// the part-count floor has pushed parts to several hundred megabytes would
// shrink the largest file the backend can accept — a 4.2 TB upload has to be
// possible before it can be fast.
//
// A transfer that finds the budget already spoken for still gets that minimum:
// contending with other transfers narrows an upload, it does not fail one. The
// reservations can therefore total slightly more than the budget, bounded by
// (1 + workerFloor + transients) * PartSize per contended transfer. Planning
// fails only when the machine itself cannot hold one such minimum, which is the
// case that would otherwise end in an out-of-memory kill.
func (m *Manager) PlanUpload(transferID string, req UploadPlanRequest) (UploadPlan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// A transfer replanning itself gets to reuse what it already holds, so the
	// second plan sees the same budget as the first rather than a shrunken one.
	remaining := m.availableMemory + m.memoryReservations[transferID]
	if remaining < 0 {
		remaining = 0
	}

	// Part size is sized against the whole machine, not the unreserved remainder:
	// it is a durable property of the stored object, so it should not depend on
	// how many other uploads happen to be running. Only the pipeline width, which
	// is ephemeral, is fitted to what is left.
	plan, err := planUpload(req, m.memoryBudget, uint64(remaining), m.calculateDesiredThreads(req.FileSize, 1))
	if err != nil {
		return UploadPlan{}, err
	}

	m.releaseUploadPlanLocked(transferID)
	reserved := plan.inFlightBytes()
	m.availableMemory -= reserved
	m.memoryReservations[transferID] = reserved
	return plan, nil
}

// ReleaseUploadPlan returns a transfer's reserved upload memory to the shared
// budget. Safe to call repeatedly, and on a transfer that never planned.
func (m *Manager) ReleaseUploadPlan(transferID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseUploadPlanLocked(transferID)
}

// releaseUploadPlanLocked is called with the lock already held.
func (m *Manager) releaseUploadPlanLocked(transferID string) {
	if reserved, exists := m.memoryReservations[transferID]; exists {
		m.availableMemory += reserved
		delete(m.memoryReservations, transferID)
	}
}

// PlanUpload plans an upload against the whole machine without reserving
// anything. Callers holding a Manager must use (*Manager).PlanUpload instead so
// concurrent uploads share one budget; this exists for the upload paths that
// have no transfer handle, and therefore no pool to share.
func PlanUpload(req UploadPlanRequest) (UploadPlan, error) {
	budget := getAvailableMemory()
	return planUpload(req, budget, budget, constants.MaxThreadsPerFile)
}

// minimumBytes is the peak part-buffer memory of the narrowest pipeline that
// still runs: one queued part, workerFloor workers, and the encrypt transients.
func minimumBytes(workerFloor int, partSize int64) int64 {
	return int64(1+workerFloor+constants.UploadPipelineTransientParts) * partSize
}

// partCountFloor is the smallest part size that keeps fileSize within maxParts
// parts, rounded up to a whole MiB. Returns 0 when there is nothing to bound.
func partCountFloor(fileSize, maxParts int64) int64 {
	if fileSize <= 0 || maxParts <= 0 {
		return 0
	}
	exact := (fileSize + maxParts - 1) / maxParts
	return ((exact + constants.PartSizeAlignment - 1) / constants.PartSizeAlignment) * constants.PartSizeAlignment
}

// planUpload builds the plan. sizingBudget is the memory the part-size math sees;
// pipelineBudget is the memory the queue and workers must fit into. They differ
// only for a shared Manager, where part size is sized against the whole machine
// and the pipeline against what other transfers have left.
func planUpload(req UploadPlanRequest, sizingBudget, pipelineBudget uint64, workerCeiling int) (UploadPlan, error) {
	if req.Limits.MaxParts <= 0 || req.Limits.MaxPartSize <= 0 {
		return UploadPlan{}, fmt.Errorf("upload limits for %s are unset (max parts %d, max part size %d)",
			req.Limits.StorageType, req.Limits.MaxParts, req.Limits.MaxPartSize)
	}

	threads := req.Threads
	if threads < 1 {
		threads = 1
	}

	const mib = constants.PartSizeAlignment

	floor := partCountFloor(req.FileSize, req.Limits.MaxParts)
	if floor > req.Limits.MaxPartSize {
		return UploadPlan{}, fmt.Errorf(
			"file is too large for %s: staying within %d parts would need %d MB parts, above the %d MB per-part limit — the largest file %s can accept is about %d GB",
			req.Limits.StorageType, req.Limits.MaxParts, floor/mib,
			req.Limits.MaxPartSize/mib, req.Limits.StorageType,
			(req.Limits.MaxParts*req.Limits.MaxPartSize)/(1024*1024*1024))
	}

	// Part size is sized for the widest pipeline the transfer could reach rather
	// than the thread count this particular run was handed. It is stamped into the
	// object's metadata and read back at download time, so it should not vary with
	// an ephemeral allocation. This is also what the providers passed before there
	// was a plan.
	partSize := calculateDynamicChunkSize(req.FileSize, constants.MaxThreadsPerFile, sizingBudget)
	if floor > partSize {
		partSize = floor
	}

	// The scaler can grow past the caller's thread count, so the cap has to cover
	// the largest worker count this transfer could ever reach, not just its start.
	workerCap := threads
	if workerCeiling > workerCap {
		workerCap = workerCeiling
	}
	if workerCap > constants.MaxThreadsPerFile {
		workerCap = constants.MaxThreadsPerFile
	}
	if workerCap < 1 {
		workerCap = 1
	}
	queueDepth := threads * constants.UploadQueueDepthPerWorker

	// How far the workers may be squeezed. The cap is fixed here and the scaler
	// only ever reads it, so whatever a contended transfer is given is what it
	// runs with for its whole life — there is no recovery when the transfers that
	// crowded it out finish. One worker is therefore the wrong answer wherever a
	// wider floor is affordable, and the right one where it is not: with parts of
	// several hundred megabytes, insisting on four workers would not make the
	// upload slow, it would make it impossible.
	//
	// So the floor is the widest of the two the machine can actually hold. This
	// is deliberately not a test on part size: the part-count floor can raise a
	// part to 64 MB on a machine whose memory clamp wanted 16 MB, and reading
	// that as "normal sized" would refuse an upload that one worker could carry.
	workerFloor := threads
	if workerFloor > constants.UploadMinThrottledWorkers {
		workerFloor = constants.UploadMinThrottledWorkers
	}
	if minimumBytes(workerFloor, partSize) > int64(sizingBudget) {
		workerFloor = 1
	}

	// A machine that cannot hold one queued part and the floor's worth of workers
	// cannot run this upload at all. That is judged against the whole machine, not
	// against what other transfers currently hold — losing a race for the budget
	// should make an upload narrow, never make it fail.
	//
	// With the floor at 1 this can only trigger once the part-count floor has
	// raised the part size: the memory clamp on its own yields at most
	// budget/42 per part, and where MinChunkSize overrides it the whole working
	// set is 6 x 16 MB, inside the 512 MB that getAvailableMemory() reports at
	// its smallest.
	minimum := minimumBytes(workerFloor, partSize)
	if int64(sizingBudget) < minimum {
		return UploadPlan{}, fmt.Errorf(
			"not enough memory to upload this file to %s: parts must be %d MB to stay within %d parts, so the smallest working pipeline needs %d MB, above the %d MB transfer memory budget",
			req.Limits.StorageType, partSize/mib, req.Limits.MaxParts,
			minimum/mib, int64(sizingBudget)/mib)
	}

	// Parts the remaining budget can hold in the queue and the workers together,
	// after the encrypt stage has taken its transients off the top. Once other
	// transfers have spoken for everything, this upload still gets the minimum,
	// which is why the reservations can add up to slightly more than the budget:
	// each contended transfer is bounded by minimumParts, not by what is left.
	affordable := int64(pipelineBudget)/partSize - constants.UploadPipelineTransientParts
	if minimumAffordable := int64(1 + workerFloor); affordable < minimumAffordable {
		affordable = minimumAffordable
	}

	if int64(queueDepth+workerCap) > affordable {
		if affordable-int64(workerCap) >= 1 {
			queueDepth = int(affordable) - workerCap
		} else {
			queueDepth = 1
			workerCap = int(affordable) - 1
		}
	}

	return UploadPlan{
		PartSize:   partSize,
		WorkerCap:  workerCap,
		QueueDepth: queueDepth,
	}, nil
}
