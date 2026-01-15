// Package s3 provides an S3 implementation of the CloudTransfer interface.
// This file implements the LegacyDownloader interface for legacy (v0) format downloads.
//
// Uses S3Client directly for all operations.
// Note: StreamingConcurrentDownloader (DetectFormat, DownloadStreaming) is in
// streaming_concurrent.go.
package s3

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
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
// Uses S3Client with file-specific credentials for cross-storage downloads.
func (p *Provider) DownloadEncryptedFile(ctx context.Context, params transfer.LegacyDownloadParams) error {
	// Get or create S3 client with file-specific credentials (for cross-storage downloads)
	s3Client, err := p.getOrCreateS3ClientForFile(ctx, params.FileInfo)
	if err != nil {
		return fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Ensure fresh credentials
	if err := s3Client.EnsureFreshCredentials(ctx); err != nil {
		return fmt.Errorf("failed to refresh credentials: %w", err)
	}

	// Get file size from params, or fetch from S3 if not provided
	fileSize := params.FileSize
	if fileSize == 0 {
		// Fetch file size from S3 metadata
		headResp, err := s3Client.HeadObject(ctx, params.RemotePath)
		if err != nil {
			return fmt.Errorf("failed to get object metadata: %w", err)
		}
		fileSize = *headResp.ContentLength
	}

	// Choose download method based on file size and transfer handle
	if fileSize > constants.MultipartThreshold {
		// Use chunked download for large files
		if params.TransferHandle != nil && params.TransferHandle.GetThreads() > 1 {
			// Concurrent chunked download
			return p.downloadChunkedConcurrent(
				ctx,
				s3Client,
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
			s3Client,
			params.RemotePath,
			params.EncryptedPath,
			fileSize,
			params.ProgressCallback,
		)
	}

	// Small file: single download
	return p.downloadSingleWithProgress(
		ctx,
		s3Client,
		params.RemotePath,
		params.EncryptedPath,
		params.ProgressCallback,
		fileSize,
	)
}

// downloadSingleWithProgress downloads a file in a single GET request with progress callback.
// Uses S3Client directly.
func (p *Provider) downloadSingleWithProgress(ctx context.Context, s3Client *S3Client, objectKey, localPath string, progressCallback func(float64), totalSize int64) error {
	// Get object
	resp, err := s3Client.GetObject(ctx, objectKey)
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

	// If no progress callback or no total size, just copy directly
	if progressCallback == nil || totalSize <= 0 {
		_, err = io.Copy(file, resp.Body)
		if err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}
		return nil
	}

	// Copy with progress tracking
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
			if progress-lastProgress >= 0.01 || progress >= 1.0 {
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

	progressCallback(1.0)

	// v4.3.7: Sync file to disk before returning to ensure all data is written
	// before checksum verification. Fixes sporadic checksum failures where file
	// was read as empty due to OS buffer not being flushed.
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file to disk: %w", err)
	}

	return nil
}

// downloadChunkedWithProgress downloads a file in chunks with progress callback.
// Uses S3Client directly.
func (p *Provider) downloadChunkedWithProgress(ctx context.Context, s3Client *S3Client, objectKey, localPath string, totalSize int64, progressCallback func(float64)) error {
	// Create output file
	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	chunkSize := int64(constants.ChunkSize)
	var offset int64 = 0

	for offset < totalSize {
		// Calculate chunk size for this iteration
		currentChunkSize := chunkSize
		if offset+currentChunkSize > totalSize {
			currentChunkSize = totalSize - offset
		}

		// Download this chunk using Range header
		resp, err := s3Client.GetObjectRange(ctx, objectKey, offset, offset+currentChunkSize-1)
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

	// v4.3.7: Sync file to disk before returning to ensure all data is written
	// before checksum verification. Fixes sporadic checksum failures where file
	// was read as empty due to OS buffer not being flushed.
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file to disk: %w", err)
	}

	return nil
}

// downloadChunkedConcurrent downloads a file using concurrent range requests.
// Uses S3Client directly.
func (p *Provider) downloadChunkedConcurrent(
	ctx context.Context,
	s3Client *S3Client,
	objectKey, localPath string,
	totalSize int64,
	progressCallback func(float64),
	transferHandle *internaltransfer.Transfer,
) error {
	// If no transfer handle provided or only 1 thread, fall back to sequential
	if transferHandle == nil || transferHandle.GetThreads() <= 1 {
		return p.downloadChunkedWithProgress(ctx, s3Client, objectKey, localPath, totalSize, progressCallback)
	}

	concurrency := transferHandle.GetThreads()
	defer transferHandle.Complete()

	// Calculate total number of chunks
	chunkSize := int64(constants.ChunkSize)
	totalChunks := (totalSize + chunkSize - 1) / chunkSize

	// Check for existing resume state
	existingState, _ := state.LoadDownloadState(localPath)
	var resumeState *state.DownloadResumeState
	var chunksToDownload []int64

	if existingState != nil && existingState.ChunkSize == chunkSize {
		if err := state.ValidateDownloadState(existingState, localPath); err == nil {
			resumeState = existingState
			chunksToDownload = resumeState.GetMissingChunks(totalChunks)
			fmt.Printf("Resuming concurrent download: %d/%d chunks already completed\n",
				len(resumeState.CompletedChunks), totalChunks)
		} else {
			state.CleanupExpiredDownloadResume(existingState, localPath, true)
			chunksToDownload = makeChunkList(totalChunks)
		}
	} else {
		chunksToDownload = makeChunkList(totalChunks)
	}

	// Initialize or create resume state
	if resumeState == nil {
		resumeState = &state.DownloadResumeState{
			LocalPath:       localPath,
			EncryptedPath:   localPath,
			RemotePath:      objectKey,
			TotalSize:       totalSize,
			DownloadedBytes: 0,
			CreatedAt:       time.Now(),
			LastUpdate:      time.Now(),
			StorageType:     "S3Storage",
			ChunkSize:       chunkSize,
			CompletedChunks: make([]int64, 0),
		}
	}

	// If no chunks to download, verify file is complete
	if len(chunksToDownload) == 0 {
		fileInfo, err := os.Stat(localPath)
		if err != nil {
			return fmt.Errorf("failed to stat file: %w", err)
		}

		if fileInfo.Size() != totalSize {
			fmt.Printf("Warning: Resume state claims complete but file size mismatch\n")
			state.DeleteDownloadState(localPath)
			chunksToDownload = makeChunkList(totalChunks)
			resumeState.DownloadedBytes = 0
			resumeState.CompletedChunks = make([]int64, 0)
			resumeState.LastUpdate = time.Now()
		} else {
			fmt.Printf("Download already complete (verified), skipping\n")
			if progressCallback != nil {
				progressCallback(1.0)
			}
			return nil
		}
	}

	// Create or open output file
	var file *os.File
	var err error
	if len(resumeState.CompletedChunks) > 0 {
		file, err = os.OpenFile(localPath, os.O_RDWR, 0644)
	} else {
		file, err = os.Create(localPath)
	}
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Concurrent download implementation
	type chunkJob struct {
		chunkIndex int64
		offset     int64
		size       int64
	}

	type chunkResult struct {
		chunkIndex int64
		offset     int64
		data       []byte
		err        error
	}

	jobChan := make(chan chunkJob, concurrency*2)
	resultChan := make(chan chunkResult, totalChunks)

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
					log.Printf("PANIC in download worker %d: %v", workerID, r)
				}
			}()

			for job := range jobChan {
				select {
				case <-opCtx.Done():
					return
				default:
				}

				// Download this chunk
				startTime := time.Now()
				resp, downloadErr := s3Client.GetObjectRange(opCtx, objectKey, job.offset, job.offset+job.size-1)

				if downloadErr != nil {
					setError(fmt.Errorf("failed to download chunk %d at offset %d: %w", job.chunkIndex, job.offset, downloadErr))
					return
				}

				chunkData, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()

				if readErr != nil {
					setError(fmt.Errorf("failed to read chunk %d at offset %d: %w", job.chunkIndex, job.offset, readErr))
					return
				}

				// Record throughput
				duration := time.Since(startTime).Seconds()
				if duration > 0 {
					bytesPerSec := float64(len(chunkData)) / duration
					transferHandle.RecordThroughput(bytesPerSec)
				}

				resultChan <- chunkResult{
					chunkIndex: job.chunkIndex,
					offset:     job.offset,
					data:       chunkData,
					err:        nil,
				}
			}
		}(i)
	}

	// Queue chunks for download
	go func() {
		defer close(jobChan)

		for _, chunkIndex := range chunksToDownload {
			select {
			case <-opCtx.Done():
				return
			default:
			}

			offset := chunkIndex * chunkSize
			currentChunkSize := chunkSize
			if offset+currentChunkSize > totalSize {
				currentChunkSize = totalSize - offset
			}

			jobChan <- chunkJob{
				chunkIndex: chunkIndex,
				offset:     offset,
				size:       currentChunkSize,
			}
		}
	}()

	// Collect results
	results := make([]chunkResult, 0, len(chunksToDownload))
	var atomicDownloadedBytes int64 = resumeState.DownloadedBytes
	var resumeStateMu sync.Mutex

	// Progress ticker
	progressTicker := time.NewTicker(300 * time.Millisecond)
	defer progressTicker.Stop()

	progressDone := make(chan struct{})
	defer close(progressDone)

	go func() {
		for {
			select {
			case <-progressTicker.C:
				if progressCallback != nil && totalSize > 0 {
					currentBytes := atomic.LoadInt64(&atomicDownloadedBytes)
					if currentBytes > 0 {
						progressCallback(float64(currentBytes) / float64(totalSize))
					}
				}
			case <-progressDone:
				return
			}
		}
	}()

	// Wait for workers to finish
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

		results = append(results, result)
		atomic.AddInt64(&atomicDownloadedBytes, int64(len(result.data)))

		// Update resume state
		resumeStateMu.Lock()
		resumeState.MarkChunkCompleted(result.chunkIndex, int64(len(result.data)))
		state.SaveDownloadState(resumeState, localPath)
		resumeStateMu.Unlock()
	}

	// Check for errors
	errorMu.Lock()
	if firstError != nil {
		err := firstError
		errorMu.Unlock()
		resumeStateMu.Lock()
		state.SaveDownloadState(resumeState, localPath)
		resumeStateMu.Unlock()
		return err
	}
	errorMu.Unlock()

	// Save final state
	resumeStateMu.Lock()
	state.SaveDownloadState(resumeState, localPath)
	resumeStateMu.Unlock()

	// Sort results by chunk index and write to file
	sort.Slice(results, func(i, j int) bool {
		return results[i].chunkIndex < results[j].chunkIndex
	})

	for _, result := range results {
		if _, err := file.Seek(result.offset, 0); err != nil {
			return fmt.Errorf("failed to seek to offset %d: %w", result.offset, err)
		}
		if _, err := file.Write(result.data); err != nil {
			return fmt.Errorf("failed to write chunk at offset %d: %w", result.offset, err)
		}
	}

	// v4.3.7: Sync file to disk before returning to ensure all data is written
	// before checksum verification. Fixes sporadic checksum failures where file
	// was read as empty due to OS buffer not being flushed.
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file to disk: %w", err)
	}

	// Download complete - clean up resume state
	state.DeleteDownloadState(localPath)

	return nil
}

// makeChunkList creates a list of chunk indices from 0 to totalChunks-1
func makeChunkList(totalChunks int64) []int64 {
	chunks := make([]int64, totalChunks)
	for i := range chunks {
		chunks[i] = int64(i)
	}
	return chunks
}
