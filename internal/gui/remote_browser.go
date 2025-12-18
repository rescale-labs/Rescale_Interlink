// Package gui provides the graphical user interface for rescale-int.
// Remote (Rescale) filesystem browser component.
package gui

import (
	"context"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
)

// BreadcrumbEntry represents a folder in the navigation path
type BreadcrumbEntry struct {
	ID   string
	Name string
}

// RemoteBrowser is a widget for browsing Rescale files
// Sprint F.4: Uses FileListWidget pagination with multi-page API fetching
type RemoteBrowser struct {
	widget.BaseWidget

	mu              sync.RWMutex
	engine          *core.Engine
	currentFolderID string
	currentRoot     string // "library" or "jobs"
	rootFolders     *models.RootFolders
	breadcrumb      []BreadcrumbEntry

	// Loaded items and API pagination cursor
	loadedItems []FileItem // All items loaded so far for current folder
	apiNextURL  string     // Next API page URL (empty if all loaded)
	hasMoreData bool       // True if more API pages available

	// Cancellation for in-flight operations
	cancelMu   sync.Mutex
	cancelLoad context.CancelFunc
	loadingMu  sync.Mutex // Serializes load operations

	// UI components
	fileList      *FileListWidget
	rootToggle    *widget.RadioGroup
	breadcrumbBar *fyne.Container
	refreshBtn    *widget.Button
	backBtn       *widget.Button // Back/up navigation button

	// Callbacks
	OnSelectionChanged func(selected []FileItem)

	// State
	isLoading bool

	// Window reference for dialogs
	window fyne.Window

	// Logger
	logger *logging.Logger
}

// NewRemoteBrowser creates a new Rescale file browser
func NewRemoteBrowser(engine *core.Engine, window fyne.Window) *RemoteBrowser {
	b := &RemoteBrowser{
		engine:      engine,
		window:      window,
		currentRoot: "library",
		loadedItems: make([]FileItem, 0),
		logger:      logging.NewLogger("remote-browser", nil),
	}
	b.ExtendBaseWidget(b)
	return b
}

// CreateRenderer implements fyne.Widget
func (b *RemoteBrowser) CreateRenderer() fyne.WidgetRenderer {
	// Title
	title := widget.NewLabel("Rescale Files")
	title.TextStyle = fyne.TextStyle{Bold: true}

	// Back button for navigation (disabled at root)
	b.backBtn = widget.NewButtonWithIcon("", theme.NavigateBackIcon(), b.goUp)
	b.backBtn.Disable() // Disabled at root

	// Root toggle (My Library / My Jobs)
	b.rootToggle = widget.NewRadioGroup([]string{"My Library", "My Jobs"}, b.onRootChanged)
	b.rootToggle.Horizontal = true
	b.rootToggle.SetSelected("My Library")

	// Breadcrumb navigation
	b.breadcrumbBar = container.NewHBox()

	// Refresh button
	b.refreshBtn = widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), b.refresh)

	// Navigation bar with back button and proper spacing
	// Left side: back button + root toggle with spacing from edge and breadcrumb
	leftControls := container.NewHBox(
		HorizontalSpacer(4), // Buffer from left edge
		b.backBtn,
		HorizontalSpacer(4), // Buffer between back and toggle
		b.rootToggle,
		HorizontalSpacer(8), // Buffer between toggle and breadcrumb
	)
	// Right side: refresh button with spacing from breadcrumb and edge
	rightControls := container.NewHBox(
		HorizontalSpacer(8), // Buffer between breadcrumb and refresh
		b.refreshBtn,
		HorizontalSpacer(4), // Buffer from right edge
	)
	navBar := container.NewBorder(
		nil, nil,
		leftControls,
		rightControls,
		NewAcceleratedHScroll(b.breadcrumbBar),
	)

	// File list widget - uses its own pagination controls
	// Sprint F.4: We fetch multiple API pages to fill the user's desired page size
	b.fileList = NewFileListWidget()
	b.fileList.OnFolderOpen = func(item FileItem) {
		b.navigateToFolder(item.ID, item.Name)
	}
	b.fileList.OnSelectionChanged = func(selected []FileItem) {
		if b.OnSelectionChanged != nil {
			b.OnSelectionChanged(selected)
		}
	}
	// Callback when user navigates to a page - load more data if needed
	b.fileList.OnPageChange = func(page, pageSize, totalItems int) {
		go b.onPageChange(page, pageSize, totalItems)
	}

	// Layout - title is provided by parent container (FileBrowserTab)
	// Sprint F.4: FileListWidget handles pagination; we fetch API pages as needed
	_ = title // Title managed by FileBrowserTab
	content := container.NewBorder(
		navBar,
		nil, // FileListWidget has its own pagination bar
		nil, nil,
		b.fileList,
	)

	// Start loading data
	go b.initialize()

	return widget.NewSimpleRenderer(content)
}

// initialize loads initial data
func (b *RemoteBrowser) initialize() {
	// Check if API key is configured BEFORE making any API calls
	// This prevents 401 errors on startup when no API key is set
	cfg := b.engine.GetConfig()
	if cfg == nil || cfg.APIKey == "" {
		fyne.Do(func() {
			b.fileList.SetStatus("API key not configured. Set up your API key in the Setup tab.")
			b.fileList.SetPath("Not connected")
		})
		b.logger.Info().Msg("Skipping remote browser init - no API key configured")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b.setLoading(true)
	defer b.setLoading(false)

	// Get API client
	apiClient := b.engine.API()
	if apiClient == nil {
		// v3.4.0: Use fyne.Do() for thread safety
		fyne.Do(func() {
			b.fileList.SetStatus("Not connected. Configure API key in Setup tab.")
			b.fileList.SetPath("Not connected")
		})
		return
	}

	// Get root folders
	roots, err := apiClient.GetRootFolders(ctx)
	if err != nil {
		// v3.4.0: Use fyne.Do() for thread safety
		fyne.Do(func() {
			b.fileList.SetStatus("Error: " + err.Error())
		})
		b.logger.Error().Err(err).Msg("Failed to get root folders")
		return
	}

	b.mu.Lock()
	b.rootFolders = roots
	if b.currentRoot == "library" {
		b.currentFolderID = roots.MyLibrary
		b.breadcrumb = []BreadcrumbEntry{{ID: roots.MyLibrary, Name: "My Library"}}
	} else {
		b.currentFolderID = roots.MyJobs
		b.breadcrumb = []BreadcrumbEntry{{ID: roots.MyJobs, Name: "My Jobs"}}
	}
	b.mu.Unlock()

	b.updateBreadcrumbUI()
	b.loadCurrentFolder()
}

// loadCurrentFolder loads folder contents, fetching enough API pages to fill user's page size
// Sprint F.4: Fetches multiple 25-item API pages to fill user's desired page size (max 200)
func (b *RemoteBrowser) loadCurrentFolder() {
	// Reset state for new folder
	b.mu.Lock()
	b.loadedItems = make([]FileItem, 0)
	b.apiNextURL = ""
	b.hasMoreData = false
	b.mu.Unlock()

	// Clear hasMoreServerData in FileListWidget to prevent stale state from previous folder
	// v3.4.0: Use fyne.Do() for thread safety
	if b.fileList != nil {
		fyne.Do(func() {
			b.fileList.SetHasMoreServerData(false)
		})
	}

	// Load initial data (0 = use FileListWidget's page size)
	b.loadMoreItems(0, true) // 0 = use page size, true = scroll to top
}

// loadMoreItems fetches API pages to fill the current display page
// targetCount: how many items we need total (0 = use FileListWidget's page size)
// scrollToTop: if true, scrolls to top after loading (for new folder navigation)
func (b *RemoteBrowser) loadMoreItems(targetCount int, scrollToTop bool) {
	// IMPORTANT: Try to acquire lock BEFORE canceling any previous operation
	// This prevents a race where a second call cancels the first, then fails TryLock
	// and returns - leaving the first operation canceled but no replacement running
	if !b.loadingMu.TryLock() {
		b.logger.Debug().Msg("Skipping load - another operation in progress")
		return
	}
	// Lock acquired - we're going to proceed, safe to cancel previous operation now
	defer b.loadingMu.Unlock()

	// Cancel any previous load operation's context
	b.cancelMu.Lock()
	if b.cancelLoad != nil {
		b.cancelLoad()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	b.cancelLoad = cancel
	b.cancelMu.Unlock()

	// Get folder ID and current state
	b.mu.RLock()
	folderID := b.currentFolderID
	currentItems := len(b.loadedItems)
	nextURL := b.apiNextURL
	hasMore := b.hasMoreData
	b.mu.RUnlock()

	if folderID == "" {
		fyne.Do(func() {
			b.fileList.SetStatus("No folder selected")
		})
		return
	}

	// If no more data available, just return
	if currentItems > 0 && !hasMore {
		return
	}

	// Determine target: only fetch enough to fill the display page
	// Default to FileListWidget's page size if not specified
	if targetCount <= 0 {
		targetCount = b.fileList.GetPageSize()
	}
	// Clamp to max 200
	if targetCount > 200 {
		targetCount = 200
	}

	// If we already have enough items, no need to fetch
	if currentItems >= targetCount {
		return
	}

	// Set loading state
	b.setLoading(true)
	defer b.setLoading(false)

	// Check API connection
	apiClient := b.engine.API()
	if apiClient == nil {
		fyne.Do(func() {
			b.fileList.SetStatus("Not connected. Configure API key in Setup tab.")
		})
		return
	}

	// Fetch API pages until we have enough items or no more pages
	// API returns 25 items per page
	var newItems []FileItem
	pageURL := nextURL
	if pageURL == "" && currentItems == 0 {
		pageURL = "" // First load - use empty string for first page
	}

	pagesLoaded := 0
	for currentItems+len(newItems) < targetCount {
		// Check cancellation
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Fetch a page
		contents, err := apiClient.ListFolderContentsPage(ctx, folderID, pageURL)
		if err != nil {
			b.logger.Error().Err(err).Str("folder_id", folderID).Msg("Failed to load folder")
			if len(newItems) == 0 && currentItems == 0 {
				fyne.Do(func() {
					b.fileList.SetStatus("Error: " + err.Error())
				})
			}
			break
		}

		pagesLoaded++

		// Process items from this page
		for _, f := range contents.Folders {
			newItems = append(newItems, FileItem{
				ID:       f.ID,
				Name:     f.Name,
				IsFolder: true,
				ModTime:  f.DateUploaded,
			})
		}
		for _, f := range contents.Files {
			newItems = append(newItems, FileItem{
				ID:      f.ID,
				Name:    f.Name,
				Size:    f.DecryptedSize,
				ModTime: f.DateUploaded,
			})
		}

		// Update next URL and check if more pages available
		pageURL = contents.NextURL
		if pageURL == "" {
			// No more pages
			b.mu.Lock()
			b.hasMoreData = false
			b.apiNextURL = ""
			b.mu.Unlock()
			break
		}

		// Store next URL for potential future loading
		b.mu.Lock()
		b.hasMoreData = true
		b.apiNextURL = pageURL
		b.mu.Unlock()
	}

	// Append new items to loaded items
	b.mu.Lock()
	b.loadedItems = append(b.loadedItems, newItems...)
	allItems := make([]FileItem, len(b.loadedItems))
	copy(allItems, b.loadedItems)
	hasMore = b.hasMoreData
	b.mu.Unlock()

	// Get path for display
	b.mu.RLock()
	pathParts := make([]string, len(b.breadcrumb))
	for i, bc := range b.breadcrumb {
		pathParts[i] = bc.Name
	}
	b.mu.RUnlock()
	pathStr := strings.Join(pathParts, " > ")

	// Final cancellation check
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Update UI
	fyne.Do(func() {
		b.fileList.SetHasDateInfo(true) // Remote files have dateUploaded from API
		b.fileList.SetHasMoreServerData(hasMore) // Tell FileListWidget if more data is available
		if scrollToTop {
			b.fileList.SetItemsAndScrollToTop(allItems)
		} else {
			b.fileList.SetItems(allItems)
		}
		b.fileList.SetPath(pathStr)
	})

	b.logger.Debug().
		Str("folder_id", folderID).
		Int("items_loaded", len(allItems)).
		Int("pages_fetched", pagesLoaded).
		Int("target", targetCount).
		Bool("has_more", hasMore).
		Msg("Loaded folder contents")
}

// onPageChange is called when user navigates pages in FileListWidget
// Loads more data if user needs items beyond what's loaded
func (b *RemoteBrowser) onPageChange(page, pageSize, totalItems int) {
	// Calculate how many items we need for the requested page
	// page is 0-indexed, so page 1 needs items 0 to pageSize-1
	// page 2 needs items pageSize to 2*pageSize-1, etc.
	itemsNeeded := (page + 1) * pageSize // Items needed to show this page

	b.mu.RLock()
	loadedCount := len(b.loadedItems)
	hasMore := b.hasMoreData
	b.mu.RUnlock()

	// Load more if we need items beyond what's loaded
	if hasMore && itemsNeeded > loadedCount {
		b.logger.Debug().
			Int("page", page).
			Int("pageSize", pageSize).
			Int("itemsNeeded", itemsNeeded).
			Int("loadedCount", loadedCount).
			Msg("Loading more items for pagination")
		go b.loadMoreItems(itemsNeeded, false)
	} else {
		// Even if we're not loading more, update hasMoreServerData
		// This prevents stale state when navigating within already-loaded data
		// e.g., user is on page 5 but all data is loaded and hasMore is false
		fyne.Do(func() {
			b.fileList.SetHasMoreServerData(hasMore)
		})
	}
}

// navigateToFolder navigates to a subfolder
func (b *RemoteBrowser) navigateToFolder(folderID, folderName string) {
	b.mu.Lock()
	b.currentFolderID = folderID
	b.breadcrumb = append(b.breadcrumb, BreadcrumbEntry{ID: folderID, Name: folderName})
	b.mu.Unlock()

	b.updateBreadcrumbUI()
	go b.loadCurrentFolder()
}

// navigateToBreadcrumb navigates to a breadcrumb entry
func (b *RemoteBrowser) navigateToBreadcrumb(index int) {
	b.mu.Lock()
	if index >= len(b.breadcrumb) {
		b.mu.Unlock()
		return
	}
	entry := b.breadcrumb[index]
	b.currentFolderID = entry.ID
	b.breadcrumb = b.breadcrumb[:index+1]
	b.mu.Unlock()

	b.updateBreadcrumbUI()
	go b.loadCurrentFolder()
}

// goUp navigates up one level (like back button)
func (b *RemoteBrowser) goUp() {
	b.mu.Lock()
	if len(b.breadcrumb) <= 1 {
		b.mu.Unlock()
		return
	}
	b.breadcrumb = b.breadcrumb[:len(b.breadcrumb)-1]
	b.currentFolderID = b.breadcrumb[len(b.breadcrumb)-1].ID
	b.mu.Unlock()

	b.updateBreadcrumbUI()
	go b.loadCurrentFolder()
}

// updateBreadcrumbUI updates the breadcrumb navigation bar with smart truncation
func (b *RemoteBrowser) updateBreadcrumbUI() {
	b.mu.RLock()
	breadcrumb := make([]BreadcrumbEntry, len(b.breadcrumb))
	copy(breadcrumb, b.breadcrumb)
	b.mu.RUnlock()

	if b.breadcrumbBar == nil {
		return
	}

	// UI updates must be on main thread
	fyne.Do(func() {
		b.breadcrumbBar.Objects = nil

		if len(breadcrumb) <= 3 {
			// Show all levels when 3 or fewer
			for i, entry := range breadcrumb {
				idx := i
				btn := widget.NewButton(entry.Name, func() {
					b.navigateToBreadcrumb(idx)
				})
				if i == len(breadcrumb)-1 {
					btn.Importance = widget.HighImportance
				}
				b.breadcrumbBar.Add(btn)
				if i < len(breadcrumb)-1 {
					b.breadcrumbBar.Add(widget.NewLabel(">"))
				}
			}
		} else {
			// Smart truncation: root > ... > parent > current
			// Root button
			btn0 := widget.NewButton(breadcrumb[0].Name, func() {
				b.navigateToBreadcrumb(0)
			})
			b.breadcrumbBar.Add(btn0)
			b.breadcrumbBar.Add(widget.NewLabel(">"))

			// Ellipsis button with menu for skipped levels
			skipped := breadcrumb[1 : len(breadcrumb)-2]
			ellipsisBtn := widget.NewButton("...", func() {
				b.showSkippedLevelsMenu(skipped, 1)
			})
			b.breadcrumbBar.Add(ellipsisBtn)
			b.breadcrumbBar.Add(widget.NewLabel(">"))

			// Parent button
			parentIdx := len(breadcrumb) - 2
			btnParent := widget.NewButton(breadcrumb[parentIdx].Name, func() {
				b.navigateToBreadcrumb(parentIdx)
			})
			b.breadcrumbBar.Add(btnParent)
			b.breadcrumbBar.Add(widget.NewLabel(">"))

			// Current (highlighted)
			btnCurrent := widget.NewButton(breadcrumb[len(breadcrumb)-1].Name, func() {
				b.navigateToBreadcrumb(len(breadcrumb) - 1)
			})
			btnCurrent.Importance = widget.HighImportance
			b.breadcrumbBar.Add(btnCurrent)
		}

		b.breadcrumbBar.Refresh()
	})

	b.updateBackButtonState()
}

// showSkippedLevelsMenu shows a popup menu with the skipped breadcrumb levels
func (b *RemoteBrowser) showSkippedLevelsMenu(skipped []BreadcrumbEntry, startIndex int) {
	var items []*fyne.MenuItem
	for i, entry := range skipped {
		idx := startIndex + i
		name := entry.Name
		items = append(items, fyne.NewMenuItem(name, func() {
			b.navigateToBreadcrumb(idx)
		}))
	}

	menu := fyne.NewMenu("Navigate to", items...)
	canvas := fyne.CurrentApp().Driver().CanvasForObject(b.breadcrumbBar)
	popup := widget.NewPopUpMenu(menu, canvas)

	// Find the "..." button to position the popup below it
	// The ellipsis button is at index 2 in the breadcrumbBar (after root btn and first separator)
	if b.breadcrumbBar != nil && len(b.breadcrumbBar.Objects) > 2 {
		ellipsisBtn := b.breadcrumbBar.Objects[2]
		driver := fyne.CurrentApp().Driver()
		absPos := driver.AbsolutePositionForObject(ellipsisBtn)
		size := ellipsisBtn.Size()
		popup.ShowAtPosition(fyne.NewPos(absPos.X, absPos.Y+size.Height))
	} else {
		popup.Show()
	}
}

// updateBackButtonState enables/disables the back button based on current depth
func (b *RemoteBrowser) updateBackButtonState() {
	b.mu.RLock()
	atRoot := len(b.breadcrumb) <= 1
	b.mu.RUnlock()

	fyne.Do(func() {
		if b.backBtn != nil {
			if atRoot {
				b.backBtn.Disable()
			} else {
				b.backBtn.Enable()
			}
		}
	})
}

// onRootChanged handles root toggle change
func (b *RemoteBrowser) onRootChanged(selected string) {
	b.mu.Lock()
	if b.rootFolders == nil {
		b.mu.Unlock()
		return
	}

	isJobs := false
	if selected == "My Library" {
		b.currentRoot = "library"
		b.currentFolderID = b.rootFolders.MyLibrary
		b.breadcrumb = []BreadcrumbEntry{{ID: b.rootFolders.MyLibrary, Name: "My Library"}}
	} else {
		b.currentRoot = "jobs"
		b.currentFolderID = b.rootFolders.MyJobs
		b.breadcrumb = []BreadcrumbEntry{{ID: b.rootFolders.MyJobs, Name: "My Jobs"}}
		isJobs = true
	}
	b.mu.Unlock()

	// Update file list sort options based on view type
	// v3.4.0: Wrap in fyne.Do() for thread safety - this callback may be invoked
	// from various contexts and SetIsJobsView modifies widget state
	if b.fileList != nil {
		fyne.Do(func() {
			b.fileList.SetIsJobsView(isJobs)
		})
	}

	b.updateBreadcrumbUI()
	go b.loadCurrentFolder()

	// v3.4.0: Notify FileBrowserTab of root change so it can update upload button state
	// Selection is cleared when switching roots, so call with empty selection
	if b.OnSelectionChanged != nil {
		b.OnSelectionChanged(nil)
	}
}

// refresh reloads the current folder
func (b *RemoteBrowser) refresh() {
	// Clear loaded items for current folder
	b.mu.Lock()
	b.loadedItems = make([]FileItem, 0)
	b.apiNextURL = ""
	b.hasMoreData = false
	b.mu.Unlock()

	// If not initialized, do full init
	b.mu.RLock()
	needsInit := b.rootFolders == nil
	b.mu.RUnlock()

	if needsInit {
		go b.initialize()
	} else {
		go b.loadCurrentFolder()
	}
}

// setLoading sets the loading state
func (b *RemoteBrowser) setLoading(loading bool) {
	b.mu.Lock()
	b.isLoading = loading
	b.mu.Unlock()

	// v3.4.0 fix: All UI updates must be on main thread (Fyne 2.5+ requirement)
	if loading {
		fyne.Do(func() {
			b.fileList.SetStatus("Loading...")
		})
		if b.refreshBtn != nil {
			fyne.Do(func() {
				b.refreshBtn.Disable()
			})
		}
	} else {
		if b.refreshBtn != nil {
			fyne.Do(func() {
				b.refreshBtn.Enable()
			})
		}
	}
}

// GetCurrentFolderID returns the current folder ID
func (b *RemoteBrowser) GetCurrentFolderID() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.currentFolderID
}

// GetSelectedItems returns the currently selected items
func (b *RemoteBrowser) GetSelectedItems() []FileItem {
	if b.fileList == nil {
		return nil
	}
	return b.fileList.GetSelectedItems()
}

// GetSelectedCount returns the number of selected items
func (b *RemoteBrowser) GetSelectedCount() int {
	if b.fileList == nil {
		return 0
	}
	return b.fileList.GetSelectedCount()
}

// ClearSelection clears all selections
func (b *RemoteBrowser) ClearSelection() {
	if b.fileList != nil {
		b.fileList.ClearSelection()
	}
}

// ClearCache clears the loaded items cache
func (b *RemoteBrowser) ClearCache() {
	b.mu.Lock()
	b.loadedItems = make([]FileItem, 0)
	b.apiNextURL = ""
	b.hasMoreData = false
	b.mu.Unlock()
}

// Refresh reloads the current folder
func (b *RemoteBrowser) Refresh() {
	b.refresh()
	// Must be on main thread for Fyne
	fyne.Do(func() {
		b.BaseWidget.Refresh()
	})
}

// GetEngine returns the engine (for operations that need it)
func (b *RemoteBrowser) GetEngine() *core.Engine {
	return b.engine
}

// SetStatus sets the status message
func (b *RemoteBrowser) SetStatus(status string) {
	if b.fileList != nil {
		b.fileList.SetStatus(status)
	}
}

// GetBreadcrumbPath returns the current breadcrumb path as a string
func (b *RemoteBrowser) GetBreadcrumbPath() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	parts := make([]string, len(b.breadcrumb))
	for i, bc := range b.breadcrumb {
		parts[i] = bc.Name
	}
	return strings.Join(parts, " > ")
}

// IsJobsView returns true if currently viewing My Jobs (upload not allowed)
// v3.4.0: Used to disable upload button when in Jobs view
func (b *RemoteBrowser) IsJobsView() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.currentRoot == "jobs"
}
