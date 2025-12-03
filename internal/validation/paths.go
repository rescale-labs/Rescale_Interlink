// Package validation provides input validation utilities for rescale-int.
package validation

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateFilename validates a filename (not a full path) to prevent path traversal.
// This should be used for validating filenames received from external sources
// (like API responses) before using them in filepath.Join operations.
//
// Returns an error if the filename:
//   - Is empty
//   - Contains path separators (/ or \)
//   - Contains ".." components
//   - Contains null bytes
//
// This is strict validation to prevent path traversal attacks when filenames
// come from untrusted sources.
func ValidateFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("filename cannot be empty")
	}

	// Check for null bytes
	if strings.ContainsRune(filename, 0) {
		return fmt.Errorf("filename contains null byte: %s", filename)
	}

	// Reject path separators (both Unix and Windows style)
	if strings.ContainsRune(filename, '/') || strings.ContainsRune(filename, '\\') {
		return fmt.Errorf("filename cannot contain path separators: %s", filename)
	}

	// Reject ".." filename to prevent traversal
	// Note: Since path separators (/ and \) are already rejected above,
	// we only need to check for the literal ".." filename, not patterns like "../"
	// This allows legitimate filenames like "foo..bar.txt" or "data..v2.csv"
	if filename == ".." {
		return fmt.Errorf("filename cannot be '..': %s", filename)
	}

	return nil
}

// ValidatePathInDirectory validates that a path, when resolved, stays within baseDir.
// This is used when you want to ensure a path doesn't escape a designated directory.
//
// Both path and baseDir are cleaned and made absolute before comparison.
// Returns an error if the resolved path is not within baseDir.
//
// Example:
//
//	ValidatePathInDirectory("../../etc/passwd", "/tmp/uploads") // Error: escapes base dir
//	ValidatePathInDirectory("subdir/file.txt", "/tmp/uploads")   // OK: within base dir
func ValidatePathInDirectory(path string, baseDir string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}
	if baseDir == "" {
		return fmt.Errorf("base directory cannot be empty")
	}

	// Clean both paths
	cleanPath := filepath.Clean(path)
	cleanBase := filepath.Clean(baseDir)

	// Make baseDir absolute if it isn't already
	var err error
	if !filepath.IsAbs(cleanBase) {
		cleanBase, err = filepath.Abs(cleanBase)
		if err != nil {
			return fmt.Errorf("failed to resolve base directory: %w", err)
		}
	}

	// Resolve path relative to base directory
	var resolvedPath string
	if filepath.IsAbs(cleanPath) {
		resolvedPath = cleanPath
	} else {
		resolvedPath = filepath.Join(cleanBase, cleanPath)
	}

	// Clean the resolved path
	resolvedPath = filepath.Clean(resolvedPath)

	// Check if resolved path is within base directory
	// Use filepath.Rel to check containment
	relPath, err := filepath.Rel(cleanBase, resolvedPath)
	if err != nil {
		return fmt.Errorf("failed to compute relative path: %w", err)
	}

	// If the relative path starts with "..", it's outside the base directory
	if strings.HasPrefix(relPath, ".."+string(filepath.Separator)) || relPath == ".." {
		return fmt.Errorf("path escapes base directory: %s (base: %s)", path, baseDir)
	}

	return nil
}
