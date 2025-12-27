package state

import (
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/services"
)

func TestNewFileListState(t *testing.T) {
	eventBus := events.NewEventBus(100)
	state := NewFileListState("remote", eventBus)

	if state == nil {
		t.Fatal("NewFileListState returned nil")
	}

	if state.source != "remote" {
		t.Errorf("source = %q, want %q", state.source, "remote")
	}

	if len(state.GetItems()) != 0 {
		t.Error("Initial items should be empty")
	}
}

func TestFileListStateSetItems(t *testing.T) {
	eventBus := events.NewEventBus(100)
	state := NewFileListState("remote", eventBus)

	items := []services.FileItem{
		{ID: "1", Name: "file1.txt", IsFolder: false, Size: 100},
		{ID: "2", Name: "folder1", IsFolder: true},
		{ID: "3", Name: "file2.txt", IsFolder: false, Size: 200},
	}

	state.SetItems(items)

	gotItems := state.GetItems()
	if len(gotItems) != 3 {
		t.Errorf("Got %d items, want 3", len(gotItems))
	}

	// Folders should be sorted first (default sort by name ascending)
	if !gotItems[0].IsFolder {
		t.Error("First item should be a folder")
	}
}

func TestFileListStateSelection(t *testing.T) {
	eventBus := events.NewEventBus(100)
	state := NewFileListState("remote", eventBus)

	items := []services.FileItem{
		{ID: "1", Name: "file1.txt"},
		{ID: "2", Name: "file2.txt"},
		{ID: "3", Name: "file3.txt"},
	}
	state.SetItems(items)

	// Test Select
	state.Select("1")
	if !state.IsSelected("1") {
		t.Error("Item 1 should be selected")
	}
	if state.GetSelectedCount() != 1 {
		t.Errorf("Selected count = %d, want 1", state.GetSelectedCount())
	}

	// Test Deselect
	state.Deselect("1")
	if state.IsSelected("1") {
		t.Error("Item 1 should not be selected")
	}

	// Test ToggleSelect
	state.ToggleSelect("2")
	if !state.IsSelected("2") {
		t.Error("Item 2 should be selected after toggle")
	}
	state.ToggleSelect("2")
	if state.IsSelected("2") {
		t.Error("Item 2 should not be selected after second toggle")
	}

	// Test SetSelection
	state.SetSelection([]string{"1", "3"})
	if !state.IsSelected("1") || !state.IsSelected("3") {
		t.Error("Items 1 and 3 should be selected")
	}
	if state.IsSelected("2") {
		t.Error("Item 2 should not be selected")
	}

	// Test ClearSelection
	state.ClearSelection()
	if state.GetSelectedCount() != 0 {
		t.Errorf("Selected count after clear = %d, want 0", state.GetSelectedCount())
	}
}

func TestFileListStateSort(t *testing.T) {
	eventBus := events.NewEventBus(100)
	state := NewFileListState("remote", eventBus)

	now := time.Now()
	items := []services.FileItem{
		{ID: "1", Name: "zebra.txt", Size: 100, ModTime: now.Add(-1 * time.Hour)},
		{ID: "2", Name: "folder", IsFolder: true},
		{ID: "3", Name: "alpha.txt", Size: 300, ModTime: now},
		{ID: "4", Name: "beta.txt", Size: 200, ModTime: now.Add(-2 * time.Hour)},
	}
	state.SetItems(items)

	// Default sort: folders first, then by name ascending
	gotItems := state.GetItems()
	if gotItems[0].Name != "folder" {
		t.Errorf("First item = %q, want %q (folder first)", gotItems[0].Name, "folder")
	}
	if gotItems[1].Name != "alpha.txt" {
		t.Errorf("Second item = %q, want %q (alphabetical)", gotItems[1].Name, "alpha.txt")
	}

	// Sort by size ascending
	state.SetSort("size", true)
	gotItems = state.GetItems()
	if gotItems[1].Name != "zebra.txt" || gotItems[1].Size != 100 {
		t.Errorf("After size sort, second item = %q (size %d), want zebra.txt (size 100)",
			gotItems[1].Name, gotItems[1].Size)
	}

	// Sort by size descending
	state.SetSort("size", false)
	gotItems = state.GetItems()
	if gotItems[1].Name != "alpha.txt" || gotItems[1].Size != 300 {
		t.Errorf("After size desc sort, second item = %q (size %d), want alpha.txt (size 300)",
			gotItems[1].Name, gotItems[1].Size)
	}

	// Sort by date
	state.SetSort("date", true)
	gotItems = state.GetItems()
	// Oldest first (after folder)
	if gotItems[1].Name != "beta.txt" {
		t.Errorf("After date asc sort, second item = %q, want beta.txt (oldest)",
			gotItems[1].Name)
	}

	// Verify GetSort
	sortBy, ascending := state.GetSort()
	if sortBy != "date" || ascending != true {
		t.Errorf("GetSort = (%q, %v), want (date, true)", sortBy, ascending)
	}
}

func TestFileListStateCurrentFolder(t *testing.T) {
	eventBus := events.NewEventBus(100)
	state := NewFileListState("remote", eventBus)

	state.SetCurrentFolder("folder123", "/My Library/Documents")

	folderID, folderPath := state.GetCurrentFolder()
	if folderID != "folder123" {
		t.Errorf("FolderID = %q, want %q", folderID, "folder123")
	}
	if folderPath != "/My Library/Documents" {
		t.Errorf("FolderPath = %q, want %q", folderPath, "/My Library/Documents")
	}
}

func TestFileListStateLoading(t *testing.T) {
	eventBus := events.NewEventBus(100)
	state := NewFileListState("remote", eventBus)

	if state.IsLoading() {
		t.Error("Should not be loading initially")
	}

	state.SetLoading(true)
	if !state.IsLoading() {
		t.Error("Should be loading after SetLoading(true)")
	}

	state.SetLoading(false)
	if state.IsLoading() {
		t.Error("Should not be loading after SetLoading(false)")
	}
}

func TestFileListStateFindByID(t *testing.T) {
	eventBus := events.NewEventBus(100)
	state := NewFileListState("remote", eventBus)

	items := []services.FileItem{
		{ID: "1", Name: "file1.txt"},
		{ID: "2", Name: "file2.txt"},
	}
	state.SetItems(items)

	item, found := state.FindByID("2")
	if !found {
		t.Error("Should find item with ID 2")
	}
	if item.Name != "file2.txt" {
		t.Errorf("Found item name = %q, want %q", item.Name, "file2.txt")
	}

	_, found = state.FindByID("nonexistent")
	if found {
		t.Error("Should not find nonexistent item")
	}
}

func TestFileListStateClear(t *testing.T) {
	eventBus := events.NewEventBus(100)
	state := NewFileListState("remote", eventBus)

	items := []services.FileItem{
		{ID: "1", Name: "file1.txt"},
		{ID: "2", Name: "file2.txt"},
	}
	state.SetItems(items)
	state.Select("1")

	state.Clear()

	if state.Count() != 0 {
		t.Errorf("Count after clear = %d, want 0", state.Count())
	}
	if state.GetSelectedCount() != 0 {
		t.Errorf("Selected count after clear = %d, want 0", state.GetSelectedCount())
	}
}

func TestFileListStateGetSelectedItems(t *testing.T) {
	eventBus := events.NewEventBus(100)
	state := NewFileListState("remote", eventBus)

	items := []services.FileItem{
		{ID: "1", Name: "file1.txt"},
		{ID: "2", Name: "file2.txt"},
		{ID: "3", Name: "file3.txt"},
	}
	state.SetItems(items)
	state.SetSelection([]string{"1", "3"})

	selected := state.GetSelectedItems()
	if len(selected) != 2 {
		t.Errorf("Selected items count = %d, want 2", len(selected))
	}

	// Check the right items are returned
	names := make(map[string]bool)
	for _, item := range selected {
		names[item.Name] = true
	}
	if !names["file1.txt"] || !names["file3.txt"] {
		t.Error("Selected items should include file1.txt and file3.txt")
	}
}
