// Package diskspace provides utilities for checking available disk space
// across different operating systems and file systems.
package diskspace

import "fmt"

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

// IsInsufficientSpaceError checks if an error is an InsufficientSpaceError
func IsInsufficientSpaceError(err error) bool {
	_, ok := err.(*InsufficientSpaceError)
	return ok
}
