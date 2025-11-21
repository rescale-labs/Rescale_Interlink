package download

import (
	"context"
	"fmt"
	"io"
	"log"
	nethttp "net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/storage"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
)

// Local aliases for shared constants
const (
	azureRangeChunkSize          = constants.ChunkSize
	azureDownloadThreshold       = constants.MultipartThreshold
	azureMaxRetries              = constants.MaxRetries
	azureRetryInitialDelay       = constants.RetryInitialDelay
	azureRetryMaxDelay           = constants.RetryMaxDelay
	azurePeriodicRefreshInterval = constants.AzurePeriodicRefreshInterval
	azureLargeFileThreshold      = constants.LargeFileThreshold
)

// AzureDownloader handles Azure blob downloads with decryption, retry logic, and credential refresh
type AzureDownloader struct {
	client        *azblob.Client
	storageInfo   *models.StorageInfo
	credManager   *credentials.Manager
	httpClient    *nethttp.Client // Shared HTTP client for connection reuse
	apiClient     *api.Client     // For checksum verification
	clientMu      sync.Mutex      // Protects client updates during credential refresh
	cancelRefresh context.CancelFunc
	refreshMu     sync.Mutex
}

// NewAzureDownloader creates a new Azure downloader with credential refresh capability
func NewAzureDownloader(storageInfo *models.StorageInfo, creds *models.AzureCredentials, apiClient *api.Client) (*AzureDownloader, error) {
	// Create shared optimized HTTP client with proxy support from API client config
	purCfg := apiClient.GetConfig()
	httpClient, err := http.CreateOptimizedClient(purCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Build SAS URL WITHOUT container (Python pattern: BlobServiceClient with account URL only)
	accountName := storageInfo.ConnectionSettings.AccountName

	// Create client with account URL and SAS token (no container in URL)
	sasURL := fmt.Sprintf("https://%s.blob.core.windows.net/?%s",
		accountName, creds.SASToken)

	// Create Azure client with shared HTTP transport for connection reuse
	client, err := azblob.NewClientWithNoCredential(sasURL, &azblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: httpClient, // CRITICAL: Reuse HTTP client for connection pool
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure blob client: %w", err)
	}

	// Get the global credential manager
	credManager := credentials.GetManager(apiClient)

	return &AzureDownloader{
		client:      client,
		storageInfo: storageInfo,
		credManager: credManager,
		httpClient:  httpClient,
		apiClient:   apiClient,
	}, nil
}

// ensureFreshCredentials refreshes Azure credentials and recreates client
// This is called proactively before operations (Layer 1) and on auth errors (Layer 3)
func (d *AzureDownloader) ensureFreshCredentials(ctx context.Context) error {
	// Get fresh credentials from global manager (auto-refreshes if needed)
	azureCreds, err := d.credManager.GetAzureCredentials(ctx)
	if err != nil {
		return fmt.Errorf("failed to get Azure credentials: %w", err)
	}

	if azureCreds == nil {
		return fmt.Errorf("received nil Azure credentials")
	}

	// Update Azure client with fresh SAS token
	d.clientMu.Lock()
	defer d.clientMu.Unlock()

	accountName := d.storageInfo.ConnectionSettings.AccountName

	// Build SAS URL without container (Python pattern)
	sasURL := fmt.Sprintf("https://%s.blob.core.windows.net/?%s",
		accountName, azureCreds.SASToken)

	// CRITICAL: Reuse the same HTTP client to preserve connection pool
	client, err := azblob.NewClientWithNoCredential(sasURL, &azblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: d.httpClient, // Reuse HTTP client!
		},
	})
	if err != nil {
		return fmt.Errorf("failed to recreate Azure client: %w", err)
	}

	d.client = client

	return nil
}

// periodicRefresh refreshes credentials in background for large file downloads (Layer 2)
// This prevents credential expiry during long-running operations (>1GB files)
func (d *AzureDownloader) periodicRefresh(ctx context.Context, downloadCtx context.Context) {
	ticker := time.NewTicker(azurePeriodicRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Refresh credentials
			if err := d.ensureFreshCredentials(ctx); err != nil {
				log.Printf("Warning: Periodic credential refresh failed: %v", err)
			}
		case <-downloadCtx.Done():
			// Download completed or cancelled
			return
		}
	}
}

// retryWithBackoff executes a function with exponential backoff retry logic using shared retry package
func (d *AzureDownloader) retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	config := http.Config{
		MaxRetries:   azureMaxRetries,
		InitialDelay: azureRetryInitialDelay,
		MaxDelay:     azureRetryMaxDelay,
		CredentialRefresh: func(ctx context.Context) error {
			return d.ensureFreshCredentials(ctx)
		},
		OnRetry: func(attempt int, err error, errorType http.ErrorType) {
			// Optional: log retry attempts for debugging
			if os.Getenv("DEBUG_RETRY") == "true" {
				log.Printf("[RETRY] %s: attempt %d/%d, error type: %s, error: %v",
					operation, attempt, azureMaxRetries, http.ErrorTypeName(errorType), err)
			}
		},
	}

	return http.ExecuteWithRetry(ctx, config, fn)
}

// DownloadAndDecrypt downloads a blob from Azure and decrypts it with retry logic
// Returns error if download or decryption fails
// If iv is nil, it will be retrieved from blob metadata
func (d *AzureDownloader) DownloadAndDecrypt(ctx context.Context, blobPath, localPath string, encryptionKey, iv []byte) error {
	// LAYER 1: Proactive credential refresh before operation
	if err := d.ensureFreshCredentials(ctx); err != nil {
		return fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get file size and IV from blob properties (with retry)
	// Following Python pattern: client.get_blob_client(container=..., blob=...)
	var fileSize int64
	container := d.storageInfo.ConnectionSettings.Container

	err := d.retryWithBackoff(ctx, "GetBlobProperties", func() error {
		d.clientMu.Lock()
		client := d.client
		d.clientMu.Unlock()

		// Python pattern: get_blob_client with container and blob specified separately
		blobClient := client.ServiceClient().NewContainerClient(container).NewBlobClient(blobPath)
		propsResp, err := blobClient.GetProperties(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to get blob properties: %w", err)
		}

		if propsResp.ContentLength == nil {
			return fmt.Errorf("blob has no content length")
		}
		fileSize = *propsResp.ContentLength

		// If IV not provided, get it from metadata (Python pattern: metadata["iv"])
		if iv == nil {
			// Azure normalizes metadata keys - try both "iv" (Python) and "Iv" (Go SDK returns)
			var ivStr *string
			var ok bool
			if ivStr, ok = propsResp.Metadata["iv"]; !ok {
				ivStr, ok = propsResp.Metadata["Iv"] // Azure SDK may return title-cased keys
			}

			if ok && ivStr != nil {
				var decodeErr error
				iv, decodeErr = encryption.DecodeBase64(*ivStr)
				if decodeErr != nil {
					return fmt.Errorf("failed to decode IV from metadata: %w", decodeErr)
				}
			} else {
				// Build debug info
				var metadataKeys []string
				for k := range propsResp.Metadata {
					metadataKeys = append(metadataKeys, k)
				}
				return fmt.Errorf("IV not provided and not found in blob metadata (available keys: %v, blob: %s, container: %s)",
					metadataKeys, blobPath, container)
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	// CHECK DISK SPACE before download (with 15% safety buffer)
	if err := diskspace.CheckAvailableSpace(localPath, fileSize, 1.15); err != nil {
		return &diskspace.InsufficientSpaceError{
			Path:           localPath,
			RequiredBytes:  fileSize,
			AvailableBytes: diskspace.GetAvailableSpace(filepath.Dir(localPath)),
		}
	}

	// Create temp file in same directory as target (not in /tmp)
	targetDir := filepath.Dir(localPath)
	encryptedPath := localPath + ".encrypted"

	// Ensure cleanup of temp file
	defer func() {
		if removeErr := os.Remove(encryptedPath); removeErr != nil && !os.IsNotExist(removeErr) {
			fmt.Fprintf(os.Stderr, "Warning: Failed to cleanup encrypted temp file %s: %v\n", encryptedPath, removeErr)
		}
	}()

	// LAYER 2: Start periodic refresh for large files (>1GB)
	var downloadCtx context.Context
	var downloadCancel context.CancelFunc
	if fileSize > azureLargeFileThreshold {
		downloadCtx, downloadCancel = context.WithCancel(ctx)
		d.refreshMu.Lock()
		d.cancelRefresh = downloadCancel
		d.refreshMu.Unlock()

		go d.periodicRefresh(ctx, downloadCtx)

		defer func() {
			d.refreshMu.Lock()
			if d.cancelRefresh != nil {
				d.cancelRefresh()
				d.cancelRefresh = nil
			}
			d.refreshMu.Unlock()
		}()
	}

	// Choose download method based on file size
	if fileSize > azureDownloadThreshold {
		// Use chunked download for large files
		err = d.downloadChunked(ctx, blobPath, encryptedPath, fileSize)
	} else {
		// Use single request for small files
		err = d.downloadSingle(ctx, blobPath, encryptedPath)
	}

	if err != nil {
		// Convert disk full errors to standard type
		if storage.IsDiskFullError(err) {
			return &diskspace.InsufficientSpaceError{
				Path:           encryptedPath,
				RequiredBytes:  fileSize,
				AvailableBytes: diskspace.GetAvailableSpace(targetDir),
			}
		}
		return fmt.Errorf("download failed: %w", err)
	}

	// Decrypt the file
	if err := encryption.DecryptFile(encryptedPath, localPath, encryptionKey, iv); err != nil {
		// Check for disk full during decryption
		if storage.IsDiskFullError(err) {
			return &diskspace.InsufficientSpaceError{
				Path:           localPath,
				RequiredBytes:  fileSize,
				AvailableBytes: diskspace.GetAvailableSpace(targetDir),
			}
		}
		return fmt.Errorf("decryption failed: %w", err)
	}

	return nil
}

// DownloadAndDecryptWithProgress downloads a blob from Azure and decrypts it with progress reporting
// progressCallback receives progress as a float64 between 0.0 and 1.0
func (d *AzureDownloader) DownloadAndDecryptWithProgress(ctx context.Context, blobPath, localPath string, encryptionKey, iv []byte, progressCallback func(float64)) error {
	if progressCallback == nil {
		// No progress callback, use standard method
		return d.DownloadAndDecrypt(ctx, blobPath, localPath, encryptionKey, iv)
	}

	// Report 0% at start
	progressCallback(0.0)

	// TIER 2: Opportunistic cleanup of expired resume states in this directory
	dir := filepath.Dir(localPath)
	CleanupExpiredResumesInDirectory(dir, false)

	// LAYER 1: Proactive credential refresh before operation
	if err := d.ensureFreshCredentials(ctx); err != nil {
		return fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get blob properties (with retry)
	d.clientMu.Lock()
	client := d.client
	d.clientMu.Unlock()

	blobClient := client.ServiceClient().NewContainerClient(d.storageInfo.ConnectionSettings.Container).NewBlobClient(blobPath)

	var fileSize int64
	var currentETag string
	err := d.retryWithBackoff(ctx, "GetBlobProperties", func() error {
		propsResp, err := blobClient.GetProperties(ctx, nil)
		if err != nil {
			return err
		}

		if propsResp.ContentLength == nil {
			return fmt.Errorf("blob has no content length")
		}
		fileSize = *propsResp.ContentLength

		// Get ETag for validation
		if propsResp.ETag != nil {
			currentETag = string(*propsResp.ETag)
		}

		// If IV not provided, try to get it from blob metadata
		if iv == nil {
			// Azure normalizes metadata keys - try both "iv" (Python) and "Iv" (Go SDK returns)
			var ivStr *string
			var ok bool
			if ivStr, ok = propsResp.Metadata["iv"]; !ok {
				ivStr, ok = propsResp.Metadata["Iv"] // Azure SDK may return title-cased keys
			}

			if ok && ivStr != nil {
				var decodeErr error
				iv, decodeErr = encryption.DecodeBase64(*ivStr)
				if decodeErr != nil {
					return fmt.Errorf("failed to decode IV from metadata: %w", decodeErr)
				}
			} else {
				// Build debug info
				var metadataKeys []string
				for k := range propsResp.Metadata {
					metadataKeys = append(metadataKeys, k)
				}
				return fmt.Errorf("IV not provided and not found in blob metadata (available keys: %v, blob: %s, container: %s)",
					metadataKeys, blobPath, d.storageInfo.ConnectionSettings.Container)
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to get blob properties: %w", err)
	}

	// Check for existing resume state BEFORE downloading
	existingState, _ := LoadDownloadState(localPath)
	var resumeOffset int64 = 0

	if existingState != nil {
		// Validate the resume state
		if err := ValidateDownloadState(existingState, localPath); err == nil {
			resumeOffset = existingState.DownloadedBytes
			fmt.Printf("Found valid resume state, resuming download...\n")
		} else {
			// TIER 1: Validation failed - cleanup this specific file's expired resume
			CleanupExpiredResume(existingState, localPath, true)
		}
	}

	// If we have a resume offset, use the resume function
	if resumeOffset > 0 {
		return d.DownloadAndDecryptWithResumeAndProgress(ctx, blobPath, localPath,
			encryptionKey, iv, resumeOffset, currentETag, progressCallback)
	}

	// CHECK DISK SPACE before download (with 15% safety buffer)
	if err := diskspace.CheckAvailableSpace(localPath, fileSize, 1.15); err != nil {
		return &diskspace.InsufficientSpaceError{
			Path:           localPath,
			RequiredBytes:  fileSize,
			AvailableBytes: diskspace.GetAvailableSpace(filepath.Dir(localPath)),
		}
	}

	// Create temp file in same directory as target
	targetDir := filepath.Dir(localPath)
	encryptedPath := localPath + ".encrypted"

	// Ensure cleanup of temp file
	defer func() {
		if removeErr := os.Remove(encryptedPath); removeErr != nil && !os.IsNotExist(removeErr) {
			fmt.Fprintf(os.Stderr, "Warning: Failed to cleanup encrypted temp file %s: %v\n", encryptedPath, removeErr)
		}
	}()

	// LAYER 2: Start periodic refresh for large files (>1GB)
	var downloadCtx context.Context
	var downloadCancel context.CancelFunc
	if fileSize > azureLargeFileThreshold {
		downloadCtx, downloadCancel = context.WithCancel(ctx)
		d.refreshMu.Lock()
		d.cancelRefresh = downloadCancel
		d.refreshMu.Unlock()

		go d.periodicRefresh(ctx, downloadCtx)

		defer func() {
			d.refreshMu.Lock()
			if d.cancelRefresh != nil {
				d.cancelRefresh()
				d.cancelRefresh = nil
			}
			d.refreshMu.Unlock()
		}()
	}

	// Download blob (with retry)
	var resp azblob.DownloadStreamResponse
	err = d.retryWithBackoff(ctx, "DownloadBlob", func() error {
		d.clientMu.Lock()
		client := d.client
		d.clientMu.Unlock()

		blobClient := client.ServiceClient().NewContainerClient(d.storageInfo.ConnectionSettings.Container).NewBlobClient(blobPath)
		r, err := blobClient.DownloadStream(ctx, nil)
		resp = r
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to download blob: %w", err)
	}
	defer resp.Body.Close()

	// Create output file
	outFile, err := os.Create(encryptedPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	// Download with progress tracking
	var downloaded int64
	buffer := make([]byte, 32*1024) // 32KB buffer
	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := outFile.Write(buffer[:n]); writeErr != nil {
				// Check for disk full
				if storage.IsDiskFullError(writeErr) {
					return &diskspace.InsufficientSpaceError{
						Path:           encryptedPath,
						RequiredBytes:  fileSize,
						AvailableBytes: diskspace.GetAvailableSpace(targetDir),
					}
				}
				return fmt.Errorf("failed to write to file: %w", writeErr)
			}
			downloaded += int64(n)
			if fileSize > 0 {
				progressCallback(float64(downloaded) / float64(fileSize))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("download error: %w", err)
		}
	}

	progressCallback(1.0) // Complete

	// Decrypt the file
	if err := encryption.DecryptFile(encryptedPath, localPath, encryptionKey, iv); err != nil {
		// Check for disk full during decryption
		if storage.IsDiskFullError(err) {
			return &diskspace.InsufficientSpaceError{
				Path:           localPath,
				RequiredBytes:  fileSize,
				AvailableBytes: diskspace.GetAvailableSpace(targetDir),
			}
		}
		return fmt.Errorf("decryption failed: %w", err)
	}

	return nil
}

// downloadSingle downloads a blob in a single GET request (with retry)
func (d *AzureDownloader) downloadSingle(ctx context.Context, blobPath, localPath string) error {
	// Download blob (with retry)
	var resp azblob.DownloadStreamResponse
	err := d.retryWithBackoff(ctx, "DownloadBlob", func() error {
		d.clientMu.Lock()
		client := d.client
		d.clientMu.Unlock()

		blobClient := client.ServiceClient().NewContainerClient(d.storageInfo.ConnectionSettings.Container).NewBlobClient(blobPath)
		r, err := blobClient.DownloadStream(ctx, nil)
		resp = r
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to download blob: %w", err)
	}
	defer resp.Body.Close()

	// Create output file
	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Copy data
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// downloadChunked downloads a blob in chunks using Range requests with retry (64MB chunks)
func (d *AzureDownloader) downloadChunked(ctx context.Context, blobPath, localPath string, totalSize int64) error {
	// Create output file
	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	var offset int64 = 0

	for offset < totalSize {
		// Calculate chunk size for this iteration (64MB chunks)
		chunkSize := int64(azureRangeChunkSize)
		if offset+chunkSize > totalSize {
			chunkSize = totalSize - offset
		}

		// Download this chunk using Range (with retry)
		var resp azblob.DownloadStreamResponse
		err := d.retryWithBackoff(ctx, fmt.Sprintf("DownloadBlob range %d-%d", offset, offset+chunkSize-1), func() error {
			d.clientMu.Lock()
			client := d.client
			d.clientMu.Unlock()

			blobClient := client.ServiceClient().NewContainerClient(d.storageInfo.ConnectionSettings.Container).NewBlobClient(blobPath)
			r, err := blobClient.DownloadStream(ctx, &azblob.DownloadStreamOptions{
				Range: azblob.HTTPRange{
					Offset: offset,
					Count:  chunkSize,
				},
			})
			resp = r
			return err
		})

		if err != nil {
			return fmt.Errorf("failed to download chunk at offset %d: %w", offset, err)
		}

		// Read chunk data
		chunkData, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err != nil {
			return fmt.Errorf("failed to read chunk at offset %d: %w", offset, err)
		}

		// Write chunk to file
		_, err = file.Write(chunkData)
		if err != nil {
			return fmt.Errorf("failed to write chunk at offset %d: %w", offset, err)
		}

		offset += int64(len(chunkData))
	}

	return nil
}

// DownloadAndDecryptWithResumeAndProgress downloads with resume + progress + state saving
func (d *AzureDownloader) DownloadAndDecryptWithResumeAndProgress(ctx context.Context, blobPath, localPath string, encryptionKey, iv []byte, resumeOffset int64, expectedETag string, progressCallback func(float64)) error {
	return d.downloadAndDecryptWithResumeInternal(ctx, blobPath, localPath, encryptionKey, iv, resumeOffset, expectedETag, progressCallback, true)
}

// DownloadAndDecryptWithResume downloads with resume capability from a specific offset
// Useful for resuming interrupted downloads
// NOTE: This validates that the remote file hasn't changed before resuming
func (d *AzureDownloader) DownloadAndDecryptWithResume(ctx context.Context, blobPath, localPath string, encryptionKey, iv []byte, resumeOffset int64, expectedETag string) error {
	return d.downloadAndDecryptWithResumeInternal(ctx, blobPath, localPath, encryptionKey, iv, resumeOffset, expectedETag, nil, false)
}

// downloadAndDecryptWithResumeInternal is the internal implementation with optional progress and state saving
func (d *AzureDownloader) downloadAndDecryptWithResumeInternal(ctx context.Context, blobPath, localPath string, encryptionKey, iv []byte, resumeOffset int64, expectedETag string, progressCallback func(float64), saveState bool) error {
	// Get blob properties (with retry)
	d.clientMu.Lock()
	client := d.client
	d.clientMu.Unlock()

	blobClient := client.ServiceClient().NewContainerClient(d.storageInfo.ConnectionSettings.Container).NewBlobClient(blobPath)

	var totalSize int64
	var currentETag string
	err := d.retryWithBackoff(ctx, "GetBlobProperties", func() error {
		propsResp, err := blobClient.GetProperties(ctx, nil)
		if err != nil {
			return err
		}

		if propsResp.ContentLength == nil {
			return fmt.Errorf("blob has no content length")
		}
		totalSize = *propsResp.ContentLength

		// Get ETag for validation
		if propsResp.ETag != nil {
			currentETag = string(*propsResp.ETag)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to get blob properties: %w", err)
	}

	// VALIDATE: Check if file has changed since last download attempt
	if expectedETag != "" && currentETag != expectedETag {
		return fmt.Errorf("remote file has changed (ETag mismatch: expected %s, got %s) - cannot resume, must restart", expectedETag, currentETag)
	}

	// VALIDATE: Check if resume offset is valid
	if resumeOffset > totalSize {
		return fmt.Errorf("invalid resume offset %d (file size is %d)", resumeOffset, totalSize)
	}

	// If resume offset equals total size, file is already complete
	if resumeOffset == totalSize {
		// Just decrypt
		encryptedPath := localPath + ".encrypted"
		defer os.Remove(encryptedPath)

		if err := encryption.DecryptFile(encryptedPath, localPath, encryptionKey, iv); err != nil {
			return fmt.Errorf("decryption failed: %w", err)
		}
		return nil
	}

	// Download to temporary encrypted file
	encryptedPath := localPath + ".encrypted"
	defer func() {
		if removeErr := os.Remove(encryptedPath); removeErr != nil && !os.IsNotExist(removeErr) {
			fmt.Fprintf(os.Stderr, "Warning: Failed to cleanup encrypted temp file %s: %v\n", encryptedPath, removeErr)
		}
	}()

	// Open file for appending if resuming
	var file *os.File
	if resumeOffset > 0 {
		file, err = os.OpenFile(encryptedPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open file for resume: %w", err)
		}
	} else {
		file, err = os.Create(encryptedPath)
		if err != nil {
			return fmt.Errorf("failed to create file: %w", err)
		}
	}
	defer file.Close()

	// Track creation time for resume state
	createdAt := time.Now()

	// Download remaining chunks (64MB chunks with retry)
	offset := resumeOffset

	for offset < totalSize {
		chunkSize := int64(azureRangeChunkSize)
		if offset+chunkSize > totalSize {
			chunkSize = totalSize - offset
		}

		var resp azblob.DownloadStreamResponse
		err := d.retryWithBackoff(ctx, fmt.Sprintf("DownloadBlob range %d-%d", offset, offset+chunkSize-1), func() error {
			d.clientMu.Lock()
			client := d.client
			d.clientMu.Unlock()

			blobClient := client.ServiceClient().NewContainerClient(d.storageInfo.ConnectionSettings.Container).NewBlobClient(blobPath)
			r, err := blobClient.DownloadStream(ctx, &azblob.DownloadStreamOptions{
				Range: azblob.HTTPRange{
					Offset: offset,
					Count:  chunkSize,
				},
			})
			resp = r
			return err
		})

		if err != nil {
			return fmt.Errorf("failed to download chunk at offset %d: %w", offset, err)
		}

		chunkData, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err != nil {
			return fmt.Errorf("failed to read chunk at offset %d: %w", offset, err)
		}

		_, err = file.Write(chunkData)
		if err != nil {
			return fmt.Errorf("failed to write chunk at offset %d: %w", offset, err)
		}

		offset += int64(len(chunkData))

		// Save resume state after each chunk (if enabled)
		if saveState {
			currentState := &DownloadResumeState{
				LocalPath:       localPath,
				EncryptedPath:   encryptedPath,
				RemotePath:      blobPath,
				FileID:          "",
				TotalSize:       totalSize,
				DownloadedBytes: offset,
				ETag:            currentETag,
				CreatedAt:       createdAt,
				LastUpdate:      time.Now(),
				StorageType:     "AzureStorage",
			}
			SaveDownloadState(currentState, localPath)
		}

		// Progress callback
		if progressCallback != nil {
			progressCallback(float64(offset) / float64(totalSize))
		}
	}

	// Decrypt the completed file
	if err := encryption.DecryptFile(encryptedPath, localPath, encryptionKey, iv); err != nil {
		if storage.IsDiskFullError(err) {
			return &diskspace.InsufficientSpaceError{
				Path:           localPath,
				RequiredBytes:  totalSize,
				AvailableBytes: diskspace.GetAvailableSpace(filepath.Dir(localPath)),
			}
		}
		return fmt.Errorf("decryption failed: %w", err)
	}

	// Delete resume state on success
	if saveState {
		DeleteDownloadState(localPath)
	}

	return nil
}
