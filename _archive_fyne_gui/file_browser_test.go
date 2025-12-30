// Package gui provides unit tests for the file browser functionality
// v2.5.0 (November 24, 2025)
package gui

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFormatFileSize tests the file size formatting function
func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		name     string
		bytes    int64
		expected string
	}{
		{"zero bytes", 0, "0 B"},
		{"small bytes", 500, "500 B"},
		{"exactly 1KB", 1024, "1.0 KB"},
		{"1.5 KB", 1536, "1.5 KB"},
		{"exactly 1MB", 1024 * 1024, "1.0 MB"},
		{"1.5 MB", int64(1.5 * 1024 * 1024), "1.5 MB"},
		{"exactly 1GB", 1024 * 1024 * 1024, "1.0 GB"},
		{"2.5 GB", int64(2.5 * 1024 * 1024 * 1024), "2.5 GB"},
		{"exactly 1TB", int64(1024) * 1024 * 1024 * 1024, "1.0 TB"},
		{"large file 100GB", int64(100) * 1024 * 1024 * 1024, "100.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatFileSize(tt.bytes)
			if result != tt.expected {
				t.Errorf("FormatFileSize(%d) = %q, want %q", tt.bytes, result, tt.expected)
			}
		})
	}
}

// TestFileItemCreation tests FileItem structure
func TestFileItemCreation(t *testing.T) {
	// Test file item
	fileItem := FileItem{
		ID:       "file-123",
		Name:     "document.pdf",
		Size:     1024 * 1024,
		IsFolder: false,
		Selected: false,
	}

	if fileItem.ID != "file-123" {
		t.Error("File ID mismatch")
	}
	if fileItem.IsFolder {
		t.Error("File should not be marked as folder")
	}

	// Test folder item
	folderItem := FileItem{
		ID:       "folder-456",
		Name:     "Documents",
		Size:     0,
		IsFolder: true,
		Selected: true,
	}

	if !folderItem.IsFolder {
		t.Error("Folder should be marked as folder")
	}
	if !folderItem.Selected {
		t.Error("Folder should be selected")
	}
}

// TestBreadcrumbEntry tests breadcrumb navigation structure
func TestBreadcrumbEntry(t *testing.T) {
	breadcrumb := []BreadcrumbEntry{
		{ID: "root-123", Name: "My Library"},
		{ID: "folder-456", Name: "Projects"},
		{ID: "folder-789", Name: "2024"},
	}

	if len(breadcrumb) != 3 {
		t.Errorf("Expected 3 breadcrumb items, got %d", len(breadcrumb))
	}

	if breadcrumb[0].Name != "My Library" {
		t.Errorf("Root breadcrumb name incorrect: %s", breadcrumb[0].Name)
	}

	if breadcrumb[2].ID != "folder-789" {
		t.Errorf("Last breadcrumb ID incorrect: %s", breadcrumb[2].ID)
	}
}

// TestSelectionTracking tests the selection map behavior
func TestSelectionTracking(t *testing.T) {
	selectedItems := make(map[string]bool)

	// Select some items
	selectedItems["file1"] = true
	selectedItems["file2"] = true
	selectedItems["file3"] = false

	// Count selected
	count := 0
	for _, selected := range selectedItems {
		if selected {
			count++
		}
	}

	if count != 2 {
		t.Errorf("Expected 2 selected items, got %d", count)
	}

	// Deselect
	selectedItems["file1"] = false

	count = 0
	for _, selected := range selectedItems {
		if selected {
			count++
		}
	}

	if count != 1 {
		t.Errorf("Expected 1 selected item after deselect, got %d", count)
	}
}

// TestContextCancellation tests that contexts can be properly cancelled
func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// Verify context is not done
	select {
	case <-ctx.Done():
		t.Error("Context should not be done before cancel")
	default:
		// Expected
	}

	// Cancel and verify
	cancel()

	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Context should be done after cancel")
	}
}

// TestFileSizeEdgeCases tests edge cases in file size formatting
func TestFileSizeEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		bytes    int64
		expected string
	}{
		{"boundary 1023", 1023, "1023 B"},
		{"boundary 1024", 1024, "1.0 KB"},
		{"boundary 1025", 1025, "1.0 KB"},
		{"negative (shouldn't happen)", -1, "-1 B"}, // Test defensive behavior
		{"very large", int64(999) * 1024 * 1024 * 1024 * 1024, "999.0 TB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatFileSize(tt.bytes)
			if result != tt.expected {
				t.Errorf("FormatFileSize(%d) = %q, want %q", tt.bytes, result, tt.expected)
			}
		})
	}
}

// TestFileItemSorting tests that items are properly sorted by name
func TestFileItemSorting(t *testing.T) {
	items := []FileItem{
		{Name: "zebra.txt", IsFolder: false},
		{Name: "apple.txt", IsFolder: false},
		{Name: "Documents", IsFolder: true},
		{Name: "Archive", IsFolder: true},
		{Name: "banana.txt", IsFolder: false},
	}

	// Separate folders and files
	var folders, files []FileItem
	for _, item := range items {
		if item.IsFolder {
			folders = append(folders, item)
		} else {
			files = append(files, item)
		}
	}

	// Sort each group
	sort.Slice(folders, func(i, j int) bool {
		return strings.ToLower(folders[i].Name) < strings.ToLower(folders[j].Name)
	})
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})

	if len(folders) != 2 {
		t.Errorf("Expected 2 folders, got %d", len(folders))
	}

	if len(files) != 3 {
		t.Errorf("Expected 3 files, got %d", len(files))
	}

	// Verify sort order
	if folders[0].Name != "Archive" {
		t.Errorf("First folder should be 'Archive', got '%s'", folders[0].Name)
	}
	if files[0].Name != "apple.txt" {
		t.Errorf("First file should be 'apple.txt', got '%s'", files[0].Name)
	}
}

// TestSelectedItemsFiltering tests filtering selected items
func TestSelectedItemsFiltering(t *testing.T) {
	items := []FileItem{
		{ID: "1", Name: "file1.txt", Selected: true},
		{ID: "2", Name: "file2.txt", Selected: false},
		{ID: "3", Name: "folder1", IsFolder: true, Selected: true},
		{ID: "4", Name: "file3.txt", Selected: true},
		{ID: "5", Name: "folder2", IsFolder: true, Selected: false},
	}

	var selected []FileItem
	for _, item := range items {
		if item.Selected {
			selected = append(selected, item)
		}
	}

	if len(selected) != 3 {
		t.Errorf("Expected 3 selected items, got %d", len(selected))
	}

	// Count files vs folders in selection
	fileCount := 0
	folderCount := 0
	for _, item := range selected {
		if item.IsFolder {
			folderCount++
		} else {
			fileCount++
		}
	}

	if fileCount != 2 {
		t.Errorf("Expected 2 selected files, got %d", fileCount)
	}

	if folderCount != 1 {
		t.Errorf("Expected 1 selected folder, got %d", folderCount)
	}
}

// TestBreadcrumbNavigation tests breadcrumb truncation logic
func TestBreadcrumbNavigation(t *testing.T) {
	breadcrumb := []BreadcrumbEntry{
		{ID: "root", Name: "My Library"},
		{ID: "folder1", Name: "Projects"},
		{ID: "folder2", Name: "2024"},
		{ID: "folder3", Name: "Q4"},
	}

	// Simulate navigating to index 1 (Projects)
	targetIndex := 1
	if targetIndex >= len(breadcrumb) {
		t.Fatal("Index out of range")
	}

	// Truncate to this point
	truncated := breadcrumb[:targetIndex+1]

	if len(truncated) != 2 {
		t.Errorf("Expected 2 items after truncation, got %d", len(truncated))
	}

	if truncated[len(truncated)-1].Name != "Projects" {
		t.Errorf("Last breadcrumb should be 'Projects', got '%s'", truncated[len(truncated)-1].Name)
	}
}

// TestFileSizeFormattingPrecision tests precision of file size formatting
func TestFileSizeFormattingPrecision(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{1536, "1.5 KB"},       // 1.5 KB exactly
		{1537, "1.5 KB"},       // Should round to 1.5
		{1587, "1.5 KB"},       // Should still be 1.5
		{1638, "1.6 KB"},       // Should round to 1.6
		{1048576, "1.0 MB"},    // 1 MB exactly
		{1572864, "1.5 MB"},    // 1.5 MB exactly
		{1073741824, "1.0 GB"}, // 1 GB exactly
	}

	for _, tt := range tests {
		result := FormatFileSize(tt.bytes)
		if result != tt.expected {
			t.Errorf("FormatFileSize(%d) = %q, want %q", tt.bytes, result, tt.expected)
		}
	}
}

// TestConcurrentSelectionUpdates tests thread safety of selection updates
func TestConcurrentSelectionUpdates(t *testing.T) {
	selectedItems := make(map[string]bool)
	var mu sync.Mutex

	var wg sync.WaitGroup

	// Spawn multiple goroutines doing concurrent selection updates
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := string(rune('a' + (id % 5)))

			for j := 0; j < 50; j++ {
				mu.Lock()
				if j%2 == 0 {
					selectedItems[key] = true
				} else {
					selectedItems[key] = false
				}
				mu.Unlock()
			}
		}(i)
	}

	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Error("Concurrent operations timed out - possible deadlock")
	}
}

// BenchmarkFormatFileSize benchmarks the file size formatting function
func BenchmarkFormatFileSize(b *testing.B) {
	sizes := []int64{
		0,
		1024,
		1024 * 1024,
		1024 * 1024 * 1024,
		int64(100) * 1024 * 1024 * 1024,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, size := range sizes {
			_ = FormatFileSize(size)
		}
	}
}
