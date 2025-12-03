// Package cloud provides unified interfaces for cloud storage operations.
// This package defines the CloudTransfer interface that abstracts S3 and Azure
// implementations, enabling consistent behavior across storage backends with
// full support for transfer handles, concurrent operations, and resume capability.
//
// Version: 3.2.0 (Sprint 1 - Unified Backend Architecture)
// Date: 2025-11-28
package cloud

import (
	"context"
	"io"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/transfer"
)

// ProgressCallback is called during transfers to report progress (0.0 to 1.0)
type ProgressCallback func(progress float64)

// UploadParams consolidates all parameters for upload operations.
// This unified struct replaces the multiple function signatures that existed before.
type UploadParams struct {
	// Required fields
	LocalPath string // Path to the local file to upload
	FolderID  string // Target folder ID (empty = MyLibrary)

	// API and credentials (provided by orchestrator)
	APIClient   *api.Client
	StorageInfo *models.StorageInfo

	// Optional: Transfer handle for concurrent part uploads
	// If nil or threads <= 1, uses sequential upload
	TransferHandle *transfer.Transfer

	// Optional: Progress reporting
	// Called with values from 0.0 to 1.0
	ProgressCallback ProgressCallback

	// Optional: Output writer for status messages
	OutputWriter io.Writer

	// Encryption mode
	// false (default) = streaming encryption (no temp file, saves disk space)
	// true = pre-encryption (creates temp file, compatible with legacy clients)
	PreEncrypt bool
}

// DownloadParams consolidates all parameters for download operations.
// This unified struct replaces the multiple function signatures that existed before.
type DownloadParams struct {
	// Required fields
	RemotePath string // Cloud storage path (S3 key or Azure blob path)
	LocalPath  string // Where to save the decrypted file

	// File metadata (from API or cached)
	FileInfo *models.CloudFile

	// API and credentials (provided by orchestrator)
	APIClient   *api.Client
	StorageInfo *models.StorageInfo

	// Optional: Transfer handle for concurrent chunk downloads
	// If nil or threads <= 1, uses sequential download
	TransferHandle *transfer.Transfer

	// Optional: Progress reporting
	// Called with values from 0.0 to 1.0
	ProgressCallback ProgressCallback

	// Optional: Output writer for status messages
	OutputWriter io.Writer

	// Options
	SkipChecksum bool // If true, warn but don't fail on checksum mismatch
}

// UploadResult contains the result of a successful upload operation.
type UploadResult struct {
	// StoragePath is the path where the file was stored in cloud storage
	// For S3: the object key
	// For Azure: the blob path
	StoragePath string

	// EncryptionKey is the AES-256 key used to encrypt the file (32 bytes)
	EncryptionKey []byte

	// IV is the initialization vector for legacy (v0) format
	// For streaming (v1) format, this may be empty as IV is derived per-part
	IV []byte

	// FormatVersion indicates the encryption format used
	// 0 = legacy (full-file CBC with single IV)
	// 1 = streaming (per-part encryption with key derivation)
	FormatVersion int

	// FileID is the unique identifier for streaming format (v1)
	// Used for per-part key derivation
	FileID string

	// PartSize is the part size used for streaming format (v1)
	PartSize int64
}

// CloudTransfer is the unified interface for cloud storage operations.
// Both S3Provider and AzureProvider implement this interface, providing
// a consistent API regardless of the underlying storage backend.
//
// Key design principles:
//   - Single interface for both upload and download
//   - Transfer handles enable concurrent operations
//   - Resume support for all operation types
//   - Streaming encryption as default (no temp files)
type CloudTransfer interface {
	// Upload uploads a file to cloud storage with optional concurrent part uploads.
	// Uses streaming encryption by default (no temp file), unless PreEncrypt is true.
	//
	// If TransferHandle is provided with multiple threads, parts are uploaded concurrently.
	// If TransferHandle is nil or has threads <= 1, upload is sequential.
	//
	// Returns UploadResult containing the storage path, encryption key, and format details.
	Upload(ctx context.Context, params UploadParams) (*UploadResult, error)

	// Download downloads and decrypts a file from cloud storage.
	// Automatically detects encryption format (legacy v0 or streaming v1).
	//
	// If TransferHandle is provided with multiple threads, chunks are downloaded concurrently.
	// If TransferHandle is nil or has threads <= 1, download is sequential.
	//
	// Resume support: If a partial download exists, resumes from last completed chunk.
	Download(ctx context.Context, params DownloadParams) error

	// RefreshCredentials refreshes storage credentials before they expire.
	// Called automatically by Upload/Download, but can be called manually for long operations.
	RefreshCredentials(ctx context.Context) error

	// StorageType returns the storage type this provider handles.
	// Returns "S3Storage" or "AzureStorage".
	StorageType() string
}

// CloudTransferFactory creates CloudTransfer instances based on storage type.
// This enables runtime selection of the appropriate provider.
type CloudTransferFactory interface {
	// NewTransfer creates a CloudTransfer for the specified storage type.
	// storageType should be "S3Storage" or "AzureStorage".
	NewTransfer(
		ctx context.Context,
		storageType string,
		storageInfo *models.StorageInfo,
		apiClient *api.Client,
	) (CloudTransfer, error)
}

// FileInfoSetter is an optional interface for providers that support cross-storage downloads.
// Sprint F.2: Enables downloading files from storage different than user's default.
//
// When a provider implements this interface, the download orchestrator calls SetFileInfo
// before any download operations. This allows the provider to fetch credentials for the
// file's specific storage rather than the user's default storage.
//
// Use cases:
//   - S3 user downloading job outputs stored in Azure
//   - Azure user downloading job outputs stored in S3
//   - Downloading files from platform-managed storage
type FileInfoSetter interface {
	// SetFileInfo sets the file info for cross-storage credential fetching.
	// Should be called before any download operations.
	// When set, the provider uses file-specific credentials instead of user's default.
	SetFileInfo(fileInfo *models.CloudFile)
}
