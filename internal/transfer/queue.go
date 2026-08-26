// Package transfer provides transfer queue management for uploads and downloads.
// The queue tracks task state and publishes events - execution is handled by callers.
package transfer

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/constants"
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
	tasks     []*TransferTask          // All tasks in creation order
	tasksByID map[string]*TransferTask // Index by ID for quick lookup
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

	// Batches the user cancelled. A streaming batch's registration goroutine can
	// still be mid-flight when the cancel sweep runs, so tasks registered after
	// the sweep are entered directly as TaskCancelled instead of TaskQueued —
	// otherwise they sit queued forever with no worker left to run them, and the
	// batch never reaches a terminal state.
	cancelledBatches map[string]struct{}

	// Pre-registered batches visible before first task is discovered
	preRegisteredBatches map[string]*BatchStats

	// Batch-level speed/ETA tracking
	batchBytesTransferred map[string]int64        // cumulative bytes per batch
	batchSpeedWindows     map[string]*speedWindow // 10s sliding window (bytes)
	batchFilesCompleted   map[string]int64        // cumulative completed files per batch
	batchFileRateWindows  map[string]*speedWindow // 10s sliding window (files)
	batchDiscoveredTotal  map[string]int          // total files discovered (may exceed registered tasks)
	batchDiscoveredBytes  map[string]int64        // total bytes discovered
	batchSkipped          map[string]int          // entries skipped by walker (junctions, unresolvable links)
	batchPrevETA          map[string]float64      // smoothed ETA state
	batchLastETA          map[string]float64      // last computed ETA (for polling DTO)
	batchStartedAt        map[string]time.Time    // batch start time for elapsed display
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
		cancelledBatches:      make(map[string]struct{}),
		preRegisteredBatches:  make(map[string]*BatchStats),
		batchBytesTransferred: make(map[string]int64),
		batchSpeedWindows:     make(map[string]*speedWindow),
		batchFilesCompleted:   make(map[string]int64),
		batchFileRateWindows:  make(map[string]*speedWindow),
		batchDiscoveredTotal:  make(map[string]int),
		batchDiscoveredBytes:  make(map[string]int64),
		batchSkipped:          make(map[string]int),
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
	return q.track(name, size, taskType, source, dest, "", "", "")
}

// TrackTransferWithLabel registers a new transfer with a source label.
func (q *Queue) TrackTransferWithLabel(name string, size int64, taskType TaskType, source, dest, sourceLabel string) *TransferTask {
	return q.track(name, size, taskType, source, dest, sourceLabel, "", "")
}

// TrackTransferWithBatch registers a new transfer with source label and batch info.
func (q *Queue) TrackTransferWithBatch(name string, size int64, taskType TaskType, source, dest, sourceLabel, batchID, batchLabel string) *TransferTask {
	return q.track(name, size, taskType, source, dest, sourceLabel, batchID, batchLabel)
}

// track is the single registration path for all TrackTransfer* variants.
// Registration and the cancelled-batch check share one critical section so a
// task can never be inserted as TaskQueued into a batch whose cancel sweep has
// already run.
func (q *Queue) track(name string, size int64, taskType TaskType, source, dest, sourceLabel, batchID, batchLabel string) *TransferTask {
	task := NewTransferTask(taskType, name, source, dest, size)
	task.SourceLabel = sourceLabel
	task.BatchID = batchID
	task.BatchLabel = batchLabel

	q.mu.Lock()
	batchCancelled := false
	if batchID != "" {
		_, batchCancelled = q.cancelledBatches[batchID]
	}
	if batchCancelled {
		// Nothing will execute this task — enter it terminal so the batch can
		// reach a terminal state and callers waiting on it can return.
		task.State = TaskCancelled
		task.CompletedAt = time.Now()
	}
	q.tasks = append(q.tasks, task)
	q.tasksByID[task.ID] = task
	// Stamp start time if not already set (non-streaming batches skip PreRegisterBatch).
	if batchID != "" {
		if _, exists := q.batchStartedAt[batchID]; !exists {
			q.batchStartedAt[batchID] = time.Now()
		}
	}
	q.mu.Unlock()

	// Start batch progress ticker if this is the first batched task
	if batchID != "" {
		q.ensureBatchTicker()
	}

	if batchCancelled {
		q.publishTransferEvent(events.EventTransferCancelled, task)
	} else {
		q.publishTransferEvent(events.EventTransferQueued, task)
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
	if !exists || task == nil || !task.activate() {
		q.mu.Unlock()
		return false
	}
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
	if exists && task != nil {
		shouldPublish = task.beginTransfer()
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
	if !exists || task == nil || !task.failIfNotTerminal(err) {
		delete(q.cancelFuncs, taskID) // Cleanup
		q.mu.Unlock()
		return false
	}
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
		task.setSize(size)
	}
}

// SetTaskTags records the tags to apply after a successful upload, so a retry
// (which only sees the task) re-applies them instead of dropping them.
func (q *Queue) SetTaskTags(taskID string, tags []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if task, ok := q.tasksByID[taskID]; ok && task != nil {
		task.setTags(tags)
	}
}

// UpdateProgress updates a task's progress.
// Progress should be 0.0 to 1.0.
// Speed is calculated automatically using smoothed EMA.
// q.mu is held for the whole operation so the batch byte counter and the task's
// own fields advance together; the task's mutex guards the fields themselves.
func (q *Queue) UpdateProgress(taskID string, progress float64) {
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if !exists || task == nil {
		q.mu.Unlock()
		return
	}

	// Track cumulative bytes transferred for batch-level speed window
	if delta := task.applyProgress(progress); delta > 0 {
		q.batchBytesTransferred[task.BatchID] += delta
	}

	q.mu.Unlock()

	// Publish progress event (outside lock to avoid holding lock during event dispatch)
	q.publishTransferEvent(events.EventTransferProgress, task)
}

// Complete marks a task as successfully completed.
// Terminal-guarded: a task the user already cancelled stays cancelled even if
// its in-flight transfer went on to finish.
func (q *Queue) Complete(taskID string) {
	var completed bool
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if exists && task != nil {
		var batchDelta int64
		batchDelta, completed = task.completeIfNotTerminal()
		if completed && task.BatchID != "" {
			// Account for remaining bytes not yet counted toward batch total
			q.batchBytesTransferred[task.BatchID] += batchDelta
			// Track completed file count for files/sec window
			q.batchFilesCompleted[task.BatchID]++
		}
	}
	delete(q.cancelFuncs, taskID) // Clean up cancel function
	q.mu.Unlock()

	if completed {
		q.publishTransferEvent(events.EventTransferCompleted, task)
	}
}

// Fail marks a task as failed with an error.
// Terminal-guarded: after a cancel, the transfer often reports a secondary
// error (unexpected EOF, connection reset) that must not overwrite Cancelled.
func (q *Queue) Fail(taskID string, err error) {
	var failed bool
	q.mu.Lock()
	task, exists := q.tasksByID[taskID]
	if exists && task != nil {
		failed = task.failIfNotTerminal(err)
	}
	delete(q.cancelFuncs, taskID) // Clean up cancel function
	q.mu.Unlock()

	if failed {
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
	cancelFn := q.cancelFuncs[taskID]
	cancelled := task.cancelIfNotTerminal()
	if cancelled {
		delete(q.cancelFuncs, taskID)
	}
	q.mu.Unlock()

	if !cancelled {
		return errors.New("task is not cancellable")
	}
	if cancelFn != nil {
		cancelFn()
	}
	q.publishTransferEvent(events.EventTransferCancelled, task)
	return nil
}

// CancelAll cancels all active, initializing, and queued tasks, and cancels
// every registered batch context so streaming scans stop discovering work.
//
// Ordering matters: tasks are transitioned to TaskCancelled in the same
// critical section that snapshots them, before any cancel function runs. A
// worker that reacts to its cancelled context calls FailIfNotTerminal, which
// then correctly no-ops instead of recording "context canceled" as a failure.
func (q *Queue) CancelAll() {
	q.mu.Lock()

	// Mirror CancelBatch: stop scan/registration goroutines and mark their
	// batches cancelled so late registrations land terminal, not orphaned.
	batchCancels := make([]context.CancelFunc, 0, len(q.batchCancelFuncs))
	for batchID, fn := range q.batchCancelFuncs {
		q.cancelledBatches[batchID] = struct{}{}
		if fn != nil {
			batchCancels = append(batchCancels, fn)
		}
	}

	cancelFns := make([]context.CancelFunc, 0)
	var cancelled []*TransferTask
	for _, task := range q.tasks {
		state := task.GetState()
		if state != TaskActive && state != TaskInitializing && state != TaskQueued {
			continue
		}
		if fn := q.cancelFuncs[task.ID]; fn != nil {
			cancelFns = append(cancelFns, fn)
		}
		if task.cancelIfNotTerminal() {
			cancelled = append(cancelled, task)
		}
		delete(q.cancelFuncs, task.ID)
	}
	q.mu.Unlock()

	// Call cancel functions outside the lock — a cancelFn may re-enter the queue.
	for _, fn := range batchCancels {
		fn()
	}
	for _, fn := range cancelFns {
		fn()
	}

	for _, task := range cancelled {
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
	originalTask.resetForRetry()

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
	survivingBatches := make(map[string]struct{})
	for _, task := range q.tasks {
		if !task.IsTerminal() {
			filtered = append(filtered, task)
			if task.BatchID != "" {
				survivingBatches[task.BatchID] = struct{}{}
			}
		} else {
			delete(q.tasksByID, task.ID)
		}
	}
	q.tasks = filtered

	// Drop skip counts for batches with no surviving tasks. Without this,
	// a placeholder-task batch that's been Cleared would leak its skip
	// metadata in batchSkipped indefinitely.
	for batchID := range q.batchSkipped {
		if _, ok := survivingBatches[batchID]; !ok {
			delete(q.batchSkipped, batchID)
		}
	}

	// Same for the cancelled-batch markers. They must outlive CancelBatch (a
	// registration goroutine can still be streaming tasks in), so they are
	// dropped only once a batch has no tasks left — here, and in
	// ClearBatchTerminalTasks for a single batch.
	for batchID := range q.cancelledBatches {
		if _, ok := survivingBatches[batchID]; !ok {
			delete(q.cancelledBatches, batchID)
		}
	}
}

// ClearBatchTerminalTasks removes the terminal tasks of a single batch and, when
// none of its tasks remain, the batch's leftover metadata. Non-terminal tasks
// are left alone, so calling this on a batch that is still running is safe but
// will not empty it.
//
// This is the narrow counterpart to ClearCompleted for a caller that owns one
// batch and wants to reclaim it without touching anyone else's history — the
// daemon, whose queue would otherwise grow by one task per downloaded file for
// as long as it runs. Returns the number of tasks removed.
func (q *Queue) ClearBatchTerminalTasks(batchID string) int {
	if batchID == "" {
		return 0
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	removed := 0
	anyLeft := false
	filtered := make([]*TransferTask, 0, len(q.tasks))
	for _, task := range q.tasks {
		if task.BatchID == batchID && task.IsTerminal() {
			delete(q.tasksByID, task.ID)
			removed++
			continue
		}
		if task.BatchID == batchID {
			anyLeft = true
		}
		filtered = append(filtered, task)
	}
	if removed == 0 {
		return 0
	}
	q.tasks = filtered

	if !anyLeft {
		// Same reasoning as ClearCompleted: this metadata must outlive the
		// tasks' completion, but not their removal.
		delete(q.batchSkipped, batchID)
		delete(q.cancelledBatches, batchID)
		q.cleanupBatchMetrics(batchID)
	}
	return removed
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
		Size:     task.GetSize(),
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
	Progress        float64   // byte-weighted 0.0-1.0
	Speed           float64   // aggregate bytes/sec
	TotalKnown      bool      // True when scan is complete and Total is final
	FilesPerSec     float64   // file completion rate (windowed)
	ETASeconds      float64   // estimated time remaining (smoothed, -1 = unknown)
	DiscoveredTotal int       // files discovered by scan (may > Total during queueing)
	DiscoveredBytes int64     // bytes discovered by scan
	StartedAt       time.Time // batch start time for elapsed display
	Skipped         int       // entries the walker skipped (junctions, unresolvable links)
	CancelRequested bool      // user cancelled this batch (stays true for the batch's lifetime)
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
		if sk, exists := q.batchSkipped[batchID]; exists {
			bs.Skipped = sk
		}
		if _, exists := q.cancelledBatches[batchID]; exists {
			bs.CancelRequested = true
		}
		// Return last ticker-computed ETA so polling DTO doesn't zero it out
		if eta, exists := q.batchLastETA[batchID]; exists {
			bs.ETASeconds = eta
		}
	}

	// Include pre-registered batches that have no tasks yet. Clone to avoid
	// mutating the preRegisteredBatches map; flip TotalKnown based on the
	// current scan-in-progress state (a pre-registered batch that finished
	// registration with zero tasks — e.g. a daemon batch where every file
	// was already present locally — reports TotalKnown=true so WaitForBatch
	// can return the empty-batch result).
	for batchID, preBatch := range q.preRegisteredBatches {
		if _, exists := batchMap[batchID]; !exists {
			clone := *preBatch
			clone.TotalKnown = !q.batchScanInProgress[batchID]
			if sk, ok := q.batchSkipped[batchID]; ok {
				clone.Skipped = sk
			}
			if _, ok := q.cancelledBatches[batchID]; ok {
				clone.CancelRequested = true
			}
			batchMap[batchID] = &clone
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

// GetBatchStats returns the BatchStats for a single batch. Second return
// is false when the batch is neither present in the task list nor
// pre-registered. Implemented in terms of GetAllBatchStats to share the
// byte-weighted progress + ETA logic — callers like daemon WaitForBatch
// need the exact same numbers the UI sees.
func (q *Queue) GetBatchStats(batchID string) (BatchStats, bool) {
	for _, bs := range q.GetAllBatchStats() {
		if bs.BatchID == batchID {
			return bs, true
		}
	}
	return BatchStats{}, false
}

// GetBatchTasks returns paginated tasks for a specific batch.
// stateFilter: "" = all tasks, "active" = non-terminal (queued/initializing/active),
// or exact state string ("completed", "failed", "cancelled").
func (q *Queue) GetBatchTasks(batchID string, offset, limit int, stateFilter string) []TransferTask {
	if offset < 0 || limit <= 0 {
		return []TransferTask{}
	}

	q.mu.RLock()
	defer q.mu.RUnlock()

	// Filter and paginate in one pass so only the requested page is cloned.
	// Cloning every match before slicing meant asking for the first 50 rows of a
	// 20k-file batch copied all 20k tasks while holding the read lock, blocking
	// the progress writers that need the write lock — the GUI stall that made a
	// newly queued transfer take seconds to appear.
	// Cap the prealloc so an oversized limit from a caller can't demand a huge
	// allocation up front.
	page := make([]TransferTask, 0, min(limit, 256))
	matched := 0
	for _, task := range q.tasks {
		if task.BatchID != batchID {
			continue
		}
		if stateFilter != "" {
			state := task.GetState()
			if stateFilter == "active" {
				// Meta-filter: non-terminal states (queued, initializing, active).
				// Consistent with BatchStats.Active counting in GetAllBatchStats.
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
		matched++
		if matched <= offset {
			continue
		}
		page = append(page, task.Clone())
		if len(page) == limit {
			break
		}
	}
	return page
}

// GetFailedTaskErrors returns error messages from failed tasks in a batch (up to limit).
func (q *Queue) GetFailedTaskErrors(batchID string, limit int) []string {
	q.mu.RLock()
	defer q.mu.RUnlock()
	var errs []string
	for _, task := range q.tasks {
		if task.BatchID != batchID || task.GetState() != TaskFailed {
			continue
		}
		if err := task.GetError(); err != nil {
			errs = append(errs, err.Error())
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
//
// The sweep marks the batch cancelled and transitions its tasks to
// TaskCancelled in ONE critical section, and only then invokes the batch and
// per-task cancel functions. Two races depend on that order:
//
//   - A task already in flight sees its context die and calls
//     FailIfNotTerminal. Cancelling first and transitioning afterwards let the
//     worker win, recording a user cancel as Failed("context canceled").
//   - A streaming batch's registration goroutine selects between its cancelled
//     context and the next request; Go picks either when both are ready, so it
//     can register one more task after the sweep. The cancelled-batch marker
//     makes that task terminal on arrival instead of leaving it queued forever
//     with no worker left to run it (which kept the batch non-terminal, so
//     Cancel needed a second click and WaitForBatch never returned).
func (q *Queue) CancelBatch(batchID string) error {
	q.mu.Lock()
	q.cancelledBatches[batchID] = struct{}{}
	batchCancel := q.batchCancelFuncs[batchID]

	var cancelFns []context.CancelFunc
	var cancelled []*TransferTask
	for _, task := range q.tasks {
		if task.BatchID != batchID {
			continue
		}
		if task.IsTerminal() {
			continue
		}
		if fn := q.cancelFuncs[task.ID]; fn != nil {
			cancelFns = append(cancelFns, fn)
		}
		if task.cancelIfNotTerminal() {
			cancelled = append(cancelled, task)
		}
		delete(q.cancelFuncs, task.ID)
	}
	q.mu.Unlock()

	// Call cancel functions outside the lock — a cancelFn may re-enter the queue.
	if batchCancel != nil {
		batchCancel()
	}
	for _, fn := range cancelFns {
		fn()
	}

	for _, task := range cancelled {
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
	// Default scan-in-progress until a caller clears it. Keeps existing
	// streaming batches reporting TotalKnown=false (matches their discovery
	// phase) without requiring every caller to set this explicitly. Paired
	// with a MarkBatchScanInProgress(batchID, false) call when registration
	// is complete.
	q.batchScanInProgress[batchID] = true
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

// IncrementBatchSkipped increments the batch-level count of entries the walker
// chose to skip (Windows junctions, unresolvable links). Surfaced through
// BatchStats.Skipped so the UI can show "N items skipped" alongside completion.
func (q *Queue) IncrementBatchSkipped(batchID string, n int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.batchSkipped[batchID] += n
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
	ticker := time.NewTicker(constants.TableRefreshBatchInterval)
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
					Cancelled:       bs.Cancelled,
					CancelRequested: bs.CancelRequested,
					Progress:        bs.Progress,
					Speed:           bs.Speed,
					TotalKnown:      bs.TotalKnown,
					FilesPerSec:     bs.FilesPerSec,
					ETASeconds:      bs.ETASeconds,
					DiscoveredTotal: bs.DiscoveredTotal,
					DiscoveredBytes: bs.DiscoveredBytes,
					Skipped:         bs.Skipped,
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
	// Note: batchSkipped is intentionally NOT cleared here. The skip count is
	// upload-permanent metadata, not a live metric. It must remain readable as
	// long as any task with this BatchID exists (e.g., the placeholder task in
	// an all-skipped folder upload). ClearCompleted clears the count via
	// removeBatchSkippedIfNoTasks, called when no tasks remain for this batch.
}
