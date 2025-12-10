// Package s3 provides an S3 implementation of the CloudTransfer interface.
// This provider implements cloud storage operations directly using the S3Client,
// without depending on the legacy upload/download package implementations.
//
// Version: 3.2.4
// Date: 2025-12-10
package s3

import (
	"context"
	"fmt"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/models"
)

// Provider implements the CloudTransfer interface for S3 storage.
// Uses S3Client directly for all operations (no wrapper dependencies).
// Supports cross-storage downloads via stored fileInfo.
type Provider struct {
	storageInfo *models.StorageInfo
	apiClient   *api.Client

	// S3 client for all S3 operations (upload and download)
	// Created lazily on first use, protected by s3ClientMu
	s3Client   *S3Client
	s3ClientMu sync.Mutex

	// Stored fileInfo for cross-storage credential fetching.
	// When set, all subsequent operations use file-specific credentials.
	fileInfo *models.CloudFile
}

// NewProvider creates a new S3 provider.
// The uploader and downloader are lazily initialized on first upload/download.
func NewProvider(storageInfo *models.StorageInfo, apiClient *api.Client) (*Provider, error) {
	if storageInfo == nil {
		return nil, fmt.Errorf("storageInfo is required")
	}
	if apiClient == nil {
		return nil, fmt.Errorf("apiClient is required")
	}
	if storageInfo.StorageType != "S3Storage" {
		return nil, fmt.Errorf("invalid storage type: expected S3Storage, got %s", storageInfo.StorageType)
	}

	return &Provider{
		storageInfo: storageInfo,
		apiClient:   apiClient,
	}, nil
}

// SetFileInfo sets the file info for cross-storage credential fetching.
// This should be called by the download orchestrator before any download operations.
// When set, all subsequent operations (DetectFormat, DownloadStreaming, etc.) will use
// file-specific credentials, enabling cross-storage downloads (e.g., Azure user downloading
// S3-stored job outputs).
// Thread-safe: uses mutex protection.
func (p *Provider) SetFileInfo(fileInfo *models.CloudFile) {
	p.s3ClientMu.Lock()
	defer p.s3ClientMu.Unlock()

	p.fileInfo = fileInfo
	// Reset the cached client so next operation creates a new one with correct credentials
	p.s3Client = nil
}

// getOrCreateS3Client returns the S3 client, creating it if necessary.
// The client is cached for reuse across operations.
// Uses stored fileInfo for cross-storage credential fetching if available.
// Thread-safe: uses mutex protection.
func (p *Provider) getOrCreateS3Client(ctx context.Context) (*S3Client, error) {
	p.s3ClientMu.Lock()
	defer p.s3ClientMu.Unlock()

	if p.s3Client != nil {
		return p.s3Client, nil
	}

	// When fileInfo is set (via SetFileInfo), create client with file-specific credentials.
	// Otherwise, use nil for user's default storage (uploads, personal files).
	client, err := NewS3Client(p.storageInfo, p.apiClient, p.fileInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client: %w", err)
	}
	p.s3Client = client
	return client, nil
}

// getOrCreateS3ClientForFile returns an S3 client with file-specific credentials.
// This is used for downloads where we need credentials for the file's storage,
// which may be different from the user's default storage (e.g., job outputs).
func (p *Provider) getOrCreateS3ClientForFile(ctx context.Context, fileInfo *models.CloudFile) (*S3Client, error) {
	// If no fileInfo provided, fall back to default client
	if fileInfo == nil {
		return p.getOrCreateS3Client(ctx)
	}

	// Create a client with file-specific credentials
	// Note: This client is NOT cached because different files may need different credentials
	client, err := NewS3Client(p.storageInfo, p.apiClient, fileInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client for file: %w", err)
	}
	return client, nil
}

// Upload uploads a file to S3 storage.
// Uses streaming encryption by default (no temp file), unless PreEncrypt is true.
// If TransferHandle is provided with multiple threads, parts are uploaded concurrently.
//
// This method delegates to the transfer orchestrator, which calls back to the provider's
// interface methods (InitStreamingUpload, UploadEncryptedFile, etc.).
func (p *Provider) Upload(ctx context.Context, params cloud.UploadParams) (*cloud.UploadResult, error) {
	// Ensure S3 client is initialized
	_, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return nil, err
	}

	// The actual upload is handled by the transfer orchestrator, which calls:
	// - InitStreamingUpload/UploadStreamingPart/CompleteStreamingUpload for streaming mode
	// - UploadEncryptedFile for pre-encrypt mode
	//
	// For direct calls (not through orchestrator), delegate to appropriate method based on mode.
	// This maintains backward compatibility with code that calls provider.Upload() directly.

	if params.PreEncrypt {
		// Pre-encrypt mode: file is encrypted first, then uploaded
		// This is handled by the transfer orchestrator calling UploadEncryptedFile
		return nil, fmt.Errorf("pre-encrypt uploads should go through transfer.Uploader, not provider.Upload directly")
	}

	// Streaming mode: encrypt on-the-fly
	// This is handled by the transfer orchestrator calling streaming interface methods
	return nil, fmt.Errorf("streaming uploads should go through transfer.Uploader, not provider.Upload directly")
}

// Download downloads and decrypts a file from S3 storage.
// Automatically detects encryption format (legacy v0 or streaming v1).
// If TransferHandle is provided with multiple threads, chunks are downloaded concurrently.
func (p *Provider) Download(ctx context.Context, params cloud.DownloadParams) error {
	// The download is handled by the transfer orchestrator, which calls:
	// - DetectFormat to determine format version
	// - DownloadStreaming for v1 streaming format
	// - DownloadEncryptedFile for v0 legacy format
	//
	// This method should NOT be called directly. If it is, return an error
	// pointing to the correct path through the orchestrator.
	return fmt.Errorf("downloads should go through transfer.Downloader orchestrator, not provider.Download directly; use DetectFormat + DownloadStreaming or DownloadEncryptedFile instead")
}

// RefreshCredentials refreshes S3 storage credentials before they expire.
func (p *Provider) RefreshCredentials(ctx context.Context) error {
	// Credentials are refreshed on each Upload/Download call through the credential manager
	// This method allows explicit refresh for long-running operations
	_, err := p.getS3Credentials(ctx)
	return err
}

// StorageType returns "S3Storage".
func (p *Provider) StorageType() string {
	return "S3Storage"
}

// getS3Credentials retrieves S3 credentials from the API.
func (p *Provider) getS3Credentials(ctx context.Context) (*models.S3Credentials, error) {
	creds, _, err := p.apiClient.GetStorageCredentials(ctx, nil)
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return nil, fmt.Errorf("no S3 credentials returned")
	}
	return creds, nil
}

// Compile-time interface verification
var _ cloud.CloudTransfer = (*Provider)(nil)
