package transfer

import (
	"context"
	"sync"
	"testing"
	"time"
)

// scriptedExecutor stands in for the transfer service: it announces that a
// retry attempt started and waits to be told to run, so the test can hold the
// new attempt still while the old one finishes unwinding.
type scriptedExecutor struct {
	started  chan *TransferTask
	proceed  chan struct{}
	mu       sync.Mutex
	runs     int
	sawState []TaskState
}

func newScriptedExecutor() *scriptedExecutor {
	return &scriptedExecutor{
		started: make(chan *TransferTask, 4),
		proceed: make(chan struct{}),
	}
}

func (e *scriptedExecutor) ExecuteRetry(task *TransferTask) {
	e.mu.Lock()
	e.runs++
	e.sawState = append(e.sawState, task.GetState())
	e.mu.Unlock()

	e.started <- task
	<-e.proceed
}

func (e *scriptedExecutor) runCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runs
}

// awaitStart waits for a retry attempt to begin, or reports that none did.
func (e *scriptedExecutor) awaitStart(t *testing.T, within time.Duration) (*TransferTask, bool) {
	t.Helper()

	select {
	case task := <-e.started:
		return task, true
	case <-time.After(within):
		return nil, false
	}
}

// TestRetryWaitsForTheCancelledAttemptToFinish reproduces F12: a cancel is
// followed at once by a retry, and the cancelled attempt only then unwinds and
// reports through the task ID both attempts share. Its late failure must not
// land on the attempt that replaced it.
func TestRetryWaitsForTheCancelledAttemptToFinish(t *testing.T) {
	queue := NewQueue(nil)
	executor := newScriptedExecutor()
	queue.SetRetryExecutor(executor)

	task := queue.TrackTransfer("run.tar.gz", 1024, TaskTypeDownload, "file-1", "/tmp/run.tar.gz")

	// The first attempt: registered, running, then cancelled by the user. Its
	// executor has not returned yet — that is the window this covers.
	_, cancel := context.WithCancel(context.Background())
	queue.SetCancel(task.ID, cancel)
	if !queue.Activate(task.ID) {
		t.Fatal("Activate: the task should have been queued")
	}
	queue.StartTransfer(task.ID)
	if err := queue.Cancel(task.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if _, err := queue.Retry(task.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	if _, started := executor.awaitStart(t, 100*time.Millisecond); started {
		t.Fatal("the replacement attempt started while the cancelled one was still running")
	}

	// The cancelled attempt now returns, reporting the error its cancellation
	// produced.
	if queue.FailIfNotTerminal(task.ID, context.Canceled) {
		t.Error("the cancelled attempt's late error was recorded as a failure")
	}

	retried, started := executor.awaitStart(t, 2*time.Second)
	if !started {
		t.Fatal("the claimed retry never started")
	}
	if got := retried.GetState(); got != TaskQueued {
		t.Errorf("the retry started with state %q, want %q", got, TaskQueued)
	}

	// The replacement attempt runs to completion untouched by the old one.
	close(executor.proceed)
	queue.SetCancel(task.ID, func() {})
	if !queue.Activate(task.ID) {
		t.Fatal("Activate: the retried task should have been queued")
	}
	queue.Complete(task.ID)

	if got := task.GetState(); got != TaskCompleted {
		t.Errorf("task state = %q, want %q", got, TaskCompleted)
	}
	if got := executor.runCount(); got != 1 {
		t.Errorf("%d retry attempts ran, want 1", got)
	}
}

// TestCancelAllStopsAClaimedRetry covers N9. A retry claimed while the
// cancelled attempt is still unwinding sits on a task that is already terminal,
// so a Cancel All sweep skips it — and releaseAttempt then starts the transfer
// the user just stopped everything to avoid.
func TestCancelAllStopsAClaimedRetry(t *testing.T) {
	queue := NewQueue(nil)
	executor := newScriptedExecutor()
	queue.SetRetryExecutor(executor)

	task := queue.TrackTransfer("run.tar.gz", 1024, TaskTypeDownload, "file-1", "/tmp/run.tar.gz")

	_, cancel := context.WithCancel(context.Background())
	queue.SetCancel(task.ID, cancel)
	if !queue.Activate(task.ID) {
		t.Fatal("Activate: the task should have been queued")
	}
	queue.StartTransfer(task.ID)
	if err := queue.Cancel(task.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// The retry is claimed while the cancelled attempt is still unwinding.
	if _, err := queue.Retry(task.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	queue.CancelAll()

	// Only now does the old attempt return.
	queue.FailIfNotTerminal(task.ID, context.Canceled)

	if _, started := executor.awaitStart(t, 200*time.Millisecond); started {
		t.Fatal("the claimed retry started after Cancel All")
	}
	if got := task.GetState(); got != TaskCancelled {
		t.Errorf("task state = %q, want %q", got, TaskCancelled)
	}
}

// TestCancelBatchStopsAClaimedRetry is the same sequence through the per-batch
// sweep, which likewise walks past a task the claim has already made terminal.
func TestCancelBatchStopsAClaimedRetry(t *testing.T) {
	queue := NewQueue(nil)
	executor := newScriptedExecutor()
	queue.SetRetryExecutor(executor)

	task := queue.TrackTransferWithBatch("run.tar.gz", 1024, TaskTypeDownload, "file-1",
		"/tmp/run.tar.gz", "Library", "batch-1", "Batch 1")

	_, cancel := context.WithCancel(context.Background())
	queue.SetCancel(task.ID, cancel)
	if !queue.Activate(task.ID) {
		t.Fatal("Activate: the task should have been queued")
	}
	queue.StartTransfer(task.ID)
	if err := queue.Cancel(task.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := queue.Retry(task.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	if err := queue.CancelBatch("batch-1"); err != nil {
		t.Fatalf("CancelBatch: %v", err)
	}

	queue.FailIfNotTerminal(task.ID, context.Canceled)

	if _, started := executor.awaitStart(t, 200*time.Millisecond); started {
		t.Fatal("the claimed retry started after the batch was cancelled")
	}
}

// TestCancelStopsAClaimedRetry is the single-task sweep. The task is already
// terminal by the time the claim exists, so cancelling it used to report that it
// was not cancellable and leave the pending attempt to run regardless.
func TestCancelStopsAClaimedRetry(t *testing.T) {
	queue := NewQueue(nil)
	executor := newScriptedExecutor()
	queue.SetRetryExecutor(executor)

	task := queue.TrackTransfer("run.tar.gz", 1024, TaskTypeDownload, "file-1", "/tmp/run.tar.gz")

	_, cancel := context.WithCancel(context.Background())
	queue.SetCancel(task.ID, cancel)
	if !queue.Activate(task.ID) {
		t.Fatal("Activate: the task should have been queued")
	}
	queue.StartTransfer(task.ID)
	if err := queue.Cancel(task.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := queue.Retry(task.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	if err := queue.Cancel(task.ID); err != nil {
		t.Fatalf("second Cancel: %v", err)
	}

	queue.FailIfNotTerminal(task.ID, context.Canceled)

	if _, started := executor.awaitStart(t, 200*time.Millisecond); started {
		t.Fatal("the claimed retry started after the task was cancelled again")
	}
}

// TestRetryAfterCancelAllStillRuns is the boundary: dropping pending claims must
// not cost the user the deliberate retry they ask for afterwards.
func TestRetryAfterCancelAllStillRuns(t *testing.T) {
	queue := NewQueue(nil)
	executor := newScriptedExecutor()
	queue.SetRetryExecutor(executor)

	task := queue.TrackTransfer("run.tar.gz", 1024, TaskTypeDownload, "file-1", "/tmp/run.tar.gz")

	_, cancel := context.WithCancel(context.Background())
	queue.SetCancel(task.ID, cancel)
	if !queue.Activate(task.ID) {
		t.Fatal("Activate: the task should have been queued")
	}
	queue.StartTransfer(task.ID)
	queue.CancelAll()

	if _, err := queue.Retry(task.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	queue.FailIfNotTerminal(task.ID, context.Canceled)

	if _, started := executor.awaitStart(t, 2*time.Second); !started {
		t.Fatal("a retry requested after Cancel All never started")
	}
	close(executor.proceed)
}

// TestRepeatedRetryClaimsOneAttempt covers the other half of F12: the retryable
// check and the claim have to be one step. Several retry requests for the same
// task — a double click, or a batch retry overlapping a single one — must leave
// exactly one attempt claimed, and each caller must be told the retry is under
// way rather than that the task cannot be retried.
func TestRepeatedRetryClaimsOneAttempt(t *testing.T) {
	queue := NewQueue(nil)
	executor := newScriptedExecutor()
	queue.SetRetryExecutor(executor)

	task := queue.TrackTransfer("run.tar.gz", 1024, TaskTypeDownload, "file-1", "/tmp/run.tar.gz")

	_, cancel := context.WithCancel(context.Background())
	queue.SetCancel(task.ID, cancel)
	if !queue.Activate(task.ID) {
		t.Fatal("Activate: the task should have been queued")
	}
	if err := queue.Cancel(task.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	const requests = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, requests)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = queue.Retry(task.ID)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("retry request %d: %v", i, err)
		}
	}

	// Let the cancelled attempt finish, which is what releases the claim.
	queue.FailIfNotTerminal(task.ID, context.Canceled)

	if _, started := executor.awaitStart(t, 2*time.Second); !started {
		t.Fatal("no retry attempt started")
	}
	close(executor.proceed)

	// Nothing else may be waiting to run: a second attempt would write to the
	// same destination as the first.
	if _, extra := executor.awaitStart(t, 100*time.Millisecond); extra {
		t.Fatalf("%d retry attempts ran for %d requests, want 1",
			executor.runCount(), requests)
	}
}
