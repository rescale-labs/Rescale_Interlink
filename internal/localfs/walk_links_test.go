package localfs

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"sync"
	"testing"
)

// linkTree builds <parent>/tree, the folder a walk starts at, with a file and a
// folder beside it that no link may pull in, and returns the tree's path. It
// holds one link of each kind a followed walk must handle:
//
//	loop        -> ..                  the tree's parent (outside, relative)
//	elsewhere   -> <parent>/elsewhere  a folder outside (absolute)
//	outside.txt -> <parent>/beside.txt a file outside
//	broken      -> missing             nothing
//	in          -> a                   a folder inside: followed
//	a/up        -> ..                  the root: a cycle, and again as in/up
//	a/b/self    -> .                   its own folder: a cycle, and again as in/b/self
func linkTree(t *testing.T) string {
	t.Helper()
	parent := mkTree(t, []string{"elsewhere"}, []string{"beside.txt", "elsewhere/far.txt", "tree/top.txt", "tree/a/file.txt", "tree/a/b/deep.txt"})
	root := filepath.Join(parent, "tree")
	for link, target := range map[string]string{
		"loop":        "..",
		"elsewhere":   filepath.Join(parent, "elsewhere"),
		"outside.txt": filepath.Join(parent, "beside.txt"),
		"broken":      "missing",
		"in":          "a",
		"a/up":        "..",
		"a/b/self":    ".",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatalf("symlink %s -> %s: %v", link, target, err)
		}
	}
	return root
}

// linkWalk is one walk's output as sorted root-relative paths, plus each
// skipped entry's reason keyed by its root-relative path.
type linkWalk struct {
	dirs, files []string
	skipped     map[string]string
}

// add records e under the list it belongs to; the walk sorts them at the end.
func (w *linkWalk) add(root string, list *[]string, e FileEntry) {
	rel, _ := filepath.Rel(root, e.Path)
	if list == nil {
		w.skipped[filepath.ToSlash(rel)] = e.SkipReason
		return
	}
	*list = append(*list, filepath.ToSlash(rel))
}

func streamLinkWalk(t *testing.T, root string) linkWalk {
	t.Helper()
	dirChan, fileChan, skippedChan, errChan := WalkStream(context.Background(), root, WalkOptions{IncludeHidden: true, FollowSymlinks: true})
	w := linkWalk{skipped: map[string]string{}}
	var wg sync.WaitGroup
	for ch, list := range map[<-chan FileEntry]*[]string{dirChan: &w.dirs, fileChan: &w.files, skippedChan: nil} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range ch {
				w.add(root, list, e)
			}
		}()
	}
	wg.Wait()
	if err := <-errChan; err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(w.dirs)
	sort.Strings(w.files)
	return w
}

func collectLinkWalk(t *testing.T, root string) linkWalk {
	t.Helper()
	result, err := WalkCollect(root, WalkOptions{IncludeHidden: true, FollowSymlinks: true})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	w := linkWalk{skipped: map[string]string{}}
	for list, entries := range map[*[]string][]FileEntry{&w.dirs: result.Directories, &w.files: result.Files, nil: result.Symlinks} {
		for _, e := range entries {
			w.add(root, list, e)
		}
	}
	sort.Strings(w.dirs)
	sort.Strings(w.files)
	return w
}

// TestWalk_FollowedLinksStayInsideRoot is the upload-dir escape: a link may
// bring in only what already lies inside the folder being walked, a link that
// leads back into its own ancestry is not walked again (also when reached
// through a followed link), and every link not followed is reported once, with
// its reason.
func TestWalk_FollowedLinksStayInsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(noDirLinksOnWindows)
	}
	want := linkWalk{
		dirs:  []string{"a", "a/b", "in", "in/b"},
		files: []string{"a/b/deep.txt", "a/file.txt", "in/b/deep.txt", "in/file.txt", "top.txt"},
		skipped: map[string]string{
			"loop":        skipOutside,
			"elsewhere":   skipOutside,
			"outside.txt": skipOutside,
			"broken":      skipBroken,
			"a/up":        skipCycle,
			"in/up":       skipCycle,
			"a/b/self":    skipCycle,
			"in/b/self":   skipCycle,
		},
	}
	for name, walk := range map[string]func(*testing.T, string) linkWalk{
		"WalkStream":  streamLinkWalk,
		"WalkCollect": collectLinkWalk,
	} {
		t.Run(name, func(t *testing.T) {
			got := walk(t, linkTree(t))
			if !slices.Equal(got.dirs, want.dirs) {
				t.Errorf("dirs:\n got %v\nwant %v", got.dirs, want.dirs)
			}
			if !slices.Equal(got.files, want.files) {
				t.Errorf("files:\n got %v\nwant %v", got.files, want.files)
			}
			if !maps.Equal(got.skipped, want.skipped) {
				t.Errorf("skipped:\n got %v\nwant %v", got.skipped, want.skipped)
			}
		})
	}
}
