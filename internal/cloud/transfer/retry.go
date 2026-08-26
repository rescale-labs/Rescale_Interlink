package transfer

// This file holds the retry and range-fetch machinery the S3 and Azure
// providers share. Each provider keeps a thin wrapper that supplies its own
// client call; everything else — retry policy, per-attempt timeout, progress
// accounting and rollback, error wording — lives here so the two backends
// cannot drift apart.

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/http"
)

// Retrier runs fn under a provider's retry/backoff policy, labelling the
// attempts with operation. Provider clients satisfy this with their
// RetryWithBackoff method.
type Retrier func(ctx context.Context, operation string, fn func() error) error

// RetryWithBackoff executes fn with exponential backoff. refresh is called
// between attempts to renew storage credentials, and every retry is reported
// through observer.
//
// Retries are reported, not hidden behind an env var: a silent retry loop is
// what made a broken storage endpoint look like a hang.
func RetryWithBackoff(ctx context.Context, operation string, observer cloud.RetryObserver, refresh func(context.Context) error, fn func() error) error {
	retryConfig := http.Config{
		MaxRetries:        constants.MaxRetries,
		InitialDelay:      constants.RetryInitialDelay,
		MaxDelay:          constants.RetryMaxDelay,
		MaxElapsed:        constants.RetryMaxElapsed,
		CredentialRefresh: refresh,
		OnRetry: func(attempt int, err error, errorType http.ErrorType, nextDelay time.Duration) {
			observer.Notify(cloud.RetryEvent{
				Operation:   operation,
				Attempt:     attempt,
				MaxAttempts: constants.MaxRetries,
				Cause:       http.ErrorTypeName(errorType),
				Err:         err,
				NextDelay:   nextDelay,
			})
		},
	}

	return http.ExecuteWithRetry(ctx, retryConfig, fn)
}

// OpenRange opens one byte range on the remote object. Implementations must use
// their provider's non-retrying call: the caller owns the retry loop, and a
// nested one would multiply the attempt count.
type OpenRange func(ctx context.Context, offset, length int64) (io.ReadCloser, error)

// FetchRangeWithRetry downloads [offset, offset+length) into memory, wrapping
// request, read and close in a single retry so a mid-transfer proxy failure
// restarts the whole range instead of returning a short read.
//
// progressCallback (optional) receives byte deltas as they arrive. Bytes
// reported by a failed attempt are rolled back with a negative delta so the
// retry can report them again without double-counting.
func FetchRangeWithRetry(ctx context.Context, retry Retrier, offset, length int64, progressCallback func(int64), open OpenRange) ([]byte, error) {
	data, err := fetchRange(ctx, retry, fmt.Sprintf("DownloadRange [%d-%d]", offset, offset+length),
		offset, length, progressCallback, open)
	if err != nil {
		return nil, fmt.Errorf("failed to download range [%d-%d]: %w", offset, offset+length-1, err)
	}

	return data, nil
}

// fetchRange is FetchRangeWithRetry with the retry label and the error wording
// left to the caller, for the shared download drivers that name their unit of
// work differently ("DownloadPart 3", "DownloadChunk 7").
//
// Every caller asks for a range that lies inside the object, so a body shorter
// than length means the transfer was cut short. That is the one failure a range
// read cannot report on its own: the caller would otherwise write a short part
// and only discover it at the checksum, or never on a file that has none. The
// wording names EOF deliberately — the retry classifier reads it as a network
// failure, which is what a body that stops early is, so the range is retried.
func fetchRange(ctx context.Context, retry Retrier, operation string, offset, length int64, progressCallback func(int64), open OpenRange) ([]byte, error) {
	var data []byte
	var attemptBytes int64 // Track bytes reported in current attempt
	err := retry(ctx, operation, func() error {
		// Per-attempt timeout to prevent stalled reads from hanging
		attemptCtx, cancel := context.WithTimeout(ctx, constants.PartOperationTimeout)
		defer cancel()

		attemptBytes = 0

		body, err := open(attemptCtx, offset, length)
		if err != nil {
			return err
		}

		var reader io.Reader = body
		if progressCallback != nil {
			reader = &ProgressReader{
				Reader: body,
				Callback: func(n int64) {
					attemptBytes += n
					progressCallback(n)
				},
				Threshold: ProgressReaderThreshold,
			}
		}

		readData, readErr := io.ReadAll(reader)
		body.Close() // Always close, even on read error
		if readErr == nil && int64(len(readData)) != length {
			readErr = fmt.Errorf("short range read at offset %d: EOF after %d of %d bytes",
				offset, len(readData), length)
		}
		if readErr != nil {
			if progressCallback != nil && attemptBytes > 0 {
				progressCallback(-attemptBytes)
			}
			return readErr
		}
		data = readData
		return nil
	})
	if err != nil {
		return nil, err
	}

	return data, nil
}

// startCredentialRefresh renews storage credentials on a timer for as long as a
// transfer runs, and returns the call that stops it. A multi-gigabyte transfer
// outlives the token it started with, and without this the expiry only surfaces
// as a failed request part way through.
//
// A failed refresh is logged, not fatal: the retry path refreshes again when a
// request actually fails, so a missed tick is recoverable.
func startCredentialRefresh(ctx context.Context, refresh func(context.Context) error) func() {
	if refresh == nil {
		return func() {}
	}

	refreshCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(constants.PeriodicCredentialRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := refresh(refreshCtx); err != nil {
					log.Printf("[REFRESH] Periodic credential refresh failed: %v", err)
				}
			case <-refreshCtx.Done():
				return
			}
		}
	}()

	return func() {
		cancel()
		wg.Wait()
	}
}

// WriteBodyWithProgress streams body into a new file at localPath, reporting
// fractional progress at most once per 1% of totalSize. The file is synced and
// closed before returning so the caller can checksum it immediately: without
// the sync, the OS buffer may not be flushed and checksums fail sporadically.
//
// With no callback or no known size it copies straight through.
func WriteBodyWithProgress(body io.Reader, localPath string, progressCallback func(float64), totalSize int64) error {
	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	// Track whether we successfully closed the file to avoid double-close from defer
	fileClosed := false
	defer func() {
		if !fileClosed {
			_ = file.Close()
		}
	}()

	if progressCallback == nil || totalSize <= 0 {
		if _, err := io.Copy(file, body); err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}
		return syncAndClose(file, &fileClosed)
	}

	var downloaded int64
	buffer := make([]byte, 32*1024)
	lastProgress := 0.0

	for {
		n, readErr := body.Read(buffer)
		if n > 0 {
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				return fmt.Errorf("failed to write file: %w", writeErr)
			}
			downloaded += int64(n)

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

	return syncAndClose(file, &fileClosed)
}

// DownloadChunkedToFile writes [0, totalSize) to localPath one constants.ChunkSize
// chunk at a time, retrying each chunk through retry and reporting fractional
// progress after every completed chunk. Chunks are written outside the retry:
// disk errors are not retryable.
func DownloadChunkedToFile(ctx context.Context, retry Retrier, localPath string, totalSize int64, progressCallback func(float64), open OpenRange) error {
	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	// Track whether we successfully closed the file to avoid double-close from defer
	fileClosed := false
	defer func() {
		if !fileClosed {
			_ = file.Close()
		}
	}()

	chunkSize := int64(constants.ChunkSize)
	var offset int64 = 0

	for offset < totalSize {
		currentChunkSize := chunkSize
		if offset+currentChunkSize > totalSize {
			currentChunkSize = totalSize - offset
		}

		chunkData, err := fetchRange(ctx, retry, fmt.Sprintf("DownloadChunk offset=%d", offset),
			offset, currentChunkSize, nil, open)
		if err != nil {
			return fmt.Errorf("failed to download chunk at offset %d: %w", offset, err)
		}

		if _, err := file.Write(chunkData); err != nil {
			return fmt.Errorf("failed to write chunk at offset %d: %w", offset, err)
		}

		offset += int64(len(chunkData))

		if progressCallback != nil && totalSize > 0 {
			progressCallback(float64(offset) / float64(totalSize))
		}
	}

	return syncAndClose(file, &fileClosed)
}

// syncAndClose flushes the file to disk and releases the handle before the
// caller reads it back for checksum verification, marking closed so the
// caller's defer does not close it a second time.
func syncAndClose(file *os.File, closed *bool) error {
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file to disk: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close file: %w", err)
	}
	*closed = true
	return nil
}
