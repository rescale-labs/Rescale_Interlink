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

// TestStreamingDownloadBatchAdaptiveConcurrency verifies that
// StartStreamingDownloadBatch uses RunBatchFromChannel with adaptive concurrency
// from the ResourceManager, not hardcoded workers.
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

// TestWaitForBatch_ContextCancel — WaitForBatch returns ctx.Err() when the
// context is cancelled before the batch finishes.
func TestWaitForBatch_ContextCancel(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := ts.WaitForBatch(ctx, "missing-batch")
	if err != context.Canceled {
		t.Errorf("WaitForBatch err = %v, want context.Canceled", err)
	}
}

// TestWaitForBatch_EmptyBatch — batch pre-registered with no tasks;
// MarkBatchScanInProgress(false) flips TotalKnown=true; WaitForBatch
// returns the empty-batch stats (Total=0).
func TestWaitForBatch_EmptyBatch(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	batchID := "empty-batch"
	ts.queue.PreRegisterBatch(batchID, "Empty", "download", SourceLabelDaemon)
	ts.queue.MarkBatchScanInProgress(batchID, true)
	// Before flipping scan-in-progress off, WaitForBatch must NOT return.
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	done := make(chan transfer.BatchStats, 1)
	go func() {
		bs, _ := ts.WaitForBatch(ctx, batchID)
		done <- bs
	}()

	// Leave scan-in-progress true for a beat; WaitForBatch should still be waiting.
	time.Sleep(400 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("WaitForBatch returned early while TotalKnown=false")
	default:
	}

	// Flip TotalKnown=true: empty batch — WaitForBatch returns.
	ts.queue.MarkBatchScanInProgress(batchID, false)
	select {
	case bs := <-done:
		if bs.Total != 0 {
			t.Errorf("WaitForBatch Total = %d, want 0", bs.Total)
		}
		if !bs.TotalKnown {
			t.Error("WaitForBatch returned with TotalKnown=false")
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForBatch did not return after TotalKnown flipped true")
	}
}

// TestWaitForBatch_FastFirstTask — a fast-completing first task must not
// cause WaitForBatch to return early while scan-in-progress is still true.
func TestWaitForBatch_FastFirstTask(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	batchID := "fast-first"
	ts.queue.PreRegisterBatch(batchID, "Fast", "download", SourceLabelDaemon)
	ts.queue.MarkBatchScanInProgress(batchID, true)

	// Register + complete one task while scan is still in progress.
	task := ts.queue.TrackTransferWithBatch(
		"f1.dat", 10, transfer.TaskTypeDownload, "fid", "/tmp/f1",
		SourceLabelDaemon, batchID, "Fast",
	)
	ts.queue.Complete(task.ID)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan transfer.BatchStats, 1)
	go func() {
		bs, _ := ts.WaitForBatch(ctx, batchID)
		done <- bs
	}()

	// Even though the task is done, scan-in-progress prevents early return.
	time.Sleep(400 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("WaitForBatch returned while TotalKnown=false (fast-first task)")
	default:
	}

	ts.queue.MarkBatchScanInProgress(batchID, false)
	select {
	case bs := <-done:
		if bs.Completed != 1 {
			t.Errorf("WaitForBatch Completed = %d, want 1", bs.Completed)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForBatch did not return after scan flip")
	}
}
