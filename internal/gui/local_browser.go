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
	// PathValidationTimeout is max time for navigateTo stat check
	PathValidationTimeout = 10 * time.Second
)

// LocalBrowser is a widget for browsing the local filesystem
type LocalBrowser struct {
	widget.BaseWidget

	mu          sync.RWMutex
	currentPath string
	history     []string // For back navigation
	showHidden  bool     // Whether to show hidden files (starting with .)

	// Cancellation for in-flight operations
	cancelMu   sync.Mutex
	cancelLoad context.CancelFunc
	loadingMu  sync.Mutex // Serializes load operations

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

	// Load initial directory
	go b.loadDirectory(b.currentPath)

	return widget.NewSimpleRenderer(content)
}

// loadDirectory loads the contents of a directory with timeout protection
// This method properly handles cancellation, serialization, and network filesystem issues
func (b *LocalBrowser) loadDirectory(path string) {
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
	if !b.loadingMu.TryLock() {
		b.logger.Debug().Str("path", path).Msg("Skipping load - another operation in progress")
		return
	}
	defer b.loadingMu.Unlock()

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

	// Update UI to show we're loading
	fyne.Do(func() {
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

	// Start slow path warning timer
	slowTimer := time.AfterFunc(SlowPathWarningThreshold, func() {
		fyne.Do(func() {
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
		fyne.Do(func() {
			if b.fileList != nil {
				b.fileList.SetStatus("Timeout: Path may be unavailable")
				b.fileList.SetItems(nil)
			}
		})
		return
	case result := <-resultCh:
		entries = result.entries
		readErr = result.err
	}
	slowTimer.Stop()

	// Check cancellation after I/O
	select {
	case <-ctx.Done():
		return
	default:
	}

	if readErr != nil {
		b.logger.Error().Err(readErr).Str("path", path).Msg("Failed to read directory")
		fyne.Do(func() {
			if b.fileList != nil {
				b.fileList.SetStatus("Error: " + readErr.Error())
				b.fileList.SetItems(nil)
			}
		})
		return
	}

	// Get hidden file preference
	b.mu.RLock()
	showHidden := b.showHidden
	b.mu.RUnlock()

	// Process entries - show files even if stat fails (key fix for network filesystems)
	var items []FileItem
	var statErrorCount int

	for _, entry := range entries {
		// Check cancellation periodically
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Skip hidden files (starting with .) unless showHidden is enabled
		if !showHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		fullPath := filepath.Join(path, entry.Name())

		// Use os.Stat to follow symlinks - critical for network mount symlinks
		// entry.Info() uses Lstat which does NOT follow symlinks
		info, err := os.Stat(fullPath)

		var item FileItem
		if err != nil {
			// KEY FIX: Don't skip - show the file anyway!
			// This fixes files "disappearing" when security software blocks stat
			b.logger.Debug().Err(err).Str("file", entry.Name()).Msg("Cannot access file metadata")
			statErrorCount++

			// Determine if this might be a directory we should allow navigation to
			// CRITICAL: For symlinks, entry.Type().IsDir() returns FALSE (symlink itself isn't a dir)
			// But the TARGET might be a directory! We need to handle this case.
			entryType := entry.Type()
			isSymlink := entryType&os.ModeSymlink != 0

			// If it's a symlink and stat failed, assume it MIGHT be a directory
			// This allows users to attempt navigation into network mount symlinks
			isFolder := entryType.IsDir()
			if isSymlink {
				isFolder = true // Optimistic: treat symlinks as folders so user can try to navigate
				b.logger.Debug().Str("file", entry.Name()).Msg("Symlink with stat failure - treating as potential folder")
			}

			item = FileItem{
				ID:       fullPath,
				Name:     entry.Name(),
				IsFolder: isFolder,
				Size:     -1,           // -1 = unknown (stat failed)
				ModTime:  time.Time{},  // Zero = unknown
			}
		} else {
			item = FileItem{
				ID:       fullPath,
				Name:     entry.Name(),
				IsFolder: info.IsDir(), // True type (follows symlinks)
				Size:     info.Size(),
				ModTime:  info.ModTime(),
			}

			// Folders don't show size
			if info.IsDir() {
				item.Size = 0
			}
		}

		items = append(items, item)
	}

	// Log summary if there were errors
	if statErrorCount > 0 {
		b.logger.Warn().
			Int("count", statErrorCount).
			Str("path", path).
			Msg("Some files could not be fully accessed")
	}

	// Final cancellation check before UI update
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Update UI with results
	fyne.Do(func() {
		if b.fileList != nil {
			b.fileList.SetHasDateInfo(true)
			b.fileList.SetItemsAndScrollToTop(items)

			// Show warning if some files couldn't be accessed
			if statErrorCount > 0 {
				b.fileList.SetStatus(fmt.Sprintf("%d items (%d inaccessible)", len(items), statErrorCount))
			}
		}
	})

	b.logger.Debug().
		Str("path", path).
		Int("items", len(items)).
		Int("inaccessible", statErrorCount).
		Msg("Loaded directory")
}

// navigateTo navigates to a specific path with timeout protection
// This method is non-blocking and tries to load even if stat fails (for network paths)
func (b *LocalBrowser) navigateTo(path string) {
	// Show loading state immediately
	fyne.Do(func() {
		if b.pathEntry != nil {
			b.pathEntry.SetText(path)
		}
		if b.fileList != nil {
			b.fileList.SetStatus("Validating path...")
		}
	})

	// Run validation in background to avoid UI freeze
	go func() {
		// Try to stat with timeout
		type statResult struct {
			info os.FileInfo
			err  error
		}
		resultCh := make(chan statResult, 1)
		go func() {
			info, err := os.Stat(path)
			resultCh <- statResult{info: info, err: err}
		}()

		// Wait with timeout
		select {
		case <-time.After(PathValidationTimeout):
			// Stat timed out - try to load anyway (might work)
			b.logger.Warn().Str("path", path).Msg("Path validation timed out - attempting to load anyway")
			b.saveHistory(path)
			b.loadDirectory(path)
			return
		case result := <-resultCh:
			if result.err != nil {
				// Stat failed - log but ALSO try to load (directory might still be listable)
				b.logger.Warn().Err(result.err).Str("path", path).Msg("Path validation failed - attempting to load anyway")
				b.saveHistory(path)
				b.loadDirectory(path)
				return
			}
			if !result.info.IsDir() {
				fyne.Do(func() {
					dialog.ShowInformation("Not a Directory", "Please select a directory, not a file.", b.window)
				})
				return
			}
			// Success - navigate
			b.saveHistory(path)
			b.loadDirectory(path)
		}
	}()
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

	go b.loadDirectory(prevPath)
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
func (b *LocalBrowser) refresh() {
	b.mu.RLock()
	path := b.currentPath
	b.mu.RUnlock()
	go b.loadDirectory(path)
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
