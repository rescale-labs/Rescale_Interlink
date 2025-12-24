// Package localfs provides unified local filesystem operations for Rescale Interlink.
// This package consolidates duplicated filesystem logic (hidden file detection,
// directory listing, walking) to ensure consistent behavior across CLI and GUI.
package localfs

import (
	"path/filepath"
	"strings"
)

// IsHidden returns true if the file or directory at the given path is hidden.
// On Unix systems, this checks if the base name starts with a dot.
// The path can be relative or absolute.
func IsHidden(path string) bool {
	return IsHiddenName(filepath.Base(path))
}

// IsHiddenName returns true if the given filename (not path) represents a hidden file.
// This is useful when you already have just the filename and don't need path processing.
// Special entries "." and ".." are not considered hidden.
func IsHiddenName(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	return strings.HasPrefix(name, ".")
}
