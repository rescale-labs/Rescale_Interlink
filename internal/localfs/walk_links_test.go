package localfs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

// linkTree builds <parent>/tree, the folder a walk starts at, with a file
// beside it that no link may pull in, and <elsewhere>, a temp dir of its own
// that links lead out to. It returns the tree's path. The links are one of
// each kind a followed walk must handle (the two in wing by their walked path):
//
//	far.txt   -> <elsewhere>/far.txt      a file outside: followed
//	mesh      -> <elsewhere>/meshes/wing  a folder outside: followed
//	mesh/up   -> ..                       wing's parent, which contains wing: a loop
//	mesh/back -> <parent>                 the tree's parent: a loop
//	loop      -> ..                       the tree's parent: a loop
//	slash     -> /                        the file system's root: a loop
//	broken    -> missing                  nothing
//	in        -> a                        a folder inside, off the chain: followed
//	a/sib     -> ../c                     a sibling: followed, and again as in/sib
//	a/up      -> ..                       the root: a loop, and again as in/up
//	a/b/self  -> .                        its own folder: a loop, and again as in/b/self
func linkTree(t *testing.T) string {
	t.Helper()
	parent := mkTree(t, nil, []string{"beside.txt", "tree/top.txt", "tree/a/file.txt", "tree/a/b/deep.txt", "tree/c/sib.txt"})
	elsewhere := mkTree(t, nil, []string{"far.txt", "meshes/beside.txt", "meshes/wing/w.txt"})
	root, wing := filepath.Join(parent, "tree"), filepath.Join(elsewhere, "meshes", "wing")
	for dir, links := range map[string]map[string]string{
		root: {
			"far.txt": filepath.Join(elsewhere, "far.txt"), "mesh": wing, "loop": "..", "slash": "/",
			"broken": "missing", "in": "a", "a/sib": "../c", "a/up": "..", "a/b/self": ".",
		},
		wing: {"up": "..", "back": parent},
	} {
		for link, target := range links {
			if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
				t.Fatalf("symlink %s -> %s: %v", link, target, err)
			}
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

// maxLinkWalk bounds a streamed walk, so one that does not end, a loop not
// caught, fails at once rather than at the test timeout.
const maxLinkWalk = 1000

func streamLinkWalk(t *testing.T, root string) linkWalk {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dirChan, fileChan, skippedChan, errChan := WalkStream(ctx, root, WalkOptions{IncludeHidden: true, FollowSymlinks: true})
	w := linkWalk{skipped: map[string]string{}}
	var seen atomic.Int64
	var wg sync.WaitGroup
	for ch, list := range map[<-chan FileEntry]*[]string{dirChan: &w.dirs, fileChan: &w.files, skippedChan: nil} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range ch {
				if seen.Add(1) > maxLinkWalk {
					cancel()
				} else {
					w.add(root, list, e)
				}
			}
		}()
	}
	wg.Wait()
	if seen.Load() > maxLinkWalk {
		t.Fatalf("walk did not end within %d entries", maxLinkWalk)
	}
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
	return linksOf(root, result)
}

// linksOf is a walk's result as a linkWalk.
func linksOf(root string, result *WalkCollectResult) linkWalk {
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

// TestWalk_FollowsLinksButNotLoops is the upload-dir link walk: a link is
// followed wherever it points, a link to a folder that is on the chain being
// walked, or contains one, is a loop and left out (also when reached through a
// followed link), and every link not followed is reported once, with its reason.
func TestWalk_FollowsLinksButNotLoops(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(noDirLinksOnWindows)
	}
	want := linkWalk{
		dirs: []string{"a", "a/b", "a/sib", "c", "in", "in/b", "in/sib", "mesh"},
		files: []string{"a/b/deep.txt", "a/file.txt", "a/sib/sib.txt", "c/sib.txt", "far.txt",
			"in/b/deep.txt", "in/file.txt", "in/sib/sib.txt", "mesh/w.txt", "top.txt"},
		skipped: map[string]string{
			"loop":      skipCycle,
			"slash":     skipCycle,
			"broken":    skipBroken,
			"mesh/up":   skipCycle,
			"mesh/back": skipCycle,
			"a/up":      skipCycle,
			"in/up":     skipCycle,
			"a/b/self":  skipCycle,
			"in/b/self": skipCycle,
		},
	}
	// WalkStream first: it stops at maxLinkWalk entries, and WalkCollect,
	// which cannot be stopped, runs only if WalkStream passed.
	for i, walk := range []func(*testing.T, string) linkWalk{streamLinkWalk, collectLinkWalk} {
		if !t.Run([]string{"WalkStream", "WalkCollect"}[i], func(t *testing.T) {
			if got := walk(t, linkTree(t)); !reflect.DeepEqual(got, want) {
				t.Errorf("\n got %+v\nwant %+v", got, want)
			}
		}) {
			break
		}
	}
}

// TestWalk_FolderOnChainThroughAliasIsLoop covers a loop no link shows: a mount
// alias or firmlink puts a folder the walk is inside at a second path (a link
// to /System/Volumes/Data reaches /private that way), so a followed walk meets
// it as a plain folder. It is left out as a loop. An alias cannot be made under
// a temp dir, so the chain here holds sub as if one led to it.
func TestWalk_FolderOnChainThroughAliasIsLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(noDirLinksOnWindows)
	}
	target := mkTree(t, nil, []string{"y.txt", "sub/x.txt"})
	info, err := os.Stat(filepath.Join(target, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	aliased := newAncestryMap()
	aliased.entries[0], _ = getDirIdentity(info)
	link, opts := filepath.Join(t.TempDir(), "link"), WalkOptions{IncludeHidden: true}

	var streamed, collected WalkCollectResult
	dirs, files, skipped := make(chan FileEntry, 9), make(chan FileEntry, 9), make(chan FileEntry, 9)
	_ = walkSymlinkedDir(context.Background(), target, link, opts, aliased.below(target), dirs, files, skipped)
	for list, ch := range map[*[]FileEntry]chan FileEntry{&streamed.Directories: dirs, &streamed.Files: files, &streamed.Symlinks: skipped} {
		close(ch)
		for e := range ch {
			*list = append(*list, e)
		}
	}
	_ = collectSymlinkedDir(target, link, opts, aliased.below(target), &collected)

	want := linkWalk{files: []string{"y.txt"}, skipped: map[string]string{"sub": skipCycle}}
	for name, got := range map[string]linkWalk{"walkSymlinkedDir": linksOf(link, &streamed), "collectSymlinkedDir": linksOf(link, &collected)} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
		}
	}
}
