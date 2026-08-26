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
	"log"
	"path/filepath"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
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

// UploadLimits reports Azure's block blob ceilings. A committed block list stops
// at 50,000 blocks — and unlike S3 that only fails at CommitBlockList, after the
// whole file has been staged. The planner works in plaintext, so the size it
// reports leaves room for the padding CBC adds to the final part.
func (p *Provider) UploadLimits() resources.UploadLimits {
	return resources.UploadLimits{
		StorageType: p.StorageType(),
		MaxParts:    constants.MaxAzureUploadBlocks,
		MaxPartSize: constants.MaxAzurePlaintextBlockSize,
	}
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

	// Part size comes from the caller's upload plan, which keeps the block count
	// under MaxAzureUploadBlocks as well as within the memory budget.
	partSize, err := params.PartSize(p.UploadLimits())
	if err != nil {
		return nil, err
	}

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
		UploadID:     "", // Azure doesn't have upload IDs like S3
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
// Uses progressReadSeekCloser to track bytes in real-time via ByteProgressCallback.
func (p *Provider) UploadCiphertext(ctx context.Context, uploadState *transfer.StreamingUpload, partIndex int64, ciphertext []byte) (*transfer.PartResult, error) {
	providerData, ok := uploadState.ProviderData.(*azureProviderData)
	if !ok {
		return nil, fmt.Errorf("invalid provider data for Azure streaming upload")
	}

	// Generate block ID (must be consistent and base64-encoded)
	blockIDStr := fmt.Sprintf("block-%010d", partIndex)
	blockID := base64.StdEncoding.EncodeToString([]byte(blockIDStr))

	partCtx, cancel := context.WithTimeout(ctx, constants.PartOperationTimeout)
	defer cancel()

	if deadline, ok := partCtx.Deadline(); ok {
		log.Printf("[AZURE] Block %d: remaining deadline %v", partIndex, time.Until(deadline).Round(time.Second))
	}

	// Stage the block using AzureClient
	err := providerData.azureClient.RetryWithBackoff(partCtx, fmt.Sprintf("StageBlock %d", partIndex), func() error {
		client := providerData.azureClient.Client()
		blockBlobClient := client.ServiceClient().NewContainerClient(providerData.container).NewBlockBlobClient(providerData.blobPath)

		// Use progress-tracking reader if callback is set
		var reader io.ReadSeekCloser
		if uploadState.ByteProgressCallback != nil {
			reader = &transfer.UploadProgressReader{
				Reader:    bytes.NewReader(ciphertext),
				Callback:  uploadState.ByteProgressCallback,
				Threshold: transfer.ProgressReaderThreshold,
			}
		} else {
			reader = &readSeekCloser{Reader: bytes.NewReader(ciphertext)}
		}

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
		PartNumber: int32(partIndex + 1),   // 1-based for consistency with S3
		ETag:       blockID,                // Azure uses block ID instead of ETag
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
		"streamingformat": to.Ptr("cbc"),                                   // Marks file as CBC-chained streaming
		"partsize":        to.Ptr(fmt.Sprintf("%d", uploadState.PartSize)), // Required for correct download decryption
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
		UploadID:     "", // Azure doesn't have upload IDs like S3
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
	// We could optionally list the staged blocks to verify they exist, but:
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
	azureClient, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return 0, "", 0, nil, fmt.Errorf("failed to get Azure client: %w", err)
	}

	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		return 0, "", 0, nil, fmt.Errorf("failed to refresh credentials: %w", err)
	}

	props, err := azureClient.GetBlobProperties(ctx, remotePath)
	if err != nil {
		return 0, "", 0, nil, fmt.Errorf("failed to get blob properties: %w", err)
	}

	// Azure preserves whatever casing the writer used for metadata names, so the
	// map is normalized before it is read.
	format, err := transfer.ParseObjectFormat(transfer.NormalizeMetadataPointers(props.Metadata))
	if err != nil {
		return 0, "", 0, nil, err
	}
	return format.Version, format.FileID, format.PartSize, format.IV, nil
}

// DownloadStreaming downloads and decrypts a file using HKDF streaming format (v1).
// This is for backward compatibility with files uploaded before v3.2.0.
// Format metadata (fileId, partSize) is read from Azure blob metadata.
func (p *Provider) DownloadStreaming(ctx context.Context, remotePath, localPath string, masterKey []byte, progressCallback cloud.ProgressCallback) error {
	azureClient, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to get Azure client: %w", err)
	}

	return transfer.DownloadHKDFStream(ctx, transfer.HKDFStreamParams{
		LocalPath:        localPath,
		MasterKey:        masterKey,
		Retry:            azureClient.RetryWithBackoff,
		Refresh:          azureClient.EnsureFreshCredentials,
		ProgressCallback: progressCallback,
		Stat: func(statCtx context.Context) (int64, map[string]string, error) {
			props, err := azureClient.GetBlobProperties(statCtx, remotePath)
			if err != nil {
				return 0, nil, fmt.Errorf("failed to get blob properties: %w", err)
			}
			return props.ContentLength, transfer.NormalizeMetadataPointers(props.Metadata), nil
		},
		// DownloadRangeOnce is the non-retrying variant: the shared driver owns
		// the retry loop and the per-attempt timeout.
		Open: func(attemptCtx context.Context, offset, length int64) (io.ReadCloser, error) {
			resp, err := azureClient.DownloadRangeOnce(attemptCtx, remotePath, offset, length, "")
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		},
	})
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
// progressCallback (optional) is called with bytes downloaded for smooth progress.
// Wraps request+read+close in single retry with progress rollback on failure.
func (p *Provider) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, progressCallback func(int64)) ([]byte, error) {
	// Get or create Azure client
	azureClient, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get Azure client: %w", err)
	}

	// DownloadRangeOnce is the non-retrying variant: FetchRangeWithRetry owns
	// the retry loop, the per-attempt timeout, and the progress rollback.
	return transfer.FetchRangeWithRetry(ctx, azureClient.RetryWithBackoff, offset, length, progressCallback,
		func(attemptCtx context.Context, offset, length int64) (io.ReadCloser, error) {
			resp, err := azureClient.DownloadRangeOnce(attemptCtx, remotePath, offset, length, "")
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		})
}
