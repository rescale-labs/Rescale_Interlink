package cloud

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// withTiming sets RESCALE_TIMING for the test, or unsets it when value is "".
func withTiming(t *testing.T, value string) {
	t.Setenv("RESCALE_TIMING", value)
	if value == "" {
		os.Unsetenv("RESCALE_TIMING")
	}
}

func TestTimingEnabled(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "1": true, "true": false} {
		withTiming(t, value)
		if got := TimingEnabled(); got != want {
			t.Errorf("TimingEnabled() with RESCALE_TIMING=%q = %v, want %v", value, got, want)
		}
	}
}

func TestTimingLog(t *testing.T) {
	var buf bytes.Buffer

	withTiming(t, "")
	TimingLog(&buf, "test message %d", 123)
	if buf.Len() > 0 {
		t.Error("TimingLog should not write when timing is disabled")
	}

	withTiming(t, "1")
	TimingLog(&buf, "test message %d", 123)
	if output := buf.String(); !strings.Contains(output, "[TIMING] test message 123") {
		t.Errorf("TimingLog output = %q, want the prefixed, formatted message", output)
	}

	TimingLog(nil, "test message") // a nil writer falls back to stderr
}

func TestTimer(t *testing.T) {
	withTiming(t, "1")

	var buf bytes.Buffer
	timer := StartTimer(&buf, "test phase")
	time.Sleep(10 * time.Millisecond)
	if elapsed := timer.StopWithMessage("processed %d items", 42); elapsed < 10*time.Millisecond {
		t.Errorf("elapsed = %v, want at least 10ms", elapsed)
	}

	output := buf.String()
	for _, want := range []string{"[TIMING] test phase: started", "[TIMING] test phase:", "processed 42 items"} {
		if !strings.Contains(output, want) {
			t.Errorf("timer output %q does not contain %q", output, want)
		}
	}
}

// Only the first stop logs, however many goroutines race to stop the timer.
func TestTimerStopsOnce(t *testing.T) {
	withTiming(t, "1")

	var buf bytes.Buffer
	timer := StartTimer(&buf, "concurrent test")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			timer.StopWithThroughput(1024)
		}()
	}
	wg.Wait()
	timer.StopWithMessage("late")

	if n := strings.Count(buf.String(), "concurrent test:"); n != 2 {
		t.Errorf("timer name logged %d times, want 2 (start and one stop)", n)
	}
}

func TestTimerStopWithThroughput(t *testing.T) {
	withTiming(t, "1")

	var buf bytes.Buffer
	timer := StartTimer(&buf, "throughput test")
	time.Sleep(10 * time.Millisecond)
	timer.StopWithThroughput(1024 * 1024 * 10) // 10 MB

	if output := buf.String(); !strings.Contains(output, "MB/s") {
		t.Errorf("StopWithThroughput output %q should contain MB/s", output)
	}
}

func TestTimerDisabled(t *testing.T) {
	withTiming(t, "")

	var buf bytes.Buffer
	timer := StartTimer(&buf, "disabled test")
	time.Sleep(10 * time.Millisecond)
	if elapsed := timer.StopWithThroughput(1); elapsed < 10*time.Millisecond {
		t.Errorf("elapsed = %v, want at least 10ms even with timing disabled", elapsed)
	}
	if buf.Len() > 0 {
		t.Errorf("timer wrote %q with timing disabled", buf.String())
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
	}

	for _, tt := range tests {
		result := FormatBytes(tt.bytes)
		if result != tt.expected {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.bytes, result, tt.expected)
		}
	}
}

func TestFormatSpeed(t *testing.T) {
	tests := []struct {
		bytesPerSec float64
		expected    string
	}{
		{0, "0.0 B/s"},
		{512, "512.0 B/s"},
		{1024, "1.0 KB/s"},
		{1024 * 1024, "1.0 MB/s"},
		{1024 * 1024 * 10, "10.0 MB/s"},
		{1024 * 1024 * 100.5, "100.5 MB/s"},
	}

	for _, tt := range tests {
		result := FormatSpeed(tt.bytesPerSec)
		if result != tt.expected {
			t.Errorf("FormatSpeed(%f) = %q, want %q", tt.bytesPerSec, result, tt.expected)
		}
	}
}
