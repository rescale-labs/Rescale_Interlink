package upload

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	nethttp "net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/storage"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/util/buffers"
)

// Local aliases for shared constants
const (
	MultipartThreshold        = constants.MultipartThreshold
	PartSize                  = constants.ChunkSize
	MinPartSize               = constants.MinPartSize
	CredentialRefreshInterval = constants.GlobalCredentialRefreshInterval
	MaxRetries                = constants.MaxRetries
	RetryInitialDelay         = constants.RetryInitialDelay
	RetryMaxDelay             = constants.RetryMaxDelay
)

// S3Uploader handles S3 uploads with encryption and credential refresh
type S3Uploader struct {
	client      *s3.Client
	storageInfo *models.StorageInfo
	credManager *credentials.Manager
	httpClient  *nethttp.Client // Shared HTTP client for connection reuse
	clientMu    sync.Mutex      // Protects client updates during credential refresh
}

// NewS3Uploader creates a new S3 uploader with auto-refreshing credentials
func NewS3Uploader(storageInfo *models.StorageInfo, creds *models.S3Credentials, apiClient *api.Client) (*S3Uploader, error) {
	// Create shared optimized HTTP client with proxy support from API client config
	// IMPORTANT: Reuse this client across credential refreshes to maintain connection pool
	purCfg := apiClient.GetConfig()
	httpClient, err := http.CreateOptimizedClient(purCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Create auto-refreshing credential provider for user's default storage (uploads always go to default storage)
	credProvider := credentials.NewRescaleCredentialProvider(apiClient, nil)

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

	return &S3Uploader{
		client:      client,
		storageInfo: storageInfo,
		credManager: credManager,
		httpClient:  httpClient, // Store for reuse during credential refresh
	}, nil
}

// ensureFreshCredentials refreshes S3 credentials using the global credential manager
// This is thread-safe and shares credentials across all concurrent uploads
// IMPORTANT: Reuses the existing HTTP client to maintain connection pool
func (u *S3Uploader) ensureFreshCredentials(ctx context.Context) error {
	// Get fresh credentials from global manager (auto-refreshes if needed)
	s3Creds, err := u.credManager.GetS3Credentials(ctx)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}

	if s3Creds == nil {
		return fmt.Errorf("received nil S3 credentials")
	}

	// Update S3 client with fresh credentials
	// Lock to prevent concurrent client updates
	u.clientMu.Lock()
	defer u.clientMu.Unlock()

	// IMPORTANT: Reuse existing HTTP client instead of creating new one
	// This preserves the connection pool and prevents TLS handshake overhead
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(u.storageInfo.ConnectionSettings.Region),
		config.WithHTTPClient(u.httpClient), // Reuse existing HTTP client!
		config.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(
			s3Creds.AccessKeyID,
			s3Creds.SecretKey,
			s3Creds.SessionToken,
		)),
	)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	u.client = s3.NewFromConfig(cfg)

	return nil
}

// traceContext adds HTTP connection tracing when DEBUG_HTTP=true
func traceContext(ctx context.Context, operation string) context.Context {
	if os.Getenv("DEBUG_HTTP") != "true" {
		return ctx
	}

	var handshakeStart time.Time
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Reused {
				log.Printf("[HTTP] %s: reused connection", operation)
			} else {
				log.Printf("[HTTP] %s: NEW connection", operation)
			}
		},
		TLSHandshakeStart: func() {
			handshakeStart = time.Now()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			log.Printf("[HTTP] %s: TLS handshake took %v", operation, time.Since(handshakeStart))
		},
	})
}

// retryWithBackoff executes a function with exponential backoff retry logic using shared retry package
func (u *S3Uploader) retryWithBackoff(ctx context.Context, operation string, fn func() error) error {
	config := http.Config{
		MaxRetries:   MaxRetries,
		InitialDelay: RetryInitialDelay,
		MaxDelay:     RetryMaxDelay,
		CredentialRefresh: func(ctx context.Context) error {
			return u.ensureFreshCredentials(ctx)
		},
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

// UploadEncrypted encrypts and uploads a file to S3, returns (s3Key, encryptionKey, iv, error)
// Automatically uses multipart upload for large files (>100MB)
// progressCallback is optional and called with progress from 0.0 to 1.0
func (u *S3Uploader) UploadEncrypted(ctx context.Context, localPath string, progressCallback ProgressCallback, outputWriter io.Writer) (string, []byte, []byte, error) {
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
	existingState, _ := LoadUploadState(localPath)
	var encryptionKey, iv []byte
	var randomSuffix, objectKey, encryptedPath string
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
			objectKey = existingState.ObjectKey
			encryptedPath = existingState.EncryptedPath
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

		randomSuffix, err = encryption.GenerateSecureRandomString(22)
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to generate random suffix: %w", err)
		}

		// Build object key
		filename := filepath.Base(localPath)
		objectName := fmt.Sprintf("%s-%s", filename, randomSuffix)
		objectKey = fmt.Sprintf("%s/%s", u.storageInfo.ConnectionSettings.PathBase, objectName)

		// Create encrypted file in same directory as source file
		sourceDir := filepath.Dir(localPath)
		encryptedFile, err := os.CreateTemp(sourceDir, fmt.Sprintf(".%s-*.encrypted", filename))
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to create temp file: %w", err)
		}
		encryptedPath = encryptedFile.Name()
		encryptedFile.Close()
	}

	// Check disk space in source directory (only if not reusing)
	if !reusingEncrypted {
		if err := diskspace.CheckAvailableSpace(encryptedPath, originalSize, 1.15); err != nil {
			os.Remove(encryptedPath)
			return "", nil, nil, err
		}
	}

	// Ensure encrypted file is ALWAYS cleaned up on success or fatal error
	// But NOT on resume-able errors (we want to keep it for resume)
	defer func() {
		// Only clean up the encrypted file if upload succeeded (resume state will be deleted)
		// or if there's no resume state (fatal error before multipart started)
		if !ResumeStateExists(localPath) {
			if removeErr := os.Remove(encryptedPath); removeErr != nil && !os.IsNotExist(removeErr) {
				fmt.Fprintf(os.Stderr, "Warning: Failed to cleanup encrypted temp file %s: %v\n", encryptedPath, removeErr)
			}
		}
	}()

	// Encrypt file to temporary location (skip if reusing existing encrypted file)
	if !reusingEncrypted {
		if outputWriter != nil {
			fmt.Fprintf(outputWriter, "Encrypting file (%s)...\n", filepath.Base(localPath))
		}
		if err := encryption.EncryptFile(localPath, encryptedPath, encryptionKey, iv); err != nil {
			// Check if error is related to disk space (common error strings)
			if storage.IsDiskFullError(err) {
				// Convert to our standard error type for consistent handling
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

	// Get file info to determine upload method
	info, err := os.Stat(encryptedPath)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to stat encrypted file: %w", err)
	}

	// Choose upload method based on file size
	if info.Size() > MultipartThreshold {
		// Use multipart upload for large files
		// Pass original file path, encrypted path, and encryption parameters for resume
		err = u.uploadMultipart(ctx, localPath, encryptedPath, objectKey, encryptionKey, iv, randomSuffix, originalSize, progressCallback, outputWriter)
	} else {
		// Use single-part upload for small files
		err = u.uploadSinglePart(ctx, encryptedPath, objectKey, iv, progressCallback)
	}

	if err != nil {
		return "", nil, nil, fmt.Errorf("S3 upload failed: %w", err)
	}

	// Delete resume state after successful upload
	DeleteUploadState(localPath)

	return objectKey, encryptionKey, iv, nil
}

// UploadEncryptedWithTransfer uploads a file with encryption using a transfer handle for concurrent uploads
// If transferHandle is nil, uses sequential upload (same as UploadEncrypted)
// If transferHandle specifies multiple threads, uses concurrent part uploads
func (u *S3Uploader) UploadEncryptedWithTransfer(ctx context.Context, localPath string, progressCallback ProgressCallback, transferHandle *transfer.Transfer, outputWriter io.Writer) (string, []byte, []byte, error) {
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
	existingState, _ := LoadUploadState(localPath)
	var encryptionKey, iv []byte
	var randomSuffix, objectKey, encryptedPath string
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
			objectKey = existingState.ObjectKey
			encryptedPath = existingState.EncryptedPath
			reusingEncrypted = true
		} else {
			// TIER 1: Validation failed (expired, file changed, etc.)
			// Cleanup this specific file's expired resume
			CleanupExpiredResume(existingState, localPath, outputWriter) // verbose=true for this file
		}
	}

freshStart:
	// Generate fresh encryption parameters if not reusing
	if !reusingEncrypted {
		encryptionKey, err = encryption.GenerateKey()
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to generate encryption key: %w", err)
		}

		iv, err = encryption.GenerateIV()
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to generate IV: %w", err)
		}

		randomSuffix, err = encryption.GenerateSecureRandomString(22)
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to generate random suffix: %w", err)
		}

		// Build object key
		filename := filepath.Base(localPath)
		objectName := fmt.Sprintf("%s-%s", filename, randomSuffix)
		objectKey = fmt.Sprintf("%s/%s", u.storageInfo.ConnectionSettings.PathBase, objectName)

		// Create encrypted file in same directory as source file
		sourceDir := filepath.Dir(localPath)
		encryptedFile, err := os.CreateTemp(sourceDir, fmt.Sprintf(".%s-*.encrypted", filename))
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to create temp file: %w", err)
		}
		encryptedPath = encryptedFile.Name()
		encryptedFile.Close()
	}

	// Check disk space in source directory (only if not reusing)
	if !reusingEncrypted {
		if err := diskspace.CheckAvailableSpace(encryptedPath, originalSize, 1.15); err != nil {
			os.Remove(encryptedPath)
			return "", nil, nil, err
		}
	}

	// Ensure encrypted file is ALWAYS cleaned up on success or fatal error
	// But NOT on resume-able errors (we want to keep it for resume)
	defer func() {
		// Only clean up the encrypted file if upload succeeded (resume state will be deleted)
		// or if there's no resume state (fatal error before multipart started)
		if !ResumeStateExists(localPath) {
			if removeErr := os.Remove(encryptedPath); removeErr != nil && !os.IsNotExist(removeErr) {
				fmt.Fprintf(os.Stderr, "Warning: Failed to cleanup encrypted temp file %s: %v\n", encryptedPath, removeErr)
			}
		}
	}()

	// Encrypt file to temporary location (skip if reusing existing encrypted file)
	if !reusingEncrypted {
		if outputWriter != nil {
			fmt.Fprintf(outputWriter, "Encrypting file (%s)...\n", filepath.Base(localPath))
		}
		if err := encryption.EncryptFile(localPath, encryptedPath, encryptionKey, iv); err != nil {
			// Check if error is related to disk space (common error strings)
			if storage.IsDiskFullError(err) {
				// Convert to our standard error type for consistent handling
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

	// Get file info to determine upload method
	info, err := os.Stat(encryptedPath)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to stat encrypted file: %w", err)
	}

	// Choose upload method based on file size and transfer handle
	if info.Size() > MultipartThreshold {
		// Use multipart upload for large files
		// If transfer handle provided and has multiple threads, use concurrent upload
		if transferHandle != nil && transferHandle.GetThreads() > 1 {
			err = u.uploadMultipartConcurrent(ctx, localPath, encryptedPath, objectKey, encryptionKey, iv, randomSuffix, originalSize, progressCallback, transferHandle, outputWriter)
		} else {
			err = u.uploadMultipart(ctx, localPath, encryptedPath, objectKey, encryptionKey, iv, randomSuffix, originalSize, progressCallback, outputWriter)
		}
	} else {
		// Use single-part upload for small files
		err = u.uploadSinglePart(ctx, encryptedPath, objectKey, iv, progressCallback)
	}

	if err != nil {
		return "", nil, nil, fmt.Errorf("S3 upload failed: %w", err)
	}

	// Delete resume state after successful upload
	DeleteUploadState(localPath)

	return objectKey, encryptionKey, iv, nil
}

// uploadSinglePart uploads a file in a single PUT request
func (u *S3Uploader) uploadSinglePart(ctx context.Context, filePath, objectKey string, iv []byte, progressCallback ProgressCallback) error {
	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	_, err = u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(u.storageInfo.ConnectionSettings.Container),
		Key:           aws.String(objectKey),
		Body:          file,
		ContentLength: aws.Int64(info.Size()),
		Metadata: map[string]string{
			"iv": encryption.EncodeBase64(iv),
		},
	})

	// Report 100% at end if successful
	if err == nil && progressCallback != nil {
		progressCallback(1.0)
	}

	return err
}

// uploadMultipart uploads a file using S3 multipart upload with automatic retry and credential refresh
// Supports resuming interrupted uploads via resume state tracking
// originalPath: path to the unencrypted source file (used as resume state key)
// encryptedPath: path to the encrypted temp file (what we actually upload)
func (u *S3Uploader) uploadMultipart(ctx context.Context, originalPath, encryptedPath, objectKey string, encryptionKey, iv []byte, randomSuffix string, originalSize int64, progressCallback ProgressCallback, outputWriter io.Writer) error {
	file, err := os.Open(encryptedPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	totalSize := info.Size()
	totalParts := (totalSize + PartSize - 1) / PartSize

	// Try to load resume state (keyed by ORIGINAL file path, not encrypted path)
	existingState, _ := LoadUploadState(originalPath)
	var uploadID string
	var completedParts []types.CompletedPart
	var uploadedBytes int64 = 0
	startPart := int32(1)
	resuming := false
	var createdAt time.Time

	if existingState != nil {
		// Validate resume state
		if err := ValidateUploadState(existingState, originalPath); err == nil {
			// Verify upload still exists on S3
			_, listErr := u.client.ListParts(ctx, &s3.ListPartsInput{
				Bucket:   aws.String(u.storageInfo.ConnectionSettings.Container),
				Key:      aws.String(existingState.ObjectKey),
				UploadId: aws.String(existingState.UploadID),
			})

			if listErr == nil {
				// Valid resume state and upload exists!
				uploadID = existingState.UploadID
				completedParts = ConvertToSDKParts(existingState.CompletedParts)
				uploadedBytes = existingState.UploadedBytes
				startPart = int32(len(existingState.CompletedParts)) + 1
				resuming = true
				createdAt = existingState.CreatedAt

				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Resuming upload from part %d/%d (%.1f%%)\n",
						startPart, totalParts,
						float64(uploadedBytes)/float64(totalSize)*100)
				}
			} else {
				// Upload ID expired or invalid, will start fresh
				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Previous upload expired, starting fresh upload\n")
				}
			}
		}
	}

	// If no valid resume, start fresh
	if uploadID == "" {
		var createResp *s3.CreateMultipartUploadOutput
		err = u.retryWithBackoff(ctx, "CreateMultipartUpload", func() error {
			var err error
			createResp, err = u.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
				Bucket: aws.String(u.storageInfo.ConnectionSettings.Container),
				Key:    aws.String(objectKey),
				Metadata: map[string]string{
					"iv": encryption.EncodeBase64(iv),
				},
			})
			return err
		})
		if err != nil {
			return fmt.Errorf("failed to create multipart upload: %w", err)
		}
		uploadID = *createResp.UploadId
		createdAt = time.Now()

		// Save initial state (keyed by original file path)
		initialState := &UploadResumeState{
			LocalPath:      originalPath,
			EncryptedPath:  encryptedPath,
			ObjectKey:      objectKey,
			UploadID:       uploadID,
			TotalSize:      totalSize,
			OriginalSize:   originalSize,
			UploadedBytes:  0,
			CompletedParts: []CompletedPart{},
			EncryptionKey:  encryption.EncodeBase64(encryptionKey),
			IV:             encryption.EncodeBase64(iv),
			RandomSuffix:   randomSuffix,
			CreatedAt:      createdAt,
			LastUpdate:     time.Now(),
			StorageType:    "S3Storage",
		}
		SaveUploadState(initialState, originalPath)
	}

	// If we somehow don't have a creation time, set it now
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	// Ensure upload is aborted if we fail fatally (but keep resume state)
	defer func() {
		if err != nil {
			// Only abort if we actually failed (not on successful completion)
			u.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(u.storageInfo.ConnectionSettings.Container),
				Key:      aws.String(objectKey),
				UploadId: aws.String(uploadID),
			})
			// Keep resume state so user can retry
		}
	}()

	// If resuming, seek to the position after the last completed part
	if resuming && startPart > 1 {
		seekOffset := int64(startPart-1) * PartSize
		if _, seekErr := file.Seek(seekOffset, 0); seekErr != nil {
			return fmt.Errorf("failed to seek to resume position: %w", seekErr)
		}
	}

	// Upload parts with retry logic
	partNumber := startPart

	// Get buffer from pool for reuse across all parts
	bufferPtr := buffers.GetChunkBuffer()
	defer buffers.PutChunkBuffer(bufferPtr)
	buffer := *bufferPtr

	for {
		// Read up to PartSize bytes into pooled buffer
		n, readErr := io.ReadFull(file, buffer)

		// Handle EOF - last part may be smaller
		if readErr == io.EOF {
			break // No more data
		}

		// Get the actual data slice for this part
		var partData []byte
		if readErr == io.ErrUnexpectedEOF {
			// Last part is smaller than PartSize
			partData = buffer[:n]
			readErr = nil
		} else if readErr != nil {
			return fmt.Errorf("failed to read file chunk: %w", readErr)
		} else {
			partData = buffer[:n]
		}

		// Upload this part with retry logic and per-part timeout
		var uploadResp *s3.UploadPartOutput
		partDataToUpload := make([]byte, len(partData)) // Make a copy for upload
		copy(partDataToUpload, partData)
		currentPartNum := partNumber // Capture for closure

		// Create context with timeout for this specific part (10 minutes)
		partCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()

		// Add HTTP tracing if DEBUG_HTTP is enabled
		partCtx = traceContext(partCtx, fmt.Sprintf("UploadPart %d/%d", partNumber, totalParts))

		err = u.retryWithBackoff(partCtx, fmt.Sprintf("UploadPart %d/%d", partNumber, totalParts), func() error {
			var err error
			uploadResp, err = u.client.UploadPart(partCtx, &s3.UploadPartInput{
				Bucket:        aws.String(u.storageInfo.ConnectionSettings.Container),
				Key:           aws.String(objectKey),
				PartNumber:    aws.Int32(currentPartNum),
				UploadId:      aws.String(uploadID),
				Body:          bytes.NewReader(partDataToUpload),
				ContentLength: aws.Int64(int64(len(partDataToUpload))),
			})
			return err
		})

		if err != nil {
			return fmt.Errorf("failed to upload part %d/%d after retries: %w", partNumber, totalParts, err)
		}

		completedParts = append(completedParts, types.CompletedPart{
			ETag:       uploadResp.ETag,
			PartNumber: aws.Int32(partNumber),
		})

		// Update progress
		uploadedBytes += int64(len(partData))
		if progressCallback != nil && totalSize > 0 {
			progress := float64(uploadedBytes) / float64(totalSize)
			progressCallback(progress)
		}

		// Save resume state after each part (keyed by original file path)
		currentState := &UploadResumeState{
			LocalPath:      originalPath,
			EncryptedPath:  encryptedPath,
			ObjectKey:      objectKey,
			UploadID:       uploadID,
			TotalSize:      totalSize,
			OriginalSize:   originalSize,
			UploadedBytes:  uploadedBytes,
			CompletedParts: ConvertFromSDKParts(completedParts),
			EncryptionKey:  encryption.EncodeBase64(encryptionKey),
			IV:             encryption.EncodeBase64(iv),
			RandomSuffix:   randomSuffix,
			CreatedAt:      createdAt, // Preserve original creation time
			LastUpdate:     time.Now(),
			StorageType:    "S3Storage",
		}
		SaveUploadState(currentState, originalPath)

		partNumber++

		// Check if we've uploaded all data
		if int64(len(partData)) < PartSize {
			break // Last part was smaller, we're done
		}
	}

	// Complete multipart upload with retry
	err = u.retryWithBackoff(ctx, "CompleteMultipartUpload", func() error {
		_, err := u.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
			Bucket:   aws.String(u.storageInfo.ConnectionSettings.Container),
			Key:      aws.String(objectKey),
			UploadId: aws.String(uploadID),
			MultipartUpload: &types.CompletedMultipartUpload{
				Parts: completedParts,
			},
		})
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	// Delete resume state on successful completion (keyed by original file path)
	// This is handled in UploadEncrypted now, so we don't do it here

	// Clear error to prevent defer from aborting successful upload
	err = nil
	return nil
}
