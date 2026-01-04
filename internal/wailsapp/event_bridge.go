// Package wailsapp provides the event bridge between Go EventBus and Wails runtime.
package wailsapp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/rescale/rescale-int/internal/events"
)

// EventBridge forwards events from internal EventBus to Wails runtime.
type EventBridge struct {
	ctx          context.Context
	eventBus     *events.EventBus
	subscription <-chan events.Event

	// Throttling for high-frequency events
	lastProgress     map[string]time.Time
	progressInterval time.Duration

	stopC   chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	started bool // v4.0.0: Track if bridge has been started to prevent double-start
}

// NewEventBridge creates a new event bridge.
func NewEventBridge(ctx context.Context, eventBus *events.EventBus) *EventBridge {
	return &EventBridge{
		ctx:              ctx,
		eventBus:         eventBus,
		lastProgress:     make(map[string]time.Time),
		progressInterval: 100 * time.Millisecond, // Throttle to 10 updates/sec
		stopC:            make(chan struct{}),
	}
}

// Start begins forwarding events.
// v4.0.0: Added protection against double-start and nil subscription.
// v4.0.5: Fixed race condition by holding lock during subscription.
func (eb *EventBridge) Start() error {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if eb.started {
		wailsLogger.Warn().Msg("Event bridge already started, ignoring duplicate Start()")
		return nil
	}

	eb.subscription = eb.eventBus.SubscribeAll()
	if eb.subscription == nil {
		return fmt.Errorf("event bridge: failed to subscribe to event bus")
	}

	eb.started = true
	eb.wg.Add(1)
	go eb.forwardLoop()

	wailsLogger.Debug().Msg("Event bridge started")
	return nil
}

// Stop stops forwarding events.
// v4.0.0: Added protection against double-stop.
// v4.0.5: Added cleanup of lastProgress map to prevent memory leak.
func (eb *EventBridge) Stop() {
	eb.mu.Lock()
	if !eb.started {
		eb.mu.Unlock()
		wailsLogger.Warn().Msg("Event bridge not started or already stopped")
		return
	}
	eb.started = false
	// v4.0.5: Clear lastProgress map to free memory
	eb.lastProgress = make(map[string]time.Time)
	sub := eb.subscription // Capture subscription under lock
	eb.mu.Unlock()

	close(eb.stopC)
	eb.wg.Wait()
	eb.eventBus.UnsubscribeAll(sub)

	wailsLogger.Debug().Msg("Event bridge stopped")
}

func (eb *EventBridge) forwardLoop() {
	defer eb.wg.Done()

	for {
		select {
		case event, ok := <-eb.subscription:
			if !ok {
				return
			}
			eb.forwardEvent(event)

		case <-eb.stopC:
			return
		}
	}
}

func (eb *EventBridge) forwardEvent(event events.Event) {
	switch e := event.(type) {
	case *events.ProgressEvent:
		if eb.shouldThrottle(e.JobName) {
			return
		}
		runtime.EventsEmit(eb.ctx, "interlink:progress", progressEventToDTO(e))

	case *events.LogEvent:
		runtime.EventsEmit(eb.ctx, "interlink:log", logEventToDTO(e))

	case *events.StateChangeEvent:
		runtime.EventsEmit(eb.ctx, "interlink:state_change", stateChangeEventToDTO(e))

	case *events.ErrorEvent:
		runtime.EventsEmit(eb.ctx, "interlink:error", errorEventToDTO(e))

	case *events.CompleteEvent:
		runtime.EventsEmit(eb.ctx, "interlink:complete", completeEventToDTO(e))

	case *events.TransferEvent:
		// v4.0.0: Only throttle PROGRESS events - never throttle terminal states
		// (failed, completed, cancelled). These must always be delivered to UI.
		isTerminalState := e.Type() == events.EventTransferFailed ||
			e.Type() == events.EventTransferCompleted ||
			e.Type() == events.EventTransferCancelled
		if !isTerminalState && eb.shouldThrottle(e.TaskID) {
			return
		}
		runtime.EventsEmit(eb.ctx, "interlink:transfer", transferEventToDTO(e))

	case *events.EnumerationEvent:
		// v4.0.8: Enumeration events for folder scan progress
		// Don't throttle these - they're infrequent and important for UX
		runtime.EventsEmit(eb.ctx, "interlink:enumeration", enumerationEventToDTO(e))

	case *events.ScanProgressEvent:
		// v4.0.8: Scan progress events for software/hardware catalog scanning
		// Don't throttle - these are infrequent and important for UX
		runtime.EventsEmit(eb.ctx, "interlink:scan_progress", scanProgressEventToDTO(e))
	}
}

func (eb *EventBridge) shouldThrottle(key string) bool {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	now := time.Now()
	if last, ok := eb.lastProgress[key]; ok {
		if now.Sub(last) < eb.progressInterval {
			return true
		}
	}
	eb.lastProgress[key] = now
	return false
}

// DTO conversion functions for JSON-safe serialization

// ProgressEventDTO is the JSON-safe version of events.ProgressEvent.
type ProgressEventDTO struct {
	Timestamp    string  `json:"timestamp"`
	JobName      string  `json:"jobName"`
	Stage        string  `json:"stage"`
	Progress     float64 `json:"progress"`
	BytesCurrent int64   `json:"bytesCurrent"`
	BytesTotal   int64   `json:"bytesTotal"`
	Message      string  `json:"message"`
	RateBytes    float64 `json:"rateBytes"`
	ETAMs        int64   `json:"etaMs"`
}

func progressEventToDTO(e *events.ProgressEvent) ProgressEventDTO {
	return ProgressEventDTO{
		Timestamp:    e.Timestamp().Format(time.RFC3339Nano),
		JobName:      e.JobName,
		Stage:        e.Stage,
		Progress:     e.Progress,
		BytesCurrent: e.BytesCurrent,
		BytesTotal:   e.BytesTotal,
		Message:      e.Message,
		RateBytes:    e.Rate,
		ETAMs:        e.ETA.Milliseconds(),
	}
}

// LogEventDTO is the JSON-safe version of events.LogEvent.
type LogEventDTO struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Stage     string `json:"stage"`
	JobName   string `json:"jobName"`
	Error     string `json:"error,omitempty"`
}

func logEventToDTO(e *events.LogEvent) LogEventDTO {
	dto := LogEventDTO{
		Timestamp: e.Timestamp().Format(time.RFC3339Nano),
		Level:     e.Level.String(),
		Message:   e.Message,
		Stage:     e.Stage,
		JobName:   e.JobName,
	}
	if e.Error != nil {
		dto.Error = e.Error.Error()
	}
	return dto
}

// StateChangeEventDTO is the JSON-safe version of events.StateChangeEvent.
type StateChangeEventDTO struct {
	Timestamp      string  `json:"timestamp"`
	JobName        string  `json:"jobName"`
	OldStatus      string  `json:"oldStatus"`
	NewStatus      string  `json:"newStatus"`
	Stage          string  `json:"stage"`
	JobID          string  `json:"jobId,omitempty"`
	ErrorMessage   string  `json:"errorMessage,omitempty"`
	UploadProgress float64 `json:"uploadProgress"`
}

func stateChangeEventToDTO(e *events.StateChangeEvent) StateChangeEventDTO {
	return StateChangeEventDTO{
		Timestamp:      e.Timestamp().Format(time.RFC3339Nano),
		JobName:        e.JobName,
		OldStatus:      e.OldStatus,
		NewStatus:      e.NewStatus,
		Stage:          e.Stage,
		JobID:          e.JobID,
		ErrorMessage:   e.ErrorMessage,
		UploadProgress: e.UploadProgress,
	}
}

// ErrorEventDTO is the JSON-safe version of events.ErrorEvent.
type ErrorEventDTO struct {
	Timestamp string `json:"timestamp"`
	JobName   string `json:"jobName"`
	Stage     string `json:"stage"`
	Message   string `json:"message"`
}

func errorEventToDTO(e *events.ErrorEvent) ErrorEventDTO {
	msg := ""
	if e.Error != nil {
		msg = e.Error.Error()
	}
	return ErrorEventDTO{
		Timestamp: e.Timestamp().Format(time.RFC3339Nano),
		JobName:   e.JobName,
		Stage:     e.Stage,
		Message:   msg,
	}
}

// CompleteEventDTO is the JSON-safe version of events.CompleteEvent.
type CompleteEventDTO struct {
	Timestamp   string `json:"timestamp"`
	TotalJobs   int    `json:"totalJobs"`
	SuccessJobs int    `json:"successJobs"`
	FailedJobs  int    `json:"failedJobs"`
	DurationMs  int64  `json:"durationMs"`
}

func completeEventToDTO(e *events.CompleteEvent) CompleteEventDTO {
	return CompleteEventDTO{
		Timestamp:   e.Timestamp().Format(time.RFC3339Nano),
		TotalJobs:   e.TotalJobs,
		SuccessJobs: e.SuccessJobs,
		FailedJobs:  e.FailedJobs,
		DurationMs:  e.Duration.Milliseconds(),
	}
}

// TransferEventDTO is the JSON-safe version of events.TransferEvent.
type TransferEventDTO struct {
	Timestamp string  `json:"timestamp"`
	TaskID    string  `json:"taskId"`
	TaskType  string  `json:"taskType"` // "upload" or "download"
	Name      string  `json:"name"`     // Display name (filename)
	Size      int64   `json:"size"`     // File size in bytes
	Progress  float64 `json:"progress"` // 0.0 to 1.0
	Speed     float64 `json:"speed"`    // bytes/sec
	Error     string  `json:"error,omitempty"`
}

func transferEventToDTO(e *events.TransferEvent) TransferEventDTO {
	dto := TransferEventDTO{
		Timestamp: e.Timestamp().Format(time.RFC3339Nano),
		TaskID:    e.TaskID,
		TaskType:  e.TaskType,
		Name:      e.Name,
		Size:      e.Size,
		Progress:  e.Progress,
		Speed:     e.Speed,
	}
	if e.Error != nil {
		dto.Error = e.Error.Error()
	}
	return dto
}

// EnumerationEventDTO is the JSON-safe version of events.EnumerationEvent (v4.0.8).
type EnumerationEventDTO struct {
	Timestamp    string `json:"timestamp"`
	ID           string `json:"id"`
	FolderName   string `json:"folderName"`
	Direction    string `json:"direction"` // "upload" or "download"
	FoldersFound int    `json:"foldersFound"`
	FilesFound   int    `json:"filesFound"`
	BytesFound   int64  `json:"bytesFound"`
	IsComplete   bool   `json:"isComplete"`
	Error        string `json:"error,omitempty"`
}

func enumerationEventToDTO(e *events.EnumerationEvent) EnumerationEventDTO {
	return EnumerationEventDTO{
		Timestamp:    e.Timestamp().Format(time.RFC3339Nano),
		ID:           e.ID,
		FolderName:   e.FolderName,
		Direction:    e.Direction,
		FoldersFound: e.FoldersFound,
		FilesFound:   e.FilesFound,
		BytesFound:   e.BytesFound,
		IsComplete:   e.IsComplete,
		Error:        e.Error,
	}
}

// ScanProgressEventDTO is the JSON-safe version of events.ScanProgressEvent (v4.0.8).
type ScanProgressEventDTO struct {
	Timestamp  string `json:"timestamp"`
	ScanType   string `json:"scanType"`   // "software" or "hardware"
	Page       int    `json:"page"`       // Current page number
	ItemsFound int    `json:"itemsFound"` // Items discovered so far
	IsComplete bool   `json:"isComplete"` // True when scan finished
	IsCached   bool   `json:"isCached"`   // True if result came from cache
	Error      string `json:"error,omitempty"`
}

func scanProgressEventToDTO(e *events.ScanProgressEvent) ScanProgressEventDTO {
	return ScanProgressEventDTO{
		Timestamp:  e.Timestamp().Format(time.RFC3339Nano),
		ScanType:   e.ScanType,
		Page:       e.Page,
		ItemsFound: e.ItemsFound,
		IsComplete: e.IsComplete,
		IsCached:   e.IsCached,
		Error:      e.Error,
	}
}
