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

// set records a directory identity at the given depth, trimming deeper entries.
func (a *ancestryMap) set(depth int, id dirIdentity) {
	a.trimTo(depth)
	a.entries[depth] = id
}

// trimTo removes all entries at depth >= d. Used before cycle-checking a symlink
// at depth d, so that sibling real directories don't cause false cycle detection.
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
// Avoids loading all files into memory before uploads start. Uses the same
// filtering logic as WalkCollect (hidden handling, symlink skipping) for
// consistent behavior.
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

		// Initialize ancestry tracking for symlink cycle detection.
		var ancestry *ancestryMap
		if opts.FollowSymlinks {
			realRoot, _ := realPath(root)
			ancestry = newAncestryMap().below(realRoot)
		}

		// Compute root depth for relative depth calculation
		rootDepth := strings.Count(filepath.Clean(root), string(filepath.Separator))

		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // Skip inaccessible entries (matches WalkCollect)
			}

			// Check context cancellation
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// Skip root itself
			if path == root {
				return nil
			}

			name := d.Name()

			// Handle hidden items (matches WalkCollect exactly)
			if !opts.IncludeHidden && IsHiddenName(name) {
				if d.IsDir() && opts.SkipHiddenDirs {
					return filepath.SkipDir
				}
				return nil
			}

			// Check if symlink using Lstat (doesn't follow symlinks)
			fileInfo, err := os.Lstat(path)
			if err != nil {
				return nil // Skip entries we can't stat
			}

			isSymlink := fileInfo.Mode()&os.ModeSymlink != 0

			if isSymlink {
				if !opts.FollowSymlinks {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}

				// Surfaced to the caller, so the user sees that the link was
				// left out rather than silently lost.
				depth := strings.Count(filepath.Clean(path), string(filepath.Separator)) - rootDepth
				resolvedTarget, realInfo, reason := followLink(path, ancestry, depth)
				if reason != "" {
					emitSkipped(ctx, skipped, skippedLink(path, realInfo, reason))
					return nil
				}

				if realInfo.IsDir() {
					// Emit a synthetic directory entry for the symlink alias itself.
					aliasEntry := FileEntry{
						Path:    path,
						Name:    name,
						Size:    realInfo.Size(),
						IsDir:   true,
						ModTime: realInfo.ModTime(),
						Mode:    realInfo.Mode(),
					}
					select {
					case dirs <- aliasEntry:
					case <-ctx.Done():
						return ctx.Err()
					}

					// Walk the symlink target, emitting entries with ORIGINAL path prefix
					_ = walkSymlinkedDir(ctx, resolvedTarget, path, opts, ancestry.below(resolvedTarget), dirs, files, skipped)
					return nil // We handled it ourselves — return nil (not SkipDir) because d.IsDir()=false for symlinks
				}

				// Symlinked file — use real info for size/modtime
				fileInfo = realInfo
			}

			// Defensive: an entry not classified as a directory by Lstat may
			// still resolve to a directory through Stat — e.g. a Windows
			// reparse-point junction whose Lstat mode lacks ModeSymlink (some
			// legacy junctions are tagged ModeIrregular instead). Emitting it
			// as a file would propagate to UploadFile and fail with
			// "cannot upload a directory". Detect, skip, and surface.
			if shouldProbeResolvedDirectory(fileInfo.Mode(), d.IsDir()) {
				if realInfo, statErr := os.Stat(path); statErr == nil && realInfo.IsDir() {
					emitSkipped(ctx, skipped, skippedLink(path, realInfo, skipNoDirLink))
					return nil
				}
			}

			entry := FileEntry{
				Path:    path,
				Name:    name,
				Size:    fileInfo.Size(),
				IsDir:   d.IsDir(),
				ModTime: fileInfo.ModTime(),
				Mode:    fileInfo.Mode(),
			}

			if d.IsDir() {
				if opts.FollowSymlinks && ancestry != nil {
					depth := strings.Count(filepath.Clean(path), string(filepath.Separator)) - rootDepth
					if realInfo, statErr := os.Stat(path); statErr == nil {
						if id, ok := getDirIdentity(realInfo); ok {
							ancestry.set(depth, id)
						}
					}
				}
				select {
				case dirs <- entry:
				case <-ctx.Done():
					return ctx.Err()
				}
			} else {
				select {
				case files <- entry:
				case <-ctx.Done():
					return ctx.Err()
				}
			}

			return nil
		})

		if walkErr != nil && ctx.Err() == nil {
			errs <- walkErr
		}
	}()

	return dirs, files, skipped, errs
}

// symlinkedEntry is the per-entry state both symlinked-tree walks compute
// before they diverge on how they report it.
type symlinkedEntry struct {
	originalPath string
	name         string
	fileInfo     os.FileInfo
	isSymlink    bool
	depth        int // levels below the resolved root, which is depth 0
}

// resolveSymlinkedEntry maps an entry inside a resolved symlink target back to
// the path the caller sees and applies the skip rules both walks share. A nil
// entry means "not reported": the WalkDir callback returns skipErr instead —
// nil to continue, filepath.SkipDir to prune a hidden directory.
func resolveSymlinkedEntry(resolvedRoot, originalRoot, resolvedPath string,
	d fs.DirEntry, opts WalkOptions) (*symlinkedEntry, error) {
	// Skip root itself
	if resolvedPath == resolvedRoot {
		return nil, nil
	}

	// Compute the original path by replacing the resolved prefix with the original prefix
	relPath, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return nil, nil
	}
	originalPath := filepath.Join(originalRoot, relPath)
	name := d.Name()

	// Hidden handling (same as main walk)
	if !opts.IncludeHidden && IsHiddenName(name) {
		if d.IsDir() && opts.SkipHiddenDirs {
			return nil, filepath.SkipDir
		}
		return nil, nil
	}

	// Symlink handling within the symlinked tree
	fileInfo, err := os.Lstat(resolvedPath)
	if err != nil {
		return nil, nil
	}

	return &symlinkedEntry{
		originalPath: originalPath,
		name:         name,
		fileInfo:     fileInfo,
		isSymlink:    fileInfo.Mode()&os.ModeSymlink != 0,
		depth:        strings.Count(relPath, string(filepath.Separator)) + 1,
	}, nil
}

// walkSymlinkedDir walks a resolved symlink target directory, emitting entries
// with paths rewritten to use the original symlink path prefix.
// This ensures the orchestrator builds correct remote folder structure.
// The ancestry parameter is this walk's own chain (ancestryMap.below), which
// it extends with every directory it enters, so a link to any of them is a cycle.
func walkSymlinkedDir(
	ctx context.Context,
	resolvedRoot string, // The real directory path (after EvalSymlinks)
	originalRoot string, // The symlink path (what the user sees)
	opts WalkOptions,
	ancestry *ancestryMap,
	dirs chan<- FileEntry,
	files chan<- FileEntry,
	skipped chan<- FileEntry,
) error {
	return filepath.WalkDir(resolvedRoot, func(resolvedPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		se, skipErr := resolveSymlinkedEntry(resolvedRoot, originalRoot, resolvedPath, d, opts)
		if se == nil {
			return skipErr
		}
		originalPath, name, fileInfo, isSymlink := se.originalPath, se.name, se.fileInfo, se.isSymlink

		if isSymlink {
			nestedResolved, realInfo, reason := followLink(resolvedPath, ancestry, se.depth)
			if reason != "" {
				emitSkipped(ctx, skipped, skippedLink(originalPath, realInfo, reason))
				return nil
			}

			if realInfo.IsDir() {
				// Emit synthetic directory entry for the alias
				aliasEntry := FileEntry{
					Path:    originalPath,
					Name:    name,
					IsDir:   true,
					Size:    realInfo.Size(),
					ModTime: realInfo.ModTime(),
					Mode:    realInfo.Mode(),
				}
				select {
				case dirs <- aliasEntry:
				case <-ctx.Done():
					return ctx.Err()
				}

				_ = walkSymlinkedDir(ctx, nestedResolved, originalPath, opts, ancestry.below(nestedResolved), dirs, files, skipped)
				return nil
			}

			// Symlinked file — use real info
			fileInfo = realInfo
		}

		// Defensive: see WalkStream — Lstat-as-non-symlink that resolves to
		// a directory (Windows junction with ModeIrregular) must not be
		// emitted as a file.
		if shouldProbeResolvedDirectory(fileInfo.Mode(), d.IsDir()) {
			if realInfo, statErr := os.Stat(resolvedPath); statErr == nil && realInfo.IsDir() {
				emitSkipped(ctx, skipped, skippedLink(originalPath, realInfo, skipNoDirLink))
				return nil
			}
		}

		entry := FileEntry{
			Path:    originalPath, // Use ORIGINAL path, not resolved
			Name:    name,
			Size:    fileInfo.Size(),
			IsDir:   d.IsDir(),
			ModTime: fileInfo.ModTime(),
			Mode:    fileInfo.Mode(),
		}

		if d.IsDir() {
			if id, ok := getDirIdentity(fileInfo); ok {
				// A folder on the chain through a mount alias or firmlink,
				// which no link's real path shows, is a loop too.
				if ancestry.trimTo(se.depth); ancestry.contains(id) {
					emitSkipped(ctx, skipped, skippedLink(originalPath, fileInfo, skipCycle))
					return filepath.SkipDir
				}
				ancestry.entries[se.depth] = id
			}
			select {
			case dirs <- entry:
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			select {
			case files <- entry:
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		return nil
	})
}

// collectSymlinkedDir is the WalkCollect counterpart of walkSymlinkedDir.
// It collects entries into slices instead of sending to channels.
func collectSymlinkedDir(
	resolvedRoot, originalRoot string,
	opts WalkOptions,
	ancestry *ancestryMap,
	result *WalkCollectResult,
) error {
	return filepath.WalkDir(resolvedRoot, func(resolvedPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		se, skipErr := resolveSymlinkedEntry(resolvedRoot, originalRoot, resolvedPath, d, opts)
		if se == nil {
			return skipErr
		}
		originalPath, name, fileInfo, isSymlink := se.originalPath, se.name, se.fileInfo, se.isSymlink

		if isSymlink {
			nestedResolved, realInfo, reason := followLink(resolvedPath, ancestry, se.depth)
			if reason != "" {
				result.Symlinks = append(result.Symlinks, skippedLink(originalPath, realInfo, reason))
				return nil
			}

			if realInfo.IsDir() {
				// Add as directory entry
				result.Directories = append(result.Directories, FileEntry{
					Path: originalPath, Name: name, IsDir: true,
					Size: realInfo.Size(), ModTime: realInfo.ModTime(), Mode: realInfo.Mode(),
				})
				_ = collectSymlinkedDir(nestedResolved, originalPath, opts, ancestry.below(nestedResolved), result)
				return nil
			}

			fileInfo = realInfo
		}

		// Defensive: see WalkStream — Lstat-as-non-symlink that resolves to
		// a directory (Windows junction with ModeIrregular) must not be
		// emitted as a file.
		if shouldProbeResolvedDirectory(fileInfo.Mode(), d.IsDir()) {
			if realInfo, statErr := os.Stat(resolvedPath); statErr == nil && realInfo.IsDir() {
				result.Symlinks = append(result.Symlinks, skippedLink(originalPath, realInfo, skipNoDirLink))
				return nil
			}
		}

		entry := FileEntry{
			Path:    originalPath,
			Name:    name,
			Size:    fileInfo.Size(),
			IsDir:   d.IsDir(),
			ModTime: fileInfo.ModTime(),
			Mode:    fileInfo.Mode(),
		}

		if d.IsDir() {
			if id, ok := getDirIdentity(fileInfo); ok {
				if ancestry.trimTo(se.depth); ancestry.contains(id) { // see walkSymlinkedDir
					result.Symlinks = append(result.Symlinks, skippedLink(originalPath, fileInfo, skipCycle))
					return filepath.SkipDir
				}
				ancestry.entries[se.depth] = id
			}
			result.Directories = append(result.Directories, entry)
		} else {
			result.Files = append(result.Files, entry)
		}

		return nil
	})
}

// WalkCollectResult contains the categorized results of WalkCollect.
type WalkCollectResult struct {
	Directories []FileEntry // All directories found
	Files       []FileEntry // All regular files found
	Symlinks    []FileEntry // All symbolic links found
}

// WalkCollect walks a directory tree and collects entries into categorized slices.
//
// Unlike Walk which uses callbacks, WalkCollect returns all results at once.
// This is useful when you need to process all files/directories after scanning.
//
// When FollowSymlinks is false (default), symlinks are NOT followed and are collected
// in the Symlinks slice. When true, symlinks are followed as WalkStream follows
// them, their targets appear in Directories/Files, and the links left out are
// collected in Symlinks with a SkipReason.
func WalkCollect(root string, opts WalkOptions) (*WalkCollectResult, error) {
	result := &WalkCollectResult{
		Directories: make([]FileEntry, 0),
		Files:       make([]FileEntry, 0),
		Symlinks:    make([]FileEntry, 0),
	}

	var ancestry *ancestryMap
	if opts.FollowSymlinks {
		realRoot, _ := realPath(root)
		ancestry = newAncestryMap().below(realRoot)
	}
	rootDepth := strings.Count(filepath.Clean(root), string(filepath.Separator))

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Error accessing path - skip it
			return nil
		}

		// Skip root itself
		if path == root {
			return nil
		}

		name := d.Name()

		// Handle hidden items
		if !opts.IncludeHidden && IsHiddenName(name) {
			if d.IsDir() && opts.SkipHiddenDirs {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if symlink using Lstat (doesn't follow symlinks)
		fileInfo, err := os.Lstat(path)
		if err != nil {
			// Skip entries we can't stat
			return nil
		}

		isSymlink := fileInfo.Mode()&os.ModeSymlink != 0

		if isSymlink {
			if !opts.FollowSymlinks {
				// Original behavior — collect in Symlinks slice and skip
				entry := FileEntry{
					Path: path, Name: name, Size: fileInfo.Size(), IsDir: d.IsDir(),
					ModTime: fileInfo.ModTime(), Mode: fileInfo.Mode(), IsSymlink: true,
				}
				result.Symlinks = append(result.Symlinks, entry)
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			depth := strings.Count(filepath.Clean(path), string(filepath.Separator)) - rootDepth
			resolvedTarget, realInfo, reason := followLink(path, ancestry, depth)
			if reason != "" {
				result.Symlinks = append(result.Symlinks, skippedLink(path, realInfo, reason))
				return nil
			}

			if realInfo.IsDir() {
				// Emit as directory
				result.Directories = append(result.Directories, FileEntry{
					Path: path, Name: name, IsDir: true,
					Size: realInfo.Size(), ModTime: realInfo.ModTime(), Mode: realInfo.Mode(),
				})
				_ = collectSymlinkedDir(resolvedTarget, path, opts, ancestry.below(resolvedTarget), result)
				return nil
			}

			// Symlinked file — use real info
			fileInfo = realInfo
		}

		// Defensive: see WalkStream — Lstat-as-non-symlink that resolves to
		// a directory (Windows junction with ModeIrregular) must not be
		// emitted as a file.
		if shouldProbeResolvedDirectory(fileInfo.Mode(), d.IsDir()) {
			if realInfo, statErr := os.Stat(path); statErr == nil && realInfo.IsDir() {
				result.Symlinks = append(result.Symlinks, skippedLink(path, realInfo, skipNoDirLink))
				return nil
			}
		}

		entry := FileEntry{
			Path:    path,
			Name:    name,
			Size:    fileInfo.Size(),
			IsDir:   d.IsDir(),
			ModTime: fileInfo.ModTime(),
			Mode:    fileInfo.Mode(),
		}

		if d.IsDir() {
			if opts.FollowSymlinks && ancestry != nil {
				depth := strings.Count(filepath.Clean(path), string(filepath.Separator)) - rootDepth
				if realInfo, statErr := os.Stat(path); statErr == nil {
					if id, ok := getDirIdentity(realInfo); ok {
						ancestry.set(depth, id)
					}
				}
			}
			result.Directories = append(result.Directories, entry)
		} else {
			result.Files = append(result.Files, entry)
		}

		return nil
	})

	return result, err
}
