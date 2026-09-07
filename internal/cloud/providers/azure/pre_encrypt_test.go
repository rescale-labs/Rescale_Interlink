package azure

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

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

	// rejectOncePerBlock fails the first attempt at each block with an
	// authentication error, the shape a rejected SAS token arrives in.
	rejectOncePerBlock bool
	rejectedBlocks     map[string]bool
}

func newFakeBlobBackend(t *testing.T) (*fakeBlobBackend, *httptest.Server) {
	t.Helper()
	backend := &fakeBlobBackend{blocks: make(map[string]stagedBlock), rejectedBlocks: make(map[string]bool)}
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
		blockID := query.Get("blockid")
		f.mu.Lock()
		reject := f.rejectOncePerBlock && !f.rejectedBlocks[blockID]
		if reject {
			f.rejectedBlocks[blockID] = true
		}
		f.mu.Unlock()
		if reject {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("x-ms-error-code", "AuthenticationFailed")
			w.WriteHeader(nethttp.StatusForbidden)
			return
		}

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
	httpClient := testsupport.RedirectingHTTPClient(server.Listener.Addr().String())
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
	data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
	testsupport.WriteTestFile(t, localPath, encryptedSize)

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
	data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
	testsupport.WriteTestFile(t, localPath, encryptedSize)

	params := testUploadParams(localPath, encryptedPath, &resources.UploadPlan{
		PartSize:   oversizedBlockSize,
		WorkerCap:  4,
		QueueDepth: 4,
	})
	params.TransferHandle = testsupport.MultiThreadedHandle(t)

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
			testsupport.WriteTestFile(t, encryptedPath, actualSize)
			testsupport.WriteTestFile(t, localPath, actualSize)

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
				params.TransferHandle = testsupport.MultiThreadedHandle(t)
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
			data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
			testsupport.WriteTestFile(t, localPath, encryptedSize)

			testsupport.WriteResumeState(t, localPath, &state.UploadResumeState{
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
				params.TransferHandle = testsupport.MultiThreadedHandle(t)
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
	testsupport.WriteTestFile(t, encryptedPath, 1024)
	testsupport.WriteTestFile(t, localPath, 1024)

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
			data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
			testsupport.WriteTestFile(t, localPath, encryptedSize)

			params := testUploadParams(localPath, encryptedPath, &resources.UploadPlan{
				PartSize:   oversizedBlockSize,
				WorkerCap:  tt.workerCap,
				QueueDepth: 2,
			})
			params.TransferHandle = testsupport.MultiThreadedHandle(t)

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

// TestPreEncryptBlockBlobConcurrentResumesMatchingUpload is the Azure half of
// F10: with the interrupted upload's identity restored, the blocks already
// staged are not staged again, and the commit still lists the whole blob.
func TestPreEncryptBlockBlobConcurrentResumesMatchingUpload(t *testing.T) {
	backend, server := newFakeBlobBackend(t)
	azureClient := newTestAzureClient(t, server)

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")

	encryptedSize := oversizedBlockSize + 6*1024*1024
	data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
	testsupport.WriteTestFile(t, localPath, encryptedSize)

	params := testUploadParams(localPath, encryptedPath, &resources.UploadPlan{
		PartSize:   oversizedBlockSize,
		WorkerCap:  4,
		QueueDepth: 4,
	})
	params.TransferHandle = testsupport.MultiThreadedHandle(t)

	sourceInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	params.SourceModTime = sourceInfo.ModTime()

	pathForRescale := state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix)
	firstBlockID := base64.StdEncoding.EncodeToString([]byte("block-000000"))
	testsupport.WriteResumeState(t, localPath, &state.UploadResumeState{
		LocalPath:     localPath,
		EncryptedPath: encryptedPath,
		ObjectKey:     pathForRescale,
		TotalSize:     encryptedSize,
		OriginalSize:  encryptedSize,
		SourceModTime: params.SourceModTime,
		UploadedBytes: oversizedBlockSize,
		BlockIDs:      []string{firstBlockID},
		PartSize:      oversizedBlockSize,
		RandomSuffix:  params.RandomSuffix,
		CreatedAt:     time.Now(),
		LastUpdate:    time.Now(),
		StorageType:   "AzureStorage",
	})

	provider := &Provider{}
	if err := provider.uploadEncryptedBlockBlobConcurrent(context.Background(), azureClient, params, "blob", pathForRescale, encryptedSize); err != nil {
		t.Fatalf("resumed block blob upload failed: %v", err)
	}

	wantBlocks := expectedBlockHashes(data, oversizedBlockSize)

	backend.mu.Lock()
	defer backend.mu.Unlock()

	if len(backend.blocks) != 1 {
		t.Fatalf("staged %d blocks, want only the 1 that was missing", len(backend.blocks))
	}
	if _, restaged := backend.blocks[firstBlockID]; restaged {
		t.Error("the first block was staged again even though the resume state listed it as complete")
	}
	if len(backend.committed) != len(wantBlocks) {
		t.Fatalf("committed %d blocks, want the whole %d-block blob", len(backend.committed), len(wantBlocks))
	}
	if backend.committed[0] != firstBlockID {
		t.Errorf("commit lists %q first, want the block the interrupted upload staged (%q)", backend.committed[0], firstBlockID)
	}
	if backend.commits != 1 {
		t.Errorf("CommitBlockList called %d times, want 1", backend.commits)
	}
	if state.UploadResumeStateExists(localPath) {
		t.Error("the resume state survived a completed upload")
	}
}

// TestUploadCiphertextReportsEachByteOnceAcrossRetries is the Azure half of
// F18: an outer retry replaces the progress reader, and only the discarded
// reader knew how to withdraw what it had reported.
func TestUploadCiphertextReportsEachByteOnceAcrossRetries(t *testing.T) {
	backend, server := newFakeBlobBackend(t)
	backend.rejectOncePerBlock = true // the first attempt fails after reading the body
	azureClient := newTestAzureClient(t, server)

	ciphertext := make([]byte, 3*1024*1024)
	for i := range ciphertext {
		ciphertext[i] = byte(i)
	}

	var reported atomic.Int64
	uploadState := &transfer.StreamingUpload{
		StoragePath:          "blob",
		TotalParts:           1,
		ByteProgressCallback: func(n int64) { reported.Add(n) },
		ProviderData: &azureProviderData{
			container:   testContainer,
			blobPath:    "blob",
			azureClient: azureClient,
			blockIDs:    make([]string, 1),
		},
	}

	provider := &Provider{}
	if _, err := provider.UploadCiphertext(context.Background(), uploadState, 0, ciphertext); err != nil {
		t.Fatalf("block did not recover from the rejected attempt: %v", err)
	}

	if got := reported.Load(); got != int64(len(ciphertext)) {
		t.Errorf("progress reported %d bytes for a %d-byte block: the failed attempt's bytes were counted as well",
			got, len(ciphertext))
	}
}

// resumeBlockSize is a small block size for the resume-geometry tests. They are
// about which blocks go over the wire and in what order, not about the pooled
// buffer, so they do not need the oversized blocks the reader regressions use.
const resumeBlockSize = int64(4 * 1024 * 1024)

func testBlockID(index int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("block-%06d", index)))
}

// stagedBlockIDs returns the block IDs that reached the wire, in order.
func (f *fakeBlobBackend) stagedBlockIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.blocks))
	for id := range f.blocks {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// azureResumeFixture is a source, its ciphertext and the params the next attempt
// runs with, shared by the resume-geometry tests below.
type azureResumeFixture struct {
	localPath      string
	encryptedPath  string
	data           []byte
	params         transfer.EncryptedFileUploadParams
	pathForRescale string
}

func newAzureResumeFixture(t *testing.T, encryptedSize int64, plan *resources.UploadPlan) *azureResumeFixture {
	t.Helper()

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "source.dat")
	encryptedPath := filepath.Join(tmpDir, "source.dat.enc")
	data := testsupport.WriteTestFile(t, encryptedPath, encryptedSize)
	testsupport.WriteTestFile(t, localPath, encryptedSize)

	params := testUploadParams(localPath, encryptedPath, plan)
	sourceInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	params.SourceModTime = sourceInfo.ModTime()

	return &azureResumeFixture{
		localPath:      localPath,
		encryptedPath:  encryptedPath,
		data:           data,
		params:         params,
		pathForRescale: state.BuildObjectKey(testPathBase, filepath.Base(localPath), params.RandomSuffix),
	}
}

// writeState plants the checkpoint an interrupted attempt left behind.
func (f *azureResumeFixture) writeState(t *testing.T, blockSize int64, blockIDs []string, uploadedBytes int64) {
	t.Helper()
	testsupport.WriteResumeState(t, f.localPath, &state.UploadResumeState{
		LocalPath:     f.localPath,
		EncryptedPath: f.encryptedPath,
		ObjectKey:     f.pathForRescale,
		TotalSize:     int64(len(f.data)),
		OriginalSize:  int64(len(f.data)),
		SourceModTime: f.params.SourceModTime,
		UploadedBytes: uploadedBytes,
		BlockIDs:      blockIDs,
		PartSize:      blockSize,
		RandomSuffix:  f.params.RandomSuffix,
		CreatedAt:     time.Now(),
		LastUpdate:    time.Now(),
		StorageType:   "AzureStorage",
	})
}

func (f *azureResumeFixture) run(t *testing.T, azureClient *AzureClient, concurrent bool) error {
	t.Helper()
	provider := &Provider{}
	if concurrent {
		f.params.TransferHandle = testsupport.MultiThreadedHandle(t)
		return provider.uploadEncryptedBlockBlobConcurrent(context.Background(), azureClient, f.params, "blob", f.pathForRescale, int64(len(f.data)))
	}
	return provider.uploadEncryptedBlockBlob(context.Background(), azureClient, f.params, "blob", f.pathForRescale, int64(len(f.data)))
}

// TestPreEncryptBlockBlobResumesBlocksWithAGap is the out-of-order half of the
// resume. The checkpoint keeps the bytes of every completed block but compacts
// their IDs into a list without indices, so a gap makes the count and the byte
// total describe different geometries: the attempt restarts at the wrong offset
// and commits a block list that is not the file.
func TestPreEncryptBlockBlobResumesBlocksWithAGap(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeBlobBackend(t)
			azureClient := newTestAzureClient(t, server)

			encryptedSize := 3*resumeBlockSize + 1024*1024
			fixture := newAzureResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   resumeBlockSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			// Blocks 0, 1 and 3 landed; block 2 never did. The list carries no
			// indices, so only the IDs say which blocks these are.
			fixture.writeState(t, resumeBlockSize,
				[]string{testBlockID(0), testBlockID(1), testBlockID(3)},
				encryptedSize-resumeBlockSize)

			if err := fixture.run(t, azureClient, tt.concurrent); err != nil {
				t.Fatalf("resumed upload failed: %v", err)
			}

			if got := backend.stagedBlockIDs(); !slices.Equal(got, []string{testBlockID(2)}) {
				t.Errorf("staged %d block(s), want only the missing block 2", len(got))
			}
			wantBlocks := expectedBlockHashes(fixture.data, resumeBlockSize)

			backend.mu.Lock()
			defer backend.mu.Unlock()
			if staged, ok := backend.blocks[testBlockID(2)]; !ok {
				t.Fatal("the missing block was never staged")
			} else if staged.sum != wantBlocks[2] {
				t.Error("block 2 holds different bytes than the file at that offset")
			}
			want := []string{testBlockID(0), testBlockID(1), testBlockID(2), testBlockID(3)}
			if !slices.Equal(backend.committed, want) {
				t.Errorf("committed block list is not the file in index order: %v", backend.committed)
			}
			if backend.commits != 1 {
				t.Errorf("CommitBlockList called %d times, want 1", backend.commits)
			}
		})
	}
}

// TestPreEncryptBlockBlobResumeUsesSavedBlockSize pins the geometry to the
// checkpoint. The plan is recomputed on every attempt, and blocks cut with a
// different size do not line up with the ones the backend already holds.
func TestPreEncryptBlockBlobResumeUsesSavedBlockSize(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeBlobBackend(t)
			azureClient := newTestAzureClient(t, server)

			encryptedSize := 2*resumeBlockSize + 1024*1024
			// This attempt plans smaller blocks than the interrupted one used.
			fixture := newAzureResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   resumeBlockSize / 2,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			fixture.writeState(t, resumeBlockSize, []string{testBlockID(0)}, resumeBlockSize)

			if err := fixture.run(t, azureClient, tt.concurrent); err != nil {
				t.Fatalf("resumed upload failed: %v", err)
			}

			if got := backend.stagedBlockIDs(); !slices.Equal(got, []string{testBlockID(1), testBlockID(2)}) {
				t.Errorf("staged %d block(s), want the 2 the checkpoint was missing", len(got))
			}
			wantBlocks := expectedBlockHashes(fixture.data, resumeBlockSize)

			backend.mu.Lock()
			defer backend.mu.Unlock()
			for _, index := range []int{1, 2} {
				staged, ok := backend.blocks[testBlockID(index)]
				if !ok {
					t.Fatalf("block %d was never staged", index)
				}
				if staged.sum != wantBlocks[index] {
					t.Errorf("block %d was cut with the plan's block size, not the checkpoint's", index)
				}
			}
			want := []string{testBlockID(0), testBlockID(1), testBlockID(2)}
			if !slices.Equal(backend.committed, want) {
				t.Errorf("committed block list is %v, want the 3 blocks the saved size gives", backend.committed)
			}
		})
	}
}

// TestPreEncryptBlockBlobResumeWithoutBlockSizeStartsFresh covers checkpoints
// written before the block size was recorded. Nothing says how the blocks
// already staged were cut, so continuing them is a guess.
func TestPreEncryptBlockBlobResumeWithoutBlockSizeStartsFresh(t *testing.T) {
	for _, tt := range []struct {
		name       string
		concurrent bool
	}{{name: "sequential"}, {name: "concurrent", concurrent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend, server := newFakeBlobBackend(t)
			azureClient := newTestAzureClient(t, server)

			encryptedSize := 2*resumeBlockSize + 1024*1024
			fixture := newAzureResumeFixture(t, encryptedSize, &resources.UploadPlan{
				PartSize:   resumeBlockSize,
				WorkerCap:  4,
				QueueDepth: 4,
			})
			fixture.writeState(t, 0, []string{testBlockID(0)}, resumeBlockSize)

			if err := fixture.run(t, azureClient, tt.concurrent); err != nil {
				t.Fatalf("upload failed: %v", err)
			}

			want := []string{testBlockID(0), testBlockID(1), testBlockID(2)}
			if got := backend.stagedBlockIDs(); !slices.Equal(got, want) {
				t.Errorf("staged %d block(s), want the whole file re-sent", len(got))
			}
			backend.assertCommittedBlocksMatch(t, expectedBlockHashes(fixture.data, resumeBlockSize))
		})
	}
}
