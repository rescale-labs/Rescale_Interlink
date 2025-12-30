package gui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// SearchableSelect is a custom widget that combines an Entry with a filtered dropdown list.
// It's designed for large option sets (1000+ items) where a standard Select is impractical.
type SearchableSelect struct {
	widget.BaseWidget

	entry            *widget.Entry
	list             *widget.List
	listScroll       *AcceleratedScroll
	options          []string
	filtered         []string
	selected         string
	OnChanged        func(string) // Callback when selection changes
	maxVisible       int
	listVisible      bool
	selectedIdx      int
	settingSelection bool // Flag to prevent list display during programmatic SetSelected
}

// NewSearchableSelect creates a new searchable select widget
func NewSearchableSelect(placeholder string, onChanged func(string)) *SearchableSelect {
	ss := &SearchableSelect{
		options:     []string{},
		filtered:    []string{},
		OnChanged:   onChanged,
		maxVisible:  10,
		selectedIdx: -1,
	}

	ss.entry = widget.NewEntry()
	ss.entry.SetPlaceHolder(placeholder)
	ss.entry.OnChanged = ss.onEntryChanged
	ss.entry.OnSubmitted = ss.onEntrySubmitted

	ss.list = widget.NewList(
		func() int { return len(ss.filtered) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			label := obj.(*widget.Label)
			if id < len(ss.filtered) {
				label.SetText(ss.filtered[id])
			}
		},
	)
	ss.list.OnSelected = ss.onListSelected

	// Wrap list in scroll container - don't set MinSize to avoid reserving space when hidden
	ss.listScroll = NewAcceleratedVScroll(ss.list)
	ss.listScroll.Hide()

	ss.ExtendBaseWidget(ss)
	return ss
}

// SetOptions sets the available options
func (ss *SearchableSelect) SetOptions(options []string) {
	ss.options = options
	ss.filterOptions("")
}

// SetSelected sets the current selection programmatically (does not show dropdown)
func (ss *SearchableSelect) SetSelected(value string) {
	ss.settingSelection = true
	ss.selected = value
	ss.entry.SetText(value)
	ss.listScroll.Hide()
	ss.listVisible = false
	ss.settingSelection = false
}

// Selected returns the current selection
func (ss *SearchableSelect) Selected() string {
	return ss.selected
}

// onEntryChanged handles text changes in the entry
func (ss *SearchableSelect) onEntryChanged(text string) {
	// Skip list display when programmatically setting selection
	if ss.settingSelection {
		return
	}

	ss.filterOptions(text)

	// Show list if we have filtered results and user is typing
	if len(ss.filtered) > 0 && text != "" {
		ss.listScroll.Show()
		ss.listVisible = true
	} else if text == "" {
		ss.listScroll.Hide()
		ss.listVisible = false
	}

	ss.selectedIdx = -1
	ss.Refresh()
}

// onEntrySubmitted handles Enter key in the entry
func (ss *SearchableSelect) onEntrySubmitted(text string) {
	// If there's exactly one filtered result, select it
	if len(ss.filtered) == 1 {
		ss.selectItem(ss.filtered[0])
	} else if ss.selectedIdx >= 0 && ss.selectedIdx < len(ss.filtered) {
		ss.selectItem(ss.filtered[ss.selectedIdx])
	} else if text != "" {
		// Allow manual entry if it matches an option
		for _, opt := range ss.options {
			if strings.EqualFold(opt, text) {
				ss.selectItem(opt)
				return
			}
		}
		// No match found, just use what they typed
		ss.selectItem(text)
	}
}

// onListSelected handles selection from the list
func (ss *SearchableSelect) onListSelected(id widget.ListItemID) {
	if id < len(ss.filtered) {
		ss.selectItem(ss.filtered[id])
	}
}

// selectItem finalizes a selection
func (ss *SearchableSelect) selectItem(value string) {
	ss.selected = value
	ss.entry.SetText(value)
	ss.listScroll.Hide()
	ss.listVisible = false

	if ss.OnChanged != nil {
		ss.OnChanged(value)
	}
	ss.Refresh()
}

// filterOptions filters the options based on search text
func (ss *SearchableSelect) filterOptions(search string) {
	if search == "" {
		// Show all options (limited to first N for performance)
		if len(ss.options) > 100 {
			ss.filtered = ss.options[:100]
		} else {
			ss.filtered = ss.options
		}
		return
	}

	search = strings.ToLower(search)
	ss.filtered = nil

	// First pass: exact prefix matches
	for _, opt := range ss.options {
		if strings.HasPrefix(strings.ToLower(opt), search) {
			ss.filtered = append(ss.filtered, opt)
			if len(ss.filtered) >= 50 {
				break
			}
		}
	}

	// Second pass: contains matches (if we need more results)
	if len(ss.filtered) < 50 {
		for _, opt := range ss.options {
			if strings.Contains(strings.ToLower(opt), search) && !strings.HasPrefix(strings.ToLower(opt), search) {
				ss.filtered = append(ss.filtered, opt)
				if len(ss.filtered) >= 50 {
					break
				}
			}
		}
	}

	ss.list.Refresh()
}

// CreateRenderer implements fyne.Widget
func (ss *SearchableSelect) CreateRenderer() fyne.WidgetRenderer {
	return &searchableSelectRenderer{
		ss: ss,
	}
}

// searchableSelectRenderer is the renderer for SearchableSelect
type searchableSelectRenderer struct {
	ss *searchableSelect
}

type searchableSelect = SearchableSelect

func (r *searchableSelectRenderer) Layout(size fyne.Size) {
	// Entry takes full width, fixed height
	entryHeight := r.ss.entry.MinSize().Height
	r.ss.entry.Resize(fyne.NewSize(size.Width, entryHeight))
	r.ss.entry.Move(fyne.NewPos(0, 0))

	// List goes below entry when visible
	if r.ss.listVisible {
		listHeight := min(float32(200), size.Height-entryHeight)
		if listHeight > 0 {
			r.ss.listScroll.Resize(fyne.NewSize(size.Width, listHeight))
			r.ss.listScroll.Move(fyne.NewPos(0, entryHeight))
		}
	} else {
		// When hidden, give it zero size
		r.ss.listScroll.Resize(fyne.NewSize(0, 0))
	}
}

func (r *searchableSelectRenderer) MinSize() fyne.Size {
	entryMin := r.ss.entry.MinSize()
	if r.ss.listVisible {
		return fyne.NewSize(entryMin.Width, entryMin.Height+200)
	}
	return entryMin
}

func (r *searchableSelectRenderer) Refresh() {
	r.ss.entry.Refresh()
	r.ss.list.Refresh()
	r.ss.listScroll.Refresh()
}

func (r *searchableSelectRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.ss.entry, r.ss.listScroll}
}

func (r *searchableSelectRenderer) Destroy() {}
