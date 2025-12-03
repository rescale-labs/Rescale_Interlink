// Package api provides error types for Rescale API responses.
package api

import (
	"errors"
	"strings"
)

// ErrFileAlreadyExists indicates a file with the same name already exists in the folder.
// This error is returned when attempting to upload a file that would create a duplicate.
var ErrFileAlreadyExists = errors.New("file already exists")

// IsFileExistsError checks if an error indicates a duplicate file.
//
// This function detects "file already exists" errors from multiple sources:
//  1. Wrapped ErrFileAlreadyExists error
//  2. HTTP 409 Conflict status code
//  3. Error messages containing "already exists", "duplicate", or "conflict"
//
// Usage:
//
//	cloudFile, err := upload.UploadFileToFolder(...)
//	if api.IsFileExistsError(err) {
//	    // Handle conflict
//	}
func IsFileExistsError(err error) bool {
	if err == nil {
		return false
	}

	// Check for wrapped ErrFileAlreadyExists
	if errors.Is(err, ErrFileAlreadyExists) {
		return true
	}

	// Check error message for common patterns
	errStr := strings.ToLower(err.Error())

	// Common patterns indicating duplicate file
	conflictIndicators := []string{
		"already exists",
		"duplicate",
		"conflict",
		"file exists",
		"name already in use",
	}

	for _, indicator := range conflictIndicators {
		if strings.Contains(errStr, indicator) {
			return true
		}
	}

	return false
}
