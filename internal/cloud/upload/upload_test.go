package upload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
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

func (f *resumableFakeUploader) UploadEncryptedFile(_ context.Context, params transfer.EncryptedFileUploadParams) (*cloud.UploadResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	objectKey := state.BuildObjectKey("uploads", filepath.Base(params.LocalPath), params.RandomSuffix)

	encrypted, err := os.ReadFile(params.EncryptedPath)
	if err != nil {
		return nil, fmt.Errorf("fake provider: %w", err)
	}

	var completed []state.CompletedPart
	var uploadedBytes int64
	if saved, _ := state.LoadUploadState(params.LocalPath); saved != nil && saved.ObjectKey == objectKey {
		if err := state.ValidateUploadState(saved, params.LocalPath); err == nil {
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

		if err := state.SaveUploadState(&state.UploadResumeState{
			LocalPath:      params.LocalPath,
			EncryptedPath:  params.EncryptedPath,
			ObjectKey:      objectKey,
			UploadID:       "fake-upload-id",
			TotalSize:      int64(len(encrypted)),
			OriginalSize:   params.OriginalSize,
			SourceModTime:  params.SourceModTime,
			UploadedBytes:  uploadedBytes,
			CompletedParts: completed,
			EncryptionKey:  encryption.EncodeBase64(params.EncryptionKey),
			IV:             encryption.EncodeBase64(params.IV),
			RandomSuffix:   params.RandomSuffix,
			CreatedAt:      time.Now(),
			LastUpdate:     time.Now(),
			StorageType:    "FakeStorage",
		}, params.LocalPath); err != nil {
			return nil, err
		}

		if f.failAfterParts > 0 && stagedThisAttempt >= f.failAfterParts {
			f.attempts = append(f.attempts, attempt)
			return nil, errors.New("fake provider: connection reset")
		}
	}

	f.attempts = append(f.attempts, attempt)
	state.DeleteUploadState(params.LocalPath)
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
	objectKey string
	parts     map[int64][]byte // ciphertext, by part index
	handles   map[int64]string // what the backend named each part
	committed []string         // the handles the commit was asked to assemble
	aborted   bool
}

func newFakeStreamingBackend() *fakeStreamingBackend {
	return &fakeStreamingBackend{uploads: make(map[string]*fakeBackendUpload)}
}

func (b *fakeStreamingBackend) create(objectKey string) (string, *fakeBackendUpload) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	id := fmt.Sprintf("upload-%d", b.next)
	upload := &fakeBackendUpload{
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

	// failFrom interrupts the attempt at a known boundary: every part from this
	// index upwards fails, and waits for the parts below it to land first, so
	// the backend is left holding exactly that prefix however the workers
	// happened to interleave. -1 leaves every part alone.
	failFrom int64

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
}

func newResumableStreamingUploader(backend *fakeStreamingBackend, partSize int64) *resumableStreamingUploader {
	return &resumableStreamingUploader{
		backend:  backend,
		partSize: partSize,
		failFrom: -1,
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
	uploadID, _ := u.backend.create("fake/path/" + filepath.Base(params.LocalPath) + "-" + suffix)

	u.mu.Lock()
	u.uploadID = uploadID
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
	return upload != nil && !upload.aborted, nil
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

func (u *resumableStreamingUploader) AbortStreamingUpload(_ context.Context, uploadState *transfer.StreamingUpload) error {
	u.backend.mu.Lock()
	defer u.backend.mu.Unlock()
	if upload := u.backend.uploads[uploadState.UploadID]; upload != nil {
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
	// Part 1 waits for parts 0, 2 and 3, then fails: the backend ends up holding
	// parts 0, 2 and 3, of which only part 0 can be resumed from.
	uploader.holdPart = 1
	uploader.holdUntil = 3
	uploader.failFrom = 1

	// No transfer handle: the pipeline then runs its default four workers, which
	// is what lets parts 2 and 3 finish while part 1 is still in flight.
	params := UploadParams{LocalPath: localPath}
	if _, err := uploadStreaming(context.Background(), uploader, params, int64(len(data))); err == nil {
		t.Fatal("expected the attempt to fail")
	}

	saved := loadStreamingState(t, localPath)
	if saved == nil {
		t.Fatal("nothing was checkpointed")
	}
	if len(saved.StreamingParts) != 1 || saved.StreamingParts[0].PartIndex != 0 {
		t.Fatalf("checkpointed %+v, want only part 0 — parts 2 and 3 completed but part 1 did not",
			saved.StreamingParts)
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

// TestUploadStreamingAbortsOnCancellation: nobody is coming back for an upload
// the caller stopped, so its parts are discarded rather than left on the backend.
func TestUploadStreamingAbortsOnCancellation(t *testing.T) {
	const partSize = 64
	localPath, data := writeStreamingSource(t, 4*partSize)
	backend := newFakeStreamingBackend()

	uploader := newResumableStreamingUploader(backend, partSize)
	// Part 2 is held until the other three finish, which never happens: the
	// cancellation below lands while it waits.
	uploader.holdPart = 2
	uploader.holdUntil = 4

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		waitForParts(uploader, 2)
		cancel()
	}()

	params := UploadParams{LocalPath: localPath}
	_, err := uploadStreaming(ctx, uploader, params, int64(len(data)))
	if err == nil {
		t.Fatal("expected the cancelled upload to fail")
	}

	if !backend.get(uploader.uploadID).aborted {
		t.Error("a cancelled upload was left open on the backend")
	}
	if saved := loadStreamingState(t, localPath); saved != nil {
		t.Errorf("a cancelled upload left a checkpoint behind: %+v", saved)
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

// TestPreEncryptDoesNotAbortAResumableStreamingUpload guards the interaction
// between the two upload modes now that both leave a resume state behind.
//
// The pre-encrypt provider aborts the multipart upload a state names when that
// state describes a different object than the one it is about to fill — which is
// every streaming state, since the two modes never share an object key. What
// stops it reaching a streaming upload is that the orchestrator retires the
// state before the provider is ever handed it. If that ordering changed, running
// a pre-encrypt upload of a file would silently destroy the interrupted
// streaming upload of the same file.
func TestPreEncryptDoesNotAbortAResumableStreamingUpload(t *testing.T) {
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

	preEncrypt := &resumableFakeUploader{partSize: partSize}
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
	if upload := backend.get(interrupted.uploadID); upload.aborted {
		t.Error("a pre-encrypt upload aborted the streaming upload of the same file")
	}
}
