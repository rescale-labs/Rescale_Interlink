package upload

import (
	"bytes"
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
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/util/buffers"
	"github.com/rescale/rescale-int/internal/transfer"
)

// uploadMultipartConcurrent uploads a file using S3 multipart upload with concurrent part uploads
// This is a drop-in replacement for uploadMultipart that supports parallel part uploads
// originalPath: path to the unencrypted source file (used as resume state key)
// encryptedPath: path to the encrypted temp file (what we actually upload)
// transferHandle: resource allocation handle (nil = use sequential upload)
func (u *S3Uploader) uploadMultipartConcurrent(
	ctx context.Context,
	originalPath, encryptedPath, objectKey string,
	encryptionKey, iv []byte,
	randomSuffix string,
	originalSize int64,
	progressCallback ProgressCallback,
	transferHandle *transfer.Transfer,
	outputWriter io.Writer,
) error {
	// If no transfer handle provided, fall back to sequential upload
	if transferHandle == nil || transferHandle.GetThreads() <= 1 {
		return u.uploadMultipart(ctx, originalPath, encryptedPath, objectKey,
			encryptionKey, iv, randomSuffix, originalSize, progressCallback, outputWriter)
	}

	file, err := os.Open(encryptedPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	totalSize := info.Size()
	totalParts := int32((totalSize + PartSize - 1) / PartSize)
	concurrency := transferHandle.GetThreads()

	// Ensure cleanup on completion
	defer transferHandle.Complete()

	// Try to load resume state (keyed by ORIGINAL file path, not encrypted path)
	existingState, _ := LoadUploadState(originalPath)
	var uploadID string
	var completedParts []types.CompletedPart
	var uploadedBytes int64 = 0
	startPart := int32(1)
	resuming := false
	var createdAt time.Time

	if existingState != nil {
		// Validate resume state
		if err := ValidateUploadState(existingState, originalPath); err == nil {
			// Verify upload still exists on S3
			_, listErr := u.client.ListParts(ctx, &s3.ListPartsInput{
				Bucket:   aws.String(u.storageInfo.ConnectionSettings.Container),
				Key:      aws.String(existingState.ObjectKey),
				UploadId: aws.String(existingState.UploadID),
			})

			if listErr == nil {
				// Valid resume state and upload exists!
				uploadID = existingState.UploadID
				completedParts = ConvertToSDKParts(existingState.CompletedParts)
				uploadedBytes = existingState.UploadedBytes
				startPart = int32(len(existingState.CompletedParts)) + 1
				resuming = true
				createdAt = existingState.CreatedAt

				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Resuming upload from part %d/%d (%.1f%%) with %d concurrent threads\n",
						startPart, totalParts,
						float64(uploadedBytes)/float64(totalSize)*100,
						concurrency)
				}
			} else {
				// Upload ID expired or invalid, will start fresh
				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Previous upload expired, starting fresh upload with %d concurrent threads\n", concurrency)
				}
			}
		}
	}

	// If no valid resume, start fresh
	if uploadID == "" {
		var createResp *s3.CreateMultipartUploadOutput
		err = u.retryWithBackoff(ctx, "CreateMultipartUpload", func() error {
			var err error
			createResp, err = u.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
				Bucket: aws.String(u.storageInfo.ConnectionSettings.Container),
				Key:    aws.String(objectKey),
				Metadata: map[string]string{
					"iv": encryption.EncodeBase64(iv),
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
		initialState := &UploadResumeState{
			LocalPath:      originalPath,
			EncryptedPath:  encryptedPath,
			ObjectKey:      objectKey,
			UploadID:       uploadID,
			TotalSize:      totalSize,
			OriginalSize:   originalSize,
			UploadedBytes:  0,
			CompletedParts: []CompletedPart{},
			EncryptionKey:  encryption.EncodeBase64(encryptionKey),
			IV:             encryption.EncodeBase64(iv),
			RandomSuffix:   randomSuffix,
			CreatedAt:      createdAt,
			LastUpdate:     time.Now(),
			StorageType:    "S3Storage",
		}
		SaveUploadState(initialState, originalPath)

		// Inform user about concurrent upload
		if outputWriter != nil {
			fmt.Fprintf(outputWriter, "Uploading with %d concurrent threads (%d parts of 64 MB)\n", concurrency, totalParts)
		}
	}

	// Ensure upload is aborted if we fail fatally (but keep resume state)
	defer func() {
		if err != nil {
			// Only abort if we actually failed (not on successful completion)
			u.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(u.storageInfo.ConnectionSettings.Container),
				Key:      aws.String(objectKey),
				UploadId: aws.String(uploadID),
			})
			// Keep resume state so user can retry
		}
	}()

	// If resuming, seek to the position after the last completed part
	if resuming && startPart > 1 {
		seekOffset := int64(startPart-1) * PartSize
		if _, seekErr := file.Seek(seekOffset, 0); seekErr != nil {
			return fmt.Errorf("failed to seek to resume position: %w", seekErr)
		}
	}

	// Concurrent upload implementation
	type partJob struct {
		partNumber int32
		data       []byte
		offset     int64 // File offset for reading
	}

	type partResult struct {
		partNumber int32
		etag       string
		size       int64
		err        error
	}

	// Channels for coordination
	jobChan := make(chan partJob, concurrency*2)    // Buffered for better throughput
	resultChan := make(chan partResult, totalParts) // Collect all results
	errorChan := make(chan error, 1)                // First error stops everything

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

				// Upload this part with retry logic
				startTime := time.Now()
				var uploadResp *s3.UploadPartOutput
				partDataToUpload := job.data
				currentPartNum := job.partNumber

				// Create context with timeout for this specific part (10 minutes)
				partCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)

				// Add HTTP tracing if DEBUG_HTTP is enabled
				partCtx = traceContext(partCtx, fmt.Sprintf("UploadPart %d/%d (worker %d)", job.partNumber, totalParts, workerID))

				uploadErr := u.retryWithBackoff(partCtx, fmt.Sprintf("UploadPart %d/%d", job.partNumber, totalParts), func() error {
					var err error
					uploadResp, err = u.client.UploadPart(partCtx, &s3.UploadPartInput{
						Bucket:        aws.String(u.storageInfo.ConnectionSettings.Container),
						Key:           aws.String(objectKey),
						PartNumber:    aws.Int32(currentPartNum),
						UploadId:      aws.String(uploadID),
						Body:          bytes.NewReader(partDataToUpload),
						ContentLength: aws.Int64(int64(len(partDataToUpload))),
					})
					return err
				})

				cancel() // Clean up context

				if uploadErr != nil {
					// Report error and stop
					select {
					case errorChan <- fmt.Errorf("failed to upload part %d/%d: %w", job.partNumber, totalParts, uploadErr):
					default:
					}
					return
				}

				// Calculate and record throughput
				duration := time.Since(startTime).Seconds()
				if duration > 0 {
					bytesPerSec := float64(len(partDataToUpload)) / duration
					transferHandle.RecordThroughput(bytesPerSec)
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
	// This runs in the main goroutine to ensure sequential file reads
	go func() {
		defer close(jobChan)

		partNumber := startPart
		for {
			// Check if an error occurred
			select {
			case <-errorChan:
				return
			default:
			}

			// Get buffer from pool for this part
			bufferPtr := buffers.GetChunkBuffer()
			buffer := *bufferPtr

			// Read up to PartSize bytes into pooled buffer
			n, readErr := io.ReadFull(file, buffer)

			// Handle EOF - last part may be smaller
			if readErr == io.EOF {
				buffers.PutChunkBuffer(bufferPtr) // Return buffer before breaking
				break                             // No more data
			}

			// Get the actual data slice for this part and make a copy
			var partData []byte
			if readErr == io.ErrUnexpectedEOF {
				// Last part is smaller than PartSize
				partData = make([]byte, n)
				copy(partData, buffer[:n])
				readErr = nil
			} else if readErr != nil {
				buffers.PutChunkBuffer(bufferPtr) // Return buffer on error
				select {
				case errorChan <- fmt.Errorf("failed to read file chunk: %w", readErr):
				default:
				}
				return
			} else {
				partData = make([]byte, n)
				copy(partData, buffer[:n])
			}

			// Return buffer to pool immediately after copying
			buffers.PutChunkBuffer(bufferPtr)

			// Queue this part for upload
			jobChan <- partJob{
				partNumber: partNumber,
				data:       partData,
			}

			partNumber++

			// Check if we've read all data
			if int64(len(partData)) < PartSize {
				break // Last part was smaller, we're done
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
			select {
			case errorChan <- result.err:
			default:
			}
			break
		}

		// Add to completed parts
		resultsMu.Lock()
		completedParts = append(completedParts, types.CompletedPart{
			ETag:       aws.String(result.etag),
			PartNumber: aws.Int32(result.partNumber),
		})
		resultsMu.Unlock()

		// Update progress atomically (ticker will pick this up)
		atomic.AddInt64(&atomicUploadedBytes, result.size)

		resultCount++

		// Periodically save resume state (every 5 parts or 25% of parts, whichever is smaller)
		saveInterval := 5
		if expectedResults > 20 {
			saveInterval = expectedResults / 4
		}
		if resultCount%saveInterval == 0 {
			resultsMu.Lock()
			currentState := &UploadResumeState{
				LocalPath:      originalPath,
				EncryptedPath:  encryptedPath,
				ObjectKey:      objectKey,
				UploadID:       uploadID,
				TotalSize:      totalSize,
				OriginalSize:   originalSize,
				UploadedBytes:  atomic.LoadInt64(&atomicUploadedBytes),
				CompletedParts: ConvertFromSDKParts(completedParts),
				EncryptionKey:  encryption.EncodeBase64(encryptionKey),
				IV:             encryption.EncodeBase64(iv),
				RandomSuffix:   randomSuffix,
				CreatedAt:      createdAt,
				LastUpdate:     time.Now(),
				StorageType:    "S3Storage",
			}
			SaveUploadState(currentState, originalPath)
			resultsMu.Unlock()
		}
	}

	// Check for errors
	select {
	case err := <-errorChan:
		return err
	default:
	}

	// Sort completed parts by part number (S3 requires this)
	sort.Slice(completedParts, func(i, j int) bool {
		return *completedParts[i].PartNumber < *completedParts[j].PartNumber
	})

	// Complete multipart upload with retry
	err = u.retryWithBackoff(ctx, "CompleteMultipartUpload", func() error {
		_, err := u.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
			Bucket:   aws.String(u.storageInfo.ConnectionSettings.Container),
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

	// Clear error to prevent defer from aborting successful upload
	err = nil
	return nil
}
