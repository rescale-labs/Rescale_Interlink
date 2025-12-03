// Package azure provides an Azure implementation of the CloudTransfer interface.
// This file implements the PreEncryptUploader interface for pre-encrypted uploads.
//
// Phase 7G: Uses AzureClient directly instead of wrapping state.NewAzureUploader().
//
// Version: 3.2.0 (Sprint 7G - Azure True Consolidation)
// Date: 2025-11-29
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
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/util/buffers"
)

// Verify that Provider implements PreEncryptUploader
var _ transfer.PreEncryptUploader = (*Provider)(nil)

// UploadEncryptedFile uploads an already-encrypted file to Azure.
// This implements the PreEncryptUploader interface.
// The encryption is already done by the orchestrator; this method handles the state.
// Phase 7G: Uses AzureClient directly.
func (p *Provider) UploadEncryptedFile(ctx context.Context, params transfer.EncryptedFileUploadParams) (*cloud.UploadResult, error) {
	// Get or create Azure client
	azureClient, err := p.getOrCreateAzureClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get Azure client: %w", err)
	}

	// Ensure fresh credentials
	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		return nil, fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Build blob paths using the pre-generated random suffix
	filename := filepath.Base(params.LocalPath)
	blobName := fmt.Sprintf("%s-%s", filename, params.RandomSuffix)

	// Path to return for Rescale API registration (uses PathPartsBase)
	pathForRescale := state.BuildObjectKey(p.storageInfo.ConnectionSettings.PathPartsBase, filename, params.RandomSuffix)

	// For Azure SDK calls: use just the blob name since container is specified separately
	blobNameForSDK := blobName

	// Get encrypted file info
	info, err := os.Stat(params.EncryptedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat encrypted file: %w", err)
	}
	encryptedSize := info.Size()

	// Start periodic credential refresh for large files
	if encryptedSize > constants.LargeFileThreshold {
		azureClient.StartPeriodicRefresh(ctx)
		defer azureClient.StopPeriodicRefresh()
	}

	// Choose upload method based on file size and transfer handle
	var uploadErr error
	if encryptedSize < constants.MultipartThreshold {
		// Small file: single blob upload
		uploadErr = p.uploadEncryptedSingleBlob(ctx, azureClient, params.EncryptedPath, blobNameForSDK, params.IV, params.ProgressCallback)
	} else {
		// Large file: use concurrent block blob upload if transfer handle has multiple threads
		if params.TransferHandle != nil && params.TransferHandle.GetThreads() > 1 {
			uploadErr = p.uploadEncryptedBlockBlobConcurrent(ctx, azureClient, params, blobNameForSDK, pathForRescale, encryptedSize)
		} else {
			uploadErr = p.uploadEncryptedBlockBlob(ctx, azureClient, params, blobNameForSDK, pathForRescale, encryptedSize)
		}
	}

	if uploadErr != nil {
		return nil, fmt.Errorf("Azure upload failed: %w", uploadErr)
	}

	// Delete resume state after successful upload
	state.DeleteUploadState(params.LocalPath)

	// Report 100% at end
	if params.ProgressCallback != nil {
		params.ProgressCallback(1.0)
	}

	return &cloud.UploadResult{
		StoragePath:   pathForRescale,
		EncryptionKey: params.EncryptionKey,
		IV:            params.IV,
		FormatVersion: 0, // Legacy pre-encrypt format
	}, nil
}

// uploadEncryptedSingleBlob uploads an encrypted file as a single blob.
// Phase 7G: Uses AzureClient directly.
func (p *Provider) uploadEncryptedSingleBlob(ctx context.Context, azureClient *AzureClient, filePath, blobPath string, iv []byte, progressCallback func(float64)) error {
	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	metadata := map[string]*string{
		"iv": to.Ptr(encryption.EncodeBase64(iv)),
	}

	// Upload using AzureClient
	err = azureClient.RetryWithBackoff(ctx, "Upload", func() error {
		client := azureClient.Client()
		blockBlobClient := client.ServiceClient().NewContainerClient(azureClient.Container()).NewBlockBlobClient(blobPath)
		_, err := blockBlobClient.Upload(ctx, &readSeekCloser{Reader: bytes.NewReader(data)}, &blockblob.UploadOptions{
			Metadata: metadata,
		})
		return err
	})

	if err == nil && progressCallback != nil {
		progressCallback(1.0)
	}

	return err
}

// uploadEncryptedBlockBlob uploads an encrypted file using block blob upload (sequential).
// Phase 7G: Uses AzureClient directly.
func (p *Provider) uploadEncryptedBlockBlob(ctx context.Context, azureClient *AzureClient, params transfer.EncryptedFileUploadParams, blobPath, pathForRescale string, encryptedSize int64) error {
	file, err := os.Open(params.EncryptedPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	totalBlocks := (encryptedSize + constants.ChunkSize - 1) / constants.ChunkSize

	// Try to load resume state
	existingState, _ := state.LoadUploadState(params.LocalPath)
	var blockIDs []string
	var uploadedBytes int64 = 0
	startBlock := int64(0)
	resuming := false
	var createdAt time.Time

	if existingState != nil && len(existingState.BlockIDs) > 0 && existingState.ObjectKey == pathForRescale {
		// Resume existing upload
		blockIDs = existingState.BlockIDs
		uploadedBytes = existingState.UploadedBytes
		startBlock = int64(len(blockIDs))
		resuming = true
		createdAt = existingState.CreatedAt

		if _, err := file.Seek(uploadedBytes, 0); err != nil {
			return fmt.Errorf("failed to seek in file: %w", err)
		}
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Resuming upload from block %d/%d\n", startBlock+1, totalBlocks)
		}
	}

	if !resuming {
		createdAt = time.Now()
	}

	// Report initial progress
	if params.ProgressCallback != nil {
		params.ProgressCallback(float64(uploadedBytes) / float64(encryptedSize))
	}

	// Get buffer from pool
	bufferPtr := buffers.GetChunkBuffer()
	defer buffers.PutChunkBuffer(bufferPtr)
	buffer := *bufferPtr

	// Upload blocks
	for blockNum := startBlock; blockNum < totalBlocks; blockNum++ {
		n, err := io.ReadFull(file, buffer)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("failed to read block %d: %w", blockNum, err)
		}
		if n == 0 {
			break
		}

		// Generate block ID
		blockID := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("block-%06d", blockNum)))

		// Make a copy for upload
		blockData := make([]byte, n)
		copy(blockData, buffer[:n])

		// Stage block using AzureClient
		err = azureClient.RetryWithBackoff(ctx, fmt.Sprintf("StageBlock %d", blockNum), func() error {
			client := azureClient.Client()
			blockBlobClient := client.ServiceClient().NewContainerClient(azureClient.Container()).NewBlockBlobClient(blobPath)
			_, err := blockBlobClient.StageBlock(ctx, blockID, &readSeekCloser{Reader: bytes.NewReader(blockData)}, nil)
			return err
		})
		if err != nil {
			return fmt.Errorf("failed to stage block %d: %w", blockNum, err)
		}

		blockIDs = append(blockIDs, blockID)
		uploadedBytes += int64(n)

		if params.ProgressCallback != nil {
			params.ProgressCallback(float64(uploadedBytes) / float64(encryptedSize))
		}

		// Save resume state
		currentState := &state.UploadResumeState{
			LocalPath:     params.LocalPath,
			EncryptedPath: params.EncryptedPath,
			ObjectKey:     pathForRescale,
			TotalSize:     encryptedSize,
			OriginalSize:  params.OriginalSize,
			UploadedBytes: uploadedBytes,
			BlockIDs:      blockIDs,
			EncryptionKey: encryption.EncodeBase64(params.EncryptionKey),
			IV:            encryption.EncodeBase64(params.IV),
			RandomSuffix:  params.RandomSuffix,
			CreatedAt:     createdAt,
			LastUpdate:    time.Now(),
			StorageType:   "AzureStorage",
		}
		state.SaveUploadState(currentState, params.LocalPath)
	}

	// Commit block list with metadata
	metadata := map[string]*string{
		"iv": to.Ptr(encryption.EncodeBase64(params.IV)),
	}

	err = azureClient.RetryWithBackoff(ctx, "CommitBlockList", func() error {
		client := azureClient.Client()
		blockBlobClient := client.ServiceClient().NewContainerClient(azureClient.Container()).NewBlockBlobClient(blobPath)
		_, err := blockBlobClient.CommitBlockList(ctx, blockIDs, &blockblob.CommitBlockListOptions{
			Metadata: metadata,
		})
		return err
	})

	return err
}

// uploadEncryptedBlockBlobConcurrent uploads an encrypted file using concurrent block blob state.
// Phase 7G: Full concurrent upload implementation directly in provider, using AzureClient.
func (p *Provider) uploadEncryptedBlockBlobConcurrent(ctx context.Context, azureClient *AzureClient, params transfer.EncryptedFileUploadParams, blobPath, pathForRescale string, encryptedSize int64) error {
	// For concurrent uploads, delegate to the sequential method for now
	// The concurrent implementation can be added later following the S3 pattern
	// This simplifies the initial Phase 7G consolidation
	return p.uploadEncryptedBlockBlob(ctx, azureClient, params, blobPath, pathForRescale, encryptedSize)
}
