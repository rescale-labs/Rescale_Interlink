// Package transfer provides transfer queue management for uploads and downloads.
// v3.6.3: Redesigned queue to OBSERVE transfers, not execute them.
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
// v3.6.3 Architecture:
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

	// v4.7.7: Batch progress ticker
	batchTickerRunning bool

	// v4.8.0: Streaming batch support
	batchCancelFuncs    map[string]context.CancelFunc // Cancel functions for streaming batches
	batchScanInProgress map[string]bool               // True while scan is still discovering files

	// v4.8.2: Pre-registered batches visible before first task is discovered
	preRegisteredBatches map[string]*BatchStats
}

// NewQueue creates a new transfer queue with the specified event bus.
// The queue is immediately ready to track tasks - no Start() needed.
func NewQueue(eventBus *events.EventBus) *Queue {
	return &Queue{
		tasks:                make([]*TransferTask, 0),
		tasksByID:            make(map[string]*TransferTask),
		cancelFuncs:          make(map[string]context.CancelFunc),
		batchCancelFuncs:     make(map[string]context.CancelFunc),
		batchScanInProgress:  make(map[string]bool),
		preRegisteredBatches: make(map[string]*BatchStats),
		eventBus:             eventBus,
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
// v4.7.4: Added for transfer origin tracking (PUR, SingleJob, FileBrowser).
func (q *Queue) TrackTransferWithLabel(name string, size int64, taskType TaskType, source, dest, sourceLabel string) *TransferTask {
	task := q.TrackTransfer(name, size, taskType, source, dest)
	task.SourceLabel = sourceLabel
	return task
}

// TrackTransferWithBatch registers a new transfer with source label and batch info.
// v4.7.7: Added for batch grouping in Transfers tab.
func (q *Queue) TrackTransferWithBatch(name string, size int64, taskType TaskType, source, dest, sourceLabel, batchID, batchLabel string) *TransferTask {
	task := q.TrackTransferWithLabel(name, size, taskType, source, dest, sourceLabel)
	task.BatchID = batchID
	task.BatchLabel = batchLabel

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
func (q *Queue) StartTransfer(taskID string) {
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if exists && task != nil && task.State == TaskInitializing {
		task.State = TaskActive
	}
	q.mu.Unlock()

	if exists && task != nil && task.State == TaskActive {
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
//
// v4.0.3: Fixed race condition - lock is now held for entire operation to protect
// all task field updates (Progress, Speed, lastUpdateTime) from concurrent access.
func (q *Queue) UpdateProgress(taskID string, progress float64) {
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if !exists || task == nil {
		q.mu.Unlock()
		return
	}

	// v3.6.3: Improved speed calculation with better smoothing
	now := time.Now()
	elapsed := now.Sub(task.lastUpdateTime).Seconds()

	// Only calculate speed if:
	// 1. At least 0.3 seconds elapsed (avoid noisy samples)
	// 2. Progress actually increased (ignore backwards jumps)
	// 3. Progress delta is meaningful (> 0.001 = 0.1%)
	progressDelta := progress - task.Progress
	if elapsed >= 0.3 && progressDelta > 0.001 {
		bytesTransferred := progressDelta * float64(task.Size)
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

// Cancel cancels an active or initializing task by calling its stored cancel function.
func (q *Queue) Cancel(taskID string) error {
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	cancelFn := q.cancelFuncs[taskID]
	q.mu.Unlock()

	if !exists || task == nil {
		return errors.New("task not found")
	}

	// Only cancel if task is active or initializing
	state := task.GetState()
	if state != TaskActive && state != TaskInitializing {
		return errors.New("task is not active or initializing")
	}

	// Call cancel function if available
	if cancelFn != nil {
		cancelFn()
	}

	// Update state
	q.mu.Lock()
	task.State = TaskCancelled
	task.CompletedAt = time.Now()
	delete(q.cancelFuncs, taskID)
	q.mu.Unlock()

	q.publishTransferEvent(events.EventTransferCancelled, task)
	return nil
}

// CancelAll cancels all active and initializing tasks.
func (q *Queue) CancelAll() {
	q.mu.Lock()
	tasksToCancel := make([]*TransferTask, 0)
	cancelFns := make([]context.CancelFunc, 0)

	for _, task := range q.tasks {
		if task.State == TaskActive || task.State == TaskInitializing {
			tasksToCancel = append(tasksToCancel, task)
			if fn := q.cancelFuncs[task.ID]; fn != nil {
				cancelFns = append(cancelFns, fn)
			}
		}
	}
	q.mu.Unlock()

	// Call all cancel functions
	for _, fn := range cancelFns {
		fn()
	}

	// Update states and publish events
	q.mu.Lock()
	for _, task := range tasksToCancel {
		task.State = TaskCancelled
		task.CompletedAt = time.Now()
		delete(q.cancelFuncs, task.ID)
	}
	q.mu.Unlock()

	for _, task := range tasksToCancel {
		q.publishTransferEvent(events.EventTransferCancelled, task)
	}
}

// Retry resets a failed or cancelled task and re-queues it for execution.
// v4.4.0: Now reuses the same task entry instead of creating a duplicate.
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

	// v4.4.0: Reset the existing task instead of creating a new one.
	// This keeps a single entry in the queue instead of duplicates.
	originalTask.mu.Lock()
	originalTask.State = TaskQueued
	originalTask.Progress = 0.0
	originalTask.Speed = 0.0
	originalTask.Error = nil
	originalTask.StartedAt = time.Time{}
	originalTask.CompletedAt = time.Time{}
	originalTask.lastBytes = 0
	originalTask.lastUpdateTime = time.Time{}
	// Note: Keep ID, Type, Name, Source, Dest, Size, CreatedAt unchanged
	originalTask.mu.Unlock()

	q.publishTransferEvent(events.EventTransferQueued, originalTask)

	// Execute retry via executor (in goroutine to not block)
	go executor.ExecuteRetry(originalTask)

	return taskID, nil // v4.4.0: Return same ID, not a new one
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
// v4.7.7: Suppresses progress events for batched tasks to reduce event flood.
// Terminal events (completed, failed, cancelled) are always published.
func (q *Queue) publishTransferEvent(eventType events.EventType, task *TransferTask) {
	if q.eventBus == nil {
		return
	}

	// v4.7.7: Skip individual progress events for batched tasks.
	// The batch progress ticker publishes aggregate events instead.
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
// v4.7.7: Used for grouped display in Transfers tab.
type BatchStats struct {
	BatchID     string
	BatchLabel  string
	Direction   string // "upload" or "download"
	SourceLabel string
	Total       int
	Queued      int
	Active      int
	Completed   int
	Failed      int
	Cancelled   int
	TotalBytes  int64
	Progress    float64 // byte-weighted 0.0-1.0
	Speed       float64 // aggregate bytes/sec
	TotalKnown  bool    // v4.8.0: True when scan is complete and Total is final
}

// GetAllBatchStats returns aggregate stats for all batches in a single pass.
// v4.7.7: O(tasks) scan, returns one BatchStats per distinct BatchID.
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
			// v4.8.0: TotalKnown = true when scan NOT in progress
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
		case TaskQueued, TaskInitializing:
			bs.Queued++
		case TaskActive:
			bs.Active++
			bs.Speed += task.GetSpeed()
		case TaskCompleted:
			bs.Completed++
		case TaskFailed:
			bs.Failed++
		case TaskCancelled:
			bs.Cancelled++
		}
	}

	// v4.8.2: Include pre-registered batches that have no tasks yet
	for batchID, preBatch := range q.preRegisteredBatches {
		if _, exists := batchMap[batchID]; !exists {
			batchMap[batchID] = preBatch
			batchOrder = append(batchOrder, batchID)
		}
	}

	// Compute byte-weighted progress
	result := make([]BatchStats, 0, len(batchOrder))
	for _, batchID := range batchOrder {
		bs := batchMap[batchID]
		if bs.TotalBytes > 0 {
			var transferredBytes int64
			for _, task := range q.tasks {
				if task.BatchID == batchID {
					transferredBytes += int64(task.GetProgress() * float64(task.Size))
				}
			}
			bs.Progress = float64(transferredBytes) / float64(bs.TotalBytes)
		} else if bs.Total > 0 {
			// No size info — use file count
			bs.Progress = float64(bs.Completed) / float64(bs.Total)
		}
		result = append(result, *bs)
	}
	return result
}

// GetBatchTasks returns paginated tasks for a specific batch.
// v4.7.7: Used for expanded batch detail view.
func (q *Queue) GetBatchTasks(batchID string, offset, limit int) []TransferTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var matching []TransferTask
	for _, task := range q.tasks {
		if task.BatchID == batchID {
			matching = append(matching, task.Clone())
		}
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

// GetUngroupedTasks returns tasks with no BatchID.
// v4.7.7: Used by polling to avoid sending 10k batched tasks over IPC.
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
// v4.7.7: Unlike single Cancel(), this also handles queued tasks (the majority in large batches).
// v4.8.0: Also cancels the batch-level context (stops streaming scan + registration).
func (q *Queue) CancelBatch(batchID string) error {
	// v4.8.0: Cancel batch-level context first (stops scan and registration goroutines)
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

	// Update states
	q.mu.Lock()
	for _, task := range tasksToCancel {
		task.State = TaskCancelled
		task.CompletedAt = time.Now()
		delete(q.cancelFuncs, task.ID)
	}
	q.mu.Unlock()

	// Publish events
	for _, task := range tasksToCancel {
		q.publishTransferEvent(events.EventTransferCancelled, task)
	}

	// v4.8.0: Cleanup streaming batch state
	q.CleanupBatch(batchID)

	return nil
}

// RegisterBatchCancel stores a cancel function for a streaming batch.
// v4.8.0: Called by StartStreamingDownloadBatch to enable CancelBatch to stop the scan.
func (q *Queue) RegisterBatchCancel(batchID string, cancelFn context.CancelFunc) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.batchCancelFuncs[batchID] = cancelFn
}

// PreRegisterBatch creates an empty batch entry so it appears in GetAllBatchStats()
// before any tasks are registered. Used by streaming downloads where API scan
// may take 10-20s before the first file is discovered.
// v4.8.2: Eliminates batch entry "flashing" in the Transfers tab.
func (q *Queue) PreRegisterBatch(batchID, batchLabel, direction, sourceLabel string) {
	q.mu.Lock()
	q.preRegisteredBatches[batchID] = &BatchStats{
		BatchID:     batchID,
		BatchLabel:  batchLabel,
		Direction:   direction,
		SourceLabel: sourceLabel,
		TotalKnown:  false,
	}
	q.mu.Unlock()

	// v4.8.3: Fire immediate batch progress event so the batch row appears
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
// v4.8.0: Used to determine TotalKnown in batch stats.
func (q *Queue) MarkBatchScanInProgress(batchID string, inProgress bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if inProgress {
		q.batchScanInProgress[batchID] = true
	} else {
		delete(q.batchScanInProgress, batchID)
	}
}

// CleanupBatch removes all streaming batch metadata for deterministic cleanup.
// v4.8.0: Prevents long-session map growth from stale batch entries.
func (q *Queue) CleanupBatch(batchID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.batchCancelFuncs, batchID)
	delete(q.batchScanInProgress, batchID)
	delete(q.preRegisteredBatches, batchID)
}

// RetryFailedInBatch retries all failed tasks in a batch.
// v4.7.7: Batch retry for grouped Transfers tab view.
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
// v4.7.7: Publishes BatchProgressEvent at 1/sec for each active batch.
func (q *Queue) ensureBatchTicker() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.batchTickerRunning {
		return
	}
	q.batchTickerRunning = true

	go q.batchTickerLoop()
}

// batchTickerLoop publishes batch progress events every second.
func (q *Queue) batchTickerLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		stats := q.GetAllBatchStats()
		if len(stats) == 0 {
			q.mu.Lock()
			q.batchTickerRunning = false
			q.mu.Unlock()
			return
		}

		allTerminal := true
		for _, bs := range stats {
			// v4.8.2: Scanning batches (!TotalKnown) are also non-terminal
			if bs.Queued > 0 || bs.Active > 0 || !bs.TotalKnown {
				allTerminal = false
			}

			if q.eventBus != nil {
				q.eventBus.Publish(&events.BatchProgressEvent{
					BaseEvent: events.BaseEvent{
						EventType: events.EventBatchProgress,
						Time:      time.Now(),
					},
					BatchID:    bs.BatchID,
					Label:      bs.BatchLabel,
					Direction:  bs.Direction,
					Total:      bs.Total,
					Active:     bs.Active,
					Queued:     bs.Queued,
					Completed:  bs.Completed,
					Failed:     bs.Failed,
					Progress:   bs.Progress,
					Speed:      bs.Speed,
					TotalKnown: bs.TotalKnown, // v4.8.0
				})
			}
		}

		if allTerminal {
			q.mu.Lock()
			q.batchTickerRunning = false
			q.mu.Unlock()
			return
		}
	}
}
