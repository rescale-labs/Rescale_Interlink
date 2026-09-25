package transfer

import (
	"testing"

	"github.com/rescale/rescale-int/internal/resources"
)

// The manager only wraps resources.Manager, whose tests pin the thread
// policy. These pin the wrapper: it hands back the pool's allocation (one
// thread for a small file, several for a large one), releases it on Complete,
// which is idempotent, and gives every transfer its own ID.
func TestManagerWrapsResourceManager(t *testing.T) {
	// A pinned 16-core machine, so the per-file cap is not the host's core count.
	transferMgr := NewManager(resources.NewManager(resources.Config{MaxThreads: 16, AutoScale: true, CPUCores: 16, MemoryBudget: 8 << 30}))
	initial := transferMgr.GetStats()
	if initial.TotalThreads != 16 || initial.ActiveTransfers != 0 {
		t.Fatalf("fresh manager stats = %+v, want 16 threads and no transfers", initial)
	}

	small := transferMgr.AllocateTransfer(50*1024*1024, 1)
	if small.GetThreads() != 1 {
		t.Errorf("a 50 MB file got %d threads, want 1", small.GetThreads())
	}
	small.Complete()

	large := transferMgr.AllocateTransfer(10*1024*1024*1024, 1)
	if large.GetThreads() < 5 {
		t.Errorf("a 10 GB file got %d threads, want at least 5", large.GetThreads())
	}
	other := transferMgr.AllocateTransfer(2*1024*1024*1024, 1)
	if large.GetID() == "" || large.GetID() == other.GetID() || large.String() == "" {
		t.Errorf("IDs %q and %q, String %q: want two distinct IDs and a description", large.GetID(), other.GetID(), large.String())
	}
	if stats := transferMgr.GetStats(); stats.ActiveTransfers != 2 || stats.ActiveThreads == 0 {
		t.Errorf("stats with two transfers = %+v", stats)
	}

	for i := 0; i < 3; i++ {
		large.Complete()
	}
	other.Complete()
	if stats := transferMgr.GetStats(); stats.ActiveTransfers != 0 || stats.AvailableThreads != initial.AvailableThreads {
		t.Errorf("stats after completion = %+v, want everything returned (%+v)", stats, initial)
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
