// Package transfer provides unified upload and download orchestration.
// This file holds the concurrent part-staging pipeline shared by the providers'
// pre-encrypted multipart uploads.
package transfer

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"

	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/util/buffers"
)

// PartAssignment is one part handed to a staging worker. Index is zero-based;
// backends whose part numbers start at one add the offset themselves.
type PartAssignment struct {
	Index    int64
	Data     []byte
	WorkerID int
}

// PartPipelineConfig describes one concurrent part-staging run. The caller owns
// everything outside the pipeline: opening and positioning the reader, creating
// the backend's upload, holding the resume lock, and verifying and committing
// the part list once the pipeline returns.
type PartPipelineConfig struct {
	// Reader supplies the part data, already positioned at the first part this
	// run is to stage.
	Reader io.Reader

	// PartSize is the size the plan chose for every part but the last. The
	// producer reads exactly this much per part, so a short read means the end
	// of the file rather than the end of the buffer.
	PartSize int64

	// TotalParts is the part count the whole file should take, and StartPart is
	// the zero-based index of the first part this run stages (non-zero when
	// resuming). UploadedBytes is what earlier runs already staged.
	TotalParts    int64
	StartPart     int64
	UploadedBytes int64

	// Concurrency is the number of staging workers to run and QueueDepth is how
	// many read parts may wait ahead of them. Both come from the caller's
	// upload plan, which bounds how many part-sized buffers exist at once.
	Concurrency int
	QueueDepth  int

	// WorkerLabel names the workers in the panic log line.
	WorkerLabel string

	// StagePart sends one part to the backend and returns the tag that
	// identifies it in the final commit (an S3 ETag, an Azure block ID). Its
	// context is already bounded by constants.PartOperationTimeout. Errors are
	// returned to the caller as-is, so StagePart owns their wording.
	StagePart func(ctx context.Context, part PartAssignment) (string, error)

	// RecordPart files a staged part's tag with the caller. Called from the
	// result collector under the pipeline's lock, so implementations may write
	// straight into caller state.
	RecordPart func(index int64, tag string)

	// OnProgress, when set, is called with the running total after each staged
	// part. Backends that report progress elsewhere leave it nil.
	OnProgress func(uploadedBytes int64)

	// SaveState persists resume state periodically. staged is the number of
	// parts this run has staged so far. Called under the same lock as
	// RecordPart.
	SaveState func(uploadedBytes int64, staged int)
}

// RunPartPipeline reads the file one part at a time and stages the parts through
// a pool of workers, returning the total uploaded byte count including whatever
// UploadedBytes the caller resumed from.
//
// The producer stops on the first short read, so a mis-sized buffer or a
// truncated file ends with fewer parts than the file needs. That is why the
// count is returned rather than assumed: the caller has to verify it covers the
// whole file before committing the part list.
func RunPartPipeline(ctx context.Context, cfg PartPipelineConfig) (int64, error) {
	type partJob struct {
		index int64
		data  []byte
	}

	type partResult struct {
		index int64
		tag   string
		size  int64
		err   error
	}

	// Channels for coordination
	jobChan := make(chan partJob, cfg.QueueDepth)
	resultChan := make(chan partResult, int(cfg.TotalParts))

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
	for i := 0; i < cfg.Concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					setError(fmt.Errorf("worker %d panicked: %v", workerID, r))
					log.Printf("PANIC in %s %d: %v", cfg.WorkerLabel, workerID, r)
				}
			}()

			for job := range jobChan {
				// Check if context was cancelled
				select {
				case <-opCtx.Done():
					return
				default:
				}

				// Create context with timeout for this specific part
				partCtx, cancel := context.WithTimeout(opCtx, constants.PartOperationTimeout)

				// Stage this part with retry logic
				tag, stageErr := cfg.StagePart(partCtx, PartAssignment{
					Index:    job.index,
					Data:     job.data,
					WorkerID: workerID,
				})

				cancel()

				if stageErr != nil {
					setError(stageErr)
					return
				}

				// Send result
				resultChan <- partResult{
					index: job.index,
					tag:   tag,
					size:  int64(len(job.data)),
					err:   nil,
				}
			}
		}(i)
	}

	// Read parts from file and queue them for upload
	go func() {
		defer close(jobChan)

		// One buffer for the whole producer, sized to the part size the plan
		// chose. A short read can then only mean the end of the file, which is
		// what the loop below treats it as.
		buffer, releaseBuffer := buffers.GetPartBuffer(cfg.PartSize)
		defer releaseBuffer()

		partIndex := cfg.StartPart
		for {
			select {
			case <-opCtx.Done():
				return
			default:
			}

			n, readErr := io.ReadFull(cfg.Reader, buffer)

			if readErr == io.EOF {
				break
			}

			// Get the actual data slice and make a copy
			var partData []byte
			if readErr == io.ErrUnexpectedEOF {
				partData = make([]byte, n)
				copy(partData, buffer[:n])
				readErr = nil
			} else if readErr != nil {
				setError(fmt.Errorf("failed to read file chunk: %w", readErr))
				return
			} else {
				partData = make([]byte, n)
				copy(partData, buffer[:n])
			}

			// Queue this part for upload
			jobChan <- partJob{
				index: partIndex,
				data:  partData,
			}

			partIndex++

			if int64(len(partData)) < cfg.PartSize {
				break
			}
		}
	}()

	// Collect results and update progress
	var resultsMu sync.Mutex
	var atomicUploadedBytes int64 = cfg.UploadedBytes
	resultCount := 0
	expectedResults := int(cfg.TotalParts - cfg.StartPart)

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

		// Record the staged part with the caller
		resultsMu.Lock()
		cfg.RecordPart(result.index, result.tag)
		resultsMu.Unlock()

		// Update progress atomically
		atomic.AddInt64(&atomicUploadedBytes, result.size)

		// Update progress callback
		if cfg.OnProgress != nil {
			cfg.OnProgress(atomic.LoadInt64(&atomicUploadedBytes))
		}

		resultCount++

		// Periodically save resume state
		saveInterval := 5
		if expectedResults > 20 {
			saveInterval = expectedResults / 4
		}
		if resultCount%saveInterval == 0 {
			resultsMu.Lock()
			cfg.SaveState(atomic.LoadInt64(&atomicUploadedBytes), resultCount)
			resultsMu.Unlock()
		}
	}

	// Check for errors
	errorMu.Lock()
	if firstError != nil {
		err := firstError
		errorMu.Unlock()
		return atomic.LoadInt64(&atomicUploadedBytes), err
	}
	errorMu.Unlock()

	return atomic.LoadInt64(&atomicUploadedBytes), nil
}
