package folder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

// orchConfig is the configuration every orchestrator test runs with.
func orchConfig(root string) OrchestratorConfig {
	return OrchestratorConfig{
		RootPath:          root,
		RootRemoteID:      "root-id",
		IncludeHidden:     true,
		FolderConcurrency: 4,
		ConflictMode:      ConflictMergeAll,
		Cache:             NewFolderCache(),
	}
}

// buildTestItem is the BuildItem callback shared by every orchestrator test.
func buildTestItem(file localfs.FileEntry, remoteFolderID, _ string) testItem {
	return testItem{filePath: file.Path, remoteFolderID: remoteFolderID, size: file.Size}
}

// writeFiles creates one file per entry in sizes, each of that many bytes.
func writeFiles(t *testing.T, dir string, sizes ...int) {
	t.Helper()
	for i, size := range sizes {
		path := filepath.Join(dir, fmt.Sprintf("file-%d.dat", i))
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// sameSizes returns n entries of the given byte size, for writeFiles.
func sameSizes(n, size int) []int {
	sizes := make([]int, n)
	for i := range sizes {
		sizes[i] = size
	}
	return sizes
}

// drainItems collects everything the orchestrator dispatched, which also lets
// the dispatcher finish.
func drainItems(ch <-chan testItem) []testItem {
	var items []testItem
	for item := range ch {
		items = append(items, item)
	}
	return items
}

// awaitClose fails the test if ch has not closed within 5 seconds.
func awaitClose(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen within 5 seconds", what)
	}
}

// TestRunOrchestrator_Discovery walks a root holding the row's files and checks
// what the orchestrator dispatches and reports. Rows differ only in the files
// on disk; the config, the BuildItem callback and every assertion are shared.
//
// The progress callbacks run on the orchestrator goroutine, so waiting for
// OnOrchestratorDone (not just dispatchDone, which is Part B) is what makes the
// counters and the result safe to read.
func TestRunOrchestrator_Discovery(t *testing.T) {
	tests := []struct {
		name  string
		sizes []int // one file per entry, of that many bytes
	}{
		{name: "empty directory", sizes: nil},
		{name: "flat directory", sizes: sameSizes(5, 5)},
		{name: "varying file sizes", sizes: []int{10, 100, 1000}},
		{name: "many files", sizes: sameSizes(10, 4)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, tc.sizes...)

			wantFiles := len(tc.sizes)
			var wantBytes int64
			for _, size := range tc.sizes {
				wantBytes += int64(size)
			}

			var (
				discovered int
				lastFiles  int
				lastBytes  int64
				doneCalls  int
				doneFiles  int
				outputCh   = make(chan testItem, 100)
				orchDone   = make(chan struct{})
			)

			dispatchDone, result := RunOrchestrator(context.Background(),
				orchConfig(root),
				OrchestratorCallbacks[testItem]{
					OnFileDiscovered: func(snap ProgressSnapshot) {
						discovered++
						if snap.TotalFiles < 1 {
							t.Errorf("snap.TotalFiles should be >= 1, got %d", snap.TotalFiles)
						}
						if snap.TotalFiles < lastFiles {
							t.Errorf("TotalFiles went backwards: %d -> %d", lastFiles, snap.TotalFiles)
						}
						if snap.TotalBytes < lastBytes {
							t.Errorf("TotalBytes went backwards: %d -> %d", lastBytes, snap.TotalBytes)
						}
						lastFiles, lastBytes = snap.TotalFiles, snap.TotalBytes
					},
					BuildItem: buildTestItem,
					OnOrchestratorDone: func(r *OrchestratorResult) {
						doneCalls++
						doneFiles = r.DiscoveredFiles
						close(orchDone)
					},
				},
				outputCh,
			)

			awaitClose(t, dispatchDone, "dispatchDone")
			items := drainItems(outputCh)
			awaitClose(t, orchDone, "OnOrchestratorDone")

			if len(items) != wantFiles {
				t.Errorf("dispatched %d items, want %d", len(items), wantFiles)
			}
			for _, item := range items {
				if item.remoteFolderID != "root-id" {
					t.Errorf("expected remoteFolderID='root-id', got %q", item.remoteFolderID)
				}
			}
			if result.DiscoveredFiles != wantFiles {
				t.Errorf("DiscoveredFiles = %d, want %d", result.DiscoveredFiles, wantFiles)
			}
			if result.DiscoveredBytes != wantBytes {
				t.Errorf("DiscoveredBytes = %d, want %d", result.DiscoveredBytes, wantBytes)
			}
			if result.WalkError != nil {
				t.Errorf("unexpected walk error: %v", result.WalkError)
			}
			if result.FolderError != nil {
				t.Errorf("unexpected folder error: %v", result.FolderError)
			}
			if discovered != wantFiles {
				t.Errorf("OnFileDiscovered called %d times, want %d", discovered, wantFiles)
			}
			if lastFiles != wantFiles {
				t.Errorf("final snapshot file count = %d, want %d", lastFiles, wantFiles)
			}
			if doneCalls != 1 {
				t.Errorf("OnOrchestratorDone called %d times, want 1", doneCalls)
			}
			if doneFiles != wantFiles {
				t.Errorf("done callback saw %d discovered files, want %d", doneFiles, wantFiles)
			}
		})
	}
}

// TestRunOrchestrator_Cancellation verifies clean shutdown on context cancel.
func TestRunOrchestrator_Cancellation(t *testing.T) {
	root := t.TempDir()
	// Enough files that discovery takes some time.
	writeFiles(t, root, sameSizes(20, 4)...)

	ctx, cancel := context.WithCancel(context.Background())
	outputCh := make(chan testItem, 100)

	dispatchDone, _ := RunOrchestrator(ctx, orchConfig(root),
		OrchestratorCallbacks[testItem]{BuildItem: buildTestItem},
		outputCh,
	)

	cancel()

	// dispatchDone must close (no goroutine leak).
	awaitClose(t, dispatchDone, "dispatchDone after cancel")
	drainItems(outputCh)
}

// A cancelled run must report what it actually discovered and flag itself as
// cancelled. Returning zeros made a cancelled upload look like an empty folder,
// so callers anchored a "no files" placeholder — a COMPLETED task — and the
// batch rendered as a clean completion.
func TestRunOrchestrator_CancellationReportsPartialResult(t *testing.T) {
	root := t.TempDir()

	const totalFiles = 200
	writeFiles(t, root, sameSizes(totalFiles, 4)...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outputCh := make(chan testItem, totalFiles)

	var doneCalls atomic.Int32
	var cancelOnce sync.Once
	var seen *OrchestratorResult
	orchDone := make(chan struct{})

	dispatchDone, result := RunOrchestrator(ctx, orchConfig(root),
		OrchestratorCallbacks[testItem]{
			// Runs on the orchestrator goroutine, so cancelling here guarantees
			// the run is still mid-discovery — with plenty of files left, the
			// merge loop takes its ctx.Done() branch.
			OnFileDiscovered: func(snap ProgressSnapshot) {
				if snap.TotalFiles == 5 {
					cancelOnce.Do(func() {
						cancel()
						time.Sleep(2 * time.Millisecond)
					})
				}
			},
			BuildItem: buildTestItem,
			OnOrchestratorDone: func(r *OrchestratorResult) {
				doneCalls.Add(1)
				seen = r
				close(orchDone)
			},
		},
		outputCh,
	)

	// dispatchDone is Part B; the result is populated by Part C, which reports
	// through OnOrchestratorDone. Wait for both.
	awaitClose(t, dispatchDone, "dispatchDone after cancel")
	awaitClose(t, orchDone, "OnOrchestratorDone after cancel")
	drainItems(outputCh)

	if doneCalls.Load() != 1 {
		t.Fatalf("expected OnOrchestratorDone once, got %d", doneCalls.Load())
	}
	if seen != result {
		t.Error("callback should receive the same result the caller holds")
	}
	if !result.Cancelled {
		t.Error("cancelled run must set Cancelled so callers don't read it as an empty folder")
	}
	if result.DiscoveredFiles == 0 {
		t.Error("cancelled run must report the files discovered before the cancel")
	}
	if result.DiscoveredFiles > 0 && result.DiscoveredBytes == 0 {
		t.Error("expected discovered bytes alongside discovered files")
	}
}
