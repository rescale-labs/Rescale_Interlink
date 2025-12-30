// Package wailsapp provides transfer-related Wails bindings.
package wailsapp

import (
	"context"
	"time"

	"github.com/rescale/rescale-int/internal/services"
)

// TransferRequestDTO is the JSON-safe version of services.TransferRequest.
type TransferRequestDTO struct {
	Type   string `json:"type"`   // "upload" or "download"
	Source string `json:"source"` // Local path (upload) or file ID (download)
	Dest   string `json:"dest"`   // Folder ID (upload) or local path (download)
	Name   string `json:"name"`   // Display name
	Size   int64  `json:"size"`   // File size in bytes
}

// TransferTaskDTO is the JSON-safe version of services.TransferTask.
type TransferTaskDTO struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`     // "upload" or "download"
	State       string  `json:"state"`    // queued, initializing, active, completed, failed, cancelled
	Name        string  `json:"name"`     // Display name
	Source      string  `json:"source"`   // Source path or ID
	Dest        string  `json:"dest"`     // Destination path or ID
	Size        int64   `json:"size"`     // Total size in bytes
	Progress    float64 `json:"progress"` // 0.0 to 1.0
	Speed       float64 `json:"speed"`    // bytes/sec
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
			Type:   services.TransferType(r.Type),
			Source: r.Source,
			Dest:   r.Dest,
			Name:   r.Name,
			Size:   r.Size,
		}
	}

	ctx := context.Background()
	return ts.StartTransfers(ctx, reqs)
}

// CancelTransfer cancels an active transfer.
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

// CancelAllTransfers cancels all active transfers.
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

// GetTransferStats returns current transfer statistics.
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
func (a *App) GetTransferTasks() []TransferTaskDTO {
	if a.engine == nil {
		return nil
	}

	ts := a.engine.TransferService()
	if ts == nil {
		return nil
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

// transferTaskToDTO converts a services.TransferTask to a DTO.
func transferTaskToDTO(t services.TransferTask) TransferTaskDTO {
	dto := TransferTaskDTO{
		ID:        t.ID,
		Type:      string(t.Type),
		State:     string(t.State),
		Name:      t.Name,
		Source:    t.Source,
		Dest:      t.Dest,
		Size:      t.Size,
		Progress:  t.Progress,
		Speed:     t.Speed,
		CreatedAt: t.CreatedAt.Format(time.RFC3339),
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
