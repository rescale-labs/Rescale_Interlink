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

// barGroup is the mpb plumbing behind UploadUI and DownloadUI: one progress
// display, the terminal check that decides whether bars are drawn at all, and
// the log sink the rest of the CLI writes through while they are live. Both UIs
// embed it, so Wait, Writer, LogWriter and IsTerminal are one implementation
// serving uploads and downloads alike.
type barGroup struct {
	progress   *mpb.Progress
	bars       sync.Map // transfer key -> the owning UI's bar
	isTerminal bool

	// totalFiles is the denominator in each bar's "[i/n]" label. Streaming
	// uploads discover files as they go and raise it mid-flight, so it is read
	// atomically even though a fixed-size batch never changes it.
	totalFiles int32
}

func newBarGroup(totalFiles int) *barGroup {
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

	// Only claim the log sink when bars are actually drawn. With mpb writing to
	// io.Discard, routing log output through it would swallow it.
	if isTerminal {
		SetLogSink(p)
	}

	return &barGroup{
		progress:   p,
		isTerminal: isTerminal,
		totalFiles: int32(totalFiles),
	}
}

// Wait blocks until all progress bars complete
func (g *barGroup) Wait() {
	if g.progress != nil {
		ClearLogSink(g.progress)
		g.progress.Wait()
	}
}

// LogWriter returns an io.Writer that safely prints above the progress bars
func (g *barGroup) LogWriter() io.Writer {
	if g.progress != nil && g.isTerminal {
		return g.progress
	}
	return os.Stderr
}

// Writer returns an io.Writer for output during progress operations.
func (g *barGroup) Writer() io.Writer {
	return g.LogWriter()
}

// IsTerminal returns true if output is to a terminal (progress bars are active).
func (g *barGroup) IsTerminal() bool {
	return g.isTerminal
}

// barBase is the per-transfer state behind FileBar and DownloadFileBar: the mpb
// bar itself, the throttled progress accounting, and the summary line. An
// upload and a download differ only in the words around the numbers, so the
// direction-specific parts arrive as arguments: verb ("Uploading"), arrow ("→")
// and peer — the other end of the transfer, which is the destination folder for
// an upload and the remote file name for a download.
type barBase struct {
	bar        *mpb.Bar
	group      *barGroup
	index      int
	size       int64
	retries    int32
	startTime  time.Time
	lastUpdate time.Time

	// lastBytes is read by SetRetry, which runs on the retry callback's
	// goroutine while UpdateProgress writes it from the progress callback's.
	lastBytes atomic.Int64
}

// start draws the bar for one transfer, or prints the start notice when bars are
// switched off.
func (b *barBase) start(verb, arrow, localPath, peer string) {
	g := b.group
	size := b.size

	// Truncate the local path to last 2 components (shorter for readability)
	shortPath := truncatePath(localPath, 2)

	if g.isTerminal {
		b.bar = g.progress.New(size,
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
					retries := atomic.LoadInt32(&b.retries)
					base := fmt.Sprintf("[%d/%d] %s (%.1f MiB) %s %s",
						b.index, atomic.LoadInt32(&g.totalFiles),
						shortPath,
						float64(size)/(1024*1024),
						arrow, peer)
					if retries > 0 {
						return fmt.Sprintf("%s (retry %d)", base, retries)
					}
					return base
				}, decor.WCSyncSpace),
			),
			mpb.AppendDecorators(
				decor.CountersKibiByte("% .1f / % .1f", decor.WCSyncSpace),
				decor.Name("  "),
				decor.Any(func(s decor.Statistics) string {
					pct := float64(s.Current) / float64(s.Total) * 100
					if s.Total == 0 {
						pct = 0
					}
					return fmt.Sprintf("%6.2f%%", pct)
				}, decor.WCSyncSpace),
				decor.Name("  "),
				decor.EwmaSpeed(decor.SizeB1024(0), "% .1f", 60, decor.WCSyncSpace),
				decor.Name("  "),
				decor.Name("ETA ", decor.WCSyncWidth),
				decor.EwmaETA(decor.ET_STYLE_GO, 60),
			),
			mpb.BarRemoveOnComplete(),
		)
	} else {
		// Non-TTY: print simple start message
		fmt.Printf("%s [%d/%d]: %s (%.1f MiB) %s %s\n",
			verb, b.index, atomic.LoadInt32(&g.totalFiles),
			shortPath,
			float64(size)/(1024*1024),
			arrow, peer)
	}
}

// advance moves the bar to a fraction (0.0 to 1.0) of the transfer.
// Uses EWMA timing for accurate speed and ETA calculations.
// Throttles updates to reduce visual noise and improve performance.
func (b *barBase) advance(fraction float64) {
	if b.bar == nil {
		return
	}

	now := time.Now()
	elapsed := now.Sub(b.lastUpdate)

	currentBytes := int64(fraction * float64(b.size))
	bytesDelta := currentBytes - b.lastBytes.Load()

	// THROTTLE: Update every 300ms minimum to ensure smooth ticker-driven updates
	// The key insight: ticker calls us even when no bytes have changed (bytesDelta == 0)
	// We MUST always call EwmaIncrBy to let MPB track time passage for speed/ETA
	const updateInterval = 300 * time.Millisecond

	if elapsed >= updateInterval {
		// Always update MPB with elapsed time, even if no bytes transferred
		// This keeps EWMA speed calculation accurate
		b.bar.EwmaIncrBy(int(bytesDelta), elapsed)
		b.lastBytes.Store(currentBytes)
		b.lastUpdate = now
	}
}

// SetRetry updates the retry counter and visually marks the bar
func (b *barBase) SetRetry(count int) {
	atomic.StoreInt32(&b.retries, int32(count))
	if b.bar != nil && count > 0 {
		// SetRefill shows a visual indication of retry
		b.bar.SetRefill(b.lastBytes.Load())
	}
}

// ResetStartTime resets the start time to now, so the reported transfer rate
// excludes preparation the user did not ask about (encrypting before an upload,
// staging before a download).
func (b *barBase) ResetStartTime() {
	b.startTime = time.Now()
}

// finish closes out one transfer: settles the bar and prints the summary line.
func (b *barBase) finish(arrow, localPath, peer, fileID string, err error) {
	elapsed := time.Since(b.startTime)
	speed := float64(b.size) / elapsed.Seconds() / (1024 * 1024) // MB/s

	if err == nil {
		if b.bar != nil {
			// ENSURE exact 100% completion (no rounding errors)
			b.bar.SetCurrent(b.size)
			b.bar.SetTotal(b.size, true) // Mark done, trigger BarRemoveOnComplete
		}

		// Success message
		b.write(fmt.Sprintf("✓ %s %s %s (FileID: %s, %.1f MiB, %s, %.1f MiB/s)\n",
			truncatePath(localPath, 2),
			arrow,
			peer,
			fileID,
			float64(b.size)/(1024*1024),
			elapsed.Round(time.Second),
			speed))
		return
	}

	// Error: keep bar visible if terminal, print error
	if b.bar != nil {
		b.bar.Abort(false) // false = don't remove (show failure)
	}

	retries := atomic.LoadInt32(&b.retries)
	b.write(fmt.Sprintf("✗ %s %s %s: %v (after %d retries)\n",
		truncatePath(localPath, 2),
		arrow,
		peer,
		err,
		retries))
}

// write sends a summary line out.
// CRITICAL: Write through mpb's writer (not stdout) to avoid triggering redraws.
func (b *barBase) write(msg string) {
	if b.group.isTerminal && b.group.progress != nil {
		b.group.progress.Write([]byte(msg))
	} else {
		// Non-TTY: print directly to stdout
		fmt.Print(msg)
	}
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
