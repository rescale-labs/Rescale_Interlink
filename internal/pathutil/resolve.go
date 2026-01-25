// Package pathutil provides path resolution utilities for Rescale Interlink.
// v4.4.3: Extracted from internal/cli/daemon.go for shared use across CLI, GUI, and Tray.
package pathutil

import (
	"os"
	"path/filepath"
)

// ResolveAbsolutePath converts a relative path to an absolute path.
// v4.4.2: Resolves symlinks/junctions in the EXISTING portion of the path,
// then appends any non-existent components. This handles the case where
// user folders (like Downloads) are junction points but the target subdirectory
// doesn't exist yet.
//
// This function is used consistently across CLI, GUI, and Tray to ensure
// paths are resolved the same way regardless of entry point.
func ResolveAbsolutePath(path string) (string, error) {
	if path == "" {
		return os.Getwd()
	}

	// Expand ~ to home directory
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = home + path[1:]
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	// Try to resolve the full path first (fast path if it exists)
	resolved, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		return resolved, nil
	}

	// Path doesn't fully exist - find the deepest existing ancestor
	// and resolve junctions there, then append the rest
	current := absPath
	var remainder []string

	for {
		if _, err := os.Stat(current); err == nil {
			// Found an existing directory - resolve it
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				resolved = current // fallback if resolution fails
			}
			// Append the non-existent remainder
			if len(remainder) > 0 {
				// Reverse remainder (we collected bottom-up)
				for i := len(remainder) - 1; i >= 0; i-- {
					resolved = filepath.Join(resolved, remainder[i])
				}
			}
			return resolved, nil
		}

		// Move up one directory
		parent := filepath.Dir(current)
		if parent == current {
			// Reached root without finding existing dir
			return absPath, nil
		}
		remainder = append(remainder, filepath.Base(current))
		current = parent
	}
}
