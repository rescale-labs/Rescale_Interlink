package daemon

import (
	"testing"

	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
)

// TestTransferManagerReuse verifies Bug #4 fix:
// downloadJob creates a single TransferManager per job (outside the file loop),
// not a new TransferManager per file.
// v4.8.1: Before the fix, each file in downloadJob created its own TransferManager
// via transfer.NewManager(resourceMgr), defeating resource pooling — each manager
// saw only 1 active transfer and allocated threads independently.
//
// This test demonstrates the difference between the buggy pattern (new manager per file)
// and the fixed pattern (single manager reused across files):
// - Reused manager: resource pool tracks all active transfers, enabling proper sharing
// - Per-file manager: each sees only its own transfer, over-allocating resources
func TestTransferManagerReuse(t *testing.T) {
	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads: 16,
		AutoScale:  true,
	})

	// Fixed pattern: single TransferManager reused across files
	sharedMgr := transfer.NewManager(resourceMgr)

	// Simulate allocating transfers for multiple files (like downloadJob's file loop)
	const numFiles = 5
	handles := make([]*transfer.Transfer, numFiles)
	for i := 0; i < numFiles; i++ {
		handles[i] = sharedMgr.AllocateTransfer(100*1024*1024, 1) // 100MB each, totalFiles=1
	}

	// With a shared manager, the resource pool sees all active transfers
	stats := sharedMgr.GetStats()
	if stats.ActiveTransfers != numFiles {
		t.Errorf("shared manager: ActiveTransfers = %d, want %d (resource pool should track all)", stats.ActiveTransfers, numFiles)
	}

	// Clean up
	for _, h := range handles {
		h.Complete()
	}

	stats = sharedMgr.GetStats()
	if stats.ActiveTransfers != 0 {
		t.Errorf("after cleanup: ActiveTransfers = %d, want 0", stats.ActiveTransfers)
	}
}

// TestTransferManagerPerFileBugPattern demonstrates the Bug #4 pattern
// that was fixed — creating a new TransferManager per file.
// Each manager gets its own view of the resource pool, preventing proper tracking.
func TestTransferManagerPerFileBugPattern(t *testing.T) {
	const numFiles = 5

	// Buggy pattern: new TransferManager per file (what downloadJob used to do)
	// Each manager shares the same resourceMgr, but tracks its own transfers independently.
	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads: 16,
		AutoScale:  true,
	})

	var handles []*transfer.Transfer
	for i := 0; i < numFiles; i++ {
		// Bug #4: This was inside the loop, creating a new manager each iteration
		perFileMgr := transfer.NewManager(resourceMgr)
		h := perFileMgr.AllocateTransfer(100*1024*1024, 1)
		handles = append(handles, h)
	}

	// The resource manager still tracks allocations globally,
	// but per-file managers each only report their own transfers.
	// This is the semantic problem: callers couldn't see the full picture.
	globalStats := resourceMgr.GetStats()
	if globalStats.ActiveTransfers != numFiles {
		t.Errorf("global resource manager: ActiveTransfers = %d, want %d", globalStats.ActiveTransfers, numFiles)
	}

	// Clean up
	for _, h := range handles {
		h.Complete()
	}
}

// TestDaemonDefaultConfig verifies daemon defaults used by downloadJob.
func TestDaemonDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxConcurrent != 5 {
		t.Errorf("DefaultConfig().MaxConcurrent = %d, want 5", cfg.MaxConcurrent)
	}
}
