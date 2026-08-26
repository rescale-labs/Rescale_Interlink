package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/cloud/state"
)

// passThroughRetry runs the operation once. The retry policy is tested where it
// lives; these tests are about what the driver does with the bytes.
func passThroughRetry(_ context.Context, _ string, fn func() error) error {
	return fn()
}

// rangeServer answers range requests out of a fixed object, and records enough
// about the requests to check how the driver paced them.
type rangeServer struct {
	object []byte

	// failAt, when set, fails every request whose offset is in the map, so a
	// download can be stopped part way through on purpose.
	failAt map[int64]bool

	mu           sync.Mutex
	inFlight     int
	maxInFlight  int
	requestCount int
}

func (s *rangeServer) open(_ context.Context, offset, length int64) (io.ReadCloser, error) {
	s.mu.Lock()
	s.inFlight++
	s.requestCount++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	shouldFail := s.failAt[offset]
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()

	if shouldFail {
		return nil, fmt.Errorf("range at %d is unavailable", offset)
	}
	return io.NopCloser(bytes.NewReader(s.object[offset : offset+length])), nil
}

func (s *rangeServer) peakInFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInFlight
}

func objectOfSize(size int) []byte {
	object := make([]byte, size)
	for i := range object {
		object[i] = byte(i%251 + 1) // non-zero, so an unwritten hole is visible
	}
	return object
}

// TestDownloadChunkedConcurrentKeepsResumeStateHonest is the regression test for
// the crash window the S3 path used to leave open. A chunk was recorded as
// completed while its bytes were still in a slice waiting for the collect loop to
// finish, so an interrupted download left a resume state claiming chunks that
// were never on disk — and the next attempt skipped them, leaving holes that no
// later check would notice on a file without a checksum.
//
// The download here is stopped at its second chunk. Every chunk the resume state
// claims must be readable from the file.
func TestDownloadChunkedConcurrentKeepsResumeStateHonest(t *testing.T) {
	const chunkSize = 8
	object := objectOfSize(20) // chunks of 8, 8, 4
	localPath := filepath.Join(t.TempDir(), "results.dat")

	server := &rangeServer{object: object, failAt: map[int64]bool{8: true}}

	err := DownloadChunkedConcurrent(context.Background(), ChunkedConcurrentParams{
		RemotePath:  "bucket/results.dat",
		LocalPath:   localPath,
		TotalSize:   int64(len(object)),
		ChunkSize:   chunkSize,
		Concurrency: 1, // one worker, so chunk 1 fails only after chunk 0 landed
		StorageType: "S3Storage",
		ObjectETag:  `"etag-1"`,
		Retry:       passThroughRetry,
		Open:        server.open,
	})
	if err == nil {
		t.Fatal("DownloadChunkedConcurrent succeeded, want the chunk 1 failure")
	}
	if !strings.Contains(err.Error(), "chunk 1") {
		t.Errorf("error does not name the failing chunk: %v", err)
	}

	saved, loadErr := state.LoadDownloadState(localPath)
	if loadErr != nil {
		t.Fatalf("load resume state: %v", loadErr)
	}
	if saved == nil {
		t.Fatal("no resume state was kept, so the chunk that did land is lost")
	}
	if len(saved.CompletedChunks) != 1 || saved.CompletedChunks[0] != 0 {
		t.Fatalf("completed chunks = %v, want only chunk 0", saved.CompletedChunks)
	}

	written, readErr := os.ReadFile(localPath)
	if readErr != nil {
		t.Fatalf("read partial download: %v", readErr)
	}
	if int64(len(written)) != int64(len(object)) {
		t.Fatalf("file is %d bytes, want it sized to the object's %d", len(written), len(object))
	}
	for _, chunkIndex := range saved.CompletedChunks {
		start := chunkIndex * chunkSize
		end := min(start+chunkSize, int64(len(object)))
		if !bytes.Equal(written[start:end], object[start:end]) {
			t.Errorf("chunk %d is recorded as completed but its bytes are not on disk", chunkIndex)
		}
	}
}

// TestDownloadChunkedConcurrentBoundsChunksInFlight covers the other half of the
// same fix: the driver holds one chunk per worker rather than accumulating the
// whole file in memory before writing it, so a multi-gigabyte download no longer
// allocates its own size. Requests in flight is the observable form of that.
func TestDownloadChunkedConcurrentBoundsChunksInFlight(t *testing.T) {
	const chunkSize = 8
	const concurrency = 2
	object := objectOfSize(chunkSize * 6)
	localPath := filepath.Join(t.TempDir(), "results.dat")

	server := &rangeServer{object: object}

	var progress []float64
	err := DownloadChunkedConcurrent(context.Background(), ChunkedConcurrentParams{
		RemotePath:       "container/results.dat",
		LocalPath:        localPath,
		TotalSize:        int64(len(object)),
		ChunkSize:        chunkSize,
		Concurrency:      concurrency,
		StorageType:      "AzureStorage",
		ObjectETag:       `"etag-1"`,
		Retry:            passThroughRetry,
		Open:             server.open,
		ProgressCallback: func(fraction float64) { progress = append(progress, fraction) },
	})
	if err != nil {
		t.Fatalf("DownloadChunkedConcurrent: %v", err)
	}

	if peak := server.peakInFlight(); peak > concurrency {
		t.Errorf("%d chunks were in flight at once, want at most %d", peak, concurrency)
	}
	if server.requestCount != 6 {
		t.Errorf("%d range requests, want one per chunk (6)", server.requestCount)
	}

	written, readErr := os.ReadFile(localPath)
	if readErr != nil {
		t.Fatalf("read download: %v", readErr)
	}
	if !bytes.Equal(written, object) {
		t.Error("downloaded file does not match the object")
	}

	if len(progress) == 0 || progress[len(progress)-1] != 1.0 {
		t.Errorf("progress did not finish at 1.0: %v", progress)
	}

	if state.DownloadResumeStateExists(localPath) {
		t.Error("resume state survived a completed download")
	}
}

// TestDownloadChunkedConcurrentDiscardsStateForAnotherObject covers the pinning
// the S3 path was missing: a resume state written against a different version of
// the object describes bytes that are no longer there, so it must be discarded
// rather than resumed from.
func TestDownloadChunkedConcurrentDiscardsStateForAnotherObject(t *testing.T) {
	const chunkSize = 8
	object := objectOfSize(chunkSize * 2)
	localPath := filepath.Join(t.TempDir(), "results.dat")

	// Seed a full-length file of the wrong bytes plus a state claiming chunk 0,
	// as an interrupted download of the previous version of the object would.
	if err := os.WriteFile(localPath, make([]byte, len(object)), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	stale := &state.DownloadResumeState{
		LocalPath:       localPath,
		EncryptedPath:   localPath,
		RemotePath:      "bucket/results.dat",
		TotalSize:       int64(len(object)),
		DownloadedBytes: chunkSize,
		ETag:            `"etag-old"`,
		ChunkSize:       chunkSize,
		CompletedChunks: []int64{0},
		// Fresh timestamps so age validation passes and the ETag mismatch is
		// the ONLY reason this state gets discarded (pins the D1-d guard).
		CreatedAt:  time.Now(),
		LastUpdate: time.Now(),
	}
	if err := state.SaveDownloadState(stale, localPath); err != nil {
		t.Fatalf("seed resume state: %v", err)
	}

	server := &rangeServer{object: object}
	err := DownloadChunkedConcurrent(context.Background(), ChunkedConcurrentParams{
		RemotePath:  "bucket/results.dat",
		LocalPath:   localPath,
		TotalSize:   int64(len(object)),
		ChunkSize:   chunkSize,
		Concurrency: 1,
		StorageType: "S3Storage",
		ObjectETag:  `"etag-new"`,
		Retry:       passThroughRetry,
		Open:        server.open,
	})
	if err != nil {
		t.Fatalf("DownloadChunkedConcurrent: %v", err)
	}

	if server.requestCount != 2 {
		t.Errorf("%d range requests, want both chunks re-fetched (2)", server.requestCount)
	}
	written, readErr := os.ReadFile(localPath)
	if readErr != nil {
		t.Fatalf("read download: %v", readErr)
	}
	if !bytes.Equal(written, object) {
		t.Error("downloaded file kept bytes from the stale attempt")
	}
}

// TestFetchRangeWithRetryRejectsShortReads covers the assertion at the one place
// every ranged download now passes through. A body that stops early is the only
// failure a range read cannot report on its own: the caller would write a short
// part and find out at the checksum, or never on a file that carries none.
func TestFetchRangeWithRetryRejectsShortReads(t *testing.T) {
	object := objectOfSize(64)

	tests := []struct {
		name      string
		served    int64 // bytes the body actually returns
		requested int64
		wantErr   bool
	}{
		{name: "a full range is returned as is", served: 16, requested: 16},
		{name: "a body that stops early fails", served: 9, requested: 16, wantErr: true},
		{name: "an empty body fails", served: 0, requested: 16, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			open := func(_ context.Context, offset, _ int64) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(object[offset : offset+tt.served])), nil
			}

			data, err := FetchRangeWithRetry(context.Background(), passThroughRetry, 8, tt.requested, nil, open)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("FetchRangeWithRetry returned %d bytes, want the short-read failure", len(data))
				}
				if !strings.Contains(err.Error(), "short range read") {
					t.Errorf("error does not name the short read: %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("FetchRangeWithRetry: %v", err)
			}
			if !bytes.Equal(data, object[8:8+tt.requested]) {
				t.Error("returned bytes do not match the range asked for")
			}
		})
	}
}

// TestFetchRangeWithRetryRollsBackProgressOnShortRead checks that the bytes a
// failed attempt already reported are taken back, so the retry can report them
// again without the transfer appearing to move further than it has. The range is
// larger than ProgressReaderThreshold because that is what makes the reader
// report anything at all mid-range.
func TestFetchRangeWithRetryRollsBackProgressOnShortRead(t *testing.T) {
	const requested = 2 * ProgressReaderThreshold
	object := objectOfSize(requested)

	var netProgress int64
	// Serve enough to cross the reporting threshold, then stop early.
	open := func(_ context.Context, offset, _ int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(object[offset : offset+ProgressReaderThreshold+16])), nil
	}

	_, err := FetchRangeWithRetry(context.Background(), passThroughRetry, 0, requested,
		func(delta int64) { atomic.AddInt64(&netProgress, delta) }, open)
	if err == nil {
		t.Fatal("FetchRangeWithRetry succeeded, want the short-read failure")
	}
	if got := atomic.LoadInt64(&netProgress); got != 0 {
		t.Errorf("net progress after the failed attempt = %d, want 0", got)
	}
}

// TestFetchRangeWithRetryReportsOpenFailure keeps the wrapping around a failed
// range request pinned, since the drivers wrap it again with their own unit of
// work.
func TestFetchRangeWithRetryReportsOpenFailure(t *testing.T) {
	wanted := errors.New("connection reset by peer")
	_, err := FetchRangeWithRetry(context.Background(), passThroughRetry, 32, 16, nil,
		func(context.Context, int64, int64) (io.ReadCloser, error) { return nil, wanted })

	if !errors.Is(err, wanted) {
		t.Fatalf("error = %v, want it to wrap %v", err, wanted)
	}
	if !strings.Contains(err.Error(), "[32-47]") {
		t.Errorf("error does not name the range: %v", err)
	}
}
