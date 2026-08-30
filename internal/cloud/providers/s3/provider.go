// Package s3 provides an S3 implementation of the CloudTransfer interface.
// This provider implements cloud storage operations directly using the S3Client.
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

	// retryObserver is handed to each client so retries deep in the transfer
	// stack reach whoever started it. Every access here is under s3ClientMu,
	// because SetRetryObserver may land while another goroutine is creating a
	// client. The clients themselves copy it at construction and never write it,
	// so their concurrent part workers read it without a lock.
	retryObserver cloud.RetryObserver
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

// SetRetryObserver routes retry notices from this provider's operations back to
// the caller. Call before the first transfer; a client already created keeps the
// observer it was built with.
func (p *Provider) SetRetryObserver(obs cloud.RetryObserver) {
	p.s3ClientMu.Lock()
	defer p.s3ClientMu.Unlock()
	p.retryObserver = obs
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
	client, err := NewS3Client(ctx, p.storageInfo, p.apiClient, p.fileInfo, p.retryObserver)
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
	p.s3ClientMu.Lock()
	obs := p.retryObserver
	p.s3ClientMu.Unlock()

	client, err := NewS3Client(ctx, p.storageInfo, p.apiClient, fileInfo, obs)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client for file: %w", err)
	}
	return client, nil
}

// StorageType returns "S3Storage".
func (p *Provider) StorageType() string {
	return "S3Storage"
}

// Compile-time interface verification
var _ cloud.CloudTransfer = (*Provider)(nil)

// Compile-time check: the provider can receive a retry observer.
var _ cloud.RetryObserverSetter = (*Provider)(nil)
