// Package transfer provides transfer queue management for uploads and downloads.
// The queue tracks task state and publishes events - execution is handled by callers.
package transfer

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/events"
)

// RetryExecutor is implemented by components that can retry failed transfers.
// The queue calls ExecuteRetry when a user requests retry on a failed task.
type RetryExecutor interface {
	// ExecuteRetry starts execution of a retry task.
	// The task is already tracked in the queue with state TaskQueued.
	// The executor should call queue.SetCancel(), UpdateProgress(), Complete()/Fail().
	ExecuteRetry(task *TransferTask)
}

// QueueStats holds statistics about the transfer queue.
type QueueStats struct {
	Queued       int
	Initializing int
	Active       int
	Paused       int
	Completed    int
	Failed       int
	Cancelled    int
}

// Total returns total number of tasks in queue.
func (s QueueStats) Total() int {
	return s.Queued + s.Initializing + s.Active + s.Paused + s.Completed + s.Failed + s.Cancelled
}

// Queue is a passive transfer tracker that publishes events for UI updates.
// It does NOT execute transfers - that is handled by the caller (e.g., FileBrowserTab).
//
// Architecture:
//   - Queue OBSERVES transfers, does not execute them
//   - Caller registers tasks via TrackTransfer()
//   - Caller updates progress via UpdateProgress()
//   - Caller marks completion via Complete()/Fail()
//   - Queue stores cancel functions and calls them on Cancel()
//   - Queue calls RetryExecutor for Retry requests
//   - Queue publishes events for TransfersTab to display
type Queue struct {
	// Task storage
	tasks     []*TransferTask            // All tasks in creation order
	tasksByID map[string]*TransferTask   // Index by ID for quick lookup
	mu        sync.RWMutex

	// Cancel functions for active tasks
	cancelFuncs map[string]context.CancelFunc

	// Retry executor (set by GUI to handle retry requests)
	retryExecutor RetryExecutor

	// Event publishing
	eventBus *events.EventBus

	// Batch progress ticker
	batchTickerRunning bool

	// Streaming batch support
	batchCancelFuncs    map[string]context.CancelFunc // Cancel functions for streaming batches
	batchScanInProgress map[string]bool               // True while scan is still discovering files

	// Pre-registered batches visible before first task is discovered
	preRegisteredBatches map[string]*BatchStats

	// Batch-level speed/ETA tracking
	batchBytesTransferred map[string]int64       // cumulative bytes per batch
	batchSpeedWindows     map[string]*speedWindow // 10s sliding window (bytes)
	batchFilesCompleted   map[string]int64       // cumulative completed files per batch
	batchFileRateWindows  map[string]*speedWindow // 10s sliding window (files)
	batchDiscoveredTotal  map[string]int         // total files discovered (may exceed registered tasks)
	batchDiscoveredBytes  map[string]int64       // total bytes discovered
	batchPrevETA          map[string]float64     // smoothed ETA state
	batchLastETA          map[string]float64     // last computed ETA (for polling DTO)
	batchStartedAt        map[string]time.Time   // batch start time for elapsed display
}

// NewQueue creates a new transfer queue with the specified event bus.
// The queue is immediately ready to track tasks - no Start() needed.
func NewQueue(eventBus *events.EventBus) *Queue {
	return &Queue{
		tasks:                 make([]*TransferTask, 0),
		tasksByID:             make(map[string]*TransferTask),
		cancelFuncs:           make(map[string]context.CancelFunc),
		batchCancelFuncs:      make(map[string]context.CancelFunc),
		batchScanInProgress:   make(map[string]bool),
		preRegisteredBatches:  make(map[string]*BatchStats),
		batchBytesTransferred: make(map[string]int64),
		batchSpeedWindows:     make(map[string]*speedWindow),
		batchFilesCompleted:   make(map[string]int64),
		batchFileRateWindows:  make(map[string]*speedWindow),
		batchDiscoveredTotal:  make(map[string]int),
		batchDiscoveredBytes:  make(map[string]int64),
		batchPrevETA:          make(map[string]float64),
		batchLastETA:          make(map[string]float64),
		batchStartedAt:        make(map[string]time.Time),
		eventBus:              eventBus,
	}
}

// SetRetryExecutor sets the executor that handles retry requests.
// Must be called before Retry() can work.
func (q *Queue) SetRetryExecutor(executor RetryExecutor) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.retryExecutor = executor
}

// TrackTransfer registers a new transfer that will be executed elsewhere.
// The task starts in TaskQueued state. Call Activate() when the transfer
// actually starts (e.g., after acquiring a semaphore slot).
//
// Parameters:
//   - name: Display name (usually filename)
//   - size: File size in bytes
//   - taskType: TaskTypeUpload or TaskTypeDownload
//   - source: Source path (local path for upload, file ID for download)
//   - dest: Destination (folder ID for upload, local path for download)
//
// Returns the created task with a unique ID.
func (q *Queue) TrackTransfer(name string, size int64, taskType TaskType, source, dest string) *TransferTask {
	task := NewTransferTask(taskType, name, source, dest, size)
	task.State = TaskQueued // Starts as queued, call Activate() when actually running

	q.mu.Lock()
	q.tasks = append(q.tasks, task)
	q.tasksByID[task.ID] = task
	q.mu.Unlock()

	// Publish queued event
	q.publishTransferEvent(events.EventTransferQueued, task)

	return task
}

// TrackTransferWithLabel registers a new transfer with a source label.
func (q *Queue) TrackTransferWithLabel(name string, size int64, taskType TaskType, source, dest, sourceLabel string) *TransferTask {
	task := q.TrackTransfer(name, size, taskType, source, dest)
	task.SourceLabel = sourceLabel
	return task
}

// TrackTransferWithBatch registers a new transfer with source label and batch info.
func (q *Queue) TrackTransferWithBatch(name string, size int64, taskType TaskType, source, dest, sourceLabel, batchID, batchLabel string) *TransferTask {
	task := q.TrackTransferWithLabel(name, size, taskType, source, dest, sourceLabel)
	task.BatchID = batchID
	task.BatchLabel = batchLabel

	// Stamp start time if not already set (non-streaming batches skip PreRegisterBatch).
	if batchID != "" {
		q.mu.Lock()
		if _, exists := q.batchStartedAt[batchID]; !exists {
			q.batchStartedAt[batchID] = time.Now()
		}
		q.mu.Unlock()
	}

	// Start batch progress ticker if this is the first batched task
	if batchID != "" {
		q.ensureBatchTicker()
	}

	return task
}

// Activate atomically transitions a queued task to initializing when it acquires a semaphore slot.
// Returns true if the transition succeeded (task was in TaskQueued state), false otherwise.
// Call this after acquiring a semaphore slot, BEFORE the actual transfer begins.
// The task will transition to Active when StartTransfer() is called (i.e., when bytes start moving).
func (q *Queue) Activate(taskID string) bool {
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if !exists || task == nil || task.State != TaskQueued {
		q.mu.Unlock()
		return false
	}
	task.State = TaskInitializing
	task.StartedAt = time.Now()
	q.mu.Unlock()

	q.publishTransferEvent(events.EventTransferInitializing, task)
	return true
}

// StartTransfer marks an initializing task as actively transferring.
// Call this when the first progress callback fires (i.e., bytes are actually moving).
// Idempotent: only transitions from TaskInitializing to TaskActive.
// Capture transition decision inside lock to prevent data race on shouldPublish.
func (q *Queue) StartTransfer(taskID string) {
	var shouldPublish bool
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if exists && task != nil && task.State == TaskInitializing {
		task.State = TaskActive
		shouldPublish = true
	}
	q.mu.Unlock()

	if shouldPublish {
		q.publishTransferEvent(events.EventTransferStarted, task)
	}
}

// SetCancel stores the cancel function for an active task.
// Call this after creating context.WithCancel() for the transfer.
func (q *Queue) SetCancel(taskID string, cancelFn context.CancelFunc) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelFuncs[taskID] = cancelFn
}

// ClearCancel removes a stale cancel fn entry for a task.
// Used on early-return paths where the task is already terminal (e.g., cancelled by CancelBatch).
func (q *Queue) ClearCancel(taskID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.cancelFuncs, taskID)
}

// FailIfNotTerminal atomically checks if a task is non-terminal and transitions to Failed.
// Returns true if the transition happened, false if the task was already terminal or not found.
// Avoids TOCTOU race between IsTerminal check and Fail call. Used on cancel/error paths
// after SetCancel() where CancelBatch may have already set the task to TaskCancelled.
func (q *Queue) FailIfNotTerminal(taskID string, err error) bool {
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if !exists || task == nil || task.IsTerminal() {
		delete(q.cancelFuncs, taskID) // Cleanup
		q.mu.Unlock()
		return false
	}
	task.State = TaskFailed
	task.Error = err
	task.CompletedAt = time.Now()
	delete(q.cancelFuncs, taskID)
	q.mu.Unlock()
	q.publishTransferEvent(events.EventTransferFailed, task)
	return true
}

// UpdateSize updates a task's total size. Used when the size isn't known at
// track time (e.g., pipeline uploads where the caller doesn't pass size).
func (q *Queue) UpdateSize(taskID string, size int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if task, ok := q.tasksByID[taskID]; ok && task != nil {
		task.Size = size
	}
}

// UpdateProgress updates a task's progress.
// Progress should be 0.0 to 1.0.
// Speed is calculated automatically using smoothed EMA.
// Lock is held for the entire operation to protect all task field updates
// (Progress, Speed, lastUpdateTime) from concurrent access.
func (q *Queue) UpdateProgress(taskID string, progress float64) {
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if !exists || task == nil {
		q.mu.Unlock()
		return
	}

	now := time.Now()
	elapsed := now.Sub(task.lastUpdateTime).Seconds()

	// Only calculate speed if:
	// 1. At least 0.3 seconds elapsed (avoid noisy samples)
	// 2. Progress actually increased (ignore backwards jumps)
	// 3. Byte delta is meaningful (> 100KB) — uses bytes instead of progress fraction
	//    so the threshold works for both small and very large files.
	//    (A fraction threshold fails for very large files where the delta between
	//    progress callbacks is smaller than the fraction cutoff.)
	progressDelta := progress - task.Progress
	bytesTransferred := progressDelta * float64(task.Size)
	if elapsed >= 0.3 && progressDelta > 0 && bytesTransferred > 100*1024 {
		instantSpeed := bytesTransferred / elapsed

		// Sanity check: clamp to reasonable range (1 KB/s to 1 GB/s)
		if instantSpeed < 1024 {
			instantSpeed = 0 // Ignore tiny speeds
		} else if instantSpeed > 1024*1024*1024 {
			instantSpeed = task.Speed // Keep previous if absurdly high
		}

		if instantSpeed > 0 {
			// EMA with alpha=0.1 for smoother updates (was 0.25)
			if task.Speed == 0 {
				task.Speed = instantSpeed
			} else {
				task.Speed = 0.1*instantSpeed + 0.9*task.Speed
			}
		}
	}

	task.Progress = progress
	task.lastUpdateTime = now

	// Track cumulative bytes transferred for batch-level speed window
	if task.BatchID != "" && task.Size > 0 {
		taskBytes := int64(progress * float64(task.Size))
		delta := taskBytes - task.lastBatchBytes
		if delta > 0 {
			task.lastBatchBytes = taskBytes
			q.batchBytesTransferred[task.BatchID] += delta
		}
	}

	q.mu.Unlock()

	// Publish progress event (outside lock to avoid holding lock during event dispatch)
	q.publishTransferEvent(events.EventTransferProgress, task)
}

// Complete marks a task as successfully completed.
func (q *Queue) Complete(taskID string) {
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if exists && task != nil {
		task.State = TaskCompleted
		task.Progress = 1.0
		task.CompletedAt = time.Now()

		// Account for remaining bytes not yet counted toward batch total
		if task.BatchID != "" && task.Size > 0 {
			remaining := task.Size - task.lastBatchBytes
			if remaining > 0 {
				task.lastBatchBytes = task.Size
				q.batchBytesTransferred[task.BatchID] += remaining
			}
		}
		// Track completed file count for files/sec window
		if task.BatchID != "" {
			q.batchFilesCompleted[task.BatchID]++
		}
	}
	delete(q.cancelFuncs, taskID) // Clean up cancel function
	q.mu.Unlock()

	if exists && task != nil {
		q.publishTransferEvent(events.EventTransferCompleted, task)
	}
}

// Fail marks a task as failed with an error.
func (q *Queue) Fail(taskID string, err error) {
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if exists && task != nil {
		task.State = TaskFailed
		task.Error = err
		task.CompletedAt = time.Now()
	}
	delete(q.cancelFuncs, taskID) // Clean up cancel function
	q.mu.Unlock()

	if exists && task != nil {
		q.publishTransferEvent(events.EventTransferFailed, task)
	}
}

// Cancel cancels an active, initializing, or queued task by calling its stored cancel function.
// Check+mutate merged into one critical section to prevent TOCTOU race.
func (q *Queue) Cancel(taskID string) error {
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if !exists || task == nil {
		q.mu.Unlock()
		return errors.New("task not found")
	}
	if task.State != TaskActive && task.State != TaskInitializing && task.State != TaskQueued {
		q.mu.Unlock()
		return errors.New("task is not cancellable")
	}
	cancelFn := q.cancelFuncs[taskID]
	task.State = TaskCancelled
	task.CompletedAt = time.Now()
	delete(q.cancelFuncs, taskID)
	q.mu.Unlock()

	if cancelFn != nil {
		cancelFn()
	}
	q.publishTransferEvent(events.EventTransferCancelled, task)
	return nil
}

// CancelAll cancels all active, initializing, and queued tasks.
func (q *Queue) CancelAll() {
	q.mu.Lock()
	tasksToCancel := make([]*TransferTask, 0)
	cancelFns := make([]context.CancelFunc, 0)

	for _, task := range q.tasks {
		if task.State == TaskActive || task.State == TaskInitializing || task.State == TaskQueued {
			tasksToCancel = append(tasksToCancel, task)
			if fn := q.cancelFuncs[task.ID]; fn != nil {
				cancelFns = append(cancelFns, fn)
			}
		}
	}
	q.mu.Unlock()

	// Call all cancel functions (outside lock — cancelFn may call Complete/Fail)
	for _, fn := range cancelFns {
		fn()
	}

	// Re-acquire lock and only transition tasks that haven't gone terminal
	var actuallyCancelled []*TransferTask
	q.mu.Lock()
	for _, task := range tasksToCancel {
		if !task.IsTerminal() {
			task.State = TaskCancelled
			task.CompletedAt = time.Now()
			actuallyCancelled = append(actuallyCancelled, task)
		}
		delete(q.cancelFuncs, task.ID)
	}
	q.mu.Unlock()

	for _, task := range actuallyCancelled {
		q.publishTransferEvent(events.EventTransferCancelled, task)
	}
}

// Retry resets a failed or cancelled task and re-queues it for execution.
// Reuses the same task entry instead of creating a duplicate.
// Returns the same task ID (not a new one).
func (q *Queue) Retry(taskID string) (string, error) {
	q.mu.Lock()
	originalTask, exists := q.tasksByID[taskID]
	executor := q.retryExecutor
	q.mu.Unlock()

	if !exists || originalTask == nil {
		return "", errors.New("task not found")
	}

	if !originalTask.CanRetry() {
		return "", errors.New("task cannot be retried")
	}

	if executor == nil {
		return "", errors.New("no retry executor configured")
	}

	// Reset the existing task instead of creating a new one,
	// keeping a single entry in the queue instead of duplicates.
	originalTask.mu.Lock()
	originalTask.State = TaskQueued
	originalTask.Progress = 0.0
	originalTask.Speed = 0.0
	originalTask.Error = nil
	originalTask.StartedAt = time.Time{}
	originalTask.CompletedAt = time.Time{}
	originalTask.lastBytes = 0
	originalTask.lastUpdateTime = time.Time{}
	originalTask.lastBatchBytes = 0
	// Note: Keep ID, Type, Name, Source, Dest, Size, CreatedAt unchanged
	originalTask.mu.Unlock()

	q.publishTransferEvent(events.EventTransferQueued, originalTask)

	// Execute retry via executor (in goroutine to not block)
	go executor.ExecuteRetry(originalTask)

	return taskID, nil
}

// ClearCompleted removes all completed/failed/cancelled tasks from the queue.
func (q *Queue) ClearCompleted() {
	q.mu.Lock()
	defer q.mu.Unlock()

	filtered := make([]*TransferTask, 0, len(q.tasks))
	for _, task := range q.tasks {
		if !task.IsTerminal() {
			filtered = append(filtered, task)
		} else {
			delete(q.tasksByID, task.ID)
		}
	}
	q.tasks = filtered
}

// GetStats returns current queue statistics.
func (q *Queue) GetStats() QueueStats {
	q.mu.RLock()
	defer q.mu.RUnlock()

	stats := QueueStats{}
	for _, task := range q.tasks {
		switch task.GetState() {
		case TaskQueued:
			stats.Queued++
		case TaskInitializing:
			stats.Initializing++
		case TaskActive:
			stats.Active++
		case TaskPaused:
			stats.Paused++
		case TaskCompleted:
			stats.Completed++
		case TaskFailed:
			stats.Failed++
		case TaskCancelled:
			stats.Cancelled++
		}
	}
	return stats
}

// GetTasks returns a copy of all tasks for display.
func (q *Queue) GetTasks() []TransferTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]TransferTask, len(q.tasks))
	for i, task := range q.tasks {
		result[i] = task.Clone()
	}
	return result
}

// GetTask returns a copy of a specific task by ID.
func (q *Queue) GetTask(taskID string) (TransferTask, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	task, exists := q.tasksByID[taskID]
	if !exists || task == nil {
		return TransferTask{}, false
	}
	return task.Clone(), true
}

// publishTransferEvent publishes a transfer event to the event bus.
// Suppresses progress events for batched tasks to reduce event flood;
// terminal events (completed, failed, cancelled) are always published.
func (q *Queue) publishTransferEvent(eventType events.EventType, task *TransferTask) {
	if q.eventBus == nil {
		return
	}

	// Skip individual progress events for batched tasks —
	// the batch progress ticker publishes aggregate events instead.
	if eventType == events.EventTransferProgress && task.BatchID != "" {
		return
	}

	event := &events.TransferEvent{
		BaseEvent: events.BaseEvent{
			EventType: eventType,
			Time:      time.Now(),
		},
		TaskID:   task.ID,
		TaskType: string(task.Type),
		Name:     task.Name,
		Size:     task.Size,
		Progress: task.GetProgress(),
		Speed:    task.GetSpeed(),
		Error:    task.GetError(),
	}
	q.eventBus.Publish(event)
}

// BatchStats holds aggregate stats for a batch of transfers.
type BatchStats struct {
	BatchID         string
	BatchLabel      string
	Direction       string // "upload" or "download"
	SourceLabel     string
	Total           int
	Queued          int
	Active          int
	Completed       int
	Failed          int
	Cancelled       int
	TotalBytes      int64
	Progress        float64 // byte-weighted 0.0-1.0
	Speed           float64 // aggregate bytes/sec
	TotalKnown      bool    // True when scan is complete and Total is final
	FilesPerSec     float64 // file completion rate (windowed)
	ETASeconds      float64 // estimated time remaining (smoothed, -1 = unknown)
	DiscoveredTotal int       // files discovered by scan (may > Total during queueing)
	DiscoveredBytes int64     // bytes discovered by scan
	StartedAt       time.Time // batch start time for elapsed display
}

// GetAllBatchStats returns aggregate stats for all batches in a single pass.
// O(tasks) scan, returns one BatchStats per distinct BatchID.
func (q *Queue) GetAllBatchStats() []BatchStats {
	q.mu.RLock()
	defer q.mu.RUnlock()

	batchMap := make(map[string]*BatchStats)
	var batchOrder []string // Preserve insertion order

	for _, task := range q.tasks {
		if task.BatchID == "" {
			continue
		}

		bs, exists := batchMap[task.BatchID]
		if !exists {
			// TotalKnown = true when scan NOT in progress
			// Default: batches not in batchScanInProgress map have TotalKnown=true
			bs = &BatchStats{
				BatchID:     task.BatchID,
				BatchLabel:  task.BatchLabel,
				Direction:   string(task.Type),
				SourceLabel: task.SourceLabel,
				TotalKnown:  !q.batchScanInProgress[task.BatchID],
			}
			batchMap[task.BatchID] = bs
			batchOrder = append(batchOrder, task.BatchID)
		}

		bs.Total++
		bs.TotalBytes += task.Size

		state := task.GetState()
		switch state {
		case TaskQueued:
			bs.Queued++
		case TaskInitializing:
			bs.Active++ // initializing tasks have a semaphore slot and are doing real work
		case TaskActive:
			bs.Active++
		case TaskCompleted:
			bs.Completed++
		case TaskFailed:
			bs.Failed++
		case TaskCancelled:
			bs.Cancelled++
		}
	}

	// Compute batch speed from sliding windows
	for batchID, bs := range batchMap {
		if window, exists := q.batchSpeedWindows[batchID]; exists {
			bs.Speed = window.Speed()
		}
		if window, exists := q.batchFileRateWindows[batchID]; exists {
			bs.FilesPerSec = window.Speed()
		}
		if dt, exists := q.batchDiscoveredTotal[batchID]; exists {
			bs.DiscoveredTotal = dt
		}
		if db, exists := q.batchDiscoveredBytes[batchID]; exists {
			bs.DiscoveredBytes = db
		}
		if t, exists := q.batchStartedAt[batchID]; exists {
			bs.StartedAt = t
		}
		// Return last ticker-computed ETA so polling DTO doesn't zero it out
		if eta, exists := q.batchLastETA[batchID]; exists {
			bs.ETASeconds = eta
		}
	}

	// Include pre-registered batches that have no tasks yet
	for batchID, preBatch := range q.preRegisteredBatches {
		if _, exists := batchMap[batchID]; !exists {
			batchMap[batchID] = preBatch
			batchOrder = append(batchOrder, batchID)
		}
	}

	// Compute byte-weighted progress. Use DiscoveredBytes as denominator when
	// available, since registered tasks (TotalBytes) may lag behind discovery
	// during streaming uploads.
	result := make([]BatchStats, 0, len(batchOrder))
	for _, batchID := range batchOrder {
		bs := batchMap[batchID]

		// Determine denominator: prefer DiscoveredBytes when it exceeds TotalBytes
		denomBytes := bs.TotalBytes
		if bs.DiscoveredBytes > denomBytes {
			denomBytes = bs.DiscoveredBytes
		}

		if denomBytes > 0 {
			// Numerator: use the batch-level cumulative byte counter (more accurate
			// than summing per-task progress * size, which misses in-flight deltas)
			transferredBytes, exists := q.batchBytesTransferred[batchID]
			if !exists {
				// Fallback: sum per-task progress (for pre-registered or legacy batches)
				for _, task := range q.tasks {
					if task.BatchID == batchID {
						transferredBytes += int64(task.GetProgress() * float64(task.Size))
					}
				}
			}
			bs.Progress = float64(transferredBytes) / float64(denomBytes)
			if bs.Progress > 1.0 {
				bs.Progress = 1.0
			}
		} else if bs.Total > 0 {
			// No size info — use file count
			bs.Progress = float64(bs.Completed) / float64(bs.Total)
		}
		result = append(result, *bs)
	}
	return result
}

// GetBatchTasks returns paginated tasks for a specific batch.
// stateFilter: "" = all tasks, "active" = non-terminal (queued/initializing/active),
// or exact state string ("completed", "failed", "cancelled").
func (q *Queue) GetBatchTasks(batchID string, offset, limit int, stateFilter string) []TransferTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var matching []TransferTask
	for _, task := range q.tasks {
		if task.BatchID != batchID {
			continue
		}
		if stateFilter != "" {
			state := task.GetState()
			if stateFilter == "active" {
				// Meta-filter: non-terminal states (queued, initializing, active).
				// Consistent with BatchStats.Active counting (queue.go:639-642).
				// TaskPaused excluded — BatchStats doesn't count it in Active.
				if state == TaskCompleted || state == TaskFailed || state == TaskCancelled || state == TaskPaused {
					continue
				}
			} else if stateFilter == "inprogress" {
				// Only tasks with a transfer slot (actually transferring)
				if state != TaskActive && state != TaskInitializing {
					continue
				}
			} else if stateFilter == "queued" {
				// Only tasks waiting for a slot
				if state != TaskQueued {
					continue
				}
			} else if string(state) != stateFilter {
				continue
			}
		}
		matching = append(matching, task.Clone())
	}

	// Apply pagination
	if offset >= len(matching) {
		return []TransferTask{}
	}
	end := offset + limit
	if end > len(matching) {
		end = len(matching)
	}
	return matching[offset:end]
}

// GetFailedTaskErrors returns error messages from failed tasks in a batch (up to limit).
func (q *Queue) GetFailedTaskErrors(batchID string, limit int) []string {
	q.mu.RLock()
	defer q.mu.RUnlock()
	var errs []string
	for _, task := range q.tasks {
		if task.BatchID == batchID && task.GetState() == TaskFailed && task.Error != nil {
			errs = append(errs, task.Error.Error())
			if len(errs) >= limit {
				break
			}
		}
	}
	return errs
}

// GetUngroupedTasks returns tasks with no BatchID.
// Used by polling to avoid sending 10k batched tasks over IPC.
func (q *Queue) GetUngroupedTasks() []TransferTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var result []TransferTask
	for _, task := range q.tasks {
		if task.BatchID == "" {
			result = append(result, task.Clone())
		}
	}
	if result == nil {
		return []TransferTask{}
	}
	return result
}

// CancelBatch cancels all non-terminal tasks in a batch.
// Also cancels the batch-level context (stops streaming scan + registration).
func (q *Queue) CancelBatch(batchID string) error {
	// Cancel batch-level context first (stops scan and registration goroutines)
	q.mu.Lock()
	if batchCancel, ok := q.batchCancelFuncs[batchID]; ok {
		batchCancel()
	}
	q.mu.Unlock()

	q.mu.Lock()
	var tasksToCancel []*TransferTask
	var cancelFns []context.CancelFunc

	for _, task := range q.tasks {
		if task.BatchID != batchID {
			continue
		}
		state := task.GetState()
		if state == TaskCompleted || state == TaskFailed || state == TaskCancelled {
			continue
		}
		tasksToCancel = append(tasksToCancel, task)
		if fn := q.cancelFuncs[task.ID]; fn != nil {
			cancelFns = append(cancelFns, fn)
		}
	}
	q.mu.Unlock()

	// Call cancel functions for active tasks
	for _, fn := range cancelFns {
		fn()
	}

	// Idempotent guard — only transition tasks that haven't gone terminal
	var actuallyCancelled []*TransferTask
	q.mu.Lock()
	for _, task := range tasksToCancel {
		if !task.IsTerminal() {
			task.State = TaskCancelled
			task.CompletedAt = time.Now()
			actuallyCancelled = append(actuallyCancelled, task)
		}
		delete(q.cancelFuncs, task.ID)
	}
	q.mu.Unlock()

	for _, task := range actuallyCancelled {
		q.publishTransferEvent(events.EventTransferCancelled, task)
	}

	q.CleanupBatch(batchID)

	// Cleanup speed/ETA metrics
	q.mu.Lock()
	q.cleanupBatchMetrics(batchID)
	q.mu.Unlock()

	return nil
}

// RegisterBatchCancel stores a cancel function for a streaming batch.
func (q *Queue) RegisterBatchCancel(batchID string, cancelFn context.CancelFunc) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.batchCancelFuncs[batchID] = cancelFn
}

// PreRegisterBatch creates an empty batch entry so it appears in GetAllBatchStats()
// before any tasks are registered. Used by streaming downloads where API scan
// may take 10-20s before the first file is discovered. Without this, the batch
// entry would "flash" in the Transfers tab as it appears only after the first task.
func (q *Queue) PreRegisterBatch(batchID, batchLabel, direction, sourceLabel string) {
	now := time.Now()
	q.mu.Lock()
	q.preRegisteredBatches[batchID] = &BatchStats{
		BatchID:     batchID,
		BatchLabel:  batchLabel,
		Direction:   direction,
		SourceLabel: sourceLabel,
		TotalKnown:  false,
		StartedAt:   now,
	}
	q.batchStartedAt[batchID] = now
	q.mu.Unlock()

	// Fire immediate batch progress event so the batch row appears
	// in the Transfers tab within ~100ms instead of waiting up to 1s for the ticker.
	if q.eventBus != nil {
		q.eventBus.Publish(&events.BatchProgressEvent{
			BaseEvent: events.BaseEvent{
				EventType: events.EventBatchProgress,
				Time:      time.Now(),
			},
			BatchID:    batchID,
			Label:      batchLabel,
			Direction:  direction,
			TotalKnown: false,
		})
	}

	// Start batch ticker so pre-registered batch gets tick events during scan
	q.ensureBatchTicker()
}

// MarkBatchScanInProgress sets whether a batch's scan is still discovering files.
func (q *Queue) MarkBatchScanInProgress(batchID string, inProgress bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if inProgress {
		q.batchScanInProgress[batchID] = true
	} else {
		delete(q.batchScanInProgress, batchID)
	}
}

// UpdateBatchDiscovered updates the batch-level discovered file/byte totals.
// Called as files are discovered (before they are registered as tasks),
// providing accurate denominators for progress/ETA computation.
func (q *Queue) UpdateBatchDiscovered(batchID string, totalFiles int, totalBytes int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.batchDiscoveredTotal[batchID] = totalFiles
	q.batchDiscoveredBytes[batchID] = totalBytes
}

// CleanupBatch removes all streaming batch metadata for deterministic cleanup.
// Prevents long-session map growth from stale batch entries.
func (q *Queue) CleanupBatch(batchID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.batchCancelFuncs, batchID)
	delete(q.batchScanInProgress, batchID)
	delete(q.preRegisteredBatches, batchID)
	delete(q.batchStartedAt, batchID)
}

// RetryFailedInBatch retries all failed tasks in a batch.
func (q *Queue) RetryFailedInBatch(batchID string) error {
	q.mu.RLock()
	var failedTaskIDs []string
	for _, task := range q.tasks {
		if task.BatchID == batchID && task.GetState() == TaskFailed {
			failedTaskIDs = append(failedTaskIDs, task.ID)
		}
	}
	q.mu.RUnlock()

	for _, taskID := range failedTaskIDs {
		if _, err := q.Retry(taskID); err != nil {
			// Log but continue — don't fail the whole batch retry for one task
			continue
		}
	}
	return nil
}

// ensureBatchTicker starts the batch progress ticker if not already running.
// Publishes BatchProgressEvent at 1/sec for each active batch.
func (q *Queue) ensureBatchTicker() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.batchTickerRunning {
		return
	}
	q.batchTickerRunning = true

	go q.batchTickerLoop()
}

// computeBatchETA returns the estimated time remaining in seconds for a batch.
// Returns -1 during scan (!TotalKnown), 0 when complete, else smoothed ETA.
// Must be called under q.mu write lock (reads/writes batchPrevETA).
func (q *Queue) computeBatchETA(batchID string, bs *BatchStats) float64 {
	if !bs.TotalKnown {
		return -1 // Unknown — scan still in progress
	}
	if bs.Progress >= 1.0 || (bs.Total > 0 && bs.Completed >= bs.Total) {
		return 0 // Complete
	}
	if bs.Speed <= 0 {
		return -1 // No speed data — can't estimate
	}

	// Use DiscoveredBytes when available for more accurate remaining calculation
	denomBytes := bs.TotalBytes
	if bs.DiscoveredBytes > denomBytes {
		denomBytes = bs.DiscoveredBytes
	}
	if denomBytes <= 0 {
		return -1
	}

	transferredBytes := float64(denomBytes) * bs.Progress
	remainingBytes := float64(denomBytes) - transferredBytes
	if remainingBytes <= 0 {
		return 0
	}

	rawETA := remainingBytes / bs.Speed
	return q.smoothETA(batchID, rawETA)
}

// smoothETA applies jump capping and EMA to prevent wild ETA swings.
// Must be called under q.mu write lock.
func (q *Queue) smoothETA(batchID string, rawETA float64) float64 {
	prev, hasPrev := q.batchPrevETA[batchID]
	if !hasPrev || prev <= 0 {
		q.batchPrevETA[batchID] = rawETA
		return rawETA
	}

	// Jump cap: clamp single-tick changes to 2x in either direction
	capped := rawETA
	if capped > prev*2 {
		capped = prev * 2
	} else if capped < prev*0.5 {
		capped = prev * 0.5
	}

	// EMA with alpha=0.3
	const alpha = 0.3
	smoothed := alpha*capped + (1-alpha)*prev

	// Fast convergence: if raw ETA < 0.5x smoothed, apply alpha twice
	if rawETA < smoothed*0.5 {
		smoothed = alpha*capped + (1-alpha)*smoothed
	}

	q.batchPrevETA[batchID] = smoothed
	return smoothed
}

// batchTickerLoop publishes batch progress events every second.
func (q *Queue) batchTickerLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Record speed/rate samples under write lock before computing stats
		q.mu.Lock()
		now := time.Now()
		for batchID, totalBytes := range q.batchBytesTransferred {
			window, exists := q.batchSpeedWindows[batchID]
			if !exists {
				window = newSpeedWindow(10 * time.Second)
				q.batchSpeedWindows[batchID] = window
			}
			window.Record(now, totalBytes)
		}
		for batchID, filesCompleted := range q.batchFilesCompleted {
			window, exists := q.batchFileRateWindows[batchID]
			if !exists {
				window = newSpeedWindow(10 * time.Second)
				q.batchFileRateWindows[batchID] = window
			}
			window.Record(now, filesCompleted)
		}
		q.mu.Unlock()

		stats := q.GetAllBatchStats()
		if len(stats) == 0 {
			q.mu.Lock()
			q.batchTickerRunning = false
			q.mu.Unlock()
			return
		}

		// Compute ETAs under write lock (needs batchPrevETA state).
		// Store last computed ETA so polling DTO can return it.
		q.mu.Lock()
		for i := range stats {
			stats[i].ETASeconds = q.computeBatchETA(stats[i].BatchID, &stats[i])
			q.batchLastETA[stats[i].BatchID] = stats[i].ETASeconds
		}
		q.mu.Unlock()

		allTerminal := true
		for _, bs := range stats {
			// Scanning batches (!TotalKnown) are also non-terminal
			if bs.Queued > 0 || bs.Active > 0 || !bs.TotalKnown {
				allTerminal = false
			}

			if q.eventBus != nil {
				q.eventBus.Publish(&events.BatchProgressEvent{
					BaseEvent: events.BaseEvent{
						EventType: events.EventBatchProgress,
						Time:      time.Now(),
					},
					BatchID:         bs.BatchID,
					Label:           bs.BatchLabel,
					Direction:       bs.Direction,
					Total:           bs.Total,
					Active:          bs.Active,
					Queued:          bs.Queued,
					Completed:       bs.Completed,
					Failed:          bs.Failed,
					Progress:        bs.Progress,
					Speed:           bs.Speed,
					TotalKnown:      bs.TotalKnown,
					FilesPerSec:     bs.FilesPerSec,
					ETASeconds:      bs.ETASeconds,
					DiscoveredTotal: bs.DiscoveredTotal,
					DiscoveredBytes: bs.DiscoveredBytes,
				})
			}
		}

		if allTerminal {
			// Clean up batch metrics for all terminal batches
			q.mu.Lock()
			for _, bs := range stats {
				q.cleanupBatchMetrics(bs.BatchID)
			}
			q.batchTickerRunning = false
			q.mu.Unlock()
			return
		}
	}
}

// cleanupBatchMetrics removes speed/ETA tracking state for a batch.
// Called when batch is fully terminal (all tasks complete/failed/cancelled).
// Separate from CleanupBatch() which runs at registration end, not transfer end.
func (q *Queue) cleanupBatchMetrics(batchID string) {
	delete(q.batchBytesTransferred, batchID)
	delete(q.batchSpeedWindows, batchID)
	delete(q.batchFilesCompleted, batchID)
	delete(q.batchFileRateWindows, batchID)
	delete(q.batchDiscoveredTotal, batchID)
	delete(q.batchDiscoveredBytes, batchID)
	delete(q.batchPrevETA, batchID)
	delete(q.batchLastETA, batchID)
	delete(q.batchStartedAt, batchID)
}
