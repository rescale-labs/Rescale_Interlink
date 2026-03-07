package localfs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// createTestTree creates a temporary directory tree for testing WalkStream.
// Returns the root path and a cleanup function.
func createTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// Create directory structure:
	// root/
	//   a/
	//     sub/
	//       deep/
	//         file4.txt
	//       file3.txt
	//     file1.txt
	//     file2.txt
	//   b/
	//     file5.txt
	//   file0.txt
	dirs := []string{
		"a", "a/sub", "a/sub/deep", "b",
	}
	files := map[string]string{
		"file0.txt":           "root file",
		"a/file1.txt":         "file one",
		"a/file2.txt":         "file two",
		"a/sub/file3.txt":     "file three",
		"a/sub/deep/file4.txt": "file four",
		"b/file5.txt":         "file five",
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

func TestWalkStream_BasicTraversal(t *testing.T) {
	root := createTestTree(t)
	ctx := context.Background()

	dirChan, fileChan, errChan := WalkStream(ctx, root, WalkOptions{
		IncludeHidden:  true,
		SkipHiddenDirs: false,
	})

	var dirs []string
	var files []string

	// Drain both channels
	dirsDone := false
	filesDone := false
	for !dirsDone || !filesDone {
		select {
		case entry, ok := <-dirChan:
			if !ok {
				dirsDone = true
				continue
			}
			rel, _ := filepath.Rel(root, entry.Path)
			dirs = append(dirs, rel)
		case entry, ok := <-fileChan:
			if !ok {
				filesDone = true
				continue
			}
			rel, _ := filepath.Rel(root, entry.Path)
			files = append(files, rel)
		}
	}

	// Check for errors
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	default:
	}

	sort.Strings(dirs)
	sort.Strings(files)

	expectedDirs := []string{"a", "a/sub", "a/sub/deep", "b"}
	expectedFiles := []string{"a/file1.txt", "a/file2.txt", "a/sub/deep/file4.txt", "a/sub/file3.txt", "b/file5.txt", "file0.txt"}
	sort.Strings(expectedDirs)
	sort.Strings(expectedFiles)

	if len(dirs) != len(expectedDirs) {
		t.Errorf("dirs: got %d, want %d: %v", len(dirs), len(expectedDirs), dirs)
	}
	for i := range expectedDirs {
		if i < len(dirs) && dirs[i] != expectedDirs[i] {
			t.Errorf("dirs[%d]: got %q, want %q", i, dirs[i], expectedDirs[i])
		}
	}

	if len(files) != len(expectedFiles) {
		t.Errorf("files: got %d, want %d: %v", len(files), len(expectedFiles), files)
	}
	for i := range expectedFiles {
		if i < len(files) && files[i] != expectedFiles[i] {
			t.Errorf("files[%d]: got %q, want %q", i, files[i], expectedFiles[i])
		}
	}
}

func TestWalkStream_ContextCancellation(t *testing.T) {
	root := createTestTree(t)
	ctx, cancel := context.WithCancel(context.Background())

	dirChan, fileChan, errChan := WalkStream(ctx, root, WalkOptions{
		IncludeHidden:  true,
		SkipHiddenDirs: false,
	})

	// Read one entry then cancel
	select {
	case <-dirChan:
	case <-fileChan:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for first entry")
	}

	cancel()

	// Drain remaining entries — channels should close without blocking
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-dirChan:
			if !ok {
				dirChan = nil
			}
		case _, ok := <-fileChan:
			if !ok {
				fileChan = nil
			}
		case <-timeout:
			t.Fatal("timeout draining channels after cancel")
		}
		if dirChan == nil && fileChan == nil {
			break
		}
	}

	// errChan should close without a fatal error (context.Canceled is expected/swallowed)
	select {
	case err := <-errChan:
		if err != nil {
			t.Logf("error after cancel (expected nil): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for errChan close")
	}
}

func TestWalkStream_HiddenFiles(t *testing.T) {
	root := t.TempDir()

	// Create visible and hidden items
	os.MkdirAll(filepath.Join(root, "visible"), 0755)
	os.MkdirAll(filepath.Join(root, ".hidden_dir"), 0755)
	os.WriteFile(filepath.Join(root, "visible.txt"), []byte("v"), 0644)
	os.WriteFile(filepath.Join(root, ".hidden_file"), []byte("h"), 0644)
	os.WriteFile(filepath.Join(root, ".hidden_dir", "inside.txt"), []byte("i"), 0644)

	ctx := context.Background()
	dirChan, fileChan, _ := WalkStream(ctx, root, WalkOptions{
		IncludeHidden:  false,
		SkipHiddenDirs: true,
	})

	var dirs, files []string
	dirsDone, filesDone := false, false
	for !dirsDone || !filesDone {
		select {
		case entry, ok := <-dirChan:
			if !ok { dirsDone = true; continue }
			dirs = append(dirs, filepath.Base(entry.Path))
		case entry, ok := <-fileChan:
			if !ok { filesDone = true; continue }
			files = append(files, filepath.Base(entry.Path))
		}
	}

	// Should only see visible items
	if len(dirs) != 1 || dirs[0] != "visible" {
		t.Errorf("dirs: got %v, want [visible]", dirs)
	}
	if len(files) != 1 || files[0] != "visible.txt" {
		t.Errorf("files: got %v, want [visible.txt]", files)
	}
}

func TestWalkStream_EmptyDirectory(t *testing.T) {
	root := t.TempDir()

	ctx := context.Background()
	dirChan, fileChan, errChan := WalkStream(ctx, root, WalkOptions{
		IncludeHidden: true,
	})

	var count int
	dirsDone, filesDone := false, false
	for !dirsDone || !filesDone {
		select {
		case _, ok := <-dirChan:
			if !ok { dirsDone = true; continue }
			count++
		case _, ok := <-fileChan:
			if !ok { filesDone = true; continue }
			count++
		}
	}

	if count != 0 {
		t.Errorf("expected 0 entries for empty dir, got %d", count)
	}

	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	default:
	}
}

func TestWalkStream_OrderingGuarantee(t *testing.T) {
	// Verify that parent directories are emitted before their children's files
	root := createTestTree(t)
	ctx := context.Background()

	dirChan, fileChan, _ := WalkStream(ctx, root, WalkOptions{
		IncludeHidden: true,
	})

	// Track when we first see each directory vs files in that directory
	dirSeen := make(map[string]int) // dir path -> order index
	fileSeen := make(map[string]int) // file's parent dir -> first file order index
	order := 0

	dirsDone, filesDone := false, false
	for !dirsDone || !filesDone {
		select {
		case entry, ok := <-dirChan:
			if !ok { dirsDone = true; continue }
			rel, _ := filepath.Rel(root, entry.Path)
			dirSeen[rel] = order
			order++
		case entry, ok := <-fileChan:
			if !ok { filesDone = true; continue }
			parentRel, _ := filepath.Rel(root, filepath.Dir(entry.Path))
			if _, exists := fileSeen[parentRel]; !exists {
				fileSeen[parentRel] = order
			}
			order++
		}
	}

	// For each directory that has files, verify dir was seen before first file
	for dir, dirOrder := range dirSeen {
		if fileOrder, hasFiles := fileSeen[dir]; hasFiles {
			if dirOrder > fileOrder {
				t.Errorf("directory %q (order %d) emitted after its file (order %d)", dir, dirOrder, fileOrder)
			}
		}
	}
}

func TestWalkStream_ConsistencyWithWalkCollect(t *testing.T) {
	// WalkStream and WalkCollect should find the same entries
	root := createTestTree(t)
	opts := WalkOptions{IncludeHidden: true, SkipHiddenDirs: true}

	// WalkCollect
	collectResult, err := WalkCollect(root, opts)
	if err != nil {
		t.Fatal(err)
	}

	// WalkStream
	ctx := context.Background()
	dirChan, fileChan, errChan := WalkStream(ctx, root, opts)

	var streamDirs, streamFiles []string
	dirsDone, filesDone := false, false
	for !dirsDone || !filesDone {
		select {
		case entry, ok := <-dirChan:
			if !ok { dirsDone = true; continue }
			streamDirs = append(streamDirs, entry.Path)
		case entry, ok := <-fileChan:
			if !ok { filesDone = true; continue }
			streamFiles = append(streamFiles, entry.Path)
		}
	}
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}

	// Compare counts
	collectDirPaths := make([]string, len(collectResult.Directories))
	for i, d := range collectResult.Directories {
		collectDirPaths[i] = d.Path
	}
	collectFilePaths := make([]string, len(collectResult.Files))
	for i, f := range collectResult.Files {
		collectFilePaths[i] = f.Path
	}

	sort.Strings(collectDirPaths)
	sort.Strings(collectFilePaths)
	sort.Strings(streamDirs)
	sort.Strings(streamFiles)

	if len(streamDirs) != len(collectDirPaths) {
		t.Errorf("dirs count: stream=%d, collect=%d", len(streamDirs), len(collectDirPaths))
	}
	if len(streamFiles) != len(collectFilePaths) {
		t.Errorf("files count: stream=%d, collect=%d", len(streamFiles), len(collectFilePaths))
	}

	for i := range collectDirPaths {
		if i < len(streamDirs) && streamDirs[i] != collectDirPaths[i] {
			t.Errorf("dir mismatch at %d: stream=%q, collect=%q", i, streamDirs[i], collectDirPaths[i])
		}
	}
	for i := range collectFilePaths {
		if i < len(streamFiles) && streamFiles[i] != collectFilePaths[i] {
			t.Errorf("file mismatch at %d: stream=%q, collect=%q", i, streamFiles[i], collectFilePaths[i])
		}
	}
}
