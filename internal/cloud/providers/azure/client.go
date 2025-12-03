// Package azure provides an Azure implementation of the CloudTransfer interface.
// This file contains the Azure client factory with auto-refreshing credentials.
//
// Phase 7G: This file contains the core Azure client logic that was previously
// duplicated in upload/azure.go and download/azure.go. The provider files now
// use this directly instead of wrapping upload.NewAzureUploader().
//
// Version: 3.2.0 (Sprint 7G - Azure True Consolidation)
// Date: 2025-11-29
package azure

import (
	"context"
	"fmt"
	"log"
	nethttp "net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
)

// AzureClient wraps the Azure blob client with auto-refreshing credentials and connection pooling.
// This is the core Azure client used by all provider operations (streaming, pre-encrypt, download).
//
// Thread-safe: All operations are safe for concurrent use.
//
// Cross-storage support (Sprint F.2):
// When fileInfo is provided, the client fetches credentials for that file's specific storage,
// enabling cross-storage downloads (e.g., S3 user downloading Azure-stored job outputs).
// When fileInfo is nil, the client uses the user's default storage credentials.
type AzureClient struct {
	client      *azblob.Client
	storageInfo *models.StorageInfo
	credManager *credentials.Manager
	apiClient   *api.Client          // For file-specific credential fetching
	fileInfo    *models.CloudFile    // Optional: for cross-storage downloads
	httpClient  *nethttp.Client      // Shared HTTP client for connection reuse
	clientMu    sync.Mutex           // Protects client updates during credential refresh

	// For periodic refresh goroutine cancellation
	cancelRefresh context.CancelFunc
	refreshMu     sync.Mutex
	refreshWg     sync.WaitGroup // Ensures goroutine exits before stopPeriodicRefresh returns
}

// NewAzureClient creates a new Azure client with auto-refreshing credentials.
// This is the replacement for upload.NewAzureUploader() and download.NewAzureDownloader() client creation logic.
//
// The client:
//   - Uses the global credential manager for auto-refresh (shared across operations)
//   - Maintains a connection pool via the shared HTTP client
//   - Is thread-safe for concurrent operations
//
// Parameters:
//   - storageInfo: Azure storage configuration (container, account name)
//   - apiClient: Rescale API client for credential refresh
//   - fileInfo: Optional file info for cross-storage downloads (nil for uploads or default storage)
//
// Cross-storage support (Sprint F.2):
// When fileInfo is provided (non-nil), the client fetches credentials for that file's specific
// storage rather than the user's default storage. This enables scenarios like:
//   - S3 user downloading job outputs stored in Azure
//   - Azure user downloading job outputs stored in S3
//
// The pattern mirrors S3Client's handling of file-specific credentials.
func NewAzureClient(storageInfo *models.StorageInfo, apiClient *api.Client, fileInfo *models.CloudFile) (*AzureClient, error) {
	if storageInfo == nil {
		return nil, fmt.Errorf("storageInfo is required")
	}
	if apiClient == nil {
		return nil, fmt.Errorf("apiClient is required")
	}

	// Create shared optimized HTTP client with proxy support from API client config
	// IMPORTANT: Reuse this client across credential refreshes to maintain connection pool
	purCfg := apiClient.GetConfig()
	httpClient, err := http.CreateOptimizedClient(purCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Get the global credential manager (for user's default storage)
	credManager := credentials.GetManager(apiClient)

	// Get initial credentials
	// Sprint F.2: Support file-specific credentials for cross-storage downloads
	var creds *models.AzureCredentials
	if fileInfo != nil {
		// File-specific credentials: fetch for the file's storage, not user's default
		// This enables cross-storage (e.g., S3 user downloading Azure-stored job outputs)
		_, azureCreds, err := apiClient.GetStorageCredentials(context.Background(), fileInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to get Azure credentials for file %s: %w", fileInfo.ID, err)
		}
		creds = azureCreds
	} else {
		// Default storage credentials (user's Azure storage)
		creds, err = credManager.GetAzureCredentials(context.Background())
		if err != nil {
			return nil, fmt.Errorf("failed to get initial Azure credentials: %w", err)
		}
	}

	if creds == nil {
		if fileInfo != nil {
			return nil, fmt.Errorf("Azure credentials not available for file %s (file may be stored in S3)", fileInfo.ID)
		}
		return nil, fmt.Errorf("Azure credentials not available (user may have S3 storage)")
	}

	// Build SAS URL
	sasURL, err := buildSASURL(storageInfo, creds)
	if err != nil {
		return nil, err
	}

	// Create Azure client with custom HTTP transport
	client, err := azblob.NewClientWithNoCredential(sasURL, &azblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: httpClient, // Critical: Preserve connection pool
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure client: %w", err)
	}

	return &AzureClient{
		client:      client,
		storageInfo: storageInfo,
		credManager: credManager,
		apiClient:   apiClient,   // Store for file-specific credential refresh
		fileInfo:    fileInfo,    // Store for file-specific credential refresh
		httpClient:  httpClient,
	}, nil
}

// buildSASURL constructs the Azure SAS URL from storage info and credentials.
func buildSASURL(storageInfo *models.StorageInfo, creds *models.AzureCredentials) (string, error) {
	// Azure credentials may provide URL in Paths array or we need to construct it
	var sasURL string
	if len(creds.Paths) > 0 && creds.Paths[0] != "" {
		// Use the full URL from Paths (includes account name and container)
		sasURL = creds.Paths[0]
		// Ensure SAS token is appended
		if !strings.Contains(sasURL, "?") {
			sasURL = sasURL + "?" + creds.SASToken
		}
	} else {
		// Build URL from ConnectionSettings.AccountName (as per Python reference implementation)
		// The accountName field comes from /api/v3/users/me/ response:
		// default_storage.connection_settings.accountName
		accountName := storageInfo.ConnectionSettings.AccountName

		if accountName == "" {
			// Fallback to legacy field name
			accountName = storageInfo.ConnectionSettings.StorageAccount
		}

		if accountName == "" {
			return "", fmt.Errorf("Azure storage account name not found in ConnectionSettings.AccountName")
		}

		// Python pattern: BlobServiceClient with account URL only (no container in URL)
		// Format: https://{account}.blob.core.windows.net?{sas_token}
		sasURL = fmt.Sprintf("https://%s.blob.core.windows.net/?%s", accountName, creds.SASToken)
	}

	return sasURL, nil
}

// Client returns the underlying Azure blob client.
// Thread-safe: Returns the current client under mutex protection.
func (c *AzureClient) Client() *azblob.Client {
	c.clientMu.Lock()
	defer c.clientMu.Unlock()
	return c.client
}

// StorageInfo returns the storage configuration.
func (c *AzureClient) StorageInfo() *models.StorageInfo {
	return c.storageInfo
}

// Container returns the Azure container name.
func (c *AzureClient) Container() string {
	return c.storageInfo.ConnectionSettings.Container
}

// EnsureFreshCredentials refreshes Azure credentials using the appropriate method.
// This is thread-safe and shares credentials across all concurrent operations.
// IMPORTANT: Reuses the existing HTTP client to maintain connection pool.
//
// Cross-storage support (Sprint F.2):
// When fileInfo was provided during client creation, refreshes credentials for that
// file's specific storage (not user's default). This maintains cross-storage access
// during long-running operations.
func (c *AzureClient) EnsureFreshCredentials(ctx context.Context) error {
	// Get fresh credentials using the appropriate method
	// Sprint F.2: Support file-specific credentials for cross-storage downloads
	var creds *models.AzureCredentials
	var err error

	if c.fileInfo != nil && c.apiClient != nil {
		// File-specific credentials: fetch for the file's storage, not user's default
		_, azureCreds, err := c.apiClient.GetStorageCredentials(ctx, c.fileInfo)
		if err != nil {
			return fmt.Errorf("failed to refresh Azure credentials for file: %w", err)
		}
		creds = azureCreds
	} else {
		// Default storage credentials (user's Azure storage)
		creds, err = c.credManager.GetAzureCredentials(ctx)
		if err != nil {
			return fmt.Errorf("failed to get Azure credentials: %w", err)
		}
	}

	if creds == nil {
		return fmt.Errorf("received nil Azure credentials")
	}

	// Build new SAS URL with fresh token
	sasURL, err := buildSASURL(c.storageInfo, creds)
	if err != nil {
		return err
	}

	// Lock to prevent concurrent client updates
	c.clientMu.Lock()
	defer c.clientMu.Unlock()

	// Recreate client with new SAS token BUT reuse HTTP transport
	client, err := azblob.NewClientWithNoCredential(sasURL, &azblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: c.httpClient, // CRITICAL: Reuse same HTTP client!
		},
	})
	if err != nil {
		return fmt.Errorf("failed to recreate Azure client: %w", err)
	}

	c.client = client
	return nil
}

// RetryWithBackoff executes a function with exponential backoff retry logic.
// Uses the shared retry package for consistent retry behavior across all operations.
func (c *AzureClient) RetryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	retryConfig := http.Config{
		MaxRetries:   constants.MaxRetries,
		InitialDelay: constants.RetryInitialDelay,
		MaxDelay:     constants.RetryMaxDelay,
		CredentialRefresh: func(ctx context.Context) error {
			return c.EnsureFreshCredentials(ctx)
		},
		OnRetry: func(attempt int, err error, errorType http.ErrorType) {
			// Log retry attempts for debugging
			if os.Getenv("DEBUG_RETRY") == "true" {
				log.Printf("[RETRY] %s: attempt %d/%d, error type: %s, error: %v",
					operation, attempt, constants.MaxRetries, http.ErrorTypeName(errorType), err)
			}
		},
	}

	return http.ExecuteWithRetry(ctx, retryConfig, fn)
}

// =============================================================================
// Periodic Refresh (Layer 2 of 3-layer credential strategy)
// =============================================================================

// periodicRefresh refreshes credentials every azurePeriodicRefreshInterval for long-running operations.
// This is Layer 2 of the 3-layer credential refresh strategy.
func (c *AzureClient) periodicRefresh(ctx context.Context, operationCtx context.Context) {
	defer c.refreshWg.Done()

	ticker := time.NewTicker(constants.AzurePeriodicRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := c.EnsureFreshCredentials(ctx); err != nil {
				// Log error but don't fail - Layer 3 (error-driven) will catch auth failures
				if os.Getenv("DEBUG_RETRY") == "true" {
					log.Printf("[REFRESH] Periodic credential refresh failed: %v", err)
				}
			}
		case <-operationCtx.Done():
			return
		}
	}
}

// StartPeriodicRefresh starts background credential refresh for large file operations.
// Returns a cancellable context - the caller should defer StopPeriodicRefresh().
func (c *AzureClient) StartPeriodicRefresh(ctx context.Context) context.Context {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	// Create cancellable context for refresh goroutine
	refreshCtx, cancel := context.WithCancel(ctx)
	c.cancelRefresh = cancel

	// Increment WaitGroup BEFORE starting goroutine to prevent race
	c.refreshWg.Add(1)

	// Start periodic refresh goroutine
	go c.periodicRefresh(ctx, refreshCtx)

	return refreshCtx
}

// StopPeriodicRefresh stops background credential refresh and waits for goroutine to exit.
func (c *AzureClient) StopPeriodicRefresh() {
	c.refreshMu.Lock()
	cancel := c.cancelRefresh
	c.cancelRefresh = nil
	c.refreshMu.Unlock()

	if cancel != nil {
		cancel()
		// Wait for goroutine to exit before returning
		// This prevents goroutine leaks and ensures clean shutdown
		c.refreshWg.Wait()
	}
}

// =============================================================================
// Blob Operations
// =============================================================================

// BlobProperties contains the properties of an Azure blob.
type BlobProperties struct {
	ContentLength int64
	ETag          string
	Metadata      map[string]*string
}

// GetBlobProperties retrieves blob properties (size, metadata) from Azure.
// Uses retry logic with credential refresh.
func (c *AzureClient) GetBlobProperties(ctx context.Context, blobPath string) (*BlobProperties, error) {
	var props BlobProperties
	err := c.RetryWithBackoff(ctx, "GetBlobProperties", func() error {
		c.clientMu.Lock()
		client := c.client
		c.clientMu.Unlock()

		blobClient := client.ServiceClient().NewContainerClient(c.Container()).NewBlobClient(blobPath)
		resp, err := blobClient.GetProperties(ctx, nil)
		if err != nil {
			return err
		}
		if resp.ContentLength != nil {
			props.ContentLength = *resp.ContentLength
		}
		if resp.ETag != nil {
			props.ETag = string(*resp.ETag)
		}
		props.Metadata = resp.Metadata
		return nil
	})
	return &props, err
}

// DownloadStream downloads a blob stream from Azure.
// Uses retry logic with credential refresh.
func (c *AzureClient) DownloadStream(ctx context.Context, blobPath string, options *azblob.DownloadStreamOptions) (azblob.DownloadStreamResponse, error) {
	var resp azblob.DownloadStreamResponse
	err := c.RetryWithBackoff(ctx, "DownloadStream", func() error {
		c.clientMu.Lock()
		client := c.client
		c.clientMu.Unlock()

		blobClient := client.ServiceClient().NewContainerClient(c.Container()).NewBlobClient(blobPath)
		r, err := blobClient.DownloadStream(ctx, options)
		resp = r
		return err
	})
	return resp, err
}

// DownloadRange downloads a range of bytes from an Azure blob.
// Uses retry logic with credential refresh.
func (c *AzureClient) DownloadRange(ctx context.Context, blobPath string, offset, count int64) (azblob.DownloadStreamResponse, error) {
	return c.DownloadStream(ctx, blobPath, &azblob.DownloadStreamOptions{
		Range: azblob.HTTPRange{
			Offset: offset,
			Count:  count,
		},
	})
}

// GetBlockBlobClient returns a block blob client for the specified path.
// Thread-safe: Uses current client under mutex protection.
func (c *AzureClient) GetBlockBlobClient(blobPath string) interface{} {
	c.clientMu.Lock()
	defer c.clientMu.Unlock()
	return c.client.ServiceClient().NewContainerClient(c.Container()).NewBlockBlobClient(blobPath)
}

// GetBlockList gets the list of uncommitted blocks for a blob.
// Used for resume validation.
func (c *AzureClient) GetBlockList(ctx context.Context, blobPath string) ([]string, error) {
	c.clientMu.Lock()
	client := c.client
	c.clientMu.Unlock()

	blockBlobClient := client.ServiceClient().NewContainerClient(c.Container()).NewBlockBlobClient(blobPath)

	var blockIDs []string
	err := c.RetryWithBackoff(ctx, "GetBlockList", func() error {
		resp, err := blockBlobClient.GetBlockList(ctx, "uncommitted", nil)
		if err != nil {
			return err
		}
		if resp.UncommittedBlocks != nil {
			blockIDs = make([]string, len(resp.UncommittedBlocks))
			for i, block := range resp.UncommittedBlocks {
				if block.Name != nil {
					blockIDs[i] = *block.Name
				}
			}
		}
		return nil
	})
	return blockIDs, err
}
