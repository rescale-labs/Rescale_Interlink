// Package s3 provides an S3 implementation of the CloudTransfer interface.
// This file implements the PreEncryptUploader interface for pre-encrypted uploads.
//
// Phase 7F: Concurrent upload logic moved directly into provider, eliminating
// the last dependency on state.NewS3Uploader().
//
// Version: 3.2.0 (Sprint 7F - S3 Upload True Consolidation Complete)
// Date: 2025-11-29
package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto"        // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/util/buffers"
)

// Verify that Provider implements PreEncryptUploader
var _ transfer.PreEncryptUploader = (*Provider)(nil)

// UploadEncryptedFile uploads an already-encrypted file to S3.
// This implements the PreEncryptUploader interface.
// The encryption is already done by the orchestrator; this method handles the state.
// Phase 7E: Uses S3Client directly instead of wrapping state.S3Uploader.
func (p *Provider) UploadEncryptedFile(ctx context.Context, params transfer.EncryptedFileUploadParams) (*cloud.UploadResult, error) {
	// Get or create S3 client
	s3Client, err := p.getOrCreateS3Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get S3 client: %w", err)
	}

	// Build object key using the pre-generated random suffix
	filename := filepath.Base(params.LocalPath)
	objectKey := state.BuildObjectKey(s3Client.PathBase(), filename, params.RandomSuffix)

	// Get encrypted file info
	info, err := os.Stat(params.EncryptedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat encrypted file: %w", err)
	}
	encryptedSize := info.Size()

	// Choose upload method based on file size and transfer handle
	if encryptedSize > constants.MultipartThreshold {
		// Use multipart upload for large files
		if params.TransferHandle != nil && params.TransferHandle.GetThreads() > 1 {
			err = p.uploadEncryptedMultipartConcurrent(ctx, s3Client, params, objectKey, encryptedSize)
		} else {
			err = p.uploadEncryptedMultipart(ctx, s3Client, params, objectKey, encryptedSize)
		}
	} else {
		// Use single-part upload for small files
		err = p.uploadEncryptedSingle(ctx, s3Client, params.EncryptedPath, objectKey, params.IV, params.ProgressCallback)
	}

	if err != nil {
		return nil, fmt.Errorf("S3 upload failed: %w", err)
	}

	// Delete resume state after successful upload
	state.DeleteUploadState(params.LocalPath)

	return &cloud.UploadResult{
		StoragePath:   objectKey,
		EncryptionKey: params.EncryptionKey,
		IV:            params.IV,
		FormatVersion: 0, // Legacy pre-encrypt format
	}, nil
}

// uploadEncryptedSingle uploads an encrypted file in a single PUT request.
// Phase 7E: Uses S3Client directly.
func (p *Provider) uploadEncryptedSingle(ctx context.Context, s3Client *S3Client, filePath, objectKey string, iv []byte, progressCallback func(float64)) error {
	// Report 0% at start
	if progressCallback != nil {
		progressCallback(0.0)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	err = s3Client.RetryWithBackoff(ctx, "PutObject", func() error {
		// Need to seek back to beginning on retry
		if _, seekErr := file.Seek(0, 0); seekErr != nil {
			return fmt.Errorf("failed to seek file: %w", seekErr)
		}
		_, err := s3Client.Client().PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(s3Client.Bucket()),
			Key:           aws.String(objectKey),
			Body:          file,
			ContentLength: aws.Int64(info.Size()),
			Metadata: map[string]string{
				"iv": encryption.EncodeBase64(iv),
			},
		})
		return err
	})

	if err == nil && progressCallback != nil {
		progressCallback(1.0)
	}

	return err
}

// uploadEncryptedMultipart uploads an encrypted file using S3 multipart upload (sequential).
// Phase 7E: Uses S3Client directly.
func (p *Provider) uploadEncryptedMultipart(ctx context.Context, s3Client *S3Client, params transfer.EncryptedFileUploadParams, objectKey string, encryptedSize int64) error {
	file, err := os.Open(params.EncryptedPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	partSize := int64(constants.ChunkSize)
	totalParts := (encryptedSize + partSize - 1) / partSize

	// Try to load resume state
	existingState, _ := state.LoadUploadState(params.LocalPath)
	var uploadID string
	var completedParts []types.CompletedPart
	var uploadedBytes int64 = 0
	startPart := int32(1)
	resuming := false
	var createdAt time.Time

	if existingState != nil && existingState.UploadID != "" && existingState.ObjectKey == objectKey {
		// Resume existing upload
		uploadID = existingState.UploadID
		uploadedBytes = existingState.UploadedBytes
		completedParts = convertToCompletedParts(existingState.CompletedParts)
		startPart = int32(len(completedParts)) + 1
		resuming = true
		createdAt = existingState.CreatedAt

		if _, err := file.Seek(uploadedBytes, 0); err != nil {
			return fmt.Errorf("failed to seek in file: %w", err)
		}
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Resuming upload from part %d/%d\n", startPart, totalParts)
		}
	}

	// Create new multipart upload if not resuming
	if !resuming {
		var createResp *s3.CreateMultipartUploadOutput
		err = s3Client.RetryWithBackoff(ctx, "CreateMultipartUpload", func() error {
			var err error
			createResp, err = s3Client.Client().CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
				Bucket: aws.String(s3Client.Bucket()),
				Key:    aws.String(objectKey),
				Metadata: map[string]string{
					"iv": encryption.EncodeBase64(params.IV),
				},
			})
			return err
		})
		if err != nil {
			return fmt.Errorf("failed to create multipart upload: %w", err)
		}
		uploadID = *createResp.UploadId
		createdAt = time.Now()
	}

	// Report initial progress
	if params.ProgressCallback != nil {
		params.ProgressCallback(float64(uploadedBytes) / float64(encryptedSize))
	}

	// Upload parts
	buffer := make([]byte, partSize)
	for partNum := startPart; int64(partNum) <= totalParts; partNum++ {
		n, err := io.ReadFull(file, buffer)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("failed to read part %d: %w", partNum, err)
		}
		if n == 0 {
			break
		}

		// Make a copy for upload
		partData := make([]byte, n)
		copy(partData, buffer[:n])

		var uploadResp *s3.UploadPartOutput
		err = s3Client.RetryWithBackoff(ctx, fmt.Sprintf("UploadPart %d", partNum), func() error {
			var err error
			uploadResp, err = s3Client.Client().UploadPart(ctx, &s3.UploadPartInput{
				Bucket:        aws.String(s3Client.Bucket()),
				Key:           aws.String(objectKey),
				UploadId:      aws.String(uploadID),
				PartNumber:    aws.Int32(partNum),
				Body:          bytes.NewReader(partData),
				ContentLength: aws.Int64(int64(n)),
			})
			return err
		})
		if err != nil {
			return fmt.Errorf("failed to upload part %d: %w", partNum, err)
		}

		completedParts = append(completedParts, types.CompletedPart{
			ETag:       uploadResp.ETag,
			PartNumber: aws.Int32(partNum),
		})
		uploadedBytes += int64(n)

		if params.ProgressCallback != nil {
			params.ProgressCallback(float64(uploadedBytes) / float64(encryptedSize))
		}

		// Save resume state
		currentState := &state.UploadResumeState{
			LocalPath:      params.LocalPath,
			EncryptedPath:  params.EncryptedPath,
			ObjectKey:      objectKey,
			UploadID:       uploadID,
			TotalSize:      encryptedSize,
			OriginalSize:   params.OriginalSize,
			UploadedBytes:  uploadedBytes,
			CompletedParts: convertFromCompletedParts(completedParts),
			EncryptionKey:  encryption.EncodeBase64(params.EncryptionKey),
			IV:             encryption.EncodeBase64(params.IV),
			RandomSuffix:   params.RandomSuffix,
			CreatedAt:      createdAt,
			LastUpdate:     time.Now(),
			StorageType:    "S3Storage",
		}
		state.SaveUploadState(currentState, params.LocalPath)
	}

	// Complete multipart upload
	err = s3Client.RetryWithBackoff(ctx, "CompleteMultipartUpload", func() error {
		_, err := s3Client.Client().CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
			Bucket:   aws.String(s3Client.Bucket()),
			Key:      aws.String(objectKey),
			UploadId: aws.String(uploadID),
			MultipartUpload: &types.CompletedMultipartUpload{
				Parts: completedParts,
			},
		})
		return err
	})

	return err
}

// uploadEncryptedMultipartConcurrent uploads an encrypted file using concurrent S3 multipart state.
// Phase 7F: Full concurrent upload implementation directly in provider, using S3Client.
// This eliminates the dependency on state.NewS3Uploader().
func (p *Provider) uploadEncryptedMultipartConcurrent(ctx context.Context, s3Client *S3Client, params transfer.EncryptedFileUploadParams, objectKey string, encryptedSize int64) error {
	// If no transfer handle provided, fall back to sequential upload
	if params.TransferHandle == nil || params.TransferHandle.GetThreads() <= 1 {
		return p.uploadEncryptedMultipart(ctx, s3Client, params, objectKey, encryptedSize)
	}

	file, err := os.Open(params.EncryptedPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	totalSize := encryptedSize
	partSize := int64(constants.ChunkSize)
	totalParts := int32((totalSize + partSize - 1) / partSize)
	concurrency := params.TransferHandle.GetThreads()

	// Ensure cleanup on completion
	defer params.TransferHandle.Complete()

	// Acquire upload lock to prevent concurrent uploads of the same file
	uploadLock, lockErr := state.AcquireUploadLock(params.LocalPath)
	if lockErr != nil {
		return fmt.Errorf("failed to acquire upload lock: %w", lockErr)
	}
	defer state.ReleaseUploadLock(uploadLock)

	// Try to load resume state (keyed by ORIGINAL file path, not encrypted path)
	existingState, loadErr := state.LoadUploadState(params.LocalPath)
	if loadErr != nil {
		log.Printf("Warning: Failed to load resume state: %v", loadErr)
	}
	var uploadID string
	var completedParts []types.CompletedPart
	var uploadedBytes int64 = 0
	startPart := int32(1)
	resuming := false
	var createdAt time.Time

	if existingState != nil {
		// Validate resume state
		if err := state.ValidateUploadState(existingState, params.LocalPath); err != nil {
			log.Printf("Resume state validation failed, starting fresh: %v", err)
		} else {
			// Verify upload still exists on S3
			_, listErr := s3Client.Client().ListParts(ctx, &s3.ListPartsInput{
				Bucket:   aws.String(s3Client.Bucket()),
				Key:      aws.String(existingState.ObjectKey),
				UploadId: aws.String(existingState.UploadID),
			})

			if listErr == nil {
				// Valid resume state and upload exists!
				uploadID = existingState.UploadID
				completedParts = convertToCompletedParts(existingState.CompletedParts)
				uploadedBytes = existingState.UploadedBytes
				startPart = int32(len(existingState.CompletedParts)) + 1
				resuming = true
				createdAt = existingState.CreatedAt

				if params.OutputWriter != nil {
					fmt.Fprintf(params.OutputWriter, "Resuming upload from part %d/%d (%.1f%%) with %d concurrent threads\n",
						startPart, totalParts,
						float64(uploadedBytes)/float64(totalSize)*100,
						concurrency)
				}
			} else {
				// Upload ID expired or invalid, will start fresh
				if params.OutputWriter != nil {
					fmt.Fprintf(params.OutputWriter, "Previous upload expired, starting fresh upload with %d concurrent threads\n", concurrency)
				}
			}
		}
	}

	// If no valid resume, start fresh
	if uploadID == "" {
		var createResp *s3.CreateMultipartUploadOutput
		err = s3Client.RetryWithBackoff(ctx, "CreateMultipartUpload", func() error {
			var err error
			createResp, err = s3Client.Client().CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
				Bucket: aws.String(s3Client.Bucket()),
				Key:    aws.String(objectKey),
				Metadata: map[string]string{
					"iv": encryption.EncodeBase64(params.IV),
				},
			})
			return err
		})
		if err != nil {
			return fmt.Errorf("failed to create multipart upload: %w", err)
		}
		uploadID = *createResp.UploadId
		createdAt = time.Now()

		// Save initial state (keyed by original file path)
		initialState := &state.UploadResumeState{
			LocalPath:      params.LocalPath,
			EncryptedPath:  params.EncryptedPath,
			ObjectKey:      objectKey,
			UploadID:       uploadID,
			TotalSize:      totalSize,
			OriginalSize:   params.OriginalSize,
			UploadedBytes:  0,
			CompletedParts: []state.CompletedPart{},
			EncryptionKey:  encryption.EncodeBase64(params.EncryptionKey),
			IV:             encryption.EncodeBase64(params.IV),
			RandomSuffix:   params.RandomSuffix,
			CreatedAt:      createdAt,
			LastUpdate:     time.Now(),
			StorageType:    "S3Storage",
			ProcessID:      os.Getpid(),
			LockAcquiredAt: uploadLock.AcquiredAt,
		}
		state.SaveUploadState(initialState, params.LocalPath)

		// Inform user about concurrent upload
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Uploading with %d concurrent threads (%d parts of 64 MB)\n", concurrency, totalParts)
		}
	}

	// Ensure upload is aborted if we fail fatally (but keep resume state)
	defer func() {
		if err != nil {
			// Only abort if we actually failed (not on successful completion)
			s3Client.Client().AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(s3Client.Bucket()),
				Key:      aws.String(objectKey),
				UploadId: aws.String(uploadID),
			})
			// Keep resume state so user can retry
		}
	}()

	// If resuming, seek to the position after the last completed part
	if resuming && startPart > 1 {
		seekOffset := int64(startPart-1) * partSize
		if _, seekErr := file.Seek(seekOffset, 0); seekErr != nil {
			return fmt.Errorf("failed to seek to resume position: %w", seekErr)
		}
	}

	// Concurrent upload implementation
	type partJob struct {
		partNumber int32
		data       []byte
		offset     int64
	}

	type partResult struct {
		partNumber int32
		etag       string
		size       int64
		err        error
	}

	// Channels for coordination
	jobChan := make(chan partJob, concurrency*2)
	resultChan := make(chan partResult, totalParts)

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
					log.Printf("PANIC in upload worker %d: %v", workerID, r)
				}
			}()

			for job := range jobChan {
				// Check if context was cancelled
				select {
				case <-opCtx.Done():
					return
				default:
				}

				// Upload this part with retry logic
				startTime := time.Now()
				var uploadResp *s3.UploadPartOutput
				partDataToUpload := job.data
				currentPartNum := job.partNumber

				// Create context with timeout for this specific part (10 minutes)
				partCtx, cancel := context.WithTimeout(opCtx, 10*time.Minute)

				// Add HTTP tracing if DEBUG_HTTP is enabled
				partCtx = TraceContext(partCtx, fmt.Sprintf("UploadPart %d/%d (worker %d)", job.partNumber, totalParts, workerID))

				uploadErr := s3Client.RetryWithBackoff(partCtx, fmt.Sprintf("UploadPart %d/%d", job.partNumber, totalParts), func() error {
					var err error
					uploadResp, err = s3Client.Client().UploadPart(partCtx, &s3.UploadPartInput{
						Bucket:        aws.String(s3Client.Bucket()),
						Key:           aws.String(objectKey),
						PartNumber:    aws.Int32(currentPartNum),
						UploadId:      aws.String(uploadID),
						Body:          bytes.NewReader(partDataToUpload),
						ContentLength: aws.Int64(int64(len(partDataToUpload))),
					})
					return err
				})

				cancel()

				if uploadErr != nil {
					setError(fmt.Errorf("failed to upload part %d/%d: %w", job.partNumber, totalParts, uploadErr))
					return
				}

				// Calculate and record throughput
				duration := time.Since(startTime).Seconds()
				if duration > 0 {
					bytesPerSec := float64(len(partDataToUpload)) / duration
					params.TransferHandle.RecordThroughput(bytesPerSec)
				}

				// Send result
				resultChan <- partResult{
					partNumber: job.partNumber,
					etag:       *uploadResp.ETag,
					size:       int64(len(job.data)),
					err:        nil,
				}
			}
		}(i)
	}

	// Read parts from file and queue them for upload
	go func() {
		defer close(jobChan)

		partNumber := startPart
		for {
			select {
			case <-opCtx.Done():
				return
			default:
			}

			// Get buffer from pool for this part
			bufferPtr := buffers.GetChunkBuffer()
			buffer := *bufferPtr

			// Read up to partSize bytes into pooled buffer
			n, readErr := io.ReadFull(file, buffer)

			if readErr == io.EOF {
				buffers.PutChunkBuffer(bufferPtr)
				break
			}

			// Get the actual data slice and make a copy
			var partData []byte
			if readErr == io.ErrUnexpectedEOF {
				partData = make([]byte, n)
				copy(partData, buffer[:n])
				readErr = nil
			} else if readErr != nil {
				buffers.PutChunkBuffer(bufferPtr)
				setError(fmt.Errorf("failed to read file chunk: %w", readErr))
				return
			} else {
				partData = make([]byte, n)
				copy(partData, buffer[:n])
			}

			buffers.PutChunkBuffer(bufferPtr)

			// Queue this part for upload
			jobChan <- partJob{
				partNumber: partNumber,
				data:       partData,
			}

			partNumber++

			if int64(len(partData)) < partSize {
				break
			}
		}
	}()

	// Collect results and update progress
	var resultsMu sync.Mutex
	var atomicUploadedBytes int64 = uploadedBytes
	resultCount := 0
	expectedResults := int(totalParts - startPart + 1)

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

		// Add to completed parts
		resultsMu.Lock()
		completedParts = append(completedParts, types.CompletedPart{
			ETag:       aws.String(result.etag),
			PartNumber: aws.Int32(result.partNumber),
		})
		resultsMu.Unlock()

		// Update progress atomically
		atomic.AddInt64(&atomicUploadedBytes, result.size)

		resultCount++

		// Periodically save resume state
		saveInterval := 5
		if expectedResults > 20 {
			saveInterval = expectedResults / 4
		}
		if resultCount%saveInterval == 0 {
			resultsMu.Lock()
			currentState := &state.UploadResumeState{
				LocalPath:      params.LocalPath,
				EncryptedPath:  params.EncryptedPath,
				ObjectKey:      objectKey,
				UploadID:       uploadID,
				TotalSize:      totalSize,
				OriginalSize:   params.OriginalSize,
				UploadedBytes:  atomic.LoadInt64(&atomicUploadedBytes),
				CompletedParts: convertFromCompletedParts(completedParts),
				EncryptionKey:  encryption.EncodeBase64(params.EncryptionKey),
				IV:             encryption.EncodeBase64(params.IV),
				RandomSuffix:   params.RandomSuffix,
				CreatedAt:      createdAt,
				LastUpdate:     time.Now(),
				StorageType:    "S3Storage",
				ProcessID:      os.Getpid(),
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

	// Sort completed parts by part number (S3 requires this)
	sort.Slice(completedParts, func(i, j int) bool {
		return *completedParts[i].PartNumber < *completedParts[j].PartNumber
	})

	// Complete multipart upload with retry
	err = s3Client.RetryWithBackoff(ctx, "CompleteMultipartUpload", func() error {
		_, err := s3Client.Client().CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
			Bucket:   aws.String(s3Client.Bucket()),
			Key:      aws.String(objectKey),
			UploadId: aws.String(uploadID),
			MultipartUpload: &types.CompletedMultipartUpload{
				Parts: completedParts,
			},
		})
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	// Delete resume state on successful upload
	if delErr := state.DeleteUploadState(params.LocalPath); delErr != nil {
		log.Printf("Warning: Failed to delete resume state after successful upload: %v", delErr)
	}

	// Clear error to prevent defer from aborting successful upload
	err = nil
	return nil
}

// convertToCompletedParts converts state.CompletedPart slice to types.CompletedPart slice
func convertToCompletedParts(parts []state.CompletedPart) []types.CompletedPart {
	result := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		result[i] = types.CompletedPart{
			ETag:       aws.String(p.ETag),
			PartNumber: aws.Int32(p.PartNumber),
		}
	}
	return result
}

// convertFromCompletedParts converts types.CompletedPart slice to state.CompletedPart slice
func convertFromCompletedParts(parts []types.CompletedPart) []state.CompletedPart {
	result := make([]state.CompletedPart, len(parts))
	for i, p := range parts {
		etag := ""
		if p.ETag != nil {
			etag = *p.ETag
		}
		partNum := int32(0)
		if p.PartNumber != nil {
			partNum = *p.PartNumber
		}
		result[i] = state.CompletedPart{
			ETag:       etag,
			PartNumber: partNum,
		}
	}
	return result
}
