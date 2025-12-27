// Package gui provides the graphical user interface for rescale-int.
// File list widget - reusable component for displaying files and folders.
package gui

import (
	"fmt"
	"image/color"
	"os"
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

	// Pagination defaults (for remote browser - matches API page size)
	DefaultPageSize = 25  // Default items per page (matches API page size)
	MinPageSize     = 10  // Minimum items per page
	MaxPageSize     = 200 // Maximum items per page

	// Local browser pagination (v3.6.3: separate settings for local files)
	LocalDefaultPageSize = 200  // Default items per page for local files
	LocalMaxPageSize     = 1000 // Maximum items per page for local files
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

// fileRowTemplate holds typed references to all row components (v3.5.0)
// This eliminates brittle row.Objects[...] indexing and provides compile-time safety.
// Stored in FileListWidget.rowTemplates sync.Map, keyed by row container pointer.
type fileRowTemplate struct {
	check *widget.Check
	icon  *widget.Icon
	name  *widget.RichText
	size  *canvas.Text
	typ   *canvas.Text
	date  *canvas.Text
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
	pageSize          int  // Items per page (default 25 for remote, 200 for local)
	maxPageSize       int  // Maximum page size (200 for remote, 1000 for local)
	currentPage       int  // Current page (0-indexed)
	hasMoreServerData bool // True if more data available on server (for lazy loading)

	// Callbacks
	OnFolderOpen       func(item FileItem)                  // Called when folder is double-clicked/entered
	OnSelectionChanged func(selected []FileItem)            // Called when selection changes
	OnPageChange       func(page, pageSize, totalItems int) // Called when page changes (for lazy loading)
	OnSortChanged      func(sortBy string, ascending bool)  // v3.6.3: Called when sort changes (for persistence)

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

	// Self-healing view state (v3.4.12)
	// When viewDirty is true, ensureViewLocked() will recompute sort, index, and filter
	// before any UI read. This provides self-healing: missing invalidation = perf issue, not bug.
	viewDirty bool

	// View generation counter (v3.5.0)
	// Incremented on ANY change that can invalidate in-flight row updates:
	// SetItems, SetItemsAndScrollToTop, AppendItems, filter/sort/page changes.
	// Used to detect and blank stale updateListItem calls from recycled renderers.
	viewGeneration uint64

	// rowTemplates stores typed references to row components for safe access
	// Key is the row container pointer, value is the template with component references
	rowTemplates sync.Map // map[*fyne.Container]*fileRowTemplate
}

// debugLog logs debug messages when RESCALE_GUI_DEBUG is set.
// Use for diagnosing view state issues.
func (w *FileListWidget) debugLog(format string, args ...interface{}) {
	if os.Getenv("RESCALE_GUI_DEBUG") != "" {
		fmt.Printf("[FileList] "+format+"\n", args...)
	}
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
		maxPageSize:   MaxPageSize, // Default for remote browser
		currentPage:   0,
	}
	w.ExtendBaseWidget(w)
	return w
}

// ConfigureForLocalBrowser configures the widget for local file browsing.
// v3.6.3: Uses larger page sizes for local files (200 default, 1000 max).
func (w *FileListWidget) ConfigureForLocalBrowser() {
	w.mu.Lock()
	w.pageSize = LocalDefaultPageSize
	w.maxPageSize = LocalMaxPageSize
	w.mu.Unlock()
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
			w.mu.Lock()
			defer w.mu.Unlock()
			displayItems := w.getDisplayItemsLocked() // May trigger ensureViewLocked
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
			sizeText := createSizedText("999.9 MB", fyne.TextAlignTrailing)
			typeText := createSizedText("Folder", fyne.TextAlignCenter)
			dateText := createSizedText("2025-01-01", fyne.TextAlignCenter)

			// Add padding around size, type, and date for better spacing
			sizeContainer := container.NewPadded(container.NewStack(sizeText))
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

			// Store typed template for safe access in updateListItem (v3.5.0)
			w.rowTemplates.Store(row, &fileRowTemplate{
				check: check,
				icon:  icon,
				name:  name,
				size:  sizeText,
				typ:   typeText,
				date:  dateText,
			})

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

// updateListItem updates a single list item with data (v3.5.0 rewrite)
// This is called from the main thread during list rendering.
// Key fixes:
//   - Uses typed fileRowTemplate for safe field access (eliminates brittle indexing)
//   - Captures viewGeneration BEFORE getting items to detect stale renders
//   - Total overwrite: sets EVERY field in ALL branches
//   - Calls Refresh() on every canvas.Text and widget.RichText after mutation
//   - Falls back to blankListItem on out-of-bounds or generation mismatch
func (w *FileListWidget) updateListItem(index int, obj fyne.CanvasObject) {
	row := obj.(*fyne.Container)

	// Get typed template for safe field access (v3.5.0)
	tplVal, ok := w.rowTemplates.Load(row)
	if !ok {
		// Template not found - should never happen, but blank defensively
		w.debugLog("updateListItem: template not found for row, blanking")
		return
	}
	tpl := tplVal.(*fileRowTemplate)

	// Capture generation BEFORE getting items (for stale detection)
	w.mu.Lock()
	gen := w.viewGeneration
	displayItems := w.getDisplayItemsLocked() // May trigger ensureViewLocked
	actualIndex := w.currentPage*w.pageSize + index

	// Bounds check - blank if out of range (NO EARLY RETURN without blanking)
	if actualIndex >= len(displayItems) {
		w.mu.Unlock()
		w.blankListItem(tpl)
		return
	}

	item := displayItems[actualIndex]
	w.mu.Unlock()

	// GENERATION CHECK after unlock - detect stale renders from recycled rows
	w.mu.RLock()
	currentGen := w.viewGeneration
	w.mu.RUnlock()

	if gen != currentGen {
		w.debugLog("updateListItem: generation mismatch (got=%d, current=%d), blanking", gen, currentGen)
		w.blankListItem(tpl)
		return
	}

	// === TOTAL OVERWRITE: Set EVERY field, refresh EVERY mutable object ===

	// Capture item ID for callback closure (immutable, safe from index changes)
	itemID := item.ID

	// 1. Checkbox - disable callback before setting, then re-enable
	tpl.check.OnChanged = nil
	tpl.check.Enable() // Re-enable in case it was disabled by blankListItem
	tpl.check.SetChecked(item.Selected)
	tpl.check.OnChanged = func(checked bool) {
		w.setItemSelectedByID(itemID, checked)
	}

	// 2. Icon - ALWAYS set (folder or file)
	if item.IsFolder {
		tpl.icon.SetResource(theme.FolderIcon())
	} else {
		tpl.icon.SetResource(theme.FileIcon())
	}

	// 3. Name - set AND refresh RichText (CRITICAL: Refresh required after Segments change)
	tpl.name.Segments = []widget.RichTextSegment{
		&widget.TextSegment{
			Text: item.Name,
			Style: widget.RichTextStyle{
				TextStyle: fyne.TextStyle{},
				SizeName:  theme.SizeNameCaptionText,
			},
		},
	}
	tpl.name.Refresh() // CRITICAL: Must refresh after Segments change

	// 4. Size - ALWAYS set + refresh canvas.Text
	if item.IsFolder {
		tpl.size.Text = "--"
	} else if item.Size < 0 {
		tpl.size.Text = "?"
	} else {
		tpl.size.Text = FormatFileSize(item.Size)
	}
	tpl.size.Color = theme.ForegroundColor()
	tpl.size.Refresh() // CRITICAL

	// 5. Type - ALWAYS set + refresh canvas.Text
	if item.IsFolder {
		tpl.typ.Text = "Folder"
	} else {
		tpl.typ.Text = "File"
	}
	tpl.typ.Color = theme.ForegroundColor()
	tpl.typ.Refresh() // CRITICAL

	// 6. Date - ALWAYS set + refresh canvas.Text
	if item.ModTime.IsZero() {
		tpl.date.Text = "--"
	} else {
		tpl.date.Text = item.ModTime.Format("2006-01-02")
	}
	tpl.date.Color = theme.ForegroundColor()
	tpl.date.Refresh() // CRITICAL
}

// blankListItem clears a recycled list item AND refreshes all mutated objects (v3.5.0)
// Called when index is out of bounds or generation mismatch detected.
// Uses typed template for safe field access.
func (w *FileListWidget) blankListItem(tpl *fileRowTemplate) {
	// Blank AND disable interaction to prevent stale clicks
	tpl.check.OnChanged = nil
	tpl.check.SetChecked(false)
	tpl.check.Disable()

	tpl.icon.SetResource(theme.FileIcon())

	tpl.name.Segments = []widget.RichTextSegment{
		&widget.TextSegment{Text: "", Style: widget.RichTextStyle{}},
	}
	tpl.name.Refresh() // CRITICAL

	tpl.size.Text = ""
	tpl.size.Refresh() // CRITICAL

	tpl.typ.Text = ""
	tpl.typ.Refresh() // CRITICAL

	tpl.date.Text = ""
	tpl.date.Refresh() // CRITICAL
}

// onItemTapped handles when an item is tapped (v3.5.0 enhanced with safety guards)
// Prevents stale clicks from causing wrong actions (e.g., opening wrong folder).
func (w *FileListWidget) onItemTapped(index int) {
	w.mu.Lock()
	displayItems := w.getDisplayItemsLocked() // May trigger ensureViewLocked
	// Map page-relative index to actual index
	actualIndex := w.currentPage*w.pageSize + index

	// Bounds check
	if actualIndex >= len(displayItems) {
		w.mu.Unlock()
		w.debugLog("onItemTapped: index %d out of range (len=%d), ignoring", actualIndex, len(displayItems))
		return
	}

	item := displayItems[actualIndex]

	// SAFETY GUARD (v3.5.0): Verify item ID exists in index
	// This catches stale clicks where the visual row shows old data but the data model has changed
	if _, exists := w.itemIndexByID[item.ID]; !exists {
		w.mu.Unlock()
		w.debugLog("onItemTapped: item ID %s not in index, ignoring stale click", item.ID)
		return
	}

	w.mu.Unlock()

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
// Items are automatically sorted according to current sort settings via viewDirty
func (w *FileListWidget) SetItems(items []FileItem) {
	w.mu.Lock()
	oldCount := len(w.items)

	// INCREMENT GENERATION FIRST (v3.5.0) - invalidates in-flight row updates
	w.viewGeneration++

	// Full derived-state reset (v3.4.12 self-healing architecture)
	w.items = items
	w.filteredItems = nil                        // Clear stale filtered cache
	w.selectedItems = make(map[string]bool)      // Clear selections
	w.itemIndexByID = make(map[string]int)       // Will be rebuilt in ensureViewLocked
	w.viewDirty = true                           // Mark for recompute

	// Only reset to first page if items decreased (new folder)
	// Preserve page if items increased (loading more data)
	if len(items) < oldCount {
		w.currentPage = 0
	}
	w.mu.Unlock()

	// Refresh UI on UI thread - ensureViewLocked will recompute sort/filter
	fyne.Do(func() {
		if w.list != nil {
			w.list.Refresh()
		}
	})

	w.refreshPagination() // Update pagination buttons and display
	w.updateStatus()
	w.updateSelectAllState()
}

// SetItemsAndScrollToTop sets the items and resets scroll to top (v3.5.0 enhanced)
// Use this when navigating to a new directory/folder.
// Key fixes:
//   - Increments viewGeneration FIRST to invalidate in-flight row updates
//   - Calls UnselectAll() to clear selection highlight persistence
//   - Uses double-refresh pattern to handle scroll/length edge cases
//   - Clears filter entry text on navigation
func (w *FileListWidget) SetItemsAndScrollToTop(items []FileItem) {
	w.mu.Lock()

	firstName := ""
	if len(items) > 0 {
		firstName = items[0].Name
	}
	w.debugLog("SetItemsAndScrollToTop: nav to new folder (items=%d, first=%q)", len(items), firstName)

	// INCREMENT GENERATION FIRST - invalidates any in-flight row updates (v3.5.0)
	w.viewGeneration++

	// Full derived-state reset
	w.items = items
	w.filteredItems = nil                   // Clear stale filtered cache
	w.filterQuery = ""                      // Clear filter on navigation (v3.5.0)
	w.selectedItems = make(map[string]bool) // Clear selections
	w.itemIndexByID = make(map[string]int)  // Will be rebuilt in ensureViewLocked
	w.viewDirty = true                      // Mark for recompute (sort + filter)
	w.currentPage = 0                       // Reset to first page

	// Calculate folder/file counts for status display
	folderCount, fileCount := 0, 0
	for i := range w.items {
		if w.items[i].IsFolder {
			folderCount++
		} else {
			fileCount++
		}
	}

	w.mu.Unlock()

	// Batch all UI updates into a single fyne.Do call
	fyne.Do(func() {
		// Clear filter entry text (v3.5.0)
		if w.filterEntry != nil && w.filterEntry.Text != "" {
			w.filterEntry.SetText("")
		}

		if w.list != nil {
			// === DETERMINISTIC RESET SEQUENCE (v3.5.0) ===
			// 1. Clear selection state (fixes selection persistence bug)
			w.list.UnselectAll()

			// 2. Scroll to top
			w.list.ScrollToTop()

			// 3. First refresh (updates Length, triggers UpdateItem for visible rows)
			w.list.Refresh()
		}

		// Update status with pre-calculated counts
		if w.statusLabel != nil {
			w.statusLabel.SetText(fmt.Sprintf("%d folders, %d files", folderCount, fileCount))
		}

		// Update pagination
		w.refreshPaginationUI()

		// 4. Schedule second refresh "next tick" to handle scroll/length edge cases (v3.5.0)
		fyne.Do(func() {
			if w.list != nil {
				w.list.Refresh()
			}
		})
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
// Uses viewDirty for self-healing sort recompute (v3.4.12).
// v3.6.3: Calls OnSortChanged callback for persistence.
func (w *FileListWidget) setSortMode(sortBy string, ascending bool) {
	w.mu.Lock()
	if w.sortBy == sortBy && w.sortAscending == ascending {
		w.mu.Unlock()
		return // No change
	}
	w.viewGeneration++ // (v3.5.0) Invalidate in-flight row updates
	w.sortBy = sortBy
	w.sortAscending = ascending
	w.viewDirty = true // Mark for recompute
	callback := w.OnSortChanged // Capture under lock
	w.mu.Unlock()

	// v3.6.3: Notify callback for persistence
	if callback != nil {
		callback(sortBy, ascending)
	}

	// Refresh UI - ensureViewLocked will recompute sort/filter when list reads
	fyne.Do(func() {
		if w.list != nil {
			w.list.Refresh()
		}
	})
}

// SetSort sets the initial sort mode without triggering the OnSortChanged callback.
// v3.6.3: Used for loading sort preferences from config.
func (w *FileListWidget) SetSort(sortBy string, ascending bool) {
	w.mu.Lock()
	w.sortBy = sortBy
	w.sortAscending = ascending
	w.viewDirty = true // Mark for recompute on next refresh
	w.mu.Unlock()
}

// GetSort returns the current sort settings.
// v3.6.3: Used for saving sort preferences to config.
func (w *FileListWidget) GetSort() (sortBy string, ascending bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sortBy, w.sortAscending
}

// sortAndRefresh marks view as dirty and refreshes the list.
// Actual sort/filter recompute happens lazily in ensureViewLocked() when UI reads.
// This design is simpler and self-healing - no need to duplicate sort logic here.
func (w *FileListWidget) sortAndRefresh() {
	w.mu.Lock()
	w.viewDirty = true // Mark as needing recompute
	w.mu.Unlock()

	// Refresh UI on UI thread - ensureViewLocked will recompute when list reads
	fyne.Do(func() {
		if w.list != nil {
			w.list.Refresh()
		}
	})
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

// ensureViewLocked recomputes derived state (sort, index, filtered) if needed.
// MUST be called with w.mu held.
// This is the SINGLE POINT where display correctness is guaranteed.
// Call this before any UI read (getDisplayItemsLocked, Length, UpdateItem).
//
// Self-healing design: even if we miss a viewDirty=true call somewhere,
// the next UI read will trigger recompute. Missing invalidation = perf issue, not bug.
func (w *FileListWidget) ensureViewLocked() {
	if !w.viewDirty {
		return
	}

	w.debugLog("ensureViewLocked: recomputing view (items=%d, filterQuery=%q)", len(w.items), w.filterQuery)

	// 1. Ensure lowerName is cached for all items (needed for sorting/filtering)
	for i := range w.items {
		if w.items[i].lowerName == "" {
			w.items[i].lowerName = strings.ToLower(w.items[i].Name)
		}
	}

	// 2. Sort items according to current sort mode
	w.sortItemsLocked(w.items)

	// 3. Rebuild index map from scratch (removes stale entries from previous folder)
	w.itemIndexByID = make(map[string]int, len(w.items))
	for i := range w.items {
		w.itemIndexByID[w.items[i].ID] = i
	}

	// 4. Recompute filteredItems based on filterQuery
	w.recomputeFilteredItemsLocked()

	w.viewDirty = false
}

// recomputeFilteredItemsLocked updates filteredItems based on filterQuery.
// MUST be called with w.mu held. Assumes w.items is already sorted.
// Single place for filter logic - no duplication across methods.
func (w *FileListWidget) recomputeFilteredItemsLocked() {
	if w.filterQuery == "" {
		w.filteredItems = nil
		return
	}

	// Pre-allocate with reasonable capacity (~25% of items match)
	w.filteredItems = make([]FileItem, 0, len(w.items)/4)
	for _, item := range w.items {
		if strings.Contains(item.lowerName, w.filterQuery) {
			w.filteredItems = append(w.filteredItems, item)
		}
	}
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

// SetPageSize sets the page size (clamped to MinPageSize-maxPageSize range)
// Use this for remote browsing where latency dominates - larger pages mean fewer round trips
// v3.6.3: Uses instance's maxPageSize (200 for remote, 1000 for local)
func (w *FileListWidget) SetPageSize(size int) {
	if size < MinPageSize {
		size = MinPageSize
	}
	w.mu.RLock()
	maxSize := w.maxPageSize
	if maxSize == 0 {
		maxSize = MaxPageSize // Fallback for uninitialized
	}
	w.mu.RUnlock()
	if size > maxSize {
		size = maxSize
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
// Sets viewDirty for self-healing filter recompute (v3.4.12).
func (w *FileListWidget) applyFilterInternal(query string) {
	w.mu.Lock()

	query = strings.TrimSpace(strings.ToLower(query))
	if query == w.filterQuery {
		w.mu.Unlock()
		return // No change
	}

	w.viewGeneration++ // (v3.5.0) Invalidate in-flight row updates
	w.filterQuery = query
	w.viewDirty = true  // Recompute will happen in ensureViewLocked
	w.currentPage = 0   // Reset to first page when filter changes

	w.mu.Unlock()

	// Refresh list (must schedule on main thread)
	// ensureViewLocked will recompute filteredItems when list reads
	fyne.Do(func() {
		if w.list != nil {
			w.list.Refresh()
		}
	})
	w.updateStatus()
}

// getDisplayItemsLocked returns the items to display (filtered or all)
// Must be called with w.mu held (write lock required for ensureViewLocked).
//
// KEY FIX (v3.4.12): Uses filterQuery as source of truth, NOT filteredItems != nil.
// Even if filteredItems is stale/non-nil, if filterQuery is empty, we return w.items.
// This prevents "showing old filtered items after filter cleared" bugs.
func (w *FileListWidget) getDisplayItemsLocked() []FileItem {
	w.ensureViewLocked() // Self-healing: recompute sort/filter if stale

	// Switch on filterQuery, NOT filteredItems != nil
	if w.filterQuery != "" {
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
// Sets viewDirty for self-healing sort/filter recompute (v3.4.12).
// Deduplicates items by ID to prevent duplicate entries from race conditions.
// Note: Don't refresh here - wait for FinalizeAppend after all pages loaded.
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

	// INCREMENT GENERATION (v3.5.0) - invalidates in-flight row updates
	w.viewGeneration++

	// Add new items and mark view as dirty
	w.items = append(w.items, uniqueItems...)
	w.viewDirty = true // Will re-sort and re-filter on next UI read

	// Incrementally extend index map for deduplication checks
	// (Note: may be rebuilt in ensureViewLocked, but needed for dedup above)
	start := len(w.items) - len(uniqueItems)
	for i := start; i < len(w.items); i++ {
		w.itemIndexByID[w.items[i].ID] = i
	}

	w.mu.Unlock()

	// Note: Don't refresh here - wait for FinalizeAppend
	// This avoids multiple expensive refreshes during streaming
}

// FinalizeAppend should be called after all pages are loaded.
// Triggers refresh which will recompute sort/filter via ensureViewLocked (v3.4.12).
func (w *FileListWidget) FinalizeAppend() {
	w.mu.Lock()
	w.viewGeneration++ // (v3.5.0) Invalidate in-flight row updates
	w.viewDirty = true // Ensure recompute happens
	w.mu.Unlock()

	fyne.Do(func() {
		if w.list != nil {
			w.list.Refresh()
		}
		w.refreshPaginationUI()
	})
	w.updateStatus()
}

// Pagination methods

// previousPage navigates to the previous page
func (w *FileListWidget) previousPage() {
	w.mu.Lock()
	if w.currentPage > 0 {
		w.viewGeneration++ // (v3.5.0) Invalidate in-flight row updates
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

	w.viewGeneration++ // (v3.5.0) Invalidate in-flight row updates
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
// v3.6.3: Uses instance's maxPageSize (200 for remote, 1000 for local)
func (w *FileListWidget) onPageSizeChanged(value string) {
	newSize := 0
	fmt.Sscanf(value, "%d", &newSize)

	// Clamp to valid range using instance's maxPageSize
	w.mu.Lock()
	maxSize := w.maxPageSize
	if maxSize == 0 {
		maxSize = MaxPageSize // Fallback for uninitialized
	}
	if newSize < MinPageSize {
		newSize = MinPageSize
	} else if newSize > maxSize {
		newSize = maxSize
	}

	w.viewGeneration++ // (v3.5.0) Invalidate in-flight row updates
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
	w.mu.Lock()
	displayItems := w.getDisplayItemsLocked() // May trigger ensureViewLocked
	totalItems := len(displayItems)
	totalPages := (totalItems + w.pageSize - 1) / w.pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	currentPage := w.currentPage
	hasMoreServer := w.hasMoreServerData
	w.mu.Unlock()

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
	w.mu.Lock()
	displayItems := w.getDisplayItemsLocked() // May trigger ensureViewLocked
	totalItems := len(displayItems)
	totalPages := (totalItems + w.pageSize - 1) / w.pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	w.mu.Unlock()

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

// FormatTransferRate formats a transfer rate (bytes per second) to human-readable format.
// Uses 2 decimal places for consistent, precise display.
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
	return fmt.Sprintf("%.2f %cB/s", bytesPerSec/div, "KMGTPE"[exp])
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
