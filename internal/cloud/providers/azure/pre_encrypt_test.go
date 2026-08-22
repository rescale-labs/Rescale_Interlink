package azure

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

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

// The pre-encrypt upload paths talk to the Azure SDK directly, so the only seam
// that exercises their real read loops is the wire. fakeBlobBackend is a minimal
// block blob endpoint: it records what each staged block carried and which
// blocks the commit asked Azure to assemble, which is exactly what a truncated
// upload gets wrong.

const (
	testContainer = "test-container"
	testPathBase  = "uploads"
	testAccount   = "testaccount"
)

type stagedBlock struct {
	size int64
	sum  [32]byte
}

type fakeBlobBackend struct {
	mu sync.Mutex

	blocks    map[string]stagedBlock
	committed []string
	commits   int
	requests  int
}

func newFakeBlobBackend(t *testing.T) (*fakeBlobBackend, *httptest.Server) {
	t.Helper()
	backend := &fakeBlobBackend{blocks: make(map[string]stagedBlock)}
	// TLS, because the client the provider rebuilds on every credential refresh
	// addresses the real blob endpoint template, which is https.
	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	return backend, server
}

func (f *fakeBlobBackend) ServeHTTP(w nethttp.ResponseWriter, r *nethttp.Request) {
	query := r.URL.Query()

	f.mu.Lock()
	f.requests++
	f.mu.Unlock()

	switch {
	case r.Method == nethttp.MethodPut && query.Get("comp") == "block":
		hasher := sha256.New()
		size, err := io.Copy(hasher, r.Body)
		if err != nil {
			w.WriteHeader(nethttp.StatusInternalServerError)
			return
		}
		var sum [32]byte
		copy(sum[:], hasher.Sum(nil))

		f.mu.Lock()
		f.blocks[query.Get("blockid")] = stagedBlock{size: size, sum: sum}
		f.mu.Unlock()

		w.Header().Set("x-ms-request-server-encrypted", "true")
		w.WriteHeader(nethttp.StatusCreated)

	case r.Method == nethttp.MethodPut && query.Get("comp") == "blocklist":
		var body struct {
			Latest []string `xml:"Latest"`
		}
		if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(nethttp.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.commits++
		f.committed = body.Latest
		f.mu.Unlock()

		w.Header().Set("ETag", `"committed"`)
		w.WriteHeader(nethttp.StatusCreated)

	case r.Method == nethttp.MethodPut:
		// Single-shot blob upload (small files).
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("ETag", `"uploaded"`)
		w.WriteHeader(nethttp.StatusCreated)

	default:
		w.WriteHeader(nethttp.StatusNotImplemented)
	}
}

func (f *fakeBlobBackend) totalStagedBytes() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total int64
	for _, block := range f.blocks {
		total += block.size
	}
	return total
}

// committedSizes returns the size of each committed block, in commit order.
func (f *fakeBlobBackend) committedSizes(t *testing.T) []int64 {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	sizes := make([]int64, 0, len(f.committed))
	for _, id := range f.committed {
		block, ok := f.blocks[id]
		if !ok {
			t.Fatalf("committed block %q was never staged", id)
		}
		sizes = append(sizes, block.size)
	}
	return sizes
}

func (f *fakeBlobBackend) assertCommittedBlocksMatch(t *testing.T, want [][32]byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.committed) != len(want) {
		t.Fatalf("committed %d blocks, want %d", len(f.committed), len(want))
	}
	for i, wantSum := range want {
		block, ok := f.blocks[f.committed[i]]
		if !ok {
			t.Fatalf("committed block %q was never staged", f.committed[i])
		}
		if block.sum != wantSum {
			t.Errorf("block %d holds different bytes than the file at that offset", i+1)
		}
	}
}

// redirectingHTTPClient sends every request to addr whatever hostname the SDK
// resolved. The provider refreshes credentials before every attempt, and that
// rebuilds the SDK client against the real blob endpoint template, so overriding
// only the first client's endpoint would point the second call at real Azure.
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
		_, _ = io.WriteString(w, `{"storageType":"AzureStorage","sasToken":"sv=2021-06-08&sig=test"}`)
	}))
	t.Cleanup(server.Close)
	return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
}

func newTestAzureClient(t *testing.T, server *httptest.Server) *AzureClient {
	t.Helper()
	httpClient := redirectingHTTPClient(server.Listener.Addr().String())
	apiClient := newFakeCredentialsAPI(t)

	storageInfo := &models.StorageInfo{
		StorageType: "AzureStorage",
		ConnectionSettings: models.ConnectionSettings{
			Container:     testContainer,
			AccountName:   testAccount,
			PathPartsBase: testPathBase,
		},
	}
	client, err := azblob.NewClientWithNoCredential(
		"https://"+testAccount+".blob.core.windows.net/?sv=2021-06-08&sig=test",
		&azblob.ClientOptions{ClientOptions: azcore.ClientOptions{Transport: httpClient}})
	if err != nil {
		t.Fatalf("failed to build test Azure client: %v", err)
	}

	return &AzureClient{
		client:      client,
		storageInfo: storageInfo,
		credManager: credentials.GetManager(apiClient),
		apiClient:   apiClient,
		httpClient:  httpClient,
	}
}

// oversizedBlockSize is twice the pooled buffer size — the shape of the 48-64 MB
// blocks the planner picks for files of 1 GB and up, without putting a gigabyte
// through the test. Twice rather than a hair over: the sequential loop ran a
// precomputed number of iterations, so its shortfall is (block size - pooled
// size) per iteration, and only a wide enough gap leaves the file short.
var oversizedBlockSize = int64(2 * constants.ChunkSize)

// writeTestFile writes size bytes of position-dependent data, so a block that
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

// expectedBlockHashes splits data the way a correct reader would.
func expectedBlockHashes(data []byte, blockSize int64) [][32]byte {
	var hashes [][32]byte
	for offset := int64(0); offset < int64(len(data)); offset += blockSize {
		end := offset + blockSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		hashes = append(hashes, sha256.Sum256(data[offset:end]))
	}
	return hashes
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

func testUploadParams(localPath, encryptedPath string, plan *resources.UploadPlan) transfer.EncryptedFileUploadParams {
	return transfer.EncryptedFileUploadParams{
		LocalPath:     localPath,
		EncryptedPath: encryptedPath,
		EncryptionKey: make([]byte, 32),
		IV:            make([]byte, 16),
		RandomSuffix:  "suffix",
		Plan:          plan,
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

// TestPreEncryptBlockBlobStagesEveryBlock is the regression for the sequential
// path: it read 32 MB per iteration into a pooled buffer while the block size was
// larger, and ran the loop a precomputed number of times, so it committed a blob
// holding only part of the file. The loop now runs to EOF with a right-sized
// buffer, and the commit is gated on covering every byte.
func TestPreEncryptBlockBlobStagesEveryBlock(t *testing.T) {
	backend, server := newFakeBlobBackend(t)
	azureClient := newTestAzureClient(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

	encryptedSize := oversizedBlockSize + 6*1024*1024
	data := writeTestFile(t, encryptedPath, encryptedSize)
	writeTestFile(t, localPath, encryptedSize)

	params := testUploadParams(localPath, encryptedPath, &resources.UploadPlan{PartSize: oversizedBlockSize})
	provider := &Provider{}
	pathForRescale := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)

	if err := provider.uploadEncryptedBlockBlob(context.Background(), azureClient, params, "blob", pathForRescale, encryptedSize); err != nil {
		t.Fatalf("sequential block blob upload failed: %v", err)
	}

	wantBlocks := expectedBlockHashes(data, oversizedBlockSize)
	if len(wantBlocks) != 2 {
		t.Fatalf("test setup expects 2 blocks, computed %d", len(wantBlocks))
	}
	if got := backend.totalStagedBytes(); got != encryptedSize {
		t.Errorf("staged %d bytes, want the whole %d-byte file", got, encryptedSize)
	}
	wantSizes := []int64{oversizedBlockSize, encryptedSize - oversizedBlockSize}
	if got := backend.committedSizes(t); !slices.Equal(got, wantSizes) {
		t.Errorf("committed block sizes = %v, want %v", got, wantSizes)
	}
	backend.assertCommittedBlocksMatch(t, wantBlocks)

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.commits != 1 {
		t.Errorf("CommitBlockList called %d times, want 1", backend.commits)
	}
}

// TestPreEncryptBlockBlobConcurrentStagesEveryBlock is the regression for the
// concurrent path: the producer read into a 32 MB pooled buffer, treated the
// short read as the final block, and the commit then skipped the empty slots in
// the ordered block list — papering over every block that was never staged.
func TestPreEncryptBlockBlobConcurrentStagesEveryBlock(t *testing.T) {
	backend, server := newFakeBlobBackend(t)
	azureClient := newTestAzureClient(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

	encryptedSize := oversizedBlockSize + 6*1024*1024
	data := writeTestFile(t, encryptedPath, encryptedSize)
	writeTestFile(t, localPath, encryptedSize)

	params := testUploadParams(localPath, encryptedPath, &resources.UploadPlan{
		PartSize:   oversizedBlockSize,
		WorkerCap:  4,
		QueueDepth: 4,
	})
	params.TransferHandle = multiThreadedHandle(t)

	provider := &Provider{}
	pathForRescale := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)

	if err := provider.uploadEncryptedBlockBlobConcurrent(context.Background(), azureClient, params, "blob", pathForRescale, encryptedSize); err != nil {
		t.Fatalf("concurrent block blob upload failed: %v", err)
	}

	wantBlocks := expectedBlockHashes(data, oversizedBlockSize)
	if len(wantBlocks) != 2 {
		t.Fatalf("test setup expects 2 blocks, computed %d", len(wantBlocks))
	}
	if got := backend.totalStagedBytes(); got != encryptedSize {
		t.Errorf("staged %d bytes, want the whole %d-byte file", got, encryptedSize)
	}
	wantSizes := []int64{oversizedBlockSize, encryptedSize - oversizedBlockSize}
	if got := backend.committedSizes(t); !slices.Equal(got, wantSizes) {
		t.Errorf("committed block sizes = %v, want %v", got, wantSizes)
	}
	backend.assertCommittedBlocksMatch(t, wantBlocks)

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.commits != 1 {
		t.Errorf("CommitBlockList called %d times, want 1", backend.commits)
	}
}

// TestPreEncryptBlockBlobRefusesToCommitShortUpload feeds each path a file
// shorter than the size it was told to upload — the shape any reader or sizing
// drift produces. Neither may commit a block list.
func TestPreEncryptBlockBlobRefusesToCommitShortUpload(t *testing.T) {
	tests := []struct {
		name       string
		concurrent bool
	}{
		{name: "sequential"},
		{name: "concurrent", concurrent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeBlobBackend(t)
			azureClient := newTestAzureClient(t, server)

			tmpDir := t.TempDir()
			localPath := filepath.Join(tmpDir, "source.dat")
			encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

			actualSize := oversizedBlockSize - 24*1024*1024
			writeTestFile(t, encryptedPath, actualSize)
			writeTestFile(t, localPath, actualSize)

			// The caller believes the encrypted file is a block longer than it is.
			claimedSize := actualSize + oversizedBlockSize

			params := testUploadParams(localPath, encryptedPath, &resources.UploadPlan{
				PartSize:   oversizedBlockSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			provider := &Provider{}
			pathForRescale := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)

			var err error
			if tt.concurrent {
				params.TransferHandle = multiThreadedHandle(t)
				err = provider.uploadEncryptedBlockBlobConcurrent(context.Background(), azureClient, params, "blob", pathForRescale, claimedSize)
			} else {
				err = provider.uploadEncryptedBlockBlob(context.Background(), azureClient, params, "blob", pathForRescale, claimedSize)
			}

			if err == nil {
				t.Fatal("a short upload was committed instead of failing")
			}
			if !strings.Contains(err.Error(), "refusing to commit") {
				t.Errorf("error %q does not say the commit was refused", err)
			}

			backend.mu.Lock()
			defer backend.mu.Unlock()
			if backend.commits != 0 {
				t.Errorf("CommitBlockList was called %d times for an incomplete upload", backend.commits)
			}
		})
	}
}

// TestPreEncryptBlockBlobIgnoresResumeStateForAnotherObject pins the resume
// branch on both paths. Every attempt regenerates the key, IV and blob suffix, so
// a state file left by an earlier attempt describes an upload of different
// ciphertext; resuming it would mix two encryptions into one blob.
func TestPreEncryptBlockBlobIgnoresResumeStateForAnotherObject(t *testing.T) {
	tests := []struct {
		name       string
		concurrent bool
	}{
		{name: "sequential"},
		{name: "concurrent", concurrent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeBlobBackend(t)
			azureClient := newTestAzureClient(t, server)

			tmpDir := t.TempDir()
			localPath := filepath.Join(tmpDir, "source.dat")
			encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

			encryptedSize := oversizedBlockSize + 6*1024*1024
			data := writeTestFile(t, encryptedPath, encryptedSize)
			writeTestFile(t, localPath, encryptedSize)

			writeResumeState(t, localPath, &state.UploadResumeState{
				LocalPath:     localPath,
				EncryptedPath: encryptedPath,
				ObjectKey:     state.BuildObjectKey(testPathBase, filepath.Base(localPath), "previous-suffix"),
				TotalSize:     encryptedSize,
				OriginalSize:  encryptedSize,
				UploadedBytes: oversizedBlockSize,
				BlockIDs:      []string{"c3RhbGUtYmxvY2s="},
				RandomSuffix:  "previous-suffix",
				CreatedAt:     time.Now(),
				LastUpdate:    time.Now(),
				StorageType:   "AzureStorage",
			})

			params := testUploadParams(localPath, encryptedPath, &resources.UploadPlan{
				PartSize:   oversizedBlockSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			provider := &Provider{}
			pathForRescale := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)

			var err error
			if tt.concurrent {
				params.TransferHandle = multiThreadedHandle(t)
				err = provider.uploadEncryptedBlockBlobConcurrent(context.Background(), azureClient, params, "blob", pathForRescale, encryptedSize)
			} else {
				err = provider.uploadEncryptedBlockBlob(context.Background(), azureClient, params, "blob", pathForRescale, encryptedSize)
			}
			if err != nil {
				t.Fatalf("upload failed: %v", err)
			}

			// The whole file went up under the new blob path, starting from block 0.
			if got := backend.totalStagedBytes(); got != encryptedSize {
				t.Errorf("staged %d bytes, want the whole %d-byte file", got, encryptedSize)
			}
			backend.assertCommittedBlocksMatch(t, expectedBlockHashes(data, oversizedBlockSize))
		})
	}
}

// TestPreEncryptPlanRespectsAzureBlockLimit checks the sizing the four call sites
// share: huge files get blocks large enough to stay inside Azure's block ceiling,
// files of today's sizes keep the block size they already had, and a file no
// block size can cover is refused.
func TestPreEncryptPlanRespectsAzureBlockLimit(t *testing.T) {
	provider := &Provider{}
	limits := provider.UploadLimits()

	t.Run("huge file stays within the block ceiling", func(t *testing.T) {
		const fileSize = int64(4300) * 1024 * 1024 * 1024 // ~4.2 TB
		plan, err := transfer.EncryptedFileUploadParams{}.UploadPlan(fileSize, limits)
		if err != nil {
			t.Fatalf("planning a 4.2 TB upload failed: %v", err)
		}
		if blocks := transfer.CalculateTotalParts(fileSize, plan.PartSize); blocks > constants.MaxAzureUploadBlocks {
			t.Errorf("plan needs %d blocks, above Azure's limit of %d", blocks, constants.MaxAzureUploadBlocks)
		}
	})

	t.Run("ordinary file keeps its block size", func(t *testing.T) {
		const fileSize = int64(2 * 1024 * 1024 * 1024) // 2 GB
		plan, err := transfer.EncryptedFileUploadParams{}.UploadPlan(fileSize, limits)
		if err != nil {
			t.Fatalf("planning a 2 GB upload failed: %v", err)
		}
		want := resources.CalculateDynamicChunkSize(fileSize, constants.MaxThreadsPerFile)
		if plan.PartSize != want {
			t.Errorf("block size = %d, want the unchanged %d", plan.PartSize, want)
		}
	})

	t.Run("file too large for the backend is refused", func(t *testing.T) {
		const fileSize = int64(300) * 1024 * 1024 * 1024 * 1024 // 300 TB
		_, err := transfer.EncryptedFileUploadParams{}.UploadPlan(fileSize, limits)
		if err == nil {
			t.Fatal("expected a file beyond Azure's capacity to be refused")
		}
		if !strings.Contains(err.Error(), "too large") {
			t.Errorf("error %q does not explain that the file is too large", err)
		}
	})
}

// TestPreEncryptRejectsOversizedFileBeforeAnyRequest is the fail-fast
// requirement: the refusal has to happen before any block is staged.
func TestPreEncryptRejectsOversizedFileBeforeAnyRequest(t *testing.T) {
	backend, server := newFakeBlobBackend(t)
	azureClient := newTestAzureClient(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")
	writeTestFile(t, encryptedPath, 1024)
	writeTestFile(t, localPath, 1024)

	// No plan, so the provider plans on the spot — against a size Azure cannot hold.
	params := testUploadParams(localPath, encryptedPath, nil)
	provider := &Provider{}
	pathForRescale := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)

	err := provider.uploadEncryptedBlockBlob(context.Background(), azureClient, params, "blob", pathForRescale,
		int64(300)*1024*1024*1024*1024)
	if err == nil {
		t.Fatal("expected an oversized file to be refused")
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.requests != 0 {
		t.Errorf("%d request(s) reached the backend before the file was refused", backend.requests)
	}
}

// TestPreEncryptBlockBlobConcurrentHonorsPlanWorkerCap checks that narrowing the
// pipeline narrows it rather than stopping it. A cap of zero is a caller mistake
// rather than a plan the planner produces, and it is in the table because
// starting no workers at all would hang the producer instead of failing.
func TestPreEncryptBlockBlobConcurrentHonorsPlanWorkerCap(t *testing.T) {
	tests := []struct {
		name      string
		workerCap int
	}{
		{name: "single worker", workerCap: 1},
		{name: "no cap in the plan", workerCap: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeBlobBackend(t)
			azureClient := newTestAzureClient(t, server)

			tmpDir := t.TempDir()
			localPath := filepath.Join(tmpDir, "source.dat")
			encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

			encryptedSize := oversizedBlockSize + 6*1024*1024
			data := writeTestFile(t, encryptedPath, encryptedSize)
			writeTestFile(t, localPath, encryptedSize)

			params := testUploadParams(localPath, encryptedPath, &resources.UploadPlan{
				PartSize:   oversizedBlockSize,
				WorkerCap:  tt.workerCap,
				QueueDepth: 2,
			})
			params.TransferHandle = multiThreadedHandle(t)

			provider := &Provider{}
			pathForRescale := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)
			if err := provider.uploadEncryptedBlockBlobConcurrent(context.Background(), azureClient, params, "blob", pathForRescale, encryptedSize); err != nil {
				t.Fatalf("concurrent block blob upload failed: %v", err)
			}

			if got := backend.totalStagedBytes(); got != encryptedSize {
				t.Errorf("staged %d bytes, want the whole %d-byte file", got, encryptedSize)
			}
			backend.assertCommittedBlocksMatch(t, expectedBlockHashes(data, oversizedBlockSize))
		})
	}
}
