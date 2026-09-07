package azure

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/resources"
)

// streamingTestPlan pins the part size so these tests run on a handful of blocks
// instead of whatever the machine's free memory would size them to.
var streamingTestPlan = resources.UploadPlan{PartSize: 64, WorkerCap: 1, QueueDepth: 1}

// committedBlocks reports the block list the commit was handed, under the lock
// the fake backend records it with.
func (f *fakeBlobBackend) committedBlocks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.committed)
}

// commitCount reports how many commits reached the fake backend.
func (f *fakeBlobBackend) commitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits
}

// streamingTestProvider is an Azure provider wired to the fake blob endpoint, so
// the block IDs a streaming upload stages and commits are the ones that went
// over the wire.
func streamingTestProvider(t *testing.T) (*Provider, *fakeBlobBackend) {
	t.Helper()
	backend, server := newFakeBlobBackend(t)
	client := newTestAzureClient(t, server)

	return &Provider{
		storageInfo: &models.StorageInfo{
			StorageType: "AzureStorage",
			ConnectionSettings: models.ConnectionSettings{
				Container:     testContainer,
				AccountName:   testAccount,
				PathPartsBase: testPathBase,
			},
		},
		apiClient:   client.apiClient,
		azureClient: client,
	}, backend
}

// stageStreamingParts encrypts and stages the parts in [from, to) of data.
func stageStreamingParts(t *testing.T, provider *Provider, upload *transfer.StreamingUpload, data []byte, from, to int64) []*transfer.PartResult {
	t.Helper()
	ctx := context.Background()

	var staged []*transfer.PartResult
	for index := from; index < to; index++ {
		end := (index + 1) * upload.PartSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		ciphertext, err := provider.EncryptStreamingPart(ctx, upload, index, data[index*upload.PartSize:end])
		if err != nil {
			t.Fatalf("EncryptStreamingPart(%d): %v", index, err)
		}
		part, err := provider.UploadCiphertext(ctx, upload, index, ciphertext)
		if err != nil {
			t.Fatalf("UploadCiphertext(%d): %v", index, err)
		}
		staged = append(staged, part)
	}
	return staged
}

func streamingTestData(parts int, partSize int64) []byte {
	data := make([]byte, int64(parts)*partSize)
	for i := range data {
		data[i] = byte(i*11 + 3)
	}
	return data
}

// TestStreamingResumeCommitsTheSameBlockListAsAnUninterruptedUpload is the Azure
// half of streaming resume. Azure assembles a blob from the list of block IDs
// the commit is handed, not from whatever happens to be staged, so a resumed
// attempt that only knew the IDs of the blocks IT staged would commit a list
// with holes where the interrupted attempt's blocks are — and the blob would
// silently be missing their bytes.
func TestStreamingResumeCommitsTheSameBlockListAsAnUninterruptedUpload(t *testing.T) {
	const partSize = 64
	const totalParts = 4
	data := streamingTestData(totalParts, partSize)
	localPath := filepath.Join(t.TempDir(), "streamed.dat")
	ctx := context.Background()

	// An uninterrupted upload, for the block list to be compared against.
	reference, referenceBackend := streamingTestProvider(t)
	whole, err := reference.InitStreamingUpload(ctx, transfer.StreamingUploadInitParams{
		LocalPath: localPath,
		FileSize:  int64(len(data)),
		Plan:      &streamingTestPlan,
	})
	if err != nil {
		t.Fatalf("InitStreamingUpload: %v", err)
	}
	wholeParts := stageStreamingParts(t, reference, whole, data, 0, totalParts)
	if _, err := reference.CompleteStreamingUpload(ctx, whole, wholeParts); err != nil {
		t.Fatalf("CompleteStreamingUpload: %v", err)
	}
	wantBlocks := referenceBackend.committedBlocks()

	// The same upload, interrupted after two blocks and resumed from the
	// encryption chain and the block IDs the first attempt recorded.
	provider, backend := streamingTestProvider(t)
	first, err := provider.InitStreamingUpload(ctx, transfer.StreamingUploadInitParams{
		LocalPath: localPath,
		FileSize:  int64(len(data)),
		Plan:      &streamingTestPlan,
	})
	if err != nil {
		t.Fatalf("InitStreamingUpload: %v", err)
	}
	done := stageStreamingParts(t, provider, first, data, 0, 2)
	chainIV := first.EncryptState.GetCurrentIV()

	resumed, err := provider.InitStreamingUploadFromState(ctx, transfer.StreamingUploadResumeParams{
		LocalPath:      localPath,
		FileSize:       int64(len(data)),
		StoragePath:    first.StoragePath,
		MasterKey:      first.MasterKey,
		InitialIV:      first.InitialIV,
		CurrentIV:      chainIV,
		PartSize:       first.PartSize,
		RandomSuffix:   first.RandomSuffix,
		CompletedParts: done,
	})
	if err != nil {
		t.Fatalf("InitStreamingUploadFromState: %v", err)
	}

	all := append(slices.Clone(done), stageStreamingParts(t, provider, resumed, data, 2, totalParts)...)
	if _, err := provider.CompleteStreamingUpload(ctx, resumed, all); err != nil {
		t.Fatalf("CompleteStreamingUpload after resume: %v", err)
	}

	committed := backend.committedBlocks()
	if !slices.Equal(committed, wantBlocks) {
		t.Errorf("resumed upload committed %v, want the uninterrupted list %v", committed, wantBlocks)
	}
	for i, id := range committed {
		if id == "" {
			t.Errorf("block %d of the resumed commit names nothing", i)
		}
	}
}

// TestStreamingCommitRefusesABlockListWithAHole covers the guard rather than the
// restore: Azure reports success for a commit that names only some of the blocks,
// so a list with an unfilled slot has to be refused here or the blob quietly
// loses those bytes.
func TestStreamingCommitRefusesABlockListWithAHole(t *testing.T) {
	const partSize = 64
	const totalParts = 3
	data := streamingTestData(totalParts, partSize)
	ctx := context.Background()

	provider, backend := streamingTestProvider(t)
	upload, err := provider.InitStreamingUpload(ctx, transfer.StreamingUploadInitParams{
		LocalPath: filepath.Join(t.TempDir(), "streamed.dat"),
		FileSize:  int64(len(data)),
		Plan:      &streamingTestPlan,
	})
	if err != nil {
		t.Fatalf("InitStreamingUpload: %v", err)
	}

	// Stage the last two parts only, then hand the commit a list that claims all
	// three — the shape a resume that failed to restore block 0 would produce.
	parts := stageStreamingParts(t, provider, upload, data, 1, totalParts)
	withHole := append([]*transfer.PartResult{{PartIndex: 0, PartNumber: 1}}, parts...)

	if _, err := provider.CompleteStreamingUpload(ctx, upload, withHole); err == nil {
		t.Fatal("committed a block list with a slot nothing was staged into")
	}
	if commits := backend.commitCount(); commits != 0 {
		t.Errorf("the commit reached Azure %d times, want it refused before the wire", commits)
	}
}

// hkdfBlobBackend serves one v1 (HKDF) blob over ranged GETs and reports a
// different ETag from a chosen range onwards, which is a blob being replaced
// under a download that is made of many requests.
type hkdfBlobBackend struct {
	mu sync.Mutex

	ciphertext []byte
	metadata   map[string]string
	replaceAt  int // range index from which the ETag changes; 0 disables
	ranges     int
}

// rangeCount reports how many ranged GETs the backend has served.
func (h *hkdfBlobBackend) rangeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ranges
}

func (h *hkdfBlobBackend) ServeHTTP(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.mu.Lock()
	etag := `"version-one"`
	if r.Method == nethttp.MethodGet {
		h.ranges++
		if h.replaceAt > 0 && h.ranges >= h.replaceAt {
			etag = `"version-two"`
		}
	}
	h.mu.Unlock()

	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", time.Now().UTC().Format(nethttp.TimeFormat))
	w.Header().Set("x-ms-blob-type", "BlockBlob")
	for name, value := range h.metadata {
		w.Header().Set("x-ms-meta-"+name, value)
	}

	switch r.Method {
	case nethttp.MethodHead:
		w.Header().Set("Content-Length", strconv.Itoa(len(h.ciphertext)))
		w.WriteHeader(nethttp.StatusOK)

	case nethttp.MethodGet:
		start, end := 0, len(h.ciphertext)-1
		if spec := r.Header.Get("x-ms-range"); spec != "" {
			fmt.Sscanf(spec, "bytes=%d-%d", &start, &end)
		} else if spec := r.Header.Get("Range"); spec != "" {
			fmt.Sscanf(spec, "bytes=%d-%d", &start, &end)
		}
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

// newHKDFBlob builds a v1 object of parts plaintext parts, and the metadata a
// download reads its keying from.
func newHKDFBlob(t *testing.T, parts int, partSize int64) (*hkdfBlobBackend, []byte, []byte) {
	t.Helper()

	encryptor, err := encryption.NewStreamingEncryptor(partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptor: %v", err)
	}
	masterKey, fileID := encryptor.GetMasterKey(), encryptor.GetFileId()

	plaintext := streamingTestData(parts, partSize)
	var ciphertext []byte
	for index := 0; index < parts; index++ {
		part, err := encryptor.EncryptPart(int64(index), plaintext[int64(index)*partSize:int64(index+1)*partSize])
		if err != nil {
			t.Fatalf("EncryptPart(%d): %v", index, err)
		}
		ciphertext = append(ciphertext, part...)
	}

	return &hkdfBlobBackend{
		ciphertext: ciphertext,
		metadata: map[string]string{
			"formatversion": "1",
			"fileid":        base64.StdEncoding.EncodeToString(fileID),
			"partsize":      strconv.FormatInt(partSize, 10),
		},
	}, masterKey, plaintext
}

// TestDownloadStreamingAbortsWhenTheBlobIsReplaced is the v1 half of pinning a
// ranged download to one version. The concurrent chunked paths already refuse a
// file stitched from two objects; this one fetched every part with no version
// condition at all, so a blob replaced partway through was assembled from both
// versions and — with the size right and no per-file checksum guaranteed —
// nothing afterwards noticed.
func TestDownloadStreamingAbortsWhenTheBlobIsReplaced(t *testing.T) {
	backend, masterKey, _ := newHKDFBlob(t, 3, 64)
	backend.replaceAt = 2 // the first range is the pin; the second is the new blob

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestAzureClient(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, azureClient: client}

	localPath := filepath.Join(t.TempDir(), "downloaded.dat")
	err := provider.DownloadStreaming(context.Background(), "blob.dat", localPath, masterKey, nil)
	if !errors.Is(err, transfer.ErrObjectReplaced) {
		t.Fatalf("error = %v, want it to wrap ErrObjectReplaced", err)
	}
}

// TestDownloadStreamingReadsOneVersionThrough is the other side of the pin: a
// blob that does not change downloads exactly as before.
func TestDownloadStreamingReadsOneVersionThrough(t *testing.T) {
	backend, masterKey, plaintext := newHKDFBlob(t, 3, 64)

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestAzureClient(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, azureClient: client}

	localPath := filepath.Join(t.TempDir(), "downloaded.dat")
	if err := provider.DownloadStreaming(context.Background(), "blob.dat", localPath, masterKey, nil); err != nil {
		t.Fatalf("DownloadStreaming: %v", err)
	}

	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read the downloaded file: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("downloaded %d bytes, want the %d the blob holds", len(got), len(plaintext))
	}
}

// TestGetEncryptedSizeAndRangeAbortWhenTheBlobIsReplaced is the v2 (CBC) half of
// pinning a ranged download to one version. That path reads the blob's size once
// and then fetches its parts as independent ranges with no version carried
// between them, so a blob replaced under it — a re-upload at the same path — was
// decrypted into one file from two versions. Only the sizes and the ETags matter
// here, not what the bytes decrypt to.
func TestGetEncryptedSizeAndRangeAbortWhenTheBlobIsReplaced(t *testing.T) {
	backend, _, _ := newHKDFBlob(t, 3, 64)
	backend.replaceAt = 1 // the properties call reports the first version, every range the second

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestAzureClient(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, azureClient: client}
	ctx := context.Background()

	size, version, err := provider.GetEncryptedSize(ctx, "blob.dat")
	if err != nil {
		t.Fatalf("GetEncryptedSize: %v", err)
	}
	if size != int64(len(backend.ciphertext)) {
		t.Errorf("size = %d, want the %d bytes the blob holds", size, len(backend.ciphertext))
	}
	if version != `"version-one"` {
		t.Fatalf("version = %q, want the ETag the properties call reported", version)
	}

	_, err = provider.DownloadEncryptedRange(ctx, "blob.dat", 0, 64, version, nil)
	if !errors.Is(err, transfer.ErrObjectReplaced) {
		t.Fatalf("error = %v, want it to wrap ErrObjectReplaced", err)
	}
	// A replaced blob is not a transient failure: retrying spends the budget on
	// a range that cannot come back right.
	if got := backend.rangeCount(); got != 1 {
		t.Errorf("%d ranges were fetched, want exactly the one attempt", got)
	}
}

// TestDownloadEncryptedRangeReadsThePinnedVersion is the other side of the pin:
// a range of the blob the size call measured is served as before.
func TestDownloadEncryptedRangeReadsThePinnedVersion(t *testing.T) {
	backend, _, _ := newHKDFBlob(t, 3, 64)

	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestAzureClient(t, server)
	provider := &Provider{storageInfo: client.storageInfo, apiClient: client.apiClient, azureClient: client}
	ctx := context.Background()

	_, version, err := provider.GetEncryptedSize(ctx, "blob.dat")
	if err != nil {
		t.Fatalf("GetEncryptedSize: %v", err)
	}

	got, err := provider.DownloadEncryptedRange(ctx, "blob.dat", 64, 64, version, nil)
	if err != nil {
		t.Fatalf("DownloadEncryptedRange: %v", err)
	}
	if !bytes.Equal(got, backend.ciphertext[64:128]) {
		t.Errorf("range [64-128) came back as %d bytes that are not the blob's", len(got))
	}
}

// blockListBackend answers the one question a resume has to ask Azure: which of
// this blob's blocks are staged but not yet committed. Uncommitted blocks are
// what a streaming upload leaves behind, and they are what the service discards
// after seven days.
type blockListBackend struct {
	mu          sync.Mutex
	uncommitted []string
	notFound    bool
	calls       int
}

func (b *blockListBackend) ServeHTTP(w nethttp.ResponseWriter, r *nethttp.Request) {
	if r.Method != nethttp.MethodGet || r.URL.Query().Get("comp") != "blocklist" {
		w.WriteHeader(nethttp.StatusNotImplemented)
		return
	}

	b.mu.Lock()
	b.calls++
	notFound := b.notFound
	staged := slices.Clone(b.uncommitted)
	b.mu.Unlock()

	if notFound {
		w.Header().Set("x-ms-error-code", "BlobNotFound")
		w.WriteHeader(nethttp.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(nethttp.StatusOK)
	fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?><BlockList><CommittedBlocks/><UncommittedBlocks>`)
	for _, id := range staged {
		fmt.Fprintf(w, `<Block><Name>%s</Name><Size>64</Size></Block>`, id)
	}
	fmt.Fprint(w, `</UncommittedBlocks></BlockList>`)
}

func (b *blockListBackend) requestCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func blockListTestProvider(t *testing.T, backend *blockListBackend) *Provider {
	t.Helper()
	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	client := newTestAzureClient(t, server)

	return &Provider{
		storageInfo: &models.StorageInfo{
			StorageType: "AzureStorage",
			ConnectionSettings: models.ConnectionSettings{
				Container:     testContainer,
				AccountName:   testAccount,
				PathPartsBase: testPathBase,
			},
		},
		apiClient:   client.apiClient,
		azureClient: client,
	}
}

// TestValidateStreamingUploadExistsChecksTheStagedBlocks is the Azure half of
// resuming against an upload the backend no longer holds. S3 asks ListParts and
// is told NoSuchUpload; Azure answered yes without asking anything, so a state
// whose blocks the service had already discarded was resumed — and the commit
// that followed named blocks that are not there.
func TestValidateStreamingUploadExistsChecksTheStagedBlocks(t *testing.T) {
	const blobPath = testPathBase + "/streamed.dat-suffix"
	ctx := context.Background()

	backend := &blockListBackend{}
	provider := blockListTestProvider(t, backend)

	exists, err := provider.ValidateStreamingUploadExists(ctx, "", blobPath)
	if err != nil {
		t.Fatalf("checking a blob with no staged blocks failed: %v", err)
	}
	if exists {
		t.Error("a resume was allowed against a blob whose staged blocks are gone")
	}
	if backend.requestCount() == 0 {
		t.Error("the check never asked the service whether the blocks are still there")
	}

	backend.mu.Lock()
	backend.uncommitted = []string{base64.StdEncoding.EncodeToString([]byte("block-0000000000"))}
	backend.mu.Unlock()

	exists, err = provider.ValidateStreamingUploadExists(ctx, "", blobPath)
	if err != nil {
		t.Fatalf("checking a blob with staged blocks failed: %v", err)
	}
	if !exists {
		t.Error("a resume was refused although the blocks it continues are still staged")
	}

	// A blob that was never created at all is the same answer: start fresh.
	backend.mu.Lock()
	backend.notFound = true
	backend.mu.Unlock()

	exists, err = provider.ValidateStreamingUploadExists(ctx, "", blobPath)
	if err != nil {
		t.Fatalf("checking a blob that does not exist failed: %v", err)
	}
	if exists {
		t.Error("a resume was allowed against a blob that does not exist")
	}
}
