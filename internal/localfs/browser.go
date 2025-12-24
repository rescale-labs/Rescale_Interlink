package localfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FileEntry represents a file or directory in the local filesystem.
type FileEntry struct {
	Path    string    // Full path to the file
	Name    string    // Base name of the file
	Size    int64     // Size in bytes (0 for directories)
	IsDir   bool      // True if this is a directory
	ModTime time.Time // Last modification time
	Mode    fs.FileMode // File mode/permissions
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
