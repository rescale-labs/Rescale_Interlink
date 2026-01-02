// Package localfs provides local filesystem abstractions for shared use by CLI and GUI.
// v4.0.4: Extended with symlink support and context-aware operations for North Star alignment.
package localfs

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileEntry represents a file or directory in the local filesystem.
// v4.0.4: Added IsSymlink and LinkTarget for symlink support.
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

// ListDirectory returns the contents of a directory, filtered by options.
// Returns FileEntry slice sorted by the filesystem's native order.
func ListDirectory(path string, opts ListOptions) ([]FileEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	result := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()

		// Filter hidden files unless explicitly included
		if !opts.IncludeHidden && IsHiddenName(name) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			// Skip entries we can't stat (permission issues, etc.)
			continue
		}

		result = append(result, FileEntry{
			Path:    filepath.Join(path, name),
			Name:    name,
			Size:    info.Size(),
			IsDir:   entry.IsDir(),
			ModTime: info.ModTime(),
			Mode:    info.Mode(),
		})
	}

	return result, nil
}

// WalkFunc is the callback signature for Walk.
// Return filepath.SkipDir to skip a directory, or any other error to stop walking.
type WalkFunc func(entry FileEntry) error

// Walk traverses a directory tree, calling fn for each file and directory.
// It respects WalkOptions for hidden file/directory filtering.
//
// The walk is depth-first. Directories are visited before their contents.
// If fn returns filepath.SkipDir for a directory, that directory's contents are skipped.
// If fn returns any other non-nil error, the walk stops and returns that error.
func Walk(root string, opts WalkOptions, fn WalkFunc) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Error accessing path - skip it
			return nil
		}

		name := d.Name()

		// Handle hidden items
		if !opts.IncludeHidden && IsHiddenName(name) {
			if d.IsDir() && opts.SkipHiddenDirs {
				return filepath.SkipDir
			}
			// Skip hidden files
			return nil
		}

		info, err := d.Info()
		if err != nil {
			// Skip entries we can't stat
			return nil
		}

		entry := FileEntry{
			Path:    path,
			Name:    name,
			Size:    info.Size(),
			IsDir:   d.IsDir(),
			ModTime: info.ModTime(),
			Mode:    info.Mode(),
		}

		return fn(entry)
	})
}

// WalkFiles is a convenience wrapper around Walk that only visits regular files
// (not directories). This is useful for collecting files for upload operations.
func WalkFiles(root string, opts WalkOptions, fn WalkFunc) error {
	return Walk(root, opts, func(entry FileEntry) error {
		if entry.IsDir {
			return nil // Skip directories, continue walking
		}
		return fn(entry)
	})
}

// =============================================================================
// v4.0.4: Extended API for North Star alignment (CLI/GUI code reuse)
// =============================================================================

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
// v4.0.4: Context-aware with timeout support and parallel symlink resolution.
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

// WalkCollectResult contains the categorized results of WalkCollect.
type WalkCollectResult struct {
	Directories []FileEntry // All directories found
	Files       []FileEntry // All regular files found
	Symlinks    []FileEntry // All symbolic links found
}

// WalkCollect walks a directory tree and collects entries into categorized slices.
// v4.0.4: Shared implementation for CLI folder uploads and similar operations.
//
// Unlike Walk which uses callbacks, WalkCollect returns all results at once.
// This is useful when you need to process all files/directories after scanning.
//
// Symlinks are NOT followed (to prevent infinite loops). They are collected
// separately in the Symlinks slice for the caller to handle.
func WalkCollect(root string, opts WalkOptions) (*WalkCollectResult, error) {
	result := &WalkCollectResult{
		Directories: make([]FileEntry, 0),
		Files:       make([]FileEntry, 0),
		Symlinks:    make([]FileEntry, 0),
	}

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

		entry := FileEntry{
			Path:      path,
			Name:      name,
			Size:      fileInfo.Size(),
			IsDir:     d.IsDir(),
			ModTime:   fileInfo.ModTime(),
			Mode:      fileInfo.Mode(),
			IsSymlink: isSymlink,
		}

		// Categorize
		if isSymlink {
			result.Symlinks = append(result.Symlinks, entry)
			// Skip symlinked directories to prevent infinite loops
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			result.Directories = append(result.Directories, entry)
		} else {
			result.Files = append(result.Files, entry)
		}

		return nil
	})

	return result, err
}
