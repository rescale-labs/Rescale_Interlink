package wailsapp

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rescale/rescale-int/internal/validation"
)

// resolveSafeDownloadPath validates that relativePath stays within baseDir
// and returns the resolved absolute path. Returns error if path escapes baseDir.
func resolveSafeDownloadPath(relativePath, baseDir string) (string, error) {
	localPath, err := validation.ResolvePathInDirectory(relativePath, baseDir)
	if err != nil {
		return "", fmt.Errorf("path traversal rejected: %w", err)
	}
	return localPath, nil
}

// stripJobIOPrefix removes a leading "Input" or "Output" path segment (the
// platform's job-folder split) from a scanned relative path, so a job folder
// downloads with its files directly under the job folder — matching the
// auto-download layout. Deeper structure is preserved (e.g. "Output/run1/a.dat"
// -> "run1/a.dat"). A bare "Input"/"Output" segment maps to "". Comparison is
// case-insensitive since the platform's casing is not guaranteed. Paths without
// such a prefix are returned unchanged.
func stripJobIOPrefix(relPath string) string {
	if relPath == "" {
		return ""
	}
	// Normalize separators so the split is consistent regardless of how the
	// scanner joined the path on this platform.
	norm := filepath.ToSlash(relPath)
	first := norm
	rest := ""
	if idx := strings.IndexByte(norm, '/'); idx >= 0 {
		first = norm[:idx]
		rest = norm[idx+1:]
	}
	switch strings.ToLower(first) {
	case "input", "output":
		return filepath.FromSlash(rest)
	default:
		return relPath
	}
}
