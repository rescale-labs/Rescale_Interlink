package upload

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
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

// CloudTransfer base interface methods
func (f *fakeStreamingUploader) Upload(_ context.Context, _ cloud.UploadParams) (*cloud.UploadResult, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeStreamingUploader) Download(_ context.Context, _ cloud.DownloadParams) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeStreamingUploader) RefreshCredentials(_ context.Context) error {
	return nil
}

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

func (f *fakeStreamingUploader) UploadStreamingPart(_ context.Context, _ *transfer.StreamingUpload, _ int64, _ []byte) (*transfer.PartResult, error) {
	return nil, fmt.Errorf("not implemented - use EncryptStreamingPart + UploadCiphertext")
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

func (f *fakePreEncryptUploader) Upload(_ context.Context, _ cloud.UploadParams) (*cloud.UploadResult, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakePreEncryptUploader) Download(_ context.Context, _ cloud.DownloadParams) error {
	return fmt.Errorf("not implemented")
}

func (f *fakePreEncryptUploader) RefreshCredentials(_ context.Context) error { return nil }

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
