package folder

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/localfs"
)

// testItem is a simple backlog item for orchestrator tests.
type testItem struct {
	filePath       string
	remoteFolderID string
	size           int64
}

// TestRunOrchestrator_EmptyDirectory verifies clean shutdown with no files.
func TestRunOrchestrator_EmptyDirectory(t *testing.T) {
	root := t.TempDir()

	outputCh := make(chan testItem, 100)
	cache := NewFolderCache()

	dispatchDone, result := RunOrchestrator(context.Background(),
		OrchestratorConfig{
			RootPath:          root,
			RootRemoteID:      "root-id",
			IncludeHidden:     true,
			FolderConcurrency: 4,
			ConflictMode:      ConflictMergeAll,
			Cache:             cache,
		},
		OrchestratorCallbacks[testItem]{
			BuildItem: func(file localfs.FileEntry, remoteFolderID, rootPath string) testItem {
				return testItem{filePath: file.Path, remoteFolderID: remoteFolderID, size: file.Size}
			},
		},
		outputCh,
	)

	<-dispatchDone

	// Collect items
	var items []testItem
	for item := range outputCh {
		items = append(items, item)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
	if result.DiscoveredFiles != 0 {
		t.Errorf("expected 0 discovered files, got %d", result.DiscoveredFiles)
	}
	if result.WalkError != nil {
		t.Errorf("unexpected walk error: %v", result.WalkError)
	}
	if result.FolderError != nil {
		t.Errorf("unexpected folder error: %v", result.FolderError)
	}
}

// TestRunOrchestrator_FlatDirectory verifies N files in root → N items on outputCh.
func TestRunOrchestrator_FlatDirectory(t *testing.T) {
	root := t.TempDir()

	// Create 5 files in root
	for i := 0; i < 5; i++ {
		f, err := os.CreateTemp(root, "file-*.txt")
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte("hello"))
		f.Close()
	}

	outputCh := make(chan testItem, 100)
	cache := NewFolderCache()
	var fileCount int32

	dispatchDone, result := RunOrchestrator(context.Background(),
		OrchestratorConfig{
			RootPath:          root,
			RootRemoteID:      "root-id",
			IncludeHidden:     true,
			FolderConcurrency: 4,
			ConflictMode:      ConflictMergeAll,
			Cache:             cache,
		},
		OrchestratorCallbacks[testItem]{
			OnFileDiscovered: func(snap ProgressSnapshot) {
				atomic.AddInt32(&fileCount, 1)
			},
			BuildItem: func(file localfs.FileEntry, remoteFolderID, rootPath string) testItem {
				return testItem{filePath: file.Path, remoteFolderID: remoteFolderID, size: file.Size}
			},
		},
		outputCh,
	)

	<-dispatchDone

	var items []testItem
	for item := range outputCh {
		items = append(items, item)
	}

	if len(items) != 5 {
		t.Errorf("expected 5 items, got %d", len(items))
	}
	for _, item := range items {
		if item.remoteFolderID != "root-id" {
			t.Errorf("expected remoteFolderID='root-id', got %q", item.remoteFolderID)
		}
	}
	if result.DiscoveredFiles != 5 {
		t.Errorf("expected 5 discovered files, got %d", result.DiscoveredFiles)
	}
	if int(atomic.LoadInt32(&fileCount)) != 5 {
		t.Errorf("OnFileDiscovered called %d times, want 5", atomic.LoadInt32(&fileCount))
	}
}

// TestRunOrchestrator_MultipleRootFiles verifies that multiple files in
// root directory are all dispatched with the correct remote folder ID.
// This tests the "parent already mapped" (non-pending) path for all items.
func TestRunOrchestrator_MultipleRootFiles(t *testing.T) {
	root := t.TempDir()

	// Create files with varying sizes
	sizes := []int{10, 100, 1000}
	for _, sz := range sizes {
		f, _ := os.CreateTemp(root, "sized-*.dat")
		f.Write(make([]byte, sz))
		f.Close()
	}

	outputCh := make(chan testItem, 100)
	cache := NewFolderCache()

	dispatchDone, result := RunOrchestrator(context.Background(),
		OrchestratorConfig{
			RootPath:          root,
			RootRemoteID:      "root-id",
			IncludeHidden:     true,
			FolderConcurrency: 4,
			ConflictMode:      ConflictMergeAll,
			Cache:             cache,
		},
		OrchestratorCallbacks[testItem]{
			BuildItem: func(file localfs.FileEntry, remoteFolderID, rootPath string) testItem {
				return testItem{filePath: file.Path, remoteFolderID: remoteFolderID, size: file.Size}
			},
		},
		outputCh,
	)

	<-dispatchDone

	var items []testItem
	for item := range outputCh {
		items = append(items, item)
	}

	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}

	// All should have root-id as their remote folder
	for _, item := range items {
		if item.remoteFolderID != "root-id" {
			t.Errorf("expected remoteFolderID='root-id', got %q", item.remoteFolderID)
		}
	}

	// Check discovered bytes are non-zero
	if result.DiscoveredBytes == 0 {
		t.Error("expected non-zero discovered bytes")
	}
	if result.DiscoveredFiles != 3 {
		t.Errorf("expected 3 discovered files, got %d", result.DiscoveredFiles)
	}
}

// TestRunOrchestrator_Cancellation verifies clean shutdown on context cancel.
func TestRunOrchestrator_Cancellation(t *testing.T) {
	root := t.TempDir()

	// Create enough files that discovery takes some time
	for i := 0; i < 20; i++ {
		f, _ := os.CreateTemp(root, "file-*.txt")
		f.Write([]byte("data"))
		f.Close()
	}

	ctx, cancel := context.WithCancel(context.Background())
	outputCh := make(chan testItem, 100)
	cache := NewFolderCache()

	dispatchDone, _ := RunOrchestrator(ctx,
		OrchestratorConfig{
			RootPath:          root,
			RootRemoteID:      "root-id",
			IncludeHidden:     true,
			FolderConcurrency: 4,
			ConflictMode:      ConflictMergeAll,
			Cache:             cache,
		},
		OrchestratorCallbacks[testItem]{
			BuildItem: func(file localfs.FileEntry, remoteFolderID, rootPath string) testItem {
				return testItem{filePath: file.Path, remoteFolderID: remoteFolderID, size: file.Size}
			},
		},
		outputCh,
	)

	// Cancel immediately
	cancel()

	// dispatchDone must close (no goroutine leak)
	select {
	case <-dispatchDone:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchDone did not close within 5 seconds after cancel")
	}

	// Drain outputCh
	for range outputCh {
	}
}

// TestRunOrchestrator_CallbackInvocation verifies all callbacks are called
// with correct counts.
func TestRunOrchestrator_CallbackInvocation(t *testing.T) {
	root := t.TempDir()

	// Create 3 files
	for i := 0; i < 3; i++ {
		f, _ := os.CreateTemp(root, "cb-*.txt")
		f.Write([]byte("test"))
		f.Close()
	}

	outputCh := make(chan testItem, 100)
	cache := NewFolderCache()

	var fileDiscoveredCount int32
	var orchDoneCalled int32

	dispatchDone, _ := RunOrchestrator(context.Background(),
		OrchestratorConfig{
			RootPath:          root,
			RootRemoteID:      "root-id",
			IncludeHidden:     true,
			FolderConcurrency: 4,
			ConflictMode:      ConflictMergeAll,
			Cache:             cache,
		},
		OrchestratorCallbacks[testItem]{
			OnFileDiscovered: func(snap ProgressSnapshot) {
				atomic.AddInt32(&fileDiscoveredCount, 1)
				if snap.TotalFiles < 1 {
					t.Errorf("snap.TotalFiles should be >= 1, got %d", snap.TotalFiles)
				}
			},
			OnOrchestratorDone: func(r *OrchestratorResult) {
				atomic.AddInt32(&orchDoneCalled, 1)
				if r.DiscoveredFiles != 3 {
					t.Errorf("expected 3 discovered files in done callback, got %d", r.DiscoveredFiles)
				}
			},
			BuildItem: func(file localfs.FileEntry, remoteFolderID, rootPath string) testItem {
				return testItem{filePath: file.Path, remoteFolderID: remoteFolderID, size: file.Size}
			},
		},
		outputCh,
	)

	<-dispatchDone
	for range outputCh {
	}

	if atomic.LoadInt32(&fileDiscoveredCount) != 3 {
		t.Errorf("OnFileDiscovered called %d times, want 3", atomic.LoadInt32(&fileDiscoveredCount))
	}
	if atomic.LoadInt32(&orchDoneCalled) != 1 {
		t.Errorf("OnOrchestratorDone called %d times, want 1", atomic.LoadInt32(&orchDoneCalled))
	}
}

// TestRunOrchestrator_ProgressSnapshotIntegrity verifies that ProgressSnapshot
// carries monotonically increasing counters.
func TestRunOrchestrator_ProgressSnapshotIntegrity(t *testing.T) {
	root := t.TempDir()

	for i := 0; i < 10; i++ {
		f, _ := os.CreateTemp(root, "snap-*.txt")
		f.Write([]byte("snap"))
		f.Close()
	}

	outputCh := make(chan testItem, 100)
	cache := NewFolderCache()

	var lastFiles int
	var lastBytes int64

	dispatchDone, _ := RunOrchestrator(context.Background(),
		OrchestratorConfig{
			RootPath:          root,
			RootRemoteID:      "root-id",
			IncludeHidden:     true,
			FolderConcurrency: 4,
			ConflictMode:      ConflictMergeAll,
			Cache:             cache,
		},
		OrchestratorCallbacks[testItem]{
			OnFileDiscovered: func(snap ProgressSnapshot) {
				if snap.TotalFiles < lastFiles {
					t.Errorf("TotalFiles went backwards: %d -> %d", lastFiles, snap.TotalFiles)
				}
				if snap.TotalBytes < lastBytes {
					t.Errorf("TotalBytes went backwards: %d -> %d", lastBytes, snap.TotalBytes)
				}
				lastFiles = snap.TotalFiles
				lastBytes = snap.TotalBytes
			},
			BuildItem: func(file localfs.FileEntry, remoteFolderID, rootPath string) testItem {
				return testItem{filePath: file.Path, remoteFolderID: remoteFolderID, size: file.Size}
			},
		},
		outputCh,
	)

	<-dispatchDone
	for range outputCh {
	}

	if lastFiles != 10 {
		t.Errorf("final file count = %d, want 10", lastFiles)
	}
}
