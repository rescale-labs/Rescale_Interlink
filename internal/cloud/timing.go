// Package cloud provides cloud storage transfer functionality.
// timing.go - Transfer timing instrumentation for diagnostics (v3.6.2)
//
// Enable timing output by setting RESCALE_TIMING=1 environment variable.
// Output format: [TIMING] phase_name: duration (optional_details)
//
// Example output:
//   [TIMING] Upload initialization: 45ms
//   [TIMING] Part 1/10: encrypted=12ms uploaded=850ms size=32.0 MB
//   [TIMING] Streaming upload: 9.2s (total 320.0 MB at 34.8 MB/s)
package cloud

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// TimingEnabled returns true if RESCALE_TIMING=1 environment variable is set.
// When enabled, detailed timing information is logged during transfers.
// This is useful for diagnosing performance issues, especially on Windows.
func TimingEnabled() bool {
	return os.Getenv("RESCALE_TIMING") == "1"
}

// TimingLog writes a timing message to the writer if RESCALE_TIMING=1.
// If writer is nil, os.Stderr is used.
// Format: [TIMING] message
func TimingLog(w io.Writer, format string, args ...interface{}) {
	if !TimingEnabled() {
		return
	}
	if w == nil {
		w = os.Stderr
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(w, "[TIMING] %s\n", msg)
}

// Timer tracks elapsed time for a named phase.
// Use StartTimer to create a timer and Stop to log the elapsed time.
// Thread-safe and idempotent (Stop can be called multiple times safely).
type Timer struct {
	name    string
	start   time.Time
	w       io.Writer
	stopped int32 // atomic flag
}

// StartTimer creates a new timer and logs the start if timing is enabled.
// The timer will use os.Stderr if writer is nil.
func StartTimer(w io.Writer, name string) *Timer {
	if w == nil {
		w = os.Stderr
	}
	t := &Timer{
		name:  name,
		start: time.Now(),
		w:     w,
	}
	if TimingEnabled() {
		fmt.Fprintf(w, "[TIMING] %s: started\n", name)
	}
	return t
}

// Stop logs the elapsed time and returns the duration.
// Safe to call multiple times; only the first call logs and returns accurate timing.
func (t *Timer) Stop() time.Duration {
	elapsed := time.Since(t.start)
	if atomic.CompareAndSwapInt32(&t.stopped, 0, 1) {
		if TimingEnabled() {
			fmt.Fprintf(t.w, "[TIMING] %s: %v\n", t.name, elapsed)
		}
	}
	return elapsed
}

// Elapsed returns the current elapsed time without stopping the timer.
func (t *Timer) Elapsed() time.Duration {
	return time.Since(t.start)
}

// StopWithThroughput logs elapsed time with throughput information.
// Useful for transfer operations where bytes processed is known.
func (t *Timer) StopWithThroughput(bytes int64) time.Duration {
	elapsed := time.Since(t.start)
	if atomic.CompareAndSwapInt32(&t.stopped, 0, 1) {
		if TimingEnabled() {
			bytesPerSec := float64(bytes) / elapsed.Seconds()
			fmt.Fprintf(t.w, "[TIMING] %s: %v (total %s at %s)\n",
				t.name, elapsed, FormatBytes(bytes), FormatSpeed(bytesPerSec))
		}
	}
	return elapsed
}

// StopWithMessage logs a custom message with the elapsed time.
func (t *Timer) StopWithMessage(format string, args ...interface{}) time.Duration {
	elapsed := time.Since(t.start)
	if atomic.CompareAndSwapInt32(&t.stopped, 0, 1) {
		if TimingEnabled() {
			msg := fmt.Sprintf(format, args...)
			fmt.Fprintf(t.w, "[TIMING] %s: %v (%s)\n", t.name, elapsed, msg)
		}
	}
	return elapsed
}

// PartTimer tracks timing for individual parts in a multipart transfer.
// It provides aggregate statistics and per-part timing when enabled.
type PartTimer struct {
	name       string
	w          io.Writer
	totalParts int
	mu         sync.Mutex

	// Per-part tracking
	completedParts int
	totalBytes     int64
	totalDuration  time.Duration

	// Rolling average for throughput
	recentSpeeds []float64
	maxRecent    int
}

// NewPartTimer creates a new part timer for tracking multipart operations.
func NewPartTimer(w io.Writer, name string, totalParts int) *PartTimer {
	if w == nil {
		w = os.Stderr
	}
	return &PartTimer{
		name:       name,
		w:          w,
		totalParts: totalParts,
		maxRecent:  10, // Rolling window for speed average
	}
}

// RecordPart logs timing for a completed part and updates aggregate stats.
// encryptDuration and transferDuration are the times spent in each phase.
// bytesTransferred is the size of the part.
func (pt *PartTimer) RecordPart(partNum int, encryptDuration, transferDuration time.Duration, bytesTransferred int64) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	pt.completedParts++
	pt.totalBytes += bytesTransferred
	pt.totalDuration += transferDuration

	// Calculate speed for this part
	if transferDuration > 0 {
		speed := float64(bytesTransferred) / transferDuration.Seconds()
		pt.recentSpeeds = append(pt.recentSpeeds, speed)
		if len(pt.recentSpeeds) > pt.maxRecent {
			pt.recentSpeeds = pt.recentSpeeds[1:]
		}
	}

	if !TimingEnabled() {
		return
	}

	// Log part timing
	fmt.Fprintf(pt.w, "[TIMING] Part %d/%d: encrypted=%v transferred=%v size=%s\n",
		partNum, pt.totalParts, encryptDuration, transferDuration, FormatBytes(bytesTransferred))
}

// RecordPartSimple logs timing for operations that don't have separate encrypt/transfer phases.
func (pt *PartTimer) RecordPartSimple(partNum int, duration time.Duration, bytesTransferred int64) {
	pt.RecordPart(partNum, 0, duration, bytesTransferred)
}

// Summary logs aggregate statistics for all parts.
func (pt *PartTimer) Summary() {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	if !TimingEnabled() || pt.completedParts == 0 {
		return
	}

	avgSpeed := float64(0)
	if pt.totalDuration > 0 {
		avgSpeed = float64(pt.totalBytes) / pt.totalDuration.Seconds()
	}

	// Calculate rolling average speed
	rollingAvg := float64(0)
	if len(pt.recentSpeeds) > 0 {
		sum := float64(0)
		for _, s := range pt.recentSpeeds {
			sum += s
		}
		rollingAvg = sum / float64(len(pt.recentSpeeds))
	}

	fmt.Fprintf(pt.w, "[TIMING] %s summary: %d parts, %s total, avg=%s rolling=%s\n",
		pt.name, pt.completedParts, FormatBytes(pt.totalBytes),
		FormatSpeed(avgSpeed), FormatSpeed(rollingAvg))
}

// GetStats returns current statistics without logging.
func (pt *PartTimer) GetStats() (completedParts int, totalBytes int64, avgSpeed float64) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	completedParts = pt.completedParts
	totalBytes = pt.totalBytes
	if pt.totalDuration > 0 {
		avgSpeed = float64(pt.totalBytes) / pt.totalDuration.Seconds()
	}
	return
}

// FormatBytes returns a human-readable byte count.
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// FormatSpeed returns a human-readable speed in bytes/second.
func FormatSpeed(bytesPerSec float64) string {
	if bytesPerSec < 1024 {
		return fmt.Sprintf("%.1f B/s", bytesPerSec)
	}
	if bytesPerSec < 1024*1024 {
		return fmt.Sprintf("%.1f KB/s", bytesPerSec/1024)
	}
	return fmt.Sprintf("%.1f MB/s", bytesPerSec/(1024*1024))
}
