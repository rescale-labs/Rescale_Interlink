// Package azure provides an Azure implementation of the CloudTransfer interface.
// This file implements the StreamingConcurrentUploader, StreamingConcurrentDownloader,
// and StreamingPartDownloader interfaces for concurrent streaming uploads/downloads.
//
// CBC chaining format for Rescale platform compatibility.
// Upload metadata uses `iv` field (like legacy format) instead of formatVersion/fileId/partSize.
// Download supports both legacy and HKDF formats for backward compatibility.
package azure

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/resources"
)

// Verify that Provider implements StreamingConcurrentUploader, StreamingConcurrentDownloader,
// and StreamingPartDownloader interfaces
var _ transfer.StreamingConcurrentUploader = (*Provider)(nil)
var _ transfer.StreamingConcurrentDownloader = (*Provider)(nil)
var _ transfer.StreamingPartDownloader = (*Provider)(nil)

// azureProviderData contains Azure-specific data for the upload.
type azureProviderData struct {
	container    string
	blobPath     string
	encryptState *transfer.StreamingEncryptionState
	azureClient  *AzureClient
	blockIDs     []string // Track uploaded block IDs in order
}

// InitStreamingUpload initializes a block blob upload with streaming encryption.
// Uses CBC chaining format compatible with Rescale platform.
// Metadata stores `iv` (base64) for Rescale decryption compatibility.
func (p *Provider) InitStreamingUpload(ctx context.Context, params transfer.StreamingUploadInitParams) (*transfer.StreamingUpload, error) {
	// Get or create Azure client
	azureClient, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get Azure client: %w", err)
	}

	// Ensure fresh credentials
	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		return nil, fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Generate random suffix for blob name
	randomSuffix, err := encryption.GenerateSecureRandomString(22)
	if err != nil {
		return nil, fmt.Errorf("failed to generate random suffix: %w", err)
	}

	// Build blob path
	filename := filepath.Base(params.LocalPath)
	blobName := fmt.Sprintf("%s-%s", filename, randomSuffix)

	// Path to return for Rescale API registration
	var storagePath string
	if p.storageInfo.ConnectionSettings.PathPartsBase != "" {
		storagePath = fmt.Sprintf("%s/%s", p.storageInfo.ConnectionSettings.PathPartsBase, blobName)
	} else {
		storagePath = blobName
	}

	// Calculate part size dynamically based on file size and available threads
	// This optimizes chunk size for memory constraints and throughput
	numThreads := constants.MaxThreadsPerFile // Default thread count for large files
	partSize := resources.CalculateDynamicChunkSize(params.FileSize, numThreads)

	// Create streaming encryption state (CBC chaining)
	encryptState, err := transfer.NewStreamingEncryptionState(partSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create encryption state: %w", err)
	}

	// Calculate total parts
	totalParts := transfer.CalculateTotalParts(params.FileSize, partSize)

	// Note: "Initialized streaming upload" message removed to prevent visual artifacts
	// during concurrent multi-file uploads. The message was low-value information
	// that caused ghost progress bar copies when interleaved with mpb output.
	_ = params.OutputWriter // Suppress unused warning - writer still used for other messages

	// Pre-allocate block IDs slice
	blockIDs := make([]string, totalParts)

	return &transfer.StreamingUpload{
		UploadID:     "",        // Azure doesn't have upload IDs like S3
		StoragePath:  storagePath,
		MasterKey:    encryptState.GetKey(),
		InitialIV:    encryptState.GetInitialIV(),
		FileID:       nil, // Not used in CBC format
		PartSize:     partSize,
		LocalPath:    params.LocalPath,
		TotalSize:    params.FileSize,
		TotalParts:   totalParts,
		RandomSuffix: randomSuffix,
		ProviderData: &azureProviderData{
			container:    azureClient.Container(),
			blobPath:     blobName,
			encryptState: encryptState,
			azureClient:  azureClient,
			blockIDs:     blockIDs,
		},
	}, nil
}

// UploadStreamingPart encrypts and uploads a single block.
// Uses CBC chaining - parts MUST be uploaded sequentially.
// The orchestrator already calls this sequentially (see upload.go:217).
func (p *Provider) UploadStreamingPart(ctx context.Context, uploadState *transfer.StreamingUpload, partIndex int64, plaintext []byte) (*transfer.PartResult, error) {
	providerData, ok := uploadState.ProviderData.(*azureProviderData)
	if !ok {
		return nil, fmt.Errorf("invalid provider data for Azure streaming upload")
	}

	// Determine if this is the final part
	isFinal := (partIndex == uploadState.TotalParts-1)

	// Encrypt this part with CBC chaining
	ciphertext, err := providerData.encryptState.EncryptPart(plaintext, isFinal)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt block %d: %w", partIndex, err)
	}

	// Generate block ID (must be consistent and base64-encoded)
	blockIDStr := fmt.Sprintf("block-%010d", partIndex)
	blockID := base64.StdEncoding.EncodeToString([]byte(blockIDStr))

	// Create context with timeout
	partCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Stage the block using AzureClient
	err = providerData.azureClient.RetryWithBackoff(partCtx, fmt.Sprintf("StageBlock %d", partIndex), func() error {
		client := providerData.azureClient.Client()
		blockBlobClient := client.ServiceClient().NewContainerClient(providerData.container).NewBlockBlobClient(providerData.blobPath)
		reader := &readSeekCloser{Reader: bytes.NewReader(ciphertext)}
		_, err := blockBlobClient.StageBlock(partCtx, blockID, reader, nil)
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("failed to stage block %d: %w", partIndex, err)
	}

	// Store block ID at the correct index
	providerData.blockIDs[partIndex] = blockID

	return &transfer.PartResult{
		PartIndex:  partIndex,
		PartNumber: int32(partIndex + 1), // 1-based for consistency with S3
		ETag:       blockID,              // Azure uses block ID instead of ETag
		Size:       int64(len(plaintext)),
	}, nil
}

// EncryptStreamingPart encrypts plaintext and returns ciphertext.
// Must be called sequentially due to CBC chaining constraint.
// Separated from upload to enable pipelining.
func (p *Provider) EncryptStreamingPart(ctx context.Context, uploadState *transfer.StreamingUpload, partIndex int64, plaintext []byte) ([]byte, error) {
	providerData, ok := uploadState.ProviderData.(*azureProviderData)
	if !ok {
		return nil, fmt.Errorf("invalid provider data for Azure streaming upload")
	}

	// Determine if this is the final part
	isFinal := (partIndex == uploadState.TotalParts-1)

	// Encrypt this part with CBC chaining
	ciphertext, err := providerData.encryptState.EncryptPart(plaintext, isFinal)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt block %d: %w", partIndex, err)
	}

	return ciphertext, nil
}

// UploadCiphertext uploads already-encrypted data to cloud storage.
// Can be called concurrently with EncryptStreamingPart (pipelining).
// Separated from encryption to enable pipelining.
func (p *Provider) UploadCiphertext(ctx context.Context, uploadState *transfer.StreamingUpload, partIndex int64, ciphertext []byte) (*transfer.PartResult, error) {
	providerData, ok := uploadState.ProviderData.(*azureProviderData)
	if !ok {
		return nil, fmt.Errorf("invalid provider data for Azure streaming upload")
	}

	// Generate block ID (must be consistent and base64-encoded)
	blockIDStr := fmt.Sprintf("block-%010d", partIndex)
	blockID := base64.StdEncoding.EncodeToString([]byte(blockIDStr))

	// Create context with timeout
	partCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Stage the block using AzureClient
	err := providerData.azureClient.RetryWithBackoff(partCtx, fmt.Sprintf("StageBlock %d", partIndex), func() error {
		client := providerData.azureClient.Client()
		blockBlobClient := client.ServiceClient().NewContainerClient(providerData.container).NewBlockBlobClient(providerData.blobPath)
		reader := &readSeekCloser{Reader: bytes.NewReader(ciphertext)}
		_, err := blockBlobClient.StageBlock(partCtx, blockID, reader, nil)
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("failed to stage block %d: %w", partIndex, err)
	}

	// Store block ID at the correct index
	providerData.blockIDs[partIndex] = blockID

	return &transfer.PartResult{
		PartIndex:  partIndex,
		PartNumber: int32(partIndex + 1), // 1-based for consistency with S3
		ETag:       blockID,              // Azure uses block ID instead of ETag
		Size:       int64(len(ciphertext)), // Note: ciphertext size, not plaintext
	}, nil
}

// CompleteStreamingUpload commits the block list.
// Returns IV for Rescale-compatible format (FormatVersion=0).
func (p *Provider) CompleteStreamingUpload(ctx context.Context, uploadState *transfer.StreamingUpload, parts []*transfer.PartResult) (*cloud.UploadResult, error) {
	providerData, ok := uploadState.ProviderData.(*azureProviderData)
	if !ok {
		return nil, fmt.Errorf("invalid provider data for Azure streaming upload")
	}

	// Build ordered block ID list from the stored IDs
	blockIDs := make([]string, len(parts))
	for _, part := range parts {
		blockIDs[part.PartIndex] = providerData.blockIDs[part.PartIndex]
	}

	// Metadata uses `iv` field for Rescale compatibility.
	// `streamingformat: cbc` enables streaming download (no temp file).
	metadata := map[string]*string{
		"iv":              to.Ptr(encryption.EncodeBase64(uploadState.InitialIV)),
		"streamingformat": to.Ptr("cbc"), // Marks file as CBC-chained streaming
	}

	// Commit block list using AzureClient
	err := providerData.azureClient.RetryWithBackoff(ctx, "CommitBlockList", func() error {
		client := providerData.azureClient.Client()
		blockBlobClient := client.ServiceClient().NewContainerClient(providerData.container).NewBlockBlobClient(providerData.blobPath)
		_, err := blockBlobClient.CommitBlockList(ctx, blockIDs, &blockblob.CommitBlockListOptions{
			Metadata: metadata,
		})
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("failed to commit block list: %w", err)
	}

	// Return IV for Rescale-compatible format
	return &cloud.UploadResult{
		StoragePath:   uploadState.StoragePath,
		EncryptionKey: uploadState.MasterKey,
		IV:            uploadState.InitialIV, // IV for Rescale compatibility
		FormatVersion: 0,                     // Legacy format (uses IV in metadata)
		FileID:        "",                    // Not used in CBC format
		PartSize:      uploadState.PartSize,
	}, nil
}

// AbortStreamingUpload cleans up an aborted streaming upload.
// For Azure, uncommitted blocks are automatically cleaned up by the service.
func (p *Provider) AbortStreamingUpload(ctx context.Context, uploadState *transfer.StreamingUpload) error {
	// Azure automatically cleans up uncommitted blocks after a timeout (typically 7 days)
	// There's no explicit abort operation needed like S3's AbortMultipartUpload
	return nil
}

// InitStreamingUploadFromState resumes a streaming upload with existing encryption params.
// Uses CBC chaining with InitialIV and CurrentIV for resume support.
func (p *Provider) InitStreamingUploadFromState(ctx context.Context, params transfer.StreamingUploadResumeParams) (*transfer.StreamingUpload, error) {
	// Get or create Azure client
	azureClient, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get Azure client: %w", err)
	}

	// Ensure fresh credentials
	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		return nil, fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Create encryption state from existing keys using CBC chaining with InitialIV and CurrentIV
	var encryptState *transfer.StreamingEncryptionState
	if params.InitialIV != nil && params.CurrentIV != nil {
		// CBC format resume
		encryptState, err = transfer.NewStreamingEncryptionStateFromKey(
			params.MasterKey, params.InitialIV, params.CurrentIV, params.PartSize)
	} else {
		// Cannot resume legacy HKDF format with new code - start fresh
		return nil, fmt.Errorf("cannot resume legacy HKDF upload with v3.2.0; please restart upload")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create encryption state from resume: %w", err)
	}

	// Calculate total parts
	totalParts := transfer.CalculateTotalParts(params.FileSize, params.PartSize)

	// Extract blob name from storage path
	blobName := filepath.Base(params.StoragePath)

	if params.OutputWriter != nil {
		fmt.Fprintf(params.OutputWriter, "Resuming streaming upload: %d blocks of %d MB\n",
			totalParts, params.PartSize/(1024*1024))
	}

	// Pre-allocate block IDs slice
	blockIDs := make([]string, totalParts)

	return &transfer.StreamingUpload{
		UploadID:     "",                  // Azure doesn't have upload IDs like S3
		StoragePath:  params.StoragePath,
		MasterKey:    params.MasterKey,
		InitialIV:    params.InitialIV,
		FileID:       nil, // Not used in CBC format
		PartSize:     params.PartSize,
		LocalPath:    params.LocalPath,
		TotalSize:    params.FileSize,
		TotalParts:   totalParts,
		RandomSuffix: params.RandomSuffix,
		ProviderData: &azureProviderData{
			container:    azureClient.Container(),
			blobPath:     blobName,
			encryptState: encryptState,
			azureClient:  azureClient,
			blockIDs:     blockIDs,
		},
	}, nil
}

// ValidateStreamingUploadExists checks if a streaming upload can be resumed.
// For Azure: blocks auto-expire after 7 days, so we validate via state age check.
// The state validation (in state/upload.go) already enforces MaxResumeAge of 7 days,
// which aligns with Azure's uncommitted block retention period.
// Returns (exists, error) where exists=false means upload expired and should start fresh.
func (p *Provider) ValidateStreamingUploadExists(ctx context.Context, uploadID, storagePath string) (bool, error) {
	// Azure doesn't have an explicit upload ID like S3's multipart uploads.
	// Uncommitted blocks are automatically cleaned up after ~7 days.
	// The resume state validation already checks age < MaxResumeAge (7 days),
	// so if we reach here, the state is valid and blocks should still exist.
	//
	// We could optionally call GetBlockList to verify blocks exist, but:
	// 1. It adds latency and API calls
	// 2. Deterministic encryption means we can re-upload any missing blocks
	// 3. State validation already handles the age check
	//
	// For simplicity and consistency with the Azure cleanup model, we return true.
	return true, nil
}

// readSeekCloser wraps bytes.Reader to implement io.ReadSeekCloser
type readSeekCloser struct {
	*bytes.Reader
}

func (rsc *readSeekCloser) Close() error {
	return nil
}

// =============================================================================
// StreamingConcurrentDownloader Interface Implementation
// Supports both legacy (IV in metadata) and HKDF (formatVersion/fileId/partSize) formats.
// =============================================================================

// DetectFormat detects the encryption format from Azure blob metadata.
// Returns: formatVersion (0=legacy, 1=HKDF streaming, 2=CBC streaming), fileId (base64), partSize, iv, error
// Both new uploads (IV/CBC) and old uploads (HKDF) are supported for download.
func (p *Provider) DetectFormat(ctx context.Context, remotePath string) (int, string, int64, []byte, error) {
	// Get or create Azure client
	azureClient, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return 0, "", 0, nil, fmt.Errorf("failed to get Azure client: %w", err)
	}

	// Ensure fresh credentials
	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		return 0, "", 0, nil, fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get blob properties (includes metadata)
	props, err := azureClient.GetBlobProperties(ctx, remotePath)
	if err != nil {
		return 0, "", 0, nil, fmt.Errorf("failed to get blob properties: %w", err)
	}

	// Check for streaming format metadata
	// Note: Azure may title-case metadata keys, so check multiple variants
	metadata := props.Metadata

	if metadata == nil {
		return 0, "", 0, nil, nil // No metadata, legacy format
	}

	// Check formatVersion (try various case combinations)
	var formatVersionStr string
	for _, key := range []string{"formatversion", "formatVersion", "FormatVersion", "Formatversion"} {
		if v, ok := metadata[key]; ok && v != nil {
			formatVersionStr = *v
			break
		}
	}

	if formatVersionStr == "1" {
		// HKDF streaming format (backward compatibility for files uploaded before v3.2.0)
		// Get fileId
		var fileId string
		for _, key := range []string{"fileid", "fileId", "FileId", "Fileid"} {
			if v, ok := metadata[key]; ok && v != nil {
				fileId = *v
				break
			}
		}
		if fileId == "" {
			return 0, "", 0, nil, fmt.Errorf("streaming format missing fileId in metadata")
		}

		// Get partSize
		var partSizeStr string
		for _, key := range []string{"partsize", "partSize", "PartSize", "Partsize"} {
			if v, ok := metadata[key]; ok && v != nil {
				partSizeStr = *v
				break
			}
		}
		if partSizeStr == "" {
			return 0, "", 0, nil, fmt.Errorf("streaming format missing partSize in metadata")
		}

		var partSize int64
		if _, err := fmt.Sscanf(partSizeStr, "%d", &partSize); err != nil {
			return 0, "", 0, nil, fmt.Errorf("invalid partSize in metadata: %w", err)
		}

		return 1, fileId, partSize, nil, nil
	}

	// Check for CBC streaming format (v3.2.4+) and get IV from metadata
	var iv []byte
	for _, key := range []string{"iv", "IV", "Iv"} {
		if v, ok := metadata[key]; ok && v != nil && *v != "" {
			iv, err = encryption.DecodeBase64(*v)
			if err != nil {
				// IV decode failed - might be provided via FileInfo instead
				iv = nil
			}
			break
		}
	}

	// Check for streamingformat: cbc
	for _, key := range []string{"streamingformat", "streamingFormat", "StreamingFormat", "Streamingformat"} {
		if v, ok := metadata[key]; ok && v != nil && *v == "cbc" {
			// CBC streaming format - uploaded by rescale-int v3.2.4+
			// Can use streaming download (no temp file) with sequential part decryption
			return 2, "", 0, iv, nil
		}
	}

	// Legacy format - file uploaded by Rescale platform or older rescale-int
	// Must use downloadLegacy() with temp file
	return 0, "", 0, iv, nil
}

// DownloadStreaming downloads and decrypts a file using HKDF streaming format (v1).
// This is for backward compatibility with files uploaded before v3.2.0.
// Format metadata (fileId, partSize) is read from Azure blob metadata.
func (p *Provider) DownloadStreaming(ctx context.Context, remotePath, localPath string, masterKey []byte, progressCallback cloud.ProgressCallback) error {
	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	// Get or create Azure client
	azureClient, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to get Azure client: %w", err)
	}

	// Ensure fresh credentials
	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		return fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get blob properties and streaming format metadata
	props, err := azureClient.GetBlobProperties(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("failed to get blob properties: %w", err)
	}

	encryptedSize := props.ContentLength
	metadata := props.Metadata

	// Get fileId from metadata
	var fileIdStr string
	for _, key := range []string{"fileid", "fileId", "FileId", "Fileid"} {
		if v, ok := metadata[key]; ok && v != nil {
			fileIdStr = *v
			break
		}
	}
	if fileIdStr == "" {
		return fmt.Errorf("streaming format missing fileId in metadata")
	}
	fileId, err := encryption.DecodeBase64(fileIdStr)
	if err != nil {
		return fmt.Errorf("failed to decode fileId: %w", err)
	}

	// Get partSize from metadata
	var partSizeStr string
	for _, key := range []string{"partsize", "partSize", "PartSize", "Partsize"} {
		if v, ok := metadata[key]; ok && v != nil {
			partSizeStr = *v
			break
		}
	}
	if partSizeStr == "" {
		return fmt.Errorf("streaming format missing partSize in metadata")
	}
	var partSize int64
	if _, err := fmt.Sscanf(partSizeStr, "%d", &partSize); err != nil {
		return fmt.Errorf("invalid partSize in metadata: %s", partSizeStr)
	}

	// Create HKDF streaming decryptor (legacy format)
	decryptor, err := encryption.NewStreamingDecryptor(masterKey, fileId, partSize)
	if err != nil {
		return fmt.Errorf("failed to create streaming decryptor: %w", err)
	}

	// Calculate encrypted part size (includes PKCS7 padding overhead per part for HKDF format)
	encryptedPartSize := encryption.CalculateEncryptedPartSize(partSize)

	// Calculate number of parts
	numParts := (encryptedSize + encryptedPartSize - 1) / encryptedPartSize

	// Check disk space before download
	estimatedPlaintextSize := encryptedSize - (numParts * 16) // Approximate padding overhead
	if err := diskspace.CheckAvailableSpace(localPath, estimatedPlaintextSize, 1.15); err != nil {
		return &diskspace.InsufficientSpaceError{
			Path:           localPath,
			RequiredBytes:  estimatedPlaintextSize,
			AvailableBytes: diskspace.GetAvailableSpace(filepath.Dir(localPath)),
		}
	}

	// Start periodic refresh for large files
	if encryptedSize > constants.LargeFileThreshold {
		azureClient.StartPeriodicRefresh(ctx)
		defer azureClient.StopPeriodicRefresh()
	}

	// Create output file (plaintext, no temp file needed!)
	outFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	// Download and decrypt each part
	var downloadedBytes int64 = 0
	for partIndex := int64(0); partIndex < numParts; partIndex++ {
		// Calculate byte range for this encrypted part
		startByte := partIndex * encryptedPartSize
		endByte := startByte + encryptedPartSize - 1
		if endByte >= encryptedSize {
			endByte = encryptedSize - 1
		}
		rangeSize := endByte - startByte + 1

		// Download this part's ciphertext with retry using AzureClient
		var resp azblob.DownloadStreamResponse
		err := azureClient.RetryWithBackoff(ctx, fmt.Sprintf("DownloadPart %d", partIndex), func() error {
			r, err := azureClient.DownloadRange(ctx, remotePath, startByte, rangeSize)
			resp = r
			return err
		})
		if err != nil {
			return fmt.Errorf("failed to download part %d: %w", partIndex, err)
		}

		// Read encrypted part data
		ciphertext, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("failed to read part %d: %w", partIndex, err)
		}

		// Decrypt this part (HKDF format - each part has own key/IV)
		plaintext, err := decryptor.DecryptPart(partIndex, ciphertext)
		if err != nil {
			return fmt.Errorf("failed to decrypt part %d: %w", partIndex, err)
		}

		// Write plaintext to output file
		if _, err := outFile.Write(plaintext); err != nil {
			return fmt.Errorf("failed to write part %d: %w", partIndex, err)
		}

		downloadedBytes += int64(len(ciphertext))
		if progressCallback != nil && encryptedSize > 0 {
			progressCallback(float64(downloadedBytes) / float64(encryptedSize))
		}
	}

	// Report 100% at end
	if progressCallback != nil {
		progressCallback(1.0)
	}

	return nil
}

// =============================================================================
// StreamingPartDownloader Interface Implementation
// These methods enable concurrent streaming downloads by allowing the orchestrator
// to download individual encrypted parts in parallel.
// =============================================================================

// GetEncryptedSize returns the total encrypted size of the blob in Azure.
// This is used by the concurrent download orchestrator to calculate the number of parts.
func (p *Provider) GetEncryptedSize(ctx context.Context, remotePath string) (int64, error) {
	// Get or create Azure client
	azureClient, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get Azure client: %w", err)
	}

	// Ensure fresh credentials
	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		return 0, fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get blob properties
	props, err := azureClient.GetBlobProperties(ctx, remotePath)
	if err != nil {
		return 0, fmt.Errorf("failed to get blob properties: %w", err)
	}

	return props.ContentLength, nil
}

// DownloadEncryptedRange downloads a specific byte range of the encrypted blob from Azure.
// This is used by the concurrent download orchestrator to download individual parts.
// The range is: [offset, offset+length).
func (p *Provider) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64) ([]byte, error) {
	// Get or create Azure client
	azureClient, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get Azure client: %w", err)
	}

	// Download the specified range with retry
	var resp azblob.DownloadStreamResponse
	err = azureClient.RetryWithBackoff(ctx, fmt.Sprintf("DownloadRange [%d-%d]", offset, offset+length), func() error {
		r, err := azureClient.DownloadRange(ctx, remotePath, offset, length)
		resp = r
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to download range [%d-%d]: %w", offset, offset+length-1, err)
	}
	defer resp.Body.Close()

	// Read the data
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read range data: %w", err)
	}

	return data, nil
}
