package storage

import (
	"strings"
)

// IsDiskFullError checks if an error is likely caused by running out of disk space
// This catches errors that occur during file operations when disk becomes full
//
// Checks for common error strings across different operating systems:
//   - Linux/Unix: "no space left on device", "enospc"
//   - Windows: "out of disk space", "insufficient disk space"
//   - Generic: "disk full", "not enough space"
//   - Quota: "disk quota exceeded"
func IsDiskFullError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())

	diskFullIndicators := []string{
		"no space left on device", // Linux/Unix
		"disk full",               // Generic
		"out of disk space",       // Windows
		"insufficient disk space", // Windows
		"not enough space",        // Generic
		"enospc",                  // Linux errno
		"disk quota exceeded",     // Quota systems
	}

	for _, indicator := range diskFullIndicators {
		if strings.Contains(errStr, indicator) {
			return true
		}
	}

	return false
}
