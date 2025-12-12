//go:build windows

// Package diskspace provides utilities for checking available disk space
// across different operating systems and file systems.
package diskspace

import (
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
)

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

	availableBytes := getAvailableSpaceWindows(dir)
	if availableBytes == 0 {
		// If we can't get the disk space, we can't reliably check space.
		// Return nil to allow the operation to proceed and fail naturally if needed.
		return nil
	}

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
	return getAvailableSpaceWindows(dir)
}

// getAvailableSpaceWindows uses the Windows API GetDiskFreeSpaceExW to get available disk space.
func getAvailableSpaceWindows(path string) int64 {
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}

	ret, _, _ := getDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)

	if ret == 0 {
		return 0
	}

	return int64(freeBytesAvailable)
}
