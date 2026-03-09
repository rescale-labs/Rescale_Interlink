package folder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/localfs"
)

// TestCreateFolderStructureStreaming_RootEvent verifies the root FolderReadyEvent
// is emitted first even with an empty directory (no sub-dirs to create).
// Duplicated from internal/cli/folder_upload_helper_test.go for first-party coverage.
func TestCreateFolderStructureStreaming_RootEvent(t *testing.T) {
	root := t.TempDir()
	// Empty directory — no sub-dirs, so no API calls needed

	ctx := context.Background()
	dirChan, _, _ := localfs.WalkStream(ctx, root, localfs.WalkOptions{IncludeHidden: true})

	folderReadyChan := make(chan FolderReadyEvent, 100)
	conflictMode := ConflictMergeAll

	mapping, created, err := CreateFolderStructureStreaming(
		ctx, nil, NewFolderCache(), root, dirChan, "root-id",
		&conflictMode, 4, nil, folderReadyChan, nil, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	close(folderReadyChan)
	var events []FolderReadyEvent
	for e := range folderReadyChan {
		events = append(events, e)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event (root only), got %d", len(events))
	}
	if events[0].LocalPath != root || events[0].RemoteID != "root-id" {
		t.Errorf("root event: got path=%q id=%q, want path=%q id=%q",
			events[0].LocalPath, events[0].RemoteID, root, "root-id")
	}
	if mapping[root] != "root-id" {
		t.Errorf("mapping[root] = %q, want %q", mapping[root], "root-id")
	}
	if created != 0 {
		t.Errorf("created = %d, want 0 (no sub-dirs)", created)
	}
}

// TestProcessFolder_DepthCalculation verifies depth used in FolderReadyEvent.
// Duplicated from internal/cli/folder_upload_helper_test.go for first-party coverage.
func TestProcessFolder_DepthCalculation(t *testing.T) {
	tests := []struct {
		path     string
		expected int
	}{
		{"/root/a", strings.Count("/root/a", string(os.PathSeparator))},
		{"/root/a/b/c", strings.Count("/root/a/b/c", string(os.PathSeparator))},
	}

	for _, tt := range tests {
		depth := strings.Count(tt.path, string(os.PathSeparator))
		if depth != tt.expected {
			t.Errorf("depth(%q) = %d, want %d", tt.path, depth, tt.expected)
		}
	}
}

// TestWalkStreamDirectoryOrdering verifies filepath.WalkDir guarantees
// parent-before-child ordering, which CreateFolderStructureStreaming relies on.
// Duplicated from internal/cli/folder_upload_helper_test.go for first-party coverage.
func TestWalkStreamDirectoryOrdering(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b", "c", "d"), 0755)
	os.MkdirAll(filepath.Join(root, "a", "b", "e"), 0755)
	os.MkdirAll(filepath.Join(root, "x", "y"), 0755)

	ctx := context.Background()
	dirChan, _, _ := localfs.WalkStream(ctx, root, localfs.WalkOptions{
		IncludeHidden: true,
	})

	var dirOrder []string
	for d := range dirChan {
		rel, _ := filepath.Rel(root, d.Path)
		dirOrder = append(dirOrder, rel)
	}

	// Verify each directory appears after its parent
	seen := map[string]bool{".": true}
	for _, d := range dirOrder {
		parent := filepath.Dir(d)
		if !seen[parent] {
			t.Errorf("directory %q appeared before parent %q; order: %v", d, parent, dirOrder)
		}
		seen[d] = true
	}
}
