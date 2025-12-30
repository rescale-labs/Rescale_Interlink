// Package gui provides the graphical user interface for rescale-int.
// Remote (Rescale) filesystem browser component.
package gui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
)

// BrowseMode represents the current browsing mode for remote files.
// v3.6.3: Added Legacy mode for flat file listing.
type BrowseMode string

const (
	BrowseModeLibrary BrowseMode = "library" // My Library (folder-based)
	BrowseModeJobs    BrowseMode = "jobs"    // My Jobs (folder-based)
	BrowseModeLegacy  BrowseMode = "legacy"  // Legacy (flat file list)
)

// BreadcrumbEntry represents a folder in the navigation path
type BreadcrumbEntry struct {
	ID   string
	Name string
}

// RemoteBrowser is a widget for browsing Rescale files.
// Uses FileListWidget pagination with multi-page API fetching.
type RemoteBrowser struct {
	widget.BaseWidget

	mu              sync.RWMutex
	engine          *core.Engine
	currentFolderID string
	currentMode     BrowseMode // v3.6.3: "library", "jobs", or "legacy"
	rootFolders     *models.RootFolders
	breadcrumb      []BreadcrumbEntry
	lastAPIKey      string // v3.6.3: Track API key to avoid unnecessary reloads

	// Navigation generation token - incremented on each navigation
	// Used to detect and discard stale API responses from previous navigations
	navGeneration uint64

	// Loaded items and API pagination cursor
	loadedItems []FileItem // All items loaded so far for current folder
	apiNextURL  string     // Next API page URL (empty if all loaded)
	hasMoreData bool       // True if more API pages available

	// Cancellation for in-flight operations
	cancelMu   sync.Mutex
	cancelLoad context.CancelFunc
	loadingMu  sync.Mutex // Serializes load operations

	// In-flight request deduplication
	// Prevents duplicate requests for the same folder+targetCount combination
	inFlightMu  sync.Mutex
	inFlightKey string // Current in-flight request key (folderID:targetCount)

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
		currentMode: BrowseModeLibrary,
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

	// Root toggle (My Library / My Jobs / Legacy)
	// v3.6.3: Added Legacy mode for flat file listing
	b.rootToggle = widget.NewRadioGroup([]string{"My Library", "My Jobs", "Legacy"}, b.onRootChanged)
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

	// File list widget - uses its own pagination controls.
	// Fetches multiple API pages to fill the user's desired page size.
	b.fileList = NewFileListWidget()
	// Uses DefaultPageSize (25) for consistency with local browser
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

	// Layout - title is provided by parent container (FileBrowserTab).
	// FileListWidget handles pagination; we fetch API pages as needed.
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
	b.lastAPIKey = cfg.APIKey // v3.6.3: Track current API key for change detection
	switch b.currentMode {
	case BrowseModeLibrary:
		b.currentFolderID = roots.MyLibrary
		b.breadcrumb = []BreadcrumbEntry{{ID: roots.MyLibrary, Name: "My Library"}}
	case BrowseModeJobs:
		b.currentFolderID = roots.MyJobs
		b.breadcrumb = []BreadcrumbEntry{{ID: roots.MyJobs, Name: "My Jobs"}}
	case BrowseModeLegacy:
		b.currentFolderID = "" // No folder ID for legacy mode
		b.breadcrumb = []BreadcrumbEntry{{ID: "", Name: "Legacy Files"}}
	}
	b.mu.Unlock()

	b.updateBreadcrumbUI()
	b.loadCurrentFolder()

	// v3.6.3: Pre-warm the Legacy files cache in the background.
	// The /api/v3/files/ endpoint has ~9s cold-cache latency on first call,
	// but subsequent calls are ~2s due to server-side caching.
	// By pre-fetching now, we ensure Legacy mode loads quickly when the user clicks it.
	go b.prewarmLegacyCache()
}

// prewarmLegacyCache makes a background request to warm the server's file list cache.
// This reduces latency when the user switches to Legacy mode.
func (b *RemoteBrowser) prewarmLegacyCache() {
	apiClient := b.engine.API()
	if apiClient == nil {
		return
	}

	// Use a generous timeout - this is background work and we don't want to fail
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Fetch first page of files - this warms the server cache
	_, err := apiClient.ListFilesPage(ctx, "")
	if err != nil {
		// Silently ignore errors - this is just cache warming
		b.logger.Debug().Err(err).Msg("Legacy cache prewarm failed (non-critical)")
		return
	}
	b.logger.Debug().Msg("Legacy files cache pre-warmed successfully")
}

// loadCurrentFolder loads folder contents, fetching enough API pages to fill user's page size.
// Fetches multiple 25-item API pages to fill user's desired page size (max 200).
// v3.6.3: Also handles Legacy mode (flat file list).
func (b *RemoteBrowser) loadCurrentFolder() {
	// Reset state for new folder
	b.mu.Lock()
	b.loadedItems = make([]FileItem, 0)
	b.apiNextURL = ""
	b.hasMoreData = false
	isLegacy := b.currentMode == BrowseModeLegacy
	b.mu.Unlock()

	// Clear hasMoreServerData in FileListWidget to prevent stale state from previous folder
	// v3.4.0: Use fyne.Do() for thread safety
	if b.fileList != nil {
		fyne.Do(func() {
			b.fileList.SetHasMoreServerData(false)
		})
	}

	// v3.6.3: Use different loading method for Legacy mode
	if isLegacy {
		b.loadLegacyItems(0, true) // 0 = use page size, true = scroll to top
	} else {
		b.loadMoreItems(0, true) // 0 = use page size, true = scroll to top
	}
}

// loadMoreItems fetches API pages to fill the current display page
// targetCount: how many items we need total (0 = use FileListWidget's page size)
// scrollToTop: if true, scrolls to top after loading (for new folder navigation)
func (b *RemoteBrowser) loadMoreItems(targetCount int, scrollToTop bool) {
	// TIMING: Track performance metrics
	loadStart := time.Now()

	// Capture navigation generation FIRST - we'll check this before applying results
	// to prevent stale API responses from overwriting newer navigation state
	loadGen := atomic.LoadUint64(&b.navGeneration)

	// Get folder ID early for in-flight check
	b.mu.RLock()
	folderID := b.currentFolderID
	b.mu.RUnlock()

	// Build request key for deduplication
	requestKey := fmt.Sprintf("%s:%d", folderID, targetCount)

	// Check if this exact request is already in-flight
	// This prevents duplicate API calls when multiple triggers fire
	b.inFlightMu.Lock()
	if b.inFlightKey == requestKey {
		b.inFlightMu.Unlock()
		b.logger.Debug().Str("key", requestKey).Msg("Skipping duplicate in-flight request")
		return
	}
	b.inFlightKey = requestKey
	b.inFlightMu.Unlock()

	// Clear in-flight key when done (success or failure)
	defer func() {
		b.inFlightMu.Lock()
		if b.inFlightKey == requestKey {
			b.inFlightKey = ""
		}
		b.inFlightMu.Unlock()
	}()

	// Cancel any previous load operation FIRST (like LocalBrowser)
	// This ensures stale data from previous folder doesn't get displayed
	// even if we fail to acquire the lock below
	b.cancelMu.Lock()
	if b.cancelLoad != nil {
		b.cancelLoad()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	b.cancelLoad = cancel
	b.cancelMu.Unlock()

	// Serialize load operations - only one at a time
	if !b.loadingMu.TryLock() {
		b.logger.Debug().Msg("Skipping load - another operation in progress")
		return
	}
	defer b.loadingMu.Unlock()

	// Get current state (folderID already captured above)
	b.mu.RLock()
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

	// Track if this is the first load for this folder
	isFirstLoad := currentItems == 0

	// Append new items to loaded items
	b.mu.Lock()
	b.loadedItems = append(b.loadedItems, newItems...)
	hasMore = b.hasMoreData
	b.mu.Unlock()

	// Get path for display (only needed for first load)
	var pathStr string
	if isFirstLoad {
		b.mu.RLock()
		pathParts := make([]string, len(b.breadcrumb))
		for i, bc := range b.breadcrumb {
			pathParts[i] = bc.Name
		}
		b.mu.RUnlock()
		pathStr = strings.Join(pathParts, " > ")
	}

	// Final cancellation check
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Update UI - generation check happens INSIDE fyne.Do() to prevent race condition
	// where the check passes but fyne.Do() executes after another navigation started
	fyne.Do(func() {
		// CHECK INSIDE THE CALLBACK - atomic with UI update
		currentGen := atomic.LoadUint64(&b.navGeneration)
		if loadGen != currentGen {
			b.logger.Debug().
				Uint64("load_gen", loadGen).
				Uint64("current_gen", currentGen).
				Str("folder_id", folderID).
				Msg("Discarding stale load result inside fyne.Do")
			return
		}

		b.fileList.SetHasDateInfo(true) // Remote files have dateUploaded from API
		b.fileList.SetHasMoreServerData(hasMore) // Tell FileListWidget if more data is available

		if isFirstLoad {
			// First load: set all items and scroll to top, set path
			b.mu.RLock()
			allItems := make([]FileItem, len(b.loadedItems))
			copy(allItems, b.loadedItems)
			b.mu.RUnlock()
			b.fileList.SetItemsAndScrollToTop(allItems)
			b.fileList.SetPath(pathStr)
		} else if len(newItems) > 0 {
			// Subsequent load: just append new items (preserves scroll position)
			b.fileList.AppendItems(newItems)
		}

		// If no more data, finalize (re-applies filter if active)
		if !hasMore {
			b.fileList.FinalizeAppend()
		}
	})

	b.mu.RLock()
	totalLoaded := len(b.loadedItems)
	b.mu.RUnlock()

	// TIMING: Log performance metrics
	totalDuration := time.Since(loadStart)
	b.logger.Info().
		Str("folder_id", folderID).
		Int("total_loaded", totalLoaded).
		Int("new_items", len(newItems)).
		Int("pages_fetched", pagesLoaded).
		Int("target", targetCount).
		Bool("has_more", hasMore).
		Dur("total_ms", totalDuration).
		Msg("Remote folder load complete")
}

// loadLegacyItems fetches pages from the flat files API (Legacy mode).
// v3.6.3: Similar to loadMoreItems but uses ListFilesPage instead of ListFolderContentsPage.
func (b *RemoteBrowser) loadLegacyItems(targetCount int, scrollToTop bool) {
	loadStart := time.Now()
	loadGen := atomic.LoadUint64(&b.navGeneration)

	// Build request key for deduplication
	requestKey := fmt.Sprintf("legacy:%d", targetCount)

	b.inFlightMu.Lock()
	if b.inFlightKey == requestKey {
		b.inFlightMu.Unlock()
		b.logger.Debug().Str("key", requestKey).Msg("Skipping duplicate in-flight legacy request")
		return
	}
	b.inFlightKey = requestKey
	b.inFlightMu.Unlock()

	defer func() {
		b.inFlightMu.Lock()
		if b.inFlightKey == requestKey {
			b.inFlightKey = ""
		}
		b.inFlightMu.Unlock()
	}()

	// Cancel any previous load operation
	b.cancelMu.Lock()
	if b.cancelLoad != nil {
		b.cancelLoad()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	b.cancelLoad = cancel
	b.cancelMu.Unlock()

	// Serialize load operations
	if !b.loadingMu.TryLock() {
		b.logger.Debug().Msg("Skipping legacy load - another operation in progress")
		return
	}
	defer b.loadingMu.Unlock()

	// Get current state
	b.mu.RLock()
	currentItems := len(b.loadedItems)
	nextURL := b.apiNextURL
	hasMore := b.hasMoreData
	b.mu.RUnlock()

	// If no more data available, just return
	if currentItems > 0 && !hasMore {
		return
	}

	// Determine target
	if targetCount <= 0 {
		targetCount = b.fileList.GetPageSize()
	}
	if targetCount > 200 {
		targetCount = 200
	}

	// If we already have enough items, no need to fetch
	if currentItems >= targetCount {
		return
	}

	b.setLoading(true)
	defer b.setLoading(false)

	// v3.6.3: Show informative loading message for legacy mode
	// The /api/v3/files/ endpoint is slow (~10s) due to querying all files
	fyne.Do(func() {
		b.fileList.SetStatus("Loading legacy files (this may take a moment)...")
	})

	apiClient := b.engine.API()
	if apiClient == nil {
		fyne.Do(func() {
			b.fileList.SetStatus("Not connected. Configure API key in Setup tab.")
		})
		return
	}

	// Fetch pages from flat files API
	var newItems []FileItem
	pageURL := nextURL
	pagesLoaded := 0

	for currentItems+len(newItems) < targetCount {
		select {
		case <-ctx.Done():
			return
		default:
		}

		page, err := apiClient.ListFilesPage(ctx, pageURL)
		if err != nil {
			b.logger.Error().Err(err).Msg("Failed to load legacy files")
			if len(newItems) == 0 && currentItems == 0 {
				fyne.Do(func() {
					b.fileList.SetStatus("Error: " + err.Error())
				})
			}
			break
		}

		pagesLoaded++

		// Convert API files to FileItems
		for _, f := range page.Files {
			newItems = append(newItems, FileItem{
				ID:      f.ID,
				Name:    f.Name,
				Size:    f.DecryptedSize,
				ModTime: f.DateUploaded,
				// IsFolder: false (files only in legacy mode)
			})
		}

		pageURL = page.NextURL
		if pageURL == "" {
			b.mu.Lock()
			b.hasMoreData = false
			b.apiNextURL = ""
			b.mu.Unlock()
			break
		}

		b.mu.Lock()
		b.hasMoreData = true
		b.apiNextURL = pageURL
		b.mu.Unlock()
	}

	isFirstLoad := currentItems == 0

	b.mu.Lock()
	b.loadedItems = append(b.loadedItems, newItems...)
	hasMore = b.hasMoreData
	b.mu.Unlock()

	// Final cancellation check
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Update UI
	fyne.Do(func() {
		currentGen := atomic.LoadUint64(&b.navGeneration)
		if loadGen != currentGen {
			b.logger.Debug().
				Uint64("load_gen", loadGen).
				Uint64("current_gen", currentGen).
				Msg("Discarding stale legacy load result inside fyne.Do")
			return
		}

		b.fileList.SetHasDateInfo(true)
		b.fileList.SetHasMoreServerData(hasMore)

		if isFirstLoad {
			b.mu.RLock()
			allItems := make([]FileItem, len(b.loadedItems))
			copy(allItems, b.loadedItems)
			b.mu.RUnlock()
			b.fileList.SetItemsAndScrollToTop(allItems)
			b.fileList.SetPath("Legacy Files")
		} else if len(newItems) > 0 {
			b.fileList.AppendItems(newItems)
		}

		if !hasMore {
			b.fileList.FinalizeAppend()
		}
	})

	b.mu.RLock()
	totalLoaded := len(b.loadedItems)
	b.mu.RUnlock()

	totalDuration := time.Since(loadStart)
	b.logger.Info().
		Int("total_loaded", totalLoaded).
		Int("new_items", len(newItems)).
		Int("pages_fetched", pagesLoaded).
		Int("target", targetCount).
		Bool("has_more", hasMore).
		Dur("total_ms", totalDuration).
		Msg("Legacy files load complete")
}

// onPageChange is called when user navigates pages in FileListWidget
// Loads more data if user needs items beyond what's loaded
// v3.6.3: Updated to support Legacy mode.
func (b *RemoteBrowser) onPageChange(page, pageSize, totalItems int) {
	// Calculate how many items we need for the requested page
	// page is 0-indexed, so page 1 needs items 0 to pageSize-1
	// page 2 needs items pageSize to 2*pageSize-1, etc.
	itemsNeeded := (page + 1) * pageSize // Items needed to show this page

	b.mu.RLock()
	loadedCount := len(b.loadedItems)
	hasMore := b.hasMoreData
	isLegacy := b.currentMode == BrowseModeLegacy
	b.mu.RUnlock()

	// Load more if we need items beyond what's loaded
	if hasMore && itemsNeeded > loadedCount {
		b.logger.Debug().
			Int("page", page).
			Int("pageSize", pageSize).
			Int("itemsNeeded", itemsNeeded).
			Int("loadedCount", loadedCount).
			Bool("isLegacy", isLegacy).
			Msg("Loading more items for pagination")
		if isLegacy {
			go b.loadLegacyItems(itemsNeeded, false)
		} else {
			go b.loadMoreItems(itemsNeeded, false)
		}
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
	// Clear selection first - prevents stale selection from previous folder
	if b.fileList != nil {
		b.fileList.ClearSelection()
	}

	// Increment navigation generation to invalidate any in-flight loads
	atomic.AddUint64(&b.navGeneration, 1)

	b.mu.Lock()
	b.currentFolderID = folderID
	b.breadcrumb = append(b.breadcrumb, BreadcrumbEntry{ID: folderID, Name: folderName})
	b.mu.Unlock()

	b.updateBreadcrumbUI()
	go b.loadCurrentFolder()
}

// navigateToBreadcrumb navigates to a breadcrumb entry
func (b *RemoteBrowser) navigateToBreadcrumb(index int) {
	// Clear selection first - prevents stale selection from previous folder
	if b.fileList != nil {
		b.fileList.ClearSelection()
	}

	// Increment navigation generation to invalidate any in-flight loads
	atomic.AddUint64(&b.navGeneration, 1)

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
	// Clear selection first - prevents stale selection from previous folder
	if b.fileList != nil {
		b.fileList.ClearSelection()
	}

	// Increment navigation generation to invalidate any in-flight loads
	atomic.AddUint64(&b.navGeneration, 1)

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
			// Pre-compute index to avoid capturing len(breadcrumb)-1 expression in closure
			currentIdx := len(breadcrumb) - 1
			btnCurrent := widget.NewButton(breadcrumb[currentIdx].Name, func() {
				b.navigateToBreadcrumb(currentIdx)
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
// v3.6.3: Updated to support Legacy mode.
func (b *RemoteBrowser) onRootChanged(selected string) {
	// Clear selection first - prevents stale selection from previous root
	if b.fileList != nil {
		b.fileList.ClearSelection()
	}

	// Increment navigation generation to invalidate any in-flight loads
	atomic.AddUint64(&b.navGeneration, 1)

	b.mu.Lock()
	// For Legacy mode, we don't need rootFolders
	if b.rootFolders == nil && selected != "Legacy" {
		b.mu.Unlock()
		return
	}

	isJobs := false
	switch selected {
	case "My Library":
		b.currentMode = BrowseModeLibrary
		b.currentFolderID = b.rootFolders.MyLibrary
		b.breadcrumb = []BreadcrumbEntry{{ID: b.rootFolders.MyLibrary, Name: "My Library"}}
	case "My Jobs":
		b.currentMode = BrowseModeJobs
		b.currentFolderID = b.rootFolders.MyJobs
		b.breadcrumb = []BreadcrumbEntry{{ID: b.rootFolders.MyJobs, Name: "My Jobs"}}
		isJobs = true
	case "Legacy":
		b.currentMode = BrowseModeLegacy
		b.currentFolderID = "" // No folder ID for legacy mode
		b.breadcrumb = []BreadcrumbEntry{{ID: "", Name: "Legacy Files"}}
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

// OnConfigChanged handles API key/config changes (v3.6.3)
// Called when Engine publishes EventConfigChanged.
// Only clears caches and re-initializes if the API key actually changed.
func (b *RemoteBrowser) OnConfigChanged() {
	// Check if API key actually changed
	cfg := b.engine.GetConfig()
	newAPIKey := ""
	if cfg != nil {
		newAPIKey = cfg.APIKey
	}

	b.mu.RLock()
	lastKey := b.lastAPIKey
	b.mu.RUnlock()

	// If API key hasn't changed, skip the reload
	if newAPIKey == lastKey && lastKey != "" {
		b.logger.Debug().Msg("Config changed but API key unchanged - skipping reload")
		return
	}

	b.logger.Info().Msg("API key changed - clearing caches and re-initializing")

	// Increment navigation generation to invalidate any in-flight loads
	atomic.AddUint64(&b.navGeneration, 1)

	// Clear all cached state
	b.mu.Lock()
	b.rootFolders = nil
	b.loadedItems = make([]FileItem, 0)
	b.apiNextURL = ""
	b.hasMoreData = false
	b.currentFolderID = ""
	b.currentMode = BrowseModeLibrary // Reset to default
	b.breadcrumb = nil
	b.lastAPIKey = newAPIKey // Track new key
	b.mu.Unlock()

	// Clear file list
	if b.fileList != nil {
		fyne.Do(func() {
			b.fileList.SetItems(nil)
			b.fileList.SetStatus("Re-authenticating...")
			b.fileList.SetPath("")
		})
	}

	// Reset root toggle to default
	if b.rootToggle != nil {
		fyne.Do(func() {
			b.rootToggle.SetSelected("My Library")
		})
	}

	// Re-initialize with new credentials
	go b.initialize()
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
	return b.currentMode == BrowseModeJobs
}

// IsLegacyView returns true if currently viewing Legacy Files (upload not allowed).
// v3.6.3: Used to disable upload button when in Legacy view.
func (b *RemoteBrowser) IsLegacyView() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.currentMode == BrowseModeLegacy
}

// IsUploadAllowed returns true if uploads are allowed in the current view.
// v3.6.3: Uploads are only allowed in My Library mode.
func (b *RemoteBrowser) IsUploadAllowed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.currentMode == BrowseModeLibrary
}

// GetFileList returns the file list widget for configuration.
// v3.6.3: Used to set up sort persistence callback.
func (b *RemoteBrowser) GetFileList() *FileListWidget {
	return b.fileList
}
