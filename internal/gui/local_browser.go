// Package gui provides the graphical user interface for rescale-int.
// Local filesystem browser component.
// v2.6.1 (November 26, 2025)
// - Added proper spacing/padding around navigation buttons
// v2.5.2 (November 24, 2025)
// - Removed hardcoded sorting (now handled by FileListWidget)
// - Added ModTime population for date sorting
package gui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/logging"
)

// LocalBrowser is a widget for browsing the local filesystem
type LocalBrowser struct {
	widget.BaseWidget

	mu          sync.RWMutex
	currentPath string
	history     []string // For back navigation

	// Cancellation for in-flight operations
	cancelMu    sync.Mutex
	cancelLoad  context.CancelFunc
	loadingMu   sync.Mutex // Serializes load operations

	// UI components
	fileList    *FileListWidget
	pathEntry   *widget.Entry
	backBtn     *widget.Button
	homeBtn     *widget.Button
	refreshBtn  *widget.Button
	browseBtn   *widget.Button

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

	// Navigation bar with proper spacing around buttons
	// Left side: back + home buttons with spacing from edge and path entry
	leftButtons := container.NewHBox(
		HorizontalSpacer(4), // Buffer from left edge
		b.backBtn,
		b.homeBtn,
		HorizontalSpacer(8), // Buffer between buttons and path entry
	)
	// Right side: browse + refresh buttons with spacing from path entry and edge
	rightButtons := container.NewHBox(
		HorizontalSpacer(8), // Buffer between path entry and buttons
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

// loadDirectory loads the contents of a directory
// This method properly handles cancellation and serialization to prevent lockups
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

	// Update UI to show we're loading - all UI updates in one fyne.Do()
	fyne.Do(func() {
		if b.pathEntry != nil {
			b.pathEntry.SetText(path)
		}
		if b.fileList != nil {
			b.fileList.SetPath(path)
			b.fileList.SetStatus("Loading...")
		}
	})

	// Read directory contents (blocking I/O)
	entries, err := os.ReadDir(path)

	// Check cancellation after I/O
	select {
	case <-ctx.Done():
		return
	default:
	}

	if err != nil {
		b.logger.Error().Err(err).Str("path", path).Msg("Failed to read directory")
		fyne.Do(func() {
			if b.fileList != nil {
				b.fileList.SetStatus("Error: " + err.Error())
				b.fileList.SetItems(nil)
			}
		})
		return
	}

	// Process entries (no locks held)
	var items []FileItem

	for _, entry := range entries {
		// Check cancellation periodically
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Skip hidden files (starting with .)
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		fullPath := filepath.Join(path, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		item := FileItem{
			ID:       fullPath,
			Name:     entry.Name(),
			IsFolder: entry.IsDir(),
			ModTime:  info.ModTime(), // Populate ModTime for local files
		}

		if !entry.IsDir() {
			item.Size = info.Size()
		}

		items = append(items, item)
	}

	// No sorting here - FileListWidget handles sorting internally

	// Final cancellation check before UI update
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Update UI with results - single fyne.Do() call
	// Set hasDateInfo since local files have ModTime
	// Use SetItemsAndScrollToTop when navigating to ensure we start at top of new directory
	fyne.Do(func() {
		if b.fileList != nil {
			b.fileList.SetHasDateInfo(true) // Local files have date info
			b.fileList.SetItemsAndScrollToTop(items)
		}
	})

	b.logger.Debug().
		Str("path", path).
		Int("items", len(items)).
		Msg("Loaded directory")
}

// navigateTo navigates to a specific path
func (b *LocalBrowser) navigateTo(path string) {
	// Validate path
	info, err := os.Stat(path)
	if err != nil {
		dialog.ShowError(err, b.window)
		return
	}

	if !info.IsDir() {
		dialog.ShowInformation("Not a Directory", "Please select a directory, not a file.", b.window)
		return
	}

	// Save current path to history before navigating
	b.mu.Lock()
	if b.currentPath != "" && b.currentPath != path {
		b.history = append(b.history, b.currentPath)
		// Limit history size
		if len(b.history) > 50 {
			b.history = b.history[1:]
		}
	}
	b.mu.Unlock()

	go b.loadDirectory(path)
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
