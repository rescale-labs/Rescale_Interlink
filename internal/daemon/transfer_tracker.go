package daemon

import (
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/ipc"
)

// DaemonBatchStatus tracks the progress of a single job's download batch.
type DaemonBatchStatus struct {
	BatchID     string
	BatchLabel  string
	Direction   string // always "download"
	Total       int
	Completed   int
	Failed      int
	Active      int
	TotalBytes  int64
	BytesDone   int64
	Speed       float64 // bytes/sec, computed from progress deltas
	StartedAt   time.Time
	CompletedAt time.Time // zero if still active

	// Internal state (not exported)
	activeFileBytes int64 // partial bytes of currently downloading file
	lastBytesDone   int64
	lastSpeedTime   time.Time
}

// DaemonTransferTracker provides in-memory tracking of daemon download batches.
// Enables GUI visibility into daemon auto-downloads via IPC polling.
type DaemonTransferTracker struct {
	mu        sync.RWMutex
	active    map[string]*DaemonBatchStatus
	recent    []DaemonBatchStatus
	maxRecent int
}

// NewDaemonTransferTracker creates a new transfer tracker.
func NewDaemonTransferTracker() *DaemonTransferTracker {
	return &DaemonTransferTracker{
		active:    make(map[string]*DaemonBatchStatus),
		maxRecent: 10,
	}
}

// StartBatch begins tracking a new job download batch.
func (t *DaemonTransferTracker) StartBatch(jobID, jobName string, fileCount int, totalBytes int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.active[jobID] = &DaemonBatchStatus{
		BatchID:       jobID,
		BatchLabel:    "Auto: " + jobName,
		Direction:     "download",
		Total:         fileCount,
		TotalBytes:    totalBytes,
		StartedAt:     now,
		lastSpeedTime: now,
	}
}

// UpdateFileProgress updates the in-progress file's contribution to the batch.
// fraction is 0.0-1.0 of the current file. Speed is computed internally.
func (t *DaemonTransferTracker) UpdateFileProgress(jobID string, fileSize int64, fraction float64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	batch, ok := t.active[jobID]
	if !ok {
		return
	}

	// Track partial bytes of the currently downloading file
	fileBytesNow := int64(float64(fileSize) * fraction)
	batch.activeFileBytes = fileBytesNow
	batch.Active = 1

	// Compute speed from byte deltas
	now := time.Now()
	elapsed := now.Sub(batch.lastSpeedTime).Seconds()
	if elapsed >= 0.5 { // Update speed every 500ms
		currentTotal := batch.BytesDone + fileBytesNow
		byteDelta := currentTotal - batch.lastBytesDone
		if byteDelta > 0 && elapsed > 0 {
			batch.Speed = float64(byteDelta) / elapsed
		}
		batch.lastBytesDone = currentTotal
		batch.lastSpeedTime = now
	}
}

// CompleteFile marks a file as successfully downloaded (or already present).
func (t *DaemonTransferTracker) CompleteFile(jobID string, fileSize int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	batch, ok := t.active[jobID]
	if !ok {
		return
	}

	batch.Completed++
	batch.BytesDone += fileSize
	batch.Active = 0
	batch.activeFileBytes = 0
	// Update speed tracking baseline
	batch.lastBytesDone = batch.BytesDone
	batch.lastSpeedTime = time.Now()
}

// FailFile marks a file as failed (download error or mkdir failure).
func (t *DaemonTransferTracker) FailFile(jobID string, fileSize int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	batch, ok := t.active[jobID]
	if !ok {
		return
	}

	batch.Failed++
	batch.Active = 0
	batch.activeFileBytes = 0
}

// SkipFile removes a file from the batch entirely (invalid filename).
// Decrements both Total and TotalBytes so progress can still reach 100%.
func (t *DaemonTransferTracker) SkipFile(jobID string, fileSize int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	batch, ok := t.active[jobID]
	if !ok {
		return
	}

	batch.Total--
	batch.TotalBytes -= fileSize
	if batch.Total < 0 {
		batch.Total = 0
	}
	if batch.TotalBytes < 0 {
		batch.TotalBytes = 0
	}
}

// FinalizeBatch marks a batch as complete and moves it to recent history.
func (t *DaemonTransferTracker) FinalizeBatch(jobID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	batch, ok := t.active[jobID]
	if !ok {
		return
	}

	batch.CompletedAt = time.Now()
	batch.Active = 0
	batch.Speed = 0

	// Move to recent history
	t.recent = append(t.recent, *batch)
	if len(t.recent) > t.maxRecent {
		t.recent = t.recent[len(t.recent)-t.maxRecent:]
	}

	delete(t.active, jobID)
}

// GetStatus returns all active and recent batches for IPC reporting.
// BytesDone includes partial file progress for smooth progress bar updates.
func (t *DaemonTransferTracker) GetStatus() []DaemonBatchStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]DaemonBatchStatus, 0, len(t.active)+len(t.recent))
	for _, batch := range t.active {
		snapshot := *batch
		// Include partial file progress in BytesDone for display
		snapshot.BytesDone += batch.activeFileBytes
		result = append(result, snapshot)
	}
	result = append(result, t.recent...)
	return result
}

// GetTransferStatus maps the tracker state to an IPC-native struct.
func (d *Daemon) GetTransferStatus() *ipc.TransferStatusData {
	if d.tracker == nil {
		return &ipc.TransferStatusData{}
	}

	batches := d.tracker.GetStatus()
	ipcBatches := make([]ipc.TransferBatchInfo, len(batches))
	for i, b := range batches {
		var completedAt int64
		if !b.CompletedAt.IsZero() {
			completedAt = b.CompletedAt.UnixMilli()
		}
		ipcBatches[i] = ipc.TransferBatchInfo{
			BatchID:     b.BatchID,
			BatchLabel:  b.BatchLabel,
			Total:       b.Total,
			Completed:   b.Completed,
			Failed:      b.Failed,
			Active:      b.Active,
			TotalBytes:  b.TotalBytes,
			BytesDone:   b.BytesDone,
			Speed:       b.Speed,
			StartedAt:   b.StartedAt.UnixMilli(),
			CompletedAt: completedAt,
		}
	}

	return &ipc.TransferStatusData{Batches: ipcBatches}
}
