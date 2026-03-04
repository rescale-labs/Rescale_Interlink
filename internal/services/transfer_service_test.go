package services

import (
	"context"
	"testing"

	"github.com/rescale/rescale-int/internal/events"
)

func TestNewTransferService(t *testing.T) {
	eventBus := events.NewEventBus(100)

	// Test with default config
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})
	if ts == nil {
		t.Fatal("NewTransferService returned nil")
	}

	// Queue should be initialized
	if ts.queue == nil {
		t.Error("Queue not initialized")
	}

	// Semaphore should be initialized with default capacity (MaxMaxConcurrent=20)
	// v4.8.0: Default changed from 5 to 20 for adaptive concurrency
	if cap(ts.semaphore) != 20 {
		t.Errorf("Semaphore capacity = %d, want 20", cap(ts.semaphore))
	}
}

func TestNewTransferServiceWithCustomConcurrency(t *testing.T) {
	eventBus := events.NewEventBus(100)

	ts := NewTransferService(nil, eventBus, TransferServiceConfig{
		MaxConcurrent: 10,
	})
	if ts == nil {
		t.Fatal("NewTransferService returned nil")
	}

	if cap(ts.semaphore) != 10 {
		t.Errorf("Semaphore capacity = %d, want 10", cap(ts.semaphore))
	}
}

func TestTransferStats(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	stats := ts.GetStats()
	if stats.Total() != 0 {
		t.Errorf("Initial stats total = %d, want 0", stats.Total())
	}
}

func TestGetQueue(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	queue := ts.GetQueue()
	if queue == nil {
		t.Error("GetQueue returned nil")
	}
}

func TestGetSemaphore(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{
		MaxConcurrent: 3,
	})

	sem := ts.GetSemaphore()
	if sem == nil {
		t.Error("GetSemaphore returned nil")
	}
	if cap(sem) != 3 {
		t.Errorf("Semaphore capacity = %d, want 3", cap(sem))
	}
}

func TestClearCompleted(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	// Should not panic with empty queue
	ts.ClearCompleted()

	stats := ts.GetStats()
	if stats.Total() != 0 {
		t.Errorf("Stats total after clear = %d, want 0", stats.Total())
	}
}

func TestCancelAllEmpty(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	// Should not panic with empty queue
	ts.CancelAll()
}

// TestStreamingDownloadBatchAdaptiveConcurrency verifies Bug #1 fix:
// StartStreamingDownloadBatch uses RunBatchFromChannel with adaptive concurrency
// from the ResourceManager, not hardcoded 5 workers.
// v4.8.1: The resource manager is wired through to BatchConfig.ResourceMgr.
func TestStreamingDownloadBatchAdaptiveConcurrency(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{
		MaxConcurrent: 15,
	})

	// Verify resource manager is initialized (prerequisite for adaptive concurrency).
	// RunBatchFromChannel panics if ResourceMgr is nil — this was the Bug #1 issue:
	// before the fix, StartStreamingDownloadBatch created a hardcoded 5-worker pool
	// instead of using the resource manager for adaptive concurrency.
	if ts.resourceMgr == nil {
		t.Fatal("resourceMgr is nil — RunBatchFromChannel would panic (Bug #1 regression)")
	}

	// Verify transfer manager is initialized
	if ts.transferMgr == nil {
		t.Fatal("transferMgr is nil")
	}

	// Verify semaphore cap matches config (MaxWorkers comes from cap(semaphore))
	if cap(ts.semaphore) != 15 {
		t.Errorf("semaphore capacity = %d, want 15 (MaxWorkers for BatchConfig)", cap(ts.semaphore))
	}

	// StartStreamingDownloadBatch requires an API client. Without one, it returns an error
	// immediately — before reaching RunBatchFromChannel. This verifies the error path.
	ch := make(chan TransferRequest)
	close(ch)
	err := ts.StartStreamingDownloadBatch(context.Background(), ch, "test-batch", "test", "", nil)
	if err == nil {
		t.Fatal("expected error with nil API client")
	}

	// Verify the resource manager can compute batch concurrency (the adaptive core).
	// Small files should get more workers than large files.
	smallFiles := make([]int64, 10)
	for i := range smallFiles {
		smallFiles[i] = 1024 // 1KB each
	}
	smallWorkers := ts.resourceMgr.ComputeBatchConcurrency(smallFiles, 15)

	largeFiles := make([]int64, 10)
	for i := range largeFiles {
		largeFiles[i] = 5 * 1024 * 1024 * 1024 // 5GB each
	}
	largeWorkers := ts.resourceMgr.ComputeBatchConcurrency(largeFiles, 15)

	if smallWorkers <= largeWorkers {
		t.Errorf("adaptive concurrency broken: small files got %d workers, large files got %d (expected small > large)",
			smallWorkers, largeWorkers)
	}
}
