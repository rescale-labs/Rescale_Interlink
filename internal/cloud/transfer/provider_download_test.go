package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
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

// TestDownloadChunkedConcurrentRefusesToCallCancellationSuccess covers the gap
// between "no worker reported an error" and "the download finished". Workers
// return silently when the operation is cancelled, so a cancellation that
// arrives between two range requests leaves firstError nil with chunks still
// missing. The driver used to read that as success: it reported 100%, deleted
// the resume state and returned nil, leaving a file full of holes that every
// downstream presence check accepts.
//
// The context is cancelled here once the first chunk is in hand. The download
// must fail, and the resume state must survive so the chunk that did land is
// not downloaded again.
func TestDownloadChunkedConcurrentRefusesToCallCancellationSuccess(t *testing.T) {
	const chunkSize = 8
	object := objectOfSize(chunkSize * 4)
	localPath := filepath.Join(t.TempDir(), "results.dat")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := &rangeServer{object: object}
	var served int
	cancelAfterFirstChunk := func(c context.Context, offset, length int64) (io.ReadCloser, error) {
		body, err := server.open(c, offset, length)
		served++
		if served == 1 {
			cancel()
		}
		return body, err
	}

	err := DownloadChunkedConcurrent(ctx, ChunkedConcurrentParams{
		RemotePath:  "bucket/results.dat",
		LocalPath:   localPath,
		TotalSize:   int64(len(object)),
		ChunkSize:   chunkSize,
		Concurrency: 1, // one worker, so the cancellation lands between two chunks
		StorageType: "S3Storage",
		ObjectETag:  `"etag-1"`,
		Retry:       passThroughRetry,
		Open:        cancelAfterFirstChunk,
	})
	if err == nil {
		t.Fatal("a cancelled download returned success, so its holes are now a finished file")
	}

	saved, loadErr := state.LoadDownloadState(localPath)
	if loadErr != nil {
		t.Fatalf("load resume state: %v", loadErr)
	}
	if saved == nil {
		t.Fatal("the resume state was deleted, so the chunk that did land has to be downloaded again")
	}
	if len(saved.CompletedChunks) != 1 || saved.CompletedChunks[0] != 0 {
		t.Errorf("completed chunks = %v, want only chunk 0", saved.CompletedChunks)
	}
}

// TestDownloadChunkedConcurrentReportsMissingChunks is the same contract seen
// from the other side: a worker that stops without recording an error must not
// leave the driver reporting a download it never completed. A short chunk list
// after the workers join is the assertion the driver was missing.
func TestDownloadChunkedConcurrentReportsMissingChunks(t *testing.T) {
	const chunkSize = 8
	object := objectOfSize(chunkSize * 3)
	localPath := filepath.Join(t.TempDir(), "results.dat")

	// A worker that returns on a cancelled context before its first request, so
	// no range is ever fetched and no error is ever set.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	server := &rangeServer{object: object}
	err := DownloadChunkedConcurrent(ctx, ChunkedConcurrentParams{
		RemotePath:  "container/results.dat",
		LocalPath:   localPath,
		TotalSize:   int64(len(object)),
		ChunkSize:   chunkSize,
		Concurrency: 2,
		StorageType: "AzureStorage",
		ObjectETag:  `"etag-1"`,
		Retry:       passThroughRetry,
		Open:        server.open,
	})
	if err == nil {
		t.Fatal("a download that fetched nothing returned success")
	}
	if server.requestCount != 0 {
		t.Errorf("%d range requests were made on an already-cancelled context, want none", server.requestCount)
	}
}

// recordingSink stands in for the file a chunked download writes into and
// records the order of the calls made on it, noting at each one whether the
// resume sidecar already claimed the chunk. The ordering between a chunk's
// bytes becoming durable and the sidecar claiming them is the whole of what
// this is about, and the finished file cannot show it.
type recordingSink struct {
	statePath string

	mu    sync.Mutex
	calls []string
}

func (s *recordingSink) note(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	claim := "unclaimed"
	if _, err := os.Stat(s.statePath); err == nil {
		claim = "claimed"
	}
	s.calls = append(s.calls, op+"/"+claim)
}

func (s *recordingSink) WriteAt(p []byte, off int64) (int, error) {
	s.note("write")
	return len(p), nil
}
func (s *recordingSink) Sync() error               { s.note("sync"); return nil }
func (s *recordingSink) Truncate(size int64) error { return nil }
func (s *recordingSink) Close() error              { return nil }

func (s *recordingSink) sequence() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// TestDownloadChunkedConcurrentMakesChunksDurableBeforeClaimingThem covers the
// ordering the resume sidecar depends on. The data file was synced only once,
// at the very end, while the sidecar was written after every chunk — so a
// crash, or a power failure, could persist a sidecar claiming chunks whose
// bytes were still in the page cache. The next attempt skips exactly those
// chunks, and the up-front Truncate fills their place with zeros: a hole that
// no size check sees and that only a checksum would ever catch, which files
// without one do not have.
func TestDownloadChunkedConcurrentMakesChunksDurableBeforeClaimingThem(t *testing.T) {
	const chunkSize = 8
	object := objectOfSize(chunkSize * 2)
	localPath := filepath.Join(t.TempDir(), "results.dat")

	sink := &recordingSink{statePath: localPath + ".download.resume"}
	server := &rangeServer{object: object}

	err := DownloadChunkedConcurrent(context.Background(), ChunkedConcurrentParams{
		RemotePath:  "bucket/results.dat",
		LocalPath:   localPath,
		TotalSize:   int64(len(object)),
		ChunkSize:   chunkSize,
		Concurrency: 1, // one worker, so the call sequence is the driver's, not the scheduler's
		StorageType: "S3Storage",
		ObjectETag:  `"etag-1"`,
		Retry:       passThroughRetry,
		Open:        server.open,
		openSink:    func(string) (chunkSink, error) { return sink, nil },
	})
	if err != nil {
		t.Fatalf("DownloadChunkedConcurrent: %v", err)
	}

	// Two chunks: each is written, then flushed, and only then claimed. A sync
	// that already sees the chunk claimed would mean the claim came first.
	want := []string{
		"write/unclaimed", // chunk 0's bytes
		"sync/unclaimed",  // made durable before the sidecar claims chunk 0
		"write/claimed",   // chunk 1's bytes, chunk 0 now claimed
		"sync/claimed",    // made durable before the sidecar claims chunk 1
		"sync/claimed",    // the final flush before the caller checksums the file
	}
	got := sink.sequence()
	if len(got) != len(want) {
		t.Fatalf("call sequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call sequence = %v, want %v", got, want)
		}
	}
}

// versionedRangeServer answers range requests out of one of two objects of the
// same length, switching to the second once the first replaceAfter ranges have
// been served — an object replaced under a download that is still running.
type versionedRangeServer struct {
	before, after []byte
	etagBefore    string
	etagAfter     string
	replaceAfter  int

	mu     sync.Mutex
	served int
}

func (s *versionedRangeServer) open(_ context.Context, offset, length int64) (io.ReadCloser, string, error) {
	s.mu.Lock()
	object, etag := s.before, s.etagBefore
	if s.served >= s.replaceAfter {
		object, etag = s.after, s.etagAfter
	}
	s.served++
	s.mu.Unlock()

	return io.NopCloser(bytes.NewReader(object[offset : offset+length])), etag, nil
}

// TestDownloadChunkedConcurrentAbortsWhenTheObjectIsReplaced covers the version
// consistency a ranged download had none of. Ranges were fetched with no
// condition and the ETag each response carried was thrown away, so replacing
// the object mid-download with a body of the same length produced a file
// stitched from both versions. Resume-time ETag validation does not catch it —
// it compares across attempts, not within one — and neither does the size
// check, which is all a file without a checksum has.
func TestDownloadChunkedConcurrentAbortsWhenTheObjectIsReplaced(t *testing.T) {
	const chunkSize = 8
	first := objectOfSize(chunkSize * 4)
	second := objectOfSize(chunkSize * 4)
	for i := range second {
		second[i] ^= 0xff // same length, entirely different bytes
	}
	localPath := filepath.Join(t.TempDir(), "results.dat")

	server := &versionedRangeServer{
		before:       first,
		after:        second,
		etagBefore:   `"etag-1"`,
		etagAfter:    `"etag-2"`,
		replaceAfter: 2, // the first two ranges come from the object we started on
	}

	err := DownloadChunkedConcurrent(context.Background(), ChunkedConcurrentParams{
		RemotePath:  "bucket/results.dat",
		LocalPath:   localPath,
		TotalSize:   int64(len(first)),
		ChunkSize:   chunkSize,
		Concurrency: 1, // one worker, so the replacement lands between two chunks
		StorageType: "S3Storage",
		ObjectETag:  `"etag-1"`,
		Retry:       passThroughRetry,
		Open:        PinObjectVersion(server.open, `"etag-1"`),
	})
	if err == nil {
		t.Fatal("the download succeeded, so a file made of two different objects is now the download")
	}
	if !errors.Is(err, ErrObjectReplaced) {
		t.Fatalf("error = %v, want it to wrap ErrObjectReplaced", err)
	}
	if !strings.Contains(err.Error(), "chunk 2") {
		t.Errorf("error does not name the chunk that hit the replacement: %v", err)
	}

	written, readErr := os.ReadFile(localPath)
	if readErr != nil {
		t.Fatalf("read partial download: %v", readErr)
	}
	if bytes.Equal(written, first) || bytes.Equal(written, second) {
		t.Error("the aborted download left a complete file; it must not look finished")
	}
}

// TestPinObjectVersionAdoptsTheFirstVersion covers the case where the metadata
// request reported no version: the first range that reports one sets the pin,
// and a backend that never reports one is left alone rather than being refused.
func TestPinObjectVersionAdoptsTheFirstVersion(t *testing.T) {
	object := objectOfSize(32)

	t.Run("the first reported version becomes the pin", func(t *testing.T) {
		server := &versionedRangeServer{
			before: object, after: object,
			etagBefore: `"etag-1"`, etagAfter: `"etag-2"`,
			replaceAfter: 1,
		}
		open := PinObjectVersion(server.open, "")

		if _, err := open(context.Background(), 0, 8); err != nil {
			t.Fatalf("first range: %v", err)
		}
		if _, err := open(context.Background(), 8, 8); !errors.Is(err, ErrObjectReplaced) {
			t.Fatalf("second range error = %v, want ErrObjectReplaced", err)
		}
	})

	t.Run("a backend that reports no version pins nothing", func(t *testing.T) {
		server := &versionedRangeServer{
			before: object, after: object,
			replaceAfter: 1, // both etags are empty
		}
		open := PinObjectVersion(server.open, "")

		for offset := int64(0); offset < 32; offset += 8 {
			body, err := open(context.Background(), offset, 8)
			if err != nil {
				t.Fatalf("range at %d: %v", offset, err)
			}
			body.Close()
		}
	})
}

// closeTrackingBody is a range body that records whether it was closed.
type closeTrackingBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *closeTrackingBody) Close() error {
	b.closed.Store(true)
	return nil
}

// scriptedVersionServer answers each range with the next version of its script,
// and keeps the bodies it handed out so a test can ask whether a refused one was
// closed.
type scriptedVersionServer struct {
	object   []byte
	versions []string

	mu     sync.Mutex
	served int
	bodies []*closeTrackingBody
}

func (s *scriptedVersionServer) open(_ context.Context, offset, length int64) (io.ReadCloser, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	version := ""
	if s.served < len(s.versions) {
		version = s.versions[s.served]
	}
	s.served++

	body := &closeTrackingBody{Reader: bytes.NewReader(s.object[offset : offset+length])}
	s.bodies = append(s.bodies, body)
	return body, version, nil
}

func (s *scriptedVersionServer) lastBody() *closeTrackingBody {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[len(s.bodies)-1]
}

// TestPinObjectVersionRefusesEvidenceItCannotCheck covers the half of the pin
// that failed open. A response carrying no version was handed straight back even
// when the download was pinned to one, so a range whose ETag never arrived —
// a proxy that drops the header on the response that matters, a backend that
// omits it — went into the file unchecked. Missing evidence is not evidence of
// sameness: only a download that has never seen a version at all may run
// unpinned.
func TestPinObjectVersionRefusesEvidenceItCannotCheck(t *testing.T) {
	object := objectOfSize(32)

	t.Run("a pinned download refuses a range that reports no version", func(t *testing.T) {
		server := &scriptedVersionServer{object: object, versions: []string{""}}
		open := PinObjectVersion(server.open, `"etag-1"`)

		body, err := open(context.Background(), 0, 8)
		if !errors.Is(err, ErrObjectReplaced) {
			t.Fatalf("error = %v, want it to wrap ErrObjectReplaced", err)
		}
		if body != nil {
			t.Error("a refused range handed its body back to be read")
		}
		if !server.lastBody().closed.Load() {
			t.Error("the refused body was left open")
		}
	})

	t.Run("an adopted pin refuses a later range that reports no version", func(t *testing.T) {
		server := &scriptedVersionServer{object: object, versions: []string{`"etag-1"`, ""}}
		open := PinObjectVersion(server.open, "")

		first, err := open(context.Background(), 0, 8)
		if err != nil {
			t.Fatalf("first range: %v", err)
		}
		first.Close()

		if _, err := open(context.Background(), 8, 8); !errors.Is(err, ErrObjectReplaced) {
			t.Fatalf("second range error = %v, want it to wrap ErrObjectReplaced", err)
		}
		if !server.lastBody().closed.Load() {
			t.Error("the refused body was left open")
		}
	})

	t.Run("a backend that reports no version warns once, not once per range", func(t *testing.T) {
		server := &scriptedVersionServer{object: object, versions: []string{"", "", "", ""}}
		open := PinObjectVersion(server.open, "")

		var logged bytes.Buffer
		log.SetOutput(&logged)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })

		for offset := int64(0); offset < 32; offset += 8 {
			body, err := open(context.Background(), offset, 8)
			if err != nil {
				t.Fatalf("range at %d: %v", offset, err)
			}
			body.Close()
		}

		if got := strings.Count(logged.String(), "reports no object version"); got != 1 {
			t.Errorf("an unpinnable download logged %d warnings over four ranges, want exactly one:\n%s",
				got, logged.String())
		}
	})
}
