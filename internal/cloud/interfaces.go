// Package cloud provides unified interfaces for cloud storage operations.
// This package defines the CloudTransfer interface that abstracts S3 and Azure
// implementations, enabling consistent behavior across storage backends with
// full support for transfer handles, concurrent operations, and resume capability.
package cloud

import (
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

// CloudTransfer is what every cloud storage provider has in common.
// Both S3Provider and AzureProvider implement it.
//
// Transfer work itself is reached through the optional capability interfaces
// the orchestrators type-assert on the provider they hold: FileInfoSetter and
// RetryObserverSetter in this package, and StreamingConcurrentUploader,
// PreEncryptUploader, StreamingConcurrentDownloader, StreamingPartDownloader
// and LegacyDownloader in internal/cloud/transfer.
type CloudTransfer interface {
	// StorageType returns the storage type this provider handles.
	// Returns "S3Storage" or "AzureStorage".
	StorageType() string
}

// FileInfoSetter is an optional interface for providers that support cross-storage downloads.
// Enables downloading files from storage different than user's default.
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
