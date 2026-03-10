package logging

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/events"
)

// logCollector safely collects LogEvents from an EventBus subscriber goroutine.
type logCollector struct {
	mu   sync.Mutex
	logs []events.LogEvent
}

func (c *logCollector) add(le events.LogEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, le)
}

func (c *logCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.logs)
}

func (c *logCollector) get(i int) events.LogEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logs[i]
}

// collectingEventBus creates an EventBus and a thread-safe log collector.
func collectingEventBus(t *testing.T) (*events.EventBus, *logCollector) {
	t.Helper()
	eb := events.NewEventBus(0)
	collector := &logCollector{}
	ch := eb.Subscribe(events.EventLog)
	go func() {
		for evt := range ch {
			if le, ok := evt.(*events.LogEvent); ok {
				collector.add(*le)
			}
		}
	}()
	// Give the subscriber goroutine time to start
	time.Sleep(10 * time.Millisecond)
	return eb, collector
}

func TestTeeWriter_PassThrough(t *testing.T) {
	var buf bytes.Buffer
	eb := events.NewEventBus(0)
	tw := NewTeeWriter(&buf, eb)

	input := []byte("hello world\n")
	n, err := tw.Write(input)
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != len(input) {
		t.Errorf("Write returned %d, want %d", n, len(input))
	}
	if buf.String() != string(input) {
		t.Errorf("underlying got %q, want %q", buf.String(), string(input))
	}
}

func TestClassifyLine_BATCH(t *testing.T) {
	level, stage := classifyLine("[BATCH] scaling workers 5 -> 8")
	if level != events.DebugLevel {
		t.Errorf("level = %v, want DebugLevel", level)
	}
	if stage != "BATCH" {
		t.Errorf("stage = %q, want %q", stage, "BATCH")
	}
}

func TestClassifyLine_CRED(t *testing.T) {
	level, stage := classifyLine("[CRED] credential refresh failed")
	if level != events.WarnLevel {
		t.Errorf("level = %v, want WarnLevel", level)
	}
	if stage != "CRED" {
		t.Errorf("stage = %q, want %q", stage, "CRED")
	}
}

func TestClassifyLine_SLOT(t *testing.T) {
	level, stage := classifyLine("[SLOT] DOWNLOAD file.txt: waiting")
	if level != events.DebugLevel {
		t.Errorf("level = %v, want DebugLevel", level)
	}
	if stage != "SLOT" {
		t.Errorf("stage = %q, want %q", stage, "SLOT")
	}
}

func TestClassifyLine_TIMING(t *testing.T) {
	level, stage := classifyLine("[TIMING] credential pre-warm complete")
	if level != events.InfoLevel {
		t.Errorf("level = %v, want InfoLevel", level)
	}
	if stage != "TIMING" {
		t.Errorf("stage = %q, want %q", stage, "TIMING")
	}
}

func TestClassifyLine_Unknown(t *testing.T) {
	level, stage := classifyLine("some random log output")
	if level != events.DebugLevel {
		t.Errorf("level = %v, want DebugLevel", level)
	}
	if stage != "backend" {
		t.Errorf("stage = %q, want %q", stage, "backend")
	}
}

func TestClassifyLine_BracketButNotTag(t *testing.T) {
	// Brackets with lowercase or non-alpha content should not match known prefixes
	level, stage := classifyLine("[something] lower case tag")
	if level != events.DebugLevel {
		t.Errorf("level = %v, want DebugLevel", level)
	}
	if stage != "backend" {
		t.Errorf("stage = %q, want %q", stage, "backend")
	}
}

func TestStripStdlibTimestamp(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"2026/03/10 14:05:23 [BATCH] scaling workers", "[BATCH] scaling workers"},
		{"2026/01/01 00:00:00 hello", "hello"},
		{"[BATCH] no timestamp", "[BATCH] no timestamp"},
		{"short", "short"},
		{"", ""},
		{"not a timestamp at all here", "not a timestamp at all here"},
	}
	for _, tt := range tests {
		got := stripStdlibTimestamp(tt.input)
		if got != tt.want {
			t.Errorf("stripStdlibTimestamp(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTeeWriter_Throttle(t *testing.T) {
	var buf bytes.Buffer
	eb, collector := collectingEventBus(t)
	tw := NewTeeWriter(&buf, eb)
	tw.interval = 100 * time.Millisecond // short interval for testing

	// Send 10 rapid [SLOT] messages — only the first should pass through
	for i := 0; i < 10; i++ {
		tw.Write([]byte("[SLOT] message\n"))
	}

	// Allow time for event processing
	time.Sleep(50 * time.Millisecond)

	// Should have exactly 1 (throttled to 1 per 100ms, all sent in <1ms)
	if got := collector.count(); got != 1 {
		t.Errorf("throttle: got %d events, want 1", got)
	}

	// Wait for interval to pass, send another — should be allowed
	time.Sleep(110 * time.Millisecond)
	tw.Write([]byte("[SLOT] another message\n"))
	time.Sleep(50 * time.Millisecond)

	if got := collector.count(); got != 2 {
		t.Errorf("after interval: got %d events, want 2", got)
	}

	eb.Close()
}

func TestTeeWriter_NoThrottleForBATCH(t *testing.T) {
	var buf bytes.Buffer
	eb, collector := collectingEventBus(t)
	tw := NewTeeWriter(&buf, eb)
	tw.interval = 100 * time.Millisecond

	// Send 5 rapid [BATCH] messages — all should pass through (not throttled)
	for i := 0; i < 5; i++ {
		tw.Write([]byte("[BATCH] message\n"))
	}

	time.Sleep(50 * time.Millisecond)

	if got := collector.count(); got != 5 {
		t.Errorf("no-throttle BATCH: got %d events, want 5", got)
	}

	eb.Close()
}

func TestTeeWriter_StdlibTimestampStripped(t *testing.T) {
	var buf bytes.Buffer
	eb, collector := collectingEventBus(t)
	tw := NewTeeWriter(&buf, eb)

	tw.Write([]byte("2026/03/10 14:05:23 [BATCH] scaling workers 5 -> 8\n"))

	time.Sleep(50 * time.Millisecond)

	if got := collector.count(); got != 1 {
		t.Fatalf("got %d events, want 1", got)
	}
	msg := collector.get(0).Message
	if msg != "[BATCH] scaling workers 5 -> 8" {
		t.Errorf("message = %q, want timestamp stripped", msg)
	}

	eb.Close()
}

func TestTeeWriter_NilEventBus(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTeeWriter(&buf, nil)

	// Should not panic
	n, err := tw.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != 6 {
		t.Errorf("Write returned %d, want 6", n)
	}
}
