package progress

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
	"golang.org/x/term"
)

// UploadUI manages multiple concurrent upload progress bars using mpb
type UploadUI struct {
	progress   *mpb.Progress
	bars       sync.Map // filepath -> *FileBar
	pathCache  sync.Map // folderID -> human path
	isTerminal bool
	totalFiles int
	started    int32 // Atomic counter for file index (1, 2, 3, ...)
	completed  int32
}

// FileBar represents a single file upload progress bar
type FileBar struct {
	bar        *mpb.Bar
	ui         *UploadUI
	index      int
	filepath   string
	folderPath string
	folderID   string
	size       int64
	retries    int32
	startTime  time.Time
	lastUpdate time.Time
	lastBytes  int64
}

// NewUploadUI creates a new upload UI with the given number of total files
func NewUploadUI(totalFiles int) *UploadUI {
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

	return &UploadUI{
		progress:   p,
		isTerminal: isTerminal,
		totalFiles: totalFiles,
	}
}

// SetFolderPath caches a human-readable path for a folder ID
func (u *UploadUI) SetFolderPath(folderID, path string) {
	u.pathCache.Store(folderID, path)
}

// GetFolderPath retrieves the cached human-readable path for a folder ID
func (u *UploadUI) GetFolderPath(folderID string) string {
	if path, ok := u.pathCache.Load(folderID); ok {
		return path.(string)
	}
	return folderID // fallback to ID if no path cached
}

// AddFileBar creates a new progress bar for a file upload
func (u *UploadUI) AddFileBar(localPath, folderID string, size int64) *FileBar {
	folderPath := u.GetFolderPath(folderID)

	// Atomic increment to get unique file index across all concurrent uploads
	index := int(atomic.AddInt32(&u.started, 1))

	// Truncate source path to last 2 components (shorter for readability)
	sourcePath := truncatePath(localPath, 2)

	fb := &FileBar{
		ui:         u,
		index:      index,
		filepath:   localPath,
		folderPath: folderPath,
		folderID:   folderID,
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
					base := fmt.Sprintf("[%d/%d] %s (%.1f MiB) → %s",
						fb.index, u.totalFiles,
						sourcePath,
						float64(size)/(1024*1024),
						folderPath)
					if retries > 0 {
						return fmt.Sprintf("%s (retry %d)", base, retries)
					}
					return base
				}, decor.WCSyncSpace),
			),
			mpb.AppendDecorators(
				decor.CountersKibiByte("% .1f / % .1f", decor.WCSyncSpace),
				decor.Name("  "),
				decor.Percentage(decor.WCSyncSpace),
				decor.Name("  "),
				decor.EwmaSpeed(decor.SizeB1024(0), "% .1f", 30, decor.WCSyncSpace),
				decor.Name("  "),
				decor.Name("ETA ", decor.WCSyncWidth),
				decor.EwmaETA(decor.ET_STYLE_GO, 30),
			),
			mpb.BarRemoveOnComplete(),
		)
	} else {
		// Non-TTY: print simple start message
		fmt.Printf("Uploading [%d/%d]: %s (%.1f MiB) → %s\n",
			fb.index, u.totalFiles,
			truncatePath(localPath, 2),
			float64(size)/(1024*1024),
			folderPath)
	}

	u.bars.Store(localPath, fb)
	return fb
}

// UpdateProgress updates the progress bar based on a fraction (0.0 to 1.0)
// Uses EWMA timing for accurate speed and ETA calculations
// Throttles updates to reduce visual noise and improve performance
func (f *FileBar) UpdateProgress(fraction float64) {
	// Special value -1.0 means "reset start time" (used to exclude encryption time)
	if fraction < 0 {
		f.ResetStartTime()
		return
	}

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
func (f *FileBar) SetRetry(count int) {
	atomic.StoreInt32(&f.retries, int32(count))
	if f.bar != nil && count > 0 {
		// SetRefill shows a visual indication of retry
		f.bar.SetRefill(f.lastBytes)
	}
}

// ResetStartTime resets the start time to now (used to exclude encryption time from transfer rate)
func (f *FileBar) ResetStartTime() {
	f.startTime = time.Now()
}

// Complete marks the upload as finished and prints a summary
func (f *FileBar) Complete(fileID string, err error) {
	elapsed := time.Since(f.startTime)
	speed := float64(f.size) / elapsed.Seconds() / (1024 * 1024) // MB/s

	if err == nil {
		if f.bar != nil {
			// ENSURE exact 100% completion (no rounding errors)
			f.bar.SetCurrent(f.size)
			f.bar.SetTotal(f.size, true) // Mark done, trigger BarRemoveOnComplete
		}

		// Success message
		msg := fmt.Sprintf("✓ %s → %s (FileID: %s, %.1f MiB, %s, %.1f MiB/s)\n",
			truncatePath(f.filepath, 2),
			f.folderPath,
			fileID,
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
		msg := fmt.Sprintf("✗ %s → %s: %v (after %d retries)\n",
			truncatePath(f.filepath, 2),
			f.folderPath,
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
func (u *UploadUI) Wait() {
	if u.progress != nil {
		u.progress.Wait()
	}
}

// LogWriter returns an io.Writer that safely prints above the progress bars
func (u *UploadUI) LogWriter() io.Writer {
	if u.progress != nil && u.isTerminal {
		return u.progress
	}
	return os.Stderr
}

// Writer returns an io.Writer for output during progress operations.
// Implements the ProgressUI interface.
func (u *UploadUI) Writer() io.Writer {
	return u.LogWriter()
}

// IsTerminal returns true if output is to a terminal (progress bars are active).
// Implements the ProgressUI interface.
func (u *UploadUI) IsTerminal() bool {
	return u.isTerminal
}

// truncatePath truncates a file path to show only the last N components
// Example: truncatePath("/a/b/c/d/file.txt", 3) → "…/c/d/file.txt"
func truncatePath(path string, maxComponents int) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) <= maxComponents {
		return filepath.Base(path)
	}
	relevant := parts[len(parts)-maxComponents:]
	return "…/" + strings.Join(relevant, "/")
}

// enableANSIOnWindows enables Virtual Terminal processing on Windows for ANSI escape sequences
// This is a no-op on non-Windows platforms
func enableANSIOnWindows(f *os.File) {
	// Only needed on Windows - this function is platform-specific
	// On non-Windows platforms, this is a no-op stub
	// The actual implementation is in uploadui_windows.go
	if runtime.GOOS == "windows" {
		enableWindowsANSI(f)
	}
}
