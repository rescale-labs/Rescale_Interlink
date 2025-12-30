// Package cloud provides cloud storage transfer functionality.
// timing.go - Transfer timing instrumentation for diagnostics (v3.6.2, v4.0.0)
//
// Enable timing output by:
//   - Setting RESCALE_TIMING=1 environment variable, OR
//   - Enabling "Detailed Logging" in GUI Settings tab (v4.0.0)
//
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
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/events"
)

// Global state for GUI-mode detailed logging (v4.0.0)
var (
	detailedLoggingEnabled int32           // atomic: 1 = enabled, 0 = disabled
	globalEventBus         *events.EventBus // set by wailsapp for GUI mode
)

// SetDetailedLogging enables or disables detailed logging globally.
// Called from GUI when the checkbox is toggled.
func SetDetailedLogging(enabled bool) {
	if enabled {
		atomic.StoreInt32(&detailedLoggingEnabled, 1)
	} else {
		atomic.StoreInt32(&detailedLoggingEnabled, 0)
	}
}

// SetEventBus sets the global event bus for emitting timing logs to GUI.
// Called from wailsapp during startup.
func SetEventBus(eb *events.EventBus) {
	globalEventBus = eb
}

// TimingEnabled returns true if detailed logging is enabled.
// This can be via RESCALE_TIMING=1 environment variable OR
// via the GUI DetailedLogging checkbox.
func TimingEnabled() bool {
	return os.Getenv("RESCALE_TIMING") == "1" || atomic.LoadInt32(&detailedLoggingEnabled) == 1
}

// TimingLog writes a timing message to the writer if detailed logging is enabled.
// If writer is nil, os.Stderr is used.
// v4.0.0: Also emits to EventBus for GUI Activity tab when available.
// Format: [TIMING] message
func TimingLog(w io.Writer, format string, args ...interface{}) {
	if !TimingEnabled() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	formattedMsg := fmt.Sprintf("[TIMING] %s", msg)

	// Write to provided writer (or stderr)
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, "%s\n", formattedMsg)

	// v4.0.0: Also emit to EventBus for GUI Activity tab
	if globalEventBus != nil {
		globalEventBus.PublishLog(events.DebugLevel, formattedMsg, "timing", "", nil)
	}
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
