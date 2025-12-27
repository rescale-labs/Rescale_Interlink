// Package state provides observable state containers for Rescale Interlink.
// v3.6.4: FileListState provides observable file list management.
package state

import (
	"sort"
	"strings"
	"sync"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/services"
)

// FileListState is an observable file list container.
// It holds the current list of files/folders and publishes events on changes.
// Thread-safe for concurrent access.
type FileListState struct {
	// Source identifies this file list ("local" or "remote")
	source string

	// Event bus for publishing changes
	eventBus *events.EventBus

	// Current state
	items      []services.FileItem
	selected   map[string]bool
	sortBy     string // "name", "size", "date"
	ascending  bool
	folderID   string
	folderPath string
	loading    bool
	lastError  error

	mu sync.RWMutex
}

// NewFileListState creates a new FileListState.
func NewFileListState(source string, eventBus *events.EventBus) *FileListState {
	return &FileListState{
		source:    source,
		eventBus:  eventBus,
		items:     make([]services.FileItem, 0),
		selected:  make(map[string]bool),
		sortBy:    "name",
		ascending: true,
	}
}

// GetItems returns a copy of the current items.
func (s *FileListState) GetItems() []services.FileItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]services.FileItem, len(s.items))
	copy(result, s.items)
	return result
}

// SetItems updates the file list and publishes a change event.
func (s *FileListState) SetItems(items []services.FileItem) {
	s.mu.Lock()
	s.items = items
	s.sortItems() // Apply current sort
	s.loading = false
	s.lastError = nil
	folderID := s.folderID
	folderPath := s.folderPath
	itemsCopy := make([]services.FileItem, len(s.items))
	copy(itemsCopy, s.items)
	s.mu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(NewFileListChangedEvent(s.source, folderID, folderPath, itemsCopy))
	}
}

// SetLoading marks the list as loading and publishes an event.
func (s *FileListState) SetLoading(loading bool) {
	s.mu.Lock()
	s.loading = loading
	folderID := s.folderID
	s.mu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(NewFileListLoadingEvent(s.source, folderID, loading))
	}
}

// IsLoading returns whether the list is currently loading.
func (s *FileListState) IsLoading() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loading
}

// SetError sets the last error and publishes an error event.
func (s *FileListState) SetError(err error) {
	s.mu.Lock()
	s.lastError = err
	s.loading = false
	folderID := s.folderID
	s.mu.Unlock()

	if s.eventBus != nil && err != nil {
		s.eventBus.Publish(NewFileListErrorEvent(s.source, folderID, err))
	}
}

// GetError returns the last error.
func (s *FileListState) GetError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastError
}

// SetCurrentFolder updates the current folder and publishes an event.
func (s *FileListState) SetCurrentFolder(folderID, folderPath string) {
	s.mu.Lock()
	s.folderID = folderID
	s.folderPath = folderPath
	s.mu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(NewCurrentPathChangedEvent(s.source, folderID, folderPath))
	}
}

// GetCurrentFolder returns the current folder ID and path.
func (s *FileListState) GetCurrentFolder() (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.folderID, s.folderPath
}

// Select adds an item to the selection.
func (s *FileListState) Select(id string) {
	s.mu.Lock()
	s.selected[id] = true
	selectedIDs := s.getSelectedIDsLocked()
	s.mu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(NewSelectionChangedEvent(s.source, selectedIDs))
	}
}

// Deselect removes an item from the selection.
func (s *FileListState) Deselect(id string) {
	s.mu.Lock()
	delete(s.selected, id)
	selectedIDs := s.getSelectedIDsLocked()
	s.mu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(NewSelectionChangedEvent(s.source, selectedIDs))
	}
}

// ToggleSelect toggles an item's selection state.
func (s *FileListState) ToggleSelect(id string) {
	s.mu.Lock()
	if s.selected[id] {
		delete(s.selected, id)
	} else {
		s.selected[id] = true
	}
	selectedIDs := s.getSelectedIDsLocked()
	s.mu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(NewSelectionChangedEvent(s.source, selectedIDs))
	}
}

// SetSelection sets the selection to the given IDs.
func (s *FileListState) SetSelection(ids []string) {
	s.mu.Lock()
	s.selected = make(map[string]bool)
	for _, id := range ids {
		s.selected[id] = true
	}
	selectedIDs := s.getSelectedIDsLocked()
	s.mu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(NewSelectionChangedEvent(s.source, selectedIDs))
	}
}

// ClearSelection clears all selections.
func (s *FileListState) ClearSelection() {
	s.mu.Lock()
	s.selected = make(map[string]bool)
	s.mu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(NewSelectionChangedEvent(s.source, []string{}))
	}
}

// IsSelected returns whether an item is selected.
func (s *FileListState) IsSelected(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.selected[id]
}

// GetSelectedIDs returns the IDs of selected items.
func (s *FileListState) GetSelectedIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getSelectedIDsLocked()
}

// getSelectedIDsLocked returns selected IDs (must hold lock).
func (s *FileListState) getSelectedIDsLocked() []string {
	ids := make([]string, 0, len(s.selected))
	for id := range s.selected {
		ids = append(ids, id)
	}
	return ids
}

// GetSelectedItems returns the selected items.
func (s *FileListState) GetSelectedItems() []services.FileItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]services.FileItem, 0, len(s.selected))
	for _, item := range s.items {
		if s.selected[item.ID] {
			result = append(result, item)
		}
	}
	return result
}

// GetSelectedCount returns the number of selected items.
func (s *FileListState) GetSelectedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.selected)
}

// SetSort updates the sort order and re-sorts the list.
func (s *FileListState) SetSort(sortBy string, ascending bool) {
	s.mu.Lock()
	s.sortBy = sortBy
	s.ascending = ascending
	s.sortItems()
	itemsCopy := make([]services.FileItem, len(s.items))
	copy(itemsCopy, s.items)
	folderID := s.folderID
	folderPath := s.folderPath
	s.mu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(NewSortChangedEvent(s.source, sortBy, ascending))
		s.eventBus.Publish(NewFileListChangedEvent(s.source, folderID, folderPath, itemsCopy))
	}
}

// GetSort returns the current sort settings.
func (s *FileListState) GetSort() (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sortBy, s.ascending
}

// sortItems sorts the items by current sort settings (must hold lock).
func (s *FileListState) sortItems() {
	if len(s.items) == 0 {
		return
	}

	sort.SliceStable(s.items, func(i, j int) bool {
		a, b := s.items[i], s.items[j]

		// Folders always come first
		if a.IsFolder != b.IsFolder {
			return a.IsFolder
		}

		var less bool
		switch s.sortBy {
		case "size":
			less = a.Size < b.Size
		case "date":
			less = a.ModTime.Before(b.ModTime)
		default: // "name"
			less = strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}

		if s.ascending {
			return less
		}
		return !less
	})
}

// Clear clears all items and selection.
func (s *FileListState) Clear() {
	s.mu.Lock()
	s.items = make([]services.FileItem, 0)
	s.selected = make(map[string]bool)
	s.lastError = nil
	folderID := s.folderID
	folderPath := s.folderPath
	s.mu.Unlock()

	if s.eventBus != nil {
		s.eventBus.Publish(NewFileListChangedEvent(s.source, folderID, folderPath, []services.FileItem{}))
		s.eventBus.Publish(NewSelectionChangedEvent(s.source, []string{}))
	}
}

// FindByID finds an item by ID.
func (s *FileListState) FindByID(id string) (services.FileItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, item := range s.items {
		if item.ID == id {
			return item, true
		}
	}
	return services.FileItem{}, false
}

// Count returns the number of items.
func (s *FileListState) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}
