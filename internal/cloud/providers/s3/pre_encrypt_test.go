package s3

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
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
	"github.com/rescale/rescale-int/internal/cloud/providers/testsupport"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/resources"
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

	// listPartsBroken answers a resume probe with a failure rather than an
	// answer: the upload may or may not still be there.
	listPartsBroken bool

	// creates counts CreateMultipartUpload calls so every upload this backend
	// opens has an ID of its own, which is what tells a fresh upload apart from
	// the one a planted checkpoint names.
	creates int

	// goneUploads are the IDs the service no longer holds: an upload whose
	// completion succeeded but whose response was lost, or one its seven-day
	// expiry swept. Listing or completing one answers NoSuchUpload.
	goneUploads map[string]bool

	// failCompletion refuses CompleteMultipartUpload with an error the retry
	// classifier reads as fatal, the shape a rejected part list arrives in.
	failCompletion bool

	// rejectOncePerPart fails the first attempt at each part with an
	// authentication error, the shape a rejected credential arrives in.
	rejectOncePerPart bool
	rejectedParts     map[int32]bool

	// refusePartsFrom fails every attempt at this part number and above with an
	// error the retry classifier reads as fatal, which is how an attempt gives
	// up with an upload already open and parts already staged. Zero refuses
	// nothing.
	refusePartsFrom int32
}

// createdUploadID names the nth multipart upload this backend opened. A planted
// checkpoint names testUploadID, so no upload the backend creates is ever the
// one a checkpoint describes.
func createdUploadID(n int) string {
	return fmt.Sprintf("created-upload-%d", n)
}

func newFakeS3Backend(t *testing.T) (*fakeS3Backend, *httptest.Server) {
	t.Helper()
	backend := &fakeS3Backend{
		parts:         make(map[int32]stagedPart),
		rejectedParts: make(map[int32]bool),
		goneUploads:   make(map[string]bool),
	}
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
		f.mu.Lock()
		f.creates++
		created := createdUploadID(f.creates)
		f.mu.Unlock()
		writeXML(w, nethttp.StatusOK, fmt.Sprintf(
			`<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`,
			testBucket, key, created))

	case r.Method == nethttp.MethodPut && query.Get("partNumber") != "":
		partNumber, err := strconv.Atoi(query.Get("partNumber"))
		if err != nil {
			w.WriteHeader(nethttp.StatusBadRequest)
			return
		}
		f.mu.Lock()
		reject := f.rejectOncePerPart && !f.rejectedParts[int32(partNumber)]
		if reject {
			f.rejectedParts[int32(partNumber)] = true
		}
		f.mu.Unlock()
		if reject {
			_, _ = io.Copy(io.Discard, r.Body)
			writeXML(w, nethttp.StatusForbidden,
				`<Error><Code>ExpiredToken</Code><Message>The provided token has expired</Message></Error>`)
			return
		}
		if f.refusePartsFrom > 0 && int32(partNumber) >= f.refusePartsFrom {
			_, _ = io.Copy(io.Discard, r.Body)
			writeXML(w, nethttp.StatusBadRequest,
				`<Error><Code>InvalidPart</Code><Message>the part was refused</Message></Error>`)
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
		f.mu.Lock()
		gone := f.goneUploads[query.Get("uploadId")]
		f.mu.Unlock()
		if gone {
			_, _ = io.Copy(io.Discard, r.Body)
			writeXML(w, nethttp.StatusNotFound,
				`<Error><Code>NoSuchUpload</Code><Message>upload does not exist</Message></Error>`)
			return
		}
		if f.failCompletion {
			_, _ = io.Copy(io.Discard, r.Body)
			writeXML(w, nethttp.StatusBadRequest,
				`<Error><Code>MalformedXML</Code><Message>the part list was rejected</Message></Error>`)
			return
		}
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
		if f.listPartsBroken {
			// Refused outright rather than with a retryable code: the probe gets
			// no answer, and the test does not spend the retry budget finding
			// that out.
			writeXML(w, nethttp.StatusBadRequest,
				`<Error><Code>InvalidRequest</Code><Message>the request could not be answered</Message></Error>`)
			return
		}
		f.mu.Lock()
		gone := f.goneUploads[query.Get("uploadId")]
		f.mu.Unlock()
		if !f.listPartsLive || gone {
			writeXML(w, nethttp.StatusNotFound,
				`<Error><Code>NoSuchUpload</Code><Message>upload does not exist</Message></Error>`)
			return
		}
		writeXML(w, nethttp.StatusOK, fmt.Sprintf(
			`<ListPartsResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></ListPartsResult>`,
			testBucket, key, query.Get("uploadId")))

	case r.Method == nethttp.MethodPut:
		// Single-request upload, which is the route a file below the multipart
		// threshold takes.
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("ETag", `"single"`)
		w.WriteHeader(nethttp.StatusOK)

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

// stagedPartNumbers returns the part numbers that reached the wire, in order.
func (f *fakeS3Backend) stagedPartNumbers() []int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	numbers := make([]int32, 0, len(f.parts))
	for number := range f.parts {
		numbers = append(numbers, number)
	}
	slices.Sort(numbers)
	return numbers
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
	return newTestS3ClientWithAPI(t, server, newFakeCredentialsAPI(t))
}

func newTestS3ClientWithAPI(t *testing.T, server *httptest.Server, apiClient *api.Client) *S3Client {
	t.Helper()
	httpClient := testsupport.RedirectingHTTPClient(server.Listener.Addr().String())

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

// resumePartSize is an ordinary part size, above S3's five-megabyte floor. The
// tests that use it are about which parts reach the wire rather than about the
// pooled buffer, so they do not need the oversized parts above.
const resumePartSize = int64(8 * 1024 * 1024)

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
	data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
	testsupport.WriteTestFile(t, localPath, encryptedSize)

	handle := testsupport.MultiThreadedHandle(t)
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
	data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
	testsupport.WriteTestFile(t, localPath, encryptedSize)

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
			testsupport.WriteTestFile(t, encryptedPath, actualSize)
			testsupport.WriteTestFile(t, localPath, actualSize)

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
				params.TransferHandle = testsupport.MultiThreadedHandle(t)
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
	data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
	testsupport.WriteTestFile(t, localPath, encryptedSize)

	staleKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), "previous-suffix")
	testsupport.WriteResumeState(t, localPath, &state.UploadResumeState{
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
	params.TransferHandle = testsupport.MultiThreadedHandle(t)

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
	data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
	testsupport.WriteTestFile(t, localPath, encryptedSize)

	testsupport.WriteResumeState(t, localPath, &state.UploadResumeState{
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
	testsupport.WriteTestFile(t, encryptedPath, 1024)
	testsupport.WriteTestFile(t, localPath, 1024)

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
			data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
			testsupport.WriteTestFile(t, localPath, encryptedSize)

			params := testUploadParams(t, localPath, encryptedPath, &resources.UploadPlan{
				PartSize:   oversizedPartSize,
				WorkerCap:  tt.workerCap,
				QueueDepth: 2,
			})
			params.TransferHandle = testsupport.MultiThreadedHandle(t)

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

// TestPreEncryptConcurrentResumesMatchingUpload is the other half of F10: once
// the orchestrator hands the provider back the identity of the interrupted
// attempt, the parts already accepted must not be sent again. Only the missing
// ones go over the wire, and the completion still assembles the whole object.
func TestPreEncryptConcurrentResumesMatchingUpload(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	backend.listPartsLive = true // the interrupted upload is still open on S3
	s3Client := newTestS3Client(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

	encryptedSize := 2*oversizedPartSize + 4*1024*1024
	data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
	testsupport.WriteTestFile(t, localPath, encryptedSize)

	params := testUploadParams(t, localPath, encryptedPath, &resources.UploadPlan{
		PartSize:   oversizedPartSize,
		WorkerCap:  4,
		QueueDepth: 4,
	})
	params.TransferHandle = testsupport.MultiThreadedHandle(t)

	sourceInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	params.SourceModTime = sourceInfo.ModTime()

	objectKey := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)
	testsupport.WriteResumeState(t, localPath, &state.UploadResumeState{
		LocalPath:      localPath,
		EncryptedPath:  encryptedPath,
		ObjectKey:      objectKey,
		UploadID:       testUploadID,
		TotalSize:      encryptedSize,
		OriginalSize:   encryptedSize,
		SourceModTime:  params.SourceModTime,
		UploadedBytes:  oversizedPartSize,
		CompletedParts: []state.CompletedPart{{PartNumber: 1, ETag: "etag-1"}},
		PartSize:       oversizedPartSize,
		RandomSuffix:   params.RandomSuffix,
		CreatedAt:      time.Now(),
		LastUpdate:     time.Now(),
		StorageType:    "S3Storage",
	})

	provider := &Provider{}
	if err := provider.uploadEncryptedMultipartConcurrent(context.Background(), s3Client, params, objectKey, encryptedSize); err != nil {
		t.Fatalf("resumed upload failed: %v", err)
	}

	// Part 1 was already on S3; staging it again would pay for it twice.
	wantParts := expectedPartHashes(data, oversizedPartSize)
	backend.mu.Lock()
	defer backend.mu.Unlock()

	if len(backend.parts) != 2 {
		t.Fatalf("staged %d parts, want only the 2 that were missing", len(backend.parts))
	}
	for _, number := range []int32{2, 3} {
		staged, ok := backend.parts[number]
		if !ok {
			t.Fatalf("part %d was never staged", number)
		}
		if staged.sum != wantParts[number-1] {
			t.Errorf("part %d holds different bytes than the file at that offset", number)
		}
	}
	if _, restaged := backend.parts[1]; restaged {
		t.Error("part 1 was uploaded again even though the resume state listed it as complete")
	}

	if backend.commits != 1 {
		t.Fatalf("CompleteMultipartUpload called %d times, want 1", backend.commits)
	}
	if len(backend.committed) != len(wantParts) {
		t.Errorf("completed with %d parts, want the whole %d-part object", len(backend.committed), len(wantParts))
	}
	if len(backend.aborts) != 0 {
		t.Errorf("the resumed upload was aborted: %+v", backend.aborts)
	}
	if state.UploadResumeStateExists(localPath) {
		t.Error("the resume state survived a completed upload")
	}
}

// s3ResumeFixture is a source, its ciphertext and the params the next attempt
// runs with, shared by the resume-geometry tests below.
type s3ResumeFixture struct {
	localPath     string
	encryptedPath string
	data          []byte
	params        transfer.EncryptedFileUploadParams
	objectKey     string
	// createdAt is when the interrupted attempt opened its upload, which is what
	// the seven-day expiry both backends enforce is measured from.
	createdAt time.Time
}

func newS3ResumeFixture(t *testing.T, encryptedSize int64, plan *resources.UploadPlan) *s3ResumeFixture {
	t.Helper()

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")
	data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
	testsupport.WriteTestFile(t, localPath, encryptedSize)

	params := testUploadParams(t, localPath, encryptedPath, plan)
	sourceInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	params.SourceModTime = sourceInfo.ModTime()

	return &s3ResumeFixture{
		localPath:     localPath,
		encryptedPath: encryptedPath,
		data:          data,
		params:        params,
		objectKey:     state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix),
		createdAt:     time.Now(),
	}
}

// writeState plants the checkpoint an interrupted attempt left behind.
func (f *s3ResumeFixture) writeState(t *testing.T, partSize int64, completed []state.CompletedPart, uploadedBytes int64) {
	t.Helper()
	testsupport.WriteResumeState(t, f.localPath, &state.UploadResumeState{
		LocalPath:      f.localPath,
		EncryptedPath:  f.encryptedPath,
		ObjectKey:      f.objectKey,
		UploadID:       testUploadID,
		TotalSize:      int64(len(f.data)),
		OriginalSize:   int64(len(f.data)),
		SourceModTime:  f.params.SourceModTime,
		UploadedBytes:  uploadedBytes,
		CompletedParts: completed,
		PartSize:       partSize,
		RandomSuffix:   f.params.RandomSuffix,
		CreatedAt:      f.createdAt,
		LastUpdate:     time.Now(),
		StorageType:    "S3Storage",
	})
}

func (f *s3ResumeFixture) run(t *testing.T, s3Client *S3Client, concurrent bool) error {
	t.Helper()
	provider := &Provider{}
	if concurrent {
		f.params.TransferHandle = testsupport.MultiThreadedHandle(t)
		return provider.uploadEncryptedMultipartConcurrent(context.Background(), s3Client, f.params, f.objectKey, int64(len(f.data)))
	}
	return provider.uploadEncryptedMultipart(context.Background(), s3Client, f.params, f.objectKey, int64(len(f.data)))
}

// TestPreEncryptResumesPartsWithAGap is the out-of-order half of the resume.
// Parts finish in whatever order the backend answers, so the recorded list is
// not a prefix: resuming after len(CompletedParts) re-reads from the wrong
// offset and re-sends a part number that is already taken, and the completeness
// guard then refuses the retry instead of repairing it.
func TestPreEncryptResumesPartsWithAGap(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeS3Backend(t)
			backend.listPartsLive = true
			s3Client := newTestS3Client(t, server)

			encryptedSize := 3*oversizedPartSize + 4*1024*1024
			fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   oversizedPartSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			// Recorded in completion order, which is what the concurrent path
			// writes: part 4 answered first, and part 3 never landed.
			fixture.writeState(t, oversizedPartSize, []state.CompletedPart{
				{PartNumber: 4, ETag: "etag-4"},
				{PartNumber: 1, ETag: "etag-1"},
				{PartNumber: 2, ETag: "etag-2"},
			}, encryptedSize-oversizedPartSize)

			if err := fixture.run(t, s3Client, tt.concurrent); err != nil {
				t.Fatalf("resumed upload failed: %v", err)
			}

			if got := backend.stagedPartNumbers(); !slices.Equal(got, []int32{3}) {
				t.Errorf("staged parts %v, want only the missing part 3", got)
			}
			wantParts := expectedPartHashes(fixture.data, oversizedPartSize)
			backend.mu.Lock()
			defer backend.mu.Unlock()
			if staged, ok := backend.parts[3]; !ok {
				t.Fatal("the missing part was never staged")
			} else if staged.sum != wantParts[2] {
				t.Error("part 3 holds different bytes than the file at that offset")
			}
			if !slices.Equal(backend.committed, []int32{1, 2, 3, 4}) {
				t.Errorf("completed with parts %v, want the whole object in order", backend.committed)
			}
			if backend.commits != 1 {
				t.Errorf("CompleteMultipartUpload called %d times, want 1", backend.commits)
			}
			if len(backend.aborts) != 0 {
				t.Errorf("the resumed upload was aborted: %+v", backend.aborts)
			}
		})
	}
}

// TestPreEncryptResumeUsesSavedPartSize pins the geometry to the checkpoint.
// The plan is recomputed on every attempt and a different memory budget yields a
// different part size, but the parts already on the backend were cut with the
// old one: reading the file with the new size lines nothing up.
func TestPreEncryptResumeUsesSavedPartSize(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeS3Backend(t)
			backend.listPartsLive = true
			s3Client := newTestS3Client(t, server)

			savedPartSize := oversizedPartSize
			encryptedSize := 2*savedPartSize + 4*1024*1024
			// This attempt plans smaller parts than the interrupted one used.
			fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   savedPartSize / 2,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			fixture.writeState(t, savedPartSize, []state.CompletedPart{
				{PartNumber: 1, ETag: "etag-1"},
			}, savedPartSize)

			if err := fixture.run(t, s3Client, tt.concurrent); err != nil {
				t.Fatalf("resumed upload failed: %v", err)
			}

			if got := backend.stagedPartNumbers(); !slices.Equal(got, []int32{2, 3}) {
				t.Errorf("staged parts %v, want the 2 the checkpoint was missing", got)
			}
			wantParts := expectedPartHashes(fixture.data, savedPartSize)
			backend.mu.Lock()
			defer backend.mu.Unlock()
			for _, number := range []int32{2, 3} {
				staged, ok := backend.parts[number]
				if !ok {
					t.Fatalf("part %d was never staged", number)
				}
				if staged.sum != wantParts[number-1] {
					t.Errorf("part %d was cut with the plan's part size, not the checkpoint's", number)
				}
			}
			if !slices.Equal(backend.committed, []int32{1, 2, 3}) {
				t.Errorf("completed with parts %v, want the 3 parts the saved size gives", backend.committed)
			}
		})
	}
}

// TestPreEncryptResumeWithoutPartSizeStartsFresh covers checkpoints written
// before the part size was recorded. Nothing says how the parts already on the
// backend were cut, so continuing them is a guess; the whole file goes up again.
func TestPreEncryptResumeWithoutPartSizeStartsFresh(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeS3Backend(t)
			backend.listPartsLive = true // the old upload would pass the liveness probe
			s3Client := newTestS3Client(t, server)

			encryptedSize := 2*oversizedPartSize + 4*1024*1024
			fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   oversizedPartSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			fixture.writeState(t, 0, []state.CompletedPart{
				{PartNumber: 1, ETag: "etag-1"},
			}, oversizedPartSize)

			if err := fixture.run(t, s3Client, tt.concurrent); err != nil {
				t.Fatalf("upload failed: %v", err)
			}

			if got := backend.stagedPartNumbers(); !slices.Equal(got, []int32{1, 2, 3}) {
				t.Errorf("staged parts %v, want the whole file re-sent", got)
			}
			backend.assertPartsMatch(t, expectedPartHashes(fixture.data, oversizedPartSize))
		})
	}
}

// TestPreEncryptSequentialValidatesResumeState: the sequential path judged a
// checkpoint on the object key and the part geometry alone, so it continued one
// S3 had already dropped — an unfinished multipart upload lives seven days — and
// completed a part list naming parts the service no longer holds. A checkpoint
// the concurrent path wrote lands here whenever the thread count changes between
// runs, and that path has always run this same state validation first.
func TestPreEncryptSequentialValidatesResumeState(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	s3Client := newTestS3Client(t, server)

	encryptedSize := 2*oversizedPartSize + 4*1024*1024
	fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
		PartSize:   oversizedPartSize,
		WorkerCap:  4,
		QueueDepth: 4,
	})
	fixture.createdAt = time.Now().Add(-state.MaxResumeAge - time.Hour)
	fixture.writeState(t, oversizedPartSize, []state.CompletedPart{
		{PartNumber: 1, ETag: "etag-1"},
		{PartNumber: 2, ETag: "etag-2"},
	}, 2*oversizedPartSize)

	if err := fixture.run(t, s3Client, false); err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	if got := backend.stagedPartNumbers(); !slices.Equal(got, []int32{1, 2, 3}) {
		t.Errorf("staged parts %v, want the whole file re-sent after an expired checkpoint", got)
	}
	backend.assertPartsMatch(t, expectedPartHashes(fixture.data, oversizedPartSize))

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if !slices.Equal(backend.committed, []int32{1, 2, 3}) {
		t.Errorf("completed with parts %v, want the whole object", backend.committed)
	}
	for _, number := range backend.committed {
		if _, staged := backend.parts[number]; !staged {
			t.Errorf("completed part %d was never staged", number)
		}
	}
}

// TestPreEncryptConcurrentKeepsMultipartAfterCompletionFailure is the cleanup
// boundary. A rejected CompleteMultipartUpload armed the deferred abort while
// the wrapper kept the state and the ciphertext for the retry, so the checkpoint
// named an upload that no longer existed and the retry had to send every part
// again.
func TestPreEncryptConcurrentKeepsMultipartAfterCompletionFailure(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	backend.failCompletion = true
	s3Client := newTestS3Client(t, server)

	encryptedSize := 2*oversizedPartSize + 4*1024*1024
	fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
		PartSize:   oversizedPartSize,
		WorkerCap:  4,
		QueueDepth: 4,
	})

	if err := fixture.run(t, s3Client, true); err == nil {
		t.Fatal("a rejected completion was reported as success")
	}

	backend.mu.Lock()
	if len(backend.aborts) != 0 {
		t.Errorf("the multipart upload the retry needs was aborted: %+v", backend.aborts)
	}
	backend.mu.Unlock()

	if !state.UploadResumeStateExists(fixture.localPath) {
		t.Fatal("the resume state was discarded, leaving nothing to retry")
	}

	// The retry finds the upload alive and only has to complete it again.
	backend.failCompletion = false
	backend.listPartsLive = true
	if err := fixture.run(t, s3Client, true); err != nil {
		t.Fatalf("the retry could not complete the upload the first attempt staged: %v", err)
	}
	if got := backend.stagedPartNumbers(); !slices.Equal(got, []int32{1, 2, 3}) {
		t.Errorf("staged parts %v across both attempts, want each part sent once", got)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if !slices.Equal(backend.committed, []int32{1, 2, 3}) {
		t.Errorf("completed with parts %v, want the whole object", backend.committed)
	}
}

// TestAbortContextUsesTheAbortDeadline is F-3 at the provider: a detached abort
// bounded by the ten-minute part budget holds the transfer goroutine open long
// after the caller gave up on it.
func TestAbortContextUsesTheAbortDeadline(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	abortCtx, cancelAbort := abortContext(cancelled)
	defer cancelAbort()

	if err := abortCtx.Err(); err != nil {
		t.Fatalf("the abort context inherited the cancellation: %v", err)
	}
	deadline, ok := abortCtx.Deadline()
	if !ok {
		t.Fatal("the abort context carries no deadline")
	}
	if left := time.Until(deadline); left > constants.AbortOperationTimeout {
		t.Errorf("the abort was given %s to run, want at most the %s abort deadline", left, constants.AbortOperationTimeout)
	}
}

// readCheckpoint returns the bytes of the resume state beside a source, which is
// what an attempt that holds no lock on it must leave exactly as it found.
func readCheckpoint(t *testing.T, localPath string) []byte {
	t.Helper()
	data, err := os.ReadFile(localPath + ".upload.resume")
	if err != nil {
		t.Fatalf("failed to read the checkpoint: %v", err)
	}
	return data
}

// TestPreEncryptStatelessAttemptIgnoresTheCheckpoint is D2 at the provider. An
// attempt the orchestrator could not take the upload lock for shares the sidecar
// beside the source with whatever else is running, so it must neither continue
// what that checkpoint describes nor overwrite it.
func TestPreEncryptStatelessAttemptIgnoresTheCheckpoint(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeS3Backend(t)
			backend.listPartsLive = true // the checkpoint would pass the liveness probe
			s3Client := newTestS3Client(t, server)

			encryptedSize := 3 * resumePartSize
			fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   resumePartSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			fixture.writeState(t, resumePartSize, []state.CompletedPart{
				{PartNumber: 1, ETag: "etag-1"},
				{PartNumber: 2, ETag: "etag-2"},
			}, 2*resumePartSize)
			checkpoint := readCheckpoint(t, fixture.localPath)

			fixture.params.Stateless = true
			if err := fixture.run(t, s3Client, tt.concurrent); err != nil {
				t.Fatalf("the stateless upload failed: %v", err)
			}

			if got := backend.stagedPartNumbers(); !slices.Equal(got, []int32{1, 2, 3}) {
				t.Errorf("staged parts %v, want the whole file from an attempt that cannot resume", got)
			}
			if got := readCheckpoint(t, fixture.localPath); !slices.Equal(got, checkpoint) {
				t.Error("the stateless attempt rewrote a checkpoint it holds no lock on")
			}
		})
	}
}

// TestPreEncryptStatelessSingleUploadKeepsTheCheckpoint covers the same rule on
// the canonical entry point, where a file below the multipart threshold goes up
// in one request and the checkpoint is deleted on the way out.
func TestPreEncryptStatelessSingleUploadKeepsTheCheckpoint(t *testing.T) {
	_, server := newFakeS3Backend(t)
	s3Client := newTestS3Client(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")
	testsupport.WriteTestFile(t, encryptedPath, 4096)
	testsupport.WriteTestFile(t, localPath, 4096)
	testsupport.WriteResumeState(t, localPath, &state.UploadResumeState{
		LocalPath:    localPath,
		ObjectKey:    state.BuildObjectKey(testPathBase, "source.dat", "another-suffix"),
		UploadID:     "another-upload-id",
		RandomSuffix: "another-suffix",
		StorageType:  "S3Storage",
	})
	checkpoint := readCheckpoint(t, localPath)

	params := testUploadParams(t, localPath, encryptedPath, nil)
	params.Stateless = true

	provider := &Provider{s3Client: s3Client}
	if _, err := provider.UploadEncryptedFile(context.Background(), params); err != nil {
		t.Fatalf("the stateless upload failed: %v", err)
	}

	if got := readCheckpoint(t, localPath); !slices.Equal(got, checkpoint) {
		t.Error("the stateless attempt deleted a checkpoint it holds no lock on")
	}
}

// TestPreEncryptStartsFreshWhenS3LostTheUpload is D8. The checkpoint records
// what this client did, not what S3 still holds: a completion that succeeded
// while its response was lost leaves one naming an upload ID that is gone, and
// so does an upload swept before its recorded age ran out. Restoring it skips
// the parts, repeats the completion, fails with NoSuchUpload and keeps the same
// checkpoint — which is a loop, not a retry.
func TestPreEncryptStartsFreshWhenS3LostTheUpload(t *testing.T) {
	encryptedSize := 3 * resumePartSize
	for _, shape := range []struct {
		name          string
		completed     []state.CompletedPart
		uploadedBytes int64
	}{
		{
			name: "a completion whose response was lost",
			completed: []state.CompletedPart{
				{PartNumber: 1, ETag: "etag-1"},
				{PartNumber: 2, ETag: "etag-2"},
				{PartNumber: 3, ETag: "etag-3"},
			},
			uploadedBytes: encryptedSize,
		},
		{
			name: "an upload that vanished inside its recorded age",
			completed: []state.CompletedPart{
				{PartNumber: 1, ETag: "etag-1"},
				{PartNumber: 2, ETag: "etag-2"},
			},
			uploadedBytes: 2 * resumePartSize,
		},
	} {
		for _, tt := range []struct {
			name       string
			concurrent bool
		}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
			t.Run(shape.name+"/"+tt.name, func(t *testing.T) {
				backend, server := newFakeS3Backend(t)
				backend.listPartsLive = true
				backend.goneUploads[testUploadID] = true
				s3Client := newTestS3Client(t, server)

				fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
					PartSize:   resumePartSize,
					WorkerCap:  4,
					QueueDepth: 4,
				})
				fixture.writeState(t, resumePartSize, shape.completed, shape.uploadedBytes)

				if err := fixture.run(t, s3Client, tt.concurrent); err != nil {
					t.Fatalf("the attempt after a vanished upload failed, and would fail the same way forever: %v", err)
				}

				if got := backend.stagedPartNumbers(); !slices.Equal(got, []int32{1, 2, 3}) {
					t.Errorf("staged parts %v, want the whole file under a fresh upload", got)
				}
				backend.assertPartsMatch(t, expectedPartHashes(fixture.data, resumePartSize))
				if saved, _ := state.LoadUploadState(fixture.localPath); saved != nil && saved.UploadID == testUploadID {
					t.Error("the checkpoint still names the upload S3 no longer holds, so the next attempt goes back to it")
				}

				backend.mu.Lock()
				defer backend.mu.Unlock()
				if !slices.Equal(backend.committed, []int32{1, 2, 3}) {
					t.Errorf("completed with parts %v, want the whole object", backend.committed)
				}
			})
		}
	}
}

// TestPreEncryptRetiresTheCheckpointOfAnUploadCompletedTwice covers the other
// half of D8: the completion itself is what discovers the upload is gone. The
// parts of an ordinary failed completion are all still on S3 and the checkpoint
// is what lets the retry ask for the assembly again — but a completion that
// fails because the upload no longer exists fails identically on every retry
// for as long as that checkpoint names it.
func TestPreEncryptRetiresTheCheckpointOfAnUploadCompletedTwice(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeS3Backend(t)
			s3Client := newTestS3Client(t, server)

			encryptedSize := 2 * resumePartSize
			fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   resumePartSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			// The upload this attempt opens is gone by the time it asks for the
			// assembly, which is what a completion retried after a lost success
			// meets.
			backend.goneUploads[createdUploadID(1)] = true

			if err := fixture.run(t, s3Client, tt.concurrent); err == nil {
				t.Fatal("completing an upload S3 no longer holds was reported as success")
			}
			if state.UploadResumeStateExists(fixture.localPath) {
				t.Error("the checkpoint of an upload S3 no longer holds was kept, so every retry repeats the same completion")
			}
		})
	}
}

// TestPreEncryptKeepsTheCheckpointWhenTheResumeProbeFails is the other side of
// D8's distinction. A probe that could not be made is not an answer: treating it
// as one re-sends a file whose parts are all still on S3 and leaves that upload
// open until its own expiry. The attempt fails instead, keeping the checkpoint
// the retry resumes from.
func TestPreEncryptKeepsTheCheckpointWhenTheResumeProbeFails(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeS3Backend(t)
			backend.listPartsLive = true
			backend.listPartsBroken = true
			s3Client := newTestS3Client(t, server)

			encryptedSize := 3 * resumePartSize
			fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   resumePartSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			fixture.writeState(t, resumePartSize, []state.CompletedPart{
				{PartNumber: 1, ETag: "etag-1"},
				{PartNumber: 2, ETag: "etag-2"},
			}, 2*resumePartSize)

			if err := fixture.run(t, s3Client, tt.concurrent); err == nil {
				t.Fatal("an unanswered resume probe was treated as an answer")
			}
			if got := backend.stagedPartNumbers(); len(got) != 0 {
				t.Errorf("staged parts %v, want nothing re-sent while the interrupted upload may still hold them", got)
			}
			saved, err := state.LoadUploadState(fixture.localPath)
			if err != nil || saved == nil {
				t.Fatalf("the checkpoint the retry resumes from was discarded: %v", err)
			}
			if saved.UploadID != testUploadID {
				t.Errorf("the checkpoint names upload %q, want the interrupted one", saved.UploadID)
			}
		})
	}
}

// TestPreEncryptIgnoresACheckpointTheCallerRejected is finding 4 at the
// provider. The caller judged the checkpoint beside this source unusable and
// could not delete it, so the record is still on disk while describing an upload
// this attempt must not touch: adopting it continues what the caller refused,
// and retiring it discards an identity this destination may not own. Both paths
// have to read that sidecar as absent — which a checkpoint they WOULD otherwise
// resume is what proves.
func TestPreEncryptIgnoresACheckpointTheCallerRejected(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeS3Backend(t)
			backend.listPartsLive = true // the checkpoint would pass the liveness probe
			s3Client := newTestS3Client(t, server)

			encryptedSize := 3 * resumePartSize
			fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   resumePartSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			fixture.writeState(t, resumePartSize, []state.CompletedPart{
				{PartNumber: 1, ETag: "etag-1"},
				{PartNumber: 2, ETag: "etag-2"},
			}, 2*resumePartSize)

			fixture.params.IgnoreResumeState = true
			if err := fixture.run(t, s3Client, tt.concurrent); err != nil {
				t.Fatalf("the upload failed: %v", err)
			}

			if got := backend.stagedPartNumbers(); !slices.Equal(got, []int32{1, 2, 3}) {
				t.Errorf("staged parts %v, want the whole file from an attempt that must not resume", got)
			}
			backend.mu.Lock()
			defer backend.mu.Unlock()
			for _, abort := range backend.aborts {
				if abort.uploadID == testUploadID {
					t.Errorf("the rejected checkpoint's upload was retired through this attempt: %+v", backend.aborts)
				}
			}
		})
	}
}

// TestPreEncryptDoesNotDiscardAnotherDestinationsUpload pins the remaining abort
// site. The abort this provider issues names the bucket it was built for, where
// another destination's upload ID is simply absent — an answer that reads as
// retirement while the upload it was meant to discard stays open. The
// orchestrator applies this same rule before it retires anything.
func TestPreEncryptDoesNotDiscardAnotherDestinationsUpload(t *testing.T) {
	backend, server := newFakeS3Backend(t)
	backend.listPartsLive = true
	s3Client := newTestS3Client(t, server)

	encryptedSize := 3 * resumePartSize
	fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
		PartSize:   resumePartSize,
		WorkerCap:  4,
		QueueDepth: 4,
	})
	foreignKey := state.BuildObjectKey(testPathBase, filepath.Base(fixture.localPath), "previous-suffix")
	testsupport.WriteResumeState(t, fixture.localPath, &state.UploadResumeState{
		LocalPath:      fixture.localPath,
		EncryptedPath:  fixture.encryptedPath,
		ObjectKey:      foreignKey,
		UploadID:       "another-destination-upload",
		TotalSize:      encryptedSize,
		OriginalSize:   encryptedSize,
		SourceModTime:  fixture.params.SourceModTime,
		UploadedBytes:  resumePartSize,
		CompletedParts: []state.CompletedPart{{PartNumber: 1, ETag: "etag-1"}},
		PartSize:       resumePartSize,
		RandomSuffix:   "previous-suffix",
		CreatedAt:      fixture.createdAt,
		LastUpdate:     time.Now(),
		StorageType:    "S3Storage",
		StorageID:      "storage-A",
		Container:      "bucket-a",
	})

	fixture.params.TransferHandle = testsupport.MultiThreadedHandle(t)
	provider := &Provider{storageInfo: &models.StorageInfo{
		ID:                 "storage-B",
		StorageType:        "S3Storage",
		ConnectionSettings: models.ConnectionSettings{Container: testBucket, PathBase: testPathBase},
	}}
	if err := provider.uploadEncryptedMultipartConcurrent(context.Background(), s3Client, fixture.params, fixture.objectKey, encryptedSize); err != nil {
		t.Fatalf("the upload failed: %v", err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	for _, abort := range backend.aborts {
		if abort.uploadID == "another-destination-upload" {
			t.Errorf("another destination's upload was retired through this one: %+v", backend.aborts)
		}
	}
}

// TestPreEncryptStatelessFailureDiscardsItsOwnUpload is the G2/H2 cleanup
// asymmetry. A stateless attempt writes no checkpoint, so the multipart upload
// it opened is one nothing can ever come back to: kept for a retry that has no
// record to retry from, it sits on S3 until the seven-day sweep. The streaming
// path already discards its checkpoint-free upload.
func TestPreEncryptStatelessFailureDiscardsItsOwnUpload(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeS3Backend(t)
			backend.refusePartsFrom = 2
			s3Client := newTestS3Client(t, server)

			encryptedSize := 3 * resumePartSize
			fixture := newS3ResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   resumePartSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			fixture.params.Stateless = true

			if err := fixture.run(t, s3Client, tt.concurrent); err == nil {
				t.Fatal("a refused part was reported as success")
			}

			backend.mu.Lock()
			defer backend.mu.Unlock()
			discarded := false
			for _, abort := range backend.aborts {
				if abort.uploadID == createdUploadID(1) && abort.key == fixture.objectKey {
					discarded = true
				}
			}
			if !discarded {
				t.Errorf("the upload of an attempt that recorded nothing was left open on S3: aborts = %+v", backend.aborts)
			}
		})
	}
}
