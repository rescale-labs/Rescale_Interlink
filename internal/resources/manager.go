package resources

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/constants"
)

// Manager manages a shared pool of threads/goroutines for file transfers
// It allocates threads between concurrent files and concurrent parts within files
type Manager struct {
	totalThreads     int            // Total threads in the pool
	availableThreads int            // Currently available (not allocated)
	baselineThreads  int            // Baseline calculated from CPU cores
	memoryLimit      int            // Max threads based on memory
	autoScale        bool           // Whether auto-scaling is enabled
	allocations      map[string]int // Track allocations per transfer ID
	mu               sync.Mutex     // Protects all fields
	monitor          *ThroughputMonitor
}

// Config holds configuration for the resource manager
type Config struct {
	MaxThreads int  // User-specified max threads (0 = auto-detect)
	AutoScale  bool // Enable auto-scaling
}

// NewManager creates a new resource manager
func NewManager(config Config) *Manager {
	// Calculate baseline from CPU cores
	cores := runtime.NumCPU()
	baselineThreads := cores * 2
	if baselineThreads > constants.MaxBaselineThreads {
		baselineThreads = constants.MaxBaselineThreads
	}

	// Calculate memory constraint
	availableMemory := getAvailableMemory()
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

	return &Manager{
		totalThreads:     totalThreads,
		availableThreads: totalThreads,
		baselineThreads:  baselineThreads,
		memoryLimit:      memoryThreads,
		autoScale:        config.AutoScale,
		allocations:      make(map[string]int),
		monitor:          NewThroughputMonitor(),
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

// ReleaseTransfer releases threads allocated to a transfer
func (m *Manager) ReleaseTransfer(transferID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if allocated, exists := m.allocations[transferID]; exists {
		m.availableThreads += allocated
		delete(m.allocations, transferID)
	}
}

// GetAvailableThreads returns the current number of available threads
func (m *Manager) GetAvailableThreads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.availableThreads
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

	// Cap at pool share
	if desired > poolShare {
		desired = poolShare
	}

	// Never exceed max threads per file
	if desired > constants.MaxThreadsPerFile {
		desired = constants.MaxThreadsPerFile
	}

	return desired
}

// RecordThroughput records throughput for a part/chunk
func (m *Manager) RecordThroughput(transferID string, bytesPerSecond float64) {
	m.monitor.Record(transferID, bytesPerSecond)
}

// ShouldScaleUp determines if we should increase parallelism based on throughput
func (m *Manager) ShouldScaleUp(transferID string) bool {
	if !m.autoScale {
		return false
	}
	return m.monitor.ShouldScaleUp(transferID)
}

// ShouldScaleDown determines if we should decrease parallelism
func (m *Manager) ShouldScaleDown(transferID string) bool {
	if !m.autoScale {
		return false
	}
	return m.monitor.ShouldScaleDown(transferID)
}

// String returns a human-readable representation of the manager state
func (m *Manager) String() string {
	stats := m.GetStats()
	return fmt.Sprintf("ResourceManager[total=%d available=%d active=%d transfers=%d autoscale=%v]",
		stats.TotalThreads, stats.AvailableThreads, stats.ActiveThreads,
		stats.ActiveTransfers, stats.AutoScaleEnabled)
}

// ThroughputMonitor tracks throughput for each transfer to detect saturation
type ThroughputMonitor struct {
	mu      sync.Mutex
	samples map[string][]Sample // Per-transfer samples
}

// Sample represents a single throughput measurement
type Sample struct {
	Timestamp   time.Time
	BytesPerSec float64
}

// NewThroughputMonitor creates a new throughput monitor
func NewThroughputMonitor() *ThroughputMonitor {
	return &ThroughputMonitor{
		samples: make(map[string][]Sample),
	}
}

// Record records a throughput sample
func (tm *ThroughputMonitor) Record(transferID string, bytesPerSecond float64) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	samples := tm.samples[transferID]
	samples = append(samples, Sample{
		Timestamp:   time.Now(),
		BytesPerSec: bytesPerSecond,
	})

	// Keep only last N samples
	if len(samples) > constants.MaxThroughputSamples {
		samples = samples[len(samples)-constants.MaxThroughputSamples:]
	}

	tm.samples[transferID] = samples
}

// ShouldScaleUp returns true if throughput is high and stable
func (tm *ThroughputMonitor) ShouldScaleUp(transferID string) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	samples := tm.samples[transferID]
	if len(samples) < 3 {
		return false // Not enough data
	}

	// Check if throughput is high and stable
	avg := tm.calculateAverage(samples)
	variance := tm.calculateVariance(samples, avg)

	avgMBps := avg / (1024 * 1024)
	varianceMBps := variance / (1024 * 1024)

	return avgMBps > constants.MinScaleUpThroughputMBps && varianceMBps < constants.MaxScaleUpVarianceMBps
}

// ShouldScaleDown returns true if throughput is dropping
func (tm *ThroughputMonitor) ShouldScaleDown(transferID string) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	samples := tm.samples[transferID]
	if len(samples) < 5 {
		return false // Not enough data
	}

	// Check if throughput is consistently decreasing
	recent := samples[len(samples)-3:]
	older := samples[len(samples)-6 : len(samples)-3]

	recentAvg := tm.calculateAverage(recent)
	olderAvg := tm.calculateAverage(older)

	// If recent average is significantly lower, scale down
	if recentAvg < olderAvg*constants.ScaleDownThresholdPercent {
		return true
	}

	return false
}

// calculateAverage calculates the average throughput
func (tm *ThroughputMonitor) calculateAverage(samples []Sample) float64 {
	if len(samples) == 0 {
		return 0
	}

	var sum float64
	for _, s := range samples {
		sum += s.BytesPerSec
	}
	return sum / float64(len(samples))
}

// calculateVariance calculates the variance in throughput
func (tm *ThroughputMonitor) calculateVariance(samples []Sample, avg float64) float64 {
	if len(samples) == 0 {
		return 0
	}

	var sumSquares float64
	for _, s := range samples {
		diff := s.BytesPerSec - avg
		sumSquares += diff * diff
	}
	return sumSquares / float64(len(samples))
}

// Cleanup removes samples for a completed transfer
func (tm *ThroughputMonitor) Cleanup(transferID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.samples, transferID)
}
