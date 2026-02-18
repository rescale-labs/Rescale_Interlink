package transfer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/events"
)

// Task tests

func TestNewTransferTask(t *testing.T) {
	task := NewTransferTask(TaskTypeUpload, "test.dat", "/local/path", "folder123", 1024)

	if task.ID == "" {
		t.Error("Task ID should not be empty")
	}
	if task.Type != TaskTypeUpload {
		t.Errorf("Expected TaskTypeUpload, got %v", task.Type)
	}
	if task.Name != "test.dat" {
		t.Errorf("Expected name 'test.dat', got %s", task.Name)
	}
	if task.State != TaskQueued {
		t.Errorf("Expected TaskQueued, got %v", task.State)
	}
	if task.Progress != 0.0 {
		t.Errorf("Expected progress 0.0, got %f", task.Progress)
	}
}

func TestTransferTaskState(t *testing.T) {
	task := NewTransferTask(TaskTypeDownload, "result.zip", "file123", "/local/path", 2048)

	// Test state transitions
	task.SetState(TaskActive)
	if task.GetState() != TaskActive {
		t.Errorf("Expected TaskActive, got %v", task.GetState())
	}
	if task.StartedAt.IsZero() {
		t.Error("StartedAt should be set when state changes to Active")
	}

	task.SetState(TaskCompleted)
	if task.GetState() != TaskCompleted {
		t.Errorf("Expected TaskCompleted, got %v", task.GetState())
	}
	if task.CompletedAt.IsZero() {
		t.Error("CompletedAt should be set when state changes to Completed")
	}
}

func TestTransferTaskProgress(t *testing.T) {
	task := NewTransferTask(TaskTypeUpload, "data.csv", "/path", "folder", 1000)

	task.UpdateProgress(0.5, 1000.0)
	if task.GetProgress() != 0.5 {
		t.Errorf("Expected progress 0.5, got %f", task.GetProgress())
	}
	if task.GetSpeed() != 1000.0 {
		t.Errorf("Expected speed 1000.0, got %f", task.GetSpeed())
	}
}

func TestTransferTaskCancel(t *testing.T) {
	task := NewTransferTask(TaskTypeUpload, "test.dat", "/path", "folder", 100)

	// Verify context is not cancelled initially
	select {
	case <-task.Context().Done():
		t.Error("Context should not be cancelled initially")
	default:
		// Good
	}

	// Cancel and verify
	task.Cancel()
	if task.GetState() != TaskCancelled {
		t.Errorf("Expected TaskCancelled, got %v", task.GetState())
	}

	select {
	case <-task.Context().Done():
		// Good - context is cancelled
	default:
		t.Error("Context should be cancelled after Cancel()")
	}
}

func TestTransferTaskError(t *testing.T) {
	task := NewTransferTask(TaskTypeDownload, "fail.dat", "file123", "/path", 500)

	testErr := errors.New("transfer failed")
	task.SetError(testErr)

	if task.GetState() != TaskFailed {
		t.Errorf("Expected TaskFailed, got %v", task.GetState())
	}
	if task.GetError() != testErr {
		t.Errorf("Expected error 'transfer failed', got %v", task.GetError())
	}
}

func TestTransferTaskClone(t *testing.T) {
	task := NewTransferTask(TaskTypeUpload, "clone.dat", "/path", "folder", 1024)
	task.SetState(TaskActive)
	task.UpdateProgress(0.75, 500.0)

	clone := task.Clone()
	if clone.ID != task.ID {
		t.Error("Clone should have same ID")
	}
	if clone.Progress != 0.75 {
		t.Errorf("Clone should have same progress, got %f", clone.Progress)
	}
	if clone.State != TaskActive {
		t.Errorf("Clone should have same state, got %v", clone.State)
	}
}

func TestTransferTaskIsTerminal(t *testing.T) {
	tests := []struct {
		state    TaskState
		terminal bool
	}{
		{TaskQueued, false},
		{TaskActive, false},
		{TaskPaused, false},
		{TaskCompleted, true},
		{TaskFailed, true},
		{TaskCancelled, true},
	}

	for _, tt := range tests {
		task := NewTransferTask(TaskTypeUpload, "test", "a", "b", 100)
		task.SetState(tt.state)
		if task.IsTerminal() != tt.terminal {
			t.Errorf("State %v: expected terminal=%v, got %v", tt.state, tt.terminal, task.IsTerminal())
		}
	}
}

func TestTransferTaskCanRetry(t *testing.T) {
	tests := []struct {
		state    TaskState
		canRetry bool
	}{
		{TaskQueued, false},
		{TaskActive, false},
		{TaskPaused, false},
		{TaskCompleted, false},
		{TaskFailed, true},
		{TaskCancelled, true},
	}

	for _, tt := range tests {
		task := NewTransferTask(TaskTypeUpload, "test", "a", "b", 100)
		task.SetState(tt.state)
		if task.CanRetry() != tt.canRetry {
			t.Errorf("State %v: expected canRetry=%v, got %v", tt.state, tt.canRetry, task.CanRetry())
		}
	}
}

// Queue tests - v3.6.3 observer pattern

func TestNewQueue(t *testing.T) {
	eventBus := events.NewEventBus(100)
	defer eventBus.Close()

	queue := NewQueue(eventBus)
	if queue == nil {
		t.Fatal("NewQueue returned nil")
	}

	queue2 := NewQueue(nil)
	if queue2 == nil {
		t.Fatal("NewQueue with nil eventBus should work")
	}
}

func TestQueueTrackTransfer(t *testing.T) {
	eventBus := events.NewEventBus(100)
	defer eventBus.Close()

	queue := NewQueue(eventBus)

	task := queue.TrackTransfer("upload.dat", 1024, TaskTypeUpload, "/local/path", "folder123")

	if task == nil {
		t.Fatal("TrackTransfer returned nil")
	}
	if task.ID == "" {
		t.Error("Task ID should not be empty")
	}
	if task.Name != "upload.dat" {
		t.Errorf("Expected name 'upload.dat', got %s", task.Name)
	}
	if task.State != TaskQueued {
		t.Errorf("Expected TaskQueued (starts queued), got %v", task.State)
	}

	stats := queue.GetStats()
	if stats.Queued != 1 {
		t.Errorf("Expected 1 queued, got %d", stats.Queued)
	}
}

func TestQueueActivate(t *testing.T) {
	eventBus := events.NewEventBus(100)
	defer eventBus.Close()

	queue := NewQueue(eventBus)

	task := queue.TrackTransfer("upload.dat", 1024, TaskTypeUpload, "/local/path", "folder123")

	// Initially queued
	if task.State != TaskQueued {
		t.Errorf("Expected TaskQueued, got %v", task.State)
	}

	// Activate the task (now sets TaskInitializing)
	queue.Activate(task.ID)

	retrieved, found := queue.GetTask(task.ID)
	if !found {
		t.Fatal("Task not found")
	}
	if retrieved.State != TaskInitializing {
		t.Errorf("Expected TaskInitializing after Activate(), got %v", retrieved.State)
	}
	if retrieved.StartedAt.IsZero() {
		t.Error("StartedAt should be set after Activate()")
	}

	stats := queue.GetStats()
	if stats.Initializing != 1 {
		t.Errorf("Expected 1 initializing, got %d", stats.Initializing)
	}

	// StartTransfer moves to Active
	queue.StartTransfer(task.ID)
	retrieved, _ = queue.GetTask(task.ID)
	if retrieved.State != TaskActive {
		t.Errorf("Expected TaskActive after StartTransfer(), got %v", retrieved.State)
	}

	stats = queue.GetStats()
	if stats.Active != 1 {
		t.Errorf("Expected 1 active, got %d", stats.Active)
	}
}

func TestQueueUpdateProgress(t *testing.T) {
	queue := NewQueue(nil)

	task := queue.TrackTransfer("test.dat", 1000, TaskTypeUpload, "/path", "folder")
	queue.Activate(task.ID) // Must activate before progress updates

	// Update progress
	queue.UpdateProgress(task.ID, 0.5)
	time.Sleep(10 * time.Millisecond) // Allow for timing

	retrieved, found := queue.GetTask(task.ID)
	if !found {
		t.Fatal("Task not found")
	}
	if retrieved.Progress != 0.5 {
		t.Errorf("Expected progress 0.5, got %f", retrieved.Progress)
	}
}

func TestQueueComplete(t *testing.T) {
	queue := NewQueue(nil)

	task := queue.TrackTransfer("test.dat", 1000, TaskTypeUpload, "/path", "folder")
	queue.Activate(task.ID)
	queue.Complete(task.ID)

	retrieved, found := queue.GetTask(task.ID)
	if !found {
		t.Fatal("Task not found")
	}
	if retrieved.State != TaskCompleted {
		t.Errorf("Expected TaskCompleted, got %v", retrieved.State)
	}
	if retrieved.Progress != 1.0 {
		t.Errorf("Expected progress 1.0, got %f", retrieved.Progress)
	}

	stats := queue.GetStats()
	if stats.Completed != 1 {
		t.Errorf("Expected 1 completed, got %d", stats.Completed)
	}
}

func TestQueueFail(t *testing.T) {
	queue := NewQueue(nil)

	task := queue.TrackTransfer("test.dat", 1000, TaskTypeDownload, "file123", "/path")
	queue.Activate(task.ID)
	testErr := errors.New("network error")
	queue.Fail(task.ID, testErr)

	retrieved, found := queue.GetTask(task.ID)
	if !found {
		t.Fatal("Task not found")
	}
	if retrieved.State != TaskFailed {
		t.Errorf("Expected TaskFailed, got %v", retrieved.State)
	}
	if retrieved.Error == nil || retrieved.Error.Error() != "network error" {
		t.Errorf("Expected error 'network error', got %v", retrieved.Error)
	}

	stats := queue.GetStats()
	if stats.Failed != 1 {
		t.Errorf("Expected 1 failed, got %d", stats.Failed)
	}
}

func TestQueueSetCancel(t *testing.T) {
	queue := NewQueue(nil)

	task := queue.TrackTransfer("test.dat", 1000, TaskTypeUpload, "/path", "folder")
	queue.Activate(task.ID) // Must be active to cancel

	ctx, cancel := context.WithCancel(context.Background())
	cancelled := false
	queue.SetCancel(task.ID, func() {
		cancelled = true
		cancel()
	})

	// Cancel through queue
	err := queue.Cancel(task.ID)
	if err != nil {
		t.Errorf("Cancel returned error: %v", err)
	}

	if !cancelled {
		t.Error("Cancel function was not called")
	}

	select {
	case <-ctx.Done():
		// Good
	default:
		t.Error("Context should be cancelled")
	}

	retrieved, _ := queue.GetTask(task.ID)
	if retrieved.State != TaskCancelled {
		t.Errorf("Expected TaskCancelled, got %v", retrieved.State)
	}
}

func TestQueueCancelNonActive(t *testing.T) {
	queue := NewQueue(nil)

	task := queue.TrackTransfer("test.dat", 1000, TaskTypeUpload, "/path", "folder")
	queue.Activate(task.ID)
	queue.Complete(task.ID) // Mark as completed

	err := queue.Cancel(task.ID)
	if err == nil {
		t.Error("Cancel should fail for non-active task")
	}
}

func TestQueueCancelAll(t *testing.T) {
	queue := NewQueue(nil)

	task1 := queue.TrackTransfer("file1.dat", 100, TaskTypeUpload, "/p1", "f")
	task2 := queue.TrackTransfer("file2.dat", 200, TaskTypeUpload, "/p2", "f")
	task3 := queue.TrackTransfer("file3.dat", 300, TaskTypeDownload, "id", "/p3")

	// Activate all tasks
	queue.Activate(task1.ID)
	queue.Activate(task2.ID)
	queue.Activate(task3.ID)

	cancelCount := 0
	queue.SetCancel(task1.ID, func() { cancelCount++ })
	queue.SetCancel(task2.ID, func() { cancelCount++ })
	queue.SetCancel(task3.ID, func() { cancelCount++ })

	queue.CancelAll()

	if cancelCount != 3 {
		t.Errorf("Expected 3 cancel calls, got %d", cancelCount)
	}

	stats := queue.GetStats()
	if stats.Cancelled != 3 {
		t.Errorf("Expected 3 cancelled, got %d", stats.Cancelled)
	}
	if stats.Active != 0 {
		t.Errorf("Expected 0 active, got %d", stats.Active)
	}
}

func TestQueueGetTasks(t *testing.T) {
	queue := NewQueue(nil)

	queue.TrackTransfer("file1.dat", 100, TaskTypeUpload, "/p1", "f")
	queue.TrackTransfer("file2.dat", 200, TaskTypeUpload, "/p2", "f")
	queue.TrackTransfer("file3.dat", 300, TaskTypeDownload, "id", "/p3")

	tasks := queue.GetTasks()
	if len(tasks) != 3 {
		t.Errorf("Expected 3 tasks, got %d", len(tasks))
	}

	// Verify tasks are copies
	tasks[0].Name = "modified"
	original := queue.GetTasks()
	if original[0].Name == "modified" {
		t.Error("GetTasks should return copies, not references")
	}
}

func TestQueueGetTask(t *testing.T) {
	queue := NewQueue(nil)

	task := queue.TrackTransfer("findme.dat", 100, TaskTypeUpload, "/path", "folder")

	retrieved, found := queue.GetTask(task.ID)
	if !found {
		t.Error("GetTask should find existing task")
	}
	if retrieved.Name != "findme.dat" {
		t.Errorf("Expected name 'findme.dat', got %s", retrieved.Name)
	}

	_, found = queue.GetTask("nonexistent")
	if found {
		t.Error("GetTask should not find nonexistent task")
	}
}

func TestQueueClearCompleted(t *testing.T) {
	queue := NewQueue(nil)

	task1 := queue.TrackTransfer("file1.dat", 100, TaskTypeUpload, "/p1", "f")
	task2 := queue.TrackTransfer("file2.dat", 200, TaskTypeUpload, "/p2", "f")

	queue.Activate(task1.ID)
	queue.Activate(task2.ID)
	queue.Complete(task1.ID)
	// task2 still active

	queue.ClearCompleted()

	tasks := queue.GetTasks()
	if len(tasks) != 1 {
		t.Errorf("Expected 1 task after clear, got %d", len(tasks))
	}
	if tasks[0].ID != task2.ID {
		t.Error("Wrong task remaining after clear")
	}
}

// mockRetryExecutor implements RetryExecutor for testing.
// v4.6.6: Added sync.Mutex to protect m.executed from data race — ExecuteRetry
// is called from a goroutine in Queue.Retry, while the test reads m.executed
// from the main goroutine.
type mockRetryExecutor struct {
	mu       sync.Mutex
	executed []*TransferTask
	doneCh   chan struct{} // signals each ExecuteRetry completion
}

func newMockRetryExecutor() *mockRetryExecutor {
	return &mockRetryExecutor{
		doneCh: make(chan struct{}, 10),
	}
}

func (m *mockRetryExecutor) ExecuteRetry(task *TransferTask) {
	m.mu.Lock()
	m.executed = append(m.executed, task)
	m.mu.Unlock()
	m.doneCh <- struct{}{}
}

// waitForExecutions waits for n ExecuteRetry calls with a timeout.
// Returns true if all n calls completed, false on timeout.
func (m *mockRetryExecutor) waitForExecutions(n int, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for i := 0; i < n; i++ {
		select {
		case <-m.doneCh:
		case <-timer.C:
			return false
		}
	}
	return true
}

// getExecuted returns a snapshot of the executed slice under the lock.
func (m *mockRetryExecutor) getExecuted() []*TransferTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*TransferTask, len(m.executed))
	copy(cp, m.executed)
	return cp
}

func TestQueueRetry(t *testing.T) {
	queue := NewQueue(nil)
	executor := newMockRetryExecutor()
	queue.SetRetryExecutor(executor)

	task := queue.TrackTransfer("retry.dat", 100, TaskTypeUpload, "/path", "folder")
	queue.Activate(task.ID)
	queue.Fail(task.ID, errors.New("failed"))

	newID, err := queue.Retry(task.ID)
	if err != nil {
		t.Errorf("Retry returned error: %v", err)
	}
	if newID == "" {
		t.Error("Retry should return new task ID")
	}

	// v4.6.6: Replace time.Sleep with proper synchronization to avoid data race
	if !executor.waitForExecutions(1, 5*time.Second) {
		t.Fatal("Timed out waiting for retry execution")
	}

	executed := executor.getExecuted()
	if len(executed) != 1 {
		t.Errorf("Expected 1 retry execution, got %d", len(executed))
	}
}

func TestQueueRetryNoExecutor(t *testing.T) {
	queue := NewQueue(nil)

	task := queue.TrackTransfer("retry.dat", 100, TaskTypeUpload, "/path", "folder")
	queue.Activate(task.ID)
	queue.Fail(task.ID, errors.New("failed"))

	_, err := queue.Retry(task.ID)
	if err == nil {
		t.Error("Retry without executor should fail")
	}
}

func TestQueueRetryNonFailed(t *testing.T) {
	queue := NewQueue(nil)
	executor := newMockRetryExecutor()
	queue.SetRetryExecutor(executor)

	task := queue.TrackTransfer("active.dat", 100, TaskTypeUpload, "/path", "folder")
	queue.Activate(task.ID)
	// task is now active (not failed)

	_, err := queue.Retry(task.ID)
	if err == nil {
		t.Error("Retry on active task should fail")
	}
}

func TestQueueEvents(t *testing.T) {
	eventBus := events.NewEventBus(100)
	defer eventBus.Close()

	queue := NewQueue(eventBus)

	// Subscribe to transfer events
	queuedCh := eventBus.Subscribe(events.EventTransferQueued)
	initializingCh := eventBus.Subscribe(events.EventTransferInitializing)
	startedCh := eventBus.Subscribe(events.EventTransferStarted)
	progressCh := eventBus.Subscribe(events.EventTransferProgress)
	completedCh := eventBus.Subscribe(events.EventTransferCompleted)

	task := queue.TrackTransfer("event.dat", 100, TaskTypeUpload, "/path", "folder")

	// Check queued event (TrackTransfer publishes Queued)
	select {
	case event := <-queuedCh:
		te, ok := event.(*events.TransferEvent)
		if !ok {
			t.Error("Expected TransferEvent")
		}
		if te.Name != "event.dat" {
			t.Errorf("Expected name 'event.dat', got %s", te.Name)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for queued event")
	}

	// Activate the task (now sets TaskInitializing)
	queue.Activate(task.ID)

	// Check initializing event
	select {
	case event := <-initializingCh:
		te := event.(*events.TransferEvent)
		if te.Name != "event.dat" {
			t.Errorf("Expected name 'event.dat', got %s", te.Name)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for initializing event")
	}

	// StartTransfer moves to Active
	queue.StartTransfer(task.ID)

	// Check started event
	select {
	case event := <-startedCh:
		te := event.(*events.TransferEvent)
		if te.Name != "event.dat" {
			t.Errorf("Expected name 'event.dat', got %s", te.Name)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for started event")
	}

	// Update progress
	queue.UpdateProgress(task.ID, 0.5)

	select {
	case event := <-progressCh:
		te := event.(*events.TransferEvent)
		if te.Progress != 0.5 {
			t.Errorf("Expected progress 0.5, got %f", te.Progress)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for progress event")
	}

	// Complete
	queue.Complete(task.ID)

	select {
	case event := <-completedCh:
		te := event.(*events.TransferEvent)
		if te.Progress != 1.0 {
			t.Errorf("Expected progress 1.0, got %f", te.Progress)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for completed event")
	}
}

func TestQueueStats(t *testing.T) {
	queue := NewQueue(nil)

	// Add tasks, activate, and start transfer (to make them Active)
	task1 := queue.TrackTransfer("q1", 100, TaskTypeUpload, "/p", "f")
	task2 := queue.TrackTransfer("q2", 100, TaskTypeUpload, "/p", "f")
	queue.Activate(task1.ID)
	queue.Activate(task2.ID)
	queue.StartTransfer(task1.ID) // Initializing → Active
	queue.StartTransfer(task2.ID) // Initializing → Active

	task3 := queue.TrackTransfer("cancel", 100, TaskTypeUpload, "/p", "f")
	queue.Activate(task3.ID)
	queue.SetCancel(task3.ID, func() {})
	queue.Cancel(task3.ID) // Works on Initializing tasks now

	task4 := queue.TrackTransfer("complete", 100, TaskTypeUpload, "/p", "f")
	queue.Activate(task4.ID)
	queue.Complete(task4.ID)

	task5 := queue.TrackTransfer("fail", 100, TaskTypeUpload, "/p", "f")
	queue.Activate(task5.ID)
	queue.Fail(task5.ID, errors.New("err"))

	stats := queue.GetStats()

	if stats.Active != 2 {
		t.Errorf("Expected 2 active, got %d", stats.Active)
	}
	if stats.Cancelled != 1 {
		t.Errorf("Expected 1 cancelled, got %d", stats.Cancelled)
	}
	if stats.Completed != 1 {
		t.Errorf("Expected 1 completed, got %d", stats.Completed)
	}
	if stats.Failed != 1 {
		t.Errorf("Expected 1 failed, got %d", stats.Failed)
	}
	if stats.Total() != 5 {
		t.Errorf("Expected total 5, got %d", stats.Total())
	}
}

func TestQueueSpeedCalculation(t *testing.T) {
	queue := NewQueue(nil)

	task := queue.TrackTransfer("speed.dat", 100000, TaskTypeUpload, "/path", "folder") // 100KB for realistic speed calc
	queue.Activate(task.ID)

	// First update
	queue.UpdateProgress(task.ID, 0.1)
	time.Sleep(400 * time.Millisecond) // v3.6.3: Must be > 300ms for speed calculation threshold

	// Second update - should calculate speed
	queue.UpdateProgress(task.ID, 0.2)

	retrieved, _ := queue.GetTask(task.ID)
	if retrieved.Speed == 0 {
		t.Error("Speed should be calculated after progress updates")
	}
}
