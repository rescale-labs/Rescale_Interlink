package events

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/constants"
)

// EventType defines the types of events that can be emitted
type EventType string

const (
	EventProgress    EventType = "progress"
	EventLog         EventType = "log"
	EventStateChange EventType = "state_change"
	EventError       EventType = "error"
	EventComplete    EventType = "complete"

	// v3.6.3: Transfer queue events
	EventTransferQueued       EventType = "transfer_queued"       // Task added to queue
	EventTransferInitializing EventType = "transfer_initializing" // Acquired slot, initializing
	EventTransferStarted      EventType = "transfer_started"      // Actual transfer began (bytes moving)
	EventTransferProgress     EventType = "transfer_progress"     // Progress update
	EventTransferCompleted    EventType = "transfer_completed"    // Successfully completed
	EventTransferFailed       EventType = "transfer_failed"       // Failed with error
	EventTransferCancelled    EventType = "transfer_cancelled"    // Cancelled by user

	// v3.6.3: Configuration change events
	EventConfigChanged EventType = "config_changed" // API key or config changed, caches should be invalidated
)

// LogLevel defines log severity levels
type LogLevel int

const (
	DebugLevel LogLevel = iota
	InfoLevel
	WarnLevel
	ErrorLevel
)

func (l LogLevel) String() string {
	switch l {
	case DebugLevel:
		return "DEBUG"
	case InfoLevel:
		return "INFO"
	case WarnLevel:
		return "WARN"
	case ErrorLevel:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Event is the base interface for all events
type Event interface {
	Type() EventType
	Timestamp() time.Time
}

// BaseEvent provides common event fields
type BaseEvent struct {
	EventType EventType
	Time      time.Time
}

func (e BaseEvent) Type() EventType      { return e.EventType }
func (e BaseEvent) Timestamp() time.Time { return e.Time }

// ProgressEvent represents progress updates
type ProgressEvent struct {
	BaseEvent
	JobName      string
	Stage        string  // "tar", "upload", "create", "submit", "overall"
	Progress     float64 // 0.0 to 1.0
	BytesCurrent int64
	BytesTotal   int64
	Message      string
	Rate         float64 // bytes/sec
	ETA          time.Duration
}

// LogEvent represents log messages
type LogEvent struct {
	BaseEvent
	Level   LogLevel
	Message string
	Stage   string
	JobName string
	Error   error
}

// StateChangeEvent represents job state transitions
type StateChangeEvent struct {
	BaseEvent
	JobName        string
	OldStatus      string
	NewStatus      string
	Stage          string
	JobID          string
	ErrorMessage   string
	UploadProgress float64 // 0.0 to 1.0, only used for upload stage
}

// ErrorEvent represents error conditions
type ErrorEvent struct {
	BaseEvent
	JobName   string
	Stage     string
	Error     error
	Retryable bool
}

// CompleteEvent represents pipeline completion
type CompleteEvent struct {
	BaseEvent
	TotalJobs   int
	SuccessJobs int
	FailedJobs  int
	Duration    time.Duration
}

// TransferEvent represents transfer queue events (v3.6.3)
type TransferEvent struct {
	BaseEvent
	TaskID   string  // Unique task ID
	TaskType string  // "upload" or "download"
	Name     string  // Display name (filename)
	Size     int64   // File size in bytes
	Progress float64 // 0.0 to 1.0
	Speed    float64 // bytes/sec
	Error    error   // Error if failed
}

// ConfigChangedEvent represents configuration changes (v3.6.3)
// Published when API key or other identity-related config changes.
// Subscribers should invalidate caches and re-authenticate.
type ConfigChangedEvent struct {
	BaseEvent
	Source string // "direct_input", "env_var", "token_file"
	Email  string // User email after successful auth (empty if auth failed)
}

// EventBus manages event subscriptions and publishing
type EventBus struct {
	subscribers   map[EventType][]chan Event
	all           []chan Event // Subscribers to all events
	mu            sync.RWMutex
	bufferSize    int
	closed        bool
	droppedEvents atomic.Int64 // Count of dropped events due to full buffers
}

// NewEventBus creates a new event bus with specified buffer size
func NewEventBus(bufferSize int) *EventBus {
	if bufferSize <= 0 {
		bufferSize = constants.EventBusDefaultBuffer // Use optimized default (1000)
	}
	if bufferSize > constants.EventBusMaxBuffer {
		bufferSize = constants.EventBusMaxBuffer // Cap at maximum
	}
	return &EventBus{
		subscribers: make(map[EventType][]chan Event),
		all:         make([]chan Event, 0),
		bufferSize:  bufferSize,
	}
}

// Subscribe creates a subscription to a specific event type
func (eb *EventBus) Subscribe(eventType EventType) <-chan Event {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if eb.closed {
		ch := make(chan Event)
		close(ch)
		return ch
	}

	ch := make(chan Event, eb.bufferSize)
	eb.subscribers[eventType] = append(eb.subscribers[eventType], ch)
	return ch
}

// SubscribeAll creates a subscription to all events
func (eb *EventBus) SubscribeAll() <-chan Event {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if eb.closed {
		ch := make(chan Event)
		close(ch)
		return ch
	}

	ch := make(chan Event, eb.bufferSize)
	eb.all = append(eb.all, ch)
	return ch
}

// Publish sends an event to all subscribers (non-blocking with optimized buffer)
func (eb *EventBus) Publish(event Event) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	if eb.closed {
		return
	}

	// Send to specific type subscribers
	for _, ch := range eb.subscribers[event.Type()] {
		select {
		case ch <- event:
			// Successfully sent
		default:
			// Channel full - event dropped
			// Increment atomic counter for monitoring
			dropped := eb.droppedEvents.Add(1)
			// Log warning every 100 drops to avoid log spam
			if dropped%100 == 0 {
				// Note: In production, this should use structured logging
				// For now, we just track the metric
			}
		}
	}

	// Send to all-events subscribers
	for _, ch := range eb.all {
		select {
		case ch <- event:
			// Successfully sent
		default:
			// Channel full - event dropped
			eb.droppedEvents.Add(1)
		}
	}
}

// Close shuts down the event bus and closes all channels
func (eb *EventBus) Close() {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if eb.closed {
		return
	}

	eb.closed = true

	// Close specific type channels
	for _, channels := range eb.subscribers {
		for _, ch := range channels {
			close(ch)
		}
	}

	// Close all-events channels
	for _, ch := range eb.all {
		close(ch)
	}
}

// PublishLog is a convenience method for publishing log events
func (eb *EventBus) PublishLog(level LogLevel, message, stage, jobName string, err error) {
	eb.Publish(&LogEvent{
		BaseEvent: BaseEvent{
			EventType: EventLog,
			Time:      time.Now(),
		},
		Level:   level,
		Message: message,
		Stage:   stage,
		JobName: jobName,
		Error:   err,
	})
}

// PublishProgress is a convenience method for publishing progress events
func (eb *EventBus) PublishProgress(jobName, stage string, progress float64, message string) {
	eb.Publish(&ProgressEvent{
		BaseEvent: BaseEvent{
			EventType: EventProgress,
			Time:      time.Now(),
		},
		JobName:  jobName,
		Stage:    stage,
		Progress: progress,
		Message:  message,
	})
}

// PublishStateChange is a convenience method for publishing state change events
func (eb *EventBus) PublishStateChange(jobName, oldStatus, newStatus, stage, jobID, errorMsg string) {
	eb.Publish(&StateChangeEvent{
		BaseEvent: BaseEvent{
			EventType: EventStateChange,
			Time:      time.Now(),
		},
		JobName:      jobName,
		OldStatus:    oldStatus,
		NewStatus:    newStatus,
		Stage:        stage,
		JobID:        jobID,
		ErrorMessage: errorMsg,
	})
}

// Unsubscribe removes a subscription channel from a specific event type
// This prevents memory leaks from abandoned subscriptions
func (eb *EventBus) Unsubscribe(eventType EventType, ch <-chan Event) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if eb.closed {
		return
	}

	// Find and remove the channel from the event type's subscribers
	subscribers := eb.subscribers[eventType]
	for i, subCh := range subscribers {
		if subCh == ch {
			// Remove channel by replacing with last element and truncating
			subscribers[i] = subscribers[len(subscribers)-1]
			eb.subscribers[eventType] = subscribers[:len(subscribers)-1]
			break
		}
	}
}

// UnsubscribeAll removes a subscription channel from all event types
// Use this when cleaning up a subscriber that subscribed to multiple event types
func (eb *EventBus) UnsubscribeAll(ch <-chan Event) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if eb.closed {
		return
	}

	// Remove from all event type subscribers
	for eventType, subscribers := range eb.subscribers {
		for i, subCh := range subscribers {
			if subCh == ch {
				subscribers[i] = subscribers[len(subscribers)-1]
				eb.subscribers[eventType] = subscribers[:len(subscribers)-1]
				break
			}
		}
	}

	// Remove from all-events subscribers
	for i, subCh := range eb.all {
		if subCh == ch {
			eb.all[i] = eb.all[len(eb.all)-1]
			eb.all = eb.all[:len(eb.all)-1]
			break
		}
	}
}

// GetDroppedEventCount returns the total number of events dropped due to full buffers
// Useful for monitoring and detecting if buffer sizes need adjustment
func (eb *EventBus) GetDroppedEventCount() int64 {
	return eb.droppedEvents.Load()
}

// ResetDroppedEventCount resets the dropped event counter to zero
// Useful for periodic monitoring windows
func (eb *EventBus) ResetDroppedEventCount() int64 {
	return eb.droppedEvents.Swap(0)
}
