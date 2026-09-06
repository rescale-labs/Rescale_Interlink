package folder

import (
	"context"
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
	dirChan, _, _, _ := localfs.WalkStream(ctx, root, localfs.WalkOptions{IncludeHidden: true})

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
