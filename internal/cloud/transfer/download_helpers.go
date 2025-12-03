// Package transfer provides unified upload and download orchestration.
// This file provides shared download helper functions used by both S3 and Azure providers.
// Extracting common logic reduces code duplication (~350 lines) between providers.
//
// Version: 3.2.0
// Date: 2025-12-01
package transfer

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/transfer"
)

// ChunkDownloader is the interface that providers implement to download byte ranges.
// This abstracts the provider-specific HTTP/SDK calls.
type ChunkDownloader interface {
	// DownloadRange downloads bytes from [start, end] inclusive.
	// Returns an io.ReadCloser that must be closed by the caller.
	DownloadRange(ctx context.Context, remotePath string, start, end int64) (io.ReadCloser, error)
}

// ChunkedDownloadParams contains parameters for chunked downloads.
type ChunkedDownloadParams struct {
	// Required fields
	RemotePath       string
	LocalPath        string
	TotalSize        int64
	StorageType      string // "S3Storage" or "AzureStorage"
	ChunkDownloader  ChunkDownloader
	TransferHandle   *transfer.Transfer
	ProgressCallback cloud.ProgressCallback

	// Optional fields
	OutputWriter io.Writer // For status messages
}

// DownloadChunkedSequential downloads a file in sequential chunks with progress.
// This is the shared implementation used by both S3 and Azure providers.
func DownloadChunkedSequential(ctx context.Context, params ChunkedDownloadParams) error {
	// Create output file
	file, err := os.Create(params.LocalPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	chunkSize := int64(constants.ChunkSize)
	var offset int64 = 0

	for offset < params.TotalSize {
		// Calculate chunk bounds for this iteration
		endOffset := offset + chunkSize - 1
		if endOffset >= params.TotalSize {
			endOffset = params.TotalSize - 1
		}

		// Download this chunk
		reader, err := params.ChunkDownloader.DownloadRange(ctx, params.RemotePath, offset, endOffset)
		if err != nil {
			return fmt.Errorf("failed to download chunk at offset %d: %w", offset, err)
		}

		// Read chunk data
		chunkData, err := io.ReadAll(reader)
		reader.Close()

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
		if params.ProgressCallback != nil && params.TotalSize > 0 {
			params.ProgressCallback(float64(offset) / float64(params.TotalSize))
		}
	}

	return nil
}

// DownloadChunkedConcurrent downloads a file using concurrent workers.
// This is the shared implementation used by both S3 and Azure providers.
func DownloadChunkedConcurrent(ctx context.Context, params ChunkedDownloadParams) error {
	// If no transfer handle or only 1 thread, fall back to sequential
	if params.TransferHandle == nil || params.TransferHandle.GetThreads() <= 1 {
		return DownloadChunkedSequential(ctx, params)
	}

	concurrency := params.TransferHandle.GetThreads()
	defer params.TransferHandle.Complete()

	// Calculate total number of chunks
	chunkSize := int64(constants.ChunkSize)
	totalChunks := (params.TotalSize + chunkSize - 1) / chunkSize

	// Check for existing resume state
	existingState, _ := state.LoadDownloadState(params.LocalPath)
	var resumeState *state.DownloadResumeState
	var chunksToDownload []int64

	if existingState != nil && existingState.ChunkSize == chunkSize {
		if err := state.ValidateDownloadState(existingState, params.LocalPath); err == nil {
			resumeState = existingState
			chunksToDownload = resumeState.GetMissingChunks(totalChunks)
			if params.OutputWriter != nil {
				fmt.Fprintf(params.OutputWriter, "Resuming download: %d/%d chunks already completed\n",
					len(resumeState.CompletedChunks), totalChunks)
			}
		} else {
			state.CleanupExpiredDownloadResume(existingState, params.LocalPath, true)
			chunksToDownload = makeChunkList(totalChunks)
		}
	} else {
		chunksToDownload = makeChunkList(totalChunks)
	}

	// Initialize or create resume state
	if resumeState == nil {
		resumeState = &state.DownloadResumeState{
			LocalPath:       params.LocalPath,
			EncryptedPath:   params.LocalPath,
			RemotePath:      params.RemotePath,
			TotalSize:       params.TotalSize,
			DownloadedBytes: 0,
			CreatedAt:       time.Now(),
			LastUpdate:      time.Now(),
			StorageType:     params.StorageType,
			ChunkSize:       chunkSize,
			CompletedChunks: make([]int64, 0),
		}
	}

	// If no chunks to download, verify file is complete
	if len(chunksToDownload) == 0 {
		fileInfo, err := os.Stat(params.LocalPath)
		if err != nil {
			return fmt.Errorf("failed to stat file: %w", err)
		}

		if fileInfo.Size() != params.TotalSize {
			if params.OutputWriter != nil {
				fmt.Fprintf(params.OutputWriter, "Warning: Resume state claims complete but file size mismatch\n")
			}
			state.DeleteDownloadState(params.LocalPath)
			chunksToDownload = makeChunkList(totalChunks)
			resumeState.DownloadedBytes = 0
			resumeState.CompletedChunks = make([]int64, 0)
			resumeState.LastUpdate = time.Now()
		} else {
			if params.OutputWriter != nil {
				fmt.Fprintf(params.OutputWriter, "Download already complete (verified), skipping\n")
			}
			if params.ProgressCallback != nil {
				params.ProgressCallback(1.0)
			}
			return nil
		}
	}

	// Create or open output file
	var file *os.File
	var err error
	if len(resumeState.CompletedChunks) > 0 {
		file, err = os.OpenFile(params.LocalPath, os.O_RDWR, 0644)
	} else {
		file, err = os.Create(params.LocalPath)
	}
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Pre-allocate file for concurrent writes
	if err := file.Truncate(params.TotalSize); err != nil {
		return fmt.Errorf("failed to truncate file: %w", err)
	}

	// Concurrent download types
	type chunkJob struct {
		chunkIndex int64
		offset     int64
		size       int64
	}

	type chunkResult struct {
		chunkIndex int64
		size       int64
		err        error
	}

	jobChan := make(chan chunkJob, concurrency*2)
	resultChan := make(chan chunkResult, len(chunksToDownload))

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

			for job := range jobChan {
				select {
				case <-opCtx.Done():
					return
				default:
				}

				// Download this chunk
				startTime := time.Now()
				reader, downloadErr := params.ChunkDownloader.DownloadRange(opCtx, params.RemotePath, job.offset, job.offset+job.size-1)

				if downloadErr != nil {
					setError(fmt.Errorf("failed to download chunk %d at offset %d: %w", job.chunkIndex, job.offset, downloadErr))
					resultChan <- chunkResult{chunkIndex: job.chunkIndex, err: downloadErr}
					return
				}

				chunkData, readErr := io.ReadAll(reader)
				reader.Close()

				if readErr != nil {
					setError(fmt.Errorf("failed to read chunk %d at offset %d: %w", job.chunkIndex, job.offset, readErr))
					resultChan <- chunkResult{chunkIndex: job.chunkIndex, err: readErr}
					return
				}

				// Write to file at correct offset
				_, writeErr := file.WriteAt(chunkData, job.offset)
				if writeErr != nil {
					setError(fmt.Errorf("failed to write chunk %d at offset %d: %w", job.chunkIndex, job.offset, writeErr))
					resultChan <- chunkResult{chunkIndex: job.chunkIndex, err: writeErr}
					return
				}

				// Record throughput
				duration := time.Since(startTime).Seconds()
				if duration > 0 {
					bytesPerSec := float64(len(chunkData)) / duration
					params.TransferHandle.RecordThroughput(bytesPerSec)
				}

				resultChan <- chunkResult{
					chunkIndex: job.chunkIndex,
					size:       int64(len(chunkData)),
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
			if offset+currentChunkSize > params.TotalSize {
				currentChunkSize = params.TotalSize - offset
			}

			jobChan <- chunkJob{
				chunkIndex: chunkIndex,
				offset:     offset,
				size:       currentChunkSize,
			}
		}
	}()

	// Progress tracking
	var atomicDownloadedBytes int64 = resumeState.DownloadedBytes
	var resumeStateMu sync.Mutex

	progressTicker := time.NewTicker(300 * time.Millisecond)
	defer progressTicker.Stop()

	progressDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-progressTicker.C:
				if params.ProgressCallback != nil && params.TotalSize > 0 {
					currentBytes := atomic.LoadInt64(&atomicDownloadedBytes)
					if currentBytes > 0 {
						params.ProgressCallback(float64(currentBytes) / float64(params.TotalSize))
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
			continue // Error already set via setError
		}

		atomic.AddInt64(&atomicDownloadedBytes, result.size)

		// Update resume state
		resumeStateMu.Lock()
		resumeState.MarkChunkCompleted(result.chunkIndex, result.size)
		state.SaveDownloadState(resumeState, params.LocalPath)
		resumeStateMu.Unlock()
	}

	close(progressDone)

	// Check for errors
	errorMu.Lock()
	if firstError != nil {
		err := firstError
		errorMu.Unlock()
		resumeStateMu.Lock()
		state.SaveDownloadState(resumeState, params.LocalPath)
		resumeStateMu.Unlock()
		return err
	}
	errorMu.Unlock()

	// Download complete - clean up resume state
	state.DeleteDownloadState(params.LocalPath)

	// Report 100% progress
	if params.ProgressCallback != nil {
		params.ProgressCallback(1.0)
	}

	return nil
}

// DownloadSingleWithProgress downloads a file in a single request with progress tracking.
// This is the shared implementation used by both S3 and Azure providers.
func DownloadSingleWithProgress(ctx context.Context, params ChunkedDownloadParams) error {
	// Report 0% at start
	if params.ProgressCallback != nil {
		params.ProgressCallback(0.0)
	}

	// Download entire file
	reader, err := params.ChunkDownloader.DownloadRange(ctx, params.RemotePath, 0, params.TotalSize-1)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer reader.Close()

	// Create output file
	file, err := os.Create(params.LocalPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// If no progress callback or no total size, just copy directly
	if params.ProgressCallback == nil || params.TotalSize <= 0 {
		_, err = io.Copy(file, reader)
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
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				return fmt.Errorf("failed to write file: %w", writeErr)
			}
			downloaded += int64(n)

			// Update progress (throttle to avoid excessive updates)
			progress := float64(downloaded) / float64(params.TotalSize)
			if progress-lastProgress >= 0.01 || progress >= 1.0 {
				params.ProgressCallback(progress)
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
	params.ProgressCallback(1.0)
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
