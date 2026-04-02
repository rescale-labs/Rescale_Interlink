package transfer

import (
	"fmt"
	"sync"

	"github.com/rescale/rescale-int/internal/resources"
)

// Manager allocates per-file thread pools from a shared budget, enabling adaptive concurrency.
type Manager struct {
	resourceMgr *resources.Manager
}

func NewManager(resourceMgr *resources.Manager) *Manager {
	return &Manager{
		resourceMgr: resourceMgr,
	}
}

func (m *Manager) AllocateTransfer(fileSize int64, totalFiles int) *Transfer {
	// Generate unique transfer ID
	transferID := generateTransferID()

	// Allocate threads from resource manager
	threads := m.resourceMgr.AllocateForTransfer(transferID, fileSize, totalFiles)

	return &Transfer{
		id:          transferID,
		fileSize:    fileSize,
		threads:     threads,
		resourceMgr: m.resourceMgr,
	}
}

func (m *Manager) GetStats() ManagerStats {
	resourceStats := m.resourceMgr.GetStats()
	return ManagerStats{
		TotalThreads:     resourceStats.TotalThreads,
		ActiveThreads:    resourceStats.ActiveThreads,
		AvailableThreads: resourceStats.AvailableThreads,
		ActiveTransfers:  resourceStats.ActiveTransfers,
	}
}

// ManagerStats reports pool utilization (available vs. in-use threads).
type ManagerStats struct {
	TotalThreads     int
	ActiveThreads    int
	AvailableThreads int
	ActiveTransfers  int
}

// Transfer is a handle returned by AllocateTransfer; call Complete() to release threads back to the pool.
type Transfer struct {
	id          string
	fileSize    int64
	threads     int
	resourceMgr *resources.Manager
	mu          sync.Mutex
	completed   bool
}

func (t *Transfer) GetThreads() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.threads
}

// TryAcquireMore attempts to acquire additional threads from the pool.
// Returns the number of additional threads acquired (0 if none available).
// This is used for dynamic scaling - as other transfers complete, their threads
// become available and can be claimed by active transfers to speed up.
func (t *Transfer) TryAcquireMore(maxWanted int) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.completed {
		return 0
	}

	// Calculate how many more we could use (up to max for this file size)
	maxForSize := t.resourceMgr.GetMaxForFileSize(t.fileSize)
	canUse := maxForSize - t.threads
	if canUse <= 0 {
		return 0
	}
	if canUse > maxWanted {
		canUse = maxWanted
	}

	// Try to acquire from the pool
	acquired := t.resourceMgr.TryAcquire(t.id, canUse)
	t.threads += acquired
	return acquired
}

// Complete releases this transfer's threads back to the pool. Safe to call multiple times.
func (t *Transfer) Complete() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.completed {
		t.resourceMgr.ReleaseTransfer(t.id)
		t.completed = true
	}
}

func (t *Transfer) GetID() string {
	return t.id
}

func (t *Transfer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return fmt.Sprintf("Transfer[id=%s threads=%d size=%d completed=%v]",
		t.id, t.threads, t.fileSize, t.completed)
}

var (
	transferCounter uint64
	transferMu      sync.Mutex
)

func generateTransferID() string {
	transferMu.Lock()
	defer transferMu.Unlock()
	transferCounter++
	return fmt.Sprintf("transfer-%d", transferCounter)
}
