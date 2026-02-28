// Package transfer provides transfer queue management for uploads and downloads.
// v3.6.3: Added task types and queue for GUI transfers tab.
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
	SourceLabel string // v4.7.4: Origin context ("PUR", "SingleJob", "FileBrowser")
	BatchID     string // v4.7.7: Groups related transfers for bulk display
	BatchLabel  string // v4.7.7: Display name for the batch (folder name, etc.)

	// State tracking
	State    TaskState // Current state
	Progress float64   // 0.0 to 1.0
	Speed    float64   // bytes/sec (smoothed with EMA)
	Error    error     // Error if failed

	// Speed calculation internals (for EMA smoothing)
	lastBytes      int64     // Bytes transferred at last update
	lastUpdateTime time.Time // Time of last update

	// Timestamps
	CreatedAt   time.Time // When task was enqueued
	StartedAt   time.Time // When task started executing
	CompletedAt time.Time // When task completed/failed/cancelled

	// Internal
	mu     sync.RWMutex        // Protects all fields
	ctx    context.Context     // For cancellation
	cancel context.CancelFunc  // Cancel function
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
// v4.7.4: Added for transfer origin tracking (PUR, SingleJob, FileBrowser).
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
