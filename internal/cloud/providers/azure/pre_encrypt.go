// Package azure provides an Azure implementation of the CloudTransfer interface.
// This file implements the PreEncryptUploader interface for pre-encrypted uploads.
//
// Concurrent upload logic is implemented directly in the provider.
package azure

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/util/buffers"
)

// Verify that Provider implements PreEncryptUploader
var _ transfer.PreEncryptUploader = (*Provider)(nil)

// UploadEncryptedFile uploads an already-encrypted file to Azure.
// This implements the PreEncryptUploader interface.
// The encryption is already done by the orchestrator; this method handles the state.
// Uses AzureClient directly.
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
// Uses AzureClient directly.
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
// Uses AzureClient directly.
func (p *Provider) uploadEncryptedBlockBlob(ctx context.Context, azureClient *AzureClient, params transfer.EncryptedFileUploadParams, blobPath, pathForRescale string, encryptedSize int64) error {
	file, err := os.Open(params.EncryptedPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Calculate block size dynamically based on file size
	numThreads := constants.MaxThreadsPerFile
	blockSize := resources.CalculateDynamicChunkSize(encryptedSize, numThreads)
	totalBlocks := (encryptedSize + blockSize - 1) / blockSize

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

// uploadEncryptedBlockBlobConcurrent uploads an encrypted file using concurrent block blob staging.
// Uses worker goroutines to stage multiple blocks in parallel, then commits the block list.
// Supports resume from interrupted uploads via state file.
func (p *Provider) uploadEncryptedBlockBlobConcurrent(ctx context.Context, azureClient *AzureClient, params transfer.EncryptedFileUploadParams, blobPath, pathForRescale string, encryptedSize int64) error {
	// If no transfer handle provided, fall back to sequential upload
	if params.TransferHandle == nil || params.TransferHandle.GetThreads() <= 1 {
		return p.uploadEncryptedBlockBlob(ctx, azureClient, params, blobPath, pathForRescale, encryptedSize)
	}

	file, err := os.Open(params.EncryptedPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	totalSize := encryptedSize
	// Calculate part size dynamically based on file size and available threads
	numThreads := constants.MaxThreadsPerFile
	partSize := resources.CalculateDynamicChunkSize(totalSize, numThreads)
	totalBlocks := int64((totalSize + partSize - 1) / partSize)
	concurrency := params.TransferHandle.GetThreads()

	// Ensure cleanup on completion
	defer params.TransferHandle.Complete()

	// Acquire upload lock to prevent concurrent uploads of the same file
	uploadLock, lockErr := state.AcquireUploadLock(params.LocalPath)
	if lockErr != nil {
		return fmt.Errorf("failed to acquire upload lock: %w", lockErr)
	}
	defer state.ReleaseUploadLock(uploadLock)

	// Try to load resume state
	existingState, loadErr := state.LoadUploadState(params.LocalPath)
	if loadErr != nil {
		log.Printf("Warning: Failed to load resume state: %v", loadErr)
	}
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
			return fmt.Errorf("failed to seek to resume position: %w", err)
		}

		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Resuming upload from block %d/%d (%.1f%%) with %d concurrent threads\n",
				startBlock+1, totalBlocks,
				float64(uploadedBytes)/float64(totalSize)*100,
				concurrency)
		}
	}

	if !resuming {
		createdAt = time.Now()
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Uploading with %d concurrent threads (%d blocks)\n", concurrency, totalBlocks)
		}
	}

	// Concurrent upload implementation
	type blockJob struct {
		blockIndex int64
		blockID    string
		data       []byte
	}

	type blockResult struct {
		blockIndex int64
		blockID    string
		size       int64
		err        error
	}

	// Channels for coordination
	jobChan := make(chan blockJob, concurrency*2)
	resultChan := make(chan blockResult, totalBlocks)

	// Error handling: use context cancellation to signal workers to stop
	opCtx, cancelOp := context.WithCancel(ctx)
	defer cancelOp()

	var firstError error
	var errorMu sync.Mutex
	var errorOnce sync.Once
	setError := func(err error) {
		errorOnce.Do(func() {
			errorMu.Lock()
			firstError = err
			errorMu.Unlock()
			cancelOp()
		})
	}

	// Start worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					setError(fmt.Errorf("worker %d panicked: %v", workerID, r))
					log.Printf("PANIC in Azure upload worker %d: %v", workerID, r)
				}
			}()

			for job := range jobChan {
				// Check if context was cancelled
				select {
				case <-opCtx.Done():
					return
				default:
				}

				// Stage this block with retry logic
				startTime := time.Now()
				blockDataToUpload := job.data
				currentBlockID := job.blockID

				// Create context with timeout for this specific block (v4.0.4: use centralized constant)
				blockCtx, cancel := context.WithTimeout(opCtx, constants.PartOperationTimeout)

				stageErr := azureClient.RetryWithBackoff(blockCtx, fmt.Sprintf("StageBlock %d/%d", job.blockIndex+1, totalBlocks), func() error {
					client := azureClient.Client()
					blockBlobClient := client.ServiceClient().NewContainerClient(azureClient.Container()).NewBlockBlobClient(blobPath)
					_, err := blockBlobClient.StageBlock(blockCtx, currentBlockID, &readSeekCloser{Reader: bytes.NewReader(blockDataToUpload)}, nil)
					return err
				})

				cancel()

				if stageErr != nil {
					setError(fmt.Errorf("failed to stage block %d/%d: %w", job.blockIndex+1, totalBlocks, stageErr))
					return
				}

				// Calculate and record throughput
				duration := time.Since(startTime).Seconds()
				if duration > 0 {
					bytesPerSec := float64(len(blockDataToUpload)) / duration
					params.TransferHandle.RecordThroughput(bytesPerSec)
				}

				// Send result
				resultChan <- blockResult{
					blockIndex: job.blockIndex,
					blockID:    job.blockID,
					size:       int64(len(job.data)),
					err:        nil,
				}
			}
		}(i)
	}

	// Read blocks from file and queue them for upload
	go func() {
		defer close(jobChan)

		blockIndex := startBlock
		for {
			select {
			case <-opCtx.Done():
				return
			default:
			}

			// Get buffer from pool for this block
			bufferPtr := buffers.GetChunkBuffer()
			buffer := *bufferPtr

			// Read up to partSize bytes into pooled buffer
			n, readErr := io.ReadFull(file, buffer)

			if readErr == io.EOF {
				buffers.PutChunkBuffer(bufferPtr)
				break
			}

			// Get the actual data slice and make a copy
			var blockData []byte
			if readErr == io.ErrUnexpectedEOF {
				blockData = make([]byte, n)
				copy(blockData, buffer[:n])
				readErr = nil
			} else if readErr != nil {
				buffers.PutChunkBuffer(bufferPtr)
				setError(fmt.Errorf("failed to read file chunk: %w", readErr))
				return
			} else {
				blockData = make([]byte, n)
				copy(blockData, buffer[:n])
			}

			buffers.PutChunkBuffer(bufferPtr)

			// Generate block ID
			blockID := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("block-%06d", blockIndex)))

			// Queue this block for upload
			jobChan <- blockJob{
				blockIndex: blockIndex,
				blockID:    blockID,
				data:       blockData,
			}

			blockIndex++

			if int64(len(blockData)) < partSize {
				break
			}
		}
	}()

	// Pre-allocate blockIDs slice for ordering
	allBlockIDs := make([]string, totalBlocks)
	// Copy existing block IDs from resume state
	for i := int64(0); i < startBlock; i++ {
		allBlockIDs[i] = blockIDs[i]
	}

	// Collect results and update progress
	var resultsMu sync.Mutex
	var atomicUploadedBytes int64 = uploadedBytes
	resultCount := 0
	expectedResults := int(totalBlocks - startBlock)

	// Wait for results in a separate goroutine
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect all results
	for result := range resultChan {
		if result.err != nil {
			setError(result.err)
			break
		}

		// Add to block IDs at correct position
		resultsMu.Lock()
		allBlockIDs[result.blockIndex] = result.blockID
		resultsMu.Unlock()

		// Update progress atomically
		atomic.AddInt64(&atomicUploadedBytes, result.size)

		// Update progress callback
		if params.ProgressCallback != nil {
			params.ProgressCallback(float64(atomic.LoadInt64(&atomicUploadedBytes)) / float64(totalSize))
		}

		resultCount++

		// Periodically save resume state
		saveInterval := 5
		if expectedResults > 20 {
			saveInterval = expectedResults / 4
		}
		if resultCount%saveInterval == 0 {
			resultsMu.Lock()
			// Build current block IDs list (only completed blocks)
			currentBlockIDs := make([]string, 0, resultCount+int(startBlock))
			for i := int64(0); i < int64(resultCount)+startBlock; i++ {
				if allBlockIDs[i] != "" {
					currentBlockIDs = append(currentBlockIDs, allBlockIDs[i])
				}
			}
			currentState := &state.UploadResumeState{
				LocalPath:     params.LocalPath,
				EncryptedPath: params.EncryptedPath,
				ObjectKey:     pathForRescale,
				TotalSize:     totalSize,
				OriginalSize:  params.OriginalSize,
				UploadedBytes: atomic.LoadInt64(&atomicUploadedBytes),
				BlockIDs:      currentBlockIDs,
				EncryptionKey: encryption.EncodeBase64(params.EncryptionKey),
				IV:            encryption.EncodeBase64(params.IV),
				RandomSuffix:  params.RandomSuffix,
				CreatedAt:     createdAt,
				LastUpdate:    time.Now(),
				StorageType:   "AzureStorage",
				ProcessID:     os.Getpid(),
				LockAcquiredAt: uploadLock.AcquiredAt,
			}
			state.SaveUploadState(currentState, params.LocalPath)
			resultsMu.Unlock()
		}
	}

	// Check for errors
	errorMu.Lock()
	if firstError != nil {
		err := firstError
		errorMu.Unlock()
		return err
	}
	errorMu.Unlock()

	// Build final block IDs list (in order)
	finalBlockIDs := make([]string, 0, totalBlocks)
	for i := int64(0); i < totalBlocks; i++ {
		if allBlockIDs[i] != "" {
			finalBlockIDs = append(finalBlockIDs, allBlockIDs[i])
		}
	}

	// Commit block list with metadata
	metadata := map[string]*string{
		"iv": to.Ptr(encryption.EncodeBase64(params.IV)),
	}

	err = azureClient.RetryWithBackoff(ctx, "CommitBlockList", func() error {
		client := azureClient.Client()
		blockBlobClient := client.ServiceClient().NewContainerClient(azureClient.Container()).NewBlockBlobClient(blobPath)
		_, err := blockBlobClient.CommitBlockList(ctx, finalBlockIDs, &blockblob.CommitBlockListOptions{
			Metadata: metadata,
		})
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to commit block list: %w", err)
	}

	// Delete resume state on successful upload
	if delErr := state.DeleteUploadState(params.LocalPath); delErr != nil {
		log.Printf("Warning: Failed to delete resume state after successful upload: %v", delErr)
	}

	return nil
}
