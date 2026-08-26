package resources

import (
	"runtime"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/constants"
)

func TestNewManager(t *testing.T) {
	tests := []struct {
		name          string
		config        Config
		expectMinimum int
		expectMaximum int
	}{
		{
			name:          "Auto-detect with auto-scale",
			config:        Config{MaxThreads: 0, AutoScale: true},
			expectMinimum: 1,
			expectMaximum: 32,
		},
		{
			name:          "User-specified threads",
			config:        Config{MaxThreads: 8, AutoScale: true},
			expectMinimum: 8,
			expectMaximum: 8,
		},
		{
			name:          "Single thread",
			config:        Config{MaxThreads: 1, AutoScale: false},
			expectMinimum: 1,
			expectMaximum: 1,
		},
		{
			name:          "Cap at maximum",
			config:        Config{MaxThreads: 100, AutoScale: true},
			expectMinimum: 32,
			expectMaximum: 32,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewManager(tt.config)
			if mgr == nil {
				t.Fatal("NewManager returned nil")
			}

			totalThreads := mgr.GetTotalThreads()
			if totalThreads < tt.expectMinimum || totalThreads > tt.expectMaximum {
				t.Errorf("Expected threads between %d and %d, got %d",
					tt.expectMinimum, tt.expectMaximum, totalThreads)
			}

			// Available should equal total initially
			if mgr.GetAvailableThreads() != totalThreads {
				t.Errorf("Expected available threads to equal total threads initially")
			}
		})
	}
}

func TestAllocateAndRelease(t *testing.T) {
	mgr := NewManager(Config{MaxThreads: 10, AutoScale: false})

	// Test allocation
	allocated := mgr.AllocateForTransfer("test-1", 1024*1024*1024, 1) // 1GB file
	if allocated < 1 {
		t.Errorf("Expected at least 1 thread allocated, got %d", allocated)
	}
	if allocated > 10 {
		t.Errorf("Expected at most 10 threads allocated, got %d", allocated)
	}

	initialAvailable := mgr.GetAvailableThreads()
	if initialAvailable != 10-allocated {
		t.Errorf("Expected %d available threads, got %d", 10-allocated, initialAvailable)
	}

	// Test release
	mgr.ReleaseTransfer("test-1")
	finalAvailable := mgr.GetAvailableThreads()
	if finalAvailable != 10 {
		t.Errorf("Expected all threads released (10), got %d", finalAvailable)
	}
}

func TestMultipleAllocations(t *testing.T) {
	mgr := NewManager(Config{MaxThreads: 15, AutoScale: false})

	// Allocate for multiple files
	allocated1 := mgr.AllocateForTransfer("file-1", 500*1024*1024, 3)    // 500MB
	allocated2 := mgr.AllocateForTransfer("file-2", 2*1024*1024*1024, 3) // 2GB
	allocated3 := mgr.AllocateForTransfer("file-3", 100*1024*1024, 3)    // 100MB

	total := allocated1 + allocated2 + allocated3
	if total > 15 {
		t.Errorf("Total allocated (%d) exceeds pool size (15)", total)
	}

	available := mgr.GetAvailableThreads()
	if available != 15-total {
		t.Errorf("Expected %d available, got %d", 15-total, available)
	}

	// Release one transfer
	mgr.ReleaseTransfer("file-2")
	newAvailable := mgr.GetAvailableThreads()
	if newAvailable != available+allocated2 {
		t.Errorf("Expected %d available after release, got %d",
			available+allocated2, newAvailable)
	}
}

func TestFileSizeAllocation(t *testing.T) {
	// Per-file allocation is capped at the core count, so the expectations below
	// only hold on a machine with enough cores. Pin a 16-core machine to keep the
	// tiers (and not the host) the thing under test.
	allocatorConfig := Config{MaxThreads: 16, AutoScale: true, CPUCores: 16}
	mgr := NewManager(allocatorConfig)

	tests := []struct {
		name       string
		fileSize   int64
		totalFiles int
		expectMin  int
		expectMax  int
	}{
		{
			name:       "Small file (<100MB)",
			fileSize:   50 * 1024 * 1024,
			totalFiles: 1,
			expectMin:  1,
			expectMax:  1,
		},
		{
			name:       "Medium file (500MB)",
			fileSize:   500 * 1024 * 1024,
			totalFiles: 1,
			expectMin:  1,
			expectMax:  5,
		},
		{
			name:       "Large file (5GB)",
			fileSize:   5 * 1024 * 1024 * 1024,
			totalFiles: 1,
			expectMin:  8,
			expectMax:  16,
		},
		{
			name:       "Multiple files share pool",
			fileSize:   1 * 1024 * 1024 * 1024,
			totalFiles: 5,
			expectMin:  1,
			expectMax:  5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset manager for each test
			mgr = NewManager(allocatorConfig)

			allocated := mgr.AllocateForTransfer("test", tt.fileSize, tt.totalFiles)
			if allocated < tt.expectMin || allocated > tt.expectMax {
				t.Errorf("Expected threads between %d and %d, got %d",
					tt.expectMin, tt.expectMax, allocated)
			}
			mgr.ReleaseTransfer("test")
		})
	}
}

func TestGetStats(t *testing.T) {
	mgr := NewManager(Config{MaxThreads: 12, AutoScale: true})

	stats := mgr.GetStats()
	if stats.TotalThreads != 12 {
		t.Errorf("Expected total threads 12, got %d", stats.TotalThreads)
	}
	if stats.AvailableThreads != 12 {
		t.Errorf("Expected available threads 12, got %d", stats.AvailableThreads)
	}
	if stats.ActiveThreads != 0 {
		t.Errorf("Expected active threads 0, got %d", stats.ActiveThreads)
	}
	if stats.ActiveTransfers != 0 {
		t.Errorf("Expected active transfers 0, got %d", stats.ActiveTransfers)
	}

	// Allocate some transfers
	mgr.AllocateForTransfer("t1", 1024*1024*1024, 1)
	mgr.AllocateForTransfer("t2", 500*1024*1024, 1)

	stats = mgr.GetStats()
	if stats.ActiveTransfers != 2 {
		t.Errorf("Expected 2 active transfers, got %d", stats.ActiveTransfers)
	}
	if stats.ActiveThreads == 0 {
		t.Errorf("Expected some active threads, got 0")
	}
	if stats.ActiveThreads+stats.AvailableThreads != stats.TotalThreads {
		t.Errorf("Active + Available should equal Total")
	}
}

func TestConcurrentAccess(t *testing.T) {
	mgr := NewManager(Config{MaxThreads: 20, AutoScale: true})

	// Simulate concurrent allocations and releases
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- true }()

			transferID := string(rune('A' + id))

			// Allocate
			threads := mgr.AllocateForTransfer(transferID, 1024*1024*1024, 10)
			if threads < 1 {
				t.Errorf("Worker %d got 0 threads", id)
				return
			}

			// Simulate some work
			time.Sleep(10 * time.Millisecond)

			// Release
			mgr.ReleaseTransfer(transferID)
		}(i)
	}

	// Wait for all workers
	for i := 0; i < 10; i++ {
		<-done
	}

	// All threads should be available again
	if mgr.GetAvailableThreads() != mgr.GetTotalThreads() {
		t.Errorf("Expected all threads available after concurrent test")
	}
}

func TestAutoScaleDisabled(t *testing.T) {
	mgr := NewManager(Config{MaxThreads: 10, AutoScale: false})

	// With auto-scale disabled, allocation should be conservative
	allocated := mgr.AllocateForTransfer("test", 5*1024*1024*1024, 1) // 5GB

	// Should get fewer threads than with auto-scale enabled
	if allocated > 3 {
		t.Errorf("Expected conservative allocation (<=3) with auto-scale disabled, got %d", allocated)
	}
}

func TestMemoryDetection(t *testing.T) {
	mem := getAvailableMemory()

	// Should return at least the minimum
	if mem < 512*1024*1024 {
		t.Errorf("getAvailableMemory returned too little: %d bytes", mem)
	}

	// Should not return unreasonably large values
	if mem > 128*1024*1024*1024 {
		t.Errorf("getAvailableMemory returned suspiciously large value: %d bytes", mem)
	}

	t.Logf("Detected available memory: %d MB", mem/(1024*1024))
	t.Logf("CPU cores: %d", runtime.NumCPU())
}

func TestComputeBatchConcurrency(t *testing.T) {
	small := func(n int, size int64) []int64 {
		sizes := make([]int64, n)
		for i := range sizes {
			sizes[i] = size
		}
		return sizes
	}
	const (
		mb = 1024 * 1024
		gb = 1024 * mb
	)

	tests := []struct {
		name       string
		maxThreads int
		sizes      []int64
		maxAllowed int
		wantMin    int // 0 means unconstrained
		wantMax    int // 0 means unconstrained
		wantExact  int // 0 means unconstrained
	}{
		{
			// The small-file tier is AdaptiveSmallFileConcurrency, but the memory
			// check scales it down on machines with less than ~3.4GB available,
			// so this asserts the tier band rather than a single value.
			name: "100 small files sits in the small-file band", maxThreads: 32,
			sizes: small(100, 1*mb), maxAllowed: constants.MaxMaxConcurrent,
			wantMin: constants.AdaptiveMediumFileConcurrency, wantMax: constants.AdaptiveSmallFileConcurrency,
		},
		{
			name:  "large files stay at or below the large-file tier",
			sizes: []int64{2 * gb, 3 * gb, 5 * gb}, maxAllowed: constants.MaxMaxConcurrent,
			wantMax: constants.AdaptiveLargeFileConcurrency,
		},
		{
			name:  "medium files stay at or below the medium-file tier",
			sizes: []int64{200 * mb, 500 * mb, 800 * mb}, maxAllowed: constants.MaxMaxConcurrent,
			wantMin: 1, wantMax: constants.AdaptiveMediumFileConcurrency,
		},
		{
			// The median size (50MB) picks the tier; the file count then caps it.
			name:  "mixed sizes are capped at the file count",
			sizes: []int64{1024, 50 * mb, 2 * gb}, maxAllowed: constants.MaxMaxConcurrent,
			wantMin: 1, wantMax: 3,
		},
		{
			name:  "empty batch falls back to the default",
			sizes: nil, maxAllowed: constants.MaxMaxConcurrent,
			wantExact: constants.DefaultMaxConcurrent,
		},
		{
			name:  "single file is capped at one",
			sizes: []int64{50 * mb}, maxAllowed: constants.MaxMaxConcurrent,
			wantExact: 1,
		},
		{
			name:  "maxAllowed is respected",
			sizes: small(100, 1024), maxAllowed: 8,
			wantMax: 8,
		},
		{
			name:  "three small files are capped at the file count",
			sizes: []int64{1024, 2048, 4096}, maxAllowed: constants.MaxMaxConcurrent,
			wantMax: 3,
		},
		{
			name:  "one huge file still gets the guaranteed minimum",
			sizes: []int64{10 * gb}, maxAllowed: constants.MaxMaxConcurrent,
			wantMin: constants.MinMaxConcurrent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewManager(Config{MaxThreads: tt.maxThreads, AutoScale: true})
			got := mgr.ComputeBatchConcurrency(tt.sizes, tt.maxAllowed)

			if tt.wantExact != 0 && got != tt.wantExact {
				t.Errorf("got %d, want %d", got, tt.wantExact)
			}
			if tt.wantMin != 0 && got < tt.wantMin {
				t.Errorf("got %d, want >= %d", got, tt.wantMin)
			}
			if tt.wantMax != 0 && got > tt.wantMax {
				t.Errorf("got %d, want <= %d", got, tt.wantMax)
			}
		})
	}
}
