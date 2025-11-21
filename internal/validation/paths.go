// Package validation provides input validation utilities for rescale-int.
package validation

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateFilePath validates a user-provided file path for basic safety.
// This is lenient validation suitable for CLI arguments where users have
// full filesystem access.
//
// Returns an error if the path:
//   - Is empty
//   - Contains null bytes or other dangerous characters
//
// Both absolute and relative paths (including those with "..") are allowed
// since this is for user-provided CLI input.
func ValidateFilePath(path string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}

	// Check for null bytes (can cause issues in some systems)
	if strings.ContainsRune(path, 0) {
		return fmt.Errorf("path contains null byte: %s", path)
	}

	return nil
}

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

	// Reject ".." to prevent traversal
	if filename == ".." || strings.Contains(filename, "..") {
		return fmt.Errorf("filename cannot contain '..': %s", filename)
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

// ValidateFilePaths validates multiple file paths.
// Returns an error if any path is invalid.
func ValidateFilePaths(paths []string) error {
	for i, path := range paths {
		if err := ValidateFilePath(path); err != nil {
			return fmt.Errorf("invalid path at index %d: %w", i, err)
		}
	}
	return nil
}

// ValidateFilenames validates multiple filenames.
// Returns an error if any filename is invalid.
func ValidateFilenames(filenames []string) error {
	for i, filename := range filenames {
		if err := ValidateFilename(filename); err != nil {
			return fmt.Errorf("invalid filename at index %d: %w", i, err)
		}
	}
	return nil
}

// ValidateDirectoryPath validates a directory path.
// Currently uses the same logic as ValidateFilePath, but provides
// a separate function for clarity and potential future enhancements.
func ValidateDirectoryPath(path string) error {
	return ValidateFilePath(path)
}
