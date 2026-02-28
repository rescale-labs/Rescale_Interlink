// Package services provides frontend-agnostic business logic for Rescale Interlink.
// This layer sits between the GUI/CLI and the core backend, providing a clean API
// that any frontend can use without framework-specific dependencies.
//
// v3.6.4: Created as part of Fyne -> Wails migration preparation.
package services

import (
	"context"
	"time"
)

// TransferType identifies whether a transfer is an upload or download.
type TransferType string

const (
	TransferTypeUpload   TransferType = "upload"
	TransferTypeDownload TransferType = "download"
)

// TransferState represents the current state of a transfer.
type TransferState string

const (
	TransferStateQueued       TransferState = "queued"       // Waiting for execution slot
	TransferStateInitializing TransferState = "initializing" // Acquired slot, setting up
	TransferStateActive       TransferState = "active"       // Actively transferring bytes
	TransferStatePaused       TransferState = "paused"       // Paused by user
	TransferStateCompleted    TransferState = "completed"    // Successfully completed
	TransferStateFailed       TransferState = "failed"       // Failed with error
	TransferStateCancelled    TransferState = "cancelled"    // Cancelled by user
)

// Source label constants for transfer origin tracking.
// v4.7.4: Centralized constants to prevent string drift.
const (
	SourceLabelPUR         = "PUR"
	SourceLabelSingleJob   = "SingleJob"
	SourceLabelFileBrowser = "FileBrowser"
)

// TransferRequest specifies a single transfer to be executed.
// This is the input to TransferService.StartTransfers().
type TransferRequest struct {
	// Type identifies upload or download
	Type TransferType

	// Source is the source path:
	// - For uploads: local file path
	// - For downloads: Rescale file ID
	Source string

	// Dest is the destination:
	// - For uploads: Rescale folder ID (empty = My Library root)
	// - For downloads: local directory path
	Dest string

	// Name is the display name (usually the filename)
	Name string

	// Size is the file size in bytes
	Size int64

	// SourceLabel identifies the origin ("PUR", "SingleJob", "FileBrowser").
	// v4.7.4: Used for Transfers tab badges and cancel/retry gating.
	SourceLabel string

	// BatchID groups related transfers (e.g., all files in a folder upload).
	// v4.7.7: Used for transfer grouping in Transfers tab to collapse bulk operations.
	BatchID string

	// BatchLabel is the display name for the batch (e.g., folder name, "PUR: <run>").
	// v4.7.7: Shown as the collapsed row label in Transfers tab.
	BatchLabel string

	// Tags to apply after successful upload.
	// v4.7.4: Tagging failure is non-fatal (logged as warning).
	Tags []string
}

// TransferTask represents an active or completed transfer.
// This is published via events for UI updates.
type TransferTask struct {
	// ID is the unique task identifier
	ID string

	// Type is upload or download
	Type TransferType

	// State is the current transfer state
	State TransferState

	// Name is the display name
	Name string

	// Source path (local path for upload, file ID for download)
	Source string

	// Dest path (folder ID for upload, local path for download)
	Dest string

	// Size is the total file size in bytes
	Size int64

	// SourceLabel identifies the origin ("PUR", "SingleJob", "FileBrowser").
	// v4.7.4: Used for Transfers tab badges and cancel/retry gating.
	SourceLabel string

	// BatchID groups related transfers (e.g., all files in a folder upload).
	// v4.7.7: Used for transfer grouping in Transfers tab.
	BatchID string

	// BatchLabel is the display name for the batch (e.g., folder name).
	// v4.7.7: Shown as the collapsed row label in Transfers tab.
	BatchLabel string

	// Progress is 0.0 to 1.0
	Progress float64

	// Speed is bytes per second (smoothed)
	Speed float64

	// Error if the transfer failed
	Error error

	// CreatedAt is when the transfer was queued
	CreatedAt time.Time

	// StartedAt is when the transfer began (acquired slot)
	StartedAt time.Time

	// CompletedAt is when the transfer finished (success, fail, or cancel)
	CompletedAt time.Time
}

// UploadFileSyncParams contains additional parameters for synchronous uploads.
// v4.7.4: Used by UploadFileSync() for pipeline and single-job integration.
type UploadFileSyncParams struct {
	// ExtraProgressCallback is an additional callback for the caller's own tracking
	// (e.g., pipeline's reportStateChange). Called in addition to queue progress.
	ExtraProgressCallback func(progress float64)
}

// IsTerminal returns true if the transfer is in a terminal state.
func (t *TransferTask) IsTerminal() bool {
	return t.State == TransferStateCompleted ||
		t.State == TransferStateFailed ||
		t.State == TransferStateCancelled
}

// CanRetry returns true if the transfer can be retried.
func (t *TransferTask) CanRetry() bool {
	return t.State == TransferStateFailed || t.State == TransferStateCancelled
}

// TransferStats provides aggregate statistics about transfers.
type TransferStats struct {
	Queued       int
	Initializing int
	Active       int
	Paused       int
	Completed    int
	Failed       int
	Cancelled    int
}

// Total returns the total number of tracked transfers.
func (s TransferStats) Total() int {
	return s.Queued + s.Initializing + s.Active + s.Paused + s.Completed + s.Failed + s.Cancelled
}

// FileItem represents a file or folder in a file listing.
// Used by both local and remote file browsers.
type FileItem struct {
	// ID is the unique identifier:
	// - For local files: absolute path
	// - For remote files: Rescale file/folder ID
	ID string

	// Name is the display name
	Name string

	// IsFolder indicates whether this is a directory
	IsFolder bool

	// Size is the file size in bytes (0 for folders)
	Size int64

	// ModTime is the last modification time
	ModTime time.Time

	// Path is the full path (for remote files, may include parent folder path)
	Path string

	// ParentID is the parent folder ID (for remote files)
	ParentID string
}

// BrowseMode indicates the remote browsing context.
type BrowseMode string

const (
	BrowseModeLibrary BrowseMode = "library" // My Library (folders)
	BrowseModeJobs    BrowseMode = "jobs"    // My Jobs
	BrowseModeLegacy  BrowseMode = "legacy"  // Legacy flat file list
)

// FolderContents represents the contents of a folder.
type FolderContents struct {
	// FolderID is the ID of the folder being listed (empty for root)
	FolderID string

	// FolderPath is the display path of the folder
	FolderPath string

	// Items is the list of files and folders
	Items []FileItem

	// HasMore indicates if there are more items (pagination)
	HasMore bool

	// NextCursor is the pagination cursor for the next page
	NextCursor string
}

// UploadFolderResult contains the result of a folder structure creation.
type UploadFolderResult struct {
	// LocalToRemoteMapping maps local directory paths to created remote folder IDs
	LocalToRemoteMapping map[string]string

	// RootFolderID is the ID of the created root folder
	RootFolderID string

	// FilesToUpload is the list of files to upload with their destination folder IDs
	FilesToUpload []UploadFileSpec
}

// UploadFileSpec specifies a single file upload with its destination.
type UploadFileSpec struct {
	LocalPath    string
	DestFolderID string
	Size         int64
}

// DownloadFileSpec specifies a single file download.
type DownloadFileSpec struct {
	FileID    string
	Name      string
	LocalPath string
	Size      int64
}

// TransferServiceInterface defines the transfer service API.
// Implementations handle upload/download orchestration without framework dependencies.
type TransferServiceInterface interface {
	// StartTransfers initiates one or more transfers.
	// Returns immediately; progress is published via events.
	StartTransfers(ctx context.Context, requests []TransferRequest) error

	// CancelTransfer cancels an active transfer.
	CancelTransfer(taskID string) error

	// CancelAll cancels all active transfers.
	CancelAll()

	// RetryTransfer retries a failed or cancelled transfer.
	RetryTransfer(taskID string) (string, error)

	// GetStats returns current transfer statistics.
	GetStats() TransferStats

	// GetTasks returns all tracked transfers.
	GetTasks() []TransferTask

	// ClearCompleted removes completed/failed/cancelled transfers from tracking.
	ClearCompleted()
}

// FileServiceInterface defines the file service API.
// Implementations handle file/folder operations without framework dependencies.
type FileServiceInterface interface {
	// ListFolder returns the contents of a folder.
	ListFolder(ctx context.Context, folderID string) (*FolderContents, error)

	// ListFolderPage returns a page of folder contents (for pagination).
	ListFolderPage(ctx context.Context, folderID string, cursor string) (*FolderContents, error)

	// CreateFolder creates a new folder.
	CreateFolder(ctx context.Context, name string, parentID string) (string, error)

	// DeleteFile deletes a file by ID.
	DeleteFile(ctx context.Context, fileID string) error

	// DeleteFolder deletes a folder by ID.
	DeleteFolder(ctx context.Context, folderID string) error

	// PrepareUploadFolder creates the remote folder structure for a local folder.
	PrepareUploadFolder(ctx context.Context, localPath string, remoteFolderID string) (*UploadFolderResult, error)

	// PrepareDownloadFolder scans a remote folder and prepares download specs.
	PrepareDownloadFolder(ctx context.Context, remoteFolderID string, localPath string) ([]DownloadFileSpec, error)
}
