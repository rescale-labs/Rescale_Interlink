// Package gui provides the graphical user interface for rescale-int.
// File list widget - reusable component for displaying files and folders.
package gui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// naturalLess performs natural/numeric string comparison for sorting.
// Returns true if a < b using natural sort order.
// "file2" < "file10" (unlike lexicographic "file10" < "file2")
// Handles leading zeros: "file02" == "file2" for numeric value, shorter run wins.
func naturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		// If both positions start with digits, compare as numbers
		if a[i] >= '0' && a[i] <= '9' && b[j] >= '0' && b[j] <= '9' {
			// Extract number from a (skip leading zeros for value comparison)
			numStartA := i
			for i < len(a) && a[i] == '0' {
				i++ // skip leading zeros
			}
			valStartA := i
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			numLenA := i - numStartA // total length including zeros
			valA := a[valStartA:i]   // value part (no leading zeros)

			// Extract number from b
			numStartB := j
			for j < len(b) && b[j] == '0' {
				j++
			}
			valStartB := j
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			numLenB := j - numStartB
			valB := b[valStartB:j]

			// Compare by numeric value first (length then lexicographic)
			if len(valA) != len(valB) {
				return len(valA) < len(valB)
			}
			if valA != valB {
				return valA < valB
			}
			// Equal numeric values: shorter run wins (fewer leading zeros)
			if numLenA != numLenB {
				return numLenA < numLenB
			}
			continue
		}

		// Compare as characters (case-insensitive already done via lowerName)
		if a[i] != b[j] {
			return a[i] < b[j]
		}
		i++
		j++
	}
	return len(a) < len(b)
}

const (
	// FileListFontScale reduces font size for compact display (70% = 30% reduction)
	FileListFontScale = 0.70

	// Pagination defaults
	DefaultPageSize = 25  // Default items per page (matches API page size)
	MinPageSize     = 10  // Minimum items per page
	MaxPageSize     = 200 // Maximum items per page
)

// FileItem represents a file or folder in the list
type FileItem struct {
	ID        string    // File/folder ID (for remote) or path (for local)
	Name      string
	Size      int64
	IsFolder  bool
	Selected  bool
	ModTime   time.Time // Modification time (for local files; zero for remote)
	lowerName string    // PERFORMANCE: Cached lowercase name for filtering (internal use)
}

// FileListWidget is a reusable widget for displaying a list of files and folders
type FileListWidget struct {
	widget.BaseWidget

	mu            sync.RWMutex
	items         []FileItem
	itemIndexByID map[string]int  // PERFORMANCE: O(1) lookup of item index by ID
	selectedItems map[string]bool

	// Sort state
	sortBy        string // "name", "size", "type", "date"
	sortAscending bool
	hasDateInfo   bool // True if items have ModTime populated (local files only)
	isJobsView    bool // True when viewing jobs (limits sort options to name/date only)

	// Filter state
	filterQuery       string       // Current filter query (lowercase)
	filteredItems     []FileItem   // Filtered items (nil if no filter active)
	filterDebounce    *time.Timer  // Debounce timer for filter input
	filterGeneration  uint64       // Generation counter to discard stale filter results

	// Pagination state
	pageSize          int  // Items per page (default 25, max 200)
	currentPage       int  // Current page (0-indexed)
	hasMoreServerData bool // True if more data available on server (for lazy loading)

	// Callbacks
	OnFolderOpen       func(item FileItem)                  // Called when folder is double-clicked/entered
	OnSelectionChanged func(selected []FileItem)            // Called when selection changes
	OnPageChange       func(page, pageSize, totalItems int) // Called when page changes (for lazy loading)

	// UI components
	list           *widget.List
	selectAllCheck *widget.Check
	statusLabel    *widget.Label
	pathLabel      *canvas.Text // Changed to canvas.Text for font size consistency
	sortBtn        *widget.Button
	filterEntry    *widget.Entry
	currentPath    string

	// Pagination UI
	prevPageBtn        *widget.Button
	nextPageBtn        *widget.Button
	pageLabel          *widget.Label
	pageSizeEntry      *widget.Entry
	paginationBar      *fyne.Container // Container for pagination controls
	paginationHidden   bool            // When true, pagination controls are hidden

	// Selection state management
	updatingSelectAll bool // Flag to prevent callback recursion when programmatically updating select all
}

// NewFileListWidget creates a new file list widget
func NewFileListWidget() *FileListWidget {
	w := &FileListWidget{
		itemIndexByID: make(map[string]int),
		selectedItems: make(map[string]bool),
		currentPath:   "",
		sortBy:        "type", // Default: folders first, then by date
		sortAscending: true,
		hasDateInfo:   false,
		pageSize:      DefaultPageSize,
		currentPage:   0,
	}
	w.ExtendBaseWidget(w)
	return w
}

// createSizedText creates a canvas.Text with scaled font size for compact display
func createSizedText(content string, align fyne.TextAlign) *canvas.Text {
	text := canvas.NewText(content, theme.ForegroundColor())
	text.TextSize = theme.TextSize() * FileListFontScale
	text.Alignment = align
	return text
}

// CreateRenderer implements fyne.Widget
func (w *FileListWidget) CreateRenderer() fyne.WidgetRenderer {
	// Path/breadcrumb label at top - using canvas.Text for font size consistency with file names
	w.pathLabel = canvas.NewText("", theme.ForegroundColor())
	w.pathLabel.TextSize = theme.TextSize() * FileListFontScale
	w.pathLabel.TextStyle = fyne.TextStyle{Bold: true}
	w.pathLabel.Alignment = fyne.TextAlignCenter // Center the path text

	// Sort button with label to the left
	w.sortBtn = widget.NewButtonWithIcon("", theme.MenuDropDownIcon(), w.showSortMenu)

	// Filter entry (width constrained by fixedWidthLayout in topBar)
	// Uses debounce to prevent UI freezes during rapid typing
	w.filterEntry = widget.NewEntry()
	w.filterEntry.SetPlaceHolder("Filter...")
	w.filterEntry.OnChanged = func(query string) {
		// Cancel any pending debounce timer
		if w.filterDebounce != nil {
			w.filterDebounce.Stop()
		}

		// Increment generation to mark this as the latest filter request
		gen := atomic.AddUint64(&w.filterGeneration, 1)

		// Debounce: wait 200ms before applying filter
		// This prevents UI freezes when typing quickly with large lists
		w.filterDebounce = time.AfterFunc(200*time.Millisecond, func() {
			w.applyFilterWithGeneration(query, gen)
		})
	}

	// Create the list widget with pagination support
	w.list = widget.NewList(
		func() int {
			w.mu.RLock()
			defer w.mu.RUnlock()
			displayItems := w.getDisplayItemsLocked()
			totalItems := len(displayItems)
			// Return items for current page only
			startIdx := w.currentPage * w.pageSize
			if startIdx >= totalItems {
				return 0
			}
			endIdx := startIdx + w.pageSize
			if endIdx > totalItems {
				endIdx = totalItems
			}
			return endIdx - startIdx
		},
		func() fyne.CanvasObject {
			// Template for each row: checkbox, icon, name, size, type
			check := widget.NewCheck("", nil)
			icon := widget.NewIcon(theme.FileIcon())

			// Use widget.RichText for name - supports truncation AND custom size
			name := widget.NewRichText(&widget.TextSegment{
				Text: "Filename placeholder",
				Style: widget.RichTextStyle{
					TextStyle: fyne.TextStyle{},
				},
			})
			name.Truncation = fyne.TextTruncateEllipsis

			// Use canvas.Text for size, type, and date (fixed width, no truncation needed)
			size := createSizedText("999.9 MB", fyne.TextAlignTrailing)
			typeText := createSizedText("Folder", fyne.TextAlignCenter)
			dateText := createSizedText("2025-01-01", fyne.TextAlignCenter)

			// Add padding around size, type, and date for better spacing
			sizeContainer := container.NewPadded(container.NewStack(size))
			typeStack := container.NewStack(typeText)
			typeContainer := container.NewPadded(typeStack)
			// Date column needs extra right padding to avoid touching the edge/divider
			dateStack := container.NewStack(dateText)
			dateWithRightPadding := container.NewHBox(dateStack, HorizontalSpacer(12))
			dateContainer := container.NewPadded(dateWithRightPadding)

			// Use Border layout: checkbox+icon on left, size+type+date on right, name in center
			row := container.NewBorder(
				nil, nil,
				container.NewHBox(check, icon),
				container.NewHBox(sizeContainer, typeContainer, dateContainer),
				name, // RichText in center - will truncate to fit
			)
			return row
		},
		func(i widget.ListItemID, obj fyne.CanvasObject) {
			w.updateListItem(i, obj)
		},
	)

	// Handle item selection (single click)
	w.list.OnSelected = func(id widget.ListItemID) {
		w.onItemTapped(id)
		w.list.UnselectAll() // Don't keep visual selection, we use checkboxes
	}

	// Select all checkbox
	w.selectAllCheck = widget.NewCheck("Select All", func(checked bool) {
		// Ignore programmatic updates (e.g., from updateSelectAllState)
		if w.updatingSelectAll {
			return
		}
		w.setAllSelected(checked)
	})

	// Status label
	w.statusLabel = widget.NewLabel("")
	w.statusLabel.TextStyle = fyne.TextStyle{Italic: true}

	// Layout: sort button + filter at top, list in center, controls at bottom
	// Path text removed - redundant with nav bar above
	// Add "Sort:" label and padding around sort button on left side
	sortLabel := widget.NewLabel("Sort:")
	sortWithPadding := container.NewHBox(
		HorizontalSpacer(4), // Buffer from left edge
		sortLabel,
		w.sortBtn,
	)
	// Wrap filter entry in a container with fixed max width and right padding
	filterWrapper := container.New(&fixedWidthLayout{width: 130}, w.filterEntry)
	filterWithPadding := container.NewHBox(
		filterWrapper,
		HorizontalSpacer(4), // Buffer from right edge
	)
	// Simple top bar with sort on left, filter on right (no center path text)
	topBar := container.NewBorder(nil, nil, sortWithPadding, filterWithPadding, nil)

	// Pagination controls - compact layout to minimize width impact
	w.prevPageBtn = widget.NewButtonWithIcon("", theme.NavigateBackIcon(), w.previousPage)
	w.nextPageBtn = widget.NewButtonWithIcon("", theme.NavigateNextIcon(), w.nextPage)
	w.pageLabel = widget.NewLabel("1/1") // Compact format: "1/1" instead of "Page 1 of 1"

	// Page size entry with validation - narrower
	w.pageSizeEntry = widget.NewEntry()
	w.pageSizeEntry.SetText(fmt.Sprintf("%d", w.pageSize))
	w.pageSizeEntry.OnSubmitted = w.onPageSizeChanged
	pageSizeWrapper := container.New(&fixedWidthLayout{width: 40}, w.pageSizeEntry)

	// Pagination bar: compact layout [< 1/1 > | 40]
	w.paginationBar = container.NewHBox(
		w.prevPageBtn,
		w.pageLabel,
		w.nextPageBtn,
		HorizontalSpacer(8),
		pageSizeWrapper,
	)

	// Bottom bar: select all on left, status in center, pagination on right
	bottomBar := container.NewBorder(nil, nil, w.selectAllCheck, w.paginationBar, w.statusLabel)

	// Create white background for the list
	listBackground := canvas.NewRectangle(color.White)
	listWithBackground := container.NewStack(listBackground, w.list)

	content := container.NewBorder(
		topBar,
		bottomBar,
		nil, nil,
		listWithBackground,
	)

	return widget.NewSimpleRenderer(content)
}

// updateListItem updates a single list item with data
// This is called from the main thread during list rendering
func (w *FileListWidget) updateListItem(index int, obj fyne.CanvasObject) {
	// Copy item data with lock, then release before UI updates
	w.mu.RLock()
	displayItems := w.getDisplayItemsLocked()
	// Map page-relative index to actual index
	actualIndex := w.currentPage*w.pageSize + index
	if actualIndex >= len(displayItems) {
		w.mu.RUnlock()
		return
	}
	item := displayItems[actualIndex]
	w.mu.RUnlock()

	// All UI updates happen on main thread (this function is called by Fyne during rendering)
	// No need for fyne.Do() here, but we released the lock first to minimize contention
	row := obj.(*fyne.Container)

	// Get components from the Border layout
	// Structure: row.Objects[0]=center(nameRichText), [1]=left(HBox), [2]=right(HBox)
	leftBox := row.Objects[1].(*fyne.Container)
	rightBox := row.Objects[2].(*fyne.Container)
	nameRichText := row.Objects[0].(*widget.RichText) // RichText for smaller font

	check := leftBox.Objects[0].(*widget.Check)
	icon := leftBox.Objects[1].(*widget.Icon)

	// Navigate through padded containers to get canvas.Text objects
	// rightBox is HBox containing [sizeContainer (Padded), typeContainer (Padded), dateContainer (Padded)]
	sizePadded := rightBox.Objects[0].(*fyne.Container)    // Padded container
	typePadded := rightBox.Objects[1].(*fyne.Container)    // Padded container
	datePadded := rightBox.Objects[2].(*fyne.Container)    // Padded container
	sizeStack := sizePadded.Objects[0].(*fyne.Container)   // Stack inside Padded
	// Type column: Padded -> Stack -> Text
	typeStack := typePadded.Objects[0].(*fyne.Container)   // Stack inside Padded
	// Date column: Padded -> HBox -> Stack -> Text
	dateHBox := datePadded.Objects[0].(*fyne.Container)    // HBox inside Padded
	dateStack := dateHBox.Objects[0].(*fyne.Container)     // Stack inside HBox
	sizeText := sizeStack.Objects[0].(*canvas.Text)
	typeText := typeStack.Objects[0].(*canvas.Text)
	dateText := dateStack.Objects[0].(*canvas.Text)

	// CRITICAL FIX: Capture item ID (immutable) instead of index (which changes on scroll)
	// This prevents selection state corruption when scrolling causes list item recycling
	itemID := item.ID

	// Disable callback before setting checked state to prevent spurious triggers during recycling
	check.OnChanged = nil
	check.SetChecked(item.Selected)
	check.OnChanged = func(checked bool) {
		w.setItemSelectedByID(itemID, checked)
	}

	// Update icon
	if item.IsFolder {
		icon.SetResource(theme.FolderIcon())
	} else {
		icon.SetResource(theme.FileIcon())
	}

	// Update name (RichText with smaller font, handles truncation)
	// PERFORMANCE: Don't call Refresh() on individual elements - parent list handles refresh
	nameRichText.Segments = []widget.RichTextSegment{
		&widget.TextSegment{
			Text: item.Name,
			Style: widget.RichTextStyle{
				TextStyle: fyne.TextStyle{},
				SizeName:  theme.SizeNameCaptionText, // Smaller text size
			},
		},
	}

	if item.IsFolder {
		sizeText.Text = "--"
	} else if item.Size < 0 {
		sizeText.Text = "?" // Unknown size (stat failed)
	} else {
		sizeText.Text = FormatFileSize(item.Size)
	}
	sizeText.Color = theme.ForegroundColor()

	if item.IsFolder {
		typeText.Text = "Folder"
	} else {
		typeText.Text = "File"
	}
	typeText.Color = theme.ForegroundColor()

	// Format date as YYYY-MM-DD, or "--" if no date
	if item.ModTime.IsZero() {
		dateText.Text = "--"
	} else {
		dateText.Text = item.ModTime.Format("2006-01-02")
	}
	dateText.Color = theme.ForegroundColor()
}

// onItemTapped handles when an item is tapped
func (w *FileListWidget) onItemTapped(index int) {
	w.mu.RLock()
	displayItems := w.getDisplayItemsLocked()
	// Map page-relative index to actual index
	actualIndex := w.currentPage*w.pageSize + index
	if actualIndex >= len(displayItems) {
		w.mu.RUnlock()
		return
	}
	item := displayItems[actualIndex]
	w.mu.RUnlock()

	if item.IsFolder {
		// Open folder
		if w.OnFolderOpen != nil {
			w.OnFolderOpen(item)
		}
	} else {
		// Toggle selection for files
		w.mu.Lock()
		newSelected := !item.Selected

		// PERFORMANCE: Use index map for O(1) lookup instead of linear search
		if idx, ok := w.itemIndexByID[item.ID]; ok && idx < len(w.items) {
			w.items[idx].Selected = newSelected
		}

		// Update filtered items if active
		if w.filteredItems != nil && index < len(w.filteredItems) {
			w.filteredItems[index].Selected = newSelected
		}

		w.selectedItems[item.ID] = newSelected
		w.mu.Unlock()
		w.list.RefreshItem(widget.ListItemID(index))
		w.notifySelectionChanged()
		w.updateSelectAllState()
	}
}

// setItemSelectedByID sets the selection state of a single item by ID
// This is the preferred method as it's immune to scroll-induced index changes
// PERFORMANCE: Uses O(1) index map lookup instead of O(n) linear search
func (w *FileListWidget) setItemSelectedByID(itemID string, selected bool) {
	w.mu.Lock()

	// PERFORMANCE: Use index map for O(1) lookup instead of linear search
	if idx, ok := w.itemIndexByID[itemID]; ok && idx < len(w.items) {
		w.items[idx].Selected = selected
	}

	// Also update filtered items if filtering is active
	// Note: filteredItems doesn't have an index map, but this is less critical
	// since filtering happens less frequently than selection
	if w.filteredItems != nil {
		for i := range w.filteredItems {
			if w.filteredItems[i].ID == itemID {
				w.filteredItems[i].Selected = selected
				break
			}
		}
	}

	w.selectedItems[itemID] = selected
	w.mu.Unlock()

	// Refresh the list to show updated checkbox state
	if w.list != nil {
		fyne.Do(func() {
			w.list.Refresh()
		})
	}

	w.notifySelectionChanged()
	w.updateSelectAllState()
}

// setAllSelected sets all items to selected or unselected
func (w *FileListWidget) setAllSelected(selected bool) {
	w.mu.Lock()
	for i := range w.items {
		w.items[i].Selected = selected
		w.selectedItems[w.items[i].ID] = selected
	}
	w.mu.Unlock()

	// UI updates must be on main thread
	fyne.Do(func() {
		w.list.Refresh()
	})
	w.notifySelectionChanged()
}

// notifySelectionChanged calls the selection changed callback
func (w *FileListWidget) notifySelectionChanged() {
	if w.OnSelectionChanged != nil {
		w.OnSelectionChanged(w.GetSelectedItems())
	}
}

// updateSelectAllState updates the select all checkbox based on current selections
func (w *FileListWidget) updateSelectAllState() {
	w.mu.RLock()
	allSelected := len(w.items) > 0
	for _, item := range w.items {
		if !item.Selected {
			allSelected = false
			break
		}
	}
	w.mu.RUnlock()

	// UI updates must be on main thread
	// Set flag to prevent checkbox callback from triggering setAllSelected
	if w.selectAllCheck != nil {
		fyne.Do(func() {
			w.updatingSelectAll = true
			w.selectAllCheck.SetChecked(allSelected)
			w.updatingSelectAll = false
		})
	}
}

// SetItems sets the items to display
// This preserves the current scroll position for smooth scrolling
// Items are automatically sorted according to current sort settings
func (w *FileListWidget) SetItems(items []FileItem) {
	w.mu.Lock()
	oldCount := len(w.items)
	w.items = items
	w.selectedItems = make(map[string]bool)
	// Only reset to first page if items decreased (new folder)
	// Preserve page if items increased (loading more data)
	if len(items) < oldCount {
		w.currentPage = 0
	}
	// PERFORMANCE: Cache lowercase names and build index map
	w.itemIndexByID = make(map[string]int, len(w.items))
	for i := range w.items {
		w.items[i].lowerName = strings.ToLower(w.items[i].Name)
		w.itemIndexByID[w.items[i].ID] = i
	}
	w.mu.Unlock()

	// Apply current sort and refresh
	w.sortAndRefresh()
	w.refreshPagination() // Update pagination buttons and display
	w.updateStatus()
	w.updateSelectAllState()
}

// SetItemsAndScrollToTop sets the items and resets scroll to top
// Use this when navigating to a new directory/folder
// Applies the current sort mode to maintain consistent user experience.
func (w *FileListWidget) SetItemsAndScrollToTop(items []FileItem) {
	w.mu.Lock()

	// Clear stale filter cache from previous directory
	// Without this, getDisplayItemsLocked() may return old filtered items
	w.filteredItems = nil

	w.items = items
	w.selectedItems = make(map[string]bool)
	w.currentPage = 0 // Reset to first page when items change

	// Calculate folder/file counts and cache lowercase names (needed for sorting)
	folderCount, fileCount := 0, 0
	for i := range w.items {
		if w.items[i].IsFolder {
			folderCount++
		} else {
			fileCount++
		}
		// Cache lowercase name for O(1) filtering and sorting
		w.items[i].lowerName = strings.ToLower(w.items[i].Name)
	}

	// Apply current sort mode (preserves user's selected sort across navigation)
	w.sortItemsLocked(w.items)

	// Build ID-to-index map AFTER sorting (indices have changed)
	w.itemIndexByID = make(map[string]int, len(w.items))
	for i := range w.items {
		w.itemIndexByID[w.items[i].ID] = i
	}

	// Recompute filter for new items if filter is active
	// This preserves the user's filter across navigation instead of silently clearing it
	if w.filterQuery != "" {
		w.filteredItems = make([]FileItem, 0, len(w.items)/4)
		for _, item := range w.items {
			if strings.Contains(item.lowerName, w.filterQuery) {
				w.filteredItems = append(w.filteredItems, item)
			}
		}
	}

	w.mu.Unlock()

	// PERFORMANCE: Batch all UI updates into a single fyne.Do call
	// This avoids multiple main thread round-trips
	fyne.Do(func() {
		if w.list != nil {
			w.list.Refresh()
			w.list.ScrollToTop()
		}
		// Update status with pre-calculated counts
		if w.statusLabel != nil {
			w.statusLabel.SetText(fmt.Sprintf("%d folders, %d files", folderCount, fileCount))
		}
		// Update pagination
		w.refreshPaginationUI()
	})

	w.updateSelectAllState()
}

// GetItems returns all items
func (w *FileListWidget) GetItems() []FileItem {
	w.mu.RLock()
	defer w.mu.RUnlock()
	result := make([]FileItem, len(w.items))
	copy(result, w.items)
	return result
}

// GetSelectedItems returns all selected items
func (w *FileListWidget) GetSelectedItems() []FileItem {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var selected []FileItem
	for _, item := range w.items {
		if item.Selected {
			selected = append(selected, item)
		}
	}
	return selected
}

// GetSelectedCount returns the number of selected items
func (w *FileListWidget) GetSelectedCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	count := 0
	for _, item := range w.items {
		if item.Selected {
			count++
		}
	}
	return count
}

// SetPath sets the current path display
func (w *FileListWidget) SetPath(path string) {
	w.mu.Lock()
	w.currentPath = path
	w.mu.Unlock()

	// UI updates must be on main thread
	if w.pathLabel != nil {
		fyne.Do(func() {
			w.pathLabel.Text = path
			w.pathLabel.Color = theme.ForegroundColor() // Handle theme changes
			w.pathLabel.Refresh()
		})
	}
}

// GetPath returns the current path
func (w *FileListWidget) GetPath() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.currentPath
}

// SetStatus sets the status text
func (w *FileListWidget) SetStatus(status string) {
	// UI updates must be on main thread
	if w.statusLabel != nil {
		fyne.Do(func() {
			w.statusLabel.SetText(status)
		})
	}
}

// updateStatus updates the status text based on current items
// NOTE: This only updates the status text. Pagination UI is updated by refreshPagination()
func (w *FileListWidget) updateStatus() {
	w.mu.RLock()
	folderCount := 0
	fileCount := 0
	for _, item := range w.items {
		if item.IsFolder {
			folderCount++
		} else {
			fileCount++
		}
	}
	w.mu.RUnlock()

	status := fmt.Sprintf("%d folders, %d files", folderCount, fileCount)
	w.SetStatus(status)
}

// ClearSelection clears all selections
func (w *FileListWidget) ClearSelection() {
	w.mu.Lock()
	for i := range w.items {
		w.items[i].Selected = false
	}
	w.selectedItems = make(map[string]bool)
	w.mu.Unlock()

	// UI updates must be on main thread
	if w.list != nil {
		fyne.Do(func() {
			w.list.Refresh()
		})
	}
	w.updateSelectAllState()
	w.notifySelectionChanged()
}

// Refresh refreshes the widget
// PERFORMANCE: Combined into single fyne.Do() call for efficiency
func (w *FileListWidget) Refresh() {
	fyne.Do(func() {
		if w.list != nil {
			w.list.Refresh()
		}
		w.BaseWidget.Refresh()
	})
}

// Sort functionality

// showSortMenu displays the sort options menu
func (w *FileListWidget) showSortMenu() {
	w.mu.RLock()
	hasDate := w.hasDateInfo
	isJobs := w.isJobsView
	w.mu.RUnlock()

	var items []*fyne.MenuItem

	if isJobs {
		// Jobs view: only Name and Date Created (no Size or Type since jobs don't report size)
		items = []*fyne.MenuItem{
			fyne.NewMenuItem("Name (A→Z)", func() { w.setSortMode("name", true) }),
			fyne.NewMenuItem("Name (Z→A)", func() { w.setSortMode("name", false) }),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Date Created (Oldest)", func() { w.setSortMode("date", true) }),
			fyne.NewMenuItem("Date Created (Newest)", func() { w.setSortMode("date", false) }),
		}
	} else {
		// Full options for files view
		items = []*fyne.MenuItem{
			fyne.NewMenuItem("Type (Folders First)", func() { w.setSortMode("type", true) }),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Name (A→Z)", func() { w.setSortMode("name", true) }),
			fyne.NewMenuItem("Name (Z→A)", func() { w.setSortMode("name", false) }),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Size (Smallest)", func() { w.setSortMode("size", true) }),
			fyne.NewMenuItem("Size (Largest)", func() { w.setSortMode("size", false) }),
		}

		// Add date sorting if date info is available (local or remote with dates)
		if hasDate {
			items = append(items,
				fyne.NewMenuItemSeparator(),
				fyne.NewMenuItem("Date Created (Oldest)", func() { w.setSortMode("date", true) }),
				fyne.NewMenuItem("Date Created (Newest)", func() { w.setSortMode("date", false) }),
			)
		}
	}

	menu := fyne.NewMenu("Sort By", items...)
	canvas := fyne.CurrentApp().Driver().CanvasForObject(w)
	popup := widget.NewPopUpMenu(menu, canvas)
	if w.sortBtn != nil {
		// Get button's absolute position on canvas by walking up the widget tree
		absPos := getAbsolutePosition(w.sortBtn)
		size := w.sortBtn.Size()
		popup.ShowAtPosition(fyne.NewPos(absPos.X, absPos.Y+size.Height))
	} else {
		popup.Show()
	}
}

// setSortMode sets the sort mode and refreshes the list
func (w *FileListWidget) setSortMode(sortBy string, ascending bool) {
	w.mu.Lock()
	w.sortBy = sortBy
	w.sortAscending = ascending
	w.mu.Unlock()
	w.sortAndRefresh()
}

// sortAndRefresh sorts the items and refreshes the list.
// Delegates to sortItemsLocked for consistent behavior with natural sort,
// stable sorting, and proper tie-breakers.
func (w *FileListWidget) sortAndRefresh() {
	w.mu.Lock()

	// Ensure all items have cached lowercase names (needed for sorting)
	for i := range w.items {
		if w.items[i].lowerName == "" {
			w.items[i].lowerName = strings.ToLower(w.items[i].Name)
		}
	}

	// Use the unified sorting implementation
	w.sortItemsLocked(w.items)

	// Rebuild index map after sorting (indices have changed)
	for i := range w.items {
		w.itemIndexByID[w.items[i].ID] = i
	}
	w.mu.Unlock()

	if w.list != nil {
		fyne.Do(func() {
			w.list.Refresh()
		})
	}
}

// sortItemsLocked sorts the given items slice according to current sort settings.
// Must be called with w.mu held.
// Uses the cached lowerName field for efficient name comparisons.
// Uses SliceStable and proper tie-breakers for deterministic, correct ordering.
// Uses naturalLess() for name comparisons to get "file1, file2, file10" order.
func (w *FileListWidget) sortItemsLocked(items []FileItem) {
	sortBy := w.sortBy
	ascending := w.sortAscending

	sort.SliceStable(items, func(i, j int) bool {
		item1, item2 := items[i], items[j]

		// For "type" sort: folders first, then sort by extension
		if sortBy == "type" {
			if item1.IsFolder != item2.IsFolder {
				return item1.IsFolder
			}
			// Both are same type (folder or file)
			// For files, sort by extension; for folders, sort by name
			if !item1.IsFolder {
				ext1 := filepath.Ext(item1.lowerName)
				ext2 := filepath.Ext(item2.lowerName)
				if ext1 != ext2 {
					// Plain lexicographic for extensions (natural sort rarely matters for extensions)
					if ascending {
						return ext1 < ext2
					}
					return ext1 > ext2
				}
			}
			// Same extension (or both folders): fall back to name with natural sort
			if item1.lowerName != item2.lowerName {
				if ascending {
					return naturalLess(item1.lowerName, item2.lowerName)
				}
				return naturalLess(item2.lowerName, item1.lowerName)
			}
			return false // Equal - stable sort preserves order
		}

		// For other sorts, always put folders before files
		if item1.IsFolder != item2.IsFolder {
			return item1.IsFolder
		}

		// Primary sort comparison - use direct comparison to avoid !less bug
		// (negating a boolean when items are equal breaks strict weak ordering)
		switch sortBy {
		case "name", "":
			// Use natural sort for name comparisons: file1, file2, file10
			if item1.lowerName != item2.lowerName {
				if ascending {
					return naturalLess(item1.lowerName, item2.lowerName)
				}
				return naturalLess(item2.lowerName, item1.lowerName)
			}
		case "size":
			if item1.Size != item2.Size {
				if ascending {
					return item1.Size < item2.Size
				}
				return item1.Size > item2.Size
			}
			// Tie-breaker: sort by name (natural sort) for equal sizes
			if item1.lowerName != item2.lowerName {
				if ascending {
					return naturalLess(item1.lowerName, item2.lowerName)
				}
				return naturalLess(item2.lowerName, item1.lowerName)
			}
		case "date":
			if !item1.ModTime.Equal(item2.ModTime) {
				if ascending {
					return item1.ModTime.Before(item2.ModTime)
				}
				return item1.ModTime.After(item2.ModTime)
			}
			// Tie-breaker: sort by name (natural sort) for equal dates
			if item1.lowerName != item2.lowerName {
				if ascending {
					return naturalLess(item1.lowerName, item2.lowerName)
				}
				return naturalLess(item2.lowerName, item1.lowerName)
			}
		}

		// Equal on all criteria - stable sort preserves original order
		return false
	})
}

// SetHasDateInfo indicates whether items have ModTime populated (for local files)
func (w *FileListWidget) SetHasDateInfo(hasDate bool) {
	w.mu.Lock()
	w.hasDateInfo = hasDate
	w.mu.Unlock()
}

// SetIsJobsView sets whether this widget is showing jobs (limits sort options)
func (w *FileListWidget) SetIsJobsView(isJobs bool) {
	w.mu.Lock()
	w.isJobsView = isJobs
	// When switching to jobs view and current sort is size/type, reset to date
	if isJobs && (w.sortBy == "size" || w.sortBy == "type") {
		w.sortBy = "date"
		w.sortAscending = false // Newest first
	}
	w.mu.Unlock()
}

// GetPageSize returns the current page size setting
func (w *FileListWidget) GetPageSize() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.pageSize
}

// SetPageSize sets the page size (clamped to MinPageSize-MaxPageSize range)
// Use this for remote browsing where latency dominates - larger pages mean fewer round trips
func (w *FileListWidget) SetPageSize(size int) {
	if size < MinPageSize {
		size = MinPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	w.mu.Lock()
	w.pageSize = size
	w.mu.Unlock()
	// Update the page size entry if it exists
	if w.pageSizeEntry != nil {
		fyne.Do(func() {
			w.pageSizeEntry.SetText(fmt.Sprintf("%d", size))
		})
	}
}

// SetHasMoreServerData tells the widget if more data is available on the server
// When true, allows navigating to next page even if local items are exhausted
func (w *FileListWidget) SetHasMoreServerData(hasMore bool) {
	w.mu.Lock()
	w.hasMoreServerData = hasMore
	w.mu.Unlock()
	w.refreshPagination()
}

// SetPaginationVisible shows or hides the local pagination controls.
// When hidden, use external pagination controls (e.g., for server-side pagination).
func (w *FileListWidget) SetPaginationVisible(visible bool) {
	w.mu.Lock()
	w.paginationHidden = !visible
	// When hiding local pagination, set pageSize very high so all items show
	if !visible {
		w.pageSize = 10000 // Effectively unlimited
		w.currentPage = 0
	}
	w.mu.Unlock()

	if w.paginationBar != nil {
		fyne.Do(func() {
			if visible {
				w.paginationBar.Show()
			} else {
				w.paginationBar.Hide()
			}
		})
	}
}

// applyFilter filters items based on the search query
// PERFORMANCE: Uses cached lowerName field instead of calling strings.ToLower() per item
func (w *FileListWidget) applyFilter(query string) {
	// Direct call without debounce - just apply immediately
	w.applyFilterInternal(query)
}

// applyFilterWithGeneration applies a filter only if the generation matches
// Used by debounce mechanism to discard stale filter requests
func (w *FileListWidget) applyFilterWithGeneration(query string, gen uint64) {
	// Check if this filter request is still the latest
	currentGen := atomic.LoadUint64(&w.filterGeneration)
	if gen != currentGen {
		// Stale filter request - newer one is pending
		return
	}
	w.applyFilterInternal(query)
}

// applyFilterInternal is the internal filter implementation
func (w *FileListWidget) applyFilterInternal(query string) {
	w.mu.Lock()

	query = strings.ToLower(strings.TrimSpace(query))
	w.filterQuery = query
	w.currentPage = 0 // Reset to first page when filter changes

	if query == "" {
		// Clear filter
		w.filteredItems = nil
	} else {
		// Apply filter using cached lowercase names
		// OPTIMIZATION: Since w.items is already sorted, iterating in order
		// produces a filtered slice that's also sorted - no re-sort needed.
		w.filteredItems = make([]FileItem, 0, len(w.items)/4) // Preallocate ~25% capacity
		for _, item := range w.items {
			// PERFORMANCE: Use cached lowerName instead of strings.ToLower()
			if strings.Contains(item.lowerName, query) {
				w.filteredItems = append(w.filteredItems, item)
			}
		}
	}
	w.mu.Unlock()

	// Refresh list (must schedule on main thread)
	fyne.Do(func() {
		if w.list != nil {
			w.list.Refresh()
		}
	})
	w.updateStatus()
}

// getDisplayItemsLocked returns the items to display (filtered or all)
// Must be called with w.mu held (at least RLock)
func (w *FileListWidget) getDisplayItemsLocked() []FileItem {
	if w.filteredItems != nil {
		return w.filteredItems
	}
	return w.items
}

// ClearFilter clears the current filter
func (w *FileListWidget) ClearFilter() {
	if w.filterEntry != nil {
		w.filterEntry.SetText("")
	}
	w.applyFilter("")
}

// AppendItems adds items without resetting selection or scroll position.
// Intended for remote pagination / streaming where items are loaded incrementally.
// DOES NOT SORT - relies on server-side ordering.
// Temporarily clears filter during streaming (will be re-applied via FinalizeAppend).
// Deduplicates items by ID to prevent duplicate entries from race conditions.
func (w *FileListWidget) AppendItems(newItems []FileItem) {
	w.mu.Lock()

	// Deduplicate: filter out items already in the list
	// Prevents duplicate entries from navigation race conditions
	uniqueItems := make([]FileItem, 0, len(newItems))
	for _, item := range newItems {
		if _, exists := w.itemIndexByID[item.ID]; !exists {
			uniqueItems = append(uniqueItems, item)
		}
	}
	if len(uniqueItems) == 0 {
		w.mu.Unlock()
		return
	}

	start := len(w.items)
	w.items = append(w.items, uniqueItems...)

	// Incrementally extend index map + cache lowercase names
	for i := start; i < len(w.items); i++ {
		w.items[i].lowerName = strings.ToLower(w.items[i].Name)
		w.itemIndexByID[w.items[i].ID] = i
	}

	// Clear filtered items during streaming (filter becomes stale)
	// Caller should call FinalizeAppend after all pages to re-apply filter
	if w.filterQuery != "" {
		w.filteredItems = nil
	}
	w.mu.Unlock()

	fyne.Do(func() {
		if w.list != nil {
			w.list.Refresh()
		}
		w.refreshPaginationUI()
	})

	w.updateStatus()
}

// FinalizeAppend should be called after all pages are loaded.
// Re-applies filter if active (filter was cleared during streaming).
func (w *FileListWidget) FinalizeAppend() {
	w.mu.Lock()
	if w.filterQuery != "" {
		// Re-run filter on all items
		query := w.filterQuery
		w.filteredItems = make([]FileItem, 0, len(w.items)/4)
		for _, item := range w.items {
			if strings.Contains(item.lowerName, query) {
				w.filteredItems = append(w.filteredItems, item)
			}
		}
	}
	w.mu.Unlock()

	fyne.Do(func() {
		if w.list != nil {
			w.list.Refresh()
		}
	})
	w.updateStatus()
}

// Pagination methods

// previousPage navigates to the previous page
func (w *FileListWidget) previousPage() {
	w.mu.Lock()
	if w.currentPage > 0 {
		w.currentPage--
	}
	page := w.currentPage
	pageSize := w.pageSize
	totalItems := len(w.getDisplayItemsLocked())
	callback := w.OnPageChange
	w.mu.Unlock()
	w.refreshPagination()

	// Notify listener of page change
	if callback != nil {
		callback(page, pageSize, totalItems)
	}
}

// nextPage navigates to the next page
func (w *FileListWidget) nextPage() {
	w.mu.Lock()
	displayItems := w.getDisplayItemsLocked()
	totalPages := (len(displayItems) + w.pageSize - 1) / w.pageSize
	if totalPages == 0 {
		totalPages = 1
	}

	// Only allow navigation if we have local data for the next page
	// Don't allow speculative navigation based on hasMoreServerData
	if w.currentPage >= totalPages-1 {
		w.mu.Unlock()
		return
	}

	w.currentPage++
	page := w.currentPage
	pageSize := w.pageSize
	totalItems := len(displayItems)
	callback := w.OnPageChange
	w.mu.Unlock()

	w.refreshPagination()

	// Notify listener
	if callback != nil {
		callback(page, pageSize, totalItems)
	}
}

// onPageSizeChanged handles changes to the page size entry
func (w *FileListWidget) onPageSizeChanged(value string) {
	newSize := 0
	fmt.Sscanf(value, "%d", &newSize)

	// Clamp to valid range
	if newSize < MinPageSize {
		newSize = MinPageSize
	} else if newSize > MaxPageSize {
		newSize = MaxPageSize
	}

	w.mu.Lock()
	w.pageSize = newSize
	w.currentPage = 0 // Reset to first page
	// Clear hasMoreServerData until callback confirms there's actually more data
	// This prevents navigation to empty pages during the loading period
	w.hasMoreServerData = false
	page := w.currentPage
	pageSize := w.pageSize
	totalItems := len(w.getDisplayItemsLocked())
	callback := w.OnPageChange
	w.mu.Unlock()

	// Update the entry to show clamped value
	if w.pageSizeEntry != nil {
		fyne.Do(func() {
			w.pageSizeEntry.SetText(fmt.Sprintf("%d", newSize))
		})
	}

	w.refreshPagination()

	// Notify listener of page size change (may need more data)
	// The callback will call SetHasMoreServerData with the actual value
	if callback != nil {
		callback(page, pageSize, totalItems)
	}
}

// refreshPaginationUI updates just the pagination buttons and label
// MUST be called from the main thread (inside fyne.Do or from UI callback)
// Does NOT refresh the list - caller is responsible for that
func (w *FileListWidget) refreshPaginationUI() {
	w.mu.RLock()
	displayItems := w.getDisplayItemsLocked()
	totalItems := len(displayItems)
	totalPages := (totalItems + w.pageSize - 1) / w.pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	currentPage := w.currentPage
	hasMoreServer := w.hasMoreServerData
	w.mu.RUnlock()

	// Update page label - show "+" if more server data available
	if w.pageLabel != nil {
		if hasMoreServer {
			w.pageLabel.SetText(fmt.Sprintf("%d/%d+", currentPage+1, totalPages))
		} else {
			w.pageLabel.SetText(fmt.Sprintf("%d/%d", currentPage+1, totalPages))
		}
	}

	// Update button states
	if w.prevPageBtn != nil {
		if currentPage > 0 {
			w.prevPageBtn.Enable()
		} else {
			w.prevPageBtn.Disable()
		}
	}
	if w.nextPageBtn != nil {
		if currentPage < totalPages-1 {
			w.nextPageBtn.Enable()
		} else {
			w.nextPageBtn.Disable()
		}
	}
}

// refreshPagination updates the pagination UI and list without scrolling
func (w *FileListWidget) refreshPagination() {
	w.refreshPaginationInternal(false)
}

// refreshPaginationWithScroll updates the pagination UI and list, optionally scrolling to top
func (w *FileListWidget) refreshPaginationWithScroll(scrollToTop bool) {
	w.refreshPaginationInternal(scrollToTop)
}

// refreshPaginationInternal is the internal implementation of refreshPagination
func (w *FileListWidget) refreshPaginationInternal(scrollToTop bool) {
	w.mu.RLock()
	displayItems := w.getDisplayItemsLocked()
	totalItems := len(displayItems)
	totalPages := (totalItems + w.pageSize - 1) / w.pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	w.mu.RUnlock()

	// Update pagination UI on main thread
	fyne.Do(func() {
		w.refreshPaginationUI()

		// Refresh list
		if w.list != nil {
			w.list.Refresh()
			// Only scroll to top when explicitly requested (e.g., new folder navigation)
			// Avoid scrolling during incremental updates or pagination changes
			if scrollToTop {
				w.list.ScrollToTop()
			}
		}
	})

	// NOTE: Auto-preload callback removed per plan.
	// Remote browser explicitly controls when to prefetch.
	// This prevents request storms and duplicate loads.

	w.updateStatus()
}

// FormatFileSize formats a file size in bytes to human-readable format
func FormatFileSize(bytes int64) string {
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

// FormatTransferRate formats a transfer rate (bytes per second) to human-readable format
func FormatTransferRate(bytesPerSec float64) string {
	const unit = 1024
	if bytesPerSec < unit {
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	}
	div, exp := float64(unit), 0
	for n := bytesPerSec / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB/s", bytesPerSec/div, "KMGTPE"[exp])
}

// fixedWidthLayout is a custom layout that constrains width to a fixed value
type fixedWidthLayout struct {
	width float32
}

func (l *fixedWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) == 0 {
		return fyne.NewSize(l.width, 0)
	}
	minHeight := objects[0].MinSize().Height
	return fyne.NewSize(l.width, minHeight)
}

func (l *fixedWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, obj := range objects {
		obj.Resize(fyne.NewSize(l.width, size.Height))
		obj.Move(fyne.NewPos(0, 0))
	}
}

// getAbsolutePosition calculates the absolute position of a canvas object
// using Fyne's driver method
func getAbsolutePosition(obj fyne.CanvasObject) fyne.Position {
	driver := fyne.CurrentApp().Driver()
	return driver.AbsolutePositionForObject(obj)
}
