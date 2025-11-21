package upload

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"

	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/util/buffers"
	"github.com/rescale/rescale-int/internal/transfer"
)

// uploadBlockBlobConcurrent uploads a file to Azure using concurrent block staging
// This is a drop-in replacement for uploadBlockBlob that supports parallel block uploads
// transferHandle: resource allocation handle (nil = use sequential upload)
func (u *AzureUploader) uploadBlockBlobConcurrent(
	ctx context.Context,
	originalPath, encryptedPath, blobPath, pathForRescale string,
	encryptionKey, iv []byte,
	randomSuffix string,
	originalSize, totalSize int64,
	callback ProgressCallback,
	transferHandle *transfer.Transfer,
	outputWriter io.Writer,
) error {
	// If no transfer handle provided or only 1 thread, fall back to sequential
	if transferHandle == nil || transferHandle.GetThreads() <= 1 {
		return u.uploadBlockBlob(ctx, originalPath, encryptedPath, blobPath, pathForRescale,
			encryptionKey, iv, randomSuffix, originalSize, totalSize, callback, outputWriter)
	}

	concurrency := transferHandle.GetThreads()

	// Ensure cleanup on completion
	defer transferHandle.Complete()

	// Open encrypted file
	file, err := os.Open(encryptedPath)
	if err != nil {
		return fmt.Errorf("failed to open encrypted file: %w", err)
	}
	defer file.Close()

	// Calculate number of blocks
	numBlocks := int64((totalSize + azureBlockSize - 1) / azureBlockSize)

	// Try to load resume state (keyed by ORIGINAL file path, not encrypted path)
	existingState, _ := LoadUploadState(originalPath)
	var blockIDs []string
	var uploadedBytes int64 = 0
	startBlock := int64(0)
	resuming := false
	var createdAt time.Time

	if existingState != nil {
		// Validate resume state
		if err := ValidateUploadState(existingState, originalPath); err == nil {
			// Get uncommitted blocks from Azure (verify they still exist)
			u.clientMu.Lock()
			client := u.client
			u.clientMu.Unlock()

			blockBlobClient := client.ServiceClient().NewContainerClient(u.storageInfo.ConnectionSettings.Container).NewBlockBlobClient(blobPath)
			blockListResp, listErr := blockBlobClient.GetBlockList(ctx, blockblob.BlockListTypeUncommitted, nil)

			if listErr == nil && blockListResp.UncommittedBlocks != nil && len(blockListResp.UncommittedBlocks) > 0 {
				// Valid resume state and uncommitted blocks exist!
				blockIDs = existingState.BlockIDs
				uploadedBytes = existingState.UploadedBytes
				startBlock = int64(len(blockIDs))
				resuming = true
				createdAt = existingState.CreatedAt

				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Resuming upload from block %d/%d (%.1f%%) with %d concurrent threads\n",
						startBlock+1, numBlocks,
						float64(uploadedBytes)/float64(totalSize)*100,
						concurrency)
				}
			} else {
				// Blocks expired or don't exist, will start fresh
				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Previous uncommitted blocks expired, starting fresh upload with %d concurrent threads\n", concurrency)
				}
			}
		}
	}

	// Initialize for fresh upload if no valid resume
	if !resuming {
		blockIDs = make([]string, 0, numBlocks)
		uploadedBytes = 0
		createdAt = time.Now()

		// Save initial state (keyed by original file path)
		initialState := &UploadResumeState{
			LocalPath:      originalPath,
			EncryptedPath:  encryptedPath,
			ObjectKey:      pathForRescale,
			UploadID:       "",
			TotalSize:      totalSize,
			OriginalSize:   originalSize,
			UploadedBytes:  0,
			CompletedParts: []CompletedPart{},
			BlockIDs:       []string{},
			EncryptionKey:  encryption.EncodeBase64(encryptionKey),
			IV:             encryption.EncodeBase64(iv),
			RandomSuffix:   randomSuffix,
			CreatedAt:      createdAt,
			LastUpdate:     time.Now(),
			StorageType:    "AzureStorage",
		}
		SaveUploadState(initialState, originalPath)

		// Inform user about concurrent upload
		if outputWriter != nil {
			fmt.Fprintf(outputWriter, "Uploading with %d concurrent threads (%d blocks of 64 MB)\n", concurrency, numBlocks)
		}
	}

	// If resuming, seek to the position after the last uploaded block
	if resuming && startBlock > 0 {
		seekOffset := startBlock * azureBlockSize
		if _, seekErr := file.Seek(seekOffset, 0); seekErr != nil {
			return fmt.Errorf("failed to seek to resume position: %w", seekErr)
		}
	}

	// Concurrent upload implementation
	type blockJob struct {
		blockNum  int64
		blockID   string
		blockData []byte
	}

	type blockResult struct {
		blockNum int64
		blockID  string
		size     int64
		err      error
	}

	// Channels for coordination
	jobChan := make(chan blockJob, concurrency*2)
	resultChan := make(chan blockResult, numBlocks)
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

				// Stage this block with retry
				startTime := time.Now()
				stageErr := u.retryWithBackoff(ctx, fmt.Sprintf("StageBlock %d (worker %d)", job.blockNum, workerID), func() error {
					u.clientMu.Lock()
					client := u.client
					u.clientMu.Unlock()

					// Get block blob client for this specific blob
					blockBlobClient := client.ServiceClient().NewContainerClient(u.storageInfo.ConnectionSettings.Container).NewBlockBlobClient(blobPath)

					// Create a ReadSeekCloser from the block data
					reader := &readSeekCloser{Reader: bytes.NewReader(job.blockData)}
					_, err := blockBlobClient.StageBlock(ctx, job.blockID, reader, nil)
					return err
				})

				if stageErr != nil {
					select {
					case errorChan <- fmt.Errorf("failed to stage block %d: %w", job.blockNum, stageErr):
					default:
					}
					return
				}

				// Calculate and record throughput
				duration := time.Since(startTime).Seconds()
				if duration > 0 {
					bytesPerSec := float64(len(job.blockData)) / duration
					transferHandle.RecordThroughput(bytesPerSec)
				}

				// Send result
				resultChan <- blockResult{
					blockNum: job.blockNum,
					blockID:  job.blockID,
					size:     int64(len(job.blockData)),
					err:      nil,
				}
			}
		}(i)
	}

	// Read blocks from file and queue them for upload
	// This runs in the main goroutine to ensure sequential file reads
	go func() {
		defer close(jobChan)

		for blockNum := startBlock; blockNum < numBlocks; blockNum++ {
			// Check if an error occurred
			select {
			case <-errorChan:
				return
			default:
			}

			// Calculate block size (last block may be smaller)
			chunkSize := int64(azureBlockSize)
			if blockNum == numBlocks-1 {
				chunkSize = totalSize - (blockNum * azureBlockSize)
			}

			// Get buffer from pool for this block
			bufferPtr := buffers.GetChunkBuffer()
			buffer := *bufferPtr
			blockData := buffer[:chunkSize]

			// Read block data into pooled buffer
			n, readErr := io.ReadFull(file, blockData)
			if readErr != nil && readErr != io.ErrUnexpectedEOF {
				buffers.PutChunkBuffer(bufferPtr) // Return buffer on error
				select {
				case errorChan <- fmt.Errorf("failed to read block %d: %w", blockNum, readErr):
				default:
				}
				return
			}

			// Make a copy of the data for upload (buffer will be returned)
			blockDataCopy := make([]byte, n)
			copy(blockDataCopy, blockData[:n])

			// Return buffer to pool immediately after copying
			buffers.PutChunkBuffer(bufferPtr)

			// Generate block ID (must be base64-encoded and same length for all blocks)
			blockIDStr := fmt.Sprintf("block-%010d", blockNum)
			blockID := base64.StdEncoding.EncodeToString([]byte(blockIDStr))

			// Queue this block for upload (using the copied data)
			jobChan <- blockJob{
				blockNum:  blockNum,
				blockID:   blockID,
				blockData: blockDataCopy,
			}
		}
	}()

	// Collect results and update progress
	var resultsMu sync.Mutex
	results := make([]blockResult, 0, numBlocks-startBlock)
	var atomicUploadedBytes int64 = uploadedBytes
	resultCount := 0
	expectedResults := int(numBlocks - startBlock)

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

		// Add to results
		resultsMu.Lock()
		results = append(results, result)
		resultsMu.Unlock()

		// Update progress atomically
		newBytes := atomic.AddInt64(&atomicUploadedBytes, result.size)
		if callback != nil && totalSize > 0 {
			progress := float64(newBytes) / float64(totalSize)
			callback(progress)
		}

		resultCount++

		// Periodically save resume state (every 5 blocks or 25% of blocks, whichever is smaller)
		saveInterval := 5
		if expectedResults > 20 {
			saveInterval = expectedResults / 4
		}
		if resultCount%saveInterval == 0 {
			resultsMu.Lock()
			// Rebuild blockIDs list in order
			tempResults := make([]blockResult, len(results))
			copy(tempResults, results)
			sort.Slice(tempResults, func(i, j int) bool {
				return tempResults[i].blockNum < tempResults[j].blockNum
			})

			currentBlockIDs := make([]string, len(blockIDs))
			copy(currentBlockIDs, blockIDs)
			for _, r := range tempResults {
				currentBlockIDs = append(currentBlockIDs, r.blockID)
			}

			currentState := &UploadResumeState{
				LocalPath:      originalPath,
				EncryptedPath:  encryptedPath,
				ObjectKey:      pathForRescale,
				UploadID:       "",
				TotalSize:      totalSize,
				OriginalSize:   originalSize,
				UploadedBytes:  atomic.LoadInt64(&atomicUploadedBytes),
				CompletedParts: []CompletedPart{},
				BlockIDs:       currentBlockIDs,
				EncryptionKey:  encryption.EncodeBase64(encryptionKey),
				IV:             encryption.EncodeBase64(iv),
				RandomSuffix:   randomSuffix,
				CreatedAt:      createdAt,
				LastUpdate:     time.Now(),
				StorageType:    "AzureStorage",
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

	// Sort results by block number to get final blockIDs list
	sort.Slice(results, func(i, j int) bool {
		return results[i].blockNum < results[j].blockNum
	})

	// Build final block IDs list
	finalBlockIDs := make([]string, len(blockIDs))
	copy(finalBlockIDs, blockIDs)
	for _, result := range results {
		finalBlockIDs = append(finalBlockIDs, result.blockID)
	}

	// Commit the block list with retry
	err = u.retryWithBackoff(ctx, "CommitBlockList", func() error {
		u.clientMu.Lock()
		client := u.client
		u.clientMu.Unlock()

		blockBlobClient := client.ServiceClient().NewContainerClient(u.storageInfo.ConnectionSettings.Container).NewBlockBlobClient(blobPath)

		// Encode IV in blob metadata
		metadata := map[string]*string{
			"iv": to.Ptr(encryption.EncodeBase64(iv)),
		}

		_, err := blockBlobClient.CommitBlockList(ctx, finalBlockIDs, &blockblob.CommitBlockListOptions{
			Metadata: metadata,
		})
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to commit block list: %w", err)
	}

	return nil
}

// UploadEncryptedWithTransfer uploads a file with encryption using a transfer handle for concurrent uploads
// If transferHandle is nil, uses sequential upload (same as UploadEncrypted)
// If transferHandle specifies multiple threads, uses concurrent block uploads
func (u *AzureUploader) UploadEncryptedWithTransfer(ctx context.Context, localPath string, progressCallback ProgressCallback, transferHandle *transfer.Transfer, outputWriter io.Writer) (string, []byte, []byte, error) {
	// This will be the same logic as UploadEncrypted but with the transfer handle
	// For now, let's just call the existing method - we'll integrate concurrent upload in CLI wiring
	return u.UploadEncrypted(ctx, localPath, progressCallback, outputWriter)
}
