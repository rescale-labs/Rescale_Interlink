// Package cloud provides unified interfaces for cloud storage operations.
// This package defines the CloudTransfer interface that abstracts S3 and Azure
// implementations, enabling consistent behavior across storage backends with
// full support for transfer handles, concurrent operations, and resume capability.
package cloud

import (
	"io"

	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/transfer"
)

// ProgressCallback is called during transfers to report progress (0.0 to 1.0)
type ProgressCallback func(progress float64)

// DownloadParams consolidates all parameters for download operations.
// This unified struct replaces the multiple function signatures that existed before.
type DownloadParams struct {
	// Required fields
	RemotePath string // Cloud storage path (S3 key or Azure blob path)
	LocalPath  string // Where to save the decrypted file

	// File metadata (from API or cached)
	FileInfo *models.CloudFile

	// Optional: Transfer handle for concurrent chunk downloads
	// If nil or threads <= 1, uses sequential download
	TransferHandle *transfer.Transfer

	// Optional: Progress reporting
	// Called with values from 0.0 to 1.0
	ProgressCallback ProgressCallback

	// Optional: Output writer for status messages
	OutputWriter io.Writer
}

// UploadResult contains the result of a successful upload operation.
type UploadResult struct {
	// StoragePath is the path where the file was stored in cloud storage
	// For S3: the object key
	// For Azure: the blob path
	StoragePath string

	// EncryptionKey is the AES-256 key used to encrypt the file (32 bytes)
	EncryptionKey []byte

	// IV is the initialization vector the file was encrypted with (16 bytes)
	IV []byte
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
