// Package azure provides an Azure implementation of the CloudTransfer interface.
// This file implements the PreEncryptUploader interface for pre-encrypted uploads.
//
// Concurrent blocks are staged through transfer.RunPartPipeline; this file
// supplies the provider-specific setup, staging call and commit.
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
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
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

	azureClient.StartPeriodicRefresh(ctx)
	defer azureClient.StopPeriodicRefresh()

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

// blockIDForIndex names the block at a zero-based index. Both upload paths have
// always staged under this scheme, which is what lets an index be read back out
// of a checkpoint that only keeps a flat list of IDs.
func blockIDForIndex(index int64) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("block-%06d", index)))
}

func blockIndexFromID(blockID string) (int64, error) {
	decoded, err := base64.StdEncoding.DecodeString(blockID)
	if err != nil {
		return 0, fmt.Errorf("block ID %q is not base64: %w", blockID, err)
	}
	digits, ok := strings.CutPrefix(string(decoded), "block-")
	if !ok {
		return 0, fmt.Errorf("block ID %q was not staged by this client", blockID)
	}
	index, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || index < 0 {
		return 0, fmt.Errorf("block ID %q carries no index", blockID)
	}
	return index, nil
}

// azureResume is the geometry an interrupted attempt left behind: the block size
// it cut the ciphertext with, the blocks Azure is already holding keyed by
// index, and the index of the first block no attempt has covered.
type azureResume struct {
	blockSize    int64
	completed    map[int64]string
	firstMissing int64
}

// blocksBefore returns the staged block IDs below an index, in index order,
// which is the prefix the resumed run will not read again.
func (r azureResume) blocksBefore(index int64) []string {
	blocks := make([]string, 0, index)
	for i := int64(0); i < index; i++ {
		blocks = append(blocks, r.completed[i])
	}
	return blocks
}

// resumeAzureBlocks reads an interrupted attempt's geometry out of its
// checkpoint, reporting false when the checkpoint cannot say what that geometry
// was — which means a fresh upload.
//
// The checkpoint keeps a flat list of block IDs, so the count of that list is
// not a resume point: blocks are staged out of order under concurrency and the
// list is compacted, so a gap makes the count and the byte total describe
// different geometries. Each ID names its own index, and that is what is used.
// The block size has to come from the checkpoint too — the plan is recomputed on
// every attempt, and blocks cut to a different size line up with nothing Azure
// is already holding.
func resumeAzureBlocks(saved *state.UploadResumeState, totalSize int64) (azureResume, bool) {
	if saved == nil || saved.PartSize <= 0 || len(saved.BlockIDs) == 0 {
		return azureResume{}, false
	}

	totalBlocks := transfer.CalculateTotalParts(totalSize, saved.PartSize)
	completed := make(map[int64]string, len(saved.BlockIDs))
	for _, blockID := range saved.BlockIDs {
		index, err := blockIndexFromID(blockID)
		if err == nil && index >= totalBlocks {
			err = fmt.Errorf("block ID %q is past the %d blocks this file takes", blockID, totalBlocks)
		}
		if err != nil {
			// One ID this client cannot place leaves the whole list unplaced:
			// committing a block list that is a guess is how a blob ends up
			// holding the wrong bytes and reporting success.
			log.Printf("Resume state holds a block that cannot be placed, starting fresh: %v", err)
			return azureResume{}, false
		}
		completed[index] = blockID
	}

	firstMissing := int64(0)
	for ; firstMissing < totalBlocks; firstMissing++ {
		if _, done := completed[firstMissing]; !done {
			break
		}
	}

	return azureResume{blockSize: saved.PartSize, completed: completed, firstMissing: firstMissing}, true
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

	// Block size comes from the caller's upload plan, which keeps the block count
	// under MaxAzureUploadBlocks as well as within the memory budget.
	plan, err := params.UploadPlan(encryptedSize, p.UploadLimits())
	if err != nil {
		return err
	}
	blockSize := plan.PartSize
	totalBlocks := transfer.CalculateTotalParts(encryptedSize, blockSize)

	// Try to load resume state
	existingState, _ := state.LoadUploadState(params.LocalPath)
	var blockIDs []string
	var alreadyStaged map[int64]string
	var uploadedBytes int64 = 0
	startBlock := int64(0)
	resuming := false
	var createdAt time.Time

	if existingState != nil && existingState.ObjectKey == pathForRescale {
		// The object key and the block geometry say the checkpoint describes this
		// upload; they say nothing about whether it still describes this file, or
		// whether the service still holds the blocks — uncommitted blocks live
		// seven days. A checkpoint the concurrent path wrote arrives here whenever
		// the thread count changes between runs, and that path validates first.
		resume, ok := resumeAzureBlocks(existingState, encryptedSize)
		if err := state.ValidateUploadState(existingState, params.LocalPath); err != nil {
			log.Printf("Resume state validation failed, starting fresh: %v", err)
		} else if ok {
			blockSize = resume.blockSize
			totalBlocks = transfer.CalculateTotalParts(encryptedSize, blockSize)
			alreadyStaged = resume.completed
			startBlock = resume.firstMissing
			blockIDs = resume.blocksBefore(startBlock)
			uploadedBytes = min(startBlock*blockSize, encryptedSize)
			resuming = true
			createdAt = existingState.CreatedAt

			if _, err := file.Seek(startBlock*blockSize, 0); err != nil {
				return fmt.Errorf("failed to seek in file: %w", err)
			}
			if params.OutputWriter != nil {
				fmt.Fprintf(params.OutputWriter, "Resuming upload from block %d/%d\n", startBlock+1, totalBlocks)
			}
		}
	}

	if !resuming {
		createdAt = time.Now()
	}

	// Report initial progress
	if params.ProgressCallback != nil {
		params.ProgressCallback(float64(uploadedBytes) / float64(encryptedSize))
	}

	// Sized to the block size the plan chose, so a short read means the end of the
	// file rather than the end of the buffer.
	buffer, releaseBuffer := buffers.GetPartBuffer(blockSize)
	defer releaseBuffer()

	// Upload blocks. The loop runs to EOF rather than to a precomputed block
	// count: the count is what the file SHOULD take, and the read is what it
	// actually takes — disagreeing with each other is the bug this guards.
	for blockNum := startBlock; ; blockNum++ {
		n, err := io.ReadFull(file, buffer)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("failed to read block %d: %w", blockNum, err)
		}
		if n == 0 {
			break
		}

		// Already staged by the interrupted attempt: the read above keeps the
		// byte count covering the file, but nothing is sent again.
		if staged, done := alreadyStaged[blockNum]; done {
			blockIDs = append(blockIDs, staged)
			uploadedBytes += int64(n)
			if params.ProgressCallback != nil {
				params.ProgressCallback(float64(uploadedBytes) / float64(encryptedSize))
			}
			continue
		}

		// Generate block ID
		blockID := blockIDForIndex(blockNum)

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
			SourceModTime: params.SourceModTime,
			UploadedBytes: uploadedBytes,
			BlockIDs:      blockIDs,
			PartSize:      blockSize,
			EncryptionKey: encryption.EncodeBase64(params.EncryptionKey),
			IV:            encryption.EncodeBase64(params.IV),
			RandomSuffix:  params.RandomSuffix,
			CreatedAt:     createdAt,
			LastUpdate:    time.Now(),
			StorageType:   "AzureStorage",
			StorageID:     p.storageID(),
			Container:     p.storageContainer(),
		}
		state.SaveUploadState(currentState, params.LocalPath)
	}

	// Azure commits whatever block list it is handed and reports success, so a
	// list that does not cover the whole file has to be caught here rather than
	// discovered as a short blob later.
	if err := transfer.VerifyUploadComplete(uploadedBytes, encryptedSize, int64(len(blockIDs)), totalBlocks); err != nil {
		return fmt.Errorf("refusing to commit %s: %w", pathForRescale, err)
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
	// Block size, worker count and queue depth all come from the caller's upload
	// plan: it keeps the block count under MaxAzureUploadBlocks and bounds how
	// many block-sized buffers this pipeline can hold at once.
	plan, err := params.UploadPlan(totalSize, p.UploadLimits())
	if err != nil {
		return err
	}
	partSize := plan.PartSize
	totalBlocks := transfer.CalculateTotalParts(totalSize, partSize)
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

	// Try to load resume state
	existingState, loadErr := state.LoadUploadState(params.LocalPath)
	if loadErr != nil {
		log.Printf("Warning: Failed to load resume state: %v", loadErr)
	}
	var alreadyStaged map[int64]string
	var uploadedBytes int64 = 0
	startBlock := int64(0)
	resuming := false
	var createdAt time.Time

	if existingState != nil && existingState.ObjectKey == pathForRescale {
		// The object key and the block geometry say the checkpoint describes this
		// upload; they say nothing about whether it still describes this file, or
		// whether the service still holds the blocks — uncommitted blocks live
		// seven days. Committing a list naming blocks Azure has dropped is how a
		// blob ends up short. S3's concurrent path runs this same check.
		resume, ok := resumeAzureBlocks(existingState, totalSize)
		if err := state.ValidateUploadState(existingState, params.LocalPath); err != nil {
			log.Printf("Resume state validation failed, starting fresh: %v", err)
		} else if ok {
			partSize = resume.blockSize
			totalBlocks = transfer.CalculateTotalParts(totalSize, partSize)
			alreadyStaged = resume.completed
			startBlock = resume.firstMissing
			uploadedBytes = min(startBlock*partSize, totalSize)
			resuming = true
			createdAt = existingState.CreatedAt

			if _, err := file.Seek(startBlock*partSize, 0); err != nil {
				return fmt.Errorf("failed to seek to resume position: %w", err)
			}

			if params.OutputWriter != nil {
				fmt.Fprintf(params.OutputWriter, "Resuming upload from block %d/%d (%.1f%%) with %d concurrent threads\n",
					startBlock+1, totalBlocks,
					float64(uploadedBytes)/float64(totalSize)*100,
					concurrency)
			}
		}
	}

	if !resuming {
		createdAt = time.Now()
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Uploading with %d concurrent threads (%d blocks)\n", concurrency, totalBlocks)
		}
	}

	// Pre-allocate blockIDs slice for ordering. Its length comes from the block
	// size the resume settled on, so a plan that would have chosen a different
	// one cannot leave the restored blocks hanging off the end of it.
	allBlockIDs := make([]string, totalBlocks)
	// Copy existing block IDs from resume state
	for index := int64(0); index < startBlock; index++ {
		allBlockIDs[index] = alreadyStaged[index]
	}

	// Stage every block through the shared concurrent pipeline.
	stagedBytes, pipelineErr := transfer.RunPartPipeline(ctx, transfer.PartPipelineConfig{
		Reader:        file,
		PartSize:      partSize,
		TotalParts:    totalBlocks,
		StartPart:     startBlock,
		UploadedBytes: uploadedBytes,
		Concurrency:   concurrency,
		QueueDepth:    plan.QueueDepth,
		WorkerLabel:   "Azure upload worker",
		StagePart: func(blockCtx context.Context, part transfer.PartAssignment) (string, error) {
			// Already staged by the interrupted attempt. The pipeline still
			// reads this block so the staged byte count covers the file, but
			// sending it again is what the resume is here to avoid.
			if staged, done := alreadyStaged[part.Index]; done {
				return staged, nil
			}
			blockID := blockIDForIndex(part.Index)

			stageErr := azureClient.RetryWithBackoff(blockCtx, fmt.Sprintf("StageBlock %d/%d", part.Index+1, totalBlocks), func() error {
				client := azureClient.Client()
				blockBlobClient := client.ServiceClient().NewContainerClient(azureClient.Container()).NewBlockBlobClient(blobPath)
				_, err := blockBlobClient.StageBlock(blockCtx, blockID, &readSeekCloser{Reader: bytes.NewReader(part.Data)}, nil)
				return err
			})
			if stageErr != nil {
				return "", fmt.Errorf("failed to stage block %d/%d: %w", part.Index+1, totalBlocks, stageErr)
			}

			return blockID, nil
		},
		RecordPart: func(index int64, blockID string) {
			allBlockIDs[index] = blockID
		},
		OnProgress: func(uploaded int64) {
			if params.ProgressCallback != nil {
				params.ProgressCallback(float64(uploaded) / float64(totalSize))
			}
		},
		SaveState: func(uploaded int64, staged int) {
			// Every slot that holds a block, not the first `staged` of them:
			// blocks land out of order, so a count says nothing about which
			// indices are filled. Each ID carries its own index, which is what
			// lets the next attempt put this list back where it belongs.
			currentBlockIDs := make([]string, 0, len(allBlockIDs))
			for _, blockID := range allBlockIDs {
				if blockID != "" {
					currentBlockIDs = append(currentBlockIDs, blockID)
				}
			}
			currentState := &state.UploadResumeState{
				LocalPath:     params.LocalPath,
				EncryptedPath: params.EncryptedPath,
				ObjectKey:     pathForRescale,
				TotalSize:     totalSize,
				OriginalSize:  params.OriginalSize,
				SourceModTime: params.SourceModTime,
				UploadedBytes: uploaded,
				BlockIDs:      currentBlockIDs,
				PartSize:      partSize,
				EncryptionKey: encryption.EncodeBase64(params.EncryptionKey),
				IV:            encryption.EncodeBase64(params.IV),
				RandomSuffix:  params.RandomSuffix,
				CreatedAt:     createdAt,
				LastUpdate:    time.Now(),
				StorageType:   "AzureStorage",
				StorageID:     p.storageID(),
				Container:     p.storageContainer(),
				ProcessID:     os.Getpid(),
			}
			state.SaveUploadState(currentState, params.LocalPath)
		},
	})
	if pipelineErr != nil {
		return pipelineErr
	}

	// Every slot has to be filled. Skipping the empty ones instead would close the
	// gap left by a block that was never staged and commit a blob shorter than the
	// file, which Azure accepts and reports as a success.
	finalBlockIDs := allBlockIDs
	if err := transfer.VerifyBlockList(finalBlockIDs); err != nil {
		return fmt.Errorf("refusing to commit %s: %w", pathForRescale, err)
	}
	if err := transfer.VerifyUploadComplete(stagedBytes, totalSize, int64(len(finalBlockIDs)), totalBlocks); err != nil {
		return fmt.Errorf("refusing to commit %s: %w", pathForRescale, err)
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
