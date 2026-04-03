package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

func TestRemoteFolderInfo_Fields(t *testing.T) {
	info := RemoteFolderInfo{
		FolderID:     "folder-123",
		Name:         "my-folder",
		RelativePath: "parent/my-folder",
	}
	if info.FolderID != "folder-123" {
		t.Errorf("FolderID = %q, want %q", info.FolderID, "folder-123")
	}
	if info.Name != "my-folder" {
		t.Errorf("Name = %q, want %q", info.Name, "my-folder")
	}
	if info.RelativePath != "parent/my-folder" {
		t.Errorf("RelativePath = %q, want %q", info.RelativePath, "parent/my-folder")
	}
}

func TestRemoteFileTask_Fields(t *testing.T) {
	cf := &models.CloudFile{ID: "cloud-1", Name: "test.dat"}
	task := RemoteFileTask{
		FileID:       "file-456",
		Name:         "test.dat",
		RelativePath: "subdir/test.dat",
		Size:         1024,
		CloudFile:    cf,
	}
	if task.FileID != "file-456" {
		t.Errorf("FileID = %q, want %q", task.FileID, "file-456")
	}
	if task.Size != 1024 {
		t.Errorf("Size = %d, want %d", task.Size, 1024)
	}
	if task.CloudFile == nil || task.CloudFile.ID != "cloud-1" {
		t.Error("CloudFile not set correctly")
	}
}

func TestRemoteFileTask_NilCloudFile(t *testing.T) {
	task := RemoteFileTask{
		FileID: "file-789",
		Name:   "no-cloud.txt",
		Size:   0,
	}
	if task.CloudFile != nil {
		t.Error("expected nil CloudFile")
	}
}

func TestScanEvent_FolderDiscovery(t *testing.T) {
	info := &RemoteFolderInfo{FolderID: "f1", Name: "docs"}
	event := ScanEvent{Folder: info}

	if event.Folder == nil {
		t.Fatal("expected non-nil Folder")
	}
	if event.File != nil {
		t.Error("expected nil File for folder discovery event")
	}
	if event.Folder.FolderID != "f1" {
		t.Errorf("Folder.FolderID = %q, want %q", event.Folder.FolderID, "f1")
	}
}

func TestScanEvent_FileDiscovery(t *testing.T) {
	task := &RemoteFileTask{FileID: "f2", Name: "data.csv", Size: 2048}
	event := ScanEvent{File: task}

	if event.File == nil {
		t.Fatal("expected non-nil File")
	}
	if event.Folder != nil {
		t.Error("expected nil Folder for file discovery event")
	}
	if event.File.Size != 2048 {
		t.Errorf("File.Size = %d, want %d", event.File.Size, 2048)
	}
}

func TestScanProgress_ZeroValue(t *testing.T) {
	var p ScanProgress
	if p.FoldersFound != 0 || p.FilesFound != 0 || p.BytesFound != 0 {
		t.Error("zero-value ScanProgress should have all zero fields")
	}
}

func TestScanProgress_Accumulation(t *testing.T) {
	p := ScanProgress{FoldersFound: 3, FilesFound: 10, BytesFound: 1048576}
	if p.FoldersFound != 3 {
		t.Errorf("FoldersFound = %d, want 3", p.FoldersFound)
	}
	if p.FilesFound != 10 {
		t.Errorf("FilesFound = %d, want 10", p.FilesFound)
	}
	if p.BytesFound != 1048576 {
		t.Errorf("BytesFound = %d, want 1048576", p.BytesFound)
	}
}

// --- Streaming scan regression tests ---

// folderContentsJSON builds a folder contents API response.
// Mirrors the format from api/client_test.go:folderContentsPage.
func folderContentsJSON(folders []map[string]string, files []map[string]interface{}, nextURL string) []byte {
	results := make([]map[string]interface{}, 0, len(folders)+len(files))
	for _, f := range folders {
		results = append(results, map[string]interface{}{
			"type": "folder",
			"item": map[string]interface{}{
				"id":   f["id"],
				"name": f["name"],
			},
		})
	}
	for _, f := range files {
		results = append(results, map[string]interface{}{
			"type": "file",
			"item": f,
		})
	}
	resp := map[string]interface{}{"results": results}
	if nextURL != "" {
		resp["next"] = nextURL
	}
	b, _ := json.Marshal(resp)
	return b
}

// makeFile builds a minimal file item for folderContentsJSON.
func makeFile(id, name string, size int) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "name": name,
		"decryptedSize":        json.Number(fmt.Sprintf("%d", size)),
		"encodedEncryptionKey": "k", "iv": "iv",
		"owner": "u", "path": "/p",
		"storage":   map[string]interface{}{"id": "s1", "storageType": "S3"},
		"pathParts": map[string]interface{}{"container": "b", "path": "p"},
	}
}

// newTestClient creates an api.Client pointed at a test server, bypassing
// the platform URL allowlist via api.NewClientForTest.
func newTestClient(t *testing.T, serverURL string) *api.Client {
	t.Helper()
	return api.NewClientForTest(&config.Config{
		APIBaseURL: serverURL,
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	})
}

// TestScanRemoteFolderStreaming_NoDeadlockOnWideFanOut verifies that a wide
// folder tree does not deadlock. Before the unbounded backlog fix, 8 workers
// each discovering 40+ subfolders would saturate the bounded 256-slot workCh,
// causing all workers to block on self-enqueue simultaneously.
//
// Tree structure:
//   root: 10 subfolders
//   each depth-1 folder: 40 subfolders + 2 files
//   each depth-2 folder: 2 files
// Total: 410 folders, 820 files
func TestScanRemoteFolderStreaming_NoDeadlockOnWideFanOut(t *testing.T) {
	const (
		depth1Count  = 10
		depth2Count  = 40
		filesPerLeaf = 2
	)
	expectedFolders := depth1Count + depth1Count*depth2Count // 10 + 400 = 410
	expectedFiles := depth1Count*filesPerLeaf + depth1Count*depth2Count*filesPerLeaf // 20 + 800 = 820

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Extract folder ID from URL: /api/v3/folders/{id}/contents/
		path := r.URL.Path
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 4 {
			http.Error(w, "bad path", 400)
			return
		}
		folderID := parts[3]

		switch {
		case folderID == "root":
			// Root: 10 subfolders, no files
			folders := make([]map[string]string, depth1Count)
			for i := 0; i < depth1Count; i++ {
				folders[i] = map[string]string{
					"id":   fmt.Sprintf("d1_%d", i),
					"name": fmt.Sprintf("depth1_%d", i),
				}
			}
			w.Write(folderContentsJSON(folders, nil, ""))

		case strings.HasPrefix(folderID, "d1_") && !strings.Contains(folderID, "_d2_"):
			// Depth-1 folder: 40 subfolders + 2 files
			idx := folderID[3:] // e.g. "0", "1", ...
			folders := make([]map[string]string, depth2Count)
			for j := 0; j < depth2Count; j++ {
				folders[j] = map[string]string{
					"id":   fmt.Sprintf("d1_%s_d2_%d", idx, j),
					"name": fmt.Sprintf("depth2_%d", j),
				}
			}
			files := make([]map[string]interface{}, filesPerLeaf)
			for j := 0; j < filesPerLeaf; j++ {
				files[j] = makeFile(
					fmt.Sprintf("f_d1_%s_%d", idx, j),
					fmt.Sprintf("file_d1_%s_%d.txt", idx, j),
					100,
				)
			}
			w.Write(folderContentsJSON(folders, files, ""))

		case strings.Contains(folderID, "_d2_"):
			// Depth-2 leaf folder: 2 files, no subfolders
			files := make([]map[string]interface{}, filesPerLeaf)
			for j := 0; j < filesPerLeaf; j++ {
				files[j] = makeFile(
					fmt.Sprintf("f_%s_%d", folderID, j),
					fmt.Sprintf("file_%s_%d.txt", folderID, j),
					50,
				)
			}
			w.Write(folderContentsJSON(nil, files, ""))

		default:
			http.Error(w, "unknown folder: "+folderID, 404)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	eventCh, errCh := ScanRemoteFolderStreaming(ctx, client, "root", nil)

	var folderCount, fileCount int
	for event := range eventCh {
		if event.Folder != nil {
			folderCount++
		}
		if event.File != nil {
			fileCount++
		}
	}

	// Check for scan errors
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected scan error: %v", err)
		}
	default:
	}

	if folderCount != expectedFolders {
		t.Errorf("folders = %d, want %d", folderCount, expectedFolders)
	}
	if fileCount != expectedFiles {
		t.Errorf("files = %d, want %d", fileCount, expectedFiles)
	}
}

// TestScanRemoteFolderStreaming_CancelMidScan verifies that cancelling the
// context mid-scan does not hang. The eventCh must close and all goroutines
// must exit cleanly.
func TestScanRemoteFolderStreaming_CancelMidScan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 4 {
			http.Error(w, "bad path", 400)
			return
		}
		folderID := parts[3]

		// Return a tree with many subfolders to ensure scanning is still active when we cancel
		if folderID == "root" {
			folders := make([]map[string]string, 20)
			for i := 0; i < 20; i++ {
				folders[i] = map[string]string{
					"id":   fmt.Sprintf("sub_%d", i),
					"name": fmt.Sprintf("sub_%d", i),
				}
			}
			w.Write(folderContentsJSON(folders, nil, ""))
			return
		}

		// Each subfolder has more subfolders to keep scanning going
		folders := make([]map[string]string, 5)
		for i := 0; i < 5; i++ {
			folders[i] = map[string]string{
				"id":   fmt.Sprintf("%s_%d", folderID, i),
				"name": fmt.Sprintf("child_%d", i),
			}
		}
		files := []map[string]interface{}{
			makeFile(fmt.Sprintf("f_%s", folderID), "file.txt", 100),
		}
		w.Write(folderContentsJSON(folders, files, ""))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)

	goroutinesBefore := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	eventCh, _ := ScanRemoteFolderStreaming(ctx, client, "root", nil)

	// Drain some events, then cancel
	eventsReceived := 0
	for event := range eventCh {
		_ = event
		eventsReceived++
		if eventsReceived >= 50 {
			cancel()
		}
	}
	cancel() // ensure cancel is called even if channel closed early

	// eventCh is closed — verify we got here without hanging.
	// Allow settling time for goroutines to exit (httptest server, scan workers).
	time.Sleep(500 * time.Millisecond)

	goroutinesAfter := runtime.NumGoroutine()
	// Allow delta for runtime goroutines (GC, finalizer, httptest cleanup, etc.)
	if goroutinesAfter > goroutinesBefore+10 {
		t.Errorf("potential goroutine leak: before=%d, after=%d", goroutinesBefore, goroutinesAfter)
	}
}

// TestScanRemoteFolderStreaming_PreCancelledContext verifies that passing an
// already-cancelled context does not hang and closes channels immediately.
func TestScanRemoteFolderStreaming_PreCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("API should not be called with a pre-cancelled context")
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	done := make(chan struct{})
	go func() {
		defer close(done)
		eventCh, errCh := ScanRemoteFolderStreaming(ctx, client, "root", nil)

		var events int
		for range eventCh {
			events++
		}
		if events != 0 {
			t.Errorf("expected 0 events with pre-cancelled context, got %d", events)
		}

		// errCh should also be closed
		select {
		case _, ok := <-errCh:
			if ok {
				// An error is acceptable but channel must close
			}
		case <-time.After(1 * time.Second):
			t.Error("errCh was not closed")
		}
	}()

	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("timed out — ScanRemoteFolderStreaming hung with pre-cancelled context")
	}
}

// TestScanRemoteFolderStreaming_ErrorInSubfolder verifies that a scan error
// in one subfolder is reported on errCh and the scan completes without hanging.
func TestScanRemoteFolderStreaming_ErrorInSubfolder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 4 {
			http.Error(w, "bad path", 400)
			return
		}
		folderID := parts[3]

		switch folderID {
		case "root":
			folders := []map[string]string{
				{"id": "good", "name": "good"},
				{"id": "bad", "name": "bad"},
			}
			files := []map[string]interface{}{
				makeFile("f_root", "root_file.txt", 100),
			}
			w.Write(folderContentsJSON(folders, files, ""))
		case "good":
			files := []map[string]interface{}{
				makeFile("f_good", "good_file.txt", 200),
			}
			w.Write(folderContentsJSON(nil, files, ""))
		case "bad":
			http.Error(w, "internal server error", 500)
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh, errCh := ScanRemoteFolderStreaming(ctx, client, "root", nil)

	var folderCount, fileCount int
	for event := range eventCh {
		if event.Folder != nil {
			folderCount++
		}
		if event.File != nil {
			fileCount++
		}
	}

	// Should have received an error for the "bad" folder
	var scanErr error
	select {
	case scanErr = <-errCh:
	default:
	}

	if scanErr == nil {
		t.Error("expected scan error for the bad subfolder, got nil")
	}

	// Should have received events from root and "good" folder
	if folderCount != 2 { // "good" and "bad" folders both emitted before listing contents
		t.Errorf("folders = %d, want 2", folderCount)
	}
	if fileCount < 1 { // at least root_file.txt and/or good_file.txt
		t.Errorf("files = %d, want at least 1", fileCount)
	}
}
