// Package paths provides utilities for file path handling in downloads.
package paths

import (
	"fmt"
	"path/filepath"
)

// FileForDownload represents a file to be downloaded with source and destination info.
// This struct is used by both CLI and GUI download code paths to ensure consistent
// behavior across all entry points (per CLAUDE.md north stars: "maximum code re-use").
type FileForDownload struct {
	FileID    string // Unique file identifier from Rescale
	Name      string // Original filename
	LocalPath string // Full local destination path
	Size      int64  // File size in bytes
}

// ResolveCollisions takes a list of files and ensures all LocalPaths are unique.
// When multiple files have the same LocalPath, each gets the FileID appended
// before the extension to make it unique.
//
// Example: Two files named "output.zip" become:
//   - output_ABC123.zip
//   - output_DEF456.zip
//
// This prevents concurrent downloads from corrupting each other when writing
// to the same file path.
//
// Returns the modified list (same slice, modified in place) and count of files
// that were involved in collisions.
func ResolveCollisions(files []FileForDownload) ([]FileForDownload, int) {
	if len(files) == 0 {
		return files, 0
	}

	// Group files by their LocalPath
	pathToIndices := make(map[string][]int)
	for i, f := range files {
		pathToIndices[f.LocalPath] = append(pathToIndices[f.LocalPath], i)
	}

	// Resolve collisions by appending FileID
	collisionCount := 0
	for path, indices := range pathToIndices {
		if len(indices) <= 1 {
			continue // No collision for this path
		}

		// Multiple files have the same path - resolve by appending FileID
		collisionCount += len(indices)
		for _, idx := range indices {
			f := &files[idx]
			ext := filepath.Ext(path)
			base := path[:len(path)-len(ext)]
			// Insert FileID before extension: "file.zip" -> "file_ABC123.zip"
			f.LocalPath = fmt.Sprintf("%s_%s%s", base, f.FileID, ext)
		}
	}

	return files, collisionCount
}
