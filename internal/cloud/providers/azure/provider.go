// Package azure provides an Azure implementation of the CloudTransfer interface.
// This provider uses AzureClient directly for all operations.
package azure

import (
	"context"
	"fmt"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/models"
)

// Provider implements the CloudTransfer interface for Azure storage.
// Uses AzureClient directly for all Azure operations.
// Supports cross-storage downloads via stored fileInfo.
type Provider struct {
	storageInfo *models.StorageInfo
	apiClient   *api.Client

	// Lazy-initialized Azure client, protected by azureClientMu
	azureClient   *AzureClient
	azureClientMu sync.Mutex

	// Stored fileInfo for cross-storage credential fetching.
	// When set, all subsequent operations use file-specific credentials.
	fileInfo *models.CloudFile
}

// NewProvider creates a new Azure provider.
// The AzureClient is lazily initialized on first use.
func NewProvider(storageInfo *models.StorageInfo, apiClient *api.Client) (*Provider, error) {
	if storageInfo == nil {
		return nil, fmt.Errorf("storageInfo is required")
	}
	if apiClient == nil {
		return nil, fmt.Errorf("apiClient is required")
	}
	if storageInfo.StorageType != "AzureStorage" {
		return nil, fmt.Errorf("invalid storage type: expected AzureStorage, got %s", storageInfo.StorageType)
	}

	return &Provider{
		storageInfo: storageInfo,
		apiClient:   apiClient,
	}, nil
}

// SetFileInfo sets the file info for cross-storage credential fetching.
// This should be called by the download orchestrator before any download operations.
// When set, all subsequent operations (DetectFormat, DownloadStreaming, etc.) will use
// file-specific credentials, enabling cross-storage downloads (e.g., S3 user downloading
// Azure-stored job outputs).
// Thread-safe: uses mutex protection.
func (p *Provider) SetFileInfo(fileInfo *models.CloudFile) {
	p.azureClientMu.Lock()
	defer p.azureClientMu.Unlock()

	p.fileInfo = fileInfo

	// Reset the cached client so next operation creates a new one with correct credentials
	// This is safe because the lock prevents concurrent access
	p.azureClient = nil
}

// getOrCreateAzureClient returns the existing AzureClient or creates a new one.
// Thread-safe: Uses mutex protection.
// Uses stored fileInfo for cross-storage credential fetching if available.
func (p *Provider) getOrCreateAzureClient(ctx context.Context) (*AzureClient, error) {
	p.azureClientMu.Lock()
	defer p.azureClientMu.Unlock()

	if p.azureClient != nil {
		return p.azureClient, nil
	}

	// When fileInfo is set (via SetFileInfo), create client with file-specific credentials.
	// Otherwise, use nil for user's default storage (uploads, personal files).
	client, err := NewAzureClient(ctx, p.storageInfo, p.apiClient, p.fileInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure client: %w", err)
	}
	p.azureClient = client
	return client, nil
}

// getOrCreateAzureClientForFile returns an Azure client for file-specific downloads.
// Properly supports cross-storage downloads by passing fileInfo.
//
// Cross-storage support:
// When fileInfo is provided (non-nil), creates a new client with credentials for that
// file's specific storage. This enables scenarios like:
//   - S3 user downloading job outputs stored in Azure
//   - Azure user downloading job outputs stored in a different Azure account
//
// The pattern mirrors S3Provider's getOrCreateS3ClientForFile() for consistency.
func (p *Provider) getOrCreateAzureClientForFile(ctx context.Context, fileInfo *models.CloudFile) (*AzureClient, error) {
	// If no fileInfo provided, fall back to default client (user's storage)
	if fileInfo == nil {
		return p.getOrCreateAzureClient(ctx)
	}

	// Create a client with file-specific credentials
	// Note: This client is NOT cached because different files may need different credentials
	// (same pattern as S3Provider.getOrCreateS3ClientForFile)
	client, err := NewAzureClient(ctx, p.storageInfo, p.apiClient, fileInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure client for file: %w", err)
	}
	return client, nil
}

// Upload uploads a file to Azure storage.
// Returns error - actual upload is handled by the orchestrator through specific interfaces.
// Use UploadEncryptedFile (pre-encrypt) or streaming upload methods.
func (p *Provider) Upload(ctx context.Context, params cloud.UploadParams) (*cloud.UploadResult, error) {
	return nil, fmt.Errorf("use UploadEncryptedFile or streaming upload methods")
}

// Download downloads and decrypts a file from Azure storage.
// Returns error - actual download is handled by the orchestrator through specific interfaces.
// Use DownloadEncryptedFile (legacy) or DownloadStreaming methods.
func (p *Provider) Download(ctx context.Context, params cloud.DownloadParams) error {
	return fmt.Errorf("use DownloadEncryptedFile or DownloadStreaming methods")
}

// RefreshCredentials refreshes Azure storage credentials before they expire.
func (p *Provider) RefreshCredentials(ctx context.Context) error {
	client, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return err
	}
	return client.EnsureFreshCredentials(ctx)
}

// StorageType returns "AzureStorage".
func (p *Provider) StorageType() string {
	return "AzureStorage"
}

// Compile-time interface verification
var _ cloud.CloudTransfer = (*Provider)(nil)
