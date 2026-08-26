// Package azure provides an Azure implementation of the CloudTransfer interface.
// This file implements the LegacyDownloader interface for legacy (v0) format downloads.
//
// Uses AzureClient directly for all operations.
// Note: StreamingConcurrentDownloader (DetectFormat, DownloadStreaming) is in
// streaming_concurrent.go.
package azure

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	internaltransfer "github.com/rescale/rescale-int/internal/transfer"
)

// Verify that Provider implements LegacyDownloader
var _ transfer.LegacyDownloader = (*Provider)(nil)

// DownloadEncryptedFile downloads an encrypted file (legacy v0 format) to a local path.
// This implements the LegacyDownloader interface.
// The orchestrator handles decryption; this method just downloads the encrypted bytes.
// Uses AzureClient with file-aware credential handling.
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

	azureClient.StartPeriodicRefresh(ctx)
	defer azureClient.StopPeriodicRefresh()

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
// Uses AzureClient directly.
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

	return transfer.WriteBodyWithProgress(resp.Body, localPath, progressCallback, totalSize)
}

// downloadChunkedWithProgress downloads a blob in chunks with progress callback.
// Uses AzureClient directly.
// Wraps request+read+close in single retry to handle mid-transfer proxy failures.
func (p *Provider) downloadChunkedWithProgress(ctx context.Context, azureClient *AzureClient, remotePath, localPath string, totalSize int64, progressCallback func(float64)) error {
	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	// DownloadRangeOnce is the non-retrying variant, so the shared helper's
	// per-chunk retry is the only retry.
	return transfer.DownloadChunkedToFile(ctx, azureClient.RetryWithBackoff, localPath, totalSize, progressCallback,
		func(attemptCtx context.Context, offset, length int64) (io.ReadCloser, error) {
			resp, err := azureClient.DownloadRangeOnce(attemptCtx, remotePath, offset, length)
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		})
}

// downloadChunkedConcurrent downloads a blob using concurrent range requests.
// Full concurrent download implementation directly in provider, using AzureClient.
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
	// Track whether we successfully closed the file to avoid double-close from defer
	fileClosed := false
	defer func() {
		if !fileClosed {
			file.Close()
		}
	}()

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

				// Wrap request+read+close in single retry to handle mid-transfer proxy failures.
				// Uses DownloadRangeOnce, the non-retrying variant, so this loop is the only retry.
				var chunkData []byte
				err := azureClient.RetryWithBackoff(ctx, fmt.Sprintf("DownloadChunk %d", chunkIdx), func() error {
					// Per-attempt timeout to prevent stalled reads from hanging
					attemptCtx, cancel := context.WithTimeout(ctx, constants.PartOperationTimeout)
					defer cancel()

					resp, err := azureClient.DownloadRangeOnce(attemptCtx, remotePath, startByte, rangeSize)
					if err != nil {
						return err
					}
					data, readErr := io.ReadAll(resp.Body)
					resp.Body.Close() // Always close, even on read error
					if readErr != nil {
						return readErr
					}
					chunkData = data
					return nil
				})
				if err != nil {
					errChan <- fmt.Errorf("failed to download chunk %d: %w", chunkIdx, err)
					return
				}

				// Write to file at correct offset (OUTSIDE retry - disk errors are not retryable)
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
	close(errChan)

	errs := internaltransfer.CollectErrors(errChan)
	if len(errs) > 0 {
		return errs[0]
	}

	// Delete resume state on success
	state.DeleteDownloadState(localPath)

	// Sync file to disk before returning to ensure all data is written
	// before checksum verification. Without this, sporadic checksum failures
	// occur because the OS buffer may not be flushed yet.
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file to disk: %w", err)
	}

	// Explicit Close() before returning so the file handle is released
	// before the caller reads the file for checksum verification.
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close file: %w", err)
	}
	fileClosed = true

	// Report 100% at end
	if progressCallback != nil {
		progressCallback(1.0)
	}

	return nil
}
