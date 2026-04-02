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
//
// Example: walking root/a/sub then root/b/link -> root/a/sub:
//   - At root/a: map = {0: root, 1: a}
//   - At root/a/sub: map = {0: root, 1: a, 2: sub}
//   - At root/b: trim >= 1 → map = {0: root}, then add {1: b}
//   - At root/b/link -> root/a/sub: check if a/sub is in map → NO (only root, b) → not a cycle ✓
type ancestryMap struct {
	entries  map[int]dirIdentity
	maxDepth int
}

func newAncestryMap() *ancestryMap {
	return &ancestryMap{entries: make(map[int]dirIdentity)}
}

// set records a directory identity at the given depth, trimming deeper entries.
func (a *ancestryMap) set(depth int, id dirIdentity) {
	// Trim entries at depth >= current (belong to previous sibling branches)
	for d := range a.entries {
		if d >= depth {
			delete(a.entries, d)
		}
	}
	a.entries[depth] = id
	a.maxDepth = depth
}

// trimTo removes all entries at depth >= d. Used before cycle-checking a symlink
// at depth d, so that sibling real directories don't cause false cycle detection.
func (a *ancestryMap) trimTo(depth int) {
	for d := range a.entries {
		if d >= depth {
			delete(a.entries, d)
		}
	}
	if depth-1 < a.maxDepth {
		a.maxDepth = depth - 1
	}
}

// contains checks if any ancestor in the current chain matches the given identity.
func (a *ancestryMap) contains(id dirIdentity) bool {
	for _, existing := range a.entries {
		if existing == id {
			return true
		}
	}
	return false
}

// snapshot returns a copy of the current ancestry for use in recursive walks.
func (a *ancestryMap) snapshot() []dirIdentity {
	result := make([]dirIdentity, 0, len(a.entries))
	for _, id := range a.entries {
		result = append(result, id)
	}
	return result
}

// ancestrySet is an immutable set of directory identities used by walkSymlinkedDir.
// Unlike ancestryMap (which tracks depth), this is a flat set cloned from the
// caller's ancestry at the point of the symlink. Nested symlinked walks start
// with their parent's full ancestry and add the symlink target.
type ancestrySet struct {
	ids []dirIdentity
}

func newAncestrySet(base []dirIdentity) *ancestrySet {
	copied := make([]dirIdentity, len(base))
	copy(copied, base)
	return &ancestrySet{ids: copied}
}

func (a *ancestrySet) contains(id dirIdentity) bool {
	for _, existing := range a.ids {
		if existing == id {
			return true
		}
	}
	return false
}

func (a *ancestrySet) with(id dirIdentity) *ancestrySet {
	newIDs := make([]dirIdentity, len(a.ids)+1)
	copy(newIDs, a.ids)
	newIDs[len(a.ids)] = id
	return &ancestrySet{ids: newIDs}
}

// FileEntry represents a file or directory in the local filesystem.
type FileEntry struct {
	Path       string      // Full path to the file
	Name       string      // Base name of the file
	Size       int64       // Size in bytes (0 for directories, target size for symlinks)
	IsDir      bool        // True if this is a directory (or symlink to directory)
	ModTime    time.Time   // Last modification time
	Mode       fs.FileMode // File mode/permissions
	IsSymlink  bool        // True if this is a symbolic link
	LinkTarget string      // Target path for symlinks (empty if not a symlink or resolution failed)
}

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
// Key property: filepath.WalkDir visits parents before children (depth-first,
// parent-first), so directories at depth N are emitted before files at depth N+1.
//
// Both channels are closed when the walk completes. Errors are sent to errChan
// (buffered at 1). Context cancellation stops the walk and closes all channels.
func WalkStream(ctx context.Context, root string, opts WalkOptions) (
	dirChan <-chan FileEntry,
	fileChan <-chan FileEntry,
	errChan <-chan error,
) {
	dirs := make(chan FileEntry, 1000)
	files := make(chan FileEntry, 1000)
	errs := make(chan error, 1)

	go func() {
		defer close(dirs)
		defer close(files)
		defer close(errs)

		// Initialize ancestry tracking for symlink cycle detection.
		var ancestry *ancestryMap
		if opts.FollowSymlinks {
			ancestry = newAncestryMap()
			rootInfo, statErr := os.Stat(root)
			if statErr == nil {
				if id, ok := getDirIdentity(rootInfo); ok {
					ancestry.set(0, id)
				}
			}
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

				realInfo, statErr := os.Stat(path)
				if statErr != nil {
					return nil // Broken symlink — skip silently
				}

				if realInfo.IsDir() {
					// Symlinked directory — check for cycles before descending
					id, ok := getDirIdentity(realInfo)
					if !ok {
						// Can't determine identity (Windows) — don't follow, skip safely
						return nil
					}
					// Trim ancestry to current depth before cycle check. Without this,
					// a sibling real directory (e.g., root/b visited before root/link_to_b)
					// would remain in the ancestry, causing a false cycle detection.
					depth := strings.Count(filepath.Clean(path), string(filepath.Separator)) - rootDepth
					ancestry.trimTo(depth)

					if ancestry.contains(id) {
						return nil // Cycle: target is an ancestor — return nil (not SkipDir) because d.IsDir()=false for symlinks
					}

					// Walk the symlink target, emitting entries with ORIGINAL path prefix
					resolvedTarget, evalErr := filepath.EvalSymlinks(path)
					if evalErr != nil {
						return nil
					}

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

					// Build ancestry snapshot and add symlink target
					childAncestry := newAncestrySet(ancestry.snapshot())
					childAncestry = childAncestry.with(id)

					_ = walkSymlinkedDir(ctx, resolvedTarget, path, opts, childAncestry, dirs, files)
					return nil // We handled it ourselves — return nil (not SkipDir) because d.IsDir()=false for symlinks
				}

				// Symlinked file — use real info for size/modtime
				fileInfo = realInfo
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

	return dirs, files, errs
}

// walkSymlinkedDir walks a resolved symlink target directory, emitting entries
// with paths rewritten to use the original symlink path prefix.
// This ensures the orchestrator builds correct remote folder structure.
// The ancestry parameter is a snapshot of the caller's ancestry — mutations
// do not affect the caller or sibling walks.
func walkSymlinkedDir(
	ctx context.Context,
	resolvedRoot string, // The real directory path (after EvalSymlinks)
	originalRoot string, // The symlink path (what the user sees)
	opts WalkOptions,
	ancestry *ancestrySet, // Snapshot of caller's ancestry
	dirs chan<- FileEntry,
	files chan<- FileEntry,
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

		// Skip root itself
		if resolvedPath == resolvedRoot {
			return nil
		}

		// Compute the original path by replacing the resolved prefix with the original prefix
		relPath, err := filepath.Rel(resolvedRoot, resolvedPath)
		if err != nil {
			return nil
		}
		originalPath := filepath.Join(originalRoot, relPath)
		name := d.Name()

		// Hidden handling (same as main walk)
		if !opts.IncludeHidden && IsHiddenName(name) {
			if d.IsDir() && opts.SkipHiddenDirs {
				return filepath.SkipDir
			}
			return nil
		}

		// Symlink handling within the symlinked tree
		fileInfo, err := os.Lstat(resolvedPath)
		if err != nil {
			return nil
		}

		isSymlink := fileInfo.Mode()&os.ModeSymlink != 0

		if isSymlink {
			realInfo, statErr := os.Stat(resolvedPath)
			if statErr != nil {
				return nil // Broken symlink
			}

			if realInfo.IsDir() {
				id, ok := getDirIdentity(realInfo)
				if !ok {
					return nil // Can't identify (Windows) — don't follow
				}

				if ancestry.contains(id) {
					return nil // Cycle: target is an ancestor
				}

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

				// Recursive walk with extended ancestry
				nestedResolved, evalErr := filepath.EvalSymlinks(resolvedPath)
				if evalErr != nil {
					return nil
				}
				childAncestry := ancestry.with(id)
				_ = walkSymlinkedDir(ctx, nestedResolved, originalPath, opts, childAncestry, dirs, files)
				return nil
			}

			// Symlinked file — use real info
			fileInfo = realInfo
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
	ancestry *ancestrySet,
	result *WalkCollectResult,
) error {
	return filepath.WalkDir(resolvedRoot, func(resolvedPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if resolvedPath == resolvedRoot {
			return nil
		}

		relPath, err := filepath.Rel(resolvedRoot, resolvedPath)
		if err != nil {
			return nil
		}
		originalPath := filepath.Join(originalRoot, relPath)
		name := d.Name()

		if !opts.IncludeHidden && IsHiddenName(name) {
			if d.IsDir() && opts.SkipHiddenDirs {
				return filepath.SkipDir
			}
			return nil
		}

		fileInfo, err := os.Lstat(resolvedPath)
		if err != nil {
			return nil
		}

		isSymlink := fileInfo.Mode()&os.ModeSymlink != 0

		if isSymlink {
			realInfo, statErr := os.Stat(resolvedPath)
			if statErr != nil {
				return nil
			}

			if realInfo.IsDir() {
				id, ok := getDirIdentity(realInfo)
				if !ok {
					// Can't identify — add to symlinks list and skip
					result.Symlinks = append(result.Symlinks, FileEntry{
						Path: originalPath, Name: name, IsDir: true, IsSymlink: true,
						Size: fileInfo.Size(), ModTime: fileInfo.ModTime(), Mode: fileInfo.Mode(),
					})
					return nil
				}

				if ancestry.contains(id) {
					return nil
				}

				// Add as directory entry
				result.Directories = append(result.Directories, FileEntry{
					Path: originalPath, Name: name, IsDir: true,
					Size: realInfo.Size(), ModTime: realInfo.ModTime(), Mode: realInfo.Mode(),
				})

				nestedResolved, evalErr := filepath.EvalSymlinks(resolvedPath)
				if evalErr != nil {
					return nil
				}
				childAncestry := ancestry.with(id)
				_ = collectSymlinkedDir(nestedResolved, originalPath, opts, childAncestry, result)
				return nil
			}

			fileInfo = realInfo
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
// in the Symlinks slice. When true, symlinks are followed with cycle detection
// and their targets appear in Directories/Files as appropriate.
func WalkCollect(root string, opts WalkOptions) (*WalkCollectResult, error) {
	result := &WalkCollectResult{
		Directories: make([]FileEntry, 0),
		Files:       make([]FileEntry, 0),
		Symlinks:    make([]FileEntry, 0),
	}

	var ancestry *ancestryMap
	if opts.FollowSymlinks {
		ancestry = newAncestryMap()
		rootInfo, statErr := os.Stat(root)
		if statErr == nil {
			if id, ok := getDirIdentity(rootInfo); ok {
				ancestry.set(0, id)
			}
		}
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

			realInfo, statErr := os.Stat(path)
			if statErr != nil {
				return nil // Broken symlink
			}

			if realInfo.IsDir() {
				id, ok := getDirIdentity(realInfo)
				if !ok {
					// Can't identify (Windows) — add to symlinks and skip
					result.Symlinks = append(result.Symlinks, FileEntry{
						Path: path, Name: name, IsDir: true, IsSymlink: true,
						Size: fileInfo.Size(), ModTime: fileInfo.ModTime(), Mode: fileInfo.Mode(),
					})
					return nil
				}
				// Trim ancestry to current depth before cycle check (same as WalkStream).
				depth := strings.Count(filepath.Clean(path), string(filepath.Separator)) - rootDepth
				ancestry.trimTo(depth)

				if ancestry.contains(id) {
					return nil // Cycle
				}

				// Emit as directory
				result.Directories = append(result.Directories, FileEntry{
					Path: path, Name: name, IsDir: true,
					Size: realInfo.Size(), ModTime: realInfo.ModTime(), Mode: realInfo.Mode(),
				})

				resolvedTarget, evalErr := filepath.EvalSymlinks(path)
				if evalErr != nil {
					return nil
				}
				childAncestry := newAncestrySet(ancestry.snapshot())
				childAncestry = childAncestry.with(id)
				_ = collectSymlinkedDir(resolvedTarget, path, opts, childAncestry, result)
				return nil
			}

			// Symlinked file — use real info
			fileInfo = realInfo
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
