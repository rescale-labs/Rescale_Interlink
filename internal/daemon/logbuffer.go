// Package daemon provides background service functionality for auto-downloading completed jobs.
// v4.3.2: Log buffer for IPC streaming and recent log retrieval.
package daemon

import (
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/ipc"
)

// LogBuffer maintains a circular buffer of recent log entries for IPC streaming.
// v4.3.2: Allows GUI to fetch recent daemon logs and receive new entries.
type LogBuffer struct {
	mu       sync.RWMutex
	entries  []ipc.LogEntryData
	maxSize  int
	writeIdx int
	count    int

	// Subscribers for real-time streaming
	subMu       sync.RWMutex
	subscribers map[string]chan *ipc.LogEntryData
	nextSubID   int
}

// NewLogBuffer creates a new log buffer with the specified capacity.
func NewLogBuffer(maxSize int) *LogBuffer {
	if maxSize <= 0 {
		maxSize = 1000 // Default to 1000 entries
	}
	return &LogBuffer{
		entries:     make([]ipc.LogEntryData, maxSize),
		maxSize:     maxSize,
		subscribers: make(map[string]chan *ipc.LogEntryData),
	}
}

// Add adds a log entry to the buffer and notifies subscribers.
func (lb *LogBuffer) Add(level, stage, message string, fields map[string]interface{}) {
	entry := ipc.LogEntryData{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     level,
		Stage:     stage,
		Message:   message,
		Fields:    fields,
	}

	// Add to buffer
	lb.mu.Lock()
	lb.entries[lb.writeIdx] = entry
	lb.writeIdx = (lb.writeIdx + 1) % lb.maxSize
	if lb.count < lb.maxSize {
		lb.count++
	}
	lb.mu.Unlock()

	// Notify subscribers (non-blocking)
	lb.subMu.RLock()
	for _, ch := range lb.subscribers {
		select {
		case ch <- &entry:
		default:
			// Channel full, skip (subscriber is slow)
		}
	}
	lb.subMu.RUnlock()
}

// GetRecent returns the most recent N log entries.
func (lb *LogBuffer) GetRecent(n int) []ipc.LogEntryData {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	if n <= 0 || lb.count == 0 {
		return nil
	}
	if n > lb.count {
		n = lb.count
	}

	result := make([]ipc.LogEntryData, n)

	// Calculate starting position (oldest of the N entries we want)
	startIdx := (lb.writeIdx - n + lb.maxSize) % lb.maxSize

	for i := 0; i < n; i++ {
		idx := (startIdx + i) % lb.maxSize
		result[i] = lb.entries[idx]
	}

	return result
}

// Subscribe creates a new subscription channel for real-time log streaming.
// Returns subscription ID and channel. Caller must call Unsubscribe when done.
func (lb *LogBuffer) Subscribe() (string, <-chan *ipc.LogEntryData) {
	lb.subMu.Lock()
	defer lb.subMu.Unlock()

	lb.nextSubID++
	id := string(rune(lb.nextSubID))
	ch := make(chan *ipc.LogEntryData, 100) // Buffer 100 entries

	lb.subscribers[id] = ch
	return id, ch
}

// Unsubscribe removes a subscription.
func (lb *LogBuffer) Unsubscribe(id string) {
	lb.subMu.Lock()
	defer lb.subMu.Unlock()

	if ch, ok := lb.subscribers[id]; ok {
		close(ch)
		delete(lb.subscribers, id)
	}
}

// Clear removes all entries from the buffer.
func (lb *LogBuffer) Clear() {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	lb.entries = make([]ipc.LogEntryData, lb.maxSize)
	lb.writeIdx = 0
	lb.count = 0
}
