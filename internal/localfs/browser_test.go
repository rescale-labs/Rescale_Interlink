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

// === v4.8.8: Symlink following tests ===

// drainWalkStream collects all entries from WalkStream channels into sorted path slices.
func drainWalkStream(t *testing.T, root string, opts WalkOptions) (dirs, files []string) {
	t.Helper()
	ctx := context.Background()
	dirChan, fileChan, errChan := WalkStream(ctx, root, opts)

	dirsDone, filesDone := false, false
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

	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("unexpected walk error: %v", err)
		}
	default:
	}

	sort.Strings(dirs)
	sort.Strings(files)
	return
}

func TestWalkStream_FollowSymlinks_SymlinkedDir(t *testing.T) {
	root := createTestTree(t)

	// Create symlink: root/link_to_b -> root/b
	os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "link_to_b"))

	dirs, files := drainWalkStream(t, root, WalkOptions{
		IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
	})

	// Should include both "b" (real) and "link_to_b" (symlink alias)
	if !contains(dirs, "link_to_b") {
		t.Errorf("expected link_to_b in dirs, got: %v", dirs)
	}
	// Should include files from both
	if !contains(files, "b/file5.txt") {
		t.Errorf("expected b/file5.txt in files, got: %v", files)
	}
	if !contains(files, "link_to_b/file5.txt") {
		t.Errorf("expected link_to_b/file5.txt in files, got: %v", files)
	}
}

func TestWalkStream_FollowSymlinks_SymlinkedFile(t *testing.T) {
	root := createTestTree(t)

	// Create file symlink: root/a/link_file.txt -> root/file0.txt
	os.Symlink(filepath.Join(root, "file0.txt"), filepath.Join(root, "a", "link_file.txt"))

	_, files := drainWalkStream(t, root, WalkOptions{
		IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
	})

	if !contains(files, "a/link_file.txt") {
		t.Errorf("expected a/link_file.txt in files, got: %v", files)
	}
}

func TestWalkStream_FollowSymlinks_CircularSymlink(t *testing.T) {
	root := t.TempDir()

	// root/a/
	// root/a/loop -> root/a  (cycle!)
	os.MkdirAll(filepath.Join(root, "a"), 0755)
	os.WriteFile(filepath.Join(root, "a", "file.txt"), []byte("test"), 0644)
	os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "a", "loop"))

	// Should NOT hang, should complete normally
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainWalkStream(t, root, WalkOptions{
			IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
		})
	}()

	select {
	case <-done:
		// OK — completed without hanging
	case <-time.After(10 * time.Second):
		t.Fatal("walk with circular symlink did not complete within timeout")
	}
}

func TestWalkStream_FollowSymlinks_SelfReference(t *testing.T) {
	root := t.TempDir()

	// root/self -> root (symlink to the root itself)
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("test"), 0644)
	os.Symlink(root, filepath.Join(root, "self"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		drainWalkStream(t, root, WalkOptions{
			IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
		})
	}()

	select {
	case <-done:
		// OK
	case <-time.After(10 * time.Second):
		t.Fatal("walk with self-reference symlink did not complete within timeout")
	}
}

func TestWalkStream_FollowSymlinks_BrokenSymlink(t *testing.T) {
	root := t.TempDir()

	os.WriteFile(filepath.Join(root, "file.txt"), []byte("test"), 0644)
	os.Symlink("/nonexistent/path/that/does/not/exist", filepath.Join(root, "broken_link"))

	dirs, files := drainWalkStream(t, root, WalkOptions{
		IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
	})

	// Broken symlink should be silently skipped
	if contains(dirs, "broken_link") || contains(files, "broken_link") {
		t.Errorf("broken symlink should not appear; dirs=%v, files=%v", dirs, files)
	}
	// Regular file should still be found
	if !contains(files, "file.txt") {
		t.Errorf("expected file.txt in files, got: %v", files)
	}
}

func TestWalkStream_FollowSymlinks_False_Unchanged(t *testing.T) {
	root := createTestTree(t)

	// Add a symlink — should be skipped with FollowSymlinks=false
	os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "link_to_b"))
	os.Symlink(filepath.Join(root, "file0.txt"), filepath.Join(root, "link_file.txt"))

	dirs, files := drainWalkStream(t, root, WalkOptions{
		IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: false,
	})

	// Symlinks should NOT appear
	if contains(dirs, "link_to_b") {
		t.Errorf("symlink dir should be skipped with FollowSymlinks=false; dirs=%v", dirs)
	}
	if contains(files, "link_file.txt") {
		t.Errorf("symlink file should be skipped with FollowSymlinks=false; files=%v", files)
	}
}

func TestWalkCollect_FollowSymlinks(t *testing.T) {
	root := createTestTree(t)

	os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "link_to_b"))

	result, err := WalkCollect(root, WalkOptions{
		IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	dirPaths := make([]string, len(result.Directories))
	for i, d := range result.Directories {
		dirPaths[i], _ = filepath.Rel(root, d.Path)
	}
	filePaths := make([]string, len(result.Files))
	for i, f := range result.Files {
		filePaths[i], _ = filepath.Rel(root, f.Path)
	}
	sort.Strings(dirPaths)
	sort.Strings(filePaths)

	if !contains(dirPaths, "link_to_b") {
		t.Errorf("expected link_to_b in dirs, got: %v", dirPaths)
	}
	if !contains(filePaths, "link_to_b/file5.txt") {
		t.Errorf("expected link_to_b/file5.txt in files, got: %v", filePaths)
	}
	// Symlinks slice should be empty when following
	if len(result.Symlinks) != 0 {
		syms := make([]string, len(result.Symlinks))
		for i, s := range result.Symlinks {
			syms[i], _ = filepath.Rel(root, s.Path)
		}
		t.Errorf("expected 0 symlinks when following, got: %v", syms)
	}
}

func TestWalkStream_FollowSymlinks_PathRewriting(t *testing.T) {
	root := createTestTree(t)

	// Create symlink from root/link -> root/a/sub
	os.Symlink(filepath.Join(root, "a", "sub"), filepath.Join(root, "link"))

	dirs, files := drainWalkStream(t, root, WalkOptions{
		IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
	})

	// All entries from the symlinked walk should have paths under "link/..."
	// NOT under "a/sub/..."
	if !contains(dirs, "link") {
		t.Errorf("expected link in dirs, got: %v", dirs)
	}
	if !contains(files, "link/file3.txt") {
		t.Errorf("expected link/file3.txt in files, got: %v", files)
	}
	if !contains(dirs, "link/deep") {
		t.Errorf("expected link/deep in dirs, got: %v", dirs)
	}
	if !contains(files, "link/deep/file4.txt") {
		t.Errorf("expected link/deep/file4.txt in files, got: %v", files)
	}
}

func TestWalkStream_FollowSymlinks_DuplicateAliases(t *testing.T) {
	root := t.TempDir()

	// Create a shared directory
	shared := filepath.Join(root, "shared")
	os.MkdirAll(shared, 0755)
	os.WriteFile(filepath.Join(shared, "data.txt"), []byte("shared data"), 0644)

	// Create two sibling symlinks pointing to the same target
	os.Symlink(shared, filepath.Join(root, "linkA"))
	os.Symlink(shared, filepath.Join(root, "linkB"))

	_, files := drainWalkStream(t, root, WalkOptions{
		IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
	})

	// BOTH aliases should produce entries (ancestry-stack, not global dedup)
	if !contains(files, "linkA/data.txt") {
		t.Errorf("expected linkA/data.txt in files, got: %v", files)
	}
	if !contains(files, "linkB/data.txt") {
		t.Errorf("expected linkB/data.txt in files, got: %v", files)
	}
	if !contains(files, "shared/data.txt") {
		t.Errorf("expected shared/data.txt in files, got: %v", files)
	}
}

func TestWalkStream_FollowSymlinks_AliasRootDirEntry(t *testing.T) {
	root := createTestTree(t)

	// root/link_to_sub -> root/a/sub
	os.Symlink(filepath.Join(root, "a", "sub"), filepath.Join(root, "link_to_sub"))

	dirs, _ := drainWalkStream(t, root, WalkOptions{
		IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
	})

	// Verify that "link_to_sub" appears as a directory entry
	if !contains(dirs, "link_to_sub") {
		t.Errorf("expected link_to_sub as directory entry, got: %v", dirs)
	}
}

func TestWalkCollect_FollowSymlinks_Consistency(t *testing.T) {
	root := createTestTree(t)

	os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "link_to_b"))
	os.Symlink(filepath.Join(root, "file0.txt"), filepath.Join(root, "link_file.txt"))

	opts := WalkOptions{IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true}

	// WalkCollect
	collectResult, err := WalkCollect(root, opts)
	if err != nil {
		t.Fatal(err)
	}

	// WalkStream
	streamDirs, streamFiles := drainWalkStream(t, root, opts)

	collectDirs := make([]string, len(collectResult.Directories))
	for i, d := range collectResult.Directories {
		collectDirs[i], _ = filepath.Rel(root, d.Path)
	}
	collectFiles := make([]string, len(collectResult.Files))
	for i, f := range collectResult.Files {
		collectFiles[i], _ = filepath.Rel(root, f.Path)
	}

	sort.Strings(collectDirs)
	sort.Strings(collectFiles)

	if len(streamDirs) != len(collectDirs) {
		t.Errorf("dirs count mismatch: stream=%d (%v), collect=%d (%v)", len(streamDirs), streamDirs, len(collectDirs), collectDirs)
	}
	if len(streamFiles) != len(collectFiles) {
		t.Errorf("files count mismatch: stream=%d (%v), collect=%d (%v)", len(streamFiles), streamFiles, len(collectFiles), collectFiles)
	}

	for i := range collectDirs {
		if i < len(streamDirs) && streamDirs[i] != collectDirs[i] {
			t.Errorf("dir mismatch at %d: stream=%q, collect=%q", i, streamDirs[i], collectDirs[i])
		}
	}
	for i := range collectFiles {
		if i < len(streamFiles) && streamFiles[i] != collectFiles[i] {
			t.Errorf("file mismatch at %d: stream=%q, collect=%q", i, streamFiles[i], collectFiles[i])
		}
	}
}

// Test that sibling symlinks to the same target don't cause false cycle detection.
// This validates the depth-indexed ancestry map approach vs. a wrong global dedup.
func TestWalkStream_FollowSymlinks_SiblingNoCycle(t *testing.T) {
	root := t.TempDir()

	// root/a/sub/ with files
	os.MkdirAll(filepath.Join(root, "a", "sub"), 0755)
	os.WriteFile(filepath.Join(root, "a", "sub", "file.txt"), []byte("data"), 0644)

	// root/b/link -> root/a/sub (sibling link to previously visited dir)
	os.MkdirAll(filepath.Join(root, "b"), 0755)
	os.Symlink(filepath.Join(root, "a", "sub"), filepath.Join(root, "b", "link"))

	dirs, files := drainWalkStream(t, root, WalkOptions{
		IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
	})

	// b/link should NOT be falsely detected as a cycle
	if !contains(dirs, "b/link") {
		t.Errorf("expected b/link in dirs (sibling link, not a cycle), got: %v", dirs)
	}
	if !contains(files, "b/link/file.txt") {
		t.Errorf("expected b/link/file.txt in files, got: %v", files)
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
