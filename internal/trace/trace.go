package trace

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// EventTrace tracks an event through the system
type EventTrace struct {
	ID          int64
	JobName     string
	Stage       string
	Status      string
	Timestamp   time.Time
	GoroutineID uint64
	Location    string
	Phase       string // "published", "received", "processing", "completed"
}

var (
	traces      sync.Map // map[int64][]EventTrace
	eventSeq    int64
	traceActive int32 = 1 // Set to 0 to disable tracing
)

// NewEventID creates a new event ID
func NewEventID() int64 {
	return atomic.AddInt64(&eventSeq, 1)
}

// Log records a trace point for an event
func Log(eventID int64, jobName, stage, status, phase string) {
	if atomic.LoadInt32(&traceActive) == 0 {
		return
	}

	trace := EventTrace{
		ID:          eventID,
		JobName:     jobName,
		Stage:       stage,
		Status:      status,
		Timestamp:   time.Now(),
		GoroutineID: getGoroutineID(),
		Location:    getCaller(3),
		Phase:       phase,
	}

	// Store trace
	var traceList []EventTrace
	if existing, ok := traces.Load(eventID); ok {
		traceList = existing.([]EventTrace)
	}
	traceList = append(traceList, trace)
	traces.Store(eventID, traceList)

	// Log to console
	log.Printf("[TRACE-%d] %s | job=%s stage=%s status=%s | goroutine=%d | %s",
		eventID, phase, jobName, stage, status, trace.GoroutineID, trace.Location)
}

// GetTrace retrieves all trace points for an event
func GetTrace(eventID int64) []EventTrace {
	if val, ok := traces.Load(eventID); ok {
		return val.([]EventTrace)
	}
	return nil
}

// DumpAll dumps all traces
func DumpAll() {
	log.Println("=== DUMPING ALL TRACES ===")
	traces.Range(func(key, value interface{}) bool {
		eventID := key.(int64)
		traceList := value.([]EventTrace)
		log.Printf("\nEvent %d:", eventID)
		for _, t := range traceList {
			log.Printf("  [%s] %s - job=%s stage=%s status=%s (goroutine %d)",
				t.Timestamp.Format("15:04:05.000"), t.Phase, t.JobName, t.Stage, t.Status, t.GoroutineID)
		}
		return true
	})
	log.Println("=== END TRACES ===")
}

// GetStats returns statistics about traced events
func GetStats() map[string]int {
	stats := make(map[string]int)
	traces.Range(func(key, value interface{}) bool {
		traceList := value.([]EventTrace)
		stats["total_events"]++

		phases := make(map[string]bool)
		for _, t := range traceList {
			phases[t.Phase] = true
		}

		if phases["published"] {
			stats["published"]++
		}
		if phases["received"] {
			stats["received"]++
		}
		if phases["processing"] {
			stats["processing"]++
		}
		if phases["completed"] {
			stats["completed"]++
		}

		return true
	})
	return stats
}

func getGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// Parse goroutine ID from stack trace like "goroutine 123 [running]:"
	var id uint64
	fmt.Sscanf(string(buf[:n]), "goroutine %d", &id)
	return id
}

func getCaller(skip int) string {
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "unknown"
	}
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return fmt.Sprintf("%s:%d", file, line)
	}
	return fmt.Sprintf("%s:%d", fn.Name(), line)
}
