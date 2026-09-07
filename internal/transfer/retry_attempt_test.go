package transfer

import (
	"context"
	"errors"
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

func (e *scriptedExecutor) ExecuteRetry(task *TransferTask, _ AttemptToken) {
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

// TestASecondRetryDoesNotOverlapAScheduledAttempt covers D6. A retry is
// scheduled, the user cancels before its executor has registered anything, and
// asks for another retry. With no record of the scheduled attempt the queue
// starts a second one: both reach SetCancel, only one wins Activate, and the
// loser's cleanup takes the winner's cancel function with it — leaving a
// running transfer the user can no longer stop.
func TestASecondRetryDoesNotOverlapAScheduledAttempt(t *testing.T) {
	queue := NewQueue(nil)
	executor := newScriptedExecutor()
	queue.SetRetryExecutor(executor)

	task := queue.TrackTransfer("run.tar.gz", 1024, TaskTypeDownload, "file-1", "/tmp/run.tar.gz")

	// A first attempt that fails, leaving the task retryable and unowned.
	queue.SetCancel(task.ID, func() {})
	if !queue.Activate(task.ID) {
		t.Fatal("Activate: the task should have been queued")
	}
	queue.Fail(task.ID, errors.New("500 internal server error"))

	// The first retry is scheduled. Its executor is held at the door, before it
	// registers a cancellation — that gap is the whole finding.
	if _, err := queue.Retry(task.ID); err != nil {
		t.Fatalf("first Retry: %v", err)
	}
	if _, started := executor.awaitStart(t, 2*time.Second); !started {
		t.Fatal("the first retry was never scheduled")
	}

	if err := queue.Cancel(task.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := queue.Retry(task.ID); err != nil {
		t.Fatalf("second Retry: %v", err)
	}
	if _, started := executor.awaitStart(t, 200*time.Millisecond); started {
		t.Fatal("a second attempt was scheduled while the first had not entered yet")
	}

	// The scheduled attempt now enters, finds the task cancelled under it and
	// gives it up. Only then does the retry claimed behind it start.
	close(executor.proceed)
	queue.SetCancel(task.ID, func() { t.Error("the superseded attempt's cancellation was invoked") })
	if queue.Activate(task.ID) {
		t.Fatal("Activate: a cancelled task should not be claimable")
	}
	queue.ClearCancel(task.ID)

	retried, started := executor.awaitStart(t, 2*time.Second)
	if !started {
		t.Fatal("the retry claimed behind the scheduled attempt never started")
	}
	if got := retried.GetState(); got != TaskQueued {
		t.Errorf("the retry started with state %q, want %q", got, TaskQueued)
	}

	// The attempt that did run keeps its own cancellation, so the user can still
	// stop it.
	stopped := make(chan struct{})
	queue.SetCancel(task.ID, func() { close(stopped) })
	if !queue.Activate(task.ID) {
		t.Fatal("Activate: the retried task should have been queued")
	}
	if err := queue.Cancel(task.ID); err != nil {
		t.Fatalf("cancelling the running retry: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Error("cancelling the running attempt never reached its cancellation")
	}
	if got := executor.runCount(); got != 2 {
		t.Errorf("%d retry attempts ran, want 2 (one scheduled, one claimed behind it)", got)
	}
}

// TestASupersededAttemptCannotDisownTheCurrentOne pins the ownership half of
// D6. An executor that has already given the task up must not be able to take
// the attempt that replaced it apart: removing its cancellation leaves a
// running transfer with nothing to stop it, and reporting an outcome on its
// behalf ends a transfer that is still going.
func TestASupersededAttemptCannotDisownTheCurrentOne(t *testing.T) {
	queue := NewQueue(nil)
	task := queue.TrackTransfer("run.tar.gz", 1024, TaskTypeDownload, "file-1", "/tmp/run.tar.gz")

	// The first attempt starts and gives the task up with no terminal state, the
	// way an executor that lost the Activate race does.
	first, owned := queue.BeginAttempt(task.ID, NoAttempt)
	if !owned {
		t.Fatal("BeginAttempt: an unowned task should have been claimable")
	}
	first.SetCancel(func() { t.Error("the superseded attempt's cancellation was invoked") })
	first.ClearCancel()

	// The second attempt takes over and starts transferring.
	second, owned := queue.BeginAttempt(task.ID, NoAttempt)
	if !owned {
		t.Fatal("BeginAttempt: a released task should have been claimable again")
	}
	stopped := make(chan struct{})
	second.SetCancel(func() { close(stopped) })
	if !queue.Activate(task.ID) {
		t.Fatal("Activate: the task should have been queued")
	}
	if _, extra := queue.BeginAttempt(task.ID, NoAttempt); extra {
		t.Error("BeginAttempt handed out a second claim on a task an attempt is running")
	}

	// Everything the first attempt does from here is about a task it no longer owns.
	first.ClearCancel()
	if first.FailIfNotTerminal(context.Canceled) {
		t.Error("a superseded attempt failed the task the current one is running")
	}
	first.Fail(errors.New("unexpected EOF"))
	first.Complete()

	if got := task.GetState(); got != TaskInitializing {
		t.Errorf("task state = %q, want %q — a superseded attempt reported for the running one",
			got, TaskInitializing)
	}
	if err := queue.Cancel(task.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Error("the running attempt's cancellation was gone by the time the user cancelled")
	}
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
