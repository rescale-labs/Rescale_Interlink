// Package wailsapp provides transfer-related Wails bindings.
package wailsapp

import (
	"context"
	"time"

	"github.com/rescale/rescale-int/internal/services"
	"github.com/rescale/rescale-int/internal/transfer"
)

// TransferRequestDTO is the JSON-safe version of services.TransferRequest.
type TransferRequestDTO struct {
	Type        string   `json:"type"`                  // "upload" or "download"
	Source      string   `json:"source"`                // Local path (upload) or file ID (download)
	Dest        string   `json:"dest"`                  // Folder ID (upload) or local path (download)
	Name        string   `json:"name"`                  // Display name
	Size        int64    `json:"size"`                  // File size in bytes
	SourceLabel string   `json:"sourceLabel,omitempty"` // "PUR", "SingleJob", "FileBrowser"
	BatchID     string   `json:"batchID,omitempty"`
	BatchLabel  string   `json:"batchLabel,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// TransferTaskDTO is the JSON-safe version of services.TransferTask.
type TransferTaskDTO struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`                  // "upload" or "download"
	State       string  `json:"state"`                 // queued, initializing, active, completed, failed, cancelled
	Name        string  `json:"name"`                  // Display name
	Source      string  `json:"source"`                // Source path or ID
	Dest        string  `json:"dest"`                  // Destination path or ID
	Size        int64   `json:"size"`                  // Total size in bytes
	SourceLabel string  `json:"sourceLabel,omitempty"` // "PUR", "SingleJob", "FileBrowser"
	BatchID     string  `json:"batchID,omitempty"`
	BatchLabel  string  `json:"batchLabel,omitempty"`
	Progress    float64 `json:"progress"`              // 0.0 to 1.0
	Speed       float64 `json:"speed"`                 // bytes/sec
	Error       string  `json:"error,omitempty"`
	CreatedAt   string  `json:"createdAt"`
	StartedAt   string  `json:"startedAt,omitempty"`
	CompletedAt string  `json:"completedAt,omitempty"`
}

// TransferStatsDTO is the JSON-safe version of services.TransferStats.
type TransferStatsDTO struct {
	Queued       int `json:"queued"`
	Initializing int `json:"initializing"`
	Active       int `json:"active"`
	Paused       int `json:"paused"`
	Completed    int `json:"completed"`
	Failed       int `json:"failed"`
	Cancelled    int `json:"cancelled"`
	Total        int `json:"total"`
}

// StartTransfers initiates one or more transfers.
// Returns immediately; progress is published via events.
func (a *App) StartTransfers(requests []TransferRequestDTO) error {
	if a.engine == nil {
		return ErrNoEngine
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return ErrNoTransferService
	}

	// Convert DTOs to service requests
	reqs := make([]services.TransferRequest, len(requests))
	for i, r := range requests {
		reqs[i] = services.TransferRequest{
			Type:        services.TransferType(r.Type),
			Source:      r.Source,
			Dest:        r.Dest,
			Name:        r.Name,
			Size:        r.Size,
			SourceLabel: r.SourceLabel,
			BatchID:     r.BatchID,
			BatchLabel:  r.BatchLabel,
			Tags:        r.Tags,
		}
	}

	ctx := context.Background()
	return ts.StartTransfers(ctx, reqs)
}

func (a *App) CancelTransfer(taskID string) error {
	if a.engine == nil {
		return ErrNoEngine
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return ErrNoTransferService
	}

	return ts.CancelTransfer(taskID)
}

func (a *App) CancelAllTransfers() {
	if a.engine == nil {
		return
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return
	}

	ts.CancelAll()
}

// RetryTransfer retries a failed or cancelled transfer.
// Returns the new task ID.
func (a *App) RetryTransfer(taskID string) (string, error) {
	if a.engine == nil {
		return "", ErrNoEngine
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return "", ErrNoTransferService
	}

	return ts.RetryTransfer(taskID)
}

func (a *App) GetTransferStats() TransferStatsDTO {
	if a.engine == nil {
		return TransferStatsDTO{}
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return TransferStatsDTO{}
	}

	stats := ts.GetStats()
	return TransferStatsDTO{
		Queued:       stats.Queued,
		Initializing: stats.Initializing,
		Active:       stats.Active,
		Paused:       stats.Paused,
		Completed:    stats.Completed,
		Failed:       stats.Failed,
		Cancelled:    stats.Cancelled,
		Total:        stats.Total(),
	}
}

// GetTransferTasks returns all tracked transfers.
// Returns empty slice instead of nil to prevent frontend null errors.
func (a *App) GetTransferTasks() []TransferTaskDTO {
	if a.engine == nil {
		return []TransferTaskDTO{}
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return []TransferTaskDTO{}
	}

	tasks := ts.GetTasks()
	dtos := make([]TransferTaskDTO, len(tasks))
	for i, t := range tasks {
		dtos[i] = transferTaskToDTO(t)
	}
	return dtos
}

// ClearCompletedTransfers removes completed/failed/cancelled transfers from tracking.
func (a *App) ClearCompletedTransfers() {
	if a.engine == nil {
		return
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return
	}

	ts.ClearCompleted()
}

// TransferBatchDTO is the JSON-safe aggregate view of a batch of transfers.
type TransferBatchDTO struct {
	BatchID         string  `json:"batchID"`
	BatchLabel      string  `json:"batchLabel"`
	Direction       string  `json:"direction"`              // "upload" or "download"
	SourceLabel     string  `json:"sourceLabel"`            // "FileBrowser", "PUR", "SingleJob"
	Total           int     `json:"total"`
	Queued          int     `json:"queued"`
	Active          int     `json:"active"`
	Completed       int     `json:"completed"`
	Failed          int     `json:"failed"`
	Cancelled       int     `json:"cancelled"`
	TotalBytes      int64   `json:"totalBytes"`
	Progress        float64 `json:"progress"`               // byte-weighted 0.0-1.0
	Speed           float64 `json:"speed"`                  // aggregate bytes/sec
	TotalKnown      bool    `json:"totalKnown"`             // true when scan complete
	FilesPerSec     float64 `json:"filesPerSec"`            // file completion rate (windowed)
	ETASeconds      float64 `json:"etaSeconds"`             // estimated time remaining (-1 = unknown)
	DiscoveredTotal int     `json:"discoveredTotal"`
	DiscoveredBytes int64   `json:"discoveredBytes"`
	StartedAtUnix   int64   `json:"startedAtUnix"`
}

// GetTransferBatches returns aggregate stats for each batch of transfers.
// Lightweight call for Transfers tab polling (returns ~200 bytes per batch).
func (a *App) GetTransferBatches() []TransferBatchDTO {
	if a.engine == nil {
		return []TransferBatchDTO{}
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return []TransferBatchDTO{}
	}

	batchStats := ts.GetQueue().GetAllBatchStats()
	dtos := make([]TransferBatchDTO, len(batchStats))
	for i, bs := range batchStats {
		dtos[i] = TransferBatchDTO{
			BatchID:         bs.BatchID,
			BatchLabel:      bs.BatchLabel,
			Direction:        bs.Direction,
			SourceLabel:     bs.SourceLabel,
			Total:           bs.Total,
			Queued:          bs.Queued,
			Active:          bs.Active,
			Completed:       bs.Completed,
			Failed:          bs.Failed,
			Cancelled:       bs.Cancelled,
			TotalBytes:      bs.TotalBytes,
			Progress:        bs.Progress,
			Speed:           bs.Speed,
			TotalKnown:      bs.TotalKnown,
			FilesPerSec:     bs.FilesPerSec,
			ETASeconds:      bs.ETASeconds,
			DiscoveredTotal: bs.DiscoveredTotal,
			DiscoveredBytes: bs.DiscoveredBytes,
			StartedAtUnix:   bs.StartedAt.Unix(),
		}
	}
	return dtos
}

// GetUngroupedTransferTasks returns only tasks with no BatchID.
// Used in polling path when batches exist, avoiding the full task list IPC payload.
func (a *App) GetUngroupedTransferTasks() []TransferTaskDTO {
	if a.engine == nil {
		return []TransferTaskDTO{}
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return []TransferTaskDTO{}
	}

	tasks := ts.GetQueue().GetUngroupedTasks()
	dtos := make([]TransferTaskDTO, len(tasks))
	for i := range tasks {
		dtos[i] = transferTaskToDTO(serviceTaskFromQueueTask(&tasks[i]))
	}
	return dtos
}

// GetBatchTasks returns paginated tasks for a specific batch.
func (a *App) GetBatchTasks(batchID string, offset int, limit int, stateFilter string) []TransferTaskDTO {
	if a.engine == nil {
		return []TransferTaskDTO{}
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return []TransferTaskDTO{}
	}

	tasks := ts.GetQueue().GetBatchTasks(batchID, offset, limit, stateFilter)
	dtos := make([]TransferTaskDTO, len(tasks))
	for i := range tasks {
		dtos[i] = transferTaskToDTO(serviceTaskFromQueueTask(&tasks[i]))
	}
	return dtos
}

// CancelBatch cancels all non-terminal tasks in a batch.
// Also handles queued tasks (standard CancelTransfer only handles active/initializing).
func (a *App) CancelBatch(batchID string) error {
	if a.engine == nil {
		return ErrNoEngine
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return ErrNoTransferService
	}

	return ts.GetQueue().CancelBatch(batchID)
}

// RetryFailedInBatch retries all failed tasks in a batch.
func (a *App) RetryFailedInBatch(batchID string) error {
	if a.engine == nil {
		return ErrNoEngine
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return ErrNoTransferService
	}

	return ts.GetQueue().RetryFailedInBatch(batchID)
}

// serviceTaskFromQueueTask converts a transfer.TransferTask to services.TransferTask.
// Takes pointer to avoid copying sync.RWMutex embedded in TransferTask.
func serviceTaskFromQueueTask(qt *transfer.TransferTask) services.TransferTask {
	return services.TransferTask{
		ID:          qt.ID,
		Type:        services.TransferType(qt.Type),
		State:       services.TransferState(qt.State),
		Name:        qt.Name,
		Source:      qt.Source,
		Dest:        qt.Dest,
		Size:        qt.Size,
		SourceLabel: qt.SourceLabel,
		BatchID:     qt.BatchID,
		BatchLabel:  qt.BatchLabel,
		Progress:    qt.Progress,
		Speed:       qt.Speed,
		Error:       qt.Error,
		CreatedAt:   qt.CreatedAt,
		StartedAt:   qt.StartedAt,
		CompletedAt: qt.CompletedAt,
	}
}

// transferTaskToDTO converts a services.TransferTask to a DTO.
func transferTaskToDTO(t services.TransferTask) TransferTaskDTO {
	dto := TransferTaskDTO{
		ID:          t.ID,
		Type:        string(t.Type),
		State:       string(t.State),
		Name:        t.Name,
		Source:      t.Source,
		Dest:        t.Dest,
		Size:        t.Size,
		SourceLabel: t.SourceLabel,
		BatchID:     t.BatchID,
		BatchLabel:  t.BatchLabel,
		Progress:    t.Progress,
		Speed:       t.Speed,
		CreatedAt:   t.CreatedAt.Format(time.RFC3339),
	}

	if t.Error != nil {
		dto.Error = t.Error.Error()
	}
	if !t.StartedAt.IsZero() {
		dto.StartedAt = t.StartedAt.Format(time.RFC3339)
	}
	if !t.CompletedAt.IsZero() {
		dto.CompletedAt = t.CompletedAt.Format(time.RFC3339)
	}

	return dto
}
