package progress

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
	"golang.org/x/term"
)

// DownloadUI manages multiple concurrent download progress bars using mpb
type DownloadUI struct {
	progress   *mpb.Progress
	bars       sync.Map // fileID -> *DownloadFileBar
	isTerminal bool
	totalFiles int
	completed  int32
}

// DownloadFileBar represents a single file download progress bar
type DownloadFileBar struct {
	bar        *mpb.Bar
	ui         *DownloadUI
	index      int
	fileID     string
	remoteName string
	localPath  string
	size       int64
	retries    int32
	startTime  time.Time
	lastUpdate time.Time
	lastBytes  int64
}

// NewDownloadUI creates a new download UI with the given number of total files
func NewDownloadUI(totalFiles int) *DownloadUI {
	isTerminal := term.IsTerminal(int(os.Stderr.Fd()))

	var p *mpb.Progress
	if isTerminal {
		// Enable ANSI escape sequences on Windows for proper progress bar rendering
		enableANSIOnWindows(os.Stderr)

		p = mpb.New(
			mpb.WithOutput(os.Stderr),
			mpb.WithRefreshRate(300*time.Millisecond), // ~3 times per second
			mpb.WithWidth(100),
		)
	} else {
		// Non-TTY: disable progress bars, just use text output
		p = mpb.New(mpb.WithOutput(io.Discard))
	}

	return &DownloadUI{
		progress:   p,
		isTerminal: isTerminal,
		totalFiles: totalFiles,
	}
}

// AddFileBar creates a new progress bar for a file download
func (u *DownloadUI) AddFileBar(index int, fileID, remoteName, localPath string, size int64) *DownloadFileBar {
	// Truncate local path to last 2 components (shorter for readability)
	destPath := truncatePath(localPath, 2)

	fb := &DownloadFileBar{
		ui:         u,
		index:      index,
		fileID:     fileID,
		remoteName: remoteName,
		localPath:  localPath,
		size:       size,
		startTime:  time.Now(),
		lastUpdate: time.Now(),
	}

	if u.isTerminal {
		fb.bar = u.progress.New(size,
			// Custom bar style with Unicode block characters
			mpb.BarStyle().
				Lbound("[").
				Filler("█").  // U+2588 - Full block for completed portion
				Tip("█").     // Full block at leading edge
				Padding("░"). // U+2591 - Light shade for remaining portion
				Rbound("]"),
			mpb.PrependDecorators(
				// Dynamic decorator for label with retry count
				decor.Any(func(s decor.Statistics) string {
					retries := atomic.LoadInt32(&fb.retries)
					base := fmt.Sprintf("[%d/%d] %s (%.1f MiB) ← %s",
						fb.index, u.totalFiles,
						destPath,
						float64(size)/(1024*1024),
						remoteName)
					if retries > 0 {
						return fmt.Sprintf("%s (retry %d)", base, retries)
					}
					return base
				}, decor.WCSyncSpace),
			),
			mpb.AppendDecorators(
				decor.CountersKibiByte("% .1f / % .1f", decor.WCSyncSpace),
				decor.Name("  "),
				// v3.2.2: Custom percentage with consistent 2 decimal places
				decor.Any(func(s decor.Statistics) string {
					pct := float64(s.Current) / float64(s.Total) * 100
					if s.Total == 0 {
						pct = 0
					}
					return fmt.Sprintf("%6.2f%%", pct)
				}, decor.WCSyncSpace),
				decor.Name("  "),
				// v3.2.2: Increased EWMA age from 30 to 60 for smoother speed display
				decor.EwmaSpeed(decor.SizeB1024(0), "% .1f", 60, decor.WCSyncSpace),
				decor.Name("  "),
				decor.Name("ETA ", decor.WCSyncWidth),
				decor.EwmaETA(decor.ET_STYLE_GO, 60),
			),
			mpb.BarRemoveOnComplete(),
		)
	} else {
		// Non-TTY: print simple start message
		fmt.Printf("Downloading [%d/%d]: %s (%.1f MiB) ← %s\n",
			index, u.totalFiles,
			truncatePath(localPath, 2),
			float64(size)/(1024*1024),
			remoteName)
	}

	u.bars.Store(fileID, fb)
	return fb
}

// UpdateProgress updates the progress bar based on a fraction (0.0 to 1.0)
// Uses EWMA timing for accurate speed and ETA calculations
// Throttles updates to reduce visual noise and improve performance
func (f *DownloadFileBar) UpdateProgress(fraction float64) {
	if f.bar == nil {
		return
	}

	now := time.Now()
	elapsed := now.Sub(f.lastUpdate)

	currentBytes := int64(fraction * float64(f.size))
	bytesDelta := currentBytes - f.lastBytes

	// THROTTLE: Update every 300ms minimum to ensure smooth ticker-driven updates
	// The key insight: ticker calls us even when no bytes have changed (bytesDelta == 0)
	// We MUST always call EwmaIncrBy to let MPB track time passage for speed/ETA
	const updateInterval = 300 * time.Millisecond

	if elapsed >= updateInterval {
		// Always update MPB with elapsed time, even if no bytes transferred
		// This keeps EWMA speed calculation accurate
		f.bar.EwmaIncrBy(int(bytesDelta), elapsed)
		f.lastBytes = currentBytes
		f.lastUpdate = now
	}
}

// SetRetry updates the retry counter and visually marks the bar
func (f *DownloadFileBar) SetRetry(count int) {
	atomic.StoreInt32(&f.retries, int32(count))
	if f.bar != nil && count > 0 {
		// SetRefill shows a visual indication of retry
		f.bar.SetRefill(f.lastBytes)
	}
}

// ResetStartTime resets the start time to now (used to exclude preparation time from transfer rate)
func (f *DownloadFileBar) ResetStartTime() {
	f.startTime = time.Now()
}

// Complete marks the download as finished and prints a summary
func (f *DownloadFileBar) Complete(err error) {
	elapsed := time.Since(f.startTime)
	speed := float64(f.size) / elapsed.Seconds() / (1024 * 1024) // MB/s

	if err == nil {
		if f.bar != nil {
			// ENSURE exact 100% completion (no rounding errors)
			f.bar.SetCurrent(f.size)
			f.bar.SetTotal(f.size, true) // Mark done, trigger BarRemoveOnComplete
		}

		// Success message
		msg := fmt.Sprintf("✓ %s ← %s (FileID: %s, %.1f MiB, %s, %.1f MiB/s)\n",
			truncatePath(f.localPath, 2),
			f.remoteName,
			f.fileID,
			float64(f.size)/(1024*1024),
			elapsed.Round(time.Second),
			speed)

		// CRITICAL: Write through mpb's writer (not stdout) to avoid triggering redraws
		if f.ui.isTerminal && f.ui.progress != nil {
			f.ui.progress.Write([]byte(msg))
		} else {
			// Non-TTY: print directly to stdout
			fmt.Print(msg)
		}
	} else {
		// Error: keep bar visible if terminal, print error
		if f.bar != nil {
			f.bar.Abort(false) // false = don't remove (show failure)
		}

		retries := atomic.LoadInt32(&f.retries)
		msg := fmt.Sprintf("✗ %s ← %s: %v (after %d retries)\n",
			truncatePath(f.localPath, 2),
			f.remoteName,
			err,
			retries)

		// Write through mpb's writer (not stdout)
		if f.ui.isTerminal && f.ui.progress != nil {
			f.ui.progress.Write([]byte(msg))
		} else {
			fmt.Print(msg)
		}
	}

	atomic.AddInt32(&f.ui.completed, 1)
}

// Wait blocks until all progress bars complete
func (u *DownloadUI) Wait() {
	if u.progress != nil {
		u.progress.Wait()
	}
}

// LogWriter returns an io.Writer that safely prints above the progress bars
func (u *DownloadUI) LogWriter() io.Writer {
	if u.progress != nil && u.isTerminal {
		return u.progress
	}
	return os.Stderr
}

// Writer returns an io.Writer for output during progress operations.
// Implements the ProgressUI interface.
func (u *DownloadUI) Writer() io.Writer {
	return u.LogWriter()
}

// GetCompleted returns the number of completed downloads
func (u *DownloadUI) GetCompleted() int {
	return int(atomic.LoadInt32(&u.completed))
}

// IsTerminal returns whether output is to a terminal
func (u *DownloadUI) IsTerminal() bool {
	return u.isTerminal
}
