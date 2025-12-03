// Package azure provides an Azure implementation of the CloudTransfer interface.
// This file implements the LegacyDownloader interface for legacy (v0) format downloads.
//
// Note: StreamingConcurrentDownloader (DetectFormat, DownloadStreaming) is already
// implemented in streaming_concurrent.go.
//
// Phase 7G: Uses AzureClient directly instead of wrapping state.NewAzureDownloader().
//
// Version: 3.2.0 (Sprint 7G - Azure True Consolidation)
// Date: 2025-11-29
package azure

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	internaltransfer "github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/constants"
)

// Verify that Provider implements LegacyDownloader
var _ transfer.LegacyDownloader = (*Provider)(nil)

// DownloadEncryptedFile downloads an encrypted file (legacy v0 format) to a local path.
// This implements the LegacyDownloader interface.
// The orchestrator handles decryption; this method just downloads the encrypted bytes.
// Phase 7H: Uses AzureClient with file-aware credential handling.
func (p *Provider) DownloadEncryptedFile(ctx context.Context, params transfer.LegacyDownloadParams) error {
	// Get or create Azure client (with file-aware credential handling)
	azureClient, err := p.getOrCreateAzureClientForFile(ctx, params.FileInfo)
	if err != nil {
		return fmt.Errorf("failed to get Azure client: %w", err)
	}

	// Ensure fresh credentials
	if err := azureClient.EnsureFreshCredentials(ctx); err != nil {
		return fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get file size from params, or fetch from Azure if not provided
	fileSize := params.FileSize
	if fileSize == 0 {
		// Fetch file size from Azure metadata
		props, err := azureClient.GetBlobProperties(ctx, params.RemotePath)
		if err != nil {
			return fmt.Errorf("failed to get blob properties: %w", err)
		}
		fileSize = props.ContentLength
	}

	// Start periodic refresh for large files
	if fileSize > constants.LargeFileThreshold {
		azureClient.StartPeriodicRefresh(ctx)
		defer azureClient.StopPeriodicRefresh()
	}

	// Choose download method based on file size and transfer handle
	if fileSize > constants.MultipartThreshold {
		// Use chunked download for large files
		if params.TransferHandle != nil && params.TransferHandle.GetThreads() > 1 {
			// Concurrent chunked download
			return p.downloadChunkedConcurrent(
				ctx,
				azureClient,
				params.RemotePath,
				params.EncryptedPath,
				fileSize,
				params.ProgressCallback,
				params.TransferHandle,
			)
		}
		// Sequential chunked download
		return p.downloadChunkedWithProgress(
			ctx,
			azureClient,
			params.RemotePath,
			params.EncryptedPath,
			fileSize,
			params.ProgressCallback,
		)
	}

	// Small file: single download
	return p.downloadSingleWithProgress(
		ctx,
		azureClient,
		params.RemotePath,
		params.EncryptedPath,
		params.ProgressCallback,
		fileSize,
	)
}

// downloadSingleWithProgress downloads a blob in a single GET request with progress callback.
// Phase 7G: Uses AzureClient directly.
func (p *Provider) downloadSingleWithProgress(ctx context.Context, azureClient *AzureClient, remotePath, localPath string, progressCallback func(float64), totalSize int64) error {
	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	// Download blob using AzureClient
	resp, err := azureClient.DownloadStream(ctx, remotePath, nil)
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

	// If no progress callback or no total size, just copy directly
	if progressCallback == nil || totalSize <= 0 {
		_, err = io.Copy(file, resp.Body)
		if err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}
		return nil
	}

	// Copy with progress tracking using 32KB buffer
	var downloaded int64
	buffer := make([]byte, 32*1024)
	lastProgress := 0.0

	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				return fmt.Errorf("failed to write file: %w", writeErr)
			}
			downloaded += int64(n)

			// Update progress (throttle to avoid excessive updates)
			progress := float64(downloaded) / float64(totalSize)
			if progress-lastProgress >= 0.01 || progress >= 1.0 { // Update every 1% or at completion
				progressCallback(progress)
				lastProgress = progress
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("failed to read: %w", readErr)
		}
	}

	// Ensure 100% is reported
	progressCallback(1.0)
	return nil
}

// downloadChunkedWithProgress downloads a blob in chunks with progress callback.
// Phase 7G: Uses AzureClient directly.
func (p *Provider) downloadChunkedWithProgress(ctx context.Context, azureClient *AzureClient, remotePath, localPath string, totalSize int64, progressCallback func(float64)) error {
	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	// Create output file
	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	var offset int64 = 0

	for offset < totalSize {
		// Calculate chunk size for this iteration (64MB chunks)
		chunkSize := int64(constants.ChunkSize)
		if offset+chunkSize > totalSize {
			chunkSize = totalSize - offset
		}

		// Download this chunk using Range
		resp, err := azureClient.DownloadRange(ctx, remotePath, offset, chunkSize)
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

		// Update progress after each chunk
		if progressCallback != nil && totalSize > 0 {
			progressCallback(float64(offset) / float64(totalSize))
		}
	}

	return nil
}

// downloadChunkedConcurrent downloads a blob using concurrent range requests.
// Phase 7G: Full concurrent download implementation directly in provider, using AzureClient.
func (p *Provider) downloadChunkedConcurrent(ctx context.Context, azureClient *AzureClient, remotePath, localPath string, totalSize int64, progressCallback func(float64), transferHandle *internaltransfer.Transfer) error {
	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	// Check for existing resume state
	existingState, _ := state.LoadDownloadState(localPath)
	var completedChunksSet map[int64]bool = make(map[int64]bool)
	var downloadedBytes int64 = 0
	var currentETag string

	// Get current blob ETag for resume validation
	props, err := azureClient.GetBlobProperties(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("failed to get blob properties: %w", err)
	}
	currentETag = props.ETag

	if existingState != nil {
		// Validate resume state
		if existingState.ETag == currentETag && existingState.TotalSize == totalSize {
			// Resume is valid - convert slice to set for O(1) lookup
			for _, chunkIdx := range existingState.CompletedChunks {
				completedChunksSet[chunkIdx] = true
			}
			downloadedBytes = existingState.DownloadedBytes
		} else {
			// ETag mismatch or size changed - start fresh
			state.DeleteDownloadState(localPath)
		}
	}

	// Create or open output file
	flags := os.O_CREATE | os.O_RDWR
	file, err := os.OpenFile(localPath, flags, 0644)
	if err != nil {
		return fmt.Errorf("failed to create/open file: %w", err)
	}
	defer file.Close()

	// Truncate/extend file to final size for concurrent writes
	if err := file.Truncate(totalSize); err != nil {
		return fmt.Errorf("failed to truncate file: %w", err)
	}

	// Calculate chunks
	chunkSize := int64(constants.ChunkSize)
	totalChunks := (totalSize + chunkSize - 1) / chunkSize

	// Build list of chunks to download
	var chunksToDownload []int64
	for chunkIdx := int64(0); chunkIdx < totalChunks; chunkIdx++ {
		if !completedChunksSet[chunkIdx] {
			chunksToDownload = append(chunksToDownload, chunkIdx)
		}
	}

	if len(chunksToDownload) == 0 {
		// All chunks already downloaded
		if progressCallback != nil {
			progressCallback(1.0)
		}
		return nil
	}

	// Set up worker pool
	numWorkers := 4
	if transferHandle != nil && transferHandle.GetThreads() > 0 {
		numWorkers = transferHandle.GetThreads()
	}

	// Limit workers to number of chunks
	if numWorkers > len(chunksToDownload) {
		numWorkers = len(chunksToDownload)
	}

	// Channel for chunk indices
	chunkChan := make(chan int64, len(chunksToDownload))
	for _, chunkIdx := range chunksToDownload {
		chunkChan <- chunkIdx
	}
	close(chunkChan)

	// Error channel
	errChan := make(chan error, numWorkers)

	// Track progress atomically
	var atomicDownloaded int64 = downloadedBytes
	createdAt := time.Now()
	if existingState != nil {
		createdAt = existingState.CreatedAt
	}

	// Mutex for file writes and state updates
	var fileMu sync.Mutex
	var stateMu sync.Mutex

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for chunkIdx := range chunkChan {
				// Calculate byte range
				startByte := chunkIdx * chunkSize
				endByte := startByte + chunkSize
				if endByte > totalSize {
					endByte = totalSize
				}
				rangeSize := endByte - startByte

				// Download chunk with retry
				var resp azblob.DownloadStreamResponse
				err := azureClient.RetryWithBackoff(ctx, fmt.Sprintf("DownloadChunk %d", chunkIdx), func() error {
					r, err := azureClient.DownloadRange(ctx, remotePath, startByte, rangeSize)
					resp = r
					return err
				})
				if err != nil {
					errChan <- fmt.Errorf("failed to download chunk %d: %w", chunkIdx, err)
					return
				}

				// Read chunk data
				chunkData, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					errChan <- fmt.Errorf("failed to read chunk %d: %w", chunkIdx, err)
					return
				}

				// Write to file at correct offset
				fileMu.Lock()
				_, err = file.WriteAt(chunkData, startByte)
				fileMu.Unlock()
				if err != nil {
					errChan <- fmt.Errorf("failed to write chunk %d: %w", chunkIdx, err)
					return
				}

				// Update progress
				newDownloaded := atomic.AddInt64(&atomicDownloaded, rangeSize)
				if progressCallback != nil && totalSize > 0 {
					progressCallback(float64(newDownloaded) / float64(totalSize))
				}

				// Save resume state
				stateMu.Lock()
				completedChunksSet[chunkIdx] = true
				// Convert set back to slice for serialization
				completedChunksSlice := make([]int64, 0, len(completedChunksSet))
				for idx := range completedChunksSet {
					completedChunksSlice = append(completedChunksSlice, idx)
				}
				currentState := &state.DownloadResumeState{
					LocalPath:       localPath,
					EncryptedPath:   localPath,
					RemotePath:      remotePath,
					TotalSize:       totalSize,
					DownloadedBytes: newDownloaded,
					ETag:            currentETag,
					ChunkSize:       chunkSize,
					CompletedChunks: completedChunksSlice,
					CreatedAt:       createdAt,
					LastUpdate:      time.Now(),
					StorageType:     "AzureStorage",
				}
				state.SaveDownloadState(currentState, localPath)
				stateMu.Unlock()
			}
		}()
	}

	// Wait for workers
	wg.Wait()

	// Check for errors
	select {
	case err := <-errChan:
		return err
	default:
	}

	// Delete resume state on success
	state.DeleteDownloadState(localPath)

	// Ensure directory exists for final file
	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Report 100% at end
	if progressCallback != nil {
		progressCallback(1.0)
	}

	return nil
}
