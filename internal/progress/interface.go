package progress

import "io"

// ProgressUI defines the interface for progress tracking during file operations.
// Both upload and download UIs implement this interface to provide consistent
// progress reporting and output management.
type ProgressUI interface {
	// AddFileBar creates a new progress bar for a file operation
	AddFileBar(index int, localPath, folderID string, size int64) FileBarHandle

	// SetFolderPath caches a human-readable path for a folder ID
	SetFolderPath(folderID, path string)

	// GetFolderPath retrieves the cached human-readable path for a folder ID
	GetFolderPath(folderID string) string

	// Wait blocks until all progress bars complete
	Wait()

	// Writer returns an io.Writer that safely outputs above the progress bars.
	// Returns mpb's writer if in terminal mode, otherwise returns os.Stdout/os.Stderr.
	Writer() io.Writer

	// IsTerminal returns true if output is to a terminal (progress bars are active)
	IsTerminal() bool
}

// FileBarHandle represents a handle to a single file's progress bar
type FileBarHandle interface {
	// UpdateProgress updates the progress bar based on a fraction (0.0 to 1.0)
	UpdateProgress(fraction float64)

	// SetRetry updates the retry counter and visually marks the bar
	SetRetry(count int)

	// Complete marks the operation as finished and prints a summary
	Complete(fileID string, err error)

	// ResetStartTime resets the start time to now (used to exclude encryption time from transfer rate)
	ResetStartTime()
}
