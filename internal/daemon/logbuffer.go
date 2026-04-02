// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/ipc"
)

// LogBuffer maintains a circular buffer of recent log entries for IPC streaming.
type LogBuffer struct {
	mu       sync.RWMutex
	entries  []ipc.LogEntryData
	maxSize  int
	writeIdx int
	count    int
}

func NewLogBuffer(maxSize int) *LogBuffer {
	if maxSize <= 0 {
		maxSize = 1000 // Default to 1000 entries
	}
	return &LogBuffer{
		entries: make([]ipc.LogEntryData, maxSize),
		maxSize: maxSize,
	}
}

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
}

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
