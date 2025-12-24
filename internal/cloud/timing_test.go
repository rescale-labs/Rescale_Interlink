package cloud

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTimingEnabled(t *testing.T) {
	// Save original value
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)

	// Test disabled (not set)
	os.Unsetenv("RESCALE_TIMING")
	if TimingEnabled() {
		t.Error("TimingEnabled() should return false when RESCALE_TIMING is not set")
	}

	// Test disabled (set to 0)
	os.Setenv("RESCALE_TIMING", "0")
	if TimingEnabled() {
		t.Error("TimingEnabled() should return false when RESCALE_TIMING=0")
	}

	// Test enabled
	os.Setenv("RESCALE_TIMING", "1")
	if !TimingEnabled() {
		t.Error("TimingEnabled() should return true when RESCALE_TIMING=1")
	}

	// Test with other value
	os.Setenv("RESCALE_TIMING", "true")
	if TimingEnabled() {
		t.Error("TimingEnabled() should return false when RESCALE_TIMING is not exactly '1'")
	}
}

func TestTimingLog(t *testing.T) {
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)

	var buf bytes.Buffer

	// Test with timing disabled
	os.Unsetenv("RESCALE_TIMING")
	TimingLog(&buf, "test message %d", 123)
	if buf.Len() > 0 {
		t.Error("TimingLog should not write when timing is disabled")
	}

	// Test with timing enabled
	os.Setenv("RESCALE_TIMING", "1")
	buf.Reset()
	TimingLog(&buf, "test message %d", 123)
	output := buf.String()
	if !strings.Contains(output, "[TIMING]") {
		t.Error("TimingLog output should contain [TIMING] prefix")
	}
	if !strings.Contains(output, "test message 123") {
		t.Error("TimingLog output should contain the formatted message")
	}
}

func TestTimingLogNilWriter(t *testing.T) {
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)

	// Test with nil writer (should not panic)
	os.Setenv("RESCALE_TIMING", "1")
	// This should not panic
	TimingLog(nil, "test message")
}

func TestTimer(t *testing.T) {
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)
	os.Setenv("RESCALE_TIMING", "1")

	var buf bytes.Buffer

	// Test basic timer
	timer := StartTimer(&buf, "test phase")
	time.Sleep(10 * time.Millisecond)
	elapsed := timer.Stop()

	// Check elapsed is reasonable
	if elapsed < 10*time.Millisecond {
		t.Error("Timer elapsed should be at least 10ms")
	}

	// Check output contains expected strings
	output := buf.String()
	if !strings.Contains(output, "[TIMING] test phase: started") {
		t.Error("Timer output should contain start message")
	}
	if !strings.Contains(output, "[TIMING] test phase:") {
		t.Error("Timer output should contain stop message")
	}
}

func TestTimerStopIdempotent(t *testing.T) {
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)
	os.Setenv("RESCALE_TIMING", "1")

	var buf bytes.Buffer

	timer := StartTimer(&buf, "idempotent test")
	timer.Stop()
	firstOutput := buf.String()

	// Stop again - should not log again
	timer.Stop()
	secondOutput := buf.String()

	// Output should be the same (no additional logging)
	if firstOutput != secondOutput {
		t.Error("Timer.Stop() should be idempotent - second call should not log")
	}
}

func TestTimerConcurrentStop(t *testing.T) {
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)
	os.Setenv("RESCALE_TIMING", "1")

	var buf bytes.Buffer
	timer := StartTimer(&buf, "concurrent test")

	// Concurrent Stop calls should be safe
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			timer.Stop()
		}()
	}
	wg.Wait()

	// Should only have one stop message
	output := buf.String()
	stopCount := strings.Count(output, "concurrent test:")
	// Should have start (1) + stop (1) = 2 occurrences
	if stopCount != 2 {
		t.Errorf("Expected 2 occurrences of timer name, got %d", stopCount)
	}
}

func TestTimerElapsed(t *testing.T) {
	var buf bytes.Buffer
	timer := StartTimer(&buf, "elapsed test")
	time.Sleep(10 * time.Millisecond)

	// Elapsed should work without stopping
	elapsed := timer.Elapsed()
	if elapsed < 10*time.Millisecond {
		t.Error("Timer.Elapsed() should return at least 10ms")
	}

	// Timer should still be able to stop
	timer.Stop()
}

func TestTimerStopWithThroughput(t *testing.T) {
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)
	os.Setenv("RESCALE_TIMING", "1")

	var buf bytes.Buffer
	timer := StartTimer(&buf, "throughput test")
	time.Sleep(10 * time.Millisecond)
	timer.StopWithThroughput(1024 * 1024 * 10) // 10 MB

	output := buf.String()
	if !strings.Contains(output, "MB") {
		t.Error("StopWithThroughput output should contain MB")
	}
	if !strings.Contains(output, "MB/s") {
		t.Error("StopWithThroughput output should contain MB/s")
	}
}

func TestTimerStopWithMessage(t *testing.T) {
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)
	os.Setenv("RESCALE_TIMING", "1")

	var buf bytes.Buffer
	timer := StartTimer(&buf, "message test")
	time.Sleep(10 * time.Millisecond)
	timer.StopWithMessage("processed %d items", 42)

	output := buf.String()
	if !strings.Contains(output, "processed 42 items") {
		t.Error("StopWithMessage output should contain custom message")
	}
}

func TestTimerDisabled(t *testing.T) {
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)
	os.Unsetenv("RESCALE_TIMING")

	var buf bytes.Buffer
	timer := StartTimer(&buf, "disabled test")
	time.Sleep(10 * time.Millisecond)
	elapsed := timer.Stop()

	// Timer should still track time even when logging is disabled
	if elapsed < 10*time.Millisecond {
		t.Error("Timer should track time even when timing is disabled")
	}

	// But should not output anything
	if buf.Len() > 0 {
		t.Error("Timer should not output when timing is disabled")
	}
}

func TestPartTimer(t *testing.T) {
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)
	os.Setenv("RESCALE_TIMING", "1")

	var buf bytes.Buffer
	pt := NewPartTimer(&buf, "upload", 5)

	// Record some parts
	pt.RecordPart(1, 10*time.Millisecond, 100*time.Millisecond, 1024*1024)
	pt.RecordPart(2, 15*time.Millisecond, 120*time.Millisecond, 1024*1024)
	pt.RecordPart(3, 12*time.Millisecond, 110*time.Millisecond, 1024*1024)

	output := buf.String()
	if !strings.Contains(output, "Part 1/5") {
		t.Error("PartTimer should log part 1")
	}
	if !strings.Contains(output, "Part 2/5") {
		t.Error("PartTimer should log part 2")
	}
	if !strings.Contains(output, "Part 3/5") {
		t.Error("PartTimer should log part 3")
	}

	// Check stats
	completed, totalBytes, avgSpeed := pt.GetStats()
	if completed != 3 {
		t.Errorf("Expected 3 completed parts, got %d", completed)
	}
	if totalBytes != 3*1024*1024 {
		t.Errorf("Expected %d total bytes, got %d", 3*1024*1024, totalBytes)
	}
	if avgSpeed <= 0 {
		t.Error("Average speed should be positive")
	}

	// Log summary
	pt.Summary()
	finalOutput := buf.String()
	if !strings.Contains(finalOutput, "summary") {
		t.Error("PartTimer.Summary() should log summary")
	}
}

func TestPartTimerConcurrent(t *testing.T) {
	original := os.Getenv("RESCALE_TIMING")
	defer os.Setenv("RESCALE_TIMING", original)
	os.Setenv("RESCALE_TIMING", "1")

	var buf bytes.Buffer
	pt := NewPartTimer(&buf, "concurrent", 100)

	var wg sync.WaitGroup
	for i := 1; i <= 100; i++ {
		wg.Add(1)
		go func(partNum int) {
			defer wg.Done()
			pt.RecordPart(partNum, 10*time.Millisecond, 50*time.Millisecond, 1024)
		}(i)
	}
	wg.Wait()

	completed, totalBytes, _ := pt.GetStats()
	if completed != 100 {
		t.Errorf("Expected 100 completed parts, got %d", completed)
	}
	if totalBytes != 100*1024 {
		t.Errorf("Expected %d total bytes, got %d", 100*1024, totalBytes)
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
