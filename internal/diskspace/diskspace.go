// Package diskspace provides utilities for checking available disk space
// across different operating systems and file systems.
package diskspace

import (
	"fmt"
	"path/filepath"
	"syscall"
)

// InsufficientSpaceError indicates that there is not enough disk space available.
type InsufficientSpaceError struct {
	Path           string
	RequiredBytes  int64
	AvailableBytes int64
}

func (e *InsufficientSpaceError) Error() string {
	requiredMB := float64(e.RequiredBytes) / (1024 * 1024)
	availableMB := float64(e.AvailableBytes) / (1024 * 1024)
	return fmt.Sprintf("insufficient disk space for %s: need %.2f MB, have %.2f MB available",
		e.Path, requiredMB, availableMB)
}

// CheckAvailableSpace checks if there is sufficient disk space available for a file operation.
// It checks the disk/filesystem where the target path will be created.
//
// Parameters:
//   - targetPath: The path where the file will be created (can be non-existent)
//   - requiredBytes: The number of bytes needed
//   - safetyMargin: Multiplier for safety (e.g., 1.1 for 10% buffer)
//
// Returns an InsufficientSpaceError if there is not enough space.
func CheckAvailableSpace(targetPath string, requiredBytes int64, safetyMargin float64) error {
	// Get the directory containing the target path (must exist for stat)
	dir := filepath.Dir(targetPath)

	// Get filesystem statistics
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		// If we can't stat the filesystem, we can't reliably check space.
		// Return nil to allow the operation to proceed and fail naturally if needed.
		// This handles edge cases like network filesystems, virtual filesystems, etc.
		return nil
	}

	// Calculate available space
	// stat.Bavail = blocks available to non-root users
	// stat.Bsize = block size in bytes
	availableBytes := int64(stat.Bavail) * int64(stat.Bsize)

	// Apply safety margin to required bytes
	requiredWithMargin := int64(float64(requiredBytes) * safetyMargin)

	if availableBytes < requiredWithMargin {
		return &InsufficientSpaceError{
			Path:           targetPath,
			RequiredBytes:  requiredWithMargin,
			AvailableBytes: availableBytes,
		}
	}

	return nil
}

// GetAvailableSpace returns the available space in bytes for the filesystem
// containing the given path. Returns 0 if unable to determine.
func GetAvailableSpace(path string) int64 {
	dir := filepath.Dir(path)

	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0
	}

	return int64(stat.Bavail) * int64(stat.Bsize)
}

// IsInsufficientSpaceError checks if an error is an InsufficientSpaceError
func IsInsufficientSpaceError(err error) bool {
	_, ok := err.(*InsufficientSpaceError)
	return ok
}
