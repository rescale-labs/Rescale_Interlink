// Package localfs provides local filesystem abstractions for shared use by CLI and GUI.
package localfs

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ancestryMap tracks the chain of real directory identities from root
// to the current walk position. It uses a depth-indexed map: when entering a
// directory at depth D, entries at depth >= D are removed (they belong to a
// previous sibling branch). This ensures only true ancestors are tracked,
// not previously-visited siblings — preventing false cycle detection.
// Negative depths hold every folder that contains the root or a followed
// link's target (see below), since a link into one would repeat the walk too.
//
// Example: walking root/a/sub then root/b/link -> root/a/sub:
//   - At root/a: map = {0: root, 1: a}
//   - At root/a/sub: map = {0: root, 1: a, 2: sub}
//   - At root/b: trim >= 1 → map = {0: root}, then add {1: b}
//   - At root/b/link -> root/a/sub: check if a/sub is in map → NO (only root, b) → not a cycle ✓
type ancestryMap struct {
	entries map[int]dirIdentity
}

func newAncestryMap() *ancestryMap {
	return &ancestryMap{entries: make(map[int]dirIdentity)}
}

// trimTo removes all entries at depth >= d. Used before a link or a directory
// at depth d is checked or recorded, so that sibling real directories don't
// cause false cycle detection.
func (a *ancestryMap) trimTo(depth int) {
	for d := range a.entries {
		if d >= depth {
			delete(a.entries, d)
		}
	}
}

// contains reports whether id is on the chain: a folder the walk is inside, or
// one that contains it.
func (a *ancestryMap) contains(id dirIdentity) bool {
	for _, existing := range a.entries {
		if existing == id {
			return true
		}
	}
	return false
}

// below returns the chain for a walk that starts at dir, a real path: dir at
// depth 0, then each folder that contains dir, then the current chain, at
// depths the new walk never trims. A link to any of them would walk dir, or a
// folder on the chain, again. An empty dir (a root that did not resolve) adds
// no folders. The caller's chain is not changed.
func (a *ancestryMap) below(dir string) *ancestryMap {
	child := newAncestryMap()
	for parent := ""; dir != parent; dir, parent = filepath.Dir(dir), dir {
		if info, err := os.Stat(dir); err == nil {
			if id, ok := getDirIdentity(info); ok {
				child.entries[-len(child.entries)] = id
			}
		}
	}
	for _, id := range a.entries {
		child.entries[-len(child.entries)] = id
	}
	return child
}

// realPath is p made absolute, with every link in it resolved.
func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// followLink resolves a link met at depth by a walk that follows links, to
// wherever it points. It returns the target's real path and Stat info.
// Otherwise reason says why the link is left out: it is broken, or its target
// is a directory on the chain being walked, or one containing it, which would
// walk that directory again.
func followLink(path string, ancestry *ancestryMap, depth int) (target string, info os.FileInfo, reason string) {
	target, err := realPath(path)
	if err == nil {
		info, err = os.Stat(target)
	}
	switch {
	case err != nil:
		return "", nil, skipBroken
	case !info.IsDir():
		return target, info, ""
	}
	id, ok := getDirIdentity(info)
	if !ok {
		return "", info, skipNoDirLink
	}
	ancestry.trimTo(depth)
	if ancestry.contains(id) {
		return "", info, skipCycle
	}
	return target, info, ""
}

// skippedLink is the entry a walk reports for a link it leaves out. target is
// the link's Stat info, nil when the link is broken.
func skippedLink(path string, target os.FileInfo, reason string) FileEntry {
	return FileEntry{
		Path: path, Name: filepath.Base(path), IsDir: target != nil && target.IsDir(),
		IsSymlink: true, SkipReason: reason,
	}
}

// emitSkipped non-blockingly delivers an entry to the streaming-walk
// "skipped" channel, dropping the entry if the channel is full or the
// caller has cancelled. The channel is informational; missing one
// notification is preferable to stalling a long-running walk.
func emitSkipped(ctx context.Context, skipped chan<- FileEntry, entry FileEntry) {
	if skipped == nil {
		return
	}
	select {
	case skipped <- entry:
	case <-ctx.Done():
	default:
		// Channel full — drop the notification.
	}
}

func shouldProbeResolvedDirectory(mode fs.FileMode, isDir bool) bool {
	// Windows junctions can appear as non-directories with irregular mode bits.
	// Regular files should stay on the hot path without an extra Stat call.
	return !isDir && !mode.IsRegular()
}

// FileEntry represents a file or directory in the local filesystem.
type FileEntry struct {
	Path      string      // Full path to the file
	Name      string      // Base name of the file
	Size      int64       // Size in bytes (0 for directories, target size for symlinks)
	IsDir     bool        // True if this is a directory (or symlink to directory)
	ModTime   time.Time   // Last modification time
	Mode      fs.FileMode // File mode/permissions
	IsSymlink bool        // True if this is a symbolic link

	// SkipReason says why a walk that follows links left this entry out.
	SkipReason string
}

// The reasons a walk that follows links gives for one it does not follow.
const (
	skipBroken    = "its target is missing or cannot be read"
	skipCycle     = "it leads back into a folder that contains it"
	skipNoDirLink = "links to folders are not followed on Windows"
)

// entryInfo is a helper struct for ListDirectoryEx internal use.
type entryInfo struct {
	entry     os.DirEntry
	fullPath  string
	info      os.FileInfo
	isSymlink bool
	index     int
}

// ListDirectoryExOptions configures the behavior of ListDirectoryEx.
type ListDirectoryExOptions struct {
	// IncludeHidden includes hidden files (starting with .) in results.
	IncludeHidden bool

	// ResolveSymlinks controls whether symlinks are resolved to get target info.
	// When true, IsDir/Size/ModTime reflect the target; when false, they reflect the link.
	ResolveSymlinks bool

	// SymlinkWorkers is the number of parallel workers for symlink resolution.
	// Only used when ResolveSymlinks is true. Default is 8 if <= 0.
	SymlinkWorkers int

	// Timeout is the maximum duration for the directory read operation.
	// Zero means no timeout (use context deadline instead).
	Timeout time.Duration
}

// ListDirectoryEx returns the contents of a directory with extended options.
//
// This function is the shared implementation for both CLI and GUI directory listing.
// It handles:
//   - Context cancellation and timeout
//   - Hidden file filtering
//   - Symlink detection and optional resolution
//   - Parallel symlink resolution when ResolveSymlinks is true
func ListDirectoryEx(ctx context.Context, path string, opts ListDirectoryExOptions) ([]FileEntry, error) {
	// Apply timeout if specified
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	// Read directory in a goroutine for timeout protection
	type readResult struct {
		entries []os.DirEntry
		err     error
	}
	resultChan := make(chan readResult, 1)

	go func() {
		entries, err := os.ReadDir(path)
		resultChan <- readResult{entries: entries, err: err}
	}()

	// Wait for result or context cancellation
	var entries []os.DirEntry
	select {
	case result := <-resultChan:
		if result.err != nil {
			return nil, result.err
		}
		entries = result.entries
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// First pass: filter entries and build FileEntry slice
	var filtered []entryInfo
	var symlinkIndices []int

	for _, entry := range entries {
		name := entry.Name()

		// Filter hidden files
		if !opts.IncludeHidden && IsHiddenName(name) {
			continue
		}

		fullPath := filepath.Join(path, name)

		// Get file info (uses cached info from DirEntry, fast)
		info, err := entry.Info()
		if err != nil {
			// Skip entries we can't stat
			continue
		}

		// Check if symlink
		isSymlink := entry.Type()&os.ModeSymlink != 0

		ei := entryInfo{
			entry:     entry,
			fullPath:  fullPath,
			info:      info,
			isSymlink: isSymlink,
			index:     len(filtered),
		}

		if isSymlink {
			symlinkIndices = append(symlinkIndices, ei.index)
		}

		filtered = append(filtered, ei)
	}

	// Second pass: parallel symlink resolution if requested
	if opts.ResolveSymlinks && len(symlinkIndices) > 0 {
		resolveSymlinksParallel(ctx, filtered, symlinkIndices, opts.SymlinkWorkers)
	}

	// Build result
	result := make([]FileEntry, len(filtered))
	for i, ei := range filtered {
		isDir := ei.entry.IsDir()
		size := ei.info.Size()
		modTime := ei.info.ModTime()

		// For resolved symlinks, use the target info
		if ei.isSymlink && opts.ResolveSymlinks && ei.info != nil {
			isDir = ei.info.IsDir()
			size = ei.info.Size()
			modTime = ei.info.ModTime()
		}

		result[i] = FileEntry{
			Path:      ei.fullPath,
			Name:      ei.entry.Name(),
			Size:      size,
			IsDir:     isDir,
			ModTime:   modTime,
			Mode:      ei.info.Mode(),
			IsSymlink: ei.isSymlink,
		}
	}

	return result, nil
}

// resolveSymlinksParallel resolves symlinks in parallel using a worker pool.
// Updates the info field of entryInfo in-place for symlinks.
func resolveSymlinksParallel(ctx context.Context, entries []entryInfo, symlinkIndices []int, workerCount int) {
	if len(symlinkIndices) == 0 {
		return
	}

	// Default worker count
	if workerCount <= 0 {
		workerCount = 8
	}
	if len(symlinkIndices) < workerCount {
		workerCount = len(symlinkIndices)
	}

	// Create job channel
	jobs := make(chan int, len(symlinkIndices))
	for _, idx := range symlinkIndices {
		jobs <- idx
	}
	close(jobs)

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case idx, ok := <-jobs:
					if !ok {
						return
					}
					// Resolve symlink target with os.Stat (follows symlinks)
					info, err := os.Stat(entries[idx].fullPath)
					if err == nil {
						entries[idx].info = info
					}
					// On error, keep original cached info (shows as broken symlink)
				}
			}
		}()
	}

	wg.Wait()
}

// WalkStream walks a directory tree and streams entries through separate channels
// for directories and files. Unlike WalkCollect which returns all results at once,
// WalkStream enables pipelined processing where folder creation can begin while
// the walk is still discovering files.
//
// Avoids loading all files into memory before uploads start. It is the same
// walk as WalkCollect (see walker), so both see the same entries.
//
// Ordering is guaranteed per channel, never across channels. filepath.WalkDir
// visits entries in lexical order, parents before children, and reads through
// os.ReadDir, which sorts — so dirChan delivers every directory after the parent
// it will be created under, whatever order the filesystem itself returns entries
// in. CreateFolderStructureStreaming relies on that to keep its pending-parent
// buffer small.
//
// dirChan and fileChan are independent, so a consumer reading both concurrently
// observes an arbitrary interleaving of the two and must not assume a directory
// arrives before the files inside it. The folder-upload orchestrator buffers
// files whose parent folder is not mapped yet for exactly this reason.
//
// All channels are closed when the walk completes. Errors are sent to errChan
// (buffered at 1). Context cancellation stops the walk and closes all channels.
//
// skippedChan receives entries that the walker chose not to descend into and
// not to emit as a file or directory: links it does not follow (see
// followLink) and Windows reparse-point junctions. Each entry has
// IsSymlink=true, IsDir reflecting the target type when there is a target, and
// a SkipReason. Consumers should report these to the user so that "missing
// files" cases are visible rather than silent.
func WalkStream(ctx context.Context, root string, opts WalkOptions) (
	dirChan <-chan FileEntry,
	fileChan <-chan FileEntry,
	skippedChan <-chan FileEntry,
	errChan <-chan error,
) {
	dirs := make(chan FileEntry, 1000)
	files := make(chan FileEntry, 1000)
	skipped := make(chan FileEntry, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(dirs)
		defer close(files)
		defer close(skipped)
		defer close(errs)

		w := walker{ctx: ctx, opts: opts, emit: sendTo(ctx, dirs, files, skipped)}
		if err := w.walkRoot(root); err != nil && ctx.Err() == nil {
			errs <- err
		}
	}()

	return dirs, files, skipped, errs
}

// sendTo is WalkStream's sink: a directory or a file waits for room on its
// channel, and an entry left out goes through emitSkipped.
func sendTo(ctx context.Context, dirs, files, skipped chan<- FileEntry) func(FileEntry) error {
	return func(e FileEntry) error {
		ch := files
		switch {
		case e.SkipReason != "":
			emitSkipped(ctx, skipped, e)
			return nil
		case e.IsDir:
			ch = dirs
		}
		select {
		case ch <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// walker is the one walk behind WalkStream and WalkCollect. It follows links
// to files and directories wherever they point, except a link to a directory
// the walk is inside, or to one containing it, which would walk it again; it
// reports every entry it leaves out, with a SkipReason. Cycle detection uses
// device+inode ancestry tracking on Unix. On Windows, links to directories are
// NOT followed (getDirIdentity returns false). Unless opts.IncludeHidden is
// set, hidden entries are left out and a hidden directory is not walked into.
type walker struct {
	ctx  context.Context
	opts WalkOptions
	emit func(FileEntry) error // a directory, a file, or an entry left out
}

// walkRoot walks root, whose chain starts with its real path and every
// directory that contains it (ancestryMap.below).
func (w walker) walkRoot(root string) error {
	realRoot, _ := realPath(root)
	return w.walk(root, root, newAncestryMap().below(realRoot), false)
}

// walk walks dir and reports each entry under shown, the path the caller sees
// for dir: the root itself, or the link a followed walk came through, so that
// the orchestrator builds the remote folder structure under the link's name.
// ancestry is this walk's own chain, which it extends with every directory it
// enters. A linked walk also leaves out a directory already on the chain: a
// mount alias or firmlink leads back that way where no link's real path shows
// it. The root's own walk only records its directories: checking them would
// change uploads from a tree holding a bind mount, or a file system that reuses
// inode numbers.
func (w walker) walk(dir, shown string, ancestry *ancestryMap, linked bool) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil // Skip inaccessible entries, and dir itself
		}
		if err := w.ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		shownPath, name := filepath.Join(shown, rel), d.Name()

		if !w.opts.IncludeHidden && IsHiddenName(name) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if symlink using Lstat (doesn't follow symlinks)
		info, err := os.Lstat(path)
		if err != nil {
			return nil // Skip entries we can't stat
		}
		depth := strings.Count(rel, string(filepath.Separator)) + 1 // dir is depth 0

		if info.Mode()&os.ModeSymlink != 0 {
			target, realInfo, reason := followLink(path, ancestry, depth)
			if reason != "" {
				return w.emit(skippedLink(shownPath, realInfo, reason))
			}
			if realInfo.IsDir() {
				// A directory entry for the link itself, then its target's
				// entries under the link's path.
				if err := w.emit(FileEntry{Path: shownPath, Name: name, Size: realInfo.Size(), IsDir: true,
					ModTime: realInfo.ModTime(), Mode: realInfo.Mode()}); err != nil {
					return err
				}
				_ = w.walk(target, shownPath, ancestry.below(target), true)
				return nil // not SkipDir: d.IsDir() is false for a link
			}
			info = realInfo // Symlinked file — use real info for size/modtime
		}

		// Defensive: an entry not classified as a directory by Lstat may
		// still resolve to a directory through Stat — e.g. a Windows
		// reparse-point junction whose Lstat mode lacks ModeSymlink (some
		// legacy junctions are tagged ModeIrregular instead). Emitting it
		// as a file would propagate to UploadFile and fail with
		// "cannot upload a directory". Detect, skip, and surface.
		if shouldProbeResolvedDirectory(info.Mode(), d.IsDir()) {
			if realInfo, statErr := os.Stat(path); statErr == nil && realInfo.IsDir() {
				return w.emit(skippedLink(shownPath, realInfo, skipNoDirLink))
			}
		}

		if d.IsDir() {
			if id, ok := getDirIdentity(info); ok {
				if ancestry.trimTo(depth); linked && ancestry.contains(id) {
					_ = w.emit(skippedLink(shownPath, info, skipCycle))
					return filepath.SkipDir
				}
				ancestry.entries[depth] = id
			}
		}
		return w.emit(FileEntry{Path: shownPath, Name: name, Size: info.Size(), IsDir: d.IsDir(),
			ModTime: info.ModTime(), Mode: info.Mode()})
	})
}

// WalkCollectResult contains the categorized results of WalkCollect.
type WalkCollectResult struct {
	Directories []FileEntry // All directories found
	Files       []FileEntry // All regular files found
	Symlinks    []FileEntry // The entries the walk left out, each with its SkipReason
}

// WalkCollect walks a directory tree and collects entries into categorized
// slices: the same walk as WalkStream (see walker), returned all at once. This
// is useful when you need to process all files/directories after scanning.
func WalkCollect(root string, opts WalkOptions) (*WalkCollectResult, error) {
	result := &WalkCollectResult{
		Directories: make([]FileEntry, 0),
		Files:       make([]FileEntry, 0),
		Symlinks:    make([]FileEntry, 0),
	}
	err := walker{ctx: context.Background(), opts: opts, emit: appendTo(result)}.walkRoot(root)
	return result, err
}

// appendTo is WalkCollect's sink: each entry goes on the list of its kind.
func appendTo(result *WalkCollectResult) func(FileEntry) error {
	return func(e FileEntry) error {
		switch {
		case e.SkipReason != "":
			result.Symlinks = append(result.Symlinks, e)
		case e.IsDir:
			result.Directories = append(result.Directories, e)
		default:
			result.Files = append(result.Files, e)
		}
		return nil
	}
}
