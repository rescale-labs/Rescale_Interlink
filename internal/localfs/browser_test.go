package localfs

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"
)

// createTestTree creates a temporary directory tree for testing WalkStream.
func createTestTree(t *testing.T) string {
	t.Helper()
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
	return mkTree(t,
		[]string{"a", "a/sub", "a/sub/deep", "b"},
		[]string{
			"file0.txt", "a/file1.txt", "a/file2.txt",
			"a/sub/file3.txt", "a/sub/deep/file4.txt", "b/file5.txt",
		},
	)
}

// createReverseOrderTree builds a tree whose entries are created last-lexical
// first, so a walk that leaked the filesystem's own directory-entry order would
// produce a sequence other than the lexical one asserted below.
func createReverseOrderTree(t *testing.T) string {
	t.Helper()
	return mkTree(t,
		[]string{"zdir", "adir/zsub", "adir/asub"},
		[]string{
			"zdir/zfile.txt", "zdir/afile.txt", "mfile.txt",
			"adir/zsub/f.txt", "adir/asub/f.txt",
		},
	)
}

// mkTree builds a fresh temp tree: every path in dirs is created as a
// directory, and every path in files as a non-empty regular file (its own
// relative name is the content). Paths are slash-relative to the tree root,
// which is returned. Files may name parents not listed in dirs.
func mkTree(t *testing.T, dirs, files []string) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// addSymlinks creates one symlink per {target, link} pair, both slash-relative
// to root. An absolute target is used verbatim, which is how a row points a
// link outside the tree.
func addSymlinks(t *testing.T, root string, links [][2]string) {
	t.Helper()
	for _, link := range links {
		target := link[0]
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		if err := os.Symlink(target, filepath.Join(root, link[1])); err != nil {
			t.Fatalf("symlink %s -> %s: %v", link[1], target, err)
		}
	}
}

// drainWalkStreamInOrder collects all entries from WalkStream, preserving each
// channel's own arrival order.
//
// Each channel gets its own goroutine. A single select loop over both could not
// distinguish the walker's send order from the pseudo-random pick select makes
// when both channels are ready, and draining them one after the other would
// deadlock as soon as the channel not being read filled its buffer.
func drainWalkStreamInOrder(t *testing.T, root string, opts WalkOptions) (dirs, files []string) {
	t.Helper()
	ctx := context.Background()
	dirChan, fileChan, _, errChan := WalkStream(ctx, root, opts)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for entry := range dirChan {
			rel, _ := filepath.Rel(root, entry.Path)
			dirs = append(dirs, rel)
		}
	}()
	go func() {
		defer wg.Done()
		for entry := range fileChan {
			rel, _ := filepath.Rel(root, entry.Path)
			files = append(files, rel)
		}
	}()
	wg.Wait()

	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("unexpected walk error: %v", err)
		}
	default:
	}

	return dirs, files
}

// drainWalkStream collects all entries from WalkStream channels into sorted
// slices of paths relative to root.
func drainWalkStream(t *testing.T, root string, opts WalkOptions) (dirs, files []string) {
	t.Helper()
	dirs, files = drainWalkStreamInOrder(t, root, opts)
	sort.Strings(dirs)
	sort.Strings(files)
	return dirs, files
}

// walkCollectRel runs WalkCollect and returns its three result slices as sorted
// paths relative to root, so they can be compared with drainWalkStream's.
func walkCollectRel(t *testing.T, root string, opts WalkOptions) (dirs, files, symlinks []string) {
	t.Helper()
	result, err := WalkCollect(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	rel := func(entries []FileEntry) []string {
		paths := make([]string, len(entries))
		for i, e := range entries {
			paths[i], _ = filepath.Rel(root, e.Path)
		}
		sort.Strings(paths)
		return paths
	}
	return rel(result.Directories), rel(result.Files), rel(result.Symlinks)
}

func TestWalkStream_BasicTraversal(t *testing.T) {
	root := createTestTree(t)

	dirs, files := drainWalkStream(t, root, WalkOptions{
		IncludeHidden:  true,
		SkipHiddenDirs: false,
	})

	wantDirs := []string{"a", "a/sub", "a/sub/deep", "b"}
	wantFiles := []string{
		"a/file1.txt", "a/file2.txt", "a/sub/deep/file4.txt",
		"a/sub/file3.txt", "b/file5.txt", "file0.txt",
	}
	if !slices.Equal(dirs, wantDirs) {
		t.Errorf("dirs: got %v, want %v", dirs, wantDirs)
	}
	if !slices.Equal(files, wantFiles) {
		t.Errorf("files: got %v, want %v", files, wantFiles)
	}
}

func TestWalkStream_ContextCancellation(t *testing.T) {
	root := createTestTree(t)
	ctx, cancel := context.WithCancel(context.Background())

	dirChan, fileChan, _, errChan := WalkStream(ctx, root, WalkOptions{
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
	root := mkTree(t,
		[]string{"visible", ".hidden_dir"},
		[]string{"visible.txt", ".hidden_file", ".hidden_dir/inside.txt"},
	)

	dirs, files := drainWalkStream(t, root, WalkOptions{
		IncludeHidden:  false,
		SkipHiddenDirs: true,
	})

	// Should only see visible items
	if !slices.Equal(dirs, []string{"visible"}) {
		t.Errorf("dirs: got %v, want [visible]", dirs)
	}
	if !slices.Equal(files, []string{"visible.txt"}) {
		t.Errorf("files: got %v, want [visible.txt]", files)
	}
}

func TestWalkStream_EmptyDirectory(t *testing.T) {
	dirs, files := drainWalkStream(t, t.TempDir(), WalkOptions{IncludeHidden: true})

	if len(dirs)+len(files) != 0 {
		t.Errorf("expected 0 entries for empty dir, got dirs=%v files=%v", dirs, files)
	}
}

// TestWalkStream_PerChannelOrdering pins the ordering WalkStream actually
// provides: each channel on its own delivers entries in filepath.WalkDir's
// lexical, parent-before-child order. Pinning the exact sequences also pins the
// walk as independent of the order the OS hands back directory entries, since
// WalkDir reads through os.ReadDir, which sorts — hence the second tree, whose
// entries are created in reverse lexical order.
//
// There is deliberately no assertion that a directory arrives before the files
// inside it. dirChan and fileChan are separate channels, so that order is not
// observable by a consumer reading both, and no consumer needs it: the
// folder-upload orchestrator buffers files whose parent folder is not yet mapped.
func TestWalkStream_PerChannelOrdering(t *testing.T) {
	tests := []struct {
		name      string
		makeTree  func(*testing.T) string
		wantDirs  []string
		wantFiles []string
	}{
		{
			name:     "lexical_tree",
			makeTree: createTestTree,
			wantDirs: []string{"a", "a/sub", "a/sub/deep", "b"},
			wantFiles: []string{
				"a/file1.txt", "a/file2.txt", "a/sub/deep/file4.txt",
				"a/sub/file3.txt", "b/file5.txt", "file0.txt",
			},
		},
		{
			name:     "reverse_created_tree",
			makeTree: createReverseOrderTree,
			wantDirs: []string{"adir", "adir/asub", "adir/zsub", "zdir"},
			wantFiles: []string{
				"adir/asub/f.txt", "adir/zsub/f.txt", "mfile.txt",
				"zdir/afile.txt", "zdir/zfile.txt",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.makeTree(t)
			dirs, files := drainWalkStreamInOrder(t, root, WalkOptions{IncludeHidden: true})

			if !slices.Equal(dirs, tc.wantDirs) {
				t.Errorf("dirChan order:\n got %v\nwant %v", dirs, tc.wantDirs)
			}
			if !slices.Equal(files, tc.wantFiles) {
				t.Errorf("fileChan order:\n got %v\nwant %v", files, tc.wantFiles)
			}

			// The property CreateFolderStructureStreaming depends on: a directory
			// never arrives before the parent it will be created under, and every
			// file's parent is announced on dirChan at some point.
			seen := map[string]bool{".": true}
			for _, d := range dirs {
				if parent := filepath.Dir(d); !seen[parent] {
					t.Errorf("directory %q arrived before its parent %q; order: %v", d, parent, dirs)
				}
				if seen[d] {
					t.Errorf("directory %q emitted twice; order: %v", d, dirs)
				}
				seen[d] = true
			}
			for _, f := range files {
				if parent := filepath.Dir(f); !seen[parent] {
					t.Errorf("file %q has no dirChan entry for its parent %q; dirs: %v", f, parent, dirs)
				}
			}
		})
	}
}

// TestWalkStream_WalkCollectConsistency pins WalkStream and WalkCollect to the
// same entry set for the same options, with and without symlink following.
func TestWalkStream_WalkCollectConsistency(t *testing.T) {
	tests := []struct {
		name  string
		links [][2]string
		opts  WalkOptions
	}{
		{
			name: "no_symlinks",
			opts: WalkOptions{IncludeHidden: true, SkipHiddenDirs: true},
		},
		{
			name:  "following_symlinks",
			links: [][2]string{{"b", "link_to_b"}, {"file0.txt", "link_file.txt"}},
			opts:  WalkOptions{IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := createTestTree(t)
			addSymlinks(t, root, tc.links)

			collectDirs, collectFiles, _ := walkCollectRel(t, root, tc.opts)
			streamDirs, streamFiles := drainWalkStream(t, root, tc.opts)

			if !slices.Equal(streamDirs, collectDirs) {
				t.Errorf("dirs differ:\n stream %v\ncollect %v", streamDirs, collectDirs)
			}
			if !slices.Equal(streamFiles, collectFiles) {
				t.Errorf("files differ:\n stream %v\ncollect %v", streamFiles, collectFiles)
			}
		})
	}
}

// TestWalkStream_FollowSymlinks covers what a followed link contributes to the
// walk. Every row builds a tree, adds its links, and drains WalkStream with the
// same options bar FollowSymlinks; the wants are membership claims, since a row
// only cares about the aliased entries.
func TestWalkStream_FollowSymlinks(t *testing.T) {
	tests := []struct {
		name        string
		tree        func(*testing.T) string // nil: createTestTree
		links       [][2]string
		follow      bool
		wantDirs    []string
		wantFiles   []string
		absentDirs  []string
		absentFiles []string
	}{
		{
			name:      "symlinked_dir",
			links:     [][2]string{{"b", "link_to_b"}},
			follow:    true,
			wantDirs:  []string{"link_to_b"},
			wantFiles: []string{"b/file5.txt", "link_to_b/file5.txt"},
		},
		{
			name:      "symlinked_file",
			links:     [][2]string{{"file0.txt", "a/link_file.txt"}},
			follow:    true,
			wantFiles: []string{"a/link_file.txt"},
		},
		{
			// A broken symlink is silently skipped; real entries still arrive.
			name:        "broken_symlink",
			tree:        func(t *testing.T) string { return mkTree(t, nil, []string{"file.txt"}) },
			links:       [][2]string{{"/nonexistent/path/that/does/not/exist", "broken_link"}},
			follow:      true,
			wantFiles:   []string{"file.txt"},
			absentDirs:  []string{"broken_link"},
			absentFiles: []string{"broken_link"},
		},
		{
			name:        "not_following_skips_links",
			links:       [][2]string{{"b", "link_to_b"}, {"file0.txt", "link_file.txt"}},
			follow:      false,
			absentDirs:  []string{"link_to_b"},
			absentFiles: []string{"link_file.txt"},
		},
		{
			// Entries from the aliased walk keep the alias prefix: "link/...",
			// never the resolved "a/sub/...".
			name:      "path_rewriting",
			links:     [][2]string{{"a/sub", "link"}},
			follow:    true,
			wantDirs:  []string{"link", "link/deep"},
			wantFiles: []string{"link/file3.txt", "link/deep/file4.txt"},
		},
		{
			// BOTH aliases produce entries (ancestry stack, not global dedup).
			name:      "duplicate_aliases",
			tree:      func(t *testing.T) string { return mkTree(t, nil, []string{"shared/data.txt"}) },
			links:     [][2]string{{"shared", "linkA"}, {"shared", "linkB"}},
			follow:    true,
			wantFiles: []string{"linkA/data.txt", "linkB/data.txt", "shared/data.txt"},
		},
		{
			name:     "alias_root_dir_entry",
			links:    [][2]string{{"a/sub", "link_to_sub"}},
			follow:   true,
			wantDirs: []string{"link_to_sub"},
		},
		{
			// A sibling link to an already-visited directory is not a cycle. This
			// validates the depth-indexed ancestry map against a wrong global dedup.
			name:      "sibling_no_cycle",
			tree:      func(t *testing.T) string { return mkTree(t, []string{"b"}, []string{"a/sub/file.txt"}) },
			links:     [][2]string{{"a/sub", "b/link"}},
			follow:    true,
			wantDirs:  []string{"b/link"},
			wantFiles: []string{"b/link/file.txt"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			makeTree := tc.tree
			if makeTree == nil {
				makeTree = createTestTree
			}
			root := makeTree(t)
			addSymlinks(t, root, tc.links)

			dirs, files := drainWalkStream(t, root, WalkOptions{
				IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: tc.follow,
			})

			for _, want := range tc.wantDirs {
				if !slices.Contains(dirs, want) {
					t.Errorf("expected %q in dirs, got: %v", want, dirs)
				}
			}
			for _, want := range tc.wantFiles {
				if !slices.Contains(files, want) {
					t.Errorf("expected %q in files, got: %v", want, files)
				}
			}
			for _, absent := range tc.absentDirs {
				if slices.Contains(dirs, absent) {
					t.Errorf("did not expect %q in dirs, got: %v", absent, dirs)
				}
			}
			for _, absent := range tc.absentFiles {
				if slices.Contains(files, absent) {
					t.Errorf("did not expect %q in files, got: %v", absent, files)
				}
			}
		})
	}
}

// TestWalkStream_FollowSymlinks_NoHang covers the links that would walk forever
// without cycle detection. The only assertion is that the walk finishes.
func TestWalkStream_FollowSymlinks_NoHang(t *testing.T) {
	tests := []struct {
		name  string
		tree  func(*testing.T) string
		links [][2]string
	}{
		{
			name:  "cycle_to_ancestor",
			tree:  func(t *testing.T) string { return mkTree(t, nil, []string{"a/file.txt"}) },
			links: [][2]string{{"a", "a/loop"}},
		},
		{
			name:  "self_reference_to_root",
			tree:  func(t *testing.T) string { return mkTree(t, nil, []string{"file.txt"}) },
			links: [][2]string{{".", "self"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.tree(t)
			addSymlinks(t, root, tc.links)

			done := make(chan struct{})
			go func() {
				defer close(done)
				drainWalkStream(t, root, WalkOptions{
					IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
				})
			}()

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("walk did not complete within timeout")
			}
		})
	}
}

func TestWalkCollect_FollowSymlinks(t *testing.T) {
	root := createTestTree(t)
	addSymlinks(t, root, [][2]string{{"b", "link_to_b"}})

	dirs, files, symlinks := walkCollectRel(t, root, WalkOptions{
		IncludeHidden: true, SkipHiddenDirs: true, FollowSymlinks: true,
	})

	if !slices.Contains(dirs, "link_to_b") {
		t.Errorf("expected link_to_b in dirs, got: %v", dirs)
	}
	if !slices.Contains(files, "link_to_b/file5.txt") {
		t.Errorf("expected link_to_b/file5.txt in files, got: %v", files)
	}
	// Symlinks slice should be empty when following.
	if len(symlinks) != 0 {
		t.Errorf("expected 0 symlinks when following, got: %v", symlinks)
	}
}

func TestShouldProbeResolvedDirectory(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
		dir  bool
		want bool
	}{
		{name: "regular_file", mode: 0o644, want: false},
		{name: "directory", mode: os.ModeDir | 0o755, dir: true, want: false},
		{name: "irregular_file", mode: os.ModeIrregular, want: true},
		{name: "named_pipe", mode: os.ModeNamedPipe, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldProbeResolvedDirectory(tc.mode, tc.dir)
			if got != tc.want {
				t.Fatalf("shouldProbeResolvedDirectory(%v, %v) = %v, want %v", tc.mode, tc.dir, got, tc.want)
			}
		})
	}
}

// TestWalkStream_SkippedChannelDrainsCleanly verifies that the streaming
// walker's "skipped" channel is closed when the walk completes, even when
// no entries are emitted onto it. Regression guard for the orchestrator's
// drain loop hanging on a non-closed channel.
func TestWalkStream_SkippedChannelDrainsCleanly(t *testing.T) {
	root := createTestTree(t)
	ctx := context.Background()

	dirChan, fileChan, skippedChan, errChan := WalkStream(ctx, root, WalkOptions{
		IncludeHidden: true,
	})

	// Drain dirs/files so the walker can complete.
	dirsDone, filesDone := false, false
	for !dirsDone || !filesDone {
		select {
		case _, ok := <-dirChan:
			if !ok {
				dirsDone = true
			}
		case _, ok := <-fileChan:
			if !ok {
				filesDone = true
			}
		}
	}

	// skippedChan should now close (no junctions in this tree).
	count := 0
	for entry := range skippedChan {
		count++
		if entry.Path == "" {
			t.Errorf("skipped entry has empty path")
		}
	}
	if count != 0 {
		t.Errorf("expected 0 skipped entries on a clean tree, got %d", count)
	}

	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	default:
	}
}

// TestWalkCollect_SymlinkSliceContainsBrokenSymlinks verifies the existing
// Symlinks-slice behavior. On Unix, getDirIdentity always succeeds, so this
// test does not exercise the unidentifiable branch directly — but it documents
// the contract that the Symlinks slice is the surface for "we walked it but
// couldn't follow it" cases.
func TestWalkCollect_SymlinkSliceContainsBrokenSymlinks(t *testing.T) {
	root := mkTree(t, nil, []string{"real.txt"})
	// Symlink pointing at a non-existent target: Stat will fail.
	addSymlinks(t, root, [][2]string{{"missing.txt", "broken_link"}})

	result, err := WalkCollect(root, WalkOptions{
		IncludeHidden:  true,
		FollowSymlinks: false, // Without follow, all symlinks land in Symlinks.
	})
	if err != nil {
		t.Fatalf("WalkCollect: %v", err)
	}

	foundLink := false
	for _, e := range result.Symlinks {
		if filepath.Base(e.Path) == "broken_link" {
			foundLink = true
			if !e.IsSymlink {
				t.Errorf("broken_link entry IsSymlink=false, want true")
			}
		}
	}
	if !foundLink {
		t.Errorf("broken_link not in result.Symlinks: %+v", result.Symlinks)
	}
}
