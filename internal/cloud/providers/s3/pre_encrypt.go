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
	var alreadyOnS3 map[int32]string
	var uploadedBytes int64 = 0
	startPart := int32(1)
	resuming := false
	var createdAt time.Time

	if existingState != nil && existingState.UploadID != "" && existingState.ObjectKey == objectKey {
		if resume, ok := resumeS3Parts(existingState, encryptedSize); ok {
			uploadID = existingState.UploadID
			partSize = resume.partSize
			totalParts = transfer.CalculateTotalParts(encryptedSize, partSize)
			alreadyOnS3 = resume.completed
			startPart = int32(resume.firstMissing) + 1
			completedParts = resume.partsBefore(resume.firstMissing)
			uploadedBytes = min(resume.firstMissing*partSize, encryptedSize)
			resuming = true
			createdAt = existingState.CreatedAt

			if _, err := file.Seek(resume.firstMissing*partSize, 0); err != nil {
				return fmt.Errorf("failed to seek in file: %w", err)
			}
			if params.OutputWriter != nil {
				fmt.Fprintf(params.OutputWriter, "Resuming upload from part %d/%d\n", startPart, totalParts)
			}
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

		// Already accepted by the interrupted attempt: the read above keeps the
		// byte count covering the file, but nothing is sent again.
		if etag, done := alreadyOnS3[partNum]; done {
			completedParts = append(completedParts, types.CompletedPart{
				ETag:       aws.String(etag),
				PartNumber: aws.Int32(partNum),
			})
			uploadedBytes += int64(n)
			continue
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
			SourceModTime:  params.SourceModTime,
			UploadedBytes:  uploadedBytes,
			CompletedParts: convertFromCompletedParts(completedParts),
			PartSize:       partSize,
			EncryptionKey:  encryption.EncodeBase64(params.EncryptionKey),
			IV:             encryption.EncodeBase64(params.IV),
			RandomSuffix:   params.RandomSuffix,
			CreatedAt:      createdAt,
			LastUpdate:     time.Now(),
			StorageType:    "S3Storage",
			StorageID:      p.storageID(),
			Container:      p.storageContainer(),
		}
		state.SaveUploadState(currentState, params.LocalPath)
	}

	if err := verifyS3PartsComplete(uploadedBytes, encryptedSize, completedParts, totalParts); err != nil {
		abortCtx, cancelAbort := abortContext(ctx)
		abortS3Upload(abortCtx, s3Client, objectKey, uploadID)
		cancelAbort()
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

	// Try to load resume state (keyed by ORIGINAL file path, not encrypted path)
	existingState, loadErr := state.LoadUploadState(params.LocalPath)
	if loadErr != nil {
		log.Printf("Warning: Failed to load resume state: %v", loadErr)
	}
	var uploadID string
	var completedParts []types.CompletedPart
	var alreadyOnS3 map[int32]string
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
		resume, resumable := resumeS3Parts(existingState, totalSize)
		if err := state.ValidateUploadState(existingState, params.LocalPath); err != nil {
			log.Printf("Resume state validation failed, starting fresh: %v", err)
		} else if !resumable {
			log.Printf("Resume state does not record the part size it used, starting fresh")
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
				partSize = resume.partSize
				totalParts = int32(transfer.CalculateTotalParts(totalSize, partSize))
				alreadyOnS3 = resume.completed
				completedParts = resume.partsBefore(resume.firstMissing)
				uploadedBytes = min(resume.firstMissing*partSize, totalSize)
				startPart = int32(resume.firstMissing) + 1
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
			SourceModTime:  params.SourceModTime,
			UploadedBytes:  0,
			CompletedParts: []state.CompletedPart{},
			PartSize:       partSize,
			EncryptionKey:  encryption.EncodeBase64(params.EncryptionKey),
			IV:             encryption.EncodeBase64(params.IV),
			RandomSuffix:   params.RandomSuffix,
			CreatedAt:      createdAt,
			LastUpdate:     time.Now(),
			StorageType:    "S3Storage",
			StorageID:      p.storageID(),
			Container:      p.storageContainer(),
			ProcessID:      os.Getpid(),
		}
		state.SaveUploadState(initialState, params.LocalPath)

		// Inform user about concurrent upload
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Uploading with %d concurrent threads (%d parts of %s)\n",
				concurrency, totalParts, cloud.FormatBytes(partSize))
		}
	}

	// Nothing here aborts the multipart upload on the way out. A failed attempt
	// leaves a resume state naming this upload ID, and the retry the wrapper
	// keeps the ciphertext for continues it: a failed COMPLETION above all, where
	// every part is already on S3 and only the assembly has to be asked for
	// again. The one upload that must not survive is one whose part list does
	// not cover the file, and that is aborted where it is refused.

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
			// Already accepted by the interrupted attempt. The pipeline still
			// reads this part so the staged byte count covers the file, but
			// paying for it twice is exactly what the resume is here to avoid.
			if etag, done := alreadyOnS3[partNumber]; done {
				return etag, nil
			}
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
				SourceModTime:  params.SourceModTime,
				UploadedBytes:  uploaded,
				CompletedParts: convertFromCompletedParts(completedParts),
				PartSize:       partSize,
				EncryptionKey:  encryption.EncodeBase64(params.EncryptionKey),
				IV:             encryption.EncodeBase64(params.IV),
				RandomSuffix:   params.RandomSuffix,
				CreatedAt:      createdAt,
				LastUpdate:     time.Now(),
				StorageType:    "S3Storage",
				StorageID:      p.storageID(),
				Container:      p.storageContainer(),
				ProcessID:      os.Getpid(),
			}
			state.SaveUploadState(currentState, params.LocalPath)
		},
	})

	// The upload ID in the resume state is still valid to retry against.
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
	// report as the whole file. Such an upload has nothing left to resume, so it
	// is the one this function does abort.
	if verifyErr := verifyS3PartsComplete(stagedBytes, totalSize, completedParts, int64(totalParts)); verifyErr != nil {
		abortCtx, cancelAbort := abortContext(ctx)
		abortS3Upload(abortCtx, s3Client, objectKey, uploadID)
		cancelAbort()
		return fmt.Errorf("refusing to complete upload of %s: %w", objectKey, verifyErr)
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

// s3Resume is the geometry an interrupted attempt left behind: the part size it
// cut the ciphertext with, the parts S3 has already accepted keyed by part
// number, and the index of the first part no attempt has covered.
type s3Resume struct {
	partSize     int64
	completed    map[int32]string
	firstMissing int64
}

// partsBefore returns the accepted parts below a zero-based index, in part-number
// order — the prefix the resumed run will not read again. Order is the point:
// the sequential path completes the list it built without sorting it, and S3
// assembles the object in the order the list is given.
func (r s3Resume) partsBefore(index int64) []types.CompletedPart {
	parts := make([]types.CompletedPart, 0, index)
	for i := int64(0); i < index; i++ {
		number := int32(i) + 1
		parts = append(parts, types.CompletedPart{ETag: aws.String(r.completed[number]), PartNumber: aws.Int32(number)})
	}
	return parts
}

// resumeS3Parts reads an interrupted attempt's geometry out of its checkpoint,
// reporting false when the checkpoint cannot say what that geometry was — which
// means a fresh upload.
//
// Parts finish out of order under concurrency, so the recorded list is a set and
// not a prefix: the resume point is the first index missing from it, and an
// accepted part above that point is skipped rather than sent again. The part
// size has to come from the checkpoint too. The plan is recomputed on every
// attempt against the memory the machine has then, and parts cut to a different
// size line up with nothing S3 is already holding.
func resumeS3Parts(saved *state.UploadResumeState, totalSize int64) (s3Resume, bool) {
	if saved == nil || saved.PartSize <= 0 {
		return s3Resume{}, false
	}

	totalParts := transfer.CalculateTotalParts(totalSize, saved.PartSize)
	completed := make(map[int32]string, len(saved.CompletedParts))
	for _, part := range saved.CompletedParts {
		if part.ETag == "" || part.PartNumber < 1 || int64(part.PartNumber) > totalParts {
			continue
		}
		completed[part.PartNumber] = part.ETag
	}

	firstMissing := int64(0)
	for ; firstMissing < totalParts; firstMissing++ {
		if _, done := completed[int32(firstMissing)+1]; !done {
			break
		}
	}

	return s3Resume{partSize: saved.PartSize, completed: completed, firstMissing: firstMissing}, true
}

// abortContext detaches an abort from the attempt that is giving up. The failure
// that reaches an abort is often the cancellation of that very context, and a
// request issued on a cancelled context never leaves the process — so the upload
// it was meant to discard would stay open.
func abortContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), constants.AbortOperationTimeout)
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

// storageID and storageContainer name the destination this provider uploads to.
// A resume state records them so that an upload of the same source to another
// destination — the sidecar keys on the local path alone — is not continued as
// this one. A provider built without its storage info records neither, which
// reads as "not recorded" rather than as a different destination.
func (p *Provider) storageID() string {
	if p.storageInfo == nil {
		return ""
	}
	return p.storageInfo.ID
}

func (p *Provider) storageContainer() string {
	if p.storageInfo == nil {
		return ""
	}
	return p.storageInfo.ConnectionSettings.Container
}
