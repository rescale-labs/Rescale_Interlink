// Package gui provides the graphical user interface for rescale-int.
// Local filesystem browser component.
package gui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/logging"
)

const (
	// DirectoryReadTimeout is max time to wait for directory listing
	DirectoryReadTimeout = 30 * time.Second
	// SlowPathWarningThreshold is when to show "slow path" warning
	SlowPathWarningThreshold = 5 * time.Second
)

// LocalBrowser is a widget for browsing the local filesystem
type LocalBrowser struct {
	widget.BaseWidget

	mu          sync.RWMutex
	currentPath string
	history     []string // For back navigation
	showHidden  bool     // Whether to show hidden files (starting with .)

	// Navigation tracking - prevents dropped navigation and stale results
	navGeneration uint64     // Incremented on each navigation, checked before UI update
	pendingPath   string     // Latest requested path (for when TryLock fails)
	pendingMu     sync.Mutex // Protects pendingPath

	// Cancellation for in-flight operations
	cancelMu   sync.Mutex
	cancelLoad context.CancelFunc
	loadingMu  sync.Mutex // Serializes load operations

	// Loading state - prevents actions during directory load
	isLoading atomic.Bool

	// UI components
	fileList        *FileListWidget
	pathEntry       *widget.Entry
	backBtn         *widget.Button
	homeBtn         *widget.Button
	refreshBtn      *widget.Button
	browseBtn       *widget.Button
	showHiddenCheck *widget.Check // UI toggle for hidden files

	// Callbacks
	OnSelectionChanged func(selected []FileItem)

	// Window reference for dialogs
	window fyne.Window

	// Logger
	logger *logging.Logger
}

// NewLocalBrowser creates a new local filesystem browser
func NewLocalBrowser(window fyne.Window) *LocalBrowser {
	b := &LocalBrowser{
		window: window,
		logger: logging.NewLogger("local-browser", nil),
	}
	b.ExtendBaseWidget(b)

	// Start in home directory
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/"
	}
	b.currentPath = home

	return b
}

// CreateRenderer implements fyne.Widget
func (b *LocalBrowser) CreateRenderer() fyne.WidgetRenderer {
	// Title
	title := widget.NewLabel("Local Files")
	title.TextStyle = fyne.TextStyle{Bold: true}

	// Path entry with browse button
	b.pathEntry = widget.NewEntry()
	b.pathEntry.SetPlaceHolder("Enter path...")
	b.pathEntry.OnSubmitted = func(path string) {
		b.navigateTo(path)
	}

	// Navigation buttons
	b.backBtn = widget.NewButtonWithIcon("", theme.NavigateBackIcon(), b.goBack)
	b.homeBtn = widget.NewButtonWithIcon("", theme.HomeIcon(), b.goHome)
	b.refreshBtn = widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), b.refresh)
	b.browseBtn = widget.NewButtonWithIcon("", theme.FolderOpenIcon(), b.showFolderDialog)
	b.showHiddenCheck = widget.NewCheck("Hidden", func(checked bool) {
		b.mu.Lock()
		b.showHidden = checked
		b.mu.Unlock()
		b.refresh() // Reload directory with new setting
	})

	// Navigation bar with proper spacing around buttons
	// Left side: back + home buttons with spacing from edge and path entry
	leftButtons := container.NewHBox(
		HorizontalSpacer(4), // Buffer from left edge
		b.backBtn,
		b.homeBtn,
		HorizontalSpacer(8), // Buffer between buttons and path entry
	)
	// Right side: hidden toggle + browse + refresh buttons with spacing
	rightButtons := container.NewHBox(
		HorizontalSpacer(8), // Buffer between path entry and buttons
		b.showHiddenCheck,
		b.browseBtn,
		b.refreshBtn,
		HorizontalSpacer(4), // Buffer from right edge
	)
	navBar := container.NewBorder(
		nil, nil,
		leftButtons,
		rightButtons,
		b.pathEntry,
	)

	// File list widget
	b.fileList = NewFileListWidget()
	b.fileList.OnFolderOpen = func(item FileItem) {
		b.navigateTo(item.ID) // ID is the full path for local files
	}
	b.fileList.OnSelectionChanged = func(selected []FileItem) {
		if b.OnSelectionChanged != nil {
			b.OnSelectionChanged(selected)
		}
	}

	// Layout - title is provided by parent container (FileBrowserTab)
	// We only include the navigation bar here
	_ = title // Title managed by FileBrowserTab
	content := container.NewBorder(
		navBar,
		nil, nil, nil,
		b.fileList,
	)

	// Load initial directory - use generation 0 for initial load
	gen := atomic.AddUint64(&b.navGeneration, 1)
	go b.loadDirectory(b.currentPath, gen)

	return widget.NewSimpleRenderer(content)
}

// loadDirectory loads the contents of a directory with timeout protection
// This method properly handles cancellation, serialization, and network filesystem issues.
// loadGen is the navigation generation token bound at navigation start - prevents generation drift.
func (b *LocalBrowser) loadDirectory(path string, loadGen uint64) {
	// TIMING: Track performance metrics
	loadStart := time.Now()
	var readDirDuration, symlinkResolveDuration time.Duration

	// Set loading state
	b.isLoading.Store(true)

	// Cancel any previous load operation
	b.cancelMu.Lock()
	if b.cancelLoad != nil {
		b.cancelLoad()
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancelLoad = cancel
	b.cancelMu.Unlock()

	// Serialize load operations - only one at a time
	// Use TryLock pattern to avoid blocking if another load is in progress
	// NOTE: If we can't get the lock, the pendingPath mechanism ensures
	// the current operation will load our path when it finishes
	if !b.loadingMu.TryLock() {
		b.logger.Debug().Str("path", path).Msg("Skipping load - another operation in progress (pending will be loaded after)")
		return
	}
	// Deferred cleanup: unlock and check if there's a pending path to load
	defer func() {
		b.loadingMu.Unlock()
		b.checkAndLoadPending(path)
	}()

	// Clear pending path if it matches what we're about to load
	// (we're handling it now, so it's no longer pending)
	b.pendingMu.Lock()
	if b.pendingPath == path {
		b.pendingPath = ""
	}
	b.pendingMu.Unlock()

	// Check if we were cancelled before we even started
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Update current path
	b.mu.Lock()
	b.currentPath = path
	b.mu.Unlock()

	// Update UI to show we're loading (check gen inside callback)
	fyne.Do(func() {
		if atomic.LoadUint64(&b.navGeneration) != loadGen {
			return // Stale - another navigation happened
		}
		if b.pathEntry != nil {
			b.pathEntry.SetText(path)
		}
		if b.fileList != nil {
			b.fileList.SetPath(path)
			b.fileList.SetStatus("Loading...")
		}
	})

	// Read directory contents with timeout protection
	type readResult struct {
		entries []os.DirEntry
		err     error
	}
	resultCh := make(chan readResult, 1)

	// Start directory read in background
	go func() {
		entries, err := os.ReadDir(path)
		resultCh <- readResult{entries: entries, err: err}
	}()

	// Start slow path warning timer (capture loadGen for closure)
	slowTimer := time.AfterFunc(SlowPathWarningThreshold, func() {
		fyne.Do(func() {
			if atomic.LoadUint64(&b.navGeneration) != loadGen {
				return // Stale
			}
			if b.fileList != nil {
				b.fileList.SetStatus("Loading... (network path may be slow)")
			}
		})
	})
	defer slowTimer.Stop()

	// Wait for result with timeout
	var entries []os.DirEntry
	var readErr error
	select {
	case <-ctx.Done():
		return
	case <-time.After(DirectoryReadTimeout):
		b.logger.Warn().Str("path", path).Msg("Directory read timed out")
		b.applyDirectoryLoadResult(loadGen, path, nil, 0, true, "Timeout: Path may be unavailable")
		return
	case result := <-resultCh:
		entries = result.entries
		readErr = result.err
	}
	readDirDuration = time.Since(loadStart)
	slowTimer.Stop()

	// Check cancellation after I/O
	select {
	case <-ctx.Done():
		return
	default:
	}

	if readErr != nil {
		b.logger.Error().Err(readErr).Str("path", path).Msg("Failed to read directory")
		b.applyDirectoryLoadResult(loadGen, path, nil, 0, true, "Error: "+readErr.Error())
		return
	}

	// Get hidden file preference
	b.mu.RLock()
	showHidden := b.showHidden
	b.mu.RUnlock()

	// Process entries - PERFORMANCE CRITICAL for network filesystems
	//
	// Key insight: os.ReadDir() returns DirEntry with cached metadata.
	// entry.Info() uses this cache - NO network call needed!
	// os.Stat() makes a SEPARATE network call per file - catastrophically slow.
	//
	// Strategy:
	// 1. Use entry.Info() for all non-symlinks (uses cached data - fast)
	// 2. For symlinks: resolve in PARALLEL with worker pool (8 workers)
	// 3. Show file with "?" size if stat fails
	//
	// PERFORMANCE: Parallel symlink resolution dramatically improves network FS performance

	// First pass: filter entries and identify symlinks
	// This is fast because it only uses cached DirEntry data
	type entryInfo struct {
		entry     os.DirEntry
		fullPath  string
		isSymlink bool
	}
	filteredEntries := make([]entryInfo, 0, len(entries))

	for _, entry := range entries {
		// Skip hidden files unless showHidden is enabled
		if !showHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		fullPath := filepath.Join(path, entry.Name())
		entryType := entry.Type()
		isSymlink := entryType&os.ModeSymlink != 0

		filteredEntries = append(filteredEntries, entryInfo{
			entry:     entry,
			fullPath:  fullPath,
			isSymlink: isSymlink,
		})
	}

	// Check cancellation after first pass
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Preallocate items slice (one slot per filtered entry)
	items := make([]FileItem, len(filteredEntries))
	var statErrorCount int32 // Atomic for thread-safety

	// Count symlinks to size the job channel
	symlinkCount := 0
	for _, e := range filteredEntries {
		if e.isSymlink {
			symlinkCount++
		}
	}

	if symlinkCount > 0 {
		// TIMING: Track symlink resolution
		symlinkStart := time.Now()

		// Parallel symlink resolution with worker pool
		const numStatWorkers = 8

		type symlinkJob struct {
			index    int
			fullPath string
			entry    os.DirEntry
		}

		jobs := make(chan symlinkJob, symlinkCount)
		results := make(chan struct {
			index int
			item  FileItem
			err   bool
		}, symlinkCount)

		// Start workers
		var wg sync.WaitGroup
		for w := 0; w < numStatWorkers && w < symlinkCount; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobs {
					// Check cancellation
					select {
					case <-ctx.Done():
						return
					default:
					}

					// Resolve symlink with os.Stat
					var item FileItem
					var hasError bool

					info, err := os.Stat(job.fullPath)
					if err != nil {
						// Stat failed - use cached DirEntry info as fallback
						cachedInfo, infoErr := job.entry.Info()
						if infoErr != nil {
							// Both failed - show with unknown metadata
							atomic.AddInt32(&statErrorCount, 1)
							hasError = true
							item = FileItem{
								ID:       job.fullPath,
								Name:     job.entry.Name(),
								IsFolder: true, // Assume folder for symlinks so user can try navigating
								Size:     -1,
								ModTime:  time.Time{},
							}
						} else {
							// Use cached info
							item = FileItem{
								ID:       job.fullPath,
								Name:     job.entry.Name(),
								IsFolder: cachedInfo.IsDir(),
								Size:     cachedInfo.Size(),
								ModTime:  cachedInfo.ModTime(),
							}
							if cachedInfo.IsDir() {
								item.Size = 0
							}
						}
					} else {
						// Stat succeeded - use resolved info
						item = FileItem{
							ID:       job.fullPath,
							Name:     job.entry.Name(),
							IsFolder: info.IsDir(),
							Size:     info.Size(),
							ModTime:  info.ModTime(),
						}
						if info.IsDir() {
							item.Size = 0
						}
					}

					results <- struct {
						index int
						item  FileItem
						err   bool
					}{job.index, item, hasError}
				}
			}()
		}

		// Queue symlink jobs and process non-symlinks immediately
		symlinkIndices := make([]int, 0, symlinkCount)
		for i, e := range filteredEntries {
			if e.isSymlink {
				jobs <- symlinkJob{index: i, fullPath: e.fullPath, entry: e.entry}
				symlinkIndices = append(symlinkIndices, i)
			} else {
				// Non-symlink: use cached DirEntry info (fast!)
				info, err := e.entry.Info()
				if err != nil {
					atomic.AddInt32(&statErrorCount, 1)
					b.logger.Debug().Err(err).Str("file", e.entry.Name()).Msg("Cannot access file metadata")
					items[i] = FileItem{
						ID:       e.fullPath,
						Name:     e.entry.Name(),
						IsFolder: e.entry.Type().IsDir(),
						Size:     -1,
						ModTime:  time.Time{},
					}
				} else {
					items[i] = FileItem{
						ID:       e.fullPath,
						Name:     e.entry.Name(),
						IsFolder: info.IsDir(),
						Size:     info.Size(),
						ModTime:  info.ModTime(),
					}
					if info.IsDir() {
						items[i].Size = 0
					}
				}
			}
		}
		close(jobs)

		// Collect symlink results
		for range symlinkIndices {
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case result := <-results:
				items[result.index] = result.item
			}
		}
		wg.Wait()
		symlinkResolveDuration = time.Since(symlinkStart)
	} else {
		// No symlinks - process all entries sequentially (fast path)
		for i, e := range filteredEntries {
			// Check cancellation periodically
			select {
			case <-ctx.Done():
				return
			default:
			}

			info, err := e.entry.Info()
			if err != nil {
				atomic.AddInt32(&statErrorCount, 1)
				b.logger.Debug().Err(err).Str("file", e.entry.Name()).Msg("Cannot access file metadata")
				items[i] = FileItem{
					ID:       e.fullPath,
					Name:     e.entry.Name(),
					IsFolder: e.entry.Type().IsDir(),
					Size:     -1,
					ModTime:  time.Time{},
				}
			} else {
				items[i] = FileItem{
					ID:       e.fullPath,
					Name:     e.entry.Name(),
					IsFolder: info.IsDir(),
					Size:     info.Size(),
					ModTime:  info.ModTime(),
				}
				if info.IsDir() {
					items[i].Size = 0
				}
			}
		}
	}

	// Log summary if there were errors
	errCount := int(atomic.LoadInt32(&statErrorCount))
	if errCount > 0 {
		b.logger.Warn().
			Int("count", errCount).
			Str("path", path).
			Msg("Some files could not be fully accessed")
	}

	// Final cancellation check before UI update
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Apply results to UI - generation check happens INSIDE fyne.Do() to prevent race
	b.applyDirectoryLoadResult(loadGen, path, items, errCount, false, "")

	// TIMING: Log performance metrics
	totalDuration := time.Since(loadStart)
	b.logger.Info().
		Str("path", path).
		Int("items", len(items)).
		Int("symlinks", symlinkCount).
		Int("inaccessible", errCount).
		Dur("total_ms", totalDuration).
		Dur("readdir_ms", readDirDuration).
		Dur("symlink_ms", symlinkResolveDuration).
		Msg("Directory load complete")

	// TIMING: Direct stderr output (separate from logger, not filtered by log level)
	// Enable with RESCALE_TIMING=1 or --timing flag
	if os.Getenv("RESCALE_TIMING") == "1" {
		fmt.Fprintf(os.Stderr, "[TIMING] %s: total=%v readdir=%v symlink=%v items=%d symlinks=%d\n",
			path, totalDuration.Round(time.Millisecond),
			readDirDuration.Round(time.Millisecond),
			symlinkResolveDuration.Round(time.Millisecond),
			len(items), symlinkCount)
	}
}

// applyDirectoryLoadResult commits directory load results to the UI.
// This is the ONLY place that updates path, items, and status after a load.
// Generation is checked INSIDE fyne.Do() - this prevents the race condition where
// the check passes but fyne.Do() executes after another navigation started.
func (b *LocalBrowser) applyDirectoryLoadResult(
	loadGen uint64,
	path string,
	items []FileItem,
	errCount int,
	isError bool,
	errorMsg string,
) {
	fyne.Do(func() {
		// CHECK INSIDE THE CALLBACK - atomic with UI update
		currentGen := atomic.LoadUint64(&b.navGeneration)
		if loadGen != currentGen {
			b.logger.Debug().
				Uint64("load_gen", loadGen).
				Uint64("current_gen", currentGen).
				Str("path", path).
				Msg("Discarding stale load result inside fyne.Do")
			b.isLoading.Store(false)
			return
		}

		// Update path entry
		if b.pathEntry != nil {
			b.pathEntry.SetText(path)
		}

		// Update file list
		if b.fileList != nil {
			b.fileList.SetPath(path)
			if isError {
				b.fileList.SetItems(nil)
				b.fileList.SetStatus(errorMsg)
			} else {
				b.fileList.SetHasDateInfo(true)
				b.fileList.SetItemsAndScrollToTop(items)
				if errCount > 0 {
					b.fileList.SetStatus(fmt.Sprintf("%d items (%d inaccessible)", len(items), errCount))
				}
			}
		}

		// Clear loading state
		b.isLoading.Store(false)
	})
}

// checkAndLoadPending checks if there's a pending path different from what was just loaded,
// and starts loading it. This ensures navigation requests are never dropped.
func (b *LocalBrowser) checkAndLoadPending(justLoadedPath string) {
	b.pendingMu.Lock()
	pending := b.pendingPath
	// Clear it so we don't load it multiple times
	if pending != "" && pending != justLoadedPath {
		b.pendingPath = ""
	} else {
		pending = ""
	}
	b.pendingMu.Unlock()

	if pending != "" {
		b.logger.Debug().
			Str("pending", pending).
			Str("just_loaded", justLoadedPath).
			Msg("Loading pending path after previous load completed")
		// Create new generation for pending load
		gen := atomic.AddUint64(&b.navGeneration, 1)
		go b.loadDirectory(pending, gen)
	}
}

// navigateTo navigates to a specific path
// PERFORMANCE: Skip os.Stat validation - just try to load directly.
// os.ReadDir will fail with a clear error if path is not a directory.
// This eliminates one network round trip per navigation.
func (b *LocalBrowser) navigateTo(path string) {
	// Clear selection first - prevents stale selection from previous directory
	if b.fileList != nil {
		b.fileList.ClearSelection()
	}

	// Increment generation and capture it - prevents generation drift
	gen := atomic.AddUint64(&b.navGeneration, 1)

	// Always record the pending path - this ensures the latest navigation
	// is never dropped even if TryLock fails in loadDirectory
	b.pendingMu.Lock()
	b.pendingPath = path
	b.pendingMu.Unlock()

	b.saveHistory(path)
	go b.loadDirectory(path, gen)
}

// saveHistory saves current path to history before navigation
func (b *LocalBrowser) saveHistory(newPath string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.currentPath != "" && b.currentPath != newPath {
		b.history = append(b.history, b.currentPath)
		if len(b.history) > 50 {
			b.history = b.history[1:]
		}
	}
}

// goBack navigates to the previous directory
func (b *LocalBrowser) goBack() {
	b.mu.Lock()
	if len(b.history) == 0 {
		b.mu.Unlock()
		return
	}
	prevPath := b.history[len(b.history)-1]
	b.history = b.history[:len(b.history)-1]
	b.mu.Unlock()

	// Clear selection first - prevents stale selection
	if b.fileList != nil {
		b.fileList.ClearSelection()
	}

	// Increment generation and capture it - prevents generation drift
	gen := atomic.AddUint64(&b.navGeneration, 1)
	b.pendingMu.Lock()
	b.pendingPath = prevPath
	b.pendingMu.Unlock()

	go b.loadDirectory(prevPath, gen)
}

// goHome navigates to the home directory
func (b *LocalBrowser) goHome() {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/"
	}
	b.navigateTo(home)
}

// refresh reloads the current directory
// Note: Does not clear selection - user may want to keep files selected during refresh
func (b *LocalBrowser) refresh() {
	b.mu.RLock()
	path := b.currentPath
	b.mu.RUnlock()

	// Increment generation and capture it - prevents generation drift
	gen := atomic.AddUint64(&b.navGeneration, 1)
	b.pendingMu.Lock()
	b.pendingPath = path
	b.pendingMu.Unlock()

	go b.loadDirectory(path, gen)
}

// showFolderDialog shows a folder selection dialog
func (b *LocalBrowser) showFolderDialog() {
	dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			dialog.ShowError(err, b.window)
			return
		}
		if uri == nil {
			return // User cancelled
		}
		b.navigateTo(uri.Path())
	}, b.window)
}

// GetCurrentPath returns the current directory path
func (b *LocalBrowser) GetCurrentPath() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.currentPath
}

// GetSelectedItems returns the currently selected items
func (b *LocalBrowser) GetSelectedItems() []FileItem {
	if b.fileList == nil {
		return nil
	}
	return b.fileList.GetSelectedItems()
}

// GetSelectedCount returns the number of selected items
func (b *LocalBrowser) GetSelectedCount() int {
	if b.fileList == nil {
		return 0
	}
	return b.fileList.GetSelectedCount()
}

// ClearSelection clears all selections
func (b *LocalBrowser) ClearSelection() {
	if b.fileList != nil {
		b.fileList.ClearSelection()
	}
}

// Refresh reloads the current directory
// Must use fyne.Do() for widget refresh since this may be called from goroutines
func (b *LocalBrowser) Refresh() {
	b.refresh()
	fyne.Do(func() {
		b.BaseWidget.Refresh()
	})
}
