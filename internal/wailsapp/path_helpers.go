package wailsapp

import (
	"fmt"

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
