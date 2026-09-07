package upload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/resources"
	internaltransfer "github.com/rescale/rescale-int/internal/transfer"
)

// guardTestDirectory is the runtime directory TestMain points the upload locks
// of this package's tests at.
var guardTestDirectory string

// TestMain keeps the guards of the upload locks these tests take out of the
// directory of whoever runs them. The guard's placement reads the environment,
// which is the only handle this package has on it — the seam itself belongs to
// internal/cloud/state — and a guard is never removed, so a suite that did not
// redirect it left one file per source path behind for good.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "upload-lock-guards-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create a guard directory for the tests: %v\n", err)
		os.Exit(1)
	}
	os.Setenv("XDG_RUNTIME_DIR", dir)
	guardTestDirectory = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// TestUploadLockGuardsStayInTheTestsOwnDirectory pins the redirection above
// against the code that honours it: the guard of a real lock taken by this
// package appears where the environment says, and so nowhere else.
func TestUploadLockGuardsStayInTheTestsOwnDirectory(t *testing.T) {
	localPath, _ := writeStreamingSource(t, 16)
	before := guardsInTestDirectory(t)

	lock, err := state.AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("failed to take an upload lock: %v", err)
	}
	state.ReleaseUploadLock(lock)

	if got := guardsInTestDirectory(t); got != before+1 {
		t.Errorf("the lock of %s left %d guard(s) under %s, want the one it took — anything else it left is in the directory of whoever ran this",
			localPath, got-before, guardTestDirectory)
	}
}

// guardsInTestDirectory counts the guards under the directory TestMain named.
func guardsInTestDirectory(t *testing.T) int {
	t.Helper()
	guards := 0
	err := filepath.WalkDir(guardTestDirectory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".guard") {
			guards++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read the test's guard directory: %v", err)
	}
	return guards
}

// fakeStreamingUploader implements transfer.StreamingConcurrentUploader
// for testing the upload pipeline without real cloud storage.
type fakeStreamingUploader struct {
	// Track what the pipeline does
	mu             sync.Mutex
	initCalled     bool
	encryptCalls   []fakeEncryptCall
	uploadCalls    []fakeUploadCall
	completeCalled bool
	completeParts  []*transfer.PartResult
	abortCalled    bool

	// Configure behavior
	partSize     int64
	limits       resources.UploadLimits
	encryptFn    func(partIndex int64, plaintext []byte) ([]byte, error)
	capturedPlan *resources.UploadPlan
}

type fakeEncryptCall struct {
	partIndex int64
	plaintext []byte
}

type fakeUploadCall struct {
	partIndex  int64
	ciphertext []byte
}

// CloudTransfer base interface method
func (f *fakeStreamingUploader) StorageType() string {
	return "FakeStorage"
}

// StreamingConcurrentUploader interface methods
func (f *fakeStreamingUploader) UploadLimits() resources.UploadLimits {
	if f.limits != (resources.UploadLimits{}) {
		return f.limits
	}
	return resources.UploadLimits{
		StorageType: "FakeStorage",
		MaxParts:    constants.MaxS3UploadParts,
		MaxPartSize: constants.MaxS3PlaintextPartSize,
	}
}

func (f *fakeStreamingUploader) InitStreamingUpload(_ context.Context, params transfer.StreamingUploadInitParams) (*transfer.StreamingUpload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initCalled = true
	f.capturedPlan = params.Plan

	partSize := f.partSize
	if partSize == 0 {
		partSize = 16 * 1024 * 1024 // 16MB default
	}

	totalParts := transfer.CalculateTotalParts(params.FileSize, partSize)

	return &transfer.StreamingUpload{
		UploadID:    "fake-upload-id",
		StoragePath: "fake/path/" + filepath.Base(params.LocalPath),
		MasterKey:   make([]byte, 32),
		InitialIV:   make([]byte, 16),
		PartSize:    partSize,
		LocalPath:   params.LocalPath,
		TotalSize:   params.FileSize,
		TotalParts:  totalParts,
	}, nil
}

func (f *fakeStreamingUploader) InitStreamingUploadFromState(_ context.Context, _ transfer.StreamingUploadResumeParams) (*transfer.StreamingUpload, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeStreamingUploader) ValidateStreamingUploadExists(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (f *fakeStreamingUploader) EncryptStreamingPart(_ context.Context, _ *transfer.StreamingUpload, partIndex int64, plaintext []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Record the call
	plaintextCopy := make([]byte, len(plaintext))
	copy(plaintextCopy, plaintext)
	f.encryptCalls = append(f.encryptCalls, fakeEncryptCall{
		partIndex: partIndex,
		plaintext: plaintextCopy,
	})

	// Use custom encrypt function if provided
	if f.encryptFn != nil {
		return f.encryptFn(partIndex, plaintext)
	}

	// Default: simulate PKCS7 padding (empty plaintext -> 16 bytes of padding)
	if len(plaintext) == 0 {
		return make([]byte, 16), nil // One AES block of padding
	}
	// Non-empty: return plaintext + padding (simplified)
	padded := make([]byte, len(plaintext)+16)
	copy(padded, plaintext)
	return padded, nil
}

func (f *fakeStreamingUploader) UploadCiphertext(_ context.Context, _ *transfer.StreamingUpload, partIndex int64, ciphertext []byte) (*transfer.PartResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ciphertextCopy := make([]byte, len(ciphertext))
	copy(ciphertextCopy, ciphertext)
	f.uploadCalls = append(f.uploadCalls, fakeUploadCall{
		partIndex:  partIndex,
		ciphertext: ciphertextCopy,
	})

	return &transfer.PartResult{
		PartIndex:  partIndex,
		PartNumber: int32(partIndex + 1),
		ETag:       fmt.Sprintf("etag-%d", partIndex),
		Size:       int64(len(ciphertext)),
	}, nil
}

func (f *fakeStreamingUploader) CompleteStreamingUpload(_ context.Context, upload *transfer.StreamingUpload, parts []*transfer.PartResult) (*cloud.UploadResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.completeCalled = true
	f.completeParts = parts

	return &cloud.UploadResult{
		StoragePath:   upload.StoragePath,
		EncryptionKey: upload.MasterKey,
		FormatVersion: 1,
		PartSize:      upload.PartSize,
	}, nil
}

func (f *fakeStreamingUploader) AbortStreamingUpload(_ context.Context, _ *transfer.StreamingUpload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.abortCalled = true
	return nil
}

// TestUploadStreamingEmptyFile exercises the full uploadStreaming pipeline
// with a real 0-byte file on disk and a fake cloud provider.
// This is the exact code path that was broken before the fix.
func TestUploadStreamingEmptyFile(t *testing.T) {
	// Create a real 0-byte temp file
	tmpDir := t.TempDir()
	emptyFile := filepath.Join(tmpDir, "empty.dat")
	if err := os.WriteFile(emptyFile, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create empty file: %v", err)
	}

	fake := &fakeStreamingUploader{}

	// Track progress values
	var progressMu sync.Mutex
	var progressValues []float64

	params := UploadParams{
		LocalPath: emptyFile,
		ProgressCallback: func(progress float64) {
			progressMu.Lock()
			defer progressMu.Unlock()
			progressValues = append(progressValues, progress)
		},
	}

	ctx := context.Background()
	result, err := uploadStreaming(ctx, fake, params, 0)
	if err != nil {
		t.Fatalf("uploadStreaming failed for empty file: %v", err)
	}

	// Verify the upload completed successfully
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StoragePath == "" {
		t.Error("expected non-empty storage path")
	}

	// Verify Init was called
	fake.mu.Lock()
	defer fake.mu.Unlock()

	if !fake.initCalled {
		t.Error("expected InitStreamingUpload to be called")
	}

	// Verify EncryptStreamingPart was called exactly once with empty plaintext
	if len(fake.encryptCalls) != 1 {
		t.Fatalf("expected exactly 1 EncryptStreamingPart call, got %d", len(fake.encryptCalls))
	}
	if fake.encryptCalls[0].partIndex != 0 {
		t.Errorf("expected encrypt call for part 0, got part %d", fake.encryptCalls[0].partIndex)
	}
	if len(fake.encryptCalls[0].plaintext) != 0 {
		t.Errorf("expected empty plaintext, got %d bytes", len(fake.encryptCalls[0].plaintext))
	}

	// Verify UploadCiphertext was called exactly once
	if len(fake.uploadCalls) != 1 {
		t.Fatalf("expected exactly 1 UploadCiphertext call, got %d", len(fake.uploadCalls))
	}
	if fake.uploadCalls[0].partIndex != 0 {
		t.Errorf("expected upload call for part 0, got part %d", fake.uploadCalls[0].partIndex)
	}
	if len(fake.uploadCalls[0].ciphertext) == 0 {
		t.Error("expected non-empty ciphertext (PKCS7 padding produces 16 bytes for empty input)")
	}

	// Verify CompleteStreamingUpload was called with exactly 1 part
	if !fake.completeCalled {
		t.Error("expected CompleteStreamingUpload to be called")
	}
	if len(fake.completeParts) != 1 {
		t.Fatalf("expected 1 completed part, got %d", len(fake.completeParts))
	}
	if fake.completeParts[0].PartIndex != 0 {
		t.Errorf("expected completed part index 0, got %d", fake.completeParts[0].PartIndex)
	}

	// Verify abort was NOT called (success path)
	if fake.abortCalled {
		t.Error("AbortStreamingUpload should not be called on success")
	}

	// Verify progress values: no NaN, final progress should reach 1.0
	progressMu.Lock()
	defer progressMu.Unlock()
	for i, p := range progressValues {
		if math.IsNaN(p) {
			t.Errorf("progress[%d] is NaN — division by zero not guarded for empty files", i)
		}
		if p < 0.0 || p > 1.0 {
			t.Errorf("progress[%d] = %f, expected [0.0, 1.0]", i, p)
		}
	}
}

// TestUploadStreamingNormalFile verifies that the fix doesn't regress
// normal (non-empty) file uploads.
func TestUploadStreamingNormalFile(t *testing.T) {
	// Create a temp file with some content
	tmpDir := t.TempDir()
	normalFile := filepath.Join(tmpDir, "normal.dat")
	data := make([]byte, 1024) // 1KB file
	for i := range data {
		data[i] = byte(i % 256)
	}
	if err := os.WriteFile(normalFile, data, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	fake := &fakeStreamingUploader{
		partSize: 16 * 1024 * 1024, // 16MB — file fits in one part
	}

	params := UploadParams{
		LocalPath: normalFile,
	}

	ctx := context.Background()
	result, err := uploadStreaming(ctx, fake, params, int64(len(data)))
	if err != nil {
		t.Fatalf("uploadStreaming failed for normal file: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	// Normal file should produce exactly 1 encrypt call (fits in one part)
	if len(fake.encryptCalls) != 1 {
		t.Fatalf("expected 1 encrypt call for 1KB file with 16MB parts, got %d", len(fake.encryptCalls))
	}

	// Plaintext should be the file data
	if len(fake.encryptCalls[0].plaintext) != len(data) {
		t.Errorf("expected %d bytes plaintext, got %d", len(data), len(fake.encryptCalls[0].plaintext))
	}

	// Should upload and complete with 1 part
	if len(fake.uploadCalls) != 1 {
		t.Errorf("expected 1 upload call, got %d", len(fake.uploadCalls))
	}
	if !fake.completeCalled {
		t.Error("expected CompleteStreamingUpload to be called")
	}
	if len(fake.completeParts) != 1 {
		t.Errorf("expected 1 completed part, got %d", len(fake.completeParts))
	}
	if fake.abortCalled {
		t.Error("AbortStreamingUpload should not be called on success")
	}
}

// TestUploadStreamingMultiPart verifies multi-part uploads still work correctly
// after the empty file fix.
func TestUploadStreamingMultiPart(t *testing.T) {
	// Create a file that requires 3 parts (partSize=100 bytes, file=250 bytes)
	tmpDir := t.TempDir()
	multiPartFile := filepath.Join(tmpDir, "multipart.dat")
	data := make([]byte, 250)
	for i := range data {
		data[i] = byte(i % 256)
	}
	if err := os.WriteFile(multiPartFile, data, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	fake := &fakeStreamingUploader{
		partSize: 100, // 100 bytes per part → 3 parts for 250 bytes
	}

	params := UploadParams{
		LocalPath: multiPartFile,
	}

	ctx := context.Background()
	result, err := uploadStreaming(ctx, fake, params, int64(len(data)))
	if err != nil {
		t.Fatalf("uploadStreaming failed for multi-part file: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	// 250 bytes / 100 bytes per part = 3 parts
	if len(fake.encryptCalls) != 3 {
		t.Fatalf("expected 3 encrypt calls, got %d", len(fake.encryptCalls))
	}

	// Verify part sizes: 100, 100, 50
	expectedSizes := []int{100, 100, 50}
	for i, expected := range expectedSizes {
		if len(fake.encryptCalls[i].plaintext) != expected {
			t.Errorf("part %d: expected %d bytes, got %d", i, expected, len(fake.encryptCalls[i].plaintext))
		}
	}

	// All 3 parts should be uploaded and completed
	if len(fake.uploadCalls) != 3 {
		t.Errorf("expected 3 upload calls, got %d", len(fake.uploadCalls))
	}
	if !fake.completeCalled {
		t.Error("expected CompleteStreamingUpload to be called")
	}
	if len(fake.completeParts) != 3 {
		t.Errorf("expected 3 completed parts, got %d", len(fake.completeParts))
	}
	if fake.abortCalled {
		t.Error("AbortStreamingUpload should not be called on success")
	}
}

// readCloser pairs an arbitrary Reader with the real file's Closer, so an
// injected source still releases the underlying descriptor.
type readCloser struct {
	io.Reader
	io.Closer
}

// useUploadSource swaps the streaming upload's byte source for the duration of
// the test and restores it afterwards.
func useUploadSource(t *testing.T, open func(path string) (io.ReadCloser, error)) {
	t.Helper()
	original := openUploadSource
	t.Cleanup(func() { openUploadSource = original })
	openUploadSource = open
}

// shortReadingFile opens the real file but delivers its bytes through wrap,
// standing in for a network-backed mount that satisfies a read with fewer bytes
// than were asked for. Only the delivery pattern differs from production.
func shortReadingFile(wrap func(io.Reader) io.Reader) func(string) (io.ReadCloser, error) {
	return func(path string) (io.ReadCloser, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return readCloser{Reader: wrap(f), Closer: f}, nil
	}
}

// TestUploadStreamingShortReads pins the geometry contract. The planner fixes
// TotalParts up front, the provider marks the part at TotalParts-1 as the
// CBC-padded final part, and the completion guard rejects any other count — so
// a source that returns short reads must still yield exactly the planned parts,
// all PartSize except the last, carrying the file's bytes in order. Short reads
// are legal for any Reader and routine on the network-backed filesystems this
// path runs against.
func TestUploadStreamingShortReads(t *testing.T) {
	const partSize = 100

	readers := []struct {
		name string
		wrap func(io.Reader) io.Reader
	}{
		{"whole buffer", func(r io.Reader) io.Reader { return r }},
		{"one byte per read", func(r io.Reader) io.Reader { return iotest.OneByteReader(r) }},
		{"half buffer per read", func(r io.Reader) io.Reader { return iotest.HalfReader(r) }},
	}

	sizes := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"short single part", partSize / 2},
		{"exact single part", partSize},
		{"short final part", 2*partSize + 50},
		{"exact multiple", 3 * partSize},
	}

	for _, rd := range readers {
		for _, sz := range sizes {
			t.Run(rd.name+"/"+sz.name, func(t *testing.T) {
				// 251 is prime, so the byte pattern never repeats on a part
				// boundary and misordered parts cannot compare equal.
				data := make([]byte, sz.size)
				for i := range data {
					data[i] = byte(i % 251)
				}

				file := filepath.Join(t.TempDir(), "shortread.dat")
				if err := os.WriteFile(file, data, 0644); err != nil {
					t.Fatalf("failed to create test file: %v", err)
				}

				useUploadSource(t, shortReadingFile(rd.wrap))

				fake := &fakeStreamingUploader{partSize: partSize}
				fileSize := int64(len(data))
				if _, err := uploadStreaming(context.Background(), fake, UploadParams{LocalPath: file}, fileSize); err != nil {
					t.Fatalf("uploadStreaming failed: %v", err)
				}

				wantParts := int(transfer.CalculateTotalParts(fileSize, partSize))

				fake.mu.Lock()
				defer fake.mu.Unlock()

				if len(fake.encryptCalls) != wantParts {
					t.Fatalf("encrypted %d parts, want the planned %d", len(fake.encryptCalls), wantParts)
				}
				if len(fake.completeParts) != wantParts {
					t.Errorf("completed %d parts, want the planned %d", len(fake.completeParts), wantParts)
				}
				if fake.abortCalled {
					t.Error("AbortStreamingUpload was called for an upload that should have completed")
				}

				var reassembled []byte
				for i, call := range fake.encryptCalls {
					if call.partIndex != int64(i) {
						t.Errorf("part %d carries index %d; the provider pads by index, so they must be dense and ordered", i, call.partIndex)
					}
					if i < wantParts-1 && len(call.plaintext) != partSize {
						t.Errorf("part %d is %d bytes, want a full %d — only the final part may be short", i, len(call.plaintext), partSize)
					}
					reassembled = append(reassembled, call.plaintext...)
				}

				// An exact multiple of the part size must end on a full part,
				// not a trailing zero-byte one.
				wantFinal := 0
				if len(data) > 0 {
					wantFinal = (len(data)-1)%partSize + 1
				}
				if got := len(fake.encryptCalls[wantParts-1].plaintext); got != wantFinal {
					t.Errorf("final part is %d bytes, want %d", got, wantFinal)
				}

				if !bytes.Equal(reassembled, data) {
					t.Errorf("reassembled %d bytes that do not match the %d-byte file", len(reassembled), len(data))
				}
			})
		}
	}
}

// TestUploadStreamingReadErrorAborts pins that a genuine read failure still
// fails the upload. ReadFull absorbs short reads by retrying, and it must not
// absorb this.
func TestUploadStreamingReadErrorAborts(t *testing.T) {
	const partSize = 100

	file := filepath.Join(t.TempDir(), "readerr.dat")
	if err := os.WriteFile(file, make([]byte, 250), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	readFailure := errors.New("input/output error")
	useUploadSource(t, func(string) (io.ReadCloser, error) {
		// Fails partway through the second part, so the failure lands
		// mid-stream rather than on the first read.
		return io.NopCloser(io.MultiReader(
			bytes.NewReader(make([]byte, 150)),
			iotest.ErrReader(readFailure),
		)), nil
	})

	fake := &fakeStreamingUploader{partSize: partSize}
	_, err := uploadStreaming(context.Background(), fake, UploadParams{LocalPath: file}, 250)
	if err == nil {
		t.Fatal("expected the read failure to fail the upload")
	}
	if !errors.Is(err, readFailure) {
		t.Errorf("error %q does not wrap the underlying read failure", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.abortCalled {
		t.Error("expected AbortStreamingUpload to be called after a read failure")
	}
	if fake.completeCalled {
		t.Error("CompleteStreamingUpload must not run after a read failure")
	}
}

// TestUploadStreamingEncryptError verifies proper error handling when
// encryption fails during an empty file upload.
func TestUploadStreamingEncryptError(t *testing.T) {
	tmpDir := t.TempDir()
	emptyFile := filepath.Join(tmpDir, "empty_err.dat")
	if err := os.WriteFile(emptyFile, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create empty file: %v", err)
	}

	fake := &fakeStreamingUploader{
		encryptFn: func(_ int64, _ []byte) ([]byte, error) {
			return nil, fmt.Errorf("encryption hardware failure")
		},
	}

	params := UploadParams{
		LocalPath: emptyFile,
	}

	ctx := context.Background()
	_, err := uploadStreaming(ctx, fake, params, 0)
	if err == nil {
		t.Fatal("expected error from encryption failure, got nil")
	}

	// Should abort on encryption error
	fake.mu.Lock()
	defer fake.mu.Unlock()

	if !fake.abortCalled {
		t.Error("expected AbortStreamingUpload to be called on encryption error")
	}
	if fake.completeCalled {
		t.Error("CompleteStreamingUpload should not be called on error")
	}
}

// TestUploadStreamingPassesPlanToProvider checks the wiring the part-count guard
// depends on: the orchestrator plans the pipeline and the provider receives it,
// rather than each side sizing parts on its own.
func TestUploadStreamingPassesPlanToProvider(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "planned.dat")
	data := make([]byte, 250)
	if err := os.WriteFile(file, data, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	fake := &fakeStreamingUploader{partSize: 100}

	fileSize := int64(len(data))
	if _, err := uploadStreaming(context.Background(), fake, UploadParams{LocalPath: file}, fileSize); err != nil {
		t.Fatalf("uploadStreaming failed: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	if fake.capturedPlan == nil {
		t.Fatal("provider received no upload plan; it would fall back to sizing parts itself")
	}
	want, err := resources.PlanUpload(resources.UploadPlanRequest{
		FileSize: fileSize,
		Threads:  4, // the orchestrator's default when there is no transfer handle
		Limits:   fake.UploadLimits(),
	})
	if err != nil {
		t.Fatalf("reference plan failed: %v", err)
	}
	if *fake.capturedPlan != want {
		t.Errorf("plan = %+v, want %+v", *fake.capturedPlan, want)
	}
}

// TestUploadStreamingRejectsFileTooLargeBeforeInit is the fail-fast requirement:
// a file the backend cannot hold must be refused before a multipart upload is
// opened, not after hundreds of gigabytes have been sent.
func TestUploadStreamingRejectsFileTooLargeBeforeInit(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "toobig.dat")
	if err := os.WriteFile(file, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Two parts of at most 16 MB cannot hold a 1 GB file.
	fake := &fakeStreamingUploader{
		limits: resources.UploadLimits{
			StorageType: "FakeStorage",
			MaxParts:    2,
			MaxPartSize: constants.MinChunkSize,
		},
	}

	_, err := uploadStreaming(context.Background(), fake, UploadParams{LocalPath: file}, 1024*1024*1024)
	if err == nil {
		t.Fatal("expected an oversized file to be rejected")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error %q does not explain that the file is too large", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.initCalled {
		t.Error("InitStreamingUpload was called for a file that cannot be stored")
	}
}

// TestUploadStreamingReleasesPlannedMemory covers the reserve/release lifecycle
// on both the success and the failure path: a finished upload must not keep
// holding the shared budget.
func TestUploadStreamingReleasesPlannedMemory(t *testing.T) {
	const budget = 8 * 1024 * 1024 * 1024

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "released.dat")
	data := make([]byte, 250)
	if err := os.WriteFile(file, data, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	fileSize := int64(len(data))

	tests := []struct {
		name      string
		fake      *fakeStreamingUploader
		wantError bool
	}{
		{
			name: "success",
			fake: &fakeStreamingUploader{partSize: 16 * 1024 * 1024},
		},
		{
			name: "encryption failure",
			fake: &fakeStreamingUploader{
				partSize:  16 * 1024 * 1024,
				encryptFn: func(int64, []byte) ([]byte, error) { return nil, fmt.Errorf("boom") },
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resourceMgr := resources.NewManager(resources.Config{
				MaxThreads:   8,
				AutoScale:    true,
				CPUCores:     8,
				MemoryBudget: budget,
			})
			handle := internaltransfer.NewManager(resourceMgr).AllocateTransfer(fileSize, 1)
			defer handle.Complete()

			_, err := uploadStreaming(context.Background(), tt.fake, UploadParams{
				LocalPath:      file,
				TransferHandle: handle,
			}, fileSize)
			if tt.wantError && err == nil {
				t.Fatal("expected the upload to fail")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("uploadStreaming failed: %v", err)
			}

			if got := resourceMgr.GetAvailableUploadMemory(); got != budget {
				t.Errorf("available upload memory = %d after the upload, want the full %d", got, int64(budget))
			}
		})
	}
}

// TestProgressInterpolatorEmptyFile verifies that the progress interpolator
// handles 0-byte files correctly (no NaN from 0/0 division).
func TestProgressInterpolatorEmptyFile(t *testing.T) {
	var mu sync.Mutex
	var lastProgress float64
	callCount := 0

	callback := func(progress float64) {
		mu.Lock()
		defer mu.Unlock()
		lastProgress = progress
		callCount++
	}

	pi := newProgressInterpolator(callback, 0)
	pi.emitInterpolated()

	mu.Lock()
	defer mu.Unlock()

	if callCount != 1 {
		t.Fatalf("expected callback to be called once, got %d", callCount)
	}
	if math.IsNaN(lastProgress) {
		t.Fatal("progress is NaN — division by zero not guarded for empty files")
	}
	if lastProgress != 1.0 {
		t.Errorf("expected progress 1.0 for empty file, got %f", lastProgress)
	}
}

// TestProgressInterpolatorNormalFile verifies normal progress calculation still works.
func TestProgressInterpolatorNormalFile(t *testing.T) {
	var mu sync.Mutex
	var lastProgress float64

	callback := func(progress float64) {
		mu.Lock()
		defer mu.Unlock()
		lastProgress = progress
	}

	pi := newProgressInterpolator(callback, 1000)

	pi.mu.Lock()
	pi.confirmedBytes = 500
	pi.mu.Unlock()

	pi.emitInterpolated()

	mu.Lock()
	defer mu.Unlock()

	if math.Abs(lastProgress-0.5) > 0.001 {
		t.Errorf("expected progress ~0.5, got %f", lastProgress)
	}
}

// TestProgressInterpolatorStartStop verifies the interpolator goroutine
// starts and stops cleanly for empty files.
func TestProgressInterpolatorStartStop(t *testing.T) {
	callCount := 0
	var mu sync.Mutex

	callback := func(progress float64) {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		if math.IsNaN(progress) {
			t.Error("received NaN progress")
		}
	}

	pi := newProgressInterpolator(callback, 0)
	pi.Start()

	// Let it tick at least once
	time.Sleep(600 * time.Millisecond)

	pi.Stop()

	mu.Lock()
	defer mu.Unlock()

	if callCount == 0 {
		t.Error("expected at least one progress callback from ticker")
	}
}

// fakePreEncryptUploader implements transfer.PreEncryptUploader so the
// pre-encrypt orchestration can be exercised without cloud storage.
type fakePreEncryptUploader struct {
	mu sync.Mutex

	uploadCalled  bool
	capturedPlan  *resources.UploadPlan
	encryptedSize int64

	limits    resources.UploadLimits
	uploadErr error
}

func (f *fakePreEncryptUploader) StorageType() string { return "FakeStorage" }

func (f *fakePreEncryptUploader) UploadLimits() resources.UploadLimits {
	if f.limits != (resources.UploadLimits{}) {
		return f.limits
	}
	return resources.UploadLimits{
		StorageType: "FakeStorage",
		MaxParts:    constants.MaxS3UploadParts,
		MaxPartSize: constants.MaxS3PlaintextPartSize,
	}
}

func (f *fakePreEncryptUploader) UploadEncryptedFile(_ context.Context, params transfer.EncryptedFileUploadParams) (*cloud.UploadResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.uploadCalled = true
	f.capturedPlan = params.Plan
	if info, err := os.Stat(params.EncryptedPath); err == nil {
		f.encryptedSize = info.Size()
	}
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	return &cloud.UploadResult{
		StoragePath:   "fake/path",
		EncryptionKey: params.EncryptionKey,
		IV:            params.IV,
	}, nil
}

// TestUploadPreEncryptPassesPlanToProvider checks the wiring the part-count guard
// depends on: the orchestrator plans the pipeline against the ENCRYPTED file — the
// bytes the backend actually splits into parts — and the provider receives that
// plan rather than sizing parts on its own.
func TestUploadPreEncryptPassesPlanToProvider(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "planned.dat")
	data := make([]byte, 250)
	if err := os.WriteFile(file, data, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	fake := &fakePreEncryptUploader{}
	fileSize := int64(len(data))
	if _, err := uploadPreEncrypt(context.Background(), fake, UploadParams{LocalPath: file}, fileSize); err != nil {
		t.Fatalf("uploadPreEncrypt failed: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	if fake.capturedPlan == nil {
		t.Fatal("provider received no upload plan; it would fall back to sizing parts itself")
	}
	if fake.encryptedSize <= fileSize {
		t.Fatalf("encrypted file is %d bytes, expected padding beyond the %d-byte original", fake.encryptedSize, fileSize)
	}
	want, err := resources.PlanUpload(resources.UploadPlanRequest{
		FileSize: fake.encryptedSize,
		Threads:  1, // no transfer handle: the pre-encrypt path uploads sequentially
		Limits:   fake.UploadLimits(),
	})
	if err != nil {
		t.Fatalf("reference plan failed: %v", err)
	}
	if *fake.capturedPlan != want {
		t.Errorf("plan = %+v, want %+v", *fake.capturedPlan, want)
	}
}

// TestUploadPreEncryptRejectsFileTooLargeBeforeUpload is the fail-fast
// requirement: a file the backend cannot hold must be refused before the upload
// starts, not after parts have been sent.
func TestUploadPreEncryptRejectsFileTooLargeBeforeUpload(t *testing.T) {
	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "toobig.dat")
	if err := os.WriteFile(file, make([]byte, 250), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// A backend that accepts one part of under a MiB cannot hold any file: part
	// sizes are whole MiB multiples, so no part size fits inside its limit.
	fake := &fakePreEncryptUploader{
		limits: resources.UploadLimits{
			StorageType: "FakeStorage",
			MaxParts:    1,
			MaxPartSize: constants.PartSizeAlignment - 1,
		},
	}

	_, err := uploadPreEncrypt(context.Background(), fake, UploadParams{LocalPath: file}, 250)
	if err == nil {
		t.Fatal("expected an oversized file to be rejected")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error %q does not explain that the file is too large", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.uploadCalled {
		t.Error("UploadEncryptedFile was called for a file that cannot be stored")
	}
}

// TestUploadPreEncryptReleasesPlannedMemory covers the reserve/release lifecycle
// on both the success and the failure path: a finished upload must not keep
// holding the shared budget.
func TestUploadPreEncryptReleasesPlannedMemory(t *testing.T) {
	const budget = 8 * 1024 * 1024 * 1024

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "released.dat")
	if err := os.WriteFile(file, make([]byte, 250), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	fileSize := int64(250)

	tests := []struct {
		name      string
		fake      *fakePreEncryptUploader
		wantError bool
	}{
		{name: "success", fake: &fakePreEncryptUploader{}},
		{
			name:      "upload failure",
			fake:      &fakePreEncryptUploader{uploadErr: fmt.Errorf("boom")},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resourceMgr := resources.NewManager(resources.Config{
				MaxThreads:   8,
				AutoScale:    true,
				CPUCores:     8,
				MemoryBudget: budget,
			})
			handle := internaltransfer.NewManager(resourceMgr).AllocateTransfer(fileSize, 1)
			defer handle.Complete()

			_, err := uploadPreEncrypt(context.Background(), tt.fake, UploadParams{
				LocalPath:      file,
				TransferHandle: handle,
			}, fileSize)
			if tt.wantError && err == nil {
				t.Fatal("expected the upload to fail")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("uploadPreEncrypt failed: %v", err)
			}

			if got := resourceMgr.GetAvailableUploadMemory(); got != budget {
				t.Errorf("available upload memory = %d after the upload, want the full %d", got, int64(budget))
			}
		})
	}
}

// The registered DecryptedSize is the stat taken before the transfer and the
// registered SHA-512 is computed by re-reading the file after it. A file that
// changes in between is registered with a size and a checksum that describe
// neither the uploaded bytes nor each other, and every later download of it
// fails verification with nothing to point at. Refusing the registration keeps
// the mismatch out of the library and names the file that moved.
func TestCheckSourceUnchanged(t *testing.T) {
	// The pre-upload stat UploadFile passes in.
	statOf := func(t *testing.T, path string) os.FileInfo {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		return info
	}

	newFile := func(t *testing.T, contents string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "case.inp")
		if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return path
	}

	t.Run("untouched during the upload", func(t *testing.T) {
		path := newFile(t, "input deck")
		if err := checkSourceUnchanged(path, statOf(t, path)); err != nil {
			t.Errorf("checkSourceUnchanged on an untouched file: %v", err)
		}
	})

	t.Run("grew during the upload", func(t *testing.T) {
		path := newFile(t, "input deck")
		before := statOf(t, path)

		if err := os.WriteFile(path, []byte("input deck, revised"), 0644); err != nil {
			t.Fatalf("rewrite: %v", err)
		}

		err := checkSourceUnchanged(path, before)
		if err == nil {
			t.Fatal("a file that grew mid-upload was accepted for registration")
		}
		if !strings.Contains(err.Error(), filepath.Base(path)) {
			t.Errorf("error = %v, want it to name %s", err, filepath.Base(path))
		}
		if !strings.Contains(err.Error(), "changed during") {
			t.Errorf("error = %v, want it to say the file changed during the upload", err)
		}
	})

	t.Run("rewritten at the same size during the upload", func(t *testing.T) {
		path := newFile(t, "input deck")
		before := statOf(t, path)

		// Same length, different bytes: the size still matches, so the
		// modification time is what is left to catch it.
		if err := os.WriteFile(path, []byte("INPUT DECK"), 0644); err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		if err := os.Chtimes(path, time.Now(), before.ModTime().Add(time.Second)); err != nil {
			t.Fatalf("chtimes: %v", err)
		}

		if err := checkSourceUnchanged(path, before); err == nil {
			t.Fatal("a file rewritten at the same size mid-upload was accepted for registration")
		}
	})

	t.Run("gone by the end of the upload", func(t *testing.T) {
		path := newFile(t, "input deck")
		before := statOf(t, path)

		if err := os.Remove(path); err != nil {
			t.Fatalf("remove: %v", err)
		}

		if err := checkSourceUnchanged(path, before); err == nil {
			t.Fatal("a file that vanished mid-upload was accepted for registration")
		}
	})
}

// =============================================================================
// Pre-encrypt resume (F10)
// =============================================================================

// resumableFakeUploader is a pre-encrypt provider that behaves the way the real
// ones do around resume state: it stages parts of the encrypted copy, saves the
// state after each, and continues from the parts already recorded when the
// state describes the same object. What it adds is a record of every attempt
// and every staged part, so a test can see whether a second run reused the
// first run's identity or started a new object.
type resumableFakeUploader struct {
	mu sync.Mutex

	partSize       int64
	failAfterParts int // parts to stage in a failing attempt before giving up

	// storageID and storageContainer name the destination this provider uploads
	// to, which is what it records in the checkpoints it writes — as the real
	// providers do, so that a checkpoint can be told apart from another
	// destination's.
	storageID        string
	storageContainer string

	// abortRequests is every upload ID this provider discarded on its own
	// account, reading a checkpoint that names an object it is not filling.
	// The real S3 concurrent path does exactly that, addressed through the
	// destination it was built for.
	abortRequests []string

	// backend is shared with the streaming fake when a test needs the two modes
	// to meet over one source, which is where the cross-mode leftovers live.
	backend *fakeStreamingBackend

	// duringUpload runs inside UploadEncryptedFile, where the provider does its
	// work: what the orchestrator still holds at that point is what the provider
	// is protected by.
	duringUpload func()

	attempts []preEncryptAttempt
	staged   []stagedFakePart
}

type preEncryptAttempt struct {
	encryptedPath string
	encryptionKey []byte
	iv            []byte
	randomSuffix  string
	objectKey     string
	resumedFrom   int
	planPartSize  int64
}

type stagedFakePart struct {
	objectKey  string
	partNumber int32
	data       []byte
}

func (f *resumableFakeUploader) StorageType() string { return "FakeStorage" }

func (f *resumableFakeUploader) UploadLimits() resources.UploadLimits {
	return resources.UploadLimits{
		StorageType: "FakeStorage",
		MaxParts:    constants.MaxS3UploadParts,
		MaxPartSize: constants.MaxS3PlaintextPartSize,
	}
}

// AbortUploadByID discards an upload named only by the identity a resume state
// records, which is all a state left by the other upload mode carries.
func (f *resumableFakeUploader) AbortUploadByID(ctx context.Context, uploadID, _ string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("the abort never reached the backend: %w", err)
	}
	if f.backend == nil {
		return nil
	}
	f.backend.mu.Lock()
	defer f.backend.mu.Unlock()
	if upload := f.backend.uploads[uploadID]; upload != nil {
		upload.aborted = true
	}
	return nil
}

func (f *resumableFakeUploader) UploadEncryptedFile(_ context.Context, params transfer.EncryptedFileUploadParams) (*cloud.UploadResult, error) {
	if f.duringUpload != nil {
		f.duringUpload()
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	objectKey := state.BuildObjectKey("uploads", filepath.Base(params.LocalPath), params.RandomSuffix)

	encrypted, err := os.ReadFile(params.EncryptedPath)
	if err != nil {
		return nil, fmt.Errorf("fake provider: %w", err)
	}

	// A stateless attempt holds no lock on this source, and one whose caller has
	// already judged the checkpoint has to treat it as absent; the real
	// providers read it in neither case, so this one does the same.
	var completed []state.CompletedPart
	var uploadedBytes int64
	if saved, _ := state.LoadUploadState(params.LocalPath); !params.Stateless && !params.IgnoreResumeState && saved != nil {
		switch {
		case saved.ObjectKey != objectKey:
			// The checkpoint describes a different object, so its parts are
			// unusable. The real S3 concurrent path discards that upload — an
			// abort addressed through the destination THIS provider was built
			// for — and deletes the record.
			if saved.UploadID != "" {
				f.abortRequests = append(f.abortRequests, saved.UploadID)
			}
			state.DeleteUploadState(params.LocalPath)
		case state.ValidateUploadState(saved, params.LocalPath) == nil:
			completed = saved.CompletedParts
			uploadedBytes = saved.UploadedBytes
		}
	}

	attempt := preEncryptAttempt{
		encryptedPath: params.EncryptedPath,
		encryptionKey: params.EncryptionKey,
		iv:            params.IV,
		randomSuffix:  params.RandomSuffix,
		objectKey:     objectKey,
		resumedFrom:   len(completed),
	}
	if params.Plan != nil {
		attempt.planPartSize = params.Plan.PartSize
	}

	stagedThisAttempt := 0
	for uploadedBytes < int64(len(encrypted)) {
		end := uploadedBytes + f.partSize
		if end > int64(len(encrypted)) {
			end = int64(len(encrypted))
		}
		partNumber := int32(len(completed)) + 1
		f.staged = append(f.staged, stagedFakePart{
			objectKey:  objectKey,
			partNumber: partNumber,
			data:       append([]byte(nil), encrypted[uploadedBytes:end]...),
		})
		completed = append(completed, state.CompletedPart{PartNumber: partNumber, ETag: fmt.Sprintf("etag-%d", partNumber)})
		uploadedBytes = end
		stagedThisAttempt++

		if !params.Stateless {
			// A checkpoint that cannot be written costs the ability to resume,
			// not the upload: the real providers drop this error too.
			state.SaveUploadState(&state.UploadResumeState{
				LocalPath:      params.LocalPath,
				EncryptedPath:  params.EncryptedPath,
				ObjectKey:      objectKey,
				UploadID:       "fake-upload-id",
				TotalSize:      int64(len(encrypted)),
				OriginalSize:   params.OriginalSize,
				SourceModTime:  params.SourceModTime,
				UploadedBytes:  uploadedBytes,
				CompletedParts: completed,
				PartSize:       f.partSize,
				EncryptionKey:  encryption.EncodeBase64(params.EncryptionKey),
				IV:             encryption.EncodeBase64(params.IV),
				RandomSuffix:   params.RandomSuffix,
				CreatedAt:      time.Now(),
				LastUpdate:     time.Now(),
				StorageType:    "FakeStorage",
				StorageID:      f.storageID,
				Container:      f.storageContainer,
			}, params.LocalPath)
		}

		if f.failAfterParts > 0 && stagedThisAttempt >= f.failAfterParts {
			f.attempts = append(f.attempts, attempt)
			return nil, errors.New("fake provider: connection reset")
		}
	}

	f.attempts = append(f.attempts, attempt)
	if !params.Stateless {
		state.DeleteUploadState(params.LocalPath)
	}
	return &cloud.UploadResult{
		StoragePath:   objectKey,
		EncryptionKey: params.EncryptionKey,
		IV:            params.IV,
	}, nil
}

// TestUploadPreEncryptResumesInterruptedUpload is the F10 regression: the
// orchestrator generated a fresh key, IV and object suffix on every attempt and
// deleted the encrypted copy on the way out, so the parts the backend had
// already accepted described ciphertext nothing would ever ask for again.
func TestUploadPreEncryptResumesInterruptedUpload(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "resume.dat")
	plaintext := make([]byte, 300)
	for i := range plaintext {
		plaintext[i] = byte(i)
	}
	if err := os.WriteFile(source, plaintext, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	fake := &resumableFakeUploader{partSize: 64, failAfterParts: 2}
	params := UploadParams{LocalPath: source, PreEncrypt: true}

	if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(plaintext))); err == nil {
		t.Fatal("first attempt was expected to fail")
	}

	fake.mu.Lock()
	if len(fake.attempts) != 1 {
		fake.mu.Unlock()
		t.Fatalf("provider saw %d attempts, want 1", len(fake.attempts))
	}
	first := fake.attempts[0]
	fake.mu.Unlock()

	// The encrypted copy is the artifact the retry needs; deleting it on an
	// ordinary failure is what forced every retry to start over.
	encryptedAfterFailure, err := os.ReadFile(first.encryptedPath)
	if err != nil {
		t.Fatalf("the encrypted copy was discarded on failure: %v", err)
	}
	if !state.UploadResumeStateExists(source) {
		t.Fatal("the resume state was discarded on failure")
	}

	fake.failAfterParts = 0
	if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(plaintext))); err != nil {
		t.Fatalf("second attempt failed: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	if len(fake.attempts) != 2 {
		t.Fatalf("provider saw %d attempts, want 2", len(fake.attempts))
	}
	second := fake.attempts[1]

	if second.randomSuffix != first.randomSuffix {
		t.Errorf("object suffix changed between attempts (%q then %q): the parts already sent belong to the first object",
			first.randomSuffix, second.randomSuffix)
	}
	if !bytes.Equal(second.encryptionKey, first.encryptionKey) {
		t.Error("the retry was given a different encryption key, so its parts are a different ciphertext")
	}
	if !bytes.Equal(second.iv, first.iv) {
		t.Error("the retry was given a different IV, so its parts are a different ciphertext")
	}
	if second.encryptedPath != first.encryptedPath {
		t.Errorf("the retry re-encrypted into %q instead of reusing %q", second.encryptedPath, first.encryptedPath)
	}
	if second.resumedFrom != 2 {
		t.Errorf("the retry resumed from %d completed parts, want the 2 the first attempt staged", second.resumedFrom)
	}

	// Exactly the whole ciphertext, once, in order, under one object key.
	var assembled []byte
	for i, part := range fake.staged {
		if part.objectKey != first.objectKey {
			t.Fatalf("part %d went to object %q, want %q", i+1, part.objectKey, first.objectKey)
		}
		if part.partNumber != int32(i+1) {
			t.Fatalf("staged part %d is numbered %d", i+1, part.partNumber)
		}
		assembled = append(assembled, part.data...)
	}
	if !bytes.Equal(assembled, encryptedAfterFailure) {
		t.Errorf("the staged parts assemble to %d bytes, want the %d-byte encrypted file",
			len(assembled), len(encryptedAfterFailure))
	}

	// Verified completion is what retires the artifacts.
	if state.UploadResumeStateExists(source) {
		t.Error("the resume state survived a completed upload")
	}
	if _, err := os.Stat(first.encryptedPath); !os.IsNotExist(err) {
		t.Errorf("the encrypted copy survived a completed upload: %v", err)
	}
}

// TestUploadPreEncryptAbandonsStateWhenSourceChanged covers the other side: the
// ciphertext of an earlier version of the file must never be finished under a
// registration that describes the current one.
func TestUploadPreEncryptAbandonsStateWhenSourceChanged(t *testing.T) {
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "changed.dat")
	plaintext := make([]byte, 300)
	if err := os.WriteFile(source, plaintext, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	fake := &resumableFakeUploader{partSize: 64, failAfterParts: 2}
	params := UploadParams{LocalPath: source, PreEncrypt: true}

	if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(plaintext))); err == nil {
		t.Fatal("first attempt was expected to fail")
	}

	fake.mu.Lock()
	first := fake.attempts[0]
	fake.mu.Unlock()

	// Same length, different content and a later modification time: the size
	// check alone cannot see this.
	rewritten := make([]byte, len(plaintext))
	for i := range rewritten {
		rewritten[i] = 0xAB
	}
	if err := os.WriteFile(source, rewritten, 0644); err != nil {
		t.Fatalf("failed to rewrite test file: %v", err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(source, later, later); err != nil {
		t.Fatalf("failed to age test file: %v", err)
	}

	fake.failAfterParts = 0
	if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(rewritten))); err != nil {
		t.Fatalf("second attempt failed: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	second := fake.attempts[1]

	if second.randomSuffix == first.randomSuffix {
		t.Error("the changed file was uploaded under the interrupted upload's object identity")
	}
	if second.resumedFrom != 0 {
		t.Errorf("the changed file resumed from %d parts of the previous version", second.resumedFrom)
	}
	if _, err := os.Stat(first.encryptedPath); !os.IsNotExist(err) {
		t.Errorf("the abandoned encrypted copy was left behind: %v", err)
	}
}

// =============================================================================
// Streaming upload resume
//
// The fake below is the cloud side of a streaming upload rather than a script of
// expected calls: it keeps the parts across attempts, so a resumed upload is
// checked against what the interrupted one actually left on the backend. The
// encryption is the real CBC chain, because "the ciphertext is the same as an
// uninterrupted upload's" is the property that matters and a stand-in cipher
// cannot show it.
// =============================================================================

// fakeStreamingBackend holds the multipart uploads of a test, across attempts.
type fakeStreamingBackend struct {
	mu      sync.Mutex
	uploads map[string]*fakeBackendUpload
	next    int
}

type fakeBackendUpload struct {
	// bucket is the namespace this upload lives in. An upload ID names an upload
	// within one bucket or container; addressed through another one it is simply
	// absent, which is what makes a retirement sent to the wrong destination
	// look like a successful one.
	bucket    string
	objectKey string
	parts     map[int64][]byte // ciphertext, by part index
	handles   map[int64]string // what the backend named each part
	committed []string         // the handles the commit was asked to assemble
	aborted   bool
}

func newFakeStreamingBackend() *fakeStreamingBackend {
	return &fakeStreamingBackend{uploads: make(map[string]*fakeBackendUpload)}
}

func (b *fakeStreamingBackend) create(bucket, objectKey string) (string, *fakeBackendUpload) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	id := fmt.Sprintf("upload-%d", b.next)
	upload := &fakeBackendUpload{
		bucket:    bucket,
		objectKey: objectKey,
		parts:     make(map[int64][]byte),
		handles:   make(map[int64]string),
	}
	b.uploads[id] = upload
	return id, upload
}

func (b *fakeStreamingBackend) get(id string) *fakeBackendUpload {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.uploads[id]
}

// object returns the bytes the committed block/part list assembles, which is the
// object a download would see.
func (b *fakeStreamingBackend) object(t *testing.T, id string) []byte {
	t.Helper()
	upload := b.get(id)
	if upload == nil {
		t.Fatalf("no upload %q on the backend", id)
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	var object []byte
	for _, handle := range upload.committed {
		index := int64(-1)
		for i, h := range upload.handles {
			if h == handle {
				index = i
			}
		}
		if index < 0 {
			t.Fatalf("committed handle %q was never staged", handle)
		}
		object = append(object, upload.parts[index]...)
	}
	return object
}

// resumableStreamingUploader is one attempt at a streaming upload. A new one is
// made per attempt, as the real providers are, sharing the backend.
type resumableStreamingUploader struct {
	backend  *fakeStreamingBackend
	partSize int64

	// bucket is the namespace this provider addresses uploads in — the storage
	// it was built for. Tests that upload one source to two destinations give
	// their two providers different ones.
	bucket string

	// failFrom interrupts the attempt at a known boundary: every part from this
	// index upwards fails, and waits for the parts below it to land first, so
	// the backend is left holding exactly that prefix however the workers
	// happened to interleave. -1 leaves every part alone.
	failFrom int64

	// failPart fails exactly one part, leaving the parts on either side of it to
	// land. That is what makes a gap: a checkpoint of every completed part and a
	// checkpoint of the contiguous prefix differ only when one exists.
	failPart int64

	// holdPart is held back until holdUntil other parts have finished, so the
	// completed parts deliberately do not form a prefix.
	holdPart  int64
	holdUntil int
	release   chan struct{}

	mu          sync.Mutex
	uploadID    string
	encrypted   []int64
	uploaded    []int64
	resumedFrom []*transfer.PartResult

	// abortRequests is every upload ID this provider was asked to discard,
	// whether or not its own bucket holds one by that name.
	abortRequests []string

	// abortDeadline is how long the last abort's context had left to run, which
	// is what bounds a cancelled transfer once the abort is detached from it.
	abortDeadline time.Duration

	// initPartSize is the part size the plan carried when a fresh upload was
	// opened, which is the geometry the whole object is then stuck with.
	initPartSize int64
}

func newResumableStreamingUploader(backend *fakeStreamingBackend, partSize int64) *resumableStreamingUploader {
	return &resumableStreamingUploader{
		backend:  backend,
		partSize: partSize,
		failFrom: -1,
		failPart: -1,
		holdPart: -1,
		release:  make(chan struct{}),
	}
}

func (u *resumableStreamingUploader) StorageType() string { return "FakeStorage" }

func (u *resumableStreamingUploader) UploadLimits() resources.UploadLimits {
	return resources.UploadLimits{
		StorageType: "FakeStorage",
		MaxParts:    constants.MaxS3UploadParts,
		MaxPartSize: constants.MaxS3PlaintextPartSize,
	}
}

func (u *resumableStreamingUploader) InitStreamingUpload(_ context.Context, params transfer.StreamingUploadInitParams) (*transfer.StreamingUpload, error) {
	encryptState, err := transfer.NewStreamingEncryptionState(u.partSize)
	if err != nil {
		return nil, err
	}
	suffix := fmt.Sprintf("suffix-%d", len(u.backend.uploads)+1)
	uploadID, _ := u.backend.create(u.bucket, "fake/path/"+filepath.Base(params.LocalPath)+"-"+suffix)

	u.mu.Lock()
	u.uploadID = uploadID
	if params.Plan != nil {
		u.initPartSize = params.Plan.PartSize
	}
	u.mu.Unlock()

	return &transfer.StreamingUpload{
		UploadID:     uploadID,
		StoragePath:  u.backend.get(uploadID).objectKey,
		MasterKey:    encryptState.GetKey(),
		InitialIV:    encryptState.GetInitialIV(),
		EncryptState: encryptState,
		PartSize:     u.partSize,
		LocalPath:    params.LocalPath,
		TotalSize:    params.FileSize,
		TotalParts:   transfer.CalculateTotalParts(params.FileSize, u.partSize),
		RandomSuffix: suffix,
	}, nil
}

func (u *resumableStreamingUploader) InitStreamingUploadFromState(_ context.Context, params transfer.StreamingUploadResumeParams) (*transfer.StreamingUpload, error) {
	encryptState, err := transfer.NewStreamingEncryptionStateFromKey(
		params.MasterKey, params.InitialIV, params.CurrentIV, params.PartSize)
	if err != nil {
		return nil, err
	}

	u.mu.Lock()
	u.uploadID = params.UploadID
	u.resumedFrom = params.CompletedParts
	u.mu.Unlock()

	return &transfer.StreamingUpload{
		UploadID:     params.UploadID,
		StoragePath:  params.StoragePath,
		MasterKey:    params.MasterKey,
		InitialIV:    params.InitialIV,
		EncryptState: encryptState,
		PartSize:     params.PartSize,
		LocalPath:    params.LocalPath,
		TotalSize:    params.FileSize,
		TotalParts:   transfer.CalculateTotalParts(params.FileSize, params.PartSize),
		RandomSuffix: params.RandomSuffix,
	}, nil
}

func (u *resumableStreamingUploader) ValidateStreamingUploadExists(_ context.Context, uploadID, _ string) (bool, error) {
	upload := u.backend.get(uploadID)
	return upload != nil && upload.bucket == u.bucket && !upload.aborted, nil
}

func (u *resumableStreamingUploader) EncryptStreamingPart(_ context.Context, uploadState *transfer.StreamingUpload, partIndex int64, plaintext []byte) ([]byte, error) {
	u.mu.Lock()
	u.encrypted = append(u.encrypted, partIndex)
	u.mu.Unlock()

	return uploadState.EncryptState.EncryptPart(plaintext, partIndex == uploadState.TotalParts-1)
}

func (u *resumableStreamingUploader) UploadCiphertext(ctx context.Context, uploadState *transfer.StreamingUpload, partIndex int64, ciphertext []byte) (*transfer.PartResult, error) {
	if partIndex == u.holdPart {
		select {
		case <-u.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if u.failFrom >= 0 && partIndex >= u.failFrom {
		// Wait for the prefix below the boundary, so "interrupted after k parts"
		// means exactly that and not "after whichever k the scheduler let past".
		if err := u.waitForUploaded(ctx, int(u.failFrom)); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("fake backend refused part %d", partIndex)
	}
	if partIndex == u.failPart {
		return nil, fmt.Errorf("fake backend refused part %d", partIndex)
	}

	handle := fmt.Sprintf("%s-part-%d", uploadState.UploadID, partIndex)
	upload := u.backend.get(uploadState.UploadID)

	u.backend.mu.Lock()
	stored := make([]byte, len(ciphertext))
	copy(stored, ciphertext)
	upload.parts[partIndex] = stored
	upload.handles[partIndex] = handle
	u.backend.mu.Unlock()

	u.mu.Lock()
	u.uploaded = append(u.uploaded, partIndex)
	done := len(u.uploaded)
	u.mu.Unlock()

	if u.holdPart >= 0 && done >= u.holdUntil {
		select {
		case <-u.release:
		default:
			close(u.release)
		}
	}

	return &transfer.PartResult{
		PartIndex:  partIndex,
		PartNumber: int32(partIndex + 1),
		ETag:       handle,
		Size:       int64(len(ciphertext)),
	}, nil
}

func (u *resumableStreamingUploader) CompleteStreamingUpload(_ context.Context, uploadState *transfer.StreamingUpload, parts []*transfer.PartResult) (*cloud.UploadResult, error) {
	upload := u.backend.get(uploadState.UploadID)

	u.backend.mu.Lock()
	defer u.backend.mu.Unlock()
	upload.committed = upload.committed[:0]
	for _, part := range parts {
		if part == nil {
			return nil, fmt.Errorf("the commit was handed a hole where a part should be")
		}
		upload.committed = append(upload.committed, part.ETag)
	}

	return &cloud.UploadResult{
		StoragePath:   uploadState.StoragePath,
		EncryptionKey: uploadState.MasterKey,
		IV:            uploadState.InitialIV,
		PartSize:      uploadState.PartSize,
	}, nil
}

// AbortStreamingUpload refuses a cancelled context, as a backend does: an abort
// is an ordinary request, and one issued on the context that was just cancelled
// never leaves the process. Without this the fake would accept the abort the
// real backend would never see.
func (u *resumableStreamingUploader) AbortStreamingUpload(ctx context.Context, uploadState *transfer.StreamingUpload) error {
	return u.abort(ctx, uploadState.UploadID)
}

// AbortUploadByID discards an upload named only by the identity a resume state
// records, which is what a state too damaged to rebuild a handle from still has.
func (u *resumableStreamingUploader) AbortUploadByID(ctx context.Context, uploadID, _ string) error {
	return u.abort(ctx, uploadID)
}

func (u *resumableStreamingUploader) abort(ctx context.Context, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("the abort never reached the backend: %w", err)
	}
	u.mu.Lock()
	u.abortRequests = append(u.abortRequests, uploadID)
	if deadline, ok := ctx.Deadline(); ok {
		u.abortDeadline = time.Until(deadline)
	}
	u.mu.Unlock()

	u.backend.mu.Lock()
	defer u.backend.mu.Unlock()
	// An upload ID this provider's bucket does not hold is simply absent, which
	// both backends report as NoSuchUpload and both providers read as success.
	if upload := u.backend.uploads[uploadID]; upload != nil && upload.bucket == u.bucket {
		upload.aborted = true
	}
	return nil
}

// waitForUploaded blocks until the attempt has landed at least count parts.
func (u *resumableStreamingUploader) waitForUploaded(ctx context.Context, count int) error {
	for {
		u.mu.Lock()
		done := len(u.uploaded)
		u.mu.Unlock()
		if done >= count {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

// uninterruptedCiphertext is what a single-pass upload of data under this key
// produces: the reference the resumed object has to match byte for byte.
func uninterruptedCiphertext(t *testing.T, data, masterKey, initialIV []byte, partSize int64) []byte {
	t.Helper()
	// currentIV = initialIV places the chain at part 0, where every upload starts.
	encryptState, err := transfer.NewStreamingEncryptionStateFromKey(masterKey, initialIV, initialIV, partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptionStateFromKey: %v", err)
	}

	totalParts := transfer.CalculateTotalParts(int64(len(data)), partSize)
	var object []byte
	for index := int64(0); index < totalParts; index++ {
		end := (index + 1) * partSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		part, err := encryptState.EncryptPart(data[index*partSize:end], index == totalParts-1)
		if err != nil {
			t.Fatalf("EncryptPart(%d): %v", index, err)
		}
		object = append(object, part...)
	}
	return object
}

// writeStreamingSource writes a file of size bytes with recognisable contents.
func writeStreamingSource(t *testing.T, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*7 + 1)
	}
	path := filepath.Join(t.TempDir(), "streamed.dat")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	return path, data
}

func loadStreamingState(t *testing.T, localPath string) *state.UploadResumeState {
	t.Helper()
	saved, err := state.LoadUploadState(localPath)
	if err != nil {
		t.Fatalf("failed to load the resume state: %v", err)
	}
	return saved
}

// TestUploadStreamingResumesInterruptedUpload is the regression for the default
// upload mode having no resume at all: uploadStreaming never recorded where it
// had got to, aborted the multipart upload on every failure, and could not have
// continued one anyway, because nothing saved the CBC chain position that
// InitStreamingUploadFromState needs.
func TestUploadStreamingResumesInterruptedUpload(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 5*partSize) // five whole parts
	backend := newFakeStreamingBackend()

	first := newResumableStreamingUploader(backend, partSize)
	first.failFrom = 2 // interrupted with two parts on the backend
	params := UploadParams{LocalPath: localPath}

	if _, err := uploadStreaming(context.Background(), first, params, int64(len(data))); err == nil {
		t.Fatal("expected the interrupted attempt to fail")
	}

	uploadID := first.uploadID
	if upload := backend.get(uploadID); upload.aborted {
		t.Fatal("an ordinary failure aborted the upload the retry has to continue")
	}
	saved := loadStreamingState(t, localPath)
	if saved == nil {
		t.Fatal("the interrupted attempt recorded nothing to resume from")
	}
	if len(saved.StreamingParts) != 2 {
		t.Fatalf("checkpointed %d parts, want the 2 the backend accepted", len(saved.StreamingParts))
	}

	second := newResumableStreamingUploader(backend, partSize)
	result, err := uploadStreaming(context.Background(), second, params, int64(len(data)))
	if err != nil {
		t.Fatalf("the resumed attempt failed: %v", err)
	}

	if second.uploadID != uploadID {
		t.Fatalf("the second attempt filled upload %q, want it to continue the interrupted %q", second.uploadID, uploadID)
	}
	if result.StoragePath != backend.get(uploadID).objectKey {
		t.Errorf("registered object %q, want %q", result.StoragePath, backend.get(uploadID).objectKey)
	}
	// Uploads finish out of order, so it is the set that is asserted here;
	// encryption is sequential by construction, so its order is asserted below.
	sent := slices.Clone(second.uploaded)
	slices.Sort(sent)
	if want := []int64{2, 3, 4}; !slices.Equal(sent, want) {
		t.Errorf("the resumed attempt uploaded parts %v, want exactly %v", sent, want)
	}
	if !slices.Equal(second.encrypted, []int64{2, 3, 4}) {
		t.Errorf("the resumed attempt encrypted parts %v, want it to re-encrypt only what it sends, from the boundary on", second.encrypted)
	}

	got := backend.object(t, uploadID)
	want := uninterruptedCiphertext(t, data, result.EncryptionKey, result.IV, partSize)
	if !bytes.Equal(got, want) {
		t.Errorf("the object assembled from a resumed upload is %d bytes and differs from the %d an uninterrupted upload would have written",
			len(got), len(want))
	}

	if saved := loadStreamingState(t, localPath); saved != nil {
		t.Errorf("the checkpoint outlived the completed upload: %+v", saved)
	}
}

// TestUploadStreamingCheckpointsOnlyTheContiguousPrefix covers what makes a
// checkpoint a resume point. Parts finish out of order under concurrency, so the
// set of completed parts is not somewhere an upload can restart from: the chain
// IV names one boundary, and the source is re-read from there.
func TestUploadStreamingCheckpointsOnlyTheContiguousPrefix(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	uploader := newResumableStreamingUploader(backend, partSize)
	// Exactly part 1 fails, and only once parts 0, 2 and 3 have landed: the
	// backend ends up holding three parts around a gap, of which only part 0 can
	// be resumed from. Failing part 1 alone is what makes this test load-bearing
	// — an implementation that checkpointed every completed part would record
	// parts 0, 2 and 3 here, while one that records the prefix records part 0.
	uploader.holdPart = 1
	uploader.holdUntil = 3
	uploader.failPart = 1

	// No transfer handle: the pipeline then runs its default four workers, which
	// is what lets parts 2 and 3 finish while part 1 is still in flight.
	params := UploadParams{LocalPath: localPath}
	if _, err := uploadStreaming(context.Background(), uploader, params, int64(len(data))); err == nil {
		t.Fatal("expected the attempt to fail")
	}

	landed := slices.Clone(uploader.uploaded)
	slices.Sort(landed)
	if want := []int64{0, 2, 3}; !slices.Equal(landed, want) {
		t.Fatalf("the interrupted attempt landed parts %v, want %v — the gap has to be part 1 alone", landed, want)
	}

	saved := loadStreamingState(t, localPath)
	if saved == nil {
		t.Fatal("nothing was checkpointed")
	}
	if len(saved.StreamingParts) != 1 || saved.StreamingParts[0].PartIndex != 0 {
		t.Fatalf("checkpointed %+v, want only part 0 — parts 2 and 3 completed but part 1 did not",
			saved.StreamingParts)
	}

	// The resumed attempt re-sends everything from the gap on, so the object it
	// assembles has to be the one an uninterrupted upload would have written —
	// the parts left over from the first attempt are re-encrypted from the
	// checkpoint's chain position, not kept.
	second := newResumableStreamingUploader(backend, partSize)
	result, err := uploadStreaming(context.Background(), second, params, int64(len(data)))
	if err != nil {
		t.Fatalf("the resumed attempt failed: %v", err)
	}
	if !slices.Equal(second.encrypted, []int64{1, 2, 3}) {
		t.Errorf("the resumed attempt encrypted parts %v, want it to restart from the gap at part 1", second.encrypted)
	}

	got := backend.object(t, uploader.uploadID)
	want := uninterruptedCiphertext(t, data, result.EncryptionKey, result.IV, partSize)
	if !bytes.Equal(got, want) {
		t.Errorf("the object assembled after resuming across a gap is %d bytes and differs from the %d an uninterrupted upload would have written",
			len(got), len(want))
	}
}

// TestUploadStreamingAbandonsStateWhenSourceChanged: the parts on the backend
// were cut from the file as it was, so once it changes they can only assemble an
// object that is not the file being registered.
func TestUploadStreamingAbandonsStateWhenSourceChanged(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	first := newResumableStreamingUploader(backend, partSize)
	first.failFrom = 2
	params := UploadParams{LocalPath: localPath}
	if _, err := uploadStreaming(context.Background(), first, params, int64(len(data))); err == nil {
		t.Fatal("expected the interrupted attempt to fail")
	}
	interrupted := first.uploadID

	// Same size, later modification time: a stat is all this path has to go on.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(localPath, later, later); err != nil {
		t.Fatalf("failed to touch the source: %v", err)
	}

	second := newResumableStreamingUploader(backend, partSize)
	var out bytes.Buffer
	params.OutputWriter = &out
	if _, err := uploadStreaming(context.Background(), second, params, int64(len(data))); err != nil {
		t.Fatalf("the fresh attempt failed: %v", err)
	}

	if second.uploadID == interrupted {
		t.Error("the fresh attempt continued the upload of the file as it was before it changed")
	}
	if len(second.uploaded) != 4 {
		t.Errorf("the fresh attempt sent %d parts, want all 4 of them", len(second.uploaded))
	}
	if !backend.get(interrupted).aborted {
		t.Error("the abandoned upload was left open on the backend")
	}
	if !strings.Contains(out.String(), "has been modified") {
		t.Errorf("output did not say why the upload started over: %q", out.String())
	}
}

// TestUploadStreamingAbandonsStateWithoutChainIV: the key and the initial IV
// place an encryptor at part 0, so without the chain position at the checkpoint
// boundary a resumed attempt would re-encrypt into different bytes than the
// parts the backend already holds.
func TestUploadStreamingAbandonsStateWithoutChainIV(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	first := newResumableStreamingUploader(backend, partSize)
	first.failFrom = 2
	params := UploadParams{LocalPath: localPath}
	if _, err := uploadStreaming(context.Background(), first, params, int64(len(data))); err == nil {
		t.Fatal("expected the interrupted attempt to fail")
	}
	interrupted := first.uploadID

	saved := loadStreamingState(t, localPath)
	if saved == nil {
		t.Fatal("the interrupted attempt recorded nothing to resume from")
	}
	saved.ChainIV = ""
	if err := state.SaveUploadState(saved, localPath); err != nil {
		t.Fatalf("failed to rewrite the state: %v", err)
	}

	second := newResumableStreamingUploader(backend, partSize)
	var out bytes.Buffer
	params.OutputWriter = &out
	if _, err := uploadStreaming(context.Background(), second, params, int64(len(data))); err != nil {
		t.Fatalf("the fresh attempt failed: %v", err)
	}

	if second.uploadID == interrupted {
		t.Fatal("continued an upload whose state does not say where the encryption chain stood")
	}
	if !strings.Contains(out.String(), "encryption chain") {
		t.Errorf("output did not say why the upload started over: %q", out.String())
	}
}

// TestUploadStreamingDiscardsAnUploadNothingCanReturnTo: an attempt whose very
// first parts fail has checkpointed nothing, so no retry could ever find those
// parts again — they are aborted rather than left to the backend's expiry.
func TestUploadStreamingDiscardsAnUploadNothingCanReturnTo(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 3*partSize)
	backend := newFakeStreamingBackend()

	uploader := newResumableStreamingUploader(backend, partSize)
	uploader.failFrom = 0 // nothing lands, so nothing is checkpointed

	params := UploadParams{LocalPath: localPath}
	if _, err := uploadStreaming(context.Background(), uploader, params, int64(len(data))); err == nil {
		t.Fatal("expected the attempt to fail")
	}

	if !backend.get(uploader.uploadID).aborted {
		t.Error("an upload with no checkpoint was left open on the backend")
	}
	if saved := loadStreamingState(t, localPath); saved != nil {
		t.Errorf("a state was left behind for an upload that was aborted: %+v", saved)
	}
}

// TestUploadStreamingKeepsACancelledUploadResumable: a cancelled attempt is
// precisely the one a retry comes back to, so a checkpointed one keeps both its
// checkpoint and its parts. Discarding them made every Ctrl-C throw away the
// whole transfer, and left the two upload modes disagreeing — a cancelled
// pre-encrypt attempt has always kept its state.
func TestUploadStreamingKeepsACancelledUploadResumable(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	first := newResumableStreamingUploader(backend, partSize)
	// The last part waits for a release only this test could give, so the
	// cancellation lands while the first three are already checkpointed.
	first.holdPart = 3
	first.holdUntil = math.MaxInt32

	params := UploadParams{LocalPath: localPath}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := uploadStreaming(ctx, first, params, int64(len(data)))
		done <- err
	}()
	waitForCheckpoint(t, localPath, 3)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected the cancelled upload to fail")
	}

	if backend.get(first.uploadID).aborted {
		t.Error("a cancelled upload was discarded on the backend, so its parts cannot be continued")
	}
	saved := loadStreamingState(t, localPath)
	if saved == nil {
		t.Fatal("a cancelled upload deleted the checkpoint a retry needs")
	}
	if len(saved.StreamingParts) != 3 {
		t.Errorf("the checkpoint holds %d parts, want the 3 that landed", len(saved.StreamingParts))
	}

	// What the kept state is for: the retry continues the same object.
	second := newResumableStreamingUploader(backend, partSize)
	result, err := uploadStreaming(context.Background(), second, params, int64(len(data)))
	if err != nil {
		t.Fatalf("the retry of a cancelled upload failed: %v", err)
	}
	if second.uploadID != first.uploadID {
		t.Fatalf("the retry started upload %q instead of continuing %q", second.uploadID, first.uploadID)
	}
	if len(second.uploaded) != 1 {
		t.Errorf("the retry sent %d parts, want only the one the cancellation stopped", len(second.uploaded))
	}
	object := backend.object(t, first.uploadID)
	want := uninterruptedCiphertext(t, data, result.EncryptionKey, result.IV, partSize)
	if !bytes.Equal(object, want) {
		t.Errorf("the resumed object is %d bytes and differs from the %d an uninterrupted upload writes", len(object), len(want))
	}
}

// TestUploadStreamingDiscardsACancelledUploadWithNoCheckpoint is the other half:
// parts that landed out of order never form a prefix, so nothing was
// checkpointed and nothing can come back for them.
func TestUploadStreamingDiscardsACancelledUploadWithNoCheckpoint(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	uploader := newResumableStreamingUploader(backend, partSize)
	// Part 0 never lands, so the parts that do are not a prefix of anything.
	uploader.holdPart = 0
	uploader.holdUntil = math.MaxInt32

	params := UploadParams{LocalPath: localPath}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := uploadStreaming(ctx, uploader, params, int64(len(data)))
		done <- err
	}()
	waitForParts(uploader, 3)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected the cancelled upload to fail")
	}

	if !backend.get(uploader.uploadID).aborted {
		t.Error("an upload nothing can return to was left open on the backend")
	}
	if saved := loadStreamingState(t, localPath); saved != nil {
		t.Errorf("a checkpoint was left behind for an upload that was aborted: %+v", saved)
	}
}

// waitForParts blocks until the uploader has accepted at least count parts.
func waitForParts(uploader *resumableStreamingUploader, count int) {
	for {
		uploader.mu.Lock()
		done := len(uploader.uploaded)
		uploader.mu.Unlock()
		if done >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// TestPreEncryptRetiresAnInterruptedStreamingUpload guards the interaction
// between the two upload modes now that both leave a resume state behind.
//
// The orchestrator retires the streaming state before the provider is ever
// handed it, so the provider cannot act on an object identity that is not its
// own — but that sidecar is the only record of the multipart upload the
// streaming attempt opened, so deleting it without retiring that upload stranded
// those parts until the backend's own seven-day expiry swept them. Both cross-
// mode directions now retire the backend upload before deleting its only record.
func TestPreEncryptRetiresAnInterruptedStreamingUpload(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	interrupted := newResumableStreamingUploader(backend, partSize)
	interrupted.failFrom = 2
	if _, err := uploadStreaming(context.Background(), interrupted, UploadParams{LocalPath: localPath}, int64(len(data))); err == nil {
		t.Fatal("expected the streaming attempt to fail")
	}
	if loadStreamingState(t, localPath) == nil {
		t.Fatal("the streaming attempt recorded nothing")
	}

	preEncrypt := &resumableFakeUploader{partSize: partSize, backend: backend}
	if _, err := uploadPreEncrypt(context.Background(), preEncrypt, UploadParams{LocalPath: localPath}, int64(len(data))); err != nil {
		t.Fatalf("the pre-encrypt upload failed: %v", err)
	}

	// The state the pre-encrypt provider could have acted on is gone by the time
	// it runs, and it started an object of its own instead.
	preEncrypt.mu.Lock()
	defer preEncrypt.mu.Unlock()
	if len(preEncrypt.attempts) != 1 {
		t.Fatalf("the provider ran %d times, want once", len(preEncrypt.attempts))
	}
	if preEncrypt.attempts[0].resumedFrom != 0 {
		t.Errorf("the pre-encrypt upload resumed %d parts of a streaming upload", preEncrypt.attempts[0].resumedFrom)
	}
	if upload := backend.get(interrupted.uploadID); !upload.aborted {
		t.Error("the streaming upload was orphaned: its only record was deleted without retiring it on the backend")
	}
}

// forget drops an upload from the backend, as its own expiry does after seven
// days: the state still names it, but there is nothing left to continue.
func (b *fakeStreamingBackend) forget(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.uploads, id)
}

// interruptOnce runs an attempt that stops after `landed` parts, leaving the
// backend holding that prefix and a checkpoint describing it.
func interruptOnce(t *testing.T, backend *fakeStreamingBackend, params UploadParams, data []byte, partSize int64, landed int64) string {
	t.Helper()
	attempt := newResumableStreamingUploader(backend, partSize)
	attempt.failFrom = landed
	if _, err := uploadStreaming(context.Background(), attempt, params, int64(len(data))); err == nil {
		t.Fatal("expected the interrupted attempt to fail")
	}
	if loadStreamingState(t, params.LocalPath) == nil {
		t.Fatal("the interrupted attempt recorded nothing to resume from")
	}
	return attempt.uploadID
}

// rewriteStreamingState damages one field of the checkpoint an interrupted
// attempt left behind, which is how a state that no run of this code would
// write reaches the resume path.
func rewriteStreamingState(t *testing.T, localPath string, damage func(*state.UploadResumeState)) {
	t.Helper()
	saved := loadStreamingState(t, localPath)
	if saved == nil {
		t.Fatal("there is no state to rewrite")
	}
	damage(saved)
	if err := state.SaveUploadState(saved, localPath); err != nil {
		t.Fatalf("failed to rewrite the state: %v", err)
	}
}

// waitForCheckpoint blocks until the state file describes at least count parts.
func waitForCheckpoint(t *testing.T, localPath string, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if saved, err := state.LoadUploadState(localPath); err == nil && saved != nil && len(saved.StreamingParts) >= count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no checkpoint covering %d parts appeared", count)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestUploadStreamingRefusesASecondUploadOfTheSameSource is N4. Once the
// streaming path resumes a checkpoint, that checkpoint names a multipart upload
// on the backend — so two invocations for one source fill the SAME object, and
// whichever stops first aborts the other's upload out from under it. Nothing
// excluded them: the lock the pre-encrypt providers take was never taken here.
func TestUploadStreamingRefusesASecondUploadOfTheSameSource(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	first := newResumableStreamingUploader(backend, partSize)
	// The last part waits for a release only this test gives, so the first
	// invocation is still holding the source when the second one arrives.
	first.holdPart = 3
	first.holdUntil = math.MaxInt32

	params := UploadParams{LocalPath: localPath}
	type outcome struct {
		result *cloud.UploadResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := uploadStreaming(context.Background(), first, params, int64(len(data)))
		done <- outcome{result: result, err: err}
	}()
	waitForCheckpoint(t, localPath, 3)

	// A second invocation of the same source, on a context its caller has
	// already given up on: the shape that used to abort the first's upload.
	second := newResumableStreamingUploader(backend, partSize)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := uploadStreaming(cancelled, second, params, int64(len(data)))
	if err == nil {
		t.Fatal("a second upload of a source another upload is holding was allowed to run")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("the second upload failed with %q, want a refusal from the upload lock", err)
	}
	if second.uploadID != "" {
		t.Errorf("the refused invocation opened or reopened upload %q", second.uploadID)
	}
	if backend.get(first.uploadID).aborted {
		t.Fatal("the second invocation aborted the multipart upload the first was still filling")
	}

	close(first.release)
	finished := <-done
	if finished.err != nil {
		t.Fatalf("the first upload failed: %v", finished.err)
	}
	object := backend.object(t, first.uploadID)
	want := uninterruptedCiphertext(t, data, finished.result.EncryptionKey, finished.result.IV, partSize)
	if !bytes.Equal(object, want) {
		t.Errorf("the object the first upload assembled is %d bytes, want the %d an uninterrupted upload writes",
			len(object), len(want))
	}
}

// TestUploadPreEncryptRefusesWhenTheSourceIsLocked is the other half of N4: the
// pre-encrypt path examines and deletes the artifacts of an interrupted attempt
// before the provider takes the lock, so a second invocation could retire the
// ciphertext and state of an upload that was still running.
func TestUploadPreEncryptRefusesWhenTheSourceIsLocked(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 3*partSize)

	// What the invocation that holds the lock has already recorded.
	encryptedPath := localPath + ".encrypted"
	if err := os.WriteFile(encryptedPath, []byte("ciphertext"), 0600); err != nil {
		t.Fatalf("failed to write the encrypted copy: %v", err)
	}
	if err := state.SaveUploadState(&state.UploadResumeState{
		LocalPath:     localPath,
		EncryptedPath: encryptedPath,
		ObjectKey:     "fake/path/held",
		OriginalSize:  int64(len(data)),
		TotalSize:     int64(len("ciphertext")),
		CreatedAt:     time.Now(),
		StorageType:   "FakeStorage",
	}, localPath); err != nil {
		t.Fatalf("failed to write the state: %v", err)
	}

	lock, err := state.AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("failed to take the lock the running upload would hold: %v", err)
	}
	defer state.ReleaseUploadLock(lock)

	provider := &resumableFakeUploader{partSize: partSize}
	_, err = uploadPreEncrypt(context.Background(), provider, UploadParams{LocalPath: localPath}, int64(len(data)))
	if err == nil {
		t.Fatal("a second pre-encrypt upload of a locked source was allowed to run")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("the second upload failed with %q, want a refusal from the upload lock", err)
	}

	if _, statErr := os.Stat(encryptedPath); statErr != nil {
		t.Error("the refused invocation deleted the encrypted copy of the upload that holds the lock")
	}
	if saved, _ := state.LoadUploadState(localPath); saved == nil {
		t.Error("the refused invocation deleted the state of the upload that holds the lock")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.attempts) != 0 {
		t.Errorf("the refused invocation reached the provider %d time(s)", len(provider.attempts))
	}
}

// TestUploadStreamingAbandonsAStateWithNoPartSize is N7. A state with no part
// size is rejected as unusable, and abandonment then rebuilt a provider handle
// from it — where the part count is the file size divided by that zero.
func TestUploadStreamingAbandonsAStateWithNoPartSize(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()
	params := UploadParams{LocalPath: localPath}

	interrupted := interruptOnce(t, backend, params, data, partSize, 2)
	rewriteStreamingState(t, localPath, func(saved *state.UploadResumeState) {
		saved.PartSize = 0
	})

	second := newResumableStreamingUploader(backend, partSize)
	if _, err := uploadStreaming(context.Background(), second, params, int64(len(data))); err != nil {
		t.Fatalf("the fresh attempt failed: %v", err)
	}
	if second.uploadID == interrupted {
		t.Fatal("continued an upload whose state does not say what part size it used")
	}
	if !backend.get(interrupted).aborted {
		t.Error("the abandoned upload was left open on the backend")
	}
}

// TestUploadStreamingAbandonsAStateWithAWrongLengthKey is the other half of N7:
// a key that is valid base64 but the wrong length passes every check the resume
// path makes, and fails only when the provider builds a cipher from it — after
// which the state is still there for the next attempt to fail on in the same way.
func TestUploadStreamingAbandonsAStateWithAWrongLengthKey(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()
	params := UploadParams{LocalPath: localPath}

	interrupted := interruptOnce(t, backend, params, data, partSize, 2)
	rewriteStreamingState(t, localPath, func(saved *state.UploadResumeState) {
		saved.MasterKey = encryption.EncodeBase64([]byte("sixteen bytes!!!"))
	})

	second := newResumableStreamingUploader(backend, partSize)
	if _, err := uploadStreaming(context.Background(), second, params, int64(len(data))); err != nil {
		t.Fatalf("the fresh attempt failed: %v", err)
	}
	if second.uploadID == interrupted {
		t.Fatal("continued an upload whose saved key cannot make a cipher")
	}
	if !backend.get(interrupted).aborted {
		t.Error("the abandoned upload was left open on the backend")
	}
	if saved := loadStreamingState(t, localPath); saved != nil {
		t.Errorf("the unusable state survived the attempt that rejected it: %+v", saved)
	}
}

// TestUploadStreamingStartsFreshWhenTheBackendLostTheUpload covers the resume
// against parts the backend no longer holds: the state is retired and a fresh
// object started once, rather than the same unusable resume being attempted
// again on every retry.
func TestUploadStreamingStartsFreshWhenTheBackendLostTheUpload(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()
	params := UploadParams{LocalPath: localPath}

	interrupted := interruptOnce(t, backend, params, data, partSize, 2)
	backend.forget(interrupted)

	second := newResumableStreamingUploader(backend, partSize)
	var out bytes.Buffer
	params.OutputWriter = &out
	result, err := uploadStreaming(context.Background(), second, params, int64(len(data)))
	if err != nil {
		t.Fatalf("the fresh attempt failed: %v", err)
	}
	if second.uploadID == interrupted {
		t.Fatal("continued an upload the backend no longer holds")
	}
	if len(second.uploaded) != 4 {
		t.Errorf("the fresh attempt sent %d parts, want all 4 of them", len(second.uploaded))
	}
	if !strings.Contains(out.String(), "no longer holds") {
		t.Errorf("output did not say why the upload started over: %q", out.String())
	}
	object := backend.object(t, second.uploadID)
	want := uninterruptedCiphertext(t, data, result.EncryptionKey, result.IV, partSize)
	if !bytes.Equal(object, want) {
		t.Errorf("the fresh object is %d bytes and differs from the %d an uninterrupted upload writes", len(object), len(want))
	}
}

// TestStreamingAbandonmentRetiresAPreEncryptMultipart is the reverse mode
// switch: a streaming upload of a file whose last attempt was pre-encrypt
// deletes that attempt's sidecar and ciphertext, which are the only record of
// the multipart upload it opened. Deleting them without retiring it strands
// those parts on the backend until its own expiry sweeps them.
func TestStreamingAbandonmentRetiresAPreEncryptMultipart(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 3*partSize)
	backend := newFakeStreamingBackend()

	stranded, upload := backend.create("", "fake/path/streamed.dat-preencrypt")
	encryptedPath := localPath + ".encrypted"
	if err := os.WriteFile(encryptedPath, []byte("ciphertext"), 0600); err != nil {
		t.Fatalf("failed to write the encrypted copy: %v", err)
	}
	if err := state.SaveUploadState(&state.UploadResumeState{
		LocalPath:     localPath,
		EncryptedPath: encryptedPath,
		ObjectKey:     upload.objectKey,
		UploadID:      stranded,
		OriginalSize:  int64(len(data)),
		TotalSize:     int64(len("ciphertext")),
		CreatedAt:     time.Now(),
		StorageType:   "FakeStorage",
		FormatVersion: 0,
	}, localPath); err != nil {
		t.Fatalf("failed to write the state: %v", err)
	}

	uploader := newResumableStreamingUploader(backend, partSize)
	if _, err := uploadStreaming(context.Background(), uploader, UploadParams{LocalPath: localPath}, int64(len(data))); err != nil {
		t.Fatalf("the streaming upload failed: %v", err)
	}

	if !backend.get(stranded).aborted {
		t.Error("the pre-encrypt upload was orphaned: its only record was deleted without retiring it on the backend")
	}
	if _, err := os.Stat(encryptedPath); !os.IsNotExist(err) {
		t.Error("the pre-encrypt ciphertext was left behind")
	}
}

// TestStreamingResumeBlockerHonoursTheSevenDayBoundary pins the expiry both
// backends enforce: a state a second inside seven days is still resumable, and
// one a second outside it describes parts that are no longer there.
func TestStreamingResumeBlockerHonoursTheSevenDayBoundary(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()
	params := UploadParams{LocalPath: localPath}

	interruptOnce(t, backend, params, data, partSize, 2)
	saved := loadStreamingState(t, localPath)
	sourceInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("failed to stat the source: %v", err)
	}

	saved.CreatedAt = time.Now().Add(-state.MaxResumeAge + time.Second)
	if reason := streamingResumeBlocker(saved, localPath, sourceInfo, "FakeStorage", uploadDestination{}, int64(len(data))); reason != "" {
		t.Errorf("a state a second inside the seven-day window was refused: %s", reason)
	}

	saved.CreatedAt = time.Now().Add(-state.MaxResumeAge - time.Second)
	if reason := streamingResumeBlocker(saved, localPath, sourceInfo, "FakeStorage", uploadDestination{}, int64(len(data))); !strings.Contains(reason, "expired") {
		t.Errorf("a state a second outside the seven-day window was accepted (reason %q)", reason)
	}
}

// TestUploadStreamingStartsFreshWhenTheSavedPartSizeDoesNotFit is N8 end to end:
// a checkpoint whose part size this machine cannot plan a pipeline for at all is
// retired instead of resumed. Running it anyway would hold several times the
// memory reserved for it, which on a machine already short of memory is the case
// the reservation exists to prevent.
func TestUploadStreamingStartsFreshWhenTheSavedPartSizeDoesNotFit(t *testing.T) {
	const partSize = 64
	const mib = 1024 * 1024
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	// A budget that leaves the plan holding less than the saved parts need.
	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads:   8,
		AutoScale:    true,
		CPUCores:     8,
		MemoryBudget: 160 * mib,
	})
	handle := internaltransfer.NewManager(resourceMgr).AllocateTransfer(int64(len(data)), 1)
	defer handle.Complete()

	params := UploadParams{LocalPath: localPath, TransferHandle: handle}
	interrupted := interruptOnce(t, backend, params, data, partSize, 2)
	rewriteStreamingState(t, localPath, func(saved *state.UploadResumeState) {
		saved.PartSize = 32 * mib
		saved.StreamingParts = saved.StreamingParts[:1]
	})

	second := newResumableStreamingUploader(backend, partSize)
	var out bytes.Buffer
	params.OutputWriter = &out
	if _, err := uploadStreaming(context.Background(), second, params, int64(len(data))); err != nil {
		t.Fatalf("the fresh attempt failed: %v", err)
	}

	if second.uploadID == interrupted {
		t.Fatal("continued an upload in parts this run has no memory to hold")
	}
	if !strings.Contains(out.String(), "not enough transfer memory") {
		t.Errorf("output did not say why the upload started over: %q", out.String())
	}
	if !backend.get(interrupted).aborted {
		t.Error("the abandoned upload was left open on the backend")
	}
	if got := resourceMgr.GetAvailableUploadMemory(); got != 160*mib {
		t.Errorf("available upload memory = %d after the upload, want the full %d", got, int64(160*mib))
	}
}

// TestUploadStreamingRunsWithoutALockOnAReadOnlySource: streaming uploads now
// create <source>.upload.lock beside the source, so a read-only or full source
// directory fails an upload that used to run. Such a directory cannot hold a
// resume state either, so there is no checkpoint two invocations could share and
// nothing the lock would have protected.
func TestUploadStreamingRunsWithoutALockOnAReadOnlySource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not stop file creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory regardless")
	}

	const partSize = 64
	dir := t.TempDir()
	data := make([]byte, 3*partSize)
	for i := range data {
		data[i] = byte(i*7 + 1)
	}
	localPath := filepath.Join(dir, "streamed.dat")
	if err := os.WriteFile(localPath, data, 0644); err != nil {
		t.Fatalf("failed to write the source: %v", err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("failed to make the source directory read-only: %v", err)
	}
	// Before TempDir's own cleanup, which cannot remove the file otherwise.
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	backend := newFakeStreamingBackend()
	uploader := newResumableStreamingUploader(backend, partSize)
	result, err := uploadStreaming(context.Background(), uploader, UploadParams{LocalPath: localPath}, int64(len(data)))
	if err != nil {
		t.Fatalf("an upload whose source directory cannot hold a lock file failed: %v", err)
	}

	object := backend.object(t, uploader.uploadID)
	want := uninterruptedCiphertext(t, data, result.EncryptionKey, result.IV, partSize)
	if !bytes.Equal(object, want) {
		t.Errorf("the object is %d bytes and differs from the %d an uninterrupted upload writes", len(object), len(want))
	}
}

// unlockableSource lays out a source that can be uploaded, and returns the path
// the caller uploads along with two seals: the first takes the write permission
// off the directory the upload lock would live in, the second off the directory
// the resume state lives in. Sealing is separate so a checkpoint can be left
// behind first.
//
// A symlinked source separates the two directories: the lock keys on the
// canonical path and the sidecar on the caller's spelling. Sealing the lock
// directory leaves the checkpoint writable while the lock cannot be created —
// which is what makes "the unlocked attempt does not touch the checkpoint"
// observable rather than merely impossible. Sealing the state directory is the
// inverse: the lock is taken as usual, and the checkpoint beside the source can
// be neither rewritten nor deleted. Without the symlink the two directories are
// one and both seals are the same.
func unlockableSource(t *testing.T, data []byte, viaSymlink bool) (string, func(), func()) {
	t.Helper()
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.Mkdir(sourceDir, 0700); err != nil {
		t.Fatalf("failed to create the source directory: %v", err)
	}
	sourcePath := filepath.Join(sourceDir, "unlockable.dat")
	if err := os.WriteFile(sourcePath, data, 0644); err != nil {
		t.Fatalf("failed to write the source: %v", err)
	}

	localPath, stateDir := sourcePath, sourceDir
	if viaSymlink {
		linkDir := filepath.Join(root, "links")
		if err := os.Mkdir(linkDir, 0700); err != nil {
			t.Fatalf("failed to create the link directory: %v", err)
		}
		localPath = filepath.Join(linkDir, "unlockable.dat")
		if err := os.Symlink(sourcePath, localPath); err != nil {
			t.Fatalf("failed to link the source: %v", err)
		}
		stateDir = linkDir
	}

	seal := func(dir string) func() {
		return func() {
			if err := os.Chmod(dir, 0500); err != nil {
				t.Fatalf("failed to make %s read-only: %v", dir, err)
			}
			// Before TempDir's own cleanup, which cannot remove the file otherwise.
			t.Cleanup(func() { os.Chmod(dir, 0700) })
		}
	}
	return localPath, seal(sourceDir), seal(stateDir)
}

// TestUploadStreamingIsStatelessWithoutALock is D2 on the streaming side. The
// lock could not be created, so nothing excludes a second invocation — but the
// checkpoint beside the source is readable, and a directory can become
// read-only AFTER one was written. Continuing it means two invocations filling
// one backend upload; rewriting or deleting it means one destroying the other's
// only record.
func TestUploadStreamingIsStatelessWithoutALock(t *testing.T) {
	for _, tt := range []struct {
		name       string
		viaSymlink bool
	}{
		{name: "a source directory that became read-only"},
		{name: "a symlinked source whose lock directory is read-only", viaSymlink: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("directory permissions do not stop file creation on Windows")
			}
			if os.Geteuid() == 0 {
				t.Skip("root writes into a read-only directory regardless")
			}

			const partSize = 64
			data := make([]byte, 4*partSize)
			for i := range data {
				data[i] = byte(i*7 + 1)
			}
			localPath, seal, _ := unlockableSource(t, data, tt.viaSymlink)

			backend := newFakeStreamingBackend()
			interrupted := interruptOnce(t, backend, UploadParams{LocalPath: localPath}, data, partSize, 2)
			checkpoint, err := os.ReadFile(localPath + ".upload.resume")
			if err != nil {
				t.Fatalf("the interrupted attempt left no checkpoint: %v", err)
			}
			seal()

			second := newResumableStreamingUploader(backend, partSize)
			result, err := uploadStreaming(context.Background(), second, UploadParams{LocalPath: localPath}, int64(len(data)))
			if err != nil {
				t.Fatalf("the unlocked attempt failed: %v", err)
			}

			if second.uploadID == interrupted {
				t.Error("the unlocked attempt continued a checkpoint it holds no lock on, so two invocations fill one backend upload")
			}
			if len(second.uploaded) != 4 {
				t.Errorf("the unlocked attempt sent %d parts, want all 4 of a fresh upload", len(second.uploaded))
			}
			if backend.get(interrupted).aborted {
				t.Error("the unlocked attempt retired an upload another invocation may still be filling")
			}
			after, err := os.ReadFile(localPath + ".upload.resume")
			if err != nil {
				t.Errorf("the unlocked attempt deleted a checkpoint it does not own: %v", err)
			} else if !bytes.Equal(after, checkpoint) {
				t.Error("the unlocked attempt rewrote a checkpoint it does not own")
			}

			object := backend.object(t, second.uploadID)
			want := uninterruptedCiphertext(t, data, result.EncryptionKey, result.IV, partSize)
			if !bytes.Equal(object, want) {
				t.Errorf("the object is %d bytes and differs from the %d an uninterrupted upload writes", len(object), len(want))
			}
		})
	}
}

// TestUploadPreEncryptIsStatelessWithoutALock is D2 on the pre-encrypt side,
// where the checkpoint lifecycle belongs to the provider: an unlocked attempt
// has to reach it as stateless too, or it reuses the object identity and
// encrypted copy of an attempt it cannot exclude.
func TestUploadPreEncryptIsStatelessWithoutALock(t *testing.T) {
	for _, tt := range []struct {
		name       string
		viaSymlink bool
	}{
		{name: "a source directory that became read-only"},
		{name: "a symlinked source whose lock directory is read-only", viaSymlink: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("directory permissions do not stop file creation on Windows")
			}
			if os.Geteuid() == 0 {
				t.Skip("root writes into a read-only directory regardless")
			}

			const partSize = 64
			data := make([]byte, 4*partSize)
			for i := range data {
				data[i] = byte(i*3 + 2)
			}
			localPath, seal, _ := unlockableSource(t, data, tt.viaSymlink)

			fake := &resumableFakeUploader{partSize: partSize, failAfterParts: 2}
			params := UploadParams{LocalPath: localPath, PreEncrypt: true}
			if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(data))); err == nil {
				t.Fatal("the interrupted attempt was expected to fail")
			}
			checkpoint, err := os.ReadFile(localPath + ".upload.resume")
			if err != nil {
				t.Fatalf("the interrupted attempt left no checkpoint: %v", err)
			}
			seal()

			fake.failAfterParts = 0
			if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(data))); err != nil {
				t.Fatalf("the unlocked attempt failed: %v", err)
			}

			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.attempts) != 2 {
				t.Fatalf("the provider ran %d times, want 2", len(fake.attempts))
			}
			first, second := fake.attempts[0], fake.attempts[1]

			if second.randomSuffix == first.randomSuffix {
				t.Error("the unlocked attempt filled the object of a checkpoint it holds no lock on")
			}
			if second.encryptedPath == first.encryptedPath {
				t.Error("the unlocked attempt reused the encrypted copy of an attempt it cannot exclude")
			}
			if second.resumedFrom != 0 {
				t.Errorf("the unlocked attempt resumed from %d parts of an upload it does not own", second.resumedFrom)
			}
			after, err := os.ReadFile(localPath + ".upload.resume")
			if err != nil {
				t.Errorf("the unlocked attempt deleted a checkpoint it does not own: %v", err)
			} else if !bytes.Equal(after, checkpoint) {
				t.Error("the unlocked attempt rewrote a checkpoint it does not own")
			}
		})
	}
}

// TestUploadPreEncryptKeepsARejectedCheckpointFromTheProvider is D4's remaining
// bypass. The wrapper holds the lock, judges the checkpoint — it was going to
// another destination — and declines to retire it through this one, but the
// directory the sidecar lives in is read-only and the record cannot be deleted.
// The provider then reloads that record and retires the other destination's
// upload through this one: an upload ID absent here answers as though it had
// been discarded, while the upload it named stays open.
//
// The permissions are the inverse of the stateless cases above: the canonical
// directory the lock lives in is writable, so the attempt is an ordinary locked
// one, and only the caller-spelled directory beside the symlink is sealed.
func TestUploadPreEncryptKeepsARejectedCheckpointFromTheProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not stop file creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory regardless")
	}

	const partSize = 64
	data := make([]byte, 4*partSize)
	for i := range data {
		data[i] = byte(i*5 + 3)
	}
	localPath, _, sealStateDir := unlockableSource(t, data, true)

	toA := UploadParams{
		LocalPath:        localPath,
		PreEncrypt:       true,
		StorageID:        "storage-A",
		StorageContainer: "bucket-a",
		StoragePathBase:  "uploads",
	}
	first := &resumableFakeUploader{partSize: partSize, failAfterParts: 2, storageID: "storage-A", storageContainer: "bucket-a"}
	if _, err := uploadPreEncrypt(context.Background(), first, toA, int64(len(data))); err == nil {
		t.Fatal("expected the interrupted attempt to fail")
	}
	stranded := loadStreamingState(t, localPath)
	if stranded == nil {
		t.Fatal("the interrupted attempt recorded nothing to resume from")
	}
	checkpoint, err := os.ReadFile(localPath + ".upload.resume")
	if err != nil {
		t.Fatalf("the interrupted attempt left no checkpoint: %v", err)
	}
	sealStateDir()

	var out bytes.Buffer
	toB := UploadParams{
		LocalPath:        localPath,
		PreEncrypt:       true,
		StorageID:        "storage-B",
		StorageContainer: "bucket-b",
		StoragePathBase:  "uploads",
		OutputWriter:     &out,
	}
	second := &resumableFakeUploader{partSize: partSize, storageID: "storage-B", storageContainer: "bucket-b"}
	if _, err := uploadPreEncrypt(context.Background(), second, toB, int64(len(data))); err != nil {
		t.Fatalf("the upload to the second destination failed: %v", err)
	}

	second.mu.Lock()
	defer second.mu.Unlock()
	if len(second.abortRequests) != 0 {
		t.Errorf("the second destination was asked to discard %v, which is the first destination's upload", second.abortRequests)
	}
	if len(second.attempts) != 1 {
		t.Fatalf("the second destination's provider ran %d times, want 1", len(second.attempts))
	}
	fresh := second.attempts[0]
	if fresh.objectKey == stranded.ObjectKey {
		t.Error("the upload to the second destination filled the first destination's object")
	}
	if fresh.resumedFrom != 0 {
		t.Errorf("the fresh attempt resumed from %d parts of another destination's upload", fresh.resumedFrom)
	}
	// Five parts, not four: CBC padding pushes the ciphertext of a file that
	// divides evenly into the part size one block past the last whole part.
	staged := 0
	for _, part := range second.staged {
		if part.objectKey == fresh.objectKey {
			staged++
		}
	}
	if staged != 5 {
		t.Errorf("the fresh attempt staged %d parts, want all 5 of the ciphertext", staged)
	}
	after, err := os.ReadFile(localPath + ".upload.resume")
	if err != nil {
		t.Errorf("the first destination's checkpoint was deleted: %v", err)
	} else if !bytes.Equal(after, checkpoint) {
		t.Error("the first destination's checkpoint was rewritten")
	}
	if !strings.Contains(out.String(), "different destination") {
		t.Errorf("output did not say why the upload started over: %q", out.String())
	}
	if !strings.Contains(out.String(), stranded.ObjectKey) {
		t.Errorf("output did not say that the interrupted upload was left where it was: %q", out.String())
	}
}

// TestUploadPreEncryptHoldsTheLockAcrossTheTransfer: the orchestrator used to
// hand the lock to the provider immediately before UploadEncryptedFile, because
// the lock is not re-entrant and both wanted it. Nothing excluded a second
// invocation in the window between the release and the provider's acquisition —
// and a second invocation is the one that retires this attempt's ciphertext and
// state.
func TestUploadPreEncryptHoldsTheLockAcrossTheTransfer(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 3*partSize)

	provider := &resumableFakeUploader{partSize: partSize}
	provider.duringUpload = func() {
		lock, err := state.AcquireUploadLock(localPath)
		if err == nil {
			state.ReleaseUploadLock(lock)
			t.Error("the source was not locked while the provider was uploading it")
		}
	}

	if _, err := uploadPreEncrypt(context.Background(), provider, UploadParams{LocalPath: localPath}, int64(len(data))); err != nil {
		t.Fatalf("the pre-encrypt upload failed: %v", err)
	}
}

// abortRanWithin reports how long the fake's last abort was given.
func (u *resumableStreamingUploader) abortRanWithin(t *testing.T) time.Duration {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.abortDeadline <= 0 {
		t.Fatal("the abort was issued on a context with no deadline")
	}
	return u.abortDeadline
}

// TestStreamingAbortsRunOnTheAbortDeadline is F-3. An abort issued after the
// caller's context was cancelled has to run detached from it, so its own timeout
// is how long the transfer goroutine stays alive after the user pressed cancel.
// It was the ten-minute part budget, which is a whole part transfer's worth of
// waiting for one request that will not answer.
func TestStreamingAbortsRunOnTheAbortDeadline(t *testing.T) {
	const partSize = 64

	t.Run("ending an upload nothing can return to", func(t *testing.T) {
		localPath, data := writeStreamingSource(t, 3*partSize)
		backend := newFakeStreamingBackend()

		uploader := newResumableStreamingUploader(backend, partSize)
		uploader.failFrom = 0 // nothing lands, so the attempt is discarded on the way out
		if _, err := uploadStreaming(context.Background(), uploader, UploadParams{LocalPath: localPath}, int64(len(data))); err == nil {
			t.Fatal("expected the attempt to fail")
		}

		if got := uploader.abortRanWithin(t); got > constants.AbortOperationTimeout {
			t.Errorf("the abort was given %s to run, want at most the %s abort deadline", got, constants.AbortOperationTimeout)
		}
	})

	t.Run("abandoning a state that cannot be resumed", func(t *testing.T) {
		localPath, data := writeStreamingSource(t, 4*partSize)
		backend := newFakeStreamingBackend()
		params := UploadParams{LocalPath: localPath}

		interruptOnce(t, backend, params, data, partSize, 2)
		rewriteStreamingState(t, localPath, func(saved *state.UploadResumeState) {
			saved.PartSize = 0
		})

		second := newResumableStreamingUploader(backend, partSize)
		if _, err := uploadStreaming(context.Background(), second, params, int64(len(data))); err != nil {
			t.Fatalf("the fresh attempt failed: %v", err)
		}

		if got := second.abortRanWithin(t); got > constants.AbortOperationTimeout {
			t.Errorf("the abort was given %s to run, want at most the %s abort deadline", got, constants.AbortOperationTimeout)
		}
	})
}

// TestUploadStreamingStartsFreshForADifferentDestination: the resume state and
// the upload lock key on the local path alone, so one source uploaded to two
// destinations meets on one sidecar. Continuing it hands the second destination
// the object key — and, on S3, the multipart upload ID — of the first, and
// registers whatever the second destination assembles under the first's path.
func TestUploadStreamingStartsFreshForADifferentDestination(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	toA := UploadParams{
		LocalPath:        localPath,
		StorageID:        "storage-A",
		StorageContainer: "bucket-a",
		StoragePathBase:  "fake/path",
	}
	first := newResumableStreamingUploader(backend, partSize)
	first.bucket = "bucket-a"
	first.failFrom = 2
	if _, err := uploadStreaming(context.Background(), first, toA, int64(len(data))); err == nil {
		t.Fatal("expected the interrupted attempt to fail")
	}
	interrupted := first.uploadID
	if loadStreamingState(t, localPath) == nil {
		t.Fatal("the interrupted attempt recorded nothing to resume from")
	}
	strandedKey := loadStreamingState(t, localPath).ObjectKey

	var out bytes.Buffer
	toB := UploadParams{
		LocalPath:        localPath,
		StorageID:        "storage-B",
		StorageContainer: "bucket-b",
		StoragePathBase:  "fake/path",
		OutputWriter:     &out,
	}
	second := newResumableStreamingUploader(backend, partSize)
	second.bucket = "bucket-b"
	result, err := uploadStreaming(context.Background(), second, toB, int64(len(data)))
	if err != nil {
		t.Fatalf("the upload to the second destination failed: %v", err)
	}

	if second.uploadID == interrupted {
		t.Fatal("continued an upload that was going to another destination")
	}
	if result.StoragePath == strandedKey {
		t.Error("the upload to the second destination was registered under the first destination's object key")
	}
	// D4: the abort this provider issues names ITS bucket, where the first
	// destination's upload ID is absent — an answer that reads as retirement
	// while that upload stays open, and its only local record is deleted.
	if len(second.abortRequests) != 0 {
		t.Errorf("the second destination was asked to discard %v, which is the first destination's upload", second.abortRequests)
	}
	if backend.get(interrupted).aborted {
		t.Error("the first destination's upload was discarded through the second destination's provider")
	}
	if !strings.Contains(out.String(), "different destination") {
		t.Errorf("output did not say why the upload started over: %q", out.String())
	}
	if !strings.Contains(out.String(), strandedKey) {
		t.Errorf("output did not say that the interrupted upload was left where it was: %q", out.String())
	}
	if len(second.uploaded) != 4 {
		t.Errorf("the fresh attempt sent %d parts, want all 4 of them", len(second.uploaded))
	}
	object := backend.object(t, second.uploadID)
	want := uninterruptedCiphertext(t, data, result.EncryptionKey, result.IV, partSize)
	if !bytes.Equal(object, want) {
		t.Errorf("the fresh object is %d bytes and differs from the %d an uninterrupted upload writes", len(object), len(want))
	}
}

// TestDestinationBlockerTellsTwoDestinationsApart covers the rule both resume
// blockers share. A state that records where it was going is judged on that; one
// written before v4.9.9 records nothing, and its object key is evidence only
// where this destination gives keys a prefix of its own.
func TestDestinationBlockerTellsTwoDestinationsApart(t *testing.T) {
	here := uploadDestination{storageID: "storage-A", container: "bucket-a", pathBase: "uploads"}

	for _, tt := range []struct {
		name    string
		saved   state.UploadResumeState
		dest    uploadDestination
		blocked bool
	}{
		{
			name:  "the same destination",
			saved: state.UploadResumeState{StorageID: "storage-A", Container: "bucket-a"},
			dest:  here,
		},
		{
			name:    "another storage record",
			saved:   state.UploadResumeState{StorageID: "storage-B", Container: "bucket-a"},
			dest:    here,
			blocked: true,
		},
		{
			name:    "another container",
			saved:   state.UploadResumeState{StorageID: "storage-A", Container: "bucket-b"},
			dest:    here,
			blocked: true,
		},
		{
			name:  "a shipped state whose key is under this path base",
			saved: state.UploadResumeState{ObjectKey: "uploads/source.dat-suffix"},
			dest:  here,
		},
		{
			name:    "a shipped state whose key is somewhere else",
			saved:   state.UploadResumeState{ObjectKey: "elsewhere/source.dat-suffix"},
			dest:    here,
			blocked: true,
		},
		{
			name:    "a shipped state and a destination with no path base to judge by",
			saved:   state.UploadResumeState{ObjectKey: "source.dat-suffix"},
			dest:    uploadDestination{storageID: "storage-A", container: "bucket-a"},
			blocked: true,
		},
		{
			name:  "a run that was not told where it is going",
			saved: state.UploadResumeState{StorageID: "storage-B", Container: "bucket-b"},
			dest:  uploadDestination{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reason := destinationBlocker(&tt.saved, tt.dest)
			if tt.blocked && reason == "" {
				t.Error("the state was accepted for a destination it does not describe")
			}
			if !tt.blocked && reason != "" {
				t.Errorf("the state was refused for its own destination: %s", reason)
			}
		})
	}
}

// TestPreEncryptResumeBlockerRefusesAnotherDestination: the pre-encrypt half of
// the same rule. The ciphertext beside the source is reusable, which is exactly
// what makes continuing it under another destination's object identity possible.
func TestPreEncryptResumeBlockerRefusesAnotherDestination(t *testing.T) {
	localPath, data := writeStreamingSource(t, 128)
	sourceInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("failed to stat the source: %v", err)
	}
	encryptedPath := localPath + ".encrypted"
	if err := os.WriteFile(encryptedPath, data, 0600); err != nil {
		t.Fatalf("failed to write the encrypted copy: %v", err)
	}

	saved := &state.UploadResumeState{
		LocalPath:     localPath,
		EncryptedPath: encryptedPath,
		ObjectKey:     "uploads/streamed.dat-suffix",
		OriginalSize:  int64(len(data)),
		TotalSize:     int64(len(data)),
		SourceModTime: sourceInfo.ModTime(),
		EncryptionKey: "key",
		IV:            "iv",
		RandomSuffix:  "suffix",
		CreatedAt:     time.Now(),
		StorageType:   "FakeStorage",
		StorageID:     "storage-A",
		Container:     "bucket-a",
	}
	here := uploadDestination{storageID: "storage-A", container: "bucket-a", pathBase: "uploads"}
	if reason := preEncryptResumeBlocker(saved, localPath, sourceInfo, "FakeStorage", here); reason != "" {
		t.Fatalf("a state for this destination was refused: %s", reason)
	}

	elsewhere := uploadDestination{storageID: "storage-B", container: "bucket-b", pathBase: "uploads"}
	if reason := preEncryptResumeBlocker(saved, localPath, sourceInfo, "FakeStorage", elsewhere); !strings.Contains(reason, "different destination") {
		t.Errorf("a state for another destination was accepted (reason %q)", reason)
	}
}

// TestUploadStreamingResumesUnderAReplannedPartSize is N8's follow-through. A
// resumed upload runs with the part size the interrupted attempt chained
// through, and fitting that size inside the reservation the fresh plan already
// made refuses far more resumes than the machine cannot afford: the reservation
// was sized for 16 MB parts, so 64 MB ones never fit it however wide the
// pipeline is squeezed. Planning again with the part size fixed reserves for the
// parts this attempt will actually hold, and only a machine that cannot hold the
// narrowest such pipeline starts over.
func TestUploadStreamingResumesUnderAReplannedPartSize(t *testing.T) {
	const partSize = 64
	const mib = 1024 * 1024
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	// Enough for the narrowest pipeline of 64 MB parts — (1 queued + 1 worker +
	// 4 transients) x 64 MB — and no more.
	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads:   8,
		AutoScale:    true,
		CPUCores:     8,
		MemoryBudget: 512 * mib,
	})
	handle := internaltransfer.NewManager(resourceMgr).AllocateTransfer(int64(len(data)), 1)
	defer handle.Complete()

	params := UploadParams{LocalPath: localPath, TransferHandle: handle}
	interrupted := interruptOnce(t, backend, params, data, partSize, 2)
	rewriteStreamingState(t, localPath, func(saved *state.UploadResumeState) {
		saved.PartSize = 64 * mib
		saved.StreamingParts = saved.StreamingParts[:1]
	})

	second := newResumableStreamingUploader(backend, partSize)
	var out bytes.Buffer
	params.OutputWriter = &out
	if _, err := uploadStreaming(context.Background(), second, params, int64(len(data))); err != nil {
		t.Fatalf("the resumed attempt failed: %v", err)
	}

	if second.uploadID != interrupted {
		t.Fatalf("started upload %q instead of continuing %q, which this machine has the memory for: %q",
			second.uploadID, interrupted, out.String())
	}
	if got := resourceMgr.GetAvailableUploadMemory(); got != 512*mib {
		t.Errorf("available upload memory = %d after the upload, want the full %d", got, int64(512*mib))
	}
}

// TestUploadPreEncryptPlansForTheSavedPartSize is the pre-encrypt twin of the
// re-plan. The providers resume against the part size the checkpoint records, so
// a plan made for the size this run would have chosen sizes the workers, the
// queue and the memory reservation for parts the pipeline never holds.
func TestUploadPreEncryptPlansForTheSavedPartSize(t *testing.T) {
	const mib = 1024 * 1024
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "replan.dat")
	plaintext := make([]byte, 300)
	if err := os.WriteFile(source, plaintext, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads:   8,
		AutoScale:    true,
		CPUCores:     8,
		MemoryBudget: 512 * mib,
	})
	handle := internaltransfer.NewManager(resourceMgr).AllocateTransfer(int64(len(plaintext)), 1)
	defer handle.Complete()

	fake := &resumableFakeUploader{partSize: 64, failAfterParts: 2}
	params := UploadParams{LocalPath: source, PreEncrypt: true, TransferHandle: handle}
	if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(plaintext))); err == nil {
		t.Fatal("first attempt was expected to fail")
	}

	fake.failAfterParts = 0
	if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(plaintext))); err != nil {
		t.Fatalf("second attempt failed: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	second := fake.attempts[1]
	if second.resumedFrom == 0 {
		t.Fatal("the second attempt did not resume, so there is no saved part size to plan for")
	}
	if second.planPartSize != fake.partSize {
		t.Errorf("the resumed attempt was planned for %d-byte parts, want the %d-byte parts the checkpoint records",
			second.planPartSize, fake.partSize)
	}
	if got := resourceMgr.GetAvailableUploadMemory(); got != 512*mib {
		t.Errorf("available upload memory = %d after the upload, want the full %d", got, int64(512*mib))
	}
}

// TestUploadPreEncryptRestartsWhenTheSavedPartSizeCannotBePlanned: a checkpoint
// whose part size this machine cannot plan for cannot be continued, so the
// backend upload it names is retired and the provider opens a fresh one — the
// ciphertext beside the source is still this object's and is reused.
func TestUploadPreEncryptRestartsWhenTheSavedPartSizeCannotBePlanned(t *testing.T) {
	const mib = 1024 * 1024
	tmpDir := t.TempDir()
	source := filepath.Join(tmpDir, "toobig.dat")
	plaintext := make([]byte, 300)
	if err := os.WriteFile(source, plaintext, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads:   8,
		AutoScale:    true,
		CPUCores:     8,
		MemoryBudget: 160 * mib,
	})
	handle := internaltransfer.NewManager(resourceMgr).AllocateTransfer(int64(len(plaintext)), 1)
	defer handle.Complete()

	fake := &resumableFakeUploader{partSize: 64, failAfterParts: 2}
	params := UploadParams{LocalPath: source, PreEncrypt: true, TransferHandle: handle}
	if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(plaintext))); err == nil {
		t.Fatal("first attempt was expected to fail")
	}
	// A part size no pipeline on this machine can hold: (1 + 1 + 4) x 64 MB is
	// well past the whole budget.
	rewriteStreamingState(t, source, func(saved *state.UploadResumeState) {
		saved.PartSize = 64 * mib
	})

	fake.failAfterParts = 0
	var out bytes.Buffer
	params.OutputWriter = &out
	if _, err := uploadPreEncrypt(context.Background(), fake, params, int64(len(plaintext))); err != nil {
		t.Fatalf("second attempt failed: %v", err)
	}

	if !strings.Contains(out.String(), "Restarting the upload") {
		t.Errorf("output did not say the interrupted upload was given up on: %q", out.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	second := fake.attempts[1]
	if second.resumedFrom != 0 {
		t.Errorf("the second attempt resumed %d parts it had no memory to hold", second.resumedFrom)
	}
	if second.encryptedPath != fake.attempts[0].encryptedPath {
		t.Error("the ciphertext of the interrupted attempt was discarded and made again")
	}
	if got := resourceMgr.GetAvailableUploadMemory(); got != 160*mib {
		t.Errorf("available upload memory = %d after the upload, want the full %d", got, int64(160*mib))
	}
}

// TestUploadStreamingPlansAfreshWhenTheBackendLostTheUpload: the plan made for a
// resume is made for the interrupted upload's part size. When that upload turns
// out to be gone, what follows is a new object — and planning it with the dead
// one's geometry would stamp a part size into its metadata that this run never
// chose, and hold the memory that size needs for the whole transfer.
func TestUploadStreamingPlansAfreshWhenTheBackendLostTheUpload(t *testing.T) {
	const partSize = 64
	const mib = 1024 * 1024
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads:   8,
		AutoScale:    true,
		CPUCores:     8,
		MemoryBudget: 512 * mib,
	})
	handle := internaltransfer.NewManager(resourceMgr).AllocateTransfer(int64(len(data)), 1)
	defer handle.Complete()

	params := UploadParams{LocalPath: localPath, TransferHandle: handle}
	interrupted := interruptOnce(t, backend, params, data, partSize, 2)
	rewriteStreamingState(t, localPath, func(saved *state.UploadResumeState) {
		saved.PartSize = 64 * mib
		saved.StreamingParts = saved.StreamingParts[:1]
	})
	backend.forget(interrupted)

	second := newResumableStreamingUploader(backend, partSize)
	if _, err := uploadStreaming(context.Background(), second, params, int64(len(data))); err != nil {
		t.Fatalf("the fresh attempt failed: %v", err)
	}

	if second.initPartSize == 64*mib {
		t.Error("the fresh object was opened with the part size of the upload the backend had already dropped")
	}
	if second.initPartSize != constants.MinChunkSize {
		t.Errorf("the fresh object was opened with %d-byte parts, want the %d this run plans for",
			second.initPartSize, int64(constants.MinChunkSize))
	}
	if got := resourceMgr.GetAvailableUploadMemory(); got != 512*mib {
		t.Errorf("available upload memory = %d after the upload, want the full %d", got, int64(512*mib))
	}
}
