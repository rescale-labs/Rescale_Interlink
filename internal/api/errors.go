// Package api provides error types for Rescale API responses.
package api

import (
	"errors"
	"strings"

	"github.com/rescale/rescale-int/internal/reporting"
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

// IsFolderNameTakenError reports whether err is the platform's refusal of a new
// folder whose name its parent already holds, as nameTakenAnswer reads it. A
// folder in Trash holds its name too, though no listing shows it.
func IsFolderNameTakenError(err error) bool {
	_, taken := nameTakenAnswer(err)
	return taken
}

// ExplainFolderNameTaken returns err, unless IsFolderNameTakenError recognises
// it: then it says in plain words what the refusal means and what the user can
// do, after what err says before the refusal, such as which folder. The user
// settles it, so it is never filed as an error report.
func ExplainFolderNameTaken(err error) error {
	answer, taken := nameTakenAnswer(err)
	if !taken {
		return err
	}
	return reporting.UsageError(errors.New(strings.TrimSuffix(err.Error(), answer.Error()) +
		"a folder of that name already exists in this location, possibly in Trash " +
		"(restore it or delete it permanently from Trash in the Rescale web UI, or use another name)"))
}

// nameTakenAnswer returns the innermost error err wraps. For a failed API call
// that is the client's own message, the status and the platform's answer,
// without what callers wrap around it: a folder's name there can read as a
// status or hold the refusal's words. taken reports whether it is a 400 whose
// answer opens with the refusal of a name already in use.
func nameTakenAnswer(err error) (answer error, taken bool) {
	if err == nil {
		return nil, false
	}
	answer = err
	for errors.Unwrap(answer) != nil {
		answer = errors.Unwrap(answer)
	}
	status, _ := reporting.StatusOf(answer)
	return answer, status == 400 && strings.Contains(answer.Error(), `: "duplicate key value violates unique constraint`)
}
