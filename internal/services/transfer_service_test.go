package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/transfer"
)

// TestNewTransferService pins what a new service is built with — the default
// concurrency is constants.MaxMaxConcurrent, a configured one wins — and that
// an empty service has nothing to report, clear or cancel.
func TestNewTransferService(t *testing.T) {
	for _, tc := range []struct{ configured, want int }{{0, 20}, {3, 3}} {
		ts := NewTransferService(nil, events.NewEventBus(100), TransferServiceConfig{MaxConcurrent: tc.configured})
		if ts.GetQueue() == nil || cap(ts.GetSemaphore()) != tc.want {
			t.Errorf("MaxConcurrent %d: queue set %v, semaphore capacity %d, want %d",
				tc.configured, ts.GetQueue() != nil, cap(ts.GetSemaphore()), tc.want)
		}
		ts.ClearCompleted()
		ts.CancelAll()
		if total := ts.GetStats().Total(); total != 0 {
			t.Errorf("an empty service reports %d transfers", total)
		}
	}
}

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

// newBatchFixture is a transfer service whose reportable errors the test sees.
func newBatchFixture(t *testing.T) (*TransferService, <-chan events.Event) {
	eb := events.NewEventBus(100)
	t.Cleanup(eb.Close)
	return NewTransferService(nil, eb, TransferServiceConfig{}), eb.Subscribe(events.EventReportableError)
}

// awaitReport returns the report checkBatchCompletion filed, failing the test
// when whether one was filed does not match want.
func awaitReport(t *testing.T, ch <-chan events.Event, want bool) *events.ReportableErrorEvent {
	t.Helper()
	wait := 200 * time.Millisecond
	if want {
		wait = time.Second
	}
	select {
	case event := <-ch:
		re := event.(*events.ReportableErrorEvent)
		if !want {
			t.Fatalf("unexpected report: %s", re.ErrorMessage)
		}
		return re
	case <-time.After(wait):
		if want {
			t.Fatal("expected a report, got none")
		}
		return nil
	}
}

// TestCheckBatchCompletion pins which finished batches file a report. A total
// wipeout is reported only when its representative per-task error is the
// server's; a partial failure is reported for server and network errors but
// not for auth. A lock refusal is the user's to act on, never a report.
func TestCheckBatchCompletion(t *testing.T) {
	server := errors.New("500 internal server error")
	refused := fmt.Errorf("S3Storage upload failed: failed to acquire upload lock: %w", state.ErrUploadLocked)
	tests := []struct {
		name              string
		download          bool
		completed, failed int
		err               error
		wantReport        bool
		wantClass         string // checked when set
	}{
		{"total wipeout", false, 0, 5, server, true, ""},
		{"partial network failure", false, 8, 2, errors.New("dial tcp: lookup api.rescale.com: no such host"), true, ""},
		{"partial server error", false, 3, 1, server, true, "server_error"},
		{"partial auth failure", true, 5, 2, errors.New("403 Forbidden"), false, ""},
		{"every transfer refused by a lock", false, 0, 2, refused, false, ""},
		{"some transfers refused by a lock", false, 3, 2, refused, false, ""},
		{"no failures", false, 5, 0, nil, false, ""},
		{"wipeout, local filesystem", true, 0, 5, errors.New("open /Users/x/Downloads/out/f.dat: permission denied"), false, ""},
		{"wipeout, disk full", true, 0, 5, errors.New("write /Volumes/ext/f.dat: no space left on device"), false, ""},
		{"wipeout, network down", true, 0, 5, errors.New("dial tcp: lookup api.rescale.com: no such host"), false, ""},
		{"wipeout, server error", true, 0, 5, errors.New("API returned 500 internal server error"), true, "server_error"},
		{"wipeout with no task error recorded", false, 0, 3, nil, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, ch := newBatchFixture(t)
			q := ts.GetQueue()
			taskType, direction := transfer.TaskTypeUpload, "upload"
			if tt.download {
				taskType, direction = transfer.TaskTypeDownload, "download"
			}
			for i := 0; i < tt.completed+tt.failed; i++ {
				task := q.TrackTransferWithBatch(fmt.Sprintf("file%d.dat", i), 1024, taskType,
					"/src", "/dst", "FileBrowser", "batch", "TestBatch")
				if i < tt.completed {
					q.Complete(task.ID)
				} else {
					q.Fail(task.ID, tt.err)
				}
			}

			ts.checkBatchCompletion("batch", direction)

			re := awaitReport(t, ch, tt.wantReport)
			if re != nil && (re.Category != "transfer" || re.ErrorMessage == "" || tt.wantClass != "" && re.ErrorClass != tt.wantClass) {
				t.Errorf("report: category %q, class %q, message %q; want transfer, a message and class %q",
					re.Category, re.ErrorClass, re.ErrorMessage, tt.wantClass)
			}
		})
	}
}

// The services log each failed transfer or file operation to stderr, which the
// Windows daemon keeps as daemon-stderr.log: an Azure SAS or an S3 presigned
// URL in the error is redacted there.
func TestServicesLogNoCredentials(t *testing.T) {
	stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	orig := os.Stderr
	os.Stderr = stderr
	loggers := []*logging.Logger{NewTransferService(nil, nil, TransferServiceConfig{}).logger, NewFileService(nil, nil).logger}
	os.Stderr = orig
	for _, logger := range loggers {
		logger.Error().Err(errors.New(`Put "https://a.blob.core.windows.net/c/f?comp=block&sig=SECRET": EOF`)).Msg("Upload failed")
		logger.Error().Err(errors.New(`Get "https://b.s3.amazonaws.com/k?X-Amz-Signature=SECRET": EOF`)).Msg("Download failed")
	}

	printed, _ := os.ReadFile(stderr.Name())
	if strings.Contains(string(printed), "SECRET") || strings.Count(string(printed), "=REDACTED") != 4 {
		t.Errorf("logged %q, want every credential redacted", printed)
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

func TestRegisterSkipPlaceholderTask(t *testing.T) {
	eventBus := events.NewEventBus(100)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

	const batchID = "skip-batch"
	ts.queue.PreRegisterBatch(batchID, "Public", "upload", SourceLabelFileBrowser)
	ts.queue.IncrementBatchSkipped(batchID, 17)

	ts.RegisterSkipPlaceholderTask(batchID, "Public", 17)

	stats := ts.queue.GetAllBatchStats()
	var found *transfer.BatchStats
	for i := range stats {
		if stats[i].BatchID == batchID {
			found = &stats[i]
		}
	}
	if found == nil {
		t.Fatal("placeholder-anchored batch not found")
	}
	if found.Total != 1 {
		t.Errorf("Total = %d, want 1 (the placeholder)", found.Total)
	}
	if found.Completed != 1 {
		t.Errorf("Completed = %d, want 1 (placeholder is created completed)", found.Completed)
	}
	if found.Skipped != 17 {
		t.Errorf("Skipped = %d, want 17", found.Skipped)
	}
	if found.TotalBytes != 0 {
		t.Errorf("TotalBytes = %d, want 0 (placeholder has size=0)", found.TotalBytes)
	}
	if found.BatchLabel != "Public" {
		t.Errorf("BatchLabel = %q, want \"Public\"", found.BatchLabel)
	}

	// The label must survive cleanup of the pre-registered metadata: after
	// CleanupBatch removes the pre-registered entry, the row is derived solely
	// from the placeholder task, which must carry the label itself.
	ts.queue.CleanupBatch(batchID)
	stats = ts.queue.GetAllBatchStats()
	found = nil
	for i := range stats {
		if stats[i].BatchID == batchID {
			found = &stats[i]
		}
	}
	if found == nil {
		t.Fatal("batch row vanished after CleanupBatch — placeholder task should anchor it")
	}
	if found.BatchLabel != "Public" {
		t.Errorf("after cleanup, BatchLabel = %q, want \"Public\" (label must live on the placeholder task)", found.BatchLabel)
	}

	// Empty batchID is a no-op (defensive — caller should never pass it).
	ts.RegisterSkipPlaceholderTask("", "Foo", 1)
}

func TestRegisterEmptyBatchPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		direction string
		wantType  transfer.TaskType
	}{
		{"download", "download", transfer.TaskTypeDownload},
		{"upload", "upload", transfer.TaskTypeUpload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eventBus := events.NewEventBus(100)
			ts := NewTransferService(nil, eventBus, TransferServiceConfig{})

			const batchID = "empty-batch"
			ts.queue.PreRegisterBatch(batchID, "EmptyDir", tc.direction, SourceLabelFileBrowser)
			ts.queue.MarkBatchScanInProgress(batchID, false)

			ts.RegisterEmptyBatchPlaceholder(batchID, "EmptyDir", tc.direction)

			// The batch row must survive CleanupBatch, which removes the pre-registered
			// metadata. Without the placeholder anchoring it in q.tasks, the row would
			// vanish — the bug this fix addresses.
			ts.queue.CleanupBatch(batchID)

			stats := ts.queue.GetAllBatchStats()
			var found *transfer.BatchStats
			for i := range stats {
				if stats[i].BatchID == batchID {
					found = &stats[i]
				}
			}
			if found == nil {
				t.Fatal("empty batch row vanished after CleanupBatch — placeholder should anchor it")
			}
			if found.Total != 1 || found.Completed != 1 {
				t.Errorf("Total=%d Completed=%d, want 1/1 (single completed placeholder)", found.Total, found.Completed)
			}
			if found.Skipped != 0 {
				t.Errorf("Skipped = %d, want 0 (empty, not skipped)", found.Skipped)
			}
			if found.TotalBytes != 0 {
				t.Errorf("TotalBytes = %d, want 0", found.TotalBytes)
			}
			if found.DiscoveredTotal != 0 {
				t.Errorf("DiscoveredTotal = %d, want 0 (frontend keys off this to render the empty summary)", found.DiscoveredTotal)
			}
			if found.BatchLabel != "EmptyDir" {
				t.Errorf("BatchLabel = %q, want \"EmptyDir\"", found.BatchLabel)
			}
		})
	}

	// Empty batchID is a no-op.
	eventBus := events.NewEventBus(10)
	ts := NewTransferService(nil, eventBus, TransferServiceConfig{})
	ts.RegisterEmptyBatchPlaceholder("", "Foo", "download")
}

// A batch the user cancelled must never raise an error report, whatever its
// tasks ended up recording. This is the #27 path: cancelling a large batch
// produced a wipeout "report this error" modal.
func TestCheckBatchCompletion_CancelledBatchNotReported(t *testing.T) {
	ts, ch := newBatchFixture(t)
	q := ts.GetQueue()

	// Some tasks failed for real before the user hit Cancel, the rest were
	// swept by the batch cancel — exactly what a mid-batch cancel produces.
	// The batch-level cancel is what suppresses reporting.
	for i := 0; i < 3; i++ {
		task := q.TrackTransferWithBatch(
			fmt.Sprintf("f%d.dat", i), 1024, transfer.TaskTypeDownload,
			"/src", "/dst", "FileBrowser", "batch-cancelled", "TestBatch",
		)
		q.Activate(task.ID)
		q.Fail(task.ID, fmt.Errorf("wire: something exploded"))
	}
	for i := 0; i < 2; i++ {
		task := q.TrackTransferWithBatch(
			fmt.Sprintf("c%d.dat", i), 1024, transfer.TaskTypeDownload,
			"/src", "/dst", "FileBrowser", "batch-cancelled", "TestBatch",
		)
		q.Activate(task.ID)
	}
	if err := q.CancelBatch("batch-cancelled"); err != nil {
		t.Fatalf("CancelBatch: %v", err)
	}

	ts.checkBatchCompletion("batch-cancelled", "download")

	awaitReport(t, ch, false)
}

// Cancelling ONE task must not hide the rest of the batch's real failures:
// only a batch-level cancel (CancelRequested) suppresses reporting.
func TestCheckBatchCompletion_PerTaskCancelDoesNotSuppress(t *testing.T) {
	ts, ch := newBatchFixture(t)
	q := ts.GetQueue()

	for i := 0; i < 3; i++ {
		task := q.TrackTransferWithBatch(
			fmt.Sprintf("f%d.dat", i), 1024, transfer.TaskTypeDownload,
			"/src", "/dst", "FileBrowser", "batch-mixed", "TestBatch",
		)
		q.Activate(task.ID)
		q.Fail(task.ID, fmt.Errorf("wire: something exploded"))
	}
	task := q.TrackTransferWithBatch(
		"c0.dat", 1024, transfer.TaskTypeDownload,
		"/src", "/dst", "FileBrowser", "batch-mixed", "TestBatch",
	)
	q.Activate(task.ID)
	if err := q.Cancel(task.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	ts.checkBatchCompletion("batch-mixed", "download")

	awaitReport(t, ch, true)
}

// A batch cancelled before its scan registers any task must still leave a
// (cancelled) record in the queue — CleanupBatch drops the pre-registered
// metadata, so without the placeholder the Transfers tab shows nothing.
func TestCancelBatchAnchorsCancelledPlaceholder(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()

	ts := NewTransferService(nil, eb, TransferServiceConfig{})
	q := ts.GetQueue()

	q.PreRegisterBatch("batch-prescan", "MyFolder", "upload", "Daemon")
	if err := ts.CancelBatch("batch-prescan"); err != nil {
		t.Fatalf("CancelBatch: %v", err)
	}

	for _, bs := range q.GetAllBatchStats() {
		if bs.BatchID != "batch-prescan" {
			continue
		}
		if bs.Cancelled != 1 || bs.Total != 1 {
			t.Fatalf("placeholder shape = total %d cancelled %d, want 1/1", bs.Total, bs.Cancelled)
		}
		if bs.SourceLabel != "Daemon" {
			t.Fatalf("placeholder SourceLabel = %q, want the pre-registered %q", bs.SourceLabel, "Daemon")
		}
		return
	}
	t.Fatal("cancelled batch left no record at all")
}

// gatedRetryExecutor holds a retry dispatch at the door and then runs the real
// service path. It lets a test work inside the window between the queue
// reserving that dispatch's attempt and the dispatch entering it.
type gatedRetryExecutor struct {
	ts       *TransferService
	entered  chan struct{}
	proceed  chan struct{}
	returned chan struct{}
}

func (g *gatedRetryExecutor) ExecuteRetry(task *transfer.TransferTask, token transfer.AttemptToken) {
	close(g.entered)
	<-g.proceed
	g.ts.ExecuteRetry(task, token)
	close(g.returned)
}

// TestAnInitialDispatchDoesNotTakeTheRetrysAttempt covers D6's remaining half at
// the service level. An initial executor pauses before claiming its task; the
// user cancels it and asks for it again, so the queue reserves an attempt for
// the retry it dispatches. The initial executor then resumes: if it adopts that
// reservation, the real retry is refused and returns, and the initial executor —
// whose own context died with the cancellation — releases the task. The retry
// the user was promised has disappeared.
func TestAnInitialDispatchDoesNotTakeTheRetrysAttempt(t *testing.T) {
	eventBus := events.NewEventBus(100)
	defer eventBus.Close()

	ts := NewTransferService(&api.Client{}, eventBus, TransferServiceConfig{MaxConcurrent: 1})

	// Occupy the only transfer slot, so an executor that claims the task parks
	// there holding it instead of running a transfer this test cannot serve.
	ts.semaphore <- struct{}{}

	gate := &gatedRetryExecutor{
		ts:       ts,
		entered:  make(chan struct{}),
		proceed:  make(chan struct{}),
		returned: make(chan struct{}),
	}
	ts.queue.SetRetryExecutor(gate)

	req := TransferRequest{
		Type:        TransferTypeDownload,
		Source:      "file-1",
		Dest:        t.TempDir(),
		Name:        "run.tar.gz",
		Size:        1024,
		SourceLabel: SourceLabelFileBrowser,
	}
	taskID := ts.registerDownloadTask(req)

	if err := ts.CancelTransfer(taskID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := ts.queue.Retry(taskID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	<-gate.entered // the retry's attempt is reserved and nothing has entered it

	// The initial executor resumes exactly where it paused.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	initialDone := make(chan struct{})
	go func() {
		defer close(initialDone)
		ts.executeTask(ctx, req, taskID, transfer.NoAttempt, &api.Client{}, 1, ts.downloadDirection())
	}()
	select {
	case <-initialDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the initial dispatch took the attempt the queue reserved for the retry")
	}

	// The retry now enters and holds the task: it waits for a slot rather than
	// finding its own attempt gone and returning.
	close(gate.proceed)
	select {
	case <-gate.returned:
		t.Fatal("the retry dispatch was refused its own attempt and never ran")
	case <-time.After(300 * time.Millisecond):
	}

	// And the user can still stop the transfer they asked for.
	if err := ts.CancelTransfer(taskID); err != nil {
		t.Fatalf("cancelling the running retry: %v", err)
	}
	select {
	case <-gate.returned:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the task never reached the retry that was running it")
	}
	task, ok := ts.queue.GetTask(taskID)
	if !ok {
		t.Fatal("the task is gone from the queue")
	}
	if task.State != transfer.TaskCancelled {
		t.Errorf("task state = %q, want %q", task.State, transfer.TaskCancelled)
	}
}

// TestAFailedRetryDispatchReleasesItsOwnAttempt covers the pre-entry failure
// path: a dispatch that gives up before it enters — here for want of an API
// client — has to hand back the attempt the queue reserved for it, or the task
// stays owned by an executor that never ran.
func TestAFailedRetryDispatchReleasesItsOwnAttempt(t *testing.T) {
	eventBus := events.NewEventBus(100)
	defer eventBus.Close()

	ts := NewTransferService(nil, eventBus, TransferServiceConfig{MaxConcurrent: 1})

	req := TransferRequest{
		Type:        TransferTypeDownload,
		Source:      "file-1",
		Dest:        t.TempDir(),
		Name:        "run.tar.gz",
		Size:        1024,
		SourceLabel: SourceLabelFileBrowser,
	}
	taskID := ts.registerDownloadTask(req)

	if err := ts.CancelTransfer(taskID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := ts.queue.Retry(taskID); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		task, ok := ts.queue.GetTask(taskID)
		if ok && task.State == transfer.TaskFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the retry dispatch never recorded its failure (state %q)", task.State)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, owned := ts.queue.BeginAttempt(taskID, transfer.NoAttempt); !owned {
		t.Fatal("the failed dispatch left the task owned by an attempt that never ran")
	}
}

// TestAFailedRetryDispatchLeavesARunningAttemptAlone is the ownership half of
// the same path. A dispatch whose reservation is no longer its own cannot claim
// that nothing entered: reporting its failure unscoped ends a transfer another
// attempt is running and takes that attempt's cancellation with it.
func TestAFailedRetryDispatchLeavesARunningAttemptAlone(t *testing.T) {
	eventBus := events.NewEventBus(100)
	defer eventBus.Close()

	ts := NewTransferService(nil, eventBus, TransferServiceConfig{MaxConcurrent: 1})

	task := ts.queue.TrackTransferWithLabel("run.tar.gz", 1024, transfer.TaskTypeDownload,
		"file-1", t.TempDir(), SourceLabelFileBrowser)

	// An attempt takes the task and starts transferring.
	running, owned := ts.queue.BeginAttempt(task.ID, transfer.NoAttempt)
	if !owned {
		t.Fatal("BeginAttempt: an unowned task should have been claimable")
	}
	stopped := make(chan struct{})
	running.SetCancel(func() { close(stopped) })
	if !ts.queue.Activate(task.ID) {
		t.Fatal("Activate: the task should have been queued")
	}

	// A retry dispatch whose reservation is gone finds no API client and fails
	// before entering.
	ts.ExecuteRetry(task, transfer.AttemptToken(1<<32))

	if got := task.GetState(); got != transfer.TaskInitializing {
		t.Errorf("task state = %q, want %q — a dispatch that never entered reported for the running attempt",
			got, transfer.TaskInitializing)
	}
	if err := ts.CancelTransfer(task.ID); err != nil {
		t.Fatalf("cancelling the running attempt: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Error("the running attempt's cancellation was gone by the time the user cancelled")
	}
}

// TestARefusedDispatchTouchesNothingOfTheRunningAttempt is the service-side half
// of the ownership gate. A dispatch of a task another attempt is already running
// must return having recorded nothing: registering its own cancel function over
// the running one, and then clearing it on the way out, leaves the transfer that
// is actually running with no cancellation the user can reach.
func TestARefusedDispatchTouchesNothingOfTheRunningAttempt(t *testing.T) {
	eventBus := events.NewEventBus(100)
	defer eventBus.Close()

	ts := NewTransferService(&api.Client{}, eventBus, TransferServiceConfig{MaxConcurrent: 1})

	req := TransferRequest{
		Type:        TransferTypeDownload,
		Source:      "file-1",
		Dest:        t.TempDir(),
		Name:        "run.tar.gz",
		Size:        1024,
		SourceLabel: SourceLabelFileBrowser,
	}
	taskID := ts.registerDownloadTask(req)

	// The attempt that holds the task, transferring it.
	running, owned := ts.queue.BeginAttempt(taskID, transfer.NoAttempt)
	if !owned {
		t.Fatal("BeginAttempt: an unowned task should have been claimable")
	}
	stopped := make(chan struct{})
	running.SetCancel(func() { close(stopped) })
	if !ts.queue.Activate(taskID) {
		t.Fatal("Activate: the task should have been queued")
	}

	// A second dispatch of the same task, scheduled by nobody.
	done := make(chan struct{})
	go func() {
		defer close(done)
		ts.executeTask(context.Background(), req, taskID, transfer.NoAttempt, &api.Client{}, 1, ts.downloadDirection())
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the dispatch went on to run a task another attempt was already running")
	}

	if task, ok := ts.queue.GetTask(taskID); !ok {
		t.Fatal("the task is gone from the queue")
	} else if task.State != transfer.TaskInitializing {
		t.Errorf("task state = %q, want %q — the refused dispatch reported for the running attempt",
			task.State, transfer.TaskInitializing)
	}
	if err := ts.CancelTransfer(taskID); err != nil {
		t.Fatalf("cancelling the running attempt: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Error("the running attempt's cancellation was gone by the time the user cancelled")
	}
}

// TestUploadFileSyncReportsThroughItsOwnAttempt covers the synchronous upload's
// half of the same bookkeeping. It registers the task it runs, so every state it
// records has to go through the attempt it claimed for it — and the paths that
// give up before the transfer starts have to hand that attempt back, or the task
// stays owned by an upload that is no longer running.
func TestUploadFileSyncReportsThroughItsOwnAttempt(t *testing.T) {
	newService := func(t *testing.T) *TransferService {
		t.Helper()
		eventBus := events.NewEventBus(100)
		t.Cleanup(eventBus.Close)
		return NewTransferService(&api.Client{}, eventBus, TransferServiceConfig{MaxConcurrent: 1})
	}

	// The upload registers its own task, so the queue's only entry is the one it
	// claimed an attempt for.
	onlyTaskID := func(t *testing.T, ts *TransferService) string {
		t.Helper()
		tasks := ts.queue.GetTasks()
		if len(tasks) != 1 {
			t.Fatalf("the queue holds %d tasks, want the one the upload registered", len(tasks))
		}
		return tasks[0].ID
	}

	taskState := func(t *testing.T, ts *TransferService, taskID string) transfer.TaskState {
		t.Helper()
		task, ok := ts.queue.GetTask(taskID)
		if !ok {
			t.Fatal("the task the upload registered is gone from the queue")
		}
		return task.State
	}

	t.Run("cancelled before it gets a slot", func(t *testing.T) {
		ts := newService(t)
		ts.semaphore <- struct{}{} // the only slot is taken, so the wait is the cancellable one

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := ts.UploadFileSync(ctx, TransferRequest{
			Source: filepath.Join(t.TempDir(), "payload.bin"),
			Dest:   "folder-1",
			Name:   "payload.bin",
		}, UploadFileSyncParams{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("UploadFileSync returned %v, want the cancellation", err)
		}

		taskID := onlyTaskID(t, ts)
		if got := taskState(t, ts, taskID); got != transfer.TaskFailed {
			t.Errorf("task state = %q, want %q", got, transfer.TaskFailed)
		}
		if _, owned := ts.queue.BeginAttempt(taskID, transfer.NoAttempt); !owned {
			t.Error("the abandoned upload left the task owned by an attempt that is no longer running")
		}
	})

	t.Run("registered into a cancelled batch", func(t *testing.T) {
		ts := newService(t)
		const batchID = "batch-cancelled"
		if err := ts.CancelBatch(batchID); err != nil {
			t.Fatalf("CancelBatch: %v", err)
		}

		_, err := ts.UploadFileSync(context.Background(), TransferRequest{
			Source:  filepath.Join(t.TempDir(), "payload.bin"),
			Dest:    "folder-1",
			Name:    "payload.bin",
			BatchID: batchID,
		}, UploadFileSyncParams{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("UploadFileSync returned %v, want the cancellation", err)
		}

		// The task was terminal before the upload claimed it, so the attempt is
		// given up without a terminal transition of its own.
		taskID := onlyTaskID(t, ts)
		if got := taskState(t, ts, taskID); got != transfer.TaskCancelled {
			t.Errorf("task state = %q, want %q", got, transfer.TaskCancelled)
		}
		if _, owned := ts.queue.BeginAttempt(taskID, transfer.NoAttempt); !owned {
			t.Error("the upload that never started left the task owned")
		}
	})
}
