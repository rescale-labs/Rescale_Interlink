// Package transfer provides transfer queue management for uploads and downloads.
package transfer

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TaskType indicates whether a task is an upload or download.
type TaskType string

const (
	TaskTypeUpload   TaskType = "upload"
	TaskTypeDownload TaskType = "download"
)

// TaskState represents the current state of a transfer task.
type TaskState string

const (
	TaskQueued       TaskState = "queued"       // Waiting in queue for semaphore slot
	TaskInitializing TaskState = "initializing" // Acquired slot, initializing upload/download session
	TaskActive       TaskState = "active"       // Actually transferring bytes
	TaskPaused       TaskState = "paused"       // Paused by user
	TaskCompleted    TaskState = "completed"    // Successfully completed
	TaskFailed       TaskState = "failed"       // Failed with error
	TaskCancelled    TaskState = "cancelled"    // Cancelled by user
)

// TransferTask represents a single upload or download task in the queue.
// Thread-safe: Use the provided methods to update state.
type TransferTask struct {
	ID   string   // Unique task ID
	Type TaskType // Upload or download

	// Source and destination
	Name        string // Display name (filename)
	Source      string // Local path (upload) or remote file ID (download)
	Dest        string // Remote folder ID (upload) or local path (download)
	Size        int64  // File size in bytes
	SourceLabel string // Origin context ("PUR", "SingleJob", "FileBrowser")
	BatchID     string // Groups related transfers for bulk display
	BatchLabel  string // Display name for the batch (folder name, etc.)

	// Tags to apply after a successful upload. Held on the task so a retry
	// re-applies them — the retry executor only has the task, not the original
	// request, and dropping them here loses the tags silently.
	Tags []string

	// State tracking
	State    TaskState // Current state
	Progress float64   // 0.0 to 1.0
	Speed    float64   // bytes/sec (smoothed with EMA)
	Error    error     // Error if failed

	// Speed calculation internals (for EMA smoothing)
	lastBytes      int64     // Bytes transferred at last update
	lastUpdateTime time.Time // Time of last update

	// Batch-level byte tracking
	lastBatchBytes int64 // Bytes already counted toward batch total

	// Timestamps
	CreatedAt   time.Time // When task was enqueued
	StartedAt   time.Time // When task started executing
	CompletedAt time.Time // When task completed/failed/cancelled

	// Internal
	mu     sync.RWMutex       // Protects all fields
	ctx    context.Context    // For cancellation
	cancel context.CancelFunc // Cancel function
}

// NewTransferTask creates a new transfer task with the given parameters.
// The task starts in TaskQueued state.
func NewTransferTask(taskType TaskType, name, source, dest string, size int64) *TransferTask {
	ctx, cancel := context.WithCancel(context.Background())
	return &TransferTask{
		ID:        generateTaskID(),
		Type:      taskType,
		Name:      name,
		Source:    source,
		Dest:      dest,
		Size:      size,
		State:     TaskQueued,
		Progress:  0.0,
		Speed:     0.0,
		CreatedAt: time.Now(),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// NewTransferTaskWithLabel creates a new transfer task with a source label.
func NewTransferTaskWithLabel(taskType TaskType, name, source, dest string, size int64, sourceLabel string) *TransferTask {
	task := NewTransferTask(taskType, name, source, dest, size)
	task.SourceLabel = sourceLabel
	return task
}

// GetState returns the current state (thread-safe).
func (t *TransferTask) GetState() TaskState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.State
}

// SetState updates the task state (thread-safe).
func (t *TransferTask) SetState(state TaskState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.State = state
	if state == TaskActive && t.StartedAt.IsZero() {
		t.StartedAt = time.Now()
	}
	if state == TaskCompleted || state == TaskFailed || state == TaskCancelled {
		t.CompletedAt = time.Now()
	}
}

// GetProgress returns current progress (thread-safe).
func (t *TransferTask) GetProgress() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Progress
}

// UpdateProgress updates progress and speed (thread-safe).
// Deprecated: Use UpdateProgressWithBytes for proper EMA speed calculation.
func (t *TransferTask) UpdateProgress(progress float64, speed float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Progress = progress
	t.Speed = speed
}

// UpdateProgressWithBytes updates progress and calculates speed using EMA.
// This matches the proven approach from file_browser_tab.go for smooth, responsive speed display.
// bytesTransferred: total bytes transferred so far
// totalBytes: total file size
func (t *TransferTask) UpdateProgressWithBytes(bytesTransferred, totalBytes int64) {
	if totalBytes <= 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	progress := float64(bytesTransferred) / float64(totalBytes)
	t.Progress = progress

	// Reset start time on first real progress
	if t.lastBytes == 0 && bytesTransferred > 0 {
		t.StartedAt = now
		t.lastUpdateTime = now
		t.lastBytes = bytesTransferred
		t.Speed = 0
		return
	}

	// Calculate instantaneous rate using delta since last update
	// Only calculate if we have a previous data point and enough time has passed
	if t.lastBytes > 0 && bytesTransferred > t.lastBytes {
		elapsed := now.Sub(t.lastUpdateTime).Seconds()
		if elapsed > 0.1 { // Need at least 100ms between updates for meaningful rate
			bytesDelta := bytesTransferred - t.lastBytes
			instantRate := float64(bytesDelta) / elapsed

			// EMA smoothing (alpha=0.25): 25% weight to new value, 75% to previous
			// This provides smooth display while remaining responsive to speed changes
			const speedSmoothingAlpha = 0.25
			if t.Speed > 0 {
				t.Speed = speedSmoothingAlpha*instantRate + (1-speedSmoothingAlpha)*t.Speed
			} else {
				t.Speed = instantRate
			}

			t.lastBytes = bytesTransferred
			t.lastUpdateTime = now
		}
	}
}

// GetSpeed returns current transfer speed in bytes/sec (thread-safe).
func (t *TransferTask) GetSpeed() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Speed
}

// GetSize returns the total size in bytes (thread-safe).
// Size is mutable — the queue updates it when the caller didn't know the size
// at track time (pipeline uploads) — so readers outside q.mu must use this.
func (t *TransferTask) GetSize() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Size
}

// --- Queue-side mutators ---
//
// The Queue calls these while holding q.mu, and they take t.mu themselves.
// Two lock disciplines therefore both hold: every mutation of a task field is
// made under q.mu (so queue readers holding q.mu.RLock are safe) AND under
// t.mu (so readers that hold only t.mu — Clone, GetState, GetProgress,
// publishTransferEvent — are safe). Mutating a task field directly from the
// queue satisfies only the first and races with the second.
//
// Lock order is always q.mu → t.mu. No task method acquires q.mu, so this
// cannot deadlock.

// isTerminalLocked reports whether the task reached a terminal state.
// Caller must hold t.mu.
func (t *TransferTask) isTerminalLocked() bool {
	return t.State == TaskCompleted || t.State == TaskFailed || t.State == TaskCancelled
}

// activate transitions a queued task to initializing and stamps StartedAt.
// Returns false when the task was not queued (already claimed or terminal).
func (t *TransferTask) activate() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.State != TaskQueued {
		return false
	}
	t.State = TaskInitializing
	t.StartedAt = time.Now()
	return true
}

// beginTransfer transitions an initializing task to active (bytes are moving).
// Returns false when the task is in any other state, making the call idempotent.
func (t *TransferTask) beginTransfer() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.State != TaskInitializing {
		return false
	}
	t.State = TaskActive
	return true
}

// setSize updates the total size.
func (t *TransferTask) setSize(size int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Size = size
}

// setTags records the tags to apply after a successful upload.
func (t *TransferTask) setTags(tags []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Tags = tags
}

// GetTags returns the tags to apply after a successful upload (thread-safe).
func (t *TransferTask) GetTags() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Tags
}

// failIfNotTerminal records a failure on a non-terminal task.
// Returns false when the task was already terminal — a cancelled task must not
// be overwritten by a late error from the transfer it was cancelling.
func (t *TransferTask) failIfNotTerminal(err error) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isTerminalLocked() {
		return false
	}
	t.State = TaskFailed
	t.Error = err
	t.CompletedAt = time.Now()
	return true
}

// cancelIfNotTerminal marks a non-terminal task cancelled.
// Returns false when the task reached a terminal state on its own first.
func (t *TransferTask) cancelIfNotTerminal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isTerminalLocked() {
		return false
	}
	t.State = TaskCancelled
	t.CompletedAt = time.Now()
	return true
}

// completeIfNotTerminal marks a non-terminal task completed and returns the
// bytes not yet counted toward its batch byte total. ok=false when the task was
// already terminal, so a transfer that finished after the user cancelled it
// stays cancelled instead of flipping to completed.
func (t *TransferTask) completeIfNotTerminal() (batchDelta int64, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isTerminalLocked() {
		return 0, false
	}
	t.State = TaskCompleted
	t.Progress = 1.0
	t.CompletedAt = time.Now()

	if t.BatchID == "" || t.Size <= 0 {
		return 0, true
	}
	remaining := t.Size - t.lastBatchBytes
	if remaining <= 0 {
		return 0, true
	}
	t.lastBatchBytes = t.Size
	return remaining, true
}

// applyProgress records a progress sample, updates the smoothed speed, and
// returns the byte delta to add to the task's batch-level byte counter
// (0 when the task is unbatched, sized 0, or hasn't advanced).
func (t *TransferTask) applyProgress(progress float64) (batchDelta int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(t.lastUpdateTime).Seconds()

	// Only calculate speed if:
	// 1. At least 0.3 seconds elapsed (avoid noisy samples)
	// 2. Progress actually increased (ignore backwards jumps)
	// 3. Byte delta is meaningful (> 100KB) — uses bytes instead of progress fraction
	//    so the threshold works for both small and very large files.
	//    (A fraction threshold fails for very large files where the delta between
	//    progress callbacks is smaller than the fraction cutoff.)
	progressDelta := progress - t.Progress
	bytesTransferred := progressDelta * float64(t.Size)
	if elapsed >= 0.3 && progressDelta > 0 && bytesTransferred > 100*1024 {
		instantSpeed := bytesTransferred / elapsed

		// Sanity check: clamp to reasonable range (1 KB/s to 1 GB/s)
		if instantSpeed < 1024 {
			instantSpeed = 0 // Ignore tiny speeds
		} else if instantSpeed > 1024*1024*1024 {
			instantSpeed = t.Speed // Keep previous if absurdly high
		}

		if instantSpeed > 0 {
			// EMA with alpha=0.1 for smoother updates
			if t.Speed == 0 {
				t.Speed = instantSpeed
			} else {
				t.Speed = 0.1*instantSpeed + 0.9*t.Speed
			}
		}
	}

	t.Progress = progress
	t.lastUpdateTime = now

	if t.BatchID == "" || t.Size <= 0 {
		return 0
	}
	taskBytes := int64(progress * float64(t.Size))
	delta := taskBytes - t.lastBatchBytes
	if delta <= 0 {
		return 0
	}
	t.lastBatchBytes = taskBytes
	return delta
}

// resetForRetry clears per-attempt state so the task can run again.
// Identity and immutable descriptors (ID, Type, Name, Source, Dest, Size,
// CreatedAt) are preserved so the queue keeps one entry per file.
func (t *TransferTask) resetForRetry() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.State = TaskQueued
	t.Progress = 0.0
	t.Speed = 0.0
	t.Error = nil
	t.StartedAt = time.Time{}
	t.CompletedAt = time.Time{}
	t.lastBytes = 0
	t.lastUpdateTime = time.Time{}
	t.lastBatchBytes = 0
}

// SetError sets the error and changes state to TaskFailed (thread-safe).
func (t *TransferTask) SetError(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Error = err
	t.State = TaskFailed
	t.CompletedAt = time.Now()
}

// GetError returns the error if any (thread-safe).
func (t *TransferTask) GetError() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Error
}

// Cancel cancels this task's context.
func (t *TransferTask) Cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
	if t.State == TaskQueued || t.State == TaskActive || t.State == TaskPaused {
		t.State = TaskCancelled
		t.CompletedAt = time.Now()
	}
}

// Context returns the task's context for cancellation checking.
func (t *TransferTask) Context() context.Context {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ctx
}

// Clone returns a shallow copy of the task (for safe external use).
// The copy shares the same context but has independent state.
func (t *TransferTask) Clone() TransferTask {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return TransferTask{
		ID:          t.ID,
		Type:        t.Type,
		Name:        t.Name,
		Source:      t.Source,
		Dest:        t.Dest,
		Size:        t.Size,
		SourceLabel: t.SourceLabel,
		BatchID:     t.BatchID,
		BatchLabel:  t.BatchLabel,
		Tags:        t.Tags,
		State:       t.State,
		Progress:    t.Progress,
		Speed:       t.Speed,
		Error:       t.Error,
		CreatedAt:   t.CreatedAt,
		StartedAt:   t.StartedAt,
		CompletedAt: t.CompletedAt,
	}
}

// IsTerminal returns true if the task is in a terminal state
// (completed, failed, or cancelled).
func (t *TransferTask) IsTerminal() bool {
	state := t.GetState()
	return state == TaskCompleted || state == TaskFailed || state == TaskCancelled
}

// CanRetry returns true if the task can be retried (failed or cancelled).
func (t *TransferTask) CanRetry() bool {
	state := t.GetState()
	return state == TaskFailed || state == TaskCancelled
}

// ID generation
var (
	taskCounter uint64
	taskMu      sync.Mutex
)

func generateTaskID() string {
	taskMu.Lock()
	defer taskMu.Unlock()
	taskCounter++
	return fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), taskCounter)
}
