// Package state provides observable state containers for Rescale Interlink.
// These containers emit events when state changes, allowing any frontend
// to subscribe and update its UI accordingly.
//
// v3.6.4: Created as part of Fyne -> Wails migration preparation.
package state

import (
	"time"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/services"
)

// State event types
const (
	// File list events
	EventFileListChanged    events.EventType = "file_list_changed"
	EventFileListLoading    events.EventType = "file_list_loading"
	EventFileListError      events.EventType = "file_list_error"
	EventSelectionChanged   events.EventType = "selection_changed"
	EventSortChanged        events.EventType = "sort_changed"
	EventCurrentPathChanged events.EventType = "current_path_changed"

	// Transfer state events are re-exported from services
	// (EventTransferQueued, EventTransferProgress, etc. are in events package)
)

// FileListChangedEvent is published when the file list changes.
type FileListChangedEvent struct {
	events.BaseEvent
	Items      []services.FileItem
	FolderID   string
	FolderPath string
	Source     string // "local" or "remote"
}

// FileListLoadingEvent is published when a file list is being loaded.
type FileListLoadingEvent struct {
	events.BaseEvent
	FolderID string
	Source   string // "local" or "remote"
	Loading  bool
}

// FileListErrorEvent is published when a file list load fails.
type FileListErrorEvent struct {
	events.BaseEvent
	FolderID string
	Source   string
	Error    error
}

// SelectionChangedEvent is published when the selection changes.
type SelectionChangedEvent struct {
	events.BaseEvent
	SelectedIDs []string
	Source      string // "local" or "remote"
}

// SortChangedEvent is published when the sort order changes.
type SortChangedEvent struct {
	events.BaseEvent
	SortBy    string // "name", "size", "date"
	Ascending bool
	Source    string // "local" or "remote"
}

// CurrentPathChangedEvent is published when the current path/folder changes.
type CurrentPathChangedEvent struct {
	events.BaseEvent
	FolderID   string
	FolderPath string
	Source     string // "local" or "remote"
}

// NewFileListChangedEvent creates a new FileListChangedEvent.
func NewFileListChangedEvent(source, folderID, folderPath string, items []services.FileItem) *FileListChangedEvent {
	return &FileListChangedEvent{
		BaseEvent: events.BaseEvent{
			EventType: EventFileListChanged,
			Time:      time.Now(),
		},
		Items:      items,
		FolderID:   folderID,
		FolderPath: folderPath,
		Source:     source,
	}
}

// NewFileListLoadingEvent creates a new FileListLoadingEvent.
func NewFileListLoadingEvent(source, folderID string, loading bool) *FileListLoadingEvent {
	return &FileListLoadingEvent{
		BaseEvent: events.BaseEvent{
			EventType: EventFileListLoading,
			Time:      time.Now(),
		},
		FolderID: folderID,
		Source:   source,
		Loading:  loading,
	}
}

// NewFileListErrorEvent creates a new FileListErrorEvent.
func NewFileListErrorEvent(source, folderID string, err error) *FileListErrorEvent {
	return &FileListErrorEvent{
		BaseEvent: events.BaseEvent{
			EventType: EventFileListError,
			Time:      time.Now(),
		},
		FolderID: folderID,
		Source:   source,
		Error:    err,
	}
}

// NewSelectionChangedEvent creates a new SelectionChangedEvent.
func NewSelectionChangedEvent(source string, selectedIDs []string) *SelectionChangedEvent {
	return &SelectionChangedEvent{
		BaseEvent: events.BaseEvent{
			EventType: EventSelectionChanged,
			Time:      time.Now(),
		},
		SelectedIDs: selectedIDs,
		Source:      source,
	}
}

// NewSortChangedEvent creates a new SortChangedEvent.
func NewSortChangedEvent(source, sortBy string, ascending bool) *SortChangedEvent {
	return &SortChangedEvent{
		BaseEvent: events.BaseEvent{
			EventType: EventSortChanged,
			Time:      time.Now(),
		},
		SortBy:    sortBy,
		Ascending: ascending,
		Source:    source,
	}
}

// NewCurrentPathChangedEvent creates a new CurrentPathChangedEvent.
func NewCurrentPathChangedEvent(source, folderID, folderPath string) *CurrentPathChangedEvent {
	return &CurrentPathChangedEvent{
		BaseEvent: events.BaseEvent{
			EventType: EventCurrentPathChanged,
			Time:      time.Now(),
		},
		FolderID:   folderID,
		FolderPath: folderPath,
		Source:     source,
	}
}
