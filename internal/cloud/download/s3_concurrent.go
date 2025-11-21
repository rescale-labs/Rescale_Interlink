package download

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/rescale/rescale-int/internal/cloud/storage"
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/transfer"
	"path/filepath"
)

// downloadChunkedConcurrent downloads a file from S3 using concurrent range requests
// This is a drop-in replacement for downloadChunked that supports parallel chunk downloads
// transferHandle: resource allocation handle (nil = use sequential download)
func (d *S3Downloader) downloadChunkedConcurrent(
	ctx context.Context,
	objectKey, localPath string,
	totalSize int64,
	progressCallback func(float64),
	transferHandle *transfer.Transfer,
) error {
	// If no transfer handle provided or only 1 thread, fall back to sequential
	if transferHandle == nil || transferHandle.GetThreads() <= 1 {
		return d.downloadChunked(ctx, objectKey, localPath, totalSize)
	}

	concurrency := transferHandle.GetThreads()

	// Ensure cleanup on completion
	defer transferHandle.Complete()

	// Calculate total number of chunks
	chunkSize := int64(RangeChunkSize)
	totalChunks := (totalSize + chunkSize - 1) / chunkSize

	// Check for existing resume state
	existingState, _ := LoadDownloadState(localPath)
	var resumeState *DownloadResumeState
	var chunksToDownload []int64

	if existingState != nil && existingState.ChunkSize == chunkSize {
		// Validate the resume state
		if err := ValidateDownloadState(existingState, localPath); err == nil {
			resumeState = existingState
			chunksToDownload = resumeState.GetMissingChunks(totalChunks)
			fmt.Printf("Resuming concurrent download: %d/%d chunks already completed\n",
				len(resumeState.CompletedChunks), totalChunks)
		} else {
			// Cleanup invalid state
			CleanupExpiredResume(existingState, localPath, true)
			chunksToDownload = make([]int64, totalChunks)
			for i := range chunksToDownload {
				chunksToDownload[i] = int64(i)
			}
		}
	} else {
		// No valid resume state - download all chunks
		chunksToDownload = make([]int64, totalChunks)
		for i := range chunksToDownload {
			chunksToDownload[i] = int64(i)
		}
	}

	// Initialize or create resume state
	if resumeState == nil {
		resumeState = &DownloadResumeState{
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

	// CRITICAL: If no chunks to download, verify file is actually complete BEFORE opening it
	if len(chunksToDownload) == 0 {
		// Verify the encrypted file actually has the expected size
		fileInfo, err := os.Stat(localPath)
		if err != nil {
			return fmt.Errorf("failed to stat file: %w", err)
		}

		if fileInfo.Size() != totalSize {
			// File size mismatch! Resume state is corrupt.
			fmt.Printf("Warning: Resume state claims download complete but file size mismatch\n")
			fmt.Printf("  Expected: %d bytes, Got: %d bytes (%.1f%% complete)\n",
				totalSize, fileInfo.Size(), float64(fileInfo.Size())/float64(totalSize)*100)
			fmt.Printf("  Resetting resume state and re-downloading missing data...\n")

			// Delete corrupt resume state
			DeleteDownloadState(localPath)

			// Recalculate missing chunks based on actual file content
			// For now, we'll re-download everything to be safe
			// TODO: Could be smarter and verify each chunk
			chunksToDownload = make([]int64, totalChunks)
			for i := range chunksToDownload {
				chunksToDownload[i] = int64(i)
			}

			// Reset resume state
			resumeState.DownloadedBytes = 0
			resumeState.CompletedChunks = make([]int64, 0)
			resumeState.LastUpdate = time.Now()
		} else {
			// File size matches - download is actually complete
			fmt.Printf("Download already complete (verified), skipping\n")

			// CRITICAL: Call progress callback to signal 100% complete
			// This ensures progress bars are properly created and completed
			if progressCallback != nil {
				progressCallback(1.0) // 100% complete
			}

			return nil
		}
	}

	// Create or open output file for writing
	var file *os.File
	var err error
	if len(resumeState.CompletedChunks) > 0 {
		// Resume: open existing file for writing
		file, err = os.OpenFile(localPath, os.O_RDWR, 0644)
	} else {
		// New download: create file
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

	// Channels for coordination
	jobChan := make(chan chunkJob, concurrency*2)
	resultChan := make(chan chunkResult, totalChunks)
	errorChan := make(chan error, 1)

	// Start worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for job := range jobChan {
				// Check if context was cancelled (Ctrl+C) or another worker encountered an error
				select {
				case <-ctx.Done():
					// Context cancelled by user or timeout
					select {
					case errorChan <- ctx.Err():
					default:
					}
					return
				case <-errorChan:
					return
				default:
				}

				// Download this chunk using Range header (with retry)
				startTime := time.Now()
				rangeHeader := fmt.Sprintf("bytes=%d-%d", job.offset, job.offset+job.size-1)

				var resp *s3.GetObjectOutput
				downloadErr := d.retryWithBackoff(ctx, fmt.Sprintf("GetObject range %d-%d (worker %d)", job.offset, job.offset+job.size-1, workerID), func() error {
					r, err := d.client.GetObject(ctx, &s3.GetObjectInput{
						Bucket: aws.String(d.storageInfo.ConnectionSettings.Container),
						Key:    aws.String(objectKey),
						Range:  aws.String(rangeHeader),
					})
					resp = r
					return err
				})

				if downloadErr != nil {
					select {
					case errorChan <- fmt.Errorf("failed to download chunk %d at offset %d: %w", job.chunkIndex, job.offset, downloadErr):
					default:
					}
					return
				}

				// Read chunk data
				chunkData, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()

				if readErr != nil {
					select {
					case errorChan <- fmt.Errorf("failed to read chunk %d at offset %d: %w", job.chunkIndex, job.offset, readErr):
					default:
					}
					return
				}

				// Calculate and record throughput
				duration := time.Since(startTime).Seconds()
				if duration > 0 {
					bytesPerSec := float64(len(chunkData)) / duration
					transferHandle.RecordThroughput(bytesPerSec)
				}

				// Send result
				resultChan <- chunkResult{
					chunkIndex: job.chunkIndex,
					offset:     job.offset,
					data:       chunkData,
					err:        nil,
				}
			}
		}(i)
	}

	// Queue only missing chunks for download
	go func() {
		defer close(jobChan)

		for _, chunkIndex := range chunksToDownload {
			// Check if an error occurred
			select {
			case <-errorChan:
				return
			default:
			}

			// Calculate offset and size for this chunk
			offset := chunkIndex * chunkSize
			currentChunkSize := chunkSize
			if offset+currentChunkSize > totalSize {
				currentChunkSize = totalSize - offset
			}

			// Queue this chunk
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

	// Mutex for thread-safe resume state updates
	var resumeStateMu sync.Mutex

	// Start a ticker for smoother progress updates (every 300ms, ~3x per second)
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
						progress := float64(currentBytes) / float64(totalSize)
						progressCallback(progress)
					}
				}
			case <-progressDone:
				return
			}
		}
	}()

	// Wait for results in a separate goroutine
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect all results
	chunksCompleted := 0
	for result := range resultChan {
		if result.err != nil {
			select {
			case errorChan <- result.err:
			default:
			}
			break
		}

		results = append(results, result)
		chunksCompleted++

		// Update progress atomically (ticker will pick this up)
		atomic.AddInt64(&atomicDownloadedBytes, int64(len(result.data)))

		// Update resume state (thread-safe)
		resumeStateMu.Lock()
		resumeState.MarkChunkCompleted(result.chunkIndex, int64(len(result.data)))

		// Save state frequently to handle abrupt exits (Ctrl+C, crashes)
		// Save on every chunk completion to ensure minimal data loss
		// (16 MB chunks @ ~40 MB/s = ~400ms per chunk, so this is reasonable)
		SaveDownloadState(resumeState, localPath)
		resumeStateMu.Unlock()
	}

	// Check for errors
	select {
	case err := <-errorChan:
		// Save final state before returning error
		resumeStateMu.Lock()
		SaveDownloadState(resumeState, localPath)
		resumeStateMu.Unlock()
		return err
	default:
	}

	// Save final state before writing to disk
	resumeStateMu.Lock()
	SaveDownloadState(resumeState, localPath)
	resumeStateMu.Unlock()

	// Sort results by chunk index to write in order
	sort.Slice(results, func(i, j int) bool {
		return results[i].chunkIndex < results[j].chunkIndex
	})

	// Write all chunks to file in order
	for _, result := range results {
		// Seek to the correct position
		if _, err := file.Seek(result.offset, 0); err != nil {
			return fmt.Errorf("failed to seek to offset %d: %w", result.offset, err)
		}

		// Write chunk data
		if _, err := file.Write(result.data); err != nil {
			return fmt.Errorf("failed to write chunk at offset %d: %w", result.offset, err)
		}
	}

	// Download complete - clean up resume state
	DeleteDownloadState(localPath)

	return nil
}

// DownloadAndDecryptWithTransfer downloads and decrypts a file using a transfer handle for concurrent downloads
// If transferHandle is nil, uses sequential download (same as DownloadAndDecrypt)
// If transferHandle specifies multiple threads, uses concurrent chunk downloads
func (d *S3Downloader) DownloadAndDecryptWithTransfer(
	ctx context.Context,
	objectKey, localPath string,
	encryptionKey, iv []byte,
	progressCallback func(float64),
	transferHandle *transfer.Transfer,
) error {
	// Get object metadata to check size and retrieve IV if not provided (with retry)
	var headResp *s3.HeadObjectOutput
	err := d.retryWithBackoff(ctx, "HeadObject", func() error {
		resp, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(d.storageInfo.ConnectionSettings.Container),
			Key:    aws.String(objectKey),
		})
		headResp = resp
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to get object metadata: %w", err)
	}

	// If IV not provided, try to get it from S3 metadata
	if iv == nil {
		if ivStr, ok := headResp.Metadata["iv"]; ok {
			iv, err = encryption.DecodeBase64(ivStr)
			if err != nil {
				return fmt.Errorf("failed to decode IV from metadata: %w", err)
			}
		} else {
			return fmt.Errorf("IV not provided and not found in S3 metadata")
		}
	}

	fileSize := *headResp.ContentLength

	// CHECK DISK SPACE before download (with 5% safety buffer)
	if err := diskspace.CheckAvailableSpace(localPath, fileSize, 1.05); err != nil {
		return &diskspace.InsufficientSpaceError{
			Path:           localPath,
			RequiredBytes:  fileSize,
			AvailableBytes: diskspace.GetAvailableSpace(filepath.Dir(localPath)),
		}
	}

	// Create temp file in same directory as target (not in /tmp)
	targetDir := filepath.Dir(localPath)
	encryptedPath := localPath + ".encrypted"

	// Choose download method based on file size and transfer handle
	if fileSize > DownloadThreshold {
		// Use chunked download for large files
		// If transfer handle provided and has multiple threads, use concurrent download
		if transferHandle != nil && transferHandle.GetThreads() > 1 {
			err = d.downloadChunkedConcurrent(ctx, objectKey, encryptedPath, fileSize, progressCallback, transferHandle)
		} else {
			err = d.downloadChunked(ctx, objectKey, encryptedPath, fileSize)
		}
	} else {
		// Use single request for small files
		err = d.downloadSingle(ctx, objectKey, encryptedPath)
	}

	if err != nil {
		// Convert disk full errors to standard type
		if storage.IsDiskFullError(err) {
			return &diskspace.InsufficientSpaceError{
				Path:           encryptedPath,
				RequiredBytes:  fileSize,
				AvailableBytes: diskspace.GetAvailableSpace(targetDir),
			}
		}
		// DON'T delete .encrypted file on error - needed for resume
		return fmt.Errorf("download failed: %w", err)
	}

	// Check disk space for decryption (need space for BOTH encrypted + decrypted files)
	// encrypted file already exists, need additional space for decrypted output
	if err := diskspace.CheckAvailableSpace(localPath, fileSize, 1.05); err != nil {
		return &diskspace.InsufficientSpaceError{
			Path:           localPath,
			RequiredBytes:  fileSize * 2, // Both files during decryption
			AvailableBytes: diskspace.GetAvailableSpace(targetDir),
		}
	}

	// Decrypt the file
	fmt.Printf("Decrypting %s (this may take several minutes for large files)...\n", filepath.Base(localPath))
	if err := encryption.DecryptFile(encryptedPath, localPath, encryptionKey, iv); err != nil {
		// Check for disk full during decryption
		if storage.IsDiskFullError(err) {
			return &diskspace.InsufficientSpaceError{
				Path:           localPath,
				RequiredBytes:  fileSize,
				AvailableBytes: diskspace.GetAvailableSpace(targetDir),
			}
		}
		// DON'T delete .encrypted file on decryption error - needed for resume
		return fmt.Errorf("decryption failed: %w", err)
	}

	// Success - clean up temp files
	os.Remove(encryptedPath)
	DeleteDownloadState(encryptedPath)

	return nil
}
