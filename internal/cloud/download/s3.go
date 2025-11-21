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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

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
	RangeChunkSize    = constants.ChunkSize
	DownloadThreshold = constants.MultipartThreshold
	MaxRetries        = constants.MaxRetries
	RetryInitialDelay = constants.RetryInitialDelay
	RetryMaxDelay     = constants.RetryMaxDelay
)

// S3Downloader handles S3 downloads with decryption, retry logic, and credential refresh
type S3Downloader struct {
	client      *s3.Client
	storageInfo *models.StorageInfo
	credManager *credentials.Manager
	httpClient  *nethttp.Client // Shared HTTP client for connection reuse
	apiClient   *api.Client     // For checksum verification
	clientMu    sync.Mutex      // Protects client updates during credential refresh
}

// NewS3Downloader creates a new S3 downloader with auto-refreshing credentials
// If fileInfo is provided, credentials will be refreshed for that specific file's storage
// If fileInfo is nil, credentials will be refreshed for user's default storage
func NewS3Downloader(storageInfo *models.StorageInfo, creds *models.S3Credentials, apiClient *api.Client, fileInfo *models.CloudFile) (*S3Downloader, error) {
	// Create shared optimized HTTP client with proxy support from API client config
	purCfg := apiClient.GetConfig()
	httpClient, err := http.CreateOptimizedClient(purCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Create auto-refreshing credential provider with file-specific credentials
	credProvider := credentials.NewRescaleCredentialProvider(apiClient, fileInfo)

	// Wrap with credentials cache for automatic refresh
	credCache := aws.NewCredentialsCache(credProvider, func(o *aws.CredentialsCacheOptions) {
		// Refresh 5 minutes before expiry (credentials expire at ~15 min)
		o.ExpiryWindow = 5 * time.Minute
	})

	// Load AWS config with custom HTTP client and auto-refreshing credentials
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(storageInfo.ConnectionSettings.Region),
		config.WithHTTPClient(httpClient),
		config.WithCredentialsProvider(credCache),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)

	// Get the global credential manager
	credManager := credentials.GetManager(apiClient)

	return &S3Downloader{
		client:      client,
		storageInfo: storageInfo,
		credManager: credManager,
		httpClient:  httpClient,
		apiClient:   apiClient,
	}, nil
}

// ensureFreshCredentials refreshes S3 credentials using the global credential manager
func (d *S3Downloader) ensureFreshCredentials(ctx context.Context) error {
	// Get fresh credentials from global manager (auto-refreshes if needed)
	s3Creds, err := d.credManager.GetS3Credentials(ctx)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}

	if s3Creds == nil {
		return fmt.Errorf("received nil S3 credentials")
	}

	// Update S3 client with fresh credentials
	d.clientMu.Lock()
	defer d.clientMu.Unlock()

	// IMPORTANT: Reuse existing HTTP client instead of creating new one
	// This preserves the connection pool and prevents TLS handshake overhead
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(d.storageInfo.ConnectionSettings.Region),
		config.WithHTTPClient(d.httpClient), // Reuse existing HTTP client!
		config.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(
			s3Creds.AccessKeyID,
			s3Creds.SecretKey,
			s3Creds.SessionToken,
		)),
	)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	d.client = s3.NewFromConfig(cfg)

	return nil
}

// retryWithBackoff executes a function with exponential backoff retry logic using shared retry package
func (d *S3Downloader) retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	config := http.Config{
		MaxRetries:   MaxRetries,
		InitialDelay: RetryInitialDelay,
		MaxDelay:     RetryMaxDelay,
		// No manual credential refresh needed - AWS SDK's CredentialsCache handles auto-refresh
		CredentialRefresh: nil,
		OnRetry: func(attempt int, err error, errorType http.ErrorType) {
			// Optional: log retry attempts for debugging
			if os.Getenv("DEBUG_RETRY") == "true" {
				log.Printf("[RETRY] %s: attempt %d/%d, error type: %s, error: %v",
					operation, attempt, MaxRetries, http.ErrorTypeName(errorType), err)
			}
		},
	}

	return http.ExecuteWithRetry(ctx, config, fn)
}

// DownloadAndDecrypt downloads a file from S3 and decrypts it with retry logic
// Returns error if download or decryption fails
// If iv is nil, it will be retrieved from S3 object metadata
func (d *S3Downloader) DownloadAndDecrypt(ctx context.Context, objectKey, localPath string, encryptionKey, iv []byte) error {
	// Get object metadata to check size and retrieve IV if not provided (with retry)
	var headResp *s3.HeadObjectOutput
	err := d.retryWithBackoff(ctx, "HeadObject", func() error {
		resp, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(d.storageInfo.ConnectionSettings.Container),
			Key:    aws.String(objectKey),
		})
		headResp = resp
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to get object metadata: %w", err)
	}

	// If IV not provided, try to get it from S3 metadata
	if iv == nil {
		if ivStr, ok := headResp.Metadata["iv"]; ok {
			iv, err = encryption.DecodeBase64(ivStr)
			if err != nil {
				return fmt.Errorf("failed to decode IV from metadata: %w", err)
			}
		} else {
			return fmt.Errorf("IV not provided and not found in S3 metadata")
		}
	}

	fileSize := *headResp.ContentLength

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

	// Choose download method based on file size
	if fileSize > DownloadThreshold {
		// Use chunked download for large files
		err = d.downloadChunked(ctx, objectKey, encryptedPath, fileSize)
	} else {
		// Use single request for small files
		err = d.downloadSingle(ctx, objectKey, encryptedPath)
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

// DownloadAndDecryptWithProgress downloads a file from S3 and decrypts it with progress reporting
// progressCallback receives progress as a float64 between 0.0 and 1.0
func (d *S3Downloader) DownloadAndDecryptWithProgress(ctx context.Context, objectKey, localPath string, encryptionKey, iv []byte, progressCallback func(float64)) error {
	if progressCallback == nil {
		// No progress callback, use standard method
		return d.DownloadAndDecrypt(ctx, objectKey, localPath, encryptionKey, iv)
	}

	// Report 0% at start
	progressCallback(0.0)

	// TIER 2: Opportunistic cleanup of expired resume states in this directory
	dir := filepath.Dir(localPath)
	CleanupExpiredResumesInDirectory(dir, false)

	// Get object metadata (with retry)
	var headResp *s3.HeadObjectOutput
	err := d.retryWithBackoff(ctx, "HeadObject", func() error {
		resp, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(d.storageInfo.ConnectionSettings.Container),
			Key:    aws.String(objectKey),
		})
		headResp = resp
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to get object metadata: %w", err)
	}

	// If IV not provided, try to get it from S3 metadata
	if iv == nil {
		if ivStr, ok := headResp.Metadata["iv"]; ok {
			iv, err = encryption.DecodeBase64(ivStr)
			if err != nil {
				return fmt.Errorf("failed to decode IV from metadata: %w", err)
			}
		} else {
			return fmt.Errorf("IV not provided and not found in S3 metadata")
		}
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

	// Get current ETag for validation
	currentETag := ""
	if headResp.ETag != nil {
		currentETag = *headResp.ETag
	}

	// If we have a resume offset, use the resume function
	if resumeOffset > 0 {
		return d.DownloadAndDecryptWithResumeAndProgress(ctx, objectKey, localPath,
			encryptionKey, iv, resumeOffset, currentETag, progressCallback)
	}

	fileSize := *headResp.ContentLength

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

	// Get object (with retry)
	var resp *s3.GetObjectOutput
	err = d.retryWithBackoff(ctx, "GetObject", func() error {
		r, err := d.client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(d.storageInfo.ConnectionSettings.Container),
			Key:    aws.String(objectKey),
		})
		resp = r
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to get object: %w", err)
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

// downloadSingle downloads a file in a single GET request (with retry)
func (d *S3Downloader) downloadSingle(ctx context.Context, objectKey, localPath string) error {
	// Get object (with retry)
	var resp *s3.GetObjectOutput
	err := d.retryWithBackoff(ctx, "GetObject", func() error {
		r, err := d.client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(d.storageInfo.ConnectionSettings.Container),
			Key:    aws.String(objectKey),
		})
		resp = r
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to get object: %w", err)
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

// downloadChunked downloads a file in chunks using Range requests with retry (64MB chunks)
func (d *S3Downloader) downloadChunked(ctx context.Context, objectKey, localPath string, totalSize int64) error {
	// Create output file
	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	var offset int64 = 0

	for offset < totalSize {
		// Calculate chunk size for this iteration (64MB chunks)
		chunkSize := int64(RangeChunkSize)
		if offset+chunkSize > totalSize {
			chunkSize = totalSize - offset
		}

		// Download this chunk using Range header (with retry)
		rangeHeader := fmt.Sprintf("bytes=%d-%d", offset, offset+chunkSize-1)

		var resp *s3.GetObjectOutput
		err := d.retryWithBackoff(ctx, fmt.Sprintf("GetObject range %d-%d", offset, offset+chunkSize-1), func() error {
			r, err := d.client.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(d.storageInfo.ConnectionSettings.Container),
				Key:    aws.String(objectKey),
				Range:  aws.String(rangeHeader),
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
func (d *S3Downloader) DownloadAndDecryptWithResumeAndProgress(ctx context.Context, objectKey, localPath string, encryptionKey, iv []byte, resumeOffset int64, expectedETag string, progressCallback func(float64)) error {
	return d.downloadAndDecryptWithResumeInternal(ctx, objectKey, localPath, encryptionKey, iv, resumeOffset, expectedETag, progressCallback, true)
}

// DownloadAndDecryptWithResume downloads with resume capability from a specific offset
// Useful for resuming interrupted downloads
// NOTE: This validates that the remote file hasn't changed before resuming
func (d *S3Downloader) DownloadAndDecryptWithResume(ctx context.Context, objectKey, localPath string, encryptionKey, iv []byte, resumeOffset int64, expectedETag string) error {
	return d.downloadAndDecryptWithResumeInternal(ctx, objectKey, localPath, encryptionKey, iv, resumeOffset, expectedETag, nil, false)
}

// downloadAndDecryptWithResumeInternal is the internal implementation with optional progress and state saving
func (d *S3Downloader) downloadAndDecryptWithResumeInternal(ctx context.Context, objectKey, localPath string, encryptionKey, iv []byte, resumeOffset int64, expectedETag string, progressCallback func(float64), saveState bool) error {
	// Get object metadata (with retry)
	var headResp *s3.HeadObjectOutput
	err := d.retryWithBackoff(ctx, "HeadObject", func() error {
		resp, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(d.storageInfo.ConnectionSettings.Container),
			Key:    aws.String(objectKey),
		})
		headResp = resp
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to get object metadata: %w", err)
	}

	totalSize := *headResp.ContentLength
	currentETag := ""
	if headResp.ETag != nil {
		currentETag = *headResp.ETag
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
		chunkSize := int64(RangeChunkSize)
		if offset+chunkSize > totalSize {
			chunkSize = totalSize - offset
		}

		rangeHeader := fmt.Sprintf("bytes=%d-%d", offset, offset+chunkSize-1)

		var resp *s3.GetObjectOutput
		err := d.retryWithBackoff(ctx, fmt.Sprintf("GetObject range %d-%d", offset, offset+chunkSize-1), func() error {
			r, err := d.client.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(d.storageInfo.ConnectionSettings.Container),
				Key:    aws.String(objectKey),
				Range:  aws.String(rangeHeader),
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
				RemotePath:      objectKey,
				FileID:          "",
				TotalSize:       totalSize,
				DownloadedBytes: offset,
				ETag:            currentETag,
				CreatedAt:       createdAt,
				LastUpdate:      time.Now(),
				StorageType:     "S3Storage",
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
