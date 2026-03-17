package wailsapp

import (
	"fmt"
	"path/filepath"

	"github.com/rescale/rescale-int/internal/validation"
)

// resolveSafeDownloadPath validates that relativePath stays within baseDir
// and returns the resolved absolute path. Returns error if path escapes baseDir.
func resolveSafeDownloadPath(relativePath, baseDir string) (string, error) {
	if err := validation.ValidatePathInDirectory(relativePath, baseDir); err != nil {
		return "", fmt.Errorf("path traversal rejected: %w", err)
	}
	return filepath.Join(baseDir, relativePath), nil
}
