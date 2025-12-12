// Package upload provides the canonical entry point for file uploads to Rescale cloud storage.
//
// Version: 3.2.4
// Date: 2025-12-10
package upload

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/providers"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/models"
	internaltransfer "github.com/rescale/rescale-int/internal/transfer"
)

// UploadParams consolidates all parameters for upload operations.
// This is the single canonical way to specify upload options.
type UploadParams struct {
	// Required: Path to the local file to upload
	LocalPath string

	// Optional: Target folder ID (empty = MyLibrary)
	FolderID string

	// Required: API client for Rescale operations
	APIClient *api.Client

	// Optional: Progress callback (receives values from 0.0 to 1.0)
	ProgressCallback cloud.ProgressCallback

	// Optional: Transfer handle for concurrent part uploads
	// If nil or threads <= 1, uses sequential upload
	TransferHandle *internaltransfer.Transfer

	// Optional: Output writer for status messages
	OutputWriter io.Writer

	// Optional: Encryption mode
	// false (default) = streaming encryption (no temp file, saves disk space)
	// true = pre-encryption (creates temp file, compatible with legacy clients)
	PreEncrypt bool
}

// UploadFile is THE ONLY canonical entry point for uploading files to Rescale cloud storage.
// It handles credential fetching, uploads the file with encryption, and registers it with Rescale.
//
// Default behavior (streaming mode):
//   - Encrypts on-the-fly without creating temp files
//   - Uses concurrent part uploads if TransferHandle has threads > 1
//   - Stores format metadata in cloud object metadata for later detection
//
// Pre-encrypted mode (when PreEncrypt=true):
//   - Creates encrypted temp file first
//   - Uses concurrent part uploads if TransferHandle has threads > 1
//   - Compatible with legacy Rescale clients (e.g., Python client)
//
// Returns the registered CloudFile on success, or an error on failure.
func UploadFile(ctx context.Context, params UploadParams) (*models.CloudFile, error) {
	// Validate required parameters
	if params.LocalPath == "" {
		return nil, fmt.Errorf("local path is required")
	}
	if params.APIClient == nil {
		return nil, fmt.Errorf("API client is required")
	}

	// Validate file exists and is not a directory
	fileInfo, err := os.Stat(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	if fileInfo.IsDir() {
		return nil, fmt.Errorf("cannot upload a directory: %s", params.LocalPath)
	}

	// Start SHA-512 hash calculation concurrently - it runs in parallel with
	// credential fetching, provider creation, and the upload itself.
	// The hash is only needed for file registration AFTER upload completes.
	// For large files (58GB), this avoids a 20-30 second blocking delay at startup.
	type hashResult struct {
		hash string
		err  error
	}
	hashChan := make(chan hashResult, 1)
	go func() {
		hash, err := encryption.CalculateSHA512(params.LocalPath)
		hashChan <- hashResult{hash: hash, err: err}
	}()

	// Get the global credential manager (caches user profile, credentials, and folders)
	credManager := credentials.GetManager(params.APIClient)

	// Get user profile to determine storage type (cached for 5 minutes)
	profile, err := credManager.GetUserProfile(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	// Get root folders (for currentFolderId in file registration) (cached for 5 minutes)
	folders, err := credManager.GetRootFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get root folders: %w", err)
	}

	// Create provider using factory
	factory := providers.NewFactory()
	provider, err := factory.NewTransferFromStorageInfo(ctx, &profile.DefaultStorage, params.APIClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider: %w", err)
	}

	var result *cloud.UploadResult

	// Upload based on encryption mode
	if params.PreEncrypt {
		// Pre-encrypt mode: use PreEncryptUploader interface
		result, err = uploadPreEncrypt(ctx, provider, params, fileInfo.Size())
	} else {
		// Streaming mode: use StreamingConcurrentUploader interface
		result, err = uploadStreaming(ctx, provider, params, fileInfo.Size())
	}

	if err != nil {
		return nil, fmt.Errorf("%s upload failed: %w", profile.DefaultStorage.StorageType, err)
	}

	// Wait for hash calculation to complete (started at beginning of function)
	// For large files, the hash calculation runs in parallel with the entire upload,
	// so this wait should be minimal or zero.
	hashRes := <-hashChan
	if hashRes.err != nil {
		return nil, fmt.Errorf("failed to calculate file hash: %w", hashRes.err)
	}
	fileHash := hashRes.hash

	// Determine target folder
	targetFolder := folders.MyLibrary
	if params.FolderID != "" {
		targetFolder = params.FolderID
	}

	// Build file registration request
	filename := filepath.Base(params.LocalPath)
	fileReq := &models.CloudFileRequest{
		TypeID:               1, // INPUT_FILE
		Name:                 filename,
		CurrentFolderID:      targetFolder,
		EncodedEncryptionKey: encryption.EncodeBase64(result.EncryptionKey),
		PathParts: models.CloudFilePathParts{
			Container: profile.DefaultStorage.ConnectionSettings.Container,
			Path:      result.StoragePath,
		},
		Storage: models.CloudFileStorage{
			ID:             profile.DefaultStorage.ID,
			StorageType:    profile.DefaultStorage.StorageType,
			EncryptionType: profile.DefaultStorage.EncryptionType,
		},
		IsUploaded:    true,
		DecryptedSize: fileInfo.Size(),
		FileChecksums: []models.FileChecksum{
			{
				HashFunction: "sha512",
				FileHash:     fileHash,
			},
		},
	}

	// Register file with Rescale
	cloudFile, err := params.APIClient.RegisterFile(ctx, fileReq)
	if err != nil {
		// Provide helpful context based on error type
		fileName := filepath.Base(params.LocalPath)
		if strings.Contains(err.Error(), "TLS handshake timeout") {
			return nil, fmt.Errorf("failed to register file %s (connection pool exhausted - try reducing --max-concurrent): %w",
				fileName, err)
		}
		if strings.Contains(err.Error(), "rate limiter") {
			return nil, fmt.Errorf("failed to register file %s (rate limited - this is temporary): %w",
				fileName, err)
		}
		if strings.Contains(err.Error(), "timeout") {
			return nil, fmt.Errorf("failed to register file %s (API timeout - check network): %w",
				fileName, err)
		}
		return nil, fmt.Errorf("failed to register file %s: %w", fileName, err)
	}

	return cloudFile, nil
}

// encryptedPart holds an encrypted part ready for upload (used for pipelining).
// v3.4.0: Enables encryption to run ahead of uploads.
type encryptedPart struct {
	partIndex  int64
	ciphertext []byte
	plainSize  int64 // Original plaintext size for accurate tracking
	err        error
}

// uploadStreaming uses the StreamingConcurrentUploader interface for streaming uploads.
// v3.4.0: Pipelined architecture - encryption runs ahead of uploads for 30-50% speedup.
func uploadStreaming(ctx context.Context, provider cloud.CloudTransfer, params UploadParams, fileSize int64) (*cloud.UploadResult, error) {
	// Cast to StreamingConcurrentUploader
	streamingUploader, ok := provider.(transfer.StreamingConcurrentUploader)
	if !ok {
		return nil, fmt.Errorf("provider does not support streaming upload")
	}

	// Initialize streaming upload
	initParams := transfer.StreamingUploadInitParams{
		LocalPath:    params.LocalPath,
		FileSize:     fileSize,
		OutputWriter: params.OutputWriter,
	}

	uploadState, err := streamingUploader.InitStreamingUpload(ctx, initParams)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize streaming upload: %w", err)
	}

	// Open file
	file, err := os.Open(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// v3.4.0: Pipelined upload - encryption runs ahead of network uploads
	// Buffer 2-3 encrypted parts (32-48 MB) to hide network latency
	// Encryption is still sequential (CBC constraint), but overlaps with uploads
	encryptedChan := make(chan encryptedPart, 3)

	// Encryption goroutine: reads file, encrypts parts, sends to channel
	// Must be sequential due to CBC chaining constraint
	go func() {
		defer close(encryptedChan)
		buffer := make([]byte, uploadState.PartSize)
		var partIndex int64 = 0

		for {
			// Check for context cancellation
			select {
			case <-ctx.Done():
				encryptedChan <- encryptedPart{err: ctx.Err()}
				return
			default:
			}

			n, readErr := file.Read(buffer)
			if n > 0 {
				// Make copy of plaintext (buffer will be reused)
				plaintext := make([]byte, n)
				copy(plaintext, buffer[:n])

				// Encrypt this part (sequential, CBC constraint)
				ciphertext, encErr := streamingUploader.EncryptStreamingPart(ctx, uploadState, partIndex, plaintext)
				if encErr != nil {
					encryptedChan <- encryptedPart{err: encErr}
					return
				}

				// Send encrypted part to upload goroutine
				encryptedChan <- encryptedPart{
					partIndex:  partIndex,
					ciphertext: ciphertext,
					plainSize:  int64(n),
				}
				partIndex++
			}

			if readErr == io.EOF {
				return
			}
			if readErr != nil {
				encryptedChan <- encryptedPart{err: fmt.Errorf("failed to read file: %w", readErr)}
				return
			}
		}
	}()

	// Upload loop: receives encrypted parts from channel and uploads
	// Can overlap with encryption (pipelining)
	var parts []*transfer.PartResult

	for enc := range encryptedChan {
		// Check for encryption error
		if enc.err != nil {
			_ = streamingUploader.AbortStreamingUpload(ctx, uploadState)
			return nil, enc.err
		}

		// Upload the encrypted part
		partResult, err := streamingUploader.UploadCiphertext(ctx, uploadState, enc.partIndex, enc.ciphertext)
		if err != nil {
			_ = streamingUploader.AbortStreamingUpload(ctx, uploadState)
			return nil, err
		}

		// Update size to plaintext size for accurate tracking
		partResult.Size = enc.plainSize
		parts = append(parts, partResult)

		// Report progress
		if params.ProgressCallback != nil {
			progress := float64(len(parts)) / float64(uploadState.TotalParts)
			params.ProgressCallback(progress)
		}
	}

	// Complete upload
	result, err := streamingUploader.CompleteStreamingUpload(ctx, uploadState, parts)
	if err != nil {
		return nil, fmt.Errorf("failed to complete streaming upload: %w", err)
	}

	return result, nil
}

// uploadPreEncrypt uses the PreEncryptUploader interface for pre-encrypted uploads.
func uploadPreEncrypt(ctx context.Context, provider cloud.CloudTransfer, params UploadParams, fileSize int64) (*cloud.UploadResult, error) {
	// Cast to PreEncryptUploader
	preEncryptUploader, ok := provider.(transfer.PreEncryptUploader)
	if !ok {
		return nil, fmt.Errorf("provider does not support pre-encrypt upload")
	}

	// Generate encryption key and IV
	encryptionKey, iv, randomSuffix, err := GenerateEncryptionParams()
	if err != nil {
		return nil, fmt.Errorf("failed to generate encryption params: %w", err)
	}

	// Create encrypted temp file
	encryptedPath, err := CreateEncryptedTempFile(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(encryptedPath)

	// Encrypt file
	if params.OutputWriter != nil {
		fmt.Fprintf(params.OutputWriter, "Encrypting file (%s)...\n", filepath.Base(params.LocalPath))
	}
	if err := encryption.EncryptFile(params.LocalPath, encryptedPath, encryptionKey, iv); err != nil {
		return nil, fmt.Errorf("failed to encrypt file: %w", err)
	}

	// Build upload params (providers stat the encrypted file themselves)
	uploadParams := transfer.EncryptedFileUploadParams{
		LocalPath:        params.LocalPath,
		EncryptedPath:    encryptedPath,
		EncryptionKey:    encryptionKey,
		IV:               iv,
		RandomSuffix:     randomSuffix,
		OriginalSize:     fileSize,
		ProgressCallback: params.ProgressCallback,
		TransferHandle:   params.TransferHandle,
		OutputWriter:     params.OutputWriter,
	}

	// Upload encrypted file
	result, err := preEncryptUploader.UploadEncryptedFile(ctx, uploadParams)
	if err != nil {
		return nil, fmt.Errorf("failed to upload encrypted file: %w", err)
	}

	// Clean up resume state
	state.DeleteUploadState(params.LocalPath)

	return result, nil
}

