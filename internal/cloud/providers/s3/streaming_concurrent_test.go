package s3

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/crypto"
)

// TestUploadProgressReaderSeek verifies that uploadProgressReader implements
// io.ReadSeeker: it can read all bytes, seek to 0, and re-read the same bytes.
func TestUploadProgressReaderSeek(t *testing.T) {
	data := []byte("hello, world — this is test data for seek verification")
	var totalReported int64

	pr := &transfer.UploadProgressReader{
		Reader:    bytes.NewReader(data),
		Callback:  func(n int64) { totalReported += n },
		Threshold: 1, // Report on every read for testing
	}

	// Verify io.ReadSeeker interface compliance
	var _ io.ReadSeeker = pr

	// Read all bytes
	buf1, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("first ReadAll failed: %v", err)
	}
	if !bytes.Equal(buf1, data) {
		t.Fatalf("first read: got %q, want %q", buf1, data)
	}

	// Seek back to start
	pos, err := pr.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek(0, SeekStart) failed: %v", err)
	}
	if pos != 0 {
		t.Fatalf("Seek returned position %d, want 0", pos)
	}

	// Re-read all bytes — must get the same data
	buf2, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("second ReadAll failed: %v", err)
	}
	if !bytes.Equal(buf2, data) {
		t.Fatalf("second read: got %q, want %q", buf2, data)
	}
}

// TestUploadProgressReaderSeekRollsBackProgress verifies that Seek() calls the
// callback with a negative value to roll back reported progress, preventing
// double-counting on retry.
func TestUploadProgressReaderSeekRollsBackProgress(t *testing.T) {
	data := []byte("abcdefghij") // 10 bytes
	var netProgress atomic.Int64

	pr := &transfer.UploadProgressReader{
		Reader: bytes.NewReader(data),
		Callback: func(n int64) {
			netProgress.Add(n)
		},
		Threshold: 1, // Report on every read
	}

	// Read all bytes — should report +10 total
	_, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if got := netProgress.Load(); got != int64(len(data)) {
		t.Fatalf("after read: net progress = %d, want %d", got, len(data))
	}

	// Seek to 0 — should roll back (-10), net goes to 0
	_, err = pr.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek failed: %v", err)
	}
	if got := netProgress.Load(); got != 0 {
		t.Fatalf("after seek: net progress = %d, want 0", got)
	}

	// Read again — net should be back to +10
	_, err = io.ReadAll(pr)
	if err != nil {
		t.Fatalf("second ReadAll failed: %v", err)
	}
	if got := netProgress.Load(); got != int64(len(data)) {
		t.Fatalf("after re-read: net progress = %d, want %d", got, len(data))
	}
}

// TestUploadProgressReaderThreshold verifies that the callback is only invoked
// when accumulated bytes reach the threshold (not on every small read).
func TestUploadProgressReaderThreshold(t *testing.T) {
	// 100 bytes of data, threshold at 30
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}

	var callbackCalls int
	var callbackValues []int64

	pr := &transfer.UploadProgressReader{
		Reader: bytes.NewReader(data),
		Callback: func(n int64) {
			callbackCalls++
			callbackValues = append(callbackValues, n)
		},
		Threshold: 30,
	}

	// Read in small chunks to test threshold accumulation
	buf := make([]byte, 10)
	for {
		_, err := pr.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
	}

	// With 100 bytes and threshold 30:
	// - After 30 bytes (3 reads of 10): callback with 30
	// - After 60 bytes (3 more reads): callback with 30
	// - After 90 bytes (3 more reads): callback with 30
	// - After 100 bytes (1 more read, EOF): callback with 10
	// Total: 4 callbacks
	if callbackCalls < 3 {
		t.Fatalf("expected at least 3 callback calls with threshold=30 and 100 bytes, got %d", callbackCalls)
	}

	// Verify total reported equals data length
	var totalReported int64
	for _, v := range callbackValues {
		totalReported += v
	}
	if totalReported != int64(len(data)) {
		t.Fatalf("total reported = %d, want %d", totalReported, len(data))
	}
}

// TestUploadCiphertextReportsEachByteOnceAcrossRetries is the F18 regression.
// The progress reader knows how to withdraw what it reported, but only through
// its own Seek — and an outer retry does not seek, it builds a new reader and
// drops the old one. The bytes the failed attempt reported stayed in the total
// and the retry added them again, so a transfer could show 100% before the file
// had been sent.
func TestUploadCiphertextReportsEachByteOnceAcrossRetries(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	backend.rejectOncePerPart = true // the first attempt fails after reading the body
	s3Client := newTestS3Client(t, server)

	ciphertext := make([]byte, 3*1024*1024)
	for i := range ciphertext {
		ciphertext[i] = byte(i)
	}

	var reported atomic.Int64
	uploadState := &transfer.StreamingUpload{
		UploadID:             testUploadID,
		StoragePath:          testPathBase + "/object",
		ByteProgressCallback: func(n int64) { reported.Add(n) },
		ProviderData: &s3ProviderData{
			bucket:   testBucket,
			s3Client: s3Client,
		},
	}

	provider := &Provider{}
	if _, err := provider.UploadCiphertext(context.Background(), uploadState, 0, ciphertext); err != nil {
		t.Fatalf("part did not recover from the rejected attempt: %v", err)
	}

	if got := reported.Load(); got != int64(len(ciphertext)) {
		t.Errorf("progress reported %d bytes for a %d-byte part: the failed attempt's bytes were counted as well",
			got, len(ciphertext))
	}
}

// hkdfObjectBackend serves one v1 (HKDF) object over ranged GETs and reports a
// different ETag from a chosen range onwards, which is an object being replaced
// under a download that is made of many requests.
type hkdfObjectBackend struct {
	mu sync.Mutex

	ciphertext  []byte
	metadata    map[string]string
	replaceAt   int  // range index from which the ETag changes; 0 disables
	noHeadETag  bool // the HEAD carries no ETag, the way a proxy that strips them from HEAD replies alone leaves it
	noRangeETag bool // ranged GETs carry no ETag, which leaves a pinned download nothing to compare
	ranges      int
}

// rangeCount reports how many ranged GETs the backend has served.
func (h *hkdfObjectBackend) rangeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ranges
}

func (h *hkdfObjectBackend) ServeHTTP(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.mu.Lock()
	etag := `"version-one"`
	if r.Method == nethttp.MethodGet {
		h.ranges++
		if h.replaceAt > 0 && h.ranges >= h.replaceAt {
			etag = `"version-two"`
		}
		if h.noRangeETag {
			etag = ""
		}
	} else if h.noHeadETag {
		etag = ""
	}
	h.mu.Unlock()

	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	for name, value := range h.metadata {
		w.Header().Set("x-amz-meta-"+name, value)
	}

	switch r.Method {
	case nethttp.MethodHead:
		w.Header().Set("Content-Length", strconv.Itoa(len(h.ciphertext)))
		w.WriteHeader(nethttp.StatusOK)

	case nethttp.MethodGet:
		start, end := 0, len(h.ciphertext)-1
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		if end >= len(h.ciphertext) {
			end = len(h.ciphertext) - 1
		}
		body := h.ciphertext[start : end+1]
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(h.ciphertext)))
		w.WriteHeader(nethttp.StatusPartialContent)
		_, _ = w.Write(body)

	default:
		w.WriteHeader(nethttp.StatusNotImplemented)
	}
}

// newHKDFObject builds a v1 object of parts plaintext parts, the master key it
// was written under, and the plaintext a correct download produces.
func newHKDFObject(t *testing.T, parts int, partSize int64) (*hkdfObjectBackend, []byte, []byte) {
	t.Helper()

	encryptor, err := encryption.NewStreamingEncryptor(partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptor: %v", err)
	}

	plaintext := make([]byte, int64(parts)*partSize)
	for i := range plaintext {
		plaintext[i] = byte(i*11 + 3)
	}

	var ciphertext []byte
	for index := 0; index < parts; index++ {
		part, err := encryptor.EncryptPart(int64(index), plaintext[int64(index)*partSize:int64(index+1)*partSize])
		if err != nil {
			t.Fatalf("EncryptPart(%d): %v", index, err)
		}
		ciphertext = append(ciphertext, part...)
	}

	return &hkdfObjectBackend{
		ciphertext: ciphertext,
		metadata: map[string]string{
			"formatversion": "1",
			"fileid":        base64.StdEncoding.EncodeToString(encryptor.GetFileId()),
			"partsize":      strconv.FormatInt(partSize, 10),
		},
	}, encryptor.GetMasterKey(), plaintext
}

// TestDownloadStreamingAbortsWhenTheObjectIsReplaced is the v1 half of pinning a
// ranged download to one version. The concurrent chunked paths already refuse a
// file stitched from two objects; this one fetched every part with no version
// condition at all, so an object replaced partway through was assembled from
// both versions and — with the size right and no per-file checksum guaranteed —
// nothing afterwards noticed.
func TestDownloadStreamingAbortsWhenTheObjectIsReplaced(t *testing.T) {
	backend, masterKey, _ := newHKDFObject(t, 3, 64)
	backend.replaceAt = 2 // the first range is the pin; the second is the new object

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestS3Client(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, s3Client: client}

	localPath := filepath.Join(t.TempDir(), "downloaded.dat")
	err := provider.DownloadStreaming(context.Background(), "object.dat", localPath, masterKey, nil)
	if !errors.Is(err, transfer.ErrObjectReplaced) {
		t.Fatalf("error = %v, want it to wrap ErrObjectReplaced", err)
	}
}

// TestDownloadStreamingReadsOneVersionThrough is the other side of the pin: an
// object that does not change downloads exactly as before.
func TestDownloadStreamingReadsOneVersionThrough(t *testing.T) {
	backend, masterKey, plaintext := newHKDFObject(t, 3, 64)

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestS3Client(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, s3Client: client}

	localPath := filepath.Join(t.TempDir(), "downloaded.dat")
	if err := provider.DownloadStreaming(context.Background(), "object.dat", localPath, masterKey, nil); err != nil {
		t.Fatalf("DownloadStreaming: %v", err)
	}

	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read the downloaded file: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("downloaded %d bytes, want the %d the object holds", len(got), len(plaintext))
	}
}

// TestGetEncryptedSizeAndRangeAbortWhenTheObjectIsReplaced is the v2 (CBC) half
// of pinning a ranged download to one version. That path reads the object's
// size once and then fetches its parts as independent ranges with no version
// carried between them, so an object replaced under it — a re-upload at the
// same path — was decrypted into one file from two versions. Only the sizes and
// the ETags matter here, not what the bytes decrypt to.
func TestGetEncryptedSizeAndRangeAbortWhenTheObjectIsReplaced(t *testing.T) {
	backend, _, _ := newHKDFObject(t, 3, 64)
	backend.replaceAt = 1 // the HEAD reports the first version, every range the second

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestS3Client(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, s3Client: client}
	ctx := context.Background()

	size, version, err := provider.GetEncryptedSize(ctx, "object.dat")
	if err != nil {
		t.Fatalf("GetEncryptedSize: %v", err)
	}
	if size != int64(len(backend.ciphertext)) {
		t.Errorf("size = %d, want the %d bytes the object holds", size, len(backend.ciphertext))
	}
	if version != `"version-one"` {
		t.Fatalf("version = %q, want the ETag the HEAD reported", version)
	}

	_, err = provider.DownloadEncryptedRange(ctx, "object.dat", 0, 64, version, nil)
	if !errors.Is(err, transfer.ErrObjectReplaced) {
		t.Fatalf("error = %v, want it to wrap ErrObjectReplaced", err)
	}
	// A replaced object is not a transient failure: retrying spends the budget
	// on a range that cannot come back right.
	if got := backend.rangeCount(); got != 1 {
		t.Errorf("%d ranges were fetched, want exactly the one attempt", got)
	}
}

// TestDownloadEncryptedRangeReadsThePinnedVersion is the other side of the pin:
// a range of the object the size call measured is served as before.
func TestDownloadEncryptedRangeReadsThePinnedVersion(t *testing.T) {
	backend, _, _ := newHKDFObject(t, 3, 64)

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestS3Client(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, s3Client: client}
	ctx := context.Background()

	_, version, err := provider.GetEncryptedSize(ctx, "object.dat")
	if err != nil {
		t.Fatalf("GetEncryptedSize: %v", err)
	}

	got, err := provider.DownloadEncryptedRange(ctx, "object.dat", 64, 64, version, nil)
	if err != nil {
		t.Fatalf("DownloadEncryptedRange: %v", err)
	}
	if !bytes.Equal(got, backend.ciphertext[64:128]) {
		t.Errorf("range [64-128) came back as %d bytes that are not the object's", len(got))
	}
}

// listPartsBackend answers the one question a resume asks S3: is this multipart
// upload still there? A missing one is a NoSuchUpload, which is an answer rather
// than a failure — the upload has expired or been aborted, and the caller has to
// start a fresh object rather than keep retrying an unusable resume.
type listPartsBackend struct {
	mu      sync.Mutex
	present bool
	calls   int
}

func (b *listPartsBackend) ServeHTTP(w nethttp.ResponseWriter, r *nethttp.Request) {
	if r.Method != nethttp.MethodGet || !r.URL.Query().Has("uploadId") {
		w.WriteHeader(nethttp.StatusNotImplemented)
		return
	}

	b.mu.Lock()
	b.calls++
	present := b.present
	b.mu.Unlock()

	w.Header().Set("Content-Type", "application/xml")
	if !present {
		w.WriteHeader(nethttp.StatusNotFound)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchUpload</Code><Message>The specified upload does not exist.</Message></Error>`)
		return
	}
	w.WriteHeader(nethttp.StatusOK)
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListPartsResult><Bucket>`+testBucket+`</Bucket></ListPartsResult>`)
}

func (b *listPartsBackend) requestCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// TestValidateStreamingUploadExistsReportsAVanishedUpload pins the resume-path
// check: a multipart upload the backend no longer holds is reported as absent
// rather than as an error, so the orchestrator retires the state and starts one
// fresh object instead of failing every attempt against the same missing upload.
func TestValidateStreamingUploadExistsReportsAVanishedUpload(t *testing.T) {
	backend := &listPartsBackend{}
	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)

	provider := &Provider{s3Client: newTestS3Client(t, server)}
	ctx := context.Background()

	exists, err := provider.ValidateStreamingUploadExists(ctx, testUploadID, testPathBase+"/object")
	if err != nil {
		t.Fatalf("checking a multipart upload the backend has dropped failed: %v", err)
	}
	if exists {
		t.Error("a resume was allowed against a multipart upload the backend no longer holds")
	}
	if backend.requestCount() == 0 {
		t.Error("the check never asked the backend whether the upload is still there")
	}

	backend.mu.Lock()
	backend.present = true
	backend.mu.Unlock()

	exists, err = provider.ValidateStreamingUploadExists(ctx, testUploadID, testPathBase+"/object")
	if err != nil {
		t.Fatalf("checking a live multipart upload failed: %v", err)
	}
	if !exists {
		t.Error("a resume was refused although its multipart upload is still open")
	}
}

// TestDownloadEncryptedRangeRefusesARangeWithNoVersion is the other way an
// object can be replaced without the pin noticing: not a different ETag, but no
// ETag at all on the response that matters. A pinned download has nothing to
// compare then, and had been handing those bytes over.
func TestDownloadEncryptedRangeRefusesARangeWithNoVersion(t *testing.T) {
	backend, _, _ := newHKDFObject(t, 3, 64)
	backend.noRangeETag = true // the HEAD reports a version, the ranges report none

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestS3Client(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, s3Client: client}
	ctx := context.Background()

	_, version, err := provider.GetEncryptedSize(ctx, "object.dat")
	if err != nil {
		t.Fatalf("GetEncryptedSize: %v", err)
	}
	if version != `"version-one"` {
		t.Fatalf("version = %q, want the ETag the HEAD reported", version)
	}

	_, err = provider.DownloadEncryptedRange(ctx, "object.dat", 0, 64, version, nil)
	if !errors.Is(err, transfer.ErrObjectReplaced) {
		t.Fatalf("error = %v, want it to wrap ErrObjectReplaced", err)
	}
	// Evidence that never arrives will not arrive on the retry either, and the
	// error wording has to keep the classifier reading it as fatal.
	if got := backend.rangeCount(); got != 1 {
		t.Errorf("%d ranges were fetched, want exactly the one attempt", got)
	}
}

// TestGetEncryptedSizeFallsBackToARangeForTheVersion covers the HEAD that comes
// back without an ETag. Some intercepting proxies strip response headers from
// HEAD replies and leave them on GETs, and an unpinned download is worse than
// one extra byte on the wire — so the version is read off a one-byte range
// before the download gives up on being pinned at all.
func TestGetEncryptedSizeFallsBackToARangeForTheVersion(t *testing.T) {
	backend, _, _ := newHKDFObject(t, 3, 64)
	backend.noHeadETag = true // only the HEAD is stripped
	backend.replaceAt = 2     // the object is replaced right after the fallback range

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestS3Client(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, s3Client: client}
	ctx := context.Background()

	size, version, err := provider.GetEncryptedSize(ctx, "object.dat")
	if err != nil {
		t.Fatalf("GetEncryptedSize: %v", err)
	}
	if size != int64(len(backend.ciphertext)) {
		t.Errorf("size = %d, want the %d bytes the object holds", size, len(backend.ciphertext))
	}
	if version != `"version-one"` {
		t.Fatalf("version = %q, want the ETag the fallback range reported", version)
	}
	if got := backend.rangeCount(); got != 1 {
		t.Errorf("the fallback cost %d ranges, want the single byte", got)
	}

	// And that version pins the parts: the object replaced after it is caught.
	if _, err := provider.DownloadEncryptedRange(ctx, "object.dat", 0, 64, version, nil); !errors.Is(err, transfer.ErrObjectReplaced) {
		t.Fatalf("error = %v, want it to wrap ErrObjectReplaced", err)
	}
}

// TestGetEncryptedSizeLeavesTheDownloadUnpinnedWithoutAnyVersion is the floor
// under the two above: a backend that reports no version anywhere is not
// refused, it is downloaded unpinned, exactly as it was before versions were
// compared at all.
func TestGetEncryptedSizeLeavesTheDownloadUnpinnedWithoutAnyVersion(t *testing.T) {
	backend, _, _ := newHKDFObject(t, 3, 64)
	backend.noHeadETag = true
	backend.noRangeETag = true

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestS3Client(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, s3Client: client}
	ctx := context.Background()

	size, version, err := provider.GetEncryptedSize(ctx, "object.dat")
	if err != nil {
		t.Fatalf("GetEncryptedSize: %v", err)
	}
	if size != int64(len(backend.ciphertext)) {
		t.Errorf("size = %d, want the %d bytes the object holds", size, len(backend.ciphertext))
	}
	if version != "" {
		t.Errorf("version = %q, want none: the backend reported none anywhere", version)
	}

	got, err := provider.DownloadEncryptedRange(ctx, "object.dat", 64, 64, version, nil)
	if err != nil {
		t.Fatalf("DownloadEncryptedRange: %v", err)
	}
	if !bytes.Equal(got, backend.ciphertext[64:128]) {
		t.Errorf("range [64-128) came back as %d bytes that are not the object's", len(got))
	}
}
