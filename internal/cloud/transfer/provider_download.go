package transfer

// This file holds the two download drivers the S3 and Azure providers share: the
// HKDF (v1) streaming download and the concurrent chunked download of a legacy
// (v0) object. Each provider keeps a thin wrapper that supplies only the calls
// its SDK owns — fetch metadata, refresh credentials, open one byte range — and
// everything else lives here so the two backends cannot drift apart.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/diskspace"
)

// chunkProgressInterval is how often a concurrent chunked download reports
// progress. Chunks are tens of megabytes, so reporting per completed chunk reads
// as a series of jumps; a timer over the running byte count reads as motion.
const chunkProgressInterval = 300 * time.Millisecond

// ErrObjectReplaced reports that the object changed under a ranged download, so
// the ranges fetched so far and the ones still to come no longer describe one
// object.
//
// The sentence carries no digits on purpose. The retry classifier reads error
// text, an ETag is hexadecimal, and a "500" or a "429" anywhere inside one
// would have this misread as a retryable server error and retried until the
// budget ran out. Which offset and which versions were involved goes to the log
// instead; the caller wraps this with the chunk it was fetching.
var ErrObjectReplaced = errors.New("the object was replaced during the download, so its ranges no longer come from one version")

// OpenRangeVersioned opens one byte range and reports the version of the object
// the bytes came from — the S3 or Azure ETag, empty when the backend reported
// none. Implementations use their provider's non-retrying call, as OpenRange
// does: the caller owns the retry loop.
type OpenRangeVersioned func(ctx context.Context, offset, length int64) (io.ReadCloser, string, error)

// PinObjectVersion pins a ranged download to one version of the object, so a
// file stitched together from many range requests cannot end up holding bytes
// from two different objects.
//
// A download reads its object many times over, for as long as the transfer
// takes. Replace the object in between with a body of the same length and every
// range fetched after the replacement comes from the new one: the file that
// lands is a mixture of the two. Resume-time ETag validation does not see this
// — it compares versions across attempts, not within one — and with compatible
// encryption metadata and valid final padding nothing afterwards notices
// either, because the size is right and a checksum, the one check that would
// catch it, is not attached to every file.
//
// The version each range reports is compared against the pinned one rather than
// sent as an If-Match condition. Intercepting proxies (see the open ITAR issue)
// can mangle a conditional header into a spurious 412, which would fail
// downloads that are perfectly fine; an ETag that comes back different is
// unambiguous and costs nothing on the wire. version may be empty when the
// metadata request reported none, in which case the first range that does
// report one sets the pin.
//
// Once a version is pinned, a range that reports none is refused just as one
// reporting a different version is: missing evidence is not evidence of
// sameness, and a proxy that drops the header from the one response that
// matters would otherwise bypass the comparison entirely. Only a download that
// has never seen a version at all runs unpinned, which is where a backend that
// reports none leaves it — with one warning, so a whole class of downloads
// going unpinned is visible.
func PinObjectVersion(open OpenRangeVersioned, version string) OpenRange {
	var mu sync.Mutex
	var warnOnce sync.Once
	pinned := version

	return func(ctx context.Context, offset, length int64) (io.ReadCloser, error) {
		body, reported, err := open(ctx, offset, length)
		if err != nil {
			return nil, err
		}

		mu.Lock()
		if pinned == "" && reported != "" {
			pinned = reported
		}
		want := pinned
		mu.Unlock()

		if want == "" {
			warnOnce.Do(func() {
				log.Printf("[DOWNLOAD] the backend reports no object version, so this download cannot be pinned to one")
			})
			return body, nil
		}

		if reported != want {
			body.Close()
			if reported == "" {
				log.Printf("[DOWNLOAD] the range at offset %d carried no object version, and the download is pinned to %s", offset, want)
				return nil, fmt.Errorf("the range carried no object version: %w", ErrObjectReplaced)
			}
			log.Printf("[DOWNLOAD] object version changed at offset %d: got %s, pinned to %s", offset, reported, want)
			return nil, fmt.Errorf("the range carried a different object version: %w", ErrObjectReplaced)
		}
		return body, nil
	}
}

// HKDFStreamParams is what the shared HKDF (v1) download driver needs from a
// provider.
type HKDFStreamParams struct {
	// LocalPath is where the plaintext is written.
	LocalPath string

	// MasterKey is the file's key; each part's key derives from it.
	MasterKey []byte

	// Retry runs one unit of work under the provider's retry policy.
	Retry Retrier

	// Refresh renews storage credentials. The driver calls it once before the
	// transfer and then on a timer for as long as the transfer runs.
	Refresh func(context.Context) error

	// Stat reports the encrypted object's size and its metadata with the keys
	// already lowercased (see NormalizeMetadata). The provider owns the wording
	// of the error, since only it knows what it asked for.
	Stat func(context.Context) (int64, map[string]string, error)

	// Open opens one byte range without retrying: the driver owns the retry loop,
	// and a nested one would multiply the attempt count.
	Open OpenRange

	ProgressCallback cloud.ProgressCallback
}

// DownloadHKDFStream downloads an object written in the HKDF streaming format
// (v1) and writes the plaintext straight to LocalPath, one part at a time. Each
// part carries a key and IV derived from the master key and the file ID, so
// parts decrypt independently and no encrypted temp file is needed.
//
// Only clients older than v3.2.0 wrote v1; this path exists so those files stay
// downloadable.
func DownloadHKDFStream(ctx context.Context, params HKDFStreamParams) error {
	if params.ProgressCallback != nil {
		params.ProgressCallback(0.0)
	}

	if err := params.Refresh(ctx); err != nil {
		return fmt.Errorf("failed to refresh credentials: %w", err)
	}

	encryptedSize, metadata, err := params.Stat(ctx)
	if err != nil {
		return err
	}

	encodedFileID, partSize, err := hkdfPartKeying(metadata)
	if err != nil {
		return err
	}
	fileID, err := encryption.DecodeBase64(encodedFileID)
	if err != nil {
		return fmt.Errorf("failed to decode fileId: %w", err)
	}

	decryptor, err := encryption.NewStreamingDecryptor(params.MasterKey, fileID, partSize)
	if err != nil {
		return fmt.Errorf("failed to create streaming decryptor: %w", err)
	}

	// Each encrypted part is its plaintext part plus PKCS7 padding.
	encryptedPartSize := encryption.CalculateEncryptedPartSize(partSize)
	numParts := (encryptedSize + encryptedPartSize - 1) / encryptedPartSize

	// Check the disk before writing anything. Return the error verbatim: it
	// already reports the margined requirement this check enforced and the free
	// space on LocalPath's own filesystem.
	estimatedPlaintextSize := encryptedSize - (numParts * 16) // 1-16 bytes of padding per part
	if err := diskspace.CheckAvailableSpace(params.LocalPath, estimatedPlaintextSize, 1+constants.DiskSpaceBufferPercent); err != nil {
		return err
	}

	stopRefresh := startCredentialRefresh(ctx, params.Refresh)
	defer stopRefresh()

	// Plaintext output, no temp file: parts decrypt in order straight into it.
	outFile, err := os.Create(params.LocalPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	var downloadedBytes int64
	for partIndex := int64(0); partIndex < numParts; partIndex++ {
		startByte := partIndex * encryptedPartSize
		length := encryptedPartSize
		if startByte+length > encryptedSize {
			length = encryptedSize - startByte
		}

		ciphertext, err := fetchRange(ctx, params.Retry, fmt.Sprintf("DownloadPart %d", partIndex),
			startByte, length, nil, params.Open)
		if err != nil {
			return fmt.Errorf("failed to download part %d: %w", partIndex, err)
		}

		plaintext, err := decryptor.DecryptPart(partIndex, ciphertext)
		if err != nil {
			return fmt.Errorf("failed to decrypt part %d: %w", partIndex, err)
		}

		// Written outside the retry: disk errors are not retryable.
		if _, err := outFile.Write(plaintext); err != nil {
			return fmt.Errorf("failed to write part %d: %w", partIndex, err)
		}

		downloadedBytes += int64(len(ciphertext))
		if params.ProgressCallback != nil && encryptedSize > 0 {
			params.ProgressCallback(float64(downloadedBytes) / float64(encryptedSize))
		}
	}

	if params.ProgressCallback != nil {
		params.ProgressCallback(1.0)
	}

	return nil
}

// ChunkedConcurrentParams is what the shared concurrent chunked-download driver
// needs from a provider.
type ChunkedConcurrentParams struct {
	// RemotePath is recorded in the resume state so a resumed download can be
	// told apart from one of a different object at the same local path.
	RemotePath string

	// LocalPath is the file the chunks are written into.
	LocalPath string

	// TotalSize is the object's size, which is also the file's final size.
	TotalSize int64

	// ChunkSize is the size of one range request. Zero means
	// constants.ChunkSize, which is what both providers download at.
	ChunkSize int64

	// Concurrency is how many chunks may be in flight at once.
	Concurrency int

	// StorageType is recorded in the resume state: "S3Storage" or "AzureStorage".
	StorageType string

	// ObjectETag pins the object for resume validation: a resume state that
	// names a different ETag is discarded, so a resumed download can never mix
	// two versions of an object. Range requests within one attempt are pinned
	// separately, by the provider's Open (see PinObjectVersion) — still without
	// sending If-Match, because of the proxy ETag-mangling risk. Empty disables
	// the resume validation.
	ObjectETag string

	// Retry runs one chunk fetch under the provider's retry policy.
	Retry Retrier

	// Open opens one byte range without retrying: the driver owns the retry loop.
	Open OpenRange

	ProgressCallback func(float64)

	// openSink opens the file the chunks are written into. Nil, as every caller
	// leaves it, means the real file at LocalPath. It exists as a seam for the
	// package's own tests: the order in which a chunk's bytes are made durable
	// and the sidecar claims them is the one property of this driver that the
	// finished file cannot show.
	openSink func(localPath string) (chunkSink, error)
}

// chunkSink is the file a chunked download writes into. Real downloads use an
// *os.File.
type chunkSink interface {
	WriteAt(p []byte, off int64) (int, error)
	Sync() error
	Truncate(size int64) error
	Close() error
}

// DownloadChunkedConcurrent writes [0, TotalSize) to LocalPath with several
// chunks in flight at once, resuming from a previous attempt's completed chunks
// when the object still matches.
//
// Each worker writes its own chunk at its final offset and only then records it
// as complete. The order matters: the resume state is a claim about what is on
// disk, so recording a chunk before writing it leaves a hole in the file that
// the next attempt skips and no later check would notice.
func DownloadChunkedConcurrent(ctx context.Context, params ChunkedConcurrentParams) error {
	if params.ProgressCallback != nil {
		params.ProgressCallback(0.0)
	}

	chunkSize := params.ChunkSize
	if chunkSize <= 0 {
		chunkSize = int64(constants.ChunkSize)
	}
	totalChunks := (params.TotalSize + chunkSize - 1) / chunkSize

	resumeState := resumeChunkedDownload(params, chunkSize, totalChunks)
	chunksToDownload := resumeState.GetMissingChunks(totalChunks)

	if len(chunksToDownload) == 0 {
		// The state claims every chunk landed. Trust it only if the file is the
		// size it should be: a truncated file under a complete state would
		// otherwise be reported as a finished download.
		fileInfo, err := os.Stat(params.LocalPath)
		if err != nil {
			return fmt.Errorf("failed to stat file: %w", err)
		}
		if fileInfo.Size() == params.TotalSize {
			fmt.Printf("Download already complete (verified), skipping\n")
			if params.ProgressCallback != nil {
				params.ProgressCallback(1.0)
			}
			return nil
		}

		fmt.Printf("Warning: Resume state claims complete but file size mismatch\n")
		state.DeleteDownloadState(params.LocalPath)
		resumeState = newChunkedResumeState(params, chunkSize)
		chunksToDownload = resumeState.GetMissingChunks(totalChunks)
	}

	openSink := params.openSink
	if openSink == nil {
		openSink = func(localPath string) (chunkSink, error) {
			return os.OpenFile(localPath, os.O_CREATE|os.O_RDWR, 0644)
		}
	}
	file, err := openSink(params.LocalPath)
	if err != nil {
		return fmt.Errorf("failed to create/open file: %w", err)
	}
	// Track whether we successfully closed the file to avoid double-close from defer
	fileClosed := false
	defer func() {
		if !fileClosed {
			_ = file.Close()
		}
	}()

	// Size the file up front: workers write each chunk at its final offset, and a
	// resumed download must not keep a tail left by a longer previous object.
	if err := file.Truncate(params.TotalSize); err != nil {
		return fmt.Errorf("failed to truncate file: %w", err)
	}

	concurrency := params.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(chunksToDownload) {
		concurrency = len(chunksToDownload)
	}

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

	jobChan := make(chan int64, len(chunksToDownload))
	for _, chunkIndex := range chunksToDownload {
		jobChan <- chunkIndex
	}
	close(jobChan)

	downloadedBytes := resumeState.DownloadedBytes
	var fileMu sync.Mutex
	var stateMu sync.Mutex

	progressDone := make(chan struct{})
	defer close(progressDone)
	go func() {
		ticker := time.NewTicker(chunkProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if params.ProgressCallback != nil && params.TotalSize > 0 {
					current := atomic.LoadInt64(&downloadedBytes)
					if current > 0 {
						params.ProgressCallback(float64(current) / float64(params.TotalSize))
					}
				}
			case <-progressDone:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					setError(fmt.Errorf("worker %d panicked: %v", workerID, r))
					log.Printf("PANIC in download worker %d: %v", workerID, r)
				}
			}()

			for chunkIndex := range jobChan {
				select {
				case <-opCtx.Done():
					return
				default:
				}

				offset := chunkIndex * chunkSize
				length := chunkSize
				if offset+length > params.TotalSize {
					length = params.TotalSize - offset
				}

				data, err := fetchRange(opCtx, params.Retry, fmt.Sprintf("DownloadChunk %d", chunkIndex),
					offset, length, nil, params.Open)
				if err != nil {
					setError(fmt.Errorf("failed to download chunk %d at offset %d: %w", chunkIndex, offset, err))
					return
				}

				// Written outside the retry: disk errors are not retryable.
				fileMu.Lock()
				_, err = file.WriteAt(data, offset)
				fileMu.Unlock()
				if err != nil {
					setError(fmt.Errorf("failed to write chunk %d: %w", chunkIndex, err))
					return
				}

				atomic.AddInt64(&downloadedBytes, int64(len(data)))

				// Flushed before it is claimed. The sidecar is a claim about
				// what is recoverable from the file, and a write that is only
				// in the page cache is not: a crash between the two persists
				// the claim without the bytes, and the next attempt skips
				// exactly that chunk. The up-front Truncate then supplies zeros
				// in its place, so nothing downstream sees a short file — a
				// hole only a checksum could catch, which files without one do
				// not carry.
				if syncErr := file.Sync(); syncErr != nil {
					setError(fmt.Errorf("failed to flush chunk %d to disk: %w", chunkIndex, syncErr))
					return
				}

				// Recorded only now that the bytes are durable.
				stateMu.Lock()
				resumeState.MarkChunkCompleted(chunkIndex, int64(len(data)))
				_ = state.SaveDownloadState(resumeState, params.LocalPath)
				stateMu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	errorMu.Lock()
	failure := firstError
	errorMu.Unlock()

	// A worker that sees a cancelled context returns without recording an error,
	// so an empty firstError is not the same as a finished download. Both of the
	// checks below therefore stand between the workers and the completion path:
	// without them a cancellation between two range requests was reported as
	// 100%, the resume state was deleted, and a file whose missing chunks are
	// still holes became what every downstream presence check calls complete.
	if failure == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			failure = fmt.Errorf("download cancelled after %d of %d chunks: %w",
				len(resumeState.CompletedChunks), totalChunks, ctxErr)
		} else if completed := int64(len(resumeState.CompletedChunks)); completed < totalChunks {
			failure = fmt.Errorf("incomplete download: %d of %d chunks were written", completed, totalChunks)
		}
	}

	if failure != nil {
		// Keep the resume state: the chunks already written are still good.
		stateMu.Lock()
		_ = state.SaveDownloadState(resumeState, params.LocalPath)
		stateMu.Unlock()
		return failure
	}

	// Sync before returning so all data is on disk before the caller checksums
	// the file. Without this, sporadic checksum failures occur because the OS
	// buffer may not be flushed yet.
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file to disk: %w", err)
	}

	// Explicit Close() before returning so the file handle is released before the
	// caller reads the file for checksum verification.
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close file: %w", err)
	}
	fileClosed = true

	state.DeleteDownloadState(params.LocalPath)

	if params.ProgressCallback != nil {
		params.ProgressCallback(1.0)
	}

	return nil
}

// resumeChunkedDownload returns the state to continue from, discarding a stored
// one that no longer describes this download.
func resumeChunkedDownload(params ChunkedConcurrentParams, chunkSize, totalChunks int64) *state.DownloadResumeState {
	existing, _ := state.LoadDownloadState(params.LocalPath)
	if existing == nil {
		return newChunkedResumeState(params, chunkSize)
	}

	if chunkedResumeUsable(existing, params, chunkSize) {
		fmt.Printf("Resuming concurrent download: %d/%d chunks already completed\n",
			len(existing.CompletedChunks), totalChunks)
		return existing
	}

	// The partial file cannot be resumed from, so it goes too: leaving it behind
	// only invites a later size check to mistake it for a finished download.
	state.CleanupExpiredDownloadResume(existing, params.LocalPath, true)
	return newChunkedResumeState(params, chunkSize)
}

// chunkedResumeUsable reports whether a stored state still describes the bytes on
// disk. The chunks it claims are only meaningful if the object, its size and the
// chunking all match what this download is about to do.
func chunkedResumeUsable(existing *state.DownloadResumeState, params ChunkedConcurrentParams, chunkSize int64) bool {
	if existing.ChunkSize != chunkSize || existing.TotalSize != params.TotalSize {
		return false
	}
	// An ETag we could not read pins nothing; one that changed means the object
	// was replaced and the bytes on disk belong to the version that is gone.
	if params.ObjectETag != "" && existing.ETag != params.ObjectETag {
		return false
	}
	return state.ValidateDownloadState(existing, params.LocalPath) == nil
}

func newChunkedResumeState(params ChunkedConcurrentParams, chunkSize int64) *state.DownloadResumeState {
	now := time.Now()
	return &state.DownloadResumeState{
		LocalPath:       params.LocalPath,
		EncryptedPath:   params.LocalPath,
		RemotePath:      params.RemotePath,
		TotalSize:       params.TotalSize,
		DownloadedBytes: 0,
		ETag:            params.ObjectETag,
		CreatedAt:       now,
		LastUpdate:      now,
		StorageType:     params.StorageType,
		ChunkSize:       chunkSize,
		CompletedChunks: make([]int64, 0),
	}
}
