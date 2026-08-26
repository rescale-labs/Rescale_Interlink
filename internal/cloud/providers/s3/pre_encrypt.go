// Package s3 provides an S3 implementation of the CloudTransfer interface.
// This file implements the PreEncryptUploader interface for pre-encrypted uploads.
//
// Concurrent parts are staged through transfer.RunPartPipeline; this file
// supplies the provider-specific setup, staging call and commit.
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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/util/buffers"
)

// Verify that Provider implements PreEncryptUploader
var _ transfer.PreEncryptUploader = (*Provider)(nil)

// UploadEncryptedFile uploads an already-encrypted file to S3.
// This implements the PreEncryptUploader interface.
// The encryption is already done by the orchestrator; this method handles the state.
// Uses S3Client directly instead of wrapping state.S3Uploader.
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
// Uses S3Client directly.
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
// Uses S3Client directly.
func (p *Provider) uploadEncryptedMultipart(ctx context.Context, s3Client *S3Client, params transfer.EncryptedFileUploadParams, objectKey string, encryptedSize int64) error {
	file, err := os.Open(params.EncryptedPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Part size comes from the caller's upload plan, which keeps the part count
	// under MaxS3UploadParts as well as within the memory budget.
	plan, err := params.UploadPlan(encryptedSize, p.UploadLimits())
	if err != nil {
		return err
	}
	partSize := plan.PartSize
	totalParts := transfer.CalculateTotalParts(encryptedSize, partSize)

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
	buffer, releaseBuffer := buffers.GetPartBuffer(partSize)
	defer releaseBuffer()
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

	if err := verifyS3PartsComplete(uploadedBytes, encryptedSize, completedParts, totalParts); err != nil {
		abortS3Upload(ctx, s3Client, objectKey, uploadID)
		return fmt.Errorf("refusing to complete upload of %s: %w", objectKey, err)
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

// uploadEncryptedMultipartConcurrent uploads an encrypted file using concurrent
// S3 multipart state, driving S3Client directly from the provider.
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
	// Part size, worker count and queue depth all come from the caller's upload
	// plan: it keeps the part count under MaxS3UploadParts and bounds how many
	// part-sized buffers this pipeline can hold at once.
	plan, err := params.UploadPlan(totalSize, p.UploadLimits())
	if err != nil {
		return err
	}
	partSize := plan.PartSize
	totalParts := int32(transfer.CalculateTotalParts(totalSize, partSize))
	concurrency := params.TransferHandle.GetThreads()
	if concurrency > plan.WorkerCap {
		concurrency = plan.WorkerCap
	}
	// A plan that carries no worker cap would otherwise start no workers at all:
	// nothing drains the queue, so the upload fails its completeness check and
	// strands the producer goroutine.
	if concurrency < 1 {
		concurrency = 1
	}

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

	if existingState != nil && existingState.ObjectKey != objectKey {
		// Every attempt generates a fresh key, IV and object suffix, so a state
		// left by an earlier attempt describes an upload of DIFFERENT ciphertext.
		// Resuming it would interleave two encryptions into one object; the parts
		// already sent there are unusable, so drop the whole thing and start over.
		log.Printf("Resume state is for a previous upload (%s), starting fresh", existingState.ObjectKey)
		if existingState.UploadID != "" {
			abortS3Upload(ctx, s3Client, existingState.ObjectKey, existingState.UploadID)
		}
		if delErr := state.DeleteUploadState(params.LocalPath); delErr != nil {
			log.Printf("Warning: Failed to delete stale resume state: %v", delErr)
		}
		existingState = nil
	}

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
			fmt.Fprintf(params.OutputWriter, "Uploading with %d concurrent threads (%d parts of %s)\n",
				concurrency, totalParts, cloud.FormatBytes(partSize))
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

	// Stage every part through the shared concurrent pipeline. S3 numbers parts
	// from one, so the pipeline's zero-based index is offset here.
	stagedBytes, pipelineErr := transfer.RunPartPipeline(ctx, transfer.PartPipelineConfig{
		Reader:        file,
		PartSize:      partSize,
		TotalParts:    int64(totalParts),
		StartPart:     int64(startPart) - 1,
		UploadedBytes: uploadedBytes,
		Concurrency:   concurrency,
		QueueDepth:    plan.QueueDepth,
		WorkerLabel:   "upload worker",
		StagePart: func(partCtx context.Context, part transfer.PartAssignment) (string, error) {
			partNumber := int32(part.Index) + 1
			var uploadResp *s3.UploadPartOutput

			// Add HTTP tracing if DEBUG_HTTP is enabled
			partCtx = TraceContext(partCtx, fmt.Sprintf("UploadPart %d/%d (worker %d)", partNumber, totalParts, part.WorkerID))

			uploadErr := s3Client.RetryWithBackoff(partCtx, fmt.Sprintf("UploadPart %d/%d", partNumber, totalParts), func() error {
				var err error
				uploadResp, err = s3Client.Client().UploadPart(partCtx, &s3.UploadPartInput{
					Bucket:        aws.String(s3Client.Bucket()),
					Key:           aws.String(objectKey),
					PartNumber:    aws.Int32(partNumber),
					UploadId:      aws.String(uploadID),
					Body:          bytes.NewReader(part.Data),
					ContentLength: aws.Int64(int64(len(part.Data))),
				})
				return err
			})
			if uploadErr != nil {
				return "", fmt.Errorf("failed to upload part %d/%d: %w", partNumber, totalParts, uploadErr)
			}

			return *uploadResp.ETag, nil
		},
		RecordPart: func(index int64, etag string) {
			completedParts = append(completedParts, types.CompletedPart{
				ETag:       aws.String(etag),
				PartNumber: aws.Int32(int32(index) + 1),
			})
		},
		SaveState: func(uploaded int64, staged int) {
			currentState := &state.UploadResumeState{
				LocalPath:      params.LocalPath,
				EncryptedPath:  params.EncryptedPath,
				ObjectKey:      objectKey,
				UploadID:       uploadID,
				TotalSize:      totalSize,
				OriginalSize:   params.OriginalSize,
				UploadedBytes:  uploaded,
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
		},
	})

	// Not assigned to err: the deferred abort stays disarmed so the upload ID in
	// the resume state is still valid to retry against.
	if pipelineErr != nil {
		return pipelineErr
	}

	// Sort completed parts by part number (S3 requires this)
	sort.Slice(completedParts, func(i, j int) bool {
		return *completedParts[i].PartNumber < *completedParts[j].PartNumber
	})

	// The pipeline's producer stops on the first short read, so anything that
	// makes a read return early — a mis-sized buffer, a truncated temp file —
	// ends with a part list S3 would happily assemble into a shorter object and
	// report as the whole file. Assigning err here also arms the deferred abort.
	if err = verifyS3PartsComplete(stagedBytes, totalSize, completedParts, int64(totalParts)); err != nil {
		err = fmt.Errorf("refusing to complete upload of %s: %w", objectKey, err)
		return err
	}

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

// verifyS3PartsComplete checks that an upload covers the whole encrypted file
// before it is committed. parts must already be in part-number order, which is
// the order S3 assembles them in.
func verifyS3PartsComplete(uploadedBytes, encryptedSize int64, parts []types.CompletedPart, totalParts int64) error {
	if err := transfer.VerifyUploadComplete(uploadedBytes, encryptedSize, int64(len(parts)), totalParts); err != nil {
		return err
	}
	partNumbers := make([]int32, len(parts))
	for i, part := range parts {
		if part.PartNumber != nil {
			partNumbers[i] = *part.PartNumber
		}
	}
	return transfer.VerifyPartSequence(partNumbers)
}

// abortS3Upload discards a multipart upload the caller has decided not to
// commit. Best effort: the parts already sent are billed until the bucket's
// lifecycle rules expire them, but there is nothing useful to do with a failure
// here beyond logging it.
func abortS3Upload(ctx context.Context, s3Client *S3Client, objectKey, uploadID string) {
	_, err := s3Client.Client().AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s3Client.Bucket()),
		Key:      aws.String(objectKey),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		log.Printf("Warning: Failed to abort multipart upload %s for %s: %v", uploadID, objectKey, err)
	}
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
