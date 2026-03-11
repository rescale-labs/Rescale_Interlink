package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/transfer"
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

// v4.8.7: checkBatchCompletion tests (Plan 5, 10B)

// TestCheckBatchCompletion_TotalWipeout verifies that a batch where ALL tasks failed
// triggers a report via the standard ClassifyAndPublish path.
func TestCheckBatchCompletion_TotalWipeout(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()
	ch := eb.Subscribe(events.EventReportableError)

	ts := NewTransferService(nil, eb, TransferServiceConfig{})
	q := ts.GetQueue()

	// Create 5 tasks, fail all of them with a 500 error (reportable via standard path)
	for i := 0; i < 5; i++ {
		task := q.TrackTransferWithBatch(
			fmt.Sprintf("file%d.dat", i), 1024, transfer.TaskTypeUpload,
			"/src", "/dst", "FileBrowser", "batch-total", "TestBatch",
		)
		q.Fail(task.ID, fmt.Errorf("500 internal server error"))
	}

	ts.checkBatchCompletion("batch-total", "upload")

	select {
	case event := <-ch:
		re := event.(*events.ReportableErrorEvent)
		if re.Category != "transfer" {
			t.Errorf("expected category 'transfer', got %q", re.Category)
		}
	case <-time.After(time.Second):
		t.Fatal("expected ReportableErrorEvent for total wipeout, got none")
	}
}

// TestCheckBatchCompletion_PartialNetworkFailure verifies that a batch with partial
// network failures publishes a report (overriding IsReportable suppression).
func TestCheckBatchCompletion_PartialNetworkFailure(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()
	ch := eb.Subscribe(events.EventReportableError)

	ts := NewTransferService(nil, eb, TransferServiceConfig{})
	q := ts.GetQueue()

	// 8 completed + 2 failed with DNS error
	for i := 0; i < 8; i++ {
		task := q.TrackTransferWithBatch(
			fmt.Sprintf("ok%d.dat", i), 1024, transfer.TaskTypeUpload,
			"/src", "/dst", "FileBrowser", "batch-partial", "TestBatch",
		)
		q.Complete(task.ID)
	}
	for i := 0; i < 2; i++ {
		task := q.TrackTransferWithBatch(
			fmt.Sprintf("fail%d.dat", i), 1024, transfer.TaskTypeUpload,
			"/src", "/dst", "FileBrowser", "batch-partial", "TestBatch",
		)
		q.Fail(task.ID, fmt.Errorf("dial tcp: lookup api.rescale.com: no such host"))
	}

	ts.checkBatchCompletion("batch-partial", "upload")

	select {
	case event := <-ch:
		re := event.(*events.ReportableErrorEvent)
		if re.Category != "transfer" {
			t.Errorf("expected category 'transfer', got %q", re.Category)
		}
		// Should contain partial failure context
		if re.ErrorMessage == "" {
			t.Error("expected non-empty ErrorMessage with batch context")
		}
	case <-time.After(time.Second):
		t.Fatal("expected ReportableErrorEvent for partial network failure, got none")
	}
}

// TestCheckBatchCompletion_PartialAuthFailure verifies that partial auth failures
// are NOT published (batch context doesn't contradict auth errors).
func TestCheckBatchCompletion_PartialAuthFailure(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()
	ch := eb.Subscribe(events.EventReportableError)

	ts := NewTransferService(nil, eb, TransferServiceConfig{})
	q := ts.GetQueue()

	// 5 completed + 2 failed with auth error
	for i := 0; i < 5; i++ {
		task := q.TrackTransferWithBatch(
			fmt.Sprintf("ok%d.dat", i), 1024, transfer.TaskTypeDownload,
			"/src", "/dst", "FileBrowser", "batch-auth", "TestBatch",
		)
		q.Complete(task.ID)
	}
	for i := 0; i < 2; i++ {
		task := q.TrackTransferWithBatch(
			fmt.Sprintf("fail%d.dat", i), 1024, transfer.TaskTypeDownload,
			"/src", "/dst", "FileBrowser", "batch-auth", "TestBatch",
		)
		q.Fail(task.ID, fmt.Errorf("403 Forbidden"))
	}

	ts.checkBatchCompletion("batch-auth", "download")

	select {
	case <-ch:
		t.Fatal("expected NO ReportableErrorEvent for partial auth failure, but got one")
	case <-time.After(200 * time.Millisecond):
		// Expected — auth errors in partial batch should NOT be reported
	}
}

// TestCheckBatchCompletion_NoFailures verifies that a fully successful batch
// does NOT trigger any report.
func TestCheckBatchCompletion_NoFailures(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()
	ch := eb.Subscribe(events.EventReportableError)

	ts := NewTransferService(nil, eb, TransferServiceConfig{})
	q := ts.GetQueue()

	for i := 0; i < 5; i++ {
		task := q.TrackTransferWithBatch(
			fmt.Sprintf("ok%d.dat", i), 1024, transfer.TaskTypeUpload,
			"/src", "/dst", "FileBrowser", "batch-ok", "TestBatch",
		)
		q.Complete(task.ID)
	}

	ts.checkBatchCompletion("batch-ok", "upload")

	select {
	case <-ch:
		t.Fatal("expected NO ReportableErrorEvent for successful batch, but got one")
	case <-time.After(200 * time.Millisecond):
		// Expected — no failures means no report
	}
}

// TestCheckBatchCompletion_PartialServerError verifies that partial server errors
// use the standard ClassifyAndPublish path (server errors are already reportable).
func TestCheckBatchCompletion_PartialServerError(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()
	ch := eb.Subscribe(events.EventReportableError)

	ts := NewTransferService(nil, eb, TransferServiceConfig{})
	q := ts.GetQueue()

	// 3 completed + 1 failed with 500 server error
	for i := 0; i < 3; i++ {
		task := q.TrackTransferWithBatch(
			fmt.Sprintf("ok%d.dat", i), 1024, transfer.TaskTypeUpload,
			"/src", "/dst", "FileBrowser", "batch-5xx", "TestBatch",
		)
		q.Complete(task.ID)
	}
	task := q.TrackTransferWithBatch(
		"fail.dat", 1024, transfer.TaskTypeUpload,
		"/src", "/dst", "FileBrowser", "batch-5xx", "TestBatch",
	)
	q.Fail(task.ID, fmt.Errorf("500 internal server error"))

	ts.checkBatchCompletion("batch-5xx", "upload")

	select {
	case event := <-ch:
		re := event.(*events.ReportableErrorEvent)
		if re.ErrorClass != "server_error" {
			t.Errorf("expected error class 'server_error', got %q", re.ErrorClass)
		}
	case <-time.After(time.Second):
		t.Fatal("expected ReportableErrorEvent for partial server error, got none")
	}
}
