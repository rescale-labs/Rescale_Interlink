package upload

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	nethttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/storage"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/util/buffers"
)

// Azure-specific constants (using shared constants package)
const (
	azureMultipartThreshold      = constants.MultipartThreshold
	azureBlockSize               = constants.ChunkSize
	azurePeriodicRefreshInterval = constants.AzurePeriodicRefreshInterval
	azureLargeFileThreshold      = constants.LargeFileThreshold
	azureMaxRetries              = constants.MaxRetries
	azureRetryInitialDelay       = constants.RetryInitialDelay
	azureRetryMaxDelay           = constants.RetryMaxDelay
)

// readSeekCloser wraps bytes.Reader to implement io.ReadSeekCloser
type readSeekCloser struct {
	*bytes.Reader
}

func (rsc *readSeekCloser) Close() error {
	return nil
}

// AzureUploader handles Azure blob uploads with encryption and 3-layer credential refresh
// Layer 1: Proactive refresh before operations
// Layer 2: Periodic refresh every 8 minutes for large files
// Layer 3: Error-driven refresh via retry logic
type AzureUploader struct {
	client      *azblob.Client
	storageInfo *models.StorageInfo
	credManager *credentials.Manager
	httpClient  *nethttp.Client // Shared HTTP client for connection reuse
	clientMu    sync.Mutex      // Protects client updates during credential refresh

	// For periodic refresh goroutine cancellation
	cancelRefresh context.CancelFunc
	refreshMu     sync.Mutex
}

// NewAzureUploader creates a new Azure uploader with optimized HTTP client and credential management
func NewAzureUploader(storageInfo *models.StorageInfo, creds *models.AzureCredentials, apiClient *api.Client) (*AzureUploader, error) {
	// Create shared optimized HTTP client with proxy support from API client config
	// IMPORTANT: Reuse this client across credential refreshes to maintain connection pool
	purCfg := apiClient.GetConfig()
	httpClient, err := http.CreateOptimizedClient(purCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Build SAS URL
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
			return nil, fmt.Errorf("Azure storage account name not found in ConnectionSettings.AccountName")
		}

		// Python pattern: BlobServiceClient with account URL only (no container in URL)
		// Format: https://{account}.blob.core.windows.net?{sas_token}
		sasURL = fmt.Sprintf("https://%s.blob.core.windows.net/?%s", accountName, creds.SASToken)
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

	// Get the global credential manager
	credManager := credentials.GetManager(apiClient)

	return &AzureUploader{
		client:      client,
		storageInfo: storageInfo,
		credManager: credManager,
		httpClient:  httpClient, // Store for reuse during credential refresh
	}, nil
}

// ensureFreshCredentials refreshes Azure credentials using the global credential manager
// CRITICAL: Recreates Azure client BUT reuses HTTP transport for connection pooling
func (u *AzureUploader) ensureFreshCredentials(ctx context.Context) error {
	// Get fresh credentials from global manager (may trigger refresh if stale)
	creds, err := u.credManager.GetAzureCredentials(ctx)
	if err != nil {
		return fmt.Errorf("failed to get Azure credentials: %w", err)
	}

	// Build new SAS URL with fresh token (same logic as NewAzureUploader)
	var sasURL string
	if len(creds.Paths) > 0 && creds.Paths[0] != "" {
		sasURL = creds.Paths[0]
		if !strings.Contains(sasURL, "?") {
			sasURL = sasURL + "?" + creds.SASToken
		}
	} else {
		accountName := u.storageInfo.ConnectionSettings.AccountName
		if accountName == "" {
			accountName = u.storageInfo.ConnectionSettings.StorageAccount
		}
		if accountName == "" {
			return fmt.Errorf("Azure storage account name not found in ConnectionSettings.AccountName")
		}
		// Python pattern: account URL only (no container in URL)
		sasURL = fmt.Sprintf("https://%s.blob.core.windows.net/?%s", accountName, creds.SASToken)
	}

	// Lock to prevent concurrent client updates
	u.clientMu.Lock()
	defer u.clientMu.Unlock()

	// Recreate client with new SAS token BUT reuse HTTP transport
	client, err := azblob.NewClientWithNoCredential(sasURL, &azblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: u.httpClient, // CRITICAL: Reuse same HTTP client!
		},
	})
	if err != nil {
		return fmt.Errorf("failed to recreate Azure client: %w", err)
	}

	u.client = client
	return nil
}

// periodicRefresh refreshes credentials every azurePeriodicRefreshInterval for long-running uploads
// This is Layer 2 of the 3-layer credential refresh strategy
func (u *AzureUploader) periodicRefresh(ctx context.Context, uploadCtx context.Context) {
	ticker := time.NewTicker(azurePeriodicRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Refresh credentials
			if err := u.ensureFreshCredentials(ctx); err != nil {
				// Log error but don't fail - Layer 3 (error-driven) will catch auth failures
				// This is just a proactive refresh
				continue
			}
		case <-uploadCtx.Done():
			// Upload finished, stop refreshing
			return
		}
	}
}

// startPeriodicRefresh starts background credential refresh for large files
func (u *AzureUploader) startPeriodicRefresh(ctx context.Context) context.Context {
	u.refreshMu.Lock()
	defer u.refreshMu.Unlock()

	// Create cancellable context for refresh goroutine
	refreshCtx, cancel := context.WithCancel(ctx)
	u.cancelRefresh = cancel

	// Start periodic refresh goroutine
	go u.periodicRefresh(ctx, refreshCtx)

	return refreshCtx
}

// stopPeriodicRefresh stops background credential refresh
func (u *AzureUploader) stopPeriodicRefresh() {
	u.refreshMu.Lock()
	defer u.refreshMu.Unlock()

	if u.cancelRefresh != nil {
		u.cancelRefresh()
		u.cancelRefresh = nil
	}
}

// retryWithBackoff wraps retry logic with credential refresh capability
// This is Layer 3 of the 3-layer credential refresh strategy (error-driven)
func (u *AzureUploader) retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	config := http.Config{
		MaxRetries:   azureMaxRetries,
		InitialDelay: azureRetryInitialDelay,
		MaxDelay:     azureRetryMaxDelay,
		CredentialRefresh: func(ctx context.Context) error {
			// On credential errors, refresh immediately
			return u.ensureFreshCredentials(ctx)
		},
	}

	return http.ExecuteWithRetry(ctx, config, fn)
}

// UploadEncrypted encrypts and uploads a file to Azure Blob Storage
// Returns (blobPath, encryptionKey, iv, error)
// progressCallback is optional and called with progress from 0.0 to 1.0
func (u *AzureUploader) UploadEncrypted(ctx context.Context, localPath string, progressCallback ProgressCallback, outputWriter io.Writer) (string, []byte, []byte, error) {
	// LAYER 1: Proactive credential refresh before starting
	if err := u.ensureFreshCredentials(ctx); err != nil {
		return "", nil, nil, fmt.Errorf("failed to refresh credentials before upload: %w", err)
	}

	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	// Get original file size for disk space check
	fileInfo, err := os.Stat(localPath)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to stat file: %w", err)
	}
	originalSize := fileInfo.Size()

	// TIER 2: Opportunistic cleanup of expired resume states in this directory
	// This helps cleanup orphaned temp files from previous uploads that were never resumed
	// Silent mode (verbose=false) - don't spam output during normal uploads
	dir := filepath.Dir(localPath)
	CleanupExpiredResumesInDirectory(dir, false)

	// Check for existing resume state BEFORE encrypting
	existingState, _ := LoadUploadState(localPath) // Uses ORIGINAL path as key
	var encryptionKey, iv []byte
	var randomSuffix, blobName, pathForRescale, blobNameForSDK, encryptedPath string
	var reusingEncrypted bool

	if existingState != nil {
		// Validate the resume state
		if err := ValidateUploadState(existingState, localPath); err == nil {
			// Resume state is valid! Reuse encryption parameters and encrypted file
			if outputWriter != nil {
				fmt.Fprintf(outputWriter, "Found valid resume state, reusing encrypted file...\n")
			}

			// Decode encryption key and IV from base64
			encryptionKey, err = encryption.DecodeBase64(existingState.EncryptionKey)
			if err != nil {
				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Warning: Failed to decode encryption key from resume state: %v\n", err)
				}
				goto freshStart
			}

			iv, err = encryption.DecodeBase64(existingState.IV)
			if err != nil {
				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Warning: Failed to decode IV from resume state: %v\n", err)
				}
				goto freshStart
			}

			randomSuffix = existingState.RandomSuffix
			pathForRescale = existingState.ObjectKey // For Azure, this is the path we return to Rescale
			encryptedPath = existingState.EncryptedPath

			// Reconstruct blobName and blobNameForSDK from randomSuffix
			filename := filepath.Base(localPath)
			blobName = fmt.Sprintf("%s-%s", filename, randomSuffix)
			blobNameForSDK = blobName

			reusingEncrypted = true
		} else {
			// TIER 1: Validation failed (expired, file changed, etc.)
			// Cleanup this specific file's expired resume
			CleanupExpiredResume(existingState, localPath, outputWriter) // verbose=true for this file
		}
	}

freshStart:
	if !reusingEncrypted {
		// Fresh start: generate new encryption parameters
		encryptionKey, err = encryption.GenerateKey()
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to generate encryption key: %w", err)
		}

		iv, err = encryption.GenerateIV()
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to generate IV: %w", err)
		}

		// Generate random suffix for blob name (same as S3)
		randomSuffix, err = encryption.GenerateSecureRandomString(22)
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to generate random suffix: %w", err)
		}

		// Build blob paths
		// Azure has TWO different path concepts:
		// 1. PathBase ("ag9web") - used for container name in Azure (matches container field)
		// 2. PathPartsBase ("") - used for pathParts.path in Rescale API registration
		//
		// Example:
		// - Blob uploaded to: container="ag9web", blob="file.txt-xxx"
		// - Rescale pathParts: {container: "ag9web", path: "file.txt-xxx"} (PathPartsBase is empty)
		//
		// This differs from S3 where PathBase and PathPartsBase are the same (folder prefix)
		filename := filepath.Base(localPath)
		blobName = fmt.Sprintf("%s-%s", filename, randomSuffix)

		// Path to return for Rescale API registration (uses PathPartsBase)
		// For Azure, PathPartsBase is typically empty "", so this returns just blobName
		if u.storageInfo.ConnectionSettings.PathPartsBase != "" {
			pathForRescale = fmt.Sprintf("%s/%s", u.storageInfo.ConnectionSettings.PathPartsBase, blobName)
		} else {
			pathForRescale = blobName
		}

		// For Azure SDK calls: use just the blob name since container is specified separately
		// This prevents doubling: container="ag9web" + blob="ag9web/file" = "ag9web/ag9web/file"
		blobNameForSDK = blobName
	}

	// Skip encryption if reusing (HUGE TIME SAVER!)
	if !reusingEncrypted {
		// Try to create encrypted file in system temp directory first
		// If insufficient space, fall back to same directory as source file
		var encryptedFile *os.File

		// First attempt: system temp directory (better for cleanup)
		tempDir := os.TempDir()
		encryptedFile, err = os.CreateTemp(tempDir, fmt.Sprintf("%s-*.encrypted", filepath.Base(localPath)))
		if err == nil {
			encryptedPath = encryptedFile.Name()
			encryptedFile.Close()

			// Check if temp directory has enough space (15% buffer)
			if err := diskspace.CheckAvailableSpace(encryptedPath, originalSize, 1.15); err != nil {
				// Insufficient space in /tmp - clean up and try fallback
				os.Remove(encryptedPath)
				encryptedFile = nil
			}
		}

		// Fallback: create temp file in same directory as source file
		if encryptedFile == nil {
			sourceDir := filepath.Dir(localPath)
			encryptedFile, err = os.CreateTemp(sourceDir, fmt.Sprintf(".%s-*.encrypted", filepath.Base(localPath)))
			if err != nil {
				return "", nil, nil, fmt.Errorf("failed to create temp file: %w", err)
			}
			encryptedPath = encryptedFile.Name()
			encryptedFile.Close()

			// Check disk space in source directory
			if err := diskspace.CheckAvailableSpace(encryptedPath, originalSize, 1.15); err != nil {
				os.Remove(encryptedPath)
				return "", nil, nil, err
			}
		}

		// Encrypt file to temporary location
		if outputWriter != nil {
			fmt.Fprintf(outputWriter, "Encrypting file (%s)...\n", filepath.Base(localPath))
		}
		if err := encryption.EncryptFile(localPath, encryptedPath, encryptionKey, iv); err != nil {
			os.Remove(encryptedPath)

			// Check if error is related to disk space
			if storage.IsDiskFullError(err) {
				return "", nil, nil, &diskspace.InsufficientSpaceError{
					Path:           encryptedPath,
					RequiredBytes:  originalSize,
					AvailableBytes: diskspace.GetAvailableSpace(encryptedPath),
				}
			}
			return "", nil, nil, fmt.Errorf("failed to encrypt file: %w", err)
		}
		// Reset timer after encryption to exclude encryption time from transfer rate
		if progressCallback != nil {
			progressCallback(-1.0)
		}
	}
	defer os.Remove(encryptedPath)

	// Get encrypted file size (for Azure SDK calls)
	encryptedInfo, err := os.Stat(encryptedPath)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to stat encrypted file: %w", err)
	}
	totalSize := encryptedInfo.Size()

	// LAYER 2: Start periodic refresh for large files
	if totalSize > azureLargeFileThreshold {
		_ = u.startPeriodicRefresh(ctx) // refreshCtx not needed, goroutine manages itself
		defer u.stopPeriodicRefresh()
	}

	// Choose upload method based on file size
	var uploadErr error
	if totalSize < azureMultipartThreshold {
		// Small file: single blob upload
		uploadErr = u.uploadSingleBlob(ctx, encryptedPath, blobNameForSDK, iv, progressCallback)
	} else {
		// Large file: block blob upload (multipart equivalent)
		// Pass all parameters needed for resume support
		uploadErr = u.uploadBlockBlob(ctx, localPath, encryptedPath, blobNameForSDK, pathForRescale,
			encryptionKey, iv, randomSuffix, originalSize, totalSize, progressCallback, outputWriter)
	}

	if uploadErr != nil {
		return "", nil, nil, uploadErr
	}

	// Delete resume state after successful upload
	DeleteUploadState(localPath)

	// Report 100% at end
	if progressCallback != nil {
		progressCallback(1.0)
	}

	// Return pathForRescale (uses PathPartsBase) not blobPathForAzure (uses PathBase)
	// This ensures pathParts.path has the correct format for Rescale API
	return pathForRescale, encryptionKey, iv, nil
}

// uploadSingleBlob uploads a file as a single blob (for files < 100MB)
func (u *AzureUploader) uploadSingleBlob(ctx context.Context, filePath, blobPath string, iv []byte, callback ProgressCallback) error {
	// Open encrypted file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open encrypted file: %w", err)
	}
	defer file.Close()

	// Read entire file into memory (acceptable for small files)
	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Prepare metadata with IV (must be set DURING upload, not after, due to SAS token permissions)
	metadata := map[string]*string{
		"iv": to.Ptr(encryption.EncodeBase64(iv)),
	}

	// Upload with retry using blockblob.Client.Upload (matches Python's upload_blob with metadata)
	err = u.retryWithBackoff(ctx, "Upload", func() error {
		u.clientMu.Lock()
		client := u.client
		u.clientMu.Unlock()

		// Get blockblob client (Python pattern: blob_client.upload_blob with metadata)
		blockBlobClient := client.ServiceClient().NewContainerClient(u.storageInfo.ConnectionSettings.Container).NewBlockBlobClient(blobPath)

		// Wrap data in ReadSeekCloser
		reader := &readSeekCloser{Reader: bytes.NewReader(data)}

		// Upload with metadata
		_, err := blockBlobClient.Upload(ctx, reader, &blockblob.UploadOptions{
			Metadata: metadata,
		})
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to upload blob: %w", err)
	}

	// Report progress (simple: 0 at start, 1.0 at end)
	if callback != nil {
		callback(1.0)
	}

	return nil
}

// uploadBlockBlob uploads a file using block blobs (for files >= 100MB)
// This is Azure's equivalent to S3 multipart upload
func (u *AzureUploader) uploadBlockBlob(ctx context.Context, originalPath, encryptedPath, blobPath, pathForRescale string,
	encryptionKey, iv []byte, randomSuffix string, originalSize, totalSize int64, callback ProgressCallback, outputWriter io.Writer) error {
	// Open encrypted file
	file, err := os.Open(encryptedPath)
	if err != nil {
		return fmt.Errorf("failed to open encrypted file: %w", err)
	}
	defer file.Close()

	// Calculate number of blocks
	numBlocks := (totalSize + azureBlockSize - 1) / azureBlockSize

	// Try to load resume state (keyed by ORIGINAL file path, not encrypted path)
	existingState, _ := LoadUploadState(originalPath)
	var blockIDs []string
	var uploadedBytes int64
	startBlock := int64(0)
	resuming := false
	var createdAt time.Time

	if existingState != nil {
		// Validate resume state
		if err := ValidateUploadState(existingState, originalPath); err == nil {
			// Get uncommitted blocks from Azure (verify they still exist)
			u.clientMu.Lock()
			client := u.client
			u.clientMu.Unlock()

			blockBlobClient := client.ServiceClient().NewContainerClient(u.storageInfo.ConnectionSettings.Container).NewBlockBlobClient(blobPath)
			blockListResp, listErr := blockBlobClient.GetBlockList(ctx, blockblob.BlockListTypeUncommitted, nil)

			if listErr == nil && blockListResp.UncommittedBlocks != nil && len(blockListResp.UncommittedBlocks) > 0 {
				// Valid resume state and uncommitted blocks exist!
				blockIDs = existingState.BlockIDs
				uploadedBytes = existingState.UploadedBytes
				startBlock = int64(len(blockIDs))
				resuming = true
				createdAt = existingState.CreatedAt

				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Resuming upload from block %d/%d (%.1f%%)\n",
						startBlock+1, numBlocks,
						float64(uploadedBytes)/float64(totalSize)*100)
				}
			} else {
				// Blocks expired or don't exist, will start fresh
				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Previous uncommitted blocks expired, starting fresh upload\n")
				}
			}
		}
	}

	// Initialize for fresh upload if no valid resume
	if !resuming {
		blockIDs = make([]string, 0, numBlocks)
		uploadedBytes = 0
		createdAt = time.Now()

		// Save initial state (keyed by original file path)
		initialState := &UploadResumeState{
			LocalPath:      originalPath,   // CORRECT - original file path
			EncryptedPath:  encryptedPath,  // NEW - reference to encrypted file
			ObjectKey:      pathForRescale, // Path returned to Rescale API
			UploadID:       "",             // Not applicable for Azure
			TotalSize:      totalSize,      // Encrypted file size
			OriginalSize:   originalSize,   // NEW - original file size
			UploadedBytes:  0,
			CompletedParts: []CompletedPart{},                      // Not used for Azure
			BlockIDs:       []string{},                             // Azure uses this
			EncryptionKey:  encryption.EncodeBase64(encryptionKey), // NEW
			IV:             encryption.EncodeBase64(iv),            // NEW
			RandomSuffix:   randomSuffix,                           // NEW
			CreatedAt:      createdAt,
			LastUpdate:     time.Now(),
			StorageType:    "AzureStorage",
		}
		SaveUploadState(initialState, originalPath) // CORRECT - save with original path as key
	}

	// If resuming, seek to the position after the last uploaded block
	if resuming && startBlock > 0 {
		seekOffset := startBlock * azureBlockSize
		if _, seekErr := file.Seek(seekOffset, 0); seekErr != nil {
			return fmt.Errorf("failed to seek to resume position: %w", seekErr)
		}
	}

	// Get buffer from pool for reuse across all blocks
	bufferPtr := buffers.GetChunkBuffer()
	defer buffers.PutChunkBuffer(bufferPtr)
	buffer := *bufferPtr

	// Upload each block (starting from startBlock if resuming)
	for blockNum := startBlock; blockNum < numBlocks; blockNum++ {
		// Calculate block size (last block may be smaller)
		chunkSize := int64(azureBlockSize)
		if blockNum == numBlocks-1 {
			chunkSize = totalSize - (blockNum * azureBlockSize)
		}

		// Read block data into pooled buffer
		blockData := buffer[:chunkSize] // Use slice of pooled buffer
		n, err := io.ReadFull(file, blockData)
		if err != nil && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("failed to read block %d: %w", blockNum, err)
		}

		// Make a copy of the data for the upload (since buffer will be reused)
		blockDataCopy := make([]byte, n)
		copy(blockDataCopy, blockData[:n])

		// Generate block ID (must be base64-encoded and same length for all blocks)
		// Using 10-digit zero-padded number ensures consistent length
		blockIDStr := fmt.Sprintf("block-%010d", blockNum)
		blockID := base64.StdEncoding.EncodeToString([]byte(blockIDStr))
		blockIDs = append(blockIDs, blockID)

		// Stage block with retry
		err = u.retryWithBackoff(ctx, fmt.Sprintf("StageBlock %d", blockNum), func() error {
			u.clientMu.Lock()
			client := u.client
			u.clientMu.Unlock()

			// Get block blob client for this specific blob
			blockBlobClient := client.ServiceClient().NewContainerClient(u.storageInfo.ConnectionSettings.Container).NewBlockBlobClient(blobPath)

			// Create a ReadSeekCloser from the copied block data
			reader := &readSeekCloser{Reader: bytes.NewReader(blockDataCopy)}
			_, err := blockBlobClient.StageBlock(ctx, blockID, reader, nil)
			return err
		})

		if err != nil {
			return fmt.Errorf("failed to stage block %d: %w", blockNum, err)
		}

		// Update progress
		uploadedBytes += int64(n)
		if callback != nil {
			progress := float64(uploadedBytes) / float64(totalSize)
			callback(progress)
		}

		// Save resume state after each block (keyed by original file path)
		currentState := &UploadResumeState{
			LocalPath:      originalPath,   // CORRECT - original file path
			EncryptedPath:  encryptedPath,  // Reference to encrypted file
			ObjectKey:      pathForRescale, // Path returned to Rescale API
			UploadID:       "",             // Not applicable for Azure
			TotalSize:      totalSize,      // Encrypted file size
			OriginalSize:   originalSize,   // Original file size
			UploadedBytes:  uploadedBytes,
			CompletedParts: []CompletedPart{}, // Not used for Azure
			BlockIDs:       blockIDs,          // Azure uses this
			EncryptionKey:  encryption.EncodeBase64(encryptionKey),
			IV:             encryption.EncodeBase64(iv),
			RandomSuffix:   randomSuffix,
			CreatedAt:      createdAt, // Preserve original creation time
			LastUpdate:     time.Now(),
			StorageType:    "AzureStorage",
		}
		SaveUploadState(currentState, originalPath) // CORRECT - save with original path as key
	}

	// Prepare metadata with IV (must be set DURING commit, not after, due to SAS token permissions)
	metadata := map[string]*string{
		"iv": to.Ptr(encryption.EncodeBase64(iv)),
	}

	// Commit block list with metadata (matches Python's upload_blob with metadata)
	err = u.retryWithBackoff(ctx, "CommitBlockList", func() error {
		u.clientMu.Lock()
		client := u.client
		u.clientMu.Unlock()

		// Get block blob client for this specific blob
		blockBlobClient := client.ServiceClient().NewContainerClient(u.storageInfo.ConnectionSettings.Container).NewBlockBlobClient(blobPath)
		_, err := blockBlobClient.CommitBlockList(ctx, blockIDs, &blockblob.CommitBlockListOptions{
			Metadata: metadata,
		})
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to commit block list: %w", err)
	}

	// Delete resume state on successful completion (keyed by original file path)
	DeleteUploadState(originalPath)

	return nil
}
