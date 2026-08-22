package s3

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/resources"
	internaltransfer "github.com/rescale/rescale-int/internal/transfer"
)

// The pre-encrypt upload paths talk to the AWS SDK directly, so the only seam
// that exercises their real read loops is the wire. fakeS3Backend is a minimal
// multipart endpoint: it records what each part carried and what the completion
// request asked S3 to assemble, which is exactly what a truncated upload gets
// wrong.

const (
	testBucket   = "test-bucket"
	testPathBase = "uploads"
	testUploadID = "test-upload-id"
)

type stagedPart struct {
	size int64
	sum  [32]byte
}

type abortRecord struct {
	key      string
	uploadID string
}

type fakeS3Backend struct {
	mu sync.Mutex

	parts     map[int32]stagedPart
	committed []int32
	commits   int
	aborts    []abortRecord
	requests  int

	// listPartsLive decides whether a resume probe finds the old upload alive.
	listPartsLive bool
}

func newFakeS3Backend(t *testing.T) (*fakeS3Backend, *httptest.Server) {
	t.Helper()
	backend := &fakeS3Backend{parts: make(map[int32]stagedPart)}
	// TLS, because the client the provider rebuilds on every credential refresh
	// addresses the real S3 endpoint template, which is https.
	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	return backend, server
}

func (f *fakeS3Backend) ServeHTTP(w nethttp.ResponseWriter, r *nethttp.Request) {
	query := r.URL.Query()
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"+testBucket), "/")

	f.mu.Lock()
	f.requests++
	f.mu.Unlock()

	switch {
	case r.Method == nethttp.MethodPost && query.Has("uploads"):
		writeXML(w, nethttp.StatusOK, fmt.Sprintf(
			`<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`,
			testBucket, key, testUploadID))

	case r.Method == nethttp.MethodPut && query.Get("partNumber") != "":
		partNumber, err := strconv.Atoi(query.Get("partNumber"))
		if err != nil {
			w.WriteHeader(nethttp.StatusBadRequest)
			return
		}
		size, sum, err := hashRequestPayload(r)
		if err != nil {
			w.WriteHeader(nethttp.StatusInternalServerError)
			return
		}

		f.mu.Lock()
		f.parts[int32(partNumber)] = stagedPart{size: size, sum: sum}
		f.mu.Unlock()

		w.Header().Set("ETag", fmt.Sprintf("%q", fmt.Sprintf("etag-%d", partNumber)))
		w.WriteHeader(nethttp.StatusOK)

	case r.Method == nethttp.MethodPost && query.Get("uploadId") != "":
		var body struct {
			Parts []struct {
				PartNumber int32 `xml:"PartNumber"`
			} `xml:"Part"`
		}
		if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(nethttp.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.commits++
		f.committed = nil
		for _, part := range body.Parts {
			f.committed = append(f.committed, part.PartNumber)
		}
		f.mu.Unlock()

		writeXML(w, nethttp.StatusOK, fmt.Sprintf(
			`<CompleteMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><ETag>"final"</ETag></CompleteMultipartUploadResult>`,
			testBucket, key))

	case r.Method == nethttp.MethodDelete && query.Get("uploadId") != "":
		f.mu.Lock()
		f.aborts = append(f.aborts, abortRecord{key: key, uploadID: query.Get("uploadId")})
		f.mu.Unlock()
		w.WriteHeader(nethttp.StatusNoContent)

	case r.Method == nethttp.MethodGet && query.Get("uploadId") != "":
		if !f.listPartsLive {
			writeXML(w, nethttp.StatusNotFound,
				`<Error><Code>NoSuchUpload</Code><Message>upload does not exist</Message></Error>`)
			return
		}
		writeXML(w, nethttp.StatusOK, fmt.Sprintf(
			`<ListPartsResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></ListPartsResult>`,
			testBucket, key, query.Get("uploadId")))

	default:
		writeXML(w, nethttp.StatusNotImplemented,
			`<Error><Code>NotImplemented</Code><Message>unexpected request</Message></Error>`)
	}
}

// hashRequestPayload returns the size and hash of the part body a request
// carries, unwrapping the aws-chunked framing the SDK adds when it appends a
// trailing checksum. Framing bytes are not part of the object, so counting them
// would make a truncated upload look complete.
func hashRequestPayload(r *nethttp.Request) (int64, [32]byte, error) {
	hasher := sha256.New()
	var (
		size int64
		err  error
	)
	if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") ||
		r.Header.Get("X-Amz-Decoded-Content-Length") != "" {
		size, err = copyAWSChunked(hasher, r.Body)
	} else {
		size, err = io.Copy(hasher, r.Body)
	}
	var sum [32]byte
	copy(sum[:], hasher.Sum(nil))
	return size, sum, err
}

func copyAWSChunked(dst io.Writer, src io.Reader) (int64, error) {
	reader := bufio.NewReader(src)
	var total int64
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return total, err
		}
		header := strings.TrimSpace(line)
		if semicolon := strings.IndexByte(header, ';'); semicolon >= 0 {
			header = header[:semicolon]
		}
		if header == "" {
			continue
		}
		size, err := strconv.ParseInt(header, 16, 64)
		if err != nil {
			return total, fmt.Errorf("bad aws-chunked header %q: %w", header, err)
		}
		if size == 0 {
			return total, nil
		}
		n, err := io.CopyN(dst, reader, size)
		total += n
		if err != nil {
			return total, err
		}
		if _, err := reader.Discard(2); err != nil { // trailing CRLF
			return total, err
		}
	}
}

func writeXML(w nethttp.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header+body)
}

// partSizes returns the staged part sizes in part-number order.
func (f *fakeS3Backend) partSizes(t *testing.T) []int64 {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	sizes := make([]int64, len(f.parts))
	for number, part := range f.parts {
		if number < 1 || int(number) > len(sizes) {
			t.Fatalf("part number %d is outside 1..%d", number, len(sizes))
		}
		sizes[number-1] = part.size
	}
	return sizes
}

func (f *fakeS3Backend) totalStagedBytes() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total int64
	for _, part := range f.parts {
		total += part.size
	}
	return total
}

// redirectingHTTPClient sends every request to addr whatever hostname the SDK
// resolved. The provider refreshes credentials before every attempt, and that
// rebuilds the SDK client against the real S3 endpoint template, so overriding
// only the first client's endpoint would point the second call at real S3.
func redirectingHTTPClient(addr string) *nethttp.Client {
	return &nethttp.Client{
		Transport: &nethttp.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// newFakeCredentialsAPI stands in for the Rescale API's credential endpoint,
// which the provider calls through the shared credential manager before every
// attempt.
func newFakeCredentialsAPI(t *testing.T) *api.Client {
	t.Helper()
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"storageType":"S3Storage","accessKey":"test-key","secretKey":"test-secret","sessionToken":"test-token"}`)
	}))
	t.Cleanup(server.Close)
	return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
}

func newTestS3Client(t *testing.T, server *httptest.Server) *S3Client {
	t.Helper()
	httpClient := redirectingHTTPClient(server.Listener.Addr().String())
	apiClient := newFakeCredentialsAPI(t)

	return &S3Client{
		client: awss3.New(awss3.Options{
			Region:      "us-east-1",
			Credentials: awscreds.NewStaticCredentialsProvider("test-key", "test-secret", ""),
			HTTPClient:  httpClient,
		}),
		storageInfo: &models.StorageInfo{
			StorageType: "S3Storage",
			ConnectionSettings: models.ConnectionSettings{
				Container: testBucket,
				PathBase:  testPathBase,
				Region:    "us-east-1",
			},
		},
		credManager: credentials.GetManager(apiClient),
		apiClient:   apiClient,
		httpClient:  httpClient,
	}
}

// oversizedPartSize is one MiB past the pooled buffer size. Every part size the
// planner picks for a file of 1 GB or more is larger than the pool's buffers, so
// this is the smallest size that reproduces what those uploads hit without
// putting a gigabyte through the test.
var oversizedPartSize = int64(constants.ChunkSize + constants.PartSizeAlignment)

// writeTestFile writes size bytes of position-dependent data, so a part that
// lands at the wrong offset does not hash the same as the right one.
func writeTestFile(t *testing.T, path string, size int64) []byte {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*31 + 7)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	return data
}

// expectedPartHashes splits data the way a correct reader would.
func expectedPartHashes(data []byte, partSize int64) [][32]byte {
	var hashes [][32]byte
	for offset := int64(0); offset < int64(len(data)); offset += partSize {
		end := offset + partSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		hashes = append(hashes, sha256.Sum256(data[offset:end]))
	}
	return hashes
}

func (f *fakeS3Backend) assertPartsMatch(t *testing.T, want [][32]byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.parts) != len(want) {
		t.Fatalf("staged %d parts, want %d", len(f.parts), len(want))
	}
	for i, wantSum := range want {
		got, ok := f.parts[int32(i+1)]
		if !ok {
			t.Fatalf("part %d was never staged", i+1)
		}
		if got.sum != wantSum {
			t.Errorf("part %d holds different bytes than the file at that offset", i+1)
		}
	}
}

// multiThreadedHandle returns a transfer handle with more than one thread. The
// thread count comes from the pool's view of a large file, which is independent
// of how many bytes the test actually pushes through the reader.
func multiThreadedHandle(t *testing.T) *internaltransfer.Transfer {
	t.Helper()
	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads:   8,
		AutoScale:    true,
		CPUCores:     8,
		MemoryBudget: 8 * 1024 * 1024 * 1024,
	})
	handle := internaltransfer.NewManager(resourceMgr).AllocateTransfer(2*constants.LargeFile1GB, 1)
	if handle.GetThreads() <= 1 {
		t.Fatalf("expected a multi-threaded handle, got %d thread(s)", handle.GetThreads())
	}
	return handle
}

func testUploadParams(t *testing.T, localPath, encryptedPath string, plan *resources.UploadPlan) transfer.EncryptedFileUploadParams {
	t.Helper()
	return transfer.EncryptedFileUploadParams{
		LocalPath:     localPath,
		EncryptedPath: encryptedPath,
		EncryptionKey: make([]byte, 32),
		IV:            make([]byte, 16),
		RandomSuffix:  "suffix",
		Plan:          plan,
	}
}

// TestPreEncryptConcurrentUploadsEveryPart is the regression for the concurrent
// producer: it read into a fixed 32 MB pooled buffer while the part size was
// larger, so the first read came back short, the "short read means last part"
// break fired, and a 32 MB object was completed and registered as the whole
// file. With a right-sized buffer every part is read and sent.
func TestPreEncryptConcurrentUploadsEveryPart(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	s3Client := newTestS3Client(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

	encryptedSize := 2*oversizedPartSize + 4*1024*1024
	data := writeTestFile(t, encryptedPath, encryptedSize)
	writeTestFile(t, localPath, encryptedSize)

	handle := multiThreadedHandle(t)
	params := testUploadParams(t, localPath, encryptedPath, &resources.UploadPlan{
		PartSize:   oversizedPartSize,
		WorkerCap:  4,
		QueueDepth: 4,
	})
	params.TransferHandle = handle

	provider := &Provider{}
	objectKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)
	if err := provider.uploadEncryptedMultipartConcurrent(context.Background(), s3Client, params, objectKey, encryptedSize); err != nil {
		t.Fatalf("concurrent upload failed: %v", err)
	}

	wantParts := expectedPartHashes(data, oversizedPartSize)
	if len(wantParts) != 3 {
		t.Fatalf("test setup expects 3 parts, computed %d", len(wantParts))
	}

	if got := backend.totalStagedBytes(); got != encryptedSize {
		t.Errorf("staged %d bytes, want the whole %d-byte file", got, encryptedSize)
	}
	wantSizes := []int64{oversizedPartSize, oversizedPartSize, encryptedSize - 2*oversizedPartSize}
	if got := backend.partSizes(t); !slices.Equal(got, wantSizes) {
		t.Errorf("part sizes = %v, want %v", got, wantSizes)
	}
	backend.assertPartsMatch(t, wantParts)

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.commits != 1 {
		t.Fatalf("CompleteMultipartUpload called %d times, want 1", backend.commits)
	}
	if len(backend.committed) != len(wantParts) {
		t.Errorf("completed with %d parts, want %d", len(backend.committed), len(wantParts))
	}
	for i, number := range backend.committed {
		if number != int32(i+1) {
			t.Errorf("completion part %d is numbered %d", i+1, number)
		}
	}
	if len(backend.aborts) != 0 {
		t.Errorf("upload was aborted: %+v", backend.aborts)
	}
}

// TestPreEncryptSequentialUploadsEveryPart covers the path that was already
// right, so a future change cannot regress it into the concurrent path's bug.
func TestPreEncryptSequentialUploadsEveryPart(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	s3Client := newTestS3Client(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

	encryptedSize := 2*oversizedPartSize + 4*1024*1024
	data := writeTestFile(t, encryptedPath, encryptedSize)
	writeTestFile(t, localPath, encryptedSize)

	params := testUploadParams(t, localPath, encryptedPath, &resources.UploadPlan{PartSize: oversizedPartSize})

	provider := &Provider{}
	objectKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)
	if err := provider.uploadEncryptedMultipart(context.Background(), s3Client, params, objectKey, encryptedSize); err != nil {
		t.Fatalf("sequential upload failed: %v", err)
	}

	backend.assertPartsMatch(t, expectedPartHashes(data, oversizedPartSize))
	if got := backend.totalStagedBytes(); got != encryptedSize {
		t.Errorf("staged %d bytes, want the whole %d-byte file", got, encryptedSize)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.commits != 1 {
		t.Errorf("CompleteMultipartUpload called %d times, want 1", backend.commits)
	}
}

// TestPreEncryptRefusesToCompleteShortUpload feeds each path a file shorter than
// the size it was told to upload — the shape any reader or sizing drift produces.
// Neither may complete the upload.
func TestPreEncryptRefusesToCompleteShortUpload(t *testing.T) {
	tests := []struct {
		name       string
		concurrent bool
	}{
		{name: "sequential"},
		{name: "concurrent", concurrent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeS3Backend(t)
			s3Client := newTestS3Client(t, server)

			tmpDir := t.TempDir()
			localPath := filepath.Join(tmpDir, "source.dat")
			encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

			actualSize := oversizedPartSize + 1024*1024
			writeTestFile(t, encryptedPath, actualSize)
			writeTestFile(t, localPath, actualSize)

			// The caller believes the encrypted file is a part longer than it is.
			claimedSize := actualSize + oversizedPartSize

			params := testUploadParams(t, localPath, encryptedPath, &resources.UploadPlan{
				PartSize:   oversizedPartSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			provider := &Provider{}
			objectKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)

			var err error
			if tt.concurrent {
				params.TransferHandle = multiThreadedHandle(t)
				err = provider.uploadEncryptedMultipartConcurrent(context.Background(), s3Client, params, objectKey, claimedSize)
			} else {
				err = provider.uploadEncryptedMultipart(context.Background(), s3Client, params, objectKey, claimedSize)
			}

			if err == nil {
				t.Fatal("a short upload was completed instead of failing")
			}
			if !strings.Contains(err.Error(), "upload incomplete") {
				t.Errorf("error %q does not say the upload was incomplete", err)
			}

			backend.mu.Lock()
			defer backend.mu.Unlock()
			if backend.commits != 0 {
				t.Errorf("CompleteMultipartUpload was called %d times for an incomplete upload", backend.commits)
			}
			if len(backend.aborts) == 0 {
				t.Error("the abandoned multipart upload was not aborted")
			}
		})
	}
}

// TestPreEncryptConcurrentDiscardsResumeStateForAnotherObject pins the resume
// guard. Every attempt regenerates the key, IV and object suffix, so a state file
// left by an earlier attempt describes an upload of different ciphertext. Even
// when that upload is still live on S3, resuming it would mix two encryptions
// into one object.
func TestPreEncryptConcurrentDiscardsResumeStateForAnotherObject(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	backend.listPartsLive = true // the stale upload would pass the liveness probe
	s3Client := newTestS3Client(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

	encryptedSize := 2*oversizedPartSize + 4*1024*1024
	data := writeTestFile(t, encryptedPath, encryptedSize)
	writeTestFile(t, localPath, encryptedSize)

	staleKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), "previous-suffix")
	writeResumeState(t, localPath, &state.UploadResumeState{
		LocalPath:      localPath,
		EncryptedPath:  encryptedPath,
		ObjectKey:      staleKey,
		UploadID:       "stale-upload-id",
		TotalSize:      encryptedSize,
		OriginalSize:   encryptedSize,
		UploadedBytes:  oversizedPartSize,
		CompletedParts: []state.CompletedPart{{PartNumber: 1, ETag: "stale-etag"}},
		RandomSuffix:   "previous-suffix",
		CreatedAt:      time.Now(),
		LastUpdate:     time.Now(),
		StorageType:    "S3Storage",
	})

	params := testUploadParams(t, localPath, encryptedPath, &resources.UploadPlan{
		PartSize:   oversizedPartSize,
		WorkerCap:  4,
		QueueDepth: 4,
	})
	params.TransferHandle = multiThreadedHandle(t)

	provider := &Provider{}
	objectKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)
	if err := provider.uploadEncryptedMultipartConcurrent(context.Background(), s3Client, params, objectKey, encryptedSize); err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	// The whole file went up under the new object key, starting from part 1.
	backend.assertPartsMatch(t, expectedPartHashes(data, oversizedPartSize))
	if got := backend.totalStagedBytes(); got != encryptedSize {
		t.Errorf("staged %d bytes, want the whole %d-byte file", got, encryptedSize)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	var abortedStale bool
	for _, abort := range backend.aborts {
		if abort.uploadID == "stale-upload-id" && abort.key == staleKey {
			abortedStale = true
		}
	}
	if !abortedStale {
		t.Errorf("the orphaned upload was left open on S3: aborts = %+v", backend.aborts)
	}
	if state.UploadResumeStateExists(localPath) {
		t.Error("the stale resume state file survived the upload")
	}
}

// TestPreEncryptSequentialIgnoresResumeStateForAnotherObject is the same pin for
// the sequential path, which has always compared object keys: the branch stays
// untaken when the suffix regenerates.
func TestPreEncryptSequentialIgnoresResumeStateForAnotherObject(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	s3Client := newTestS3Client(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

	encryptedSize := 2*oversizedPartSize + 4*1024*1024
	data := writeTestFile(t, encryptedPath, encryptedSize)
	writeTestFile(t, localPath, encryptedSize)

	writeResumeState(t, localPath, &state.UploadResumeState{
		LocalPath:      localPath,
		EncryptedPath:  encryptedPath,
		ObjectKey:      state.BuildObjectKey(testPathBase, filepath.Base(localPath), "previous-suffix"),
		UploadID:       "stale-upload-id",
		TotalSize:      encryptedSize,
		OriginalSize:   encryptedSize,
		UploadedBytes:  oversizedPartSize,
		CompletedParts: []state.CompletedPart{{PartNumber: 1, ETag: "stale-etag"}},
		RandomSuffix:   "previous-suffix",
		CreatedAt:      time.Now(),
		LastUpdate:     time.Now(),
		StorageType:    "S3Storage",
	})

	params := testUploadParams(t, localPath, encryptedPath, &resources.UploadPlan{PartSize: oversizedPartSize})
	provider := &Provider{}
	objectKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)
	if err := provider.uploadEncryptedMultipart(context.Background(), s3Client, params, objectKey, encryptedSize); err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	backend.assertPartsMatch(t, expectedPartHashes(data, oversizedPartSize))
	if got := backend.totalStagedBytes(); got != encryptedSize {
		t.Errorf("staged %d bytes, want the whole %d-byte file", got, encryptedSize)
	}
}

// TestPreEncryptPlanRespectsS3PartLimit checks the sizing the four call sites
// share: huge files get parts large enough to stay inside S3's part ceiling,
// files of today's sizes keep the part size they already had, and a file no
// part size can cover is refused.
func TestPreEncryptPlanRespectsS3PartLimit(t *testing.T) {
	provider := &Provider{}
	limits := provider.UploadLimits()

	t.Run("huge file stays within the part ceiling", func(t *testing.T) {
		const fileSize = int64(4300) * 1024 * 1024 * 1024 // ~4.2 TB
		plan, err := transfer.EncryptedFileUploadParams{}.UploadPlan(fileSize, limits)
		if err != nil {
			t.Fatalf("planning a 4.2 TB upload failed: %v", err)
		}
		if parts := transfer.CalculateTotalParts(fileSize, plan.PartSize); parts > constants.MaxS3UploadParts {
			t.Errorf("plan needs %d parts, above S3's limit of %d", parts, constants.MaxS3UploadParts)
		}
	})

	t.Run("ordinary file keeps its part size", func(t *testing.T) {
		const fileSize = int64(2 * 1024 * 1024 * 1024) // 2 GB
		plan, err := transfer.EncryptedFileUploadParams{}.UploadPlan(fileSize, limits)
		if err != nil {
			t.Fatalf("planning a 2 GB upload failed: %v", err)
		}
		want := resources.CalculateDynamicChunkSize(fileSize, constants.MaxThreadsPerFile)
		if plan.PartSize != want {
			t.Errorf("part size = %d, want the unchanged %d", plan.PartSize, want)
		}
	})

	t.Run("file too large for the backend is refused", func(t *testing.T) {
		const fileSize = int64(60 * 1024 * 1024 * 1024 * 1024) // 60 TB
		_, err := transfer.EncryptedFileUploadParams{}.UploadPlan(fileSize, limits)
		if err == nil {
			t.Fatal("expected a file beyond S3's capacity to be refused")
		}
		if !strings.Contains(err.Error(), "too large") {
			t.Errorf("error %q does not explain that the file is too large", err)
		}
	})
}

// TestPreEncryptRejectsOversizedFileBeforeAnyRequest is the fail-fast
// requirement: the refusal has to happen before a multipart upload is opened.
func TestPreEncryptRejectsOversizedFileBeforeAnyRequest(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	s3Client := newTestS3Client(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")
	writeTestFile(t, encryptedPath, 1024)
	writeTestFile(t, localPath, 1024)

	// No plan, so the provider plans on the spot — against a size S3 cannot hold.
	params := testUploadParams(t, localPath, encryptedPath, nil)
	provider := &Provider{}
	objectKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)

	err := provider.uploadEncryptedMultipart(context.Background(), s3Client, params, objectKey, 60*1024*1024*1024*1024)
	if err == nil {
		t.Fatal("expected an oversized file to be refused")
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.requests != 0 {
		t.Errorf("%d request(s) reached the backend before the file was refused", backend.requests)
	}
}

func writeResumeState(t *testing.T, localPath string, resumeState *state.UploadResumeState) {
	t.Helper()
	data, err := json.Marshal(resumeState)
	if err != nil {
		t.Fatalf("failed to marshal resume state: %v", err)
	}
	if err := os.WriteFile(localPath+".upload.resume", data, 0600); err != nil {
		t.Fatalf("failed to write resume state: %v", err)
	}
}

// TestPreEncryptConcurrentHonorsPlanWorkerCap checks that narrowing the pipeline
// narrows it rather than stopping it. A cap of zero is a caller mistake rather
// than a plan the planner produces, and it is in the table because starting no
// workers at all would hang the producer instead of failing.
func TestPreEncryptConcurrentHonorsPlanWorkerCap(t *testing.T) {
	tests := []struct {
		name      string
		workerCap int
	}{
		{name: "single worker", workerCap: 1},
		{name: "no cap in the plan", workerCap: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeS3Backend(t)
			s3Client := newTestS3Client(t, server)

			tmpDir := t.TempDir()
			localPath := filepath.Join(tmpDir, "source.dat")
			encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

			encryptedSize := 2*oversizedPartSize + 4*1024*1024
			data := writeTestFile(t, encryptedPath, encryptedSize)
			writeTestFile(t, localPath, encryptedSize)

			params := testUploadParams(t, localPath, encryptedPath, &resources.UploadPlan{
				PartSize:   oversizedPartSize,
				WorkerCap:  tt.workerCap,
				QueueDepth: 2,
			})
			params.TransferHandle = multiThreadedHandle(t)

			provider := &Provider{}
			objectKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)
			if err := provider.uploadEncryptedMultipartConcurrent(context.Background(), s3Client, params, objectKey, encryptedSize); err != nil {
				t.Fatalf("concurrent upload failed: %v", err)
			}

			if got := backend.totalStagedBytes(); got != encryptedSize {
				t.Errorf("staged %d bytes, want the whole %d-byte file", got, encryptedSize)
			}
			backend.assertPartsMatch(t, expectedPartHashes(data, oversizedPartSize))
		})
	}
}
