package transfer

import (
	"testing"

	"github.com/rescale/rescale-int/internal/resources"
)

func TestNewManager(t *testing.T) {
	resourceMgr := resources.NewManager(resources.Config{MaxThreads: 10, AutoScale: true})
	transferMgr := NewManager(resourceMgr)

	if transferMgr == nil {
		t.Fatal("NewManager returned nil")
	}
}

func TestAllocateTransfer(t *testing.T) {
	resourceMgr := resources.NewManager(resources.Config{MaxThreads: 10, AutoScale: true})
	transferMgr := NewManager(resourceMgr)

	transfer := transferMgr.AllocateTransfer(1024*1024*1024, 1) // 1GB file
	if transfer == nil {
		t.Fatal("AllocateTransfer returned nil")
	}

	threads := transfer.GetThreads()
	if threads < 1 {
		t.Errorf("Expected at least 1 thread, got %d", threads)
	}

	if transfer.GetID() == "" {
		t.Error("Transfer ID should not be empty")
	}

	// Complete the transfer
	transfer.Complete()
}

func TestTransferComplete(t *testing.T) {
	resourceMgr := resources.NewManager(resources.Config{MaxThreads: 10, AutoScale: true})
	transferMgr := NewManager(resourceMgr)

	initialStats := transferMgr.GetStats()

	transfer := transferMgr.AllocateTransfer(500*1024*1024, 1)
	threads := transfer.GetThreads()

	activeStats := transferMgr.GetStats()
	if activeStats.ActiveTransfers != initialStats.ActiveTransfers+1 {
		t.Error("Active transfers should increase after allocation")
	}

	// Complete the transfer
	transfer.Complete()

	completedStats := transferMgr.GetStats()
	if completedStats.AvailableThreads != initialStats.AvailableThreads {
		t.Errorf("Expected %d available threads after completion, got %d",
			initialStats.AvailableThreads, completedStats.AvailableThreads)
	}

	// Multiple Complete() calls should be safe
	transfer.Complete()
	transfer.Complete()

	// Verify threads allocation
	if threads < 1 {
		t.Errorf("Expected at least 1 thread allocated")
	}
}

func TestMultipleTransfers(t *testing.T) {
	resourceMgr := resources.NewManager(resources.Config{MaxThreads: 20, AutoScale: true})
	transferMgr := NewManager(resourceMgr)

	// Allocate multiple transfers
	transfers := make([]*Transfer, 5)
	for i := 0; i < 5; i++ {
		transfers[i] = transferMgr.AllocateTransfer(1024*1024*1024, 5)
		if transfers[i] == nil {
			t.Fatalf("Failed to allocate transfer %d", i)
		}
	}

	stats := transferMgr.GetStats()
	if stats.ActiveTransfers != 5 {
		t.Errorf("Expected 5 active transfers, got %d", stats.ActiveTransfers)
	}

	// Complete all transfers
	for _, transfer := range transfers {
		transfer.Complete()
	}

	finalStats := transferMgr.GetStats()
	if finalStats.ActiveTransfers != 0 {
		t.Errorf("Expected 0 active transfers after completion, got %d", finalStats.ActiveTransfers)
	}
	if finalStats.AvailableThreads != finalStats.TotalThreads {
		t.Error("All threads should be available after all transfers complete")
	}
}

func TestRecordThroughput(t *testing.T) {
	resourceMgr := resources.NewManager(resources.Config{MaxThreads: 10, AutoScale: true})
	transferMgr := NewManager(resourceMgr)

	transfer := transferMgr.AllocateTransfer(1024*1024*1024, 1)
	defer transfer.Complete()

	// Should not panic
	transfer.RecordThroughput(10 * 1024 * 1024) // 10 MB/s
	transfer.RecordThroughput(12 * 1024 * 1024) // 12 MB/s
	transfer.RecordThroughput(11 * 1024 * 1024) // 11 MB/s

	// Just verify it doesn't crash - throughput monitoring is tested in resources package
}

func TestGetStats(t *testing.T) {
	resourceMgr := resources.NewManager(resources.Config{MaxThreads: 15, AutoScale: true})
	transferMgr := NewManager(resourceMgr)

	stats := transferMgr.GetStats()
	if stats.TotalThreads != 15 {
		t.Errorf("Expected total threads 15, got %d", stats.TotalThreads)
	}
	if stats.ActiveTransfers != 0 {
		t.Errorf("Expected 0 active transfers, got %d", stats.ActiveTransfers)
	}

	// Allocate a transfer
	transfer := transferMgr.AllocateTransfer(2*1024*1024*1024, 1)
	defer transfer.Complete()

	stats = transferMgr.GetStats()
	if stats.ActiveTransfers != 1 {
		t.Errorf("Expected 1 active transfer, got %d", stats.ActiveTransfers)
	}
	if stats.ActiveThreads == 0 {
		t.Error("Expected some active threads")
	}
}

func TestTransferString(t *testing.T) {
	resourceMgr := resources.NewManager(resources.Config{MaxThreads: 10, AutoScale: true})
	transferMgr := NewManager(resourceMgr)

	transfer := transferMgr.AllocateTransfer(1024*1024*1024, 1)
	defer transfer.Complete()

	str := transfer.String()
	if str == "" {
		t.Error("Transfer.String() should not be empty")
	}

	// Should contain transfer ID
	if transfer.GetID() == "" {
		t.Error("Transfer ID should not be empty")
	}
}

func TestSmallFileAllocation(t *testing.T) {
	resourceMgr := resources.NewManager(resources.Config{MaxThreads: 16, AutoScale: true})
	transferMgr := NewManager(resourceMgr)

	// Small file should get 1 thread
	transfer := transferMgr.AllocateTransfer(50*1024*1024, 1) // 50MB
	defer transfer.Complete()

	if transfer.GetThreads() != 1 {
		t.Errorf("Small file should get 1 thread, got %d", transfer.GetThreads())
	}
}

func TestLargeFileAllocation(t *testing.T) {
	resourceMgr := resources.NewManager(resources.Config{MaxThreads: 16, AutoScale: true})
	transferMgr := NewManager(resourceMgr)

	// Large file should get multiple threads
	transfer := transferMgr.AllocateTransfer(10*1024*1024*1024, 1) // 10GB
	defer transfer.Complete()

	if transfer.GetThreads() < 5 {
		t.Errorf("Large file should get at least 5 threads, got %d", transfer.GetThreads())
	}
}

func TestGenerateTransferID(t *testing.T) {
	// Test that IDs are unique
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateTransferID()
		if ids[id] {
			t.Errorf("Duplicate transfer ID generated: %s", id)
		}
		ids[id] = true
	}

	if len(ids) != 100 {
		t.Errorf("Expected 100 unique IDs, got %d", len(ids))
	}
}
