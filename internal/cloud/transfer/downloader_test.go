// Package transfer provides unified upload and download orchestration.
// This file contains tests for the downloader.
package transfer

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/models"
)

// waitFor blocks until want reports true, failing the test if it never does.
// Used where the assertion is about what a running download has NOT done yet,
// so the test has to let it get as far as it will before looking.
func waitFor(t *testing.T, what string, want func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if want() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// mockCloudTransferDownload is a bare CloudTransfer: the capability interfaces
// the mocks below add are what the downloader actually reaches for.
type mockCloudTransferDownload struct{}

func (m *mockCloudTransferDownload) StorageType() string {
	return "S3Storage"
}

// mockStreamingDownloader extends mockCloudTransferDownload with format detection.
type mockStreamingDownloader struct {
	mockCloudTransferDownload
	formatVersion           int
	fileID                  string
	partSize                int64
	iv                      []byte // nil for every shipped case: v0 derives its IV from the file header
	streamingDownloadCalled bool
}

func (m *mockStreamingDownloader) DetectFormat(ctx context.Context, remotePath string) (int, string, int64, []byte, error) {
	return m.formatVersion, m.fileID, m.partSize, m.iv, nil
}

func (m *mockStreamingDownloader) DownloadStreaming(ctx context.Context, remotePath, localPath string, masterKey []byte, progressCallback cloud.ProgressCallback) error {
	m.streamingDownloadCalled = true
	return nil
}

// mockLegacyDownloader implements LegacyDownloader, writing whatever ciphertext
// it was given to the .encrypted path the orchestrator asked it to fill.
type mockLegacyDownloader struct {
	mockStreamingDownloader
	ciphertext              []byte
	downloadErr             error
	downloadEncryptedCalled bool
}

func (m *mockLegacyDownloader) DownloadEncryptedFile(ctx context.Context, params LegacyDownloadParams) error {
	m.downloadEncryptedCalled = true
	if err := os.WriteFile(params.EncryptedPath, m.ciphertext, 0644); err != nil {
		return err
	}
	// downloadErr stands in for a transfer that got some of the object onto
	// disk and then lost the connection: the bytes it did fetch are still
	// there, which is the whole point of a resumable download.
	return m.downloadErr
}

// TestNewDownloader tests downloader creation.
func TestNewDownloader(t *testing.T) {
	mock := &mockCloudTransferDownload{}
	downloader := NewDownloader(mock)

	if downloader == nil {
		t.Fatal("expected non-nil downloader")
	}
	if downloader.provider != mock {
		t.Error("expected provider to be set correctly")
	}
}

// TestDownloaderDownloadValidation tests input validation.
func TestDownloaderDownloadValidation(t *testing.T) {
	mock := &mockCloudTransferDownload{}
	downloader := NewDownloader(mock)

	tests := []struct {
		name     string
		params   cloud.DownloadParams
		errorMsg string
	}{
		{
			name:     "empty remote path",
			params:   cloud.DownloadParams{},
			errorMsg: "remote path is required",
		},
		{
			name: "empty local path",
			params: cloud.DownloadParams{
				RemotePath: "/remote/file.txt",
			},
			errorMsg: "local path is required",
		},
		{
			name: "missing file info",
			params: cloud.DownloadParams{
				RemotePath: "/remote/file.txt",
				LocalPath:  "/local/file.txt",
			},
			errorMsg: "file info is required",
		},
		{
			name: "missing encryption key",
			params: cloud.DownloadParams{
				RemotePath: "/remote/file.txt",
				LocalPath:  "/local/file.txt",
				FileInfo:   &models.CloudFile{},
			},
			errorMsg: "encryption key is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := downloader.Download(context.Background(), tt.params)
			if err == nil {
				t.Errorf("expected error containing '%s', got nil", tt.errorMsg)
			}
		})
	}
}

// TestDownloaderLegacyFormat covers the v0 path end to end: detection reports
// version 0, downloadLegacy has the provider fill the .encrypted temp file, and
// the orchestrator decrypts it into place and returns the hash it computed while
// writing. CBC streaming produces the same ciphertext legacy does, so one
// encrypted part stands in for a v0 object.
func TestDownloaderLegacyFormat(t *testing.T) {
	plaintext := []byte("legacy v0 payload")

	enc, err := encryption.NewCBCStreamingEncryptor()
	if err != nil {
		t.Fatalf("NewCBCStreamingEncryptor: %v", err)
	}
	ciphertext, err := enc.EncryptPart(plaintext, true)
	if err != nil {
		t.Fatalf("EncryptPart: %v", err)
	}

	mock := &mockLegacyDownloader{ciphertext: ciphertext}
	mock.formatVersion = 0 // Legacy format
	localPath := filepath.Join(t.TempDir(), "file.txt")

	hash, err := NewDownloader(mock).Download(context.Background(), cloud.DownloadParams{
		RemotePath: "/remote/file.txt",
		LocalPath:  localPath,
		FileInfo: &models.CloudFile{
			EncodedEncryptionKey: base64.StdEncoding.EncodeToString(enc.GetKey()),
			IV:                   base64.StdEncoding.EncodeToString(enc.GetInitialIV()),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mock.downloadEncryptedCalled {
		t.Error("expected DownloadEncryptedFile to be called for legacy format")
	}

	got, readErr := os.ReadFile(localPath)
	if readErr != nil {
		t.Fatalf("read decrypted file: %v", readErr)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("decrypted %q, want %q", got, plaintext)
	}
	want := sha512.Sum512(plaintext)
	if !strings.EqualFold(hash, hex.EncodeToString(want[:])) {
		t.Errorf("computed hash = %q, want %q", hash, hex.EncodeToString(want[:]))
	}
}

// TestDownloaderStreamingFormat tests downloading with streaming format.
func TestDownloaderStreamingFormat(t *testing.T) {
	mock := &mockStreamingDownloader{
		formatVersion: 1,                                              // Streaming format
		fileID:        "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", // 32 bytes base64
		partSize:      64 * 1024 * 1024,                               // 64 MB
	}
	downloader := NewDownloader(mock)

	// Create valid params with encryption key (base64 encoded 32-byte key)
	params := cloud.DownloadParams{
		RemotePath: "/remote/file.txt",
		LocalPath:  "/local/file.txt",
		FileInfo: &models.CloudFile{
			EncodedEncryptionKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", // 32 bytes base64
			// No IV for streaming format
		},
	}

	_, err := downloader.Download(context.Background(), params)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// For streaming format, should call DownloadStreaming
	if !mock.streamingDownloadCalled {
		t.Error("expected DownloadStreaming to be called for streaming format")
	}
}

// TestEncryptedTempFileCleanupOnSuccess verifies that the .encrypted file cleanup
// pattern (retry with backoff) works correctly when the file exists.
func TestEncryptedTempFileCleanupOnSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test_file.dat")
	encryptedPath := localPath + ".encrypted"

	// Create a fake .encrypted file
	if err := os.WriteFile(encryptedPath, []byte("fake encrypted data"), 0644); err != nil {
		t.Fatalf("failed to create temp encrypted file: %v", err)
	}

	// Verify it exists
	if _, err := os.Stat(encryptedPath); os.IsNotExist(err) {
		t.Fatal("encrypted file should exist before cleanup")
	}

	// Simulate the cleanup logic from downloadLegacy defer
	var lastErr error
	for i := 0; i < 3; i++ {
		if lastErr = os.Remove(encryptedPath); lastErr == nil || os.IsNotExist(lastErr) {
			break
		}
	}

	// Verify cleanup succeeded
	if _, err := os.Stat(encryptedPath); !os.IsNotExist(err) {
		t.Errorf("encrypted file should be cleaned up, but still exists: %v", err)
	}
}

// TestEncryptedTempFileCleanupAlreadyRemoved verifies cleanup is a no-op
// when the .encrypted file doesn't exist (already cleaned by defer).
func TestEncryptedTempFileCleanupAlreadyRemoved(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test_file.dat")
	encryptedPath := localPath + ".encrypted"

	// Don't create the file - simulate it was already cleaned

	// Simulate the cleanup logic - should not error
	var lastErr error
	for i := 0; i < 3; i++ {
		if lastErr = os.Remove(encryptedPath); lastErr == nil || os.IsNotExist(lastErr) {
			lastErr = nil // os.IsNotExist is acceptable
			break
		}
	}

	if lastErr != nil {
		t.Errorf("cleanup should succeed (no-op) for non-existent file, got: %v", lastErr)
	}
}

// TestSafetyNetEncryptedCleanup verifies the safety-net cleanup in the download
// orchestrator (download.go) correctly removes leftover .encrypted files.
func TestSafetyNetEncryptedCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "output.dat")
	encryptedPath := localPath + ".encrypted"

	// Create the output file (simulating successful download)
	if err := os.WriteFile(localPath, []byte("decrypted content"), 0644); err != nil {
		t.Fatalf("failed to create output file: %v", err)
	}

	// Create a leftover .encrypted file (simulating failed defer cleanup)
	if err := os.WriteFile(encryptedPath, []byte("stale encrypted data"), 0644); err != nil {
		t.Fatalf("failed to create encrypted file: %v", err)
	}

	// Simulate the safety-net cleanup from download.go
	_ = os.Remove(localPath + ".encrypted")

	// Verify .encrypted file is removed
	if _, err := os.Stat(encryptedPath); !os.IsNotExist(err) {
		t.Error("safety-net cleanup should have removed .encrypted file")
	}

	// Verify the output file is untouched
	if _, err := os.Stat(localPath); os.IsNotExist(err) {
		t.Error("output file should not be removed by safety-net cleanup")
	}
}

// TestDownloadLegacyKeepsPartialCiphertextOnFailure is the regression for a
// resume that could never happen. The CLI guide promises that an interrupted v0
// download re-requests only the chunks it is missing, and the chunk driver does
// record them in a .download.resume sidecar next to the partial ciphertext — but
// downloadLegacy removed that ciphertext on every return, so the sidecar always
// described a file that was no longer on disk and every retry started from zero.
func TestDownloadLegacyKeepsPartialCiphertextOnFailure(t *testing.T) {
	partial := []byte("the first few chunks of the object")

	mock := &mockLegacyDownloader{ciphertext: partial, downloadErr: fmt.Errorf("connection reset")}
	mock.formatVersion = 0
	localPath := filepath.Join(t.TempDir(), "file.txt")

	_, err := NewDownloader(mock).Download(context.Background(), cloud.DownloadParams{
		RemotePath: "/remote/file.txt",
		LocalPath:  localPath,
		FileInfo: &models.CloudFile{
			EncodedEncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
			IV:                   base64.StdEncoding.EncodeToString(make([]byte, 16)),
		},
	})
	if err == nil {
		t.Fatal("expected the download to fail")
	}

	kept, readErr := os.ReadFile(localPath + ".encrypted")
	if readErr != nil {
		t.Fatalf("the partial ciphertext a retry resumes from is gone: %v", readErr)
	}
	if !bytes.Equal(kept, partial) {
		t.Errorf("kept %q, want the %d bytes the attempt had fetched", kept, len(partial))
	}
}

// TestDownloadLegacyDropsCiphertextThatCannotBeDecrypted covers the other half of
// the rule: ciphertext that is complete but will not decrypt with this key is not
// something a retry can make progress on, so keeping it would leave a file on
// disk that every later attempt re-reads and re-fails on.
func TestDownloadLegacyDropsCiphertextThatCannotBeDecrypted(t *testing.T) {
	// Not ciphertext at all: 17 bytes is not a whole number of AES blocks, so
	// decryption refuses it outright.
	mock := &mockLegacyDownloader{ciphertext: []byte("not a block long")}
	mock.formatVersion = 0
	localPath := filepath.Join(t.TempDir(), "file.txt")

	_, err := NewDownloader(mock).Download(context.Background(), cloud.DownloadParams{
		RemotePath: "/remote/file.txt",
		LocalPath:  localPath,
		FileInfo: &models.CloudFile{
			EncodedEncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
			IV:                   base64.StdEncoding.EncodeToString(make([]byte, 16)),
		},
	})
	if err == nil {
		t.Fatal("expected decryption to fail")
	}

	if _, statErr := os.Stat(localPath + ".encrypted"); !os.IsNotExist(statErr) {
		t.Errorf("ciphertext that cannot be decrypted was kept: %v", statErr)
	}
}

// TestDownloadLegacyDiskSpaceErrorStatesEnforcedRequirement verifies that when the
// legacy path refuses a download for lack of space, the error it returns states the
// requirement it actually enforced.
//
// The refusal used to be reported by a second, hand-built error that dropped the
// safety margin and measured the parent of the download directory. Both understated
// the gap, so a download refused on the margin printed "need N MB, have M MB
// available" with N below M — a refusal contradicting its own numbers.
func TestDownloadLegacyDiskSpaceErrorStatesEnforcedRequirement(t *testing.T) {
	tmpDir := t.TempDir()
	available := diskspace.GetAvailableSpace(tmpDir)
	if available == 0 {
		t.Skip("could not determine available space for temp dir")
	}

	// downloadLegacy holds the encrypted and decrypted copies at once, so it
	// enforces fileSize*2 plus a 15% margin. Size the file so free space lands
	// between the two: fileSize*2 fits, the margined requirement does not.
	fileSize := int64(float64(available) / 2.1)
	if fileSize*2 >= available {
		t.Fatalf("test setup: %d bytes should fit in %d available", fileSize*2, available)
	}
	wantRequired := int64(float64(fileSize*2) * 1.15)

	mock := &mockLegacyDownloader{}
	downloader := NewDownloader(mock)
	prep := &DownloadPrep{
		Params: cloud.DownloadParams{
			RemotePath: "/remote/big.dat",
			LocalPath:  filepath.Join(tmpDir, "big.dat"),
			FileInfo:   &models.CloudFile{DecryptedSize: fileSize},
		},
	}

	err := downloader.downloadLegacy(context.Background(), prep)
	if err == nil {
		t.Fatalf("expected refusal: %d bytes plus a 15%% margin exceeds %d available", fileSize*2, available)
	}
	spaceErr, ok := err.(*diskspace.InsufficientSpaceError)
	if !ok {
		t.Fatalf("expected *diskspace.InsufficientSpaceError, got %T: %v", err, err)
	}

	if spaceErr.RequiredBytes != wantRequired {
		t.Errorf("RequiredBytes = %d, want the margined requirement %d that was enforced",
			spaceErr.RequiredBytes, wantRequired)
	}
	if spaceErr.RequiredBytes <= spaceErr.AvailableBytes {
		t.Errorf("message claims need %d <= have %d, contradicting its own refusal: %v",
			spaceErr.RequiredBytes, spaceErr.AvailableBytes, spaceErr)
	}
	if mock.downloadEncryptedCalled {
		t.Error("provider must not be called once the space check has refused the download")
	}
}

// mockCBCPartDownloader serves one CBC-encrypted part, which makes it a
// StreamingPartDownloader so Download() takes the v2 path instead of falling
// back to legacy.
type mockCBCPartDownloader struct {
	mockStreamingDownloader
	ciphertext []byte
}

func (m *mockCBCPartDownloader) GetEncryptedSize(ctx context.Context, remotePath string) (int64, string, error) {
	return int64(len(m.ciphertext)), "", nil
}

func (m *mockCBCPartDownloader) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, version string, progressCallback func(int64)) ([]byte, error) {
	end := offset + length
	if end > int64(len(m.ciphertext)) {
		end = int64(len(m.ciphertext))
	}
	out := make([]byte, end-offset)
	copy(out, m.ciphertext[offset:end])
	if progressCallback != nil {
		progressCallback(int64(len(out)))
	}
	return out, nil
}

// TestDownloadCBCStreamingDefersChecksumToCaller covers the v2 format's half of
// the corrupt-download contract: downloadCBCStreaming hands the hash it computed
// during write back to the caller and does NOT fail on a checksum mismatch of
// its own accord. DownloadFile owns that comparison, because it also owns the
// strict-vs---skip-checksum decision and the quarantine of the corrupt file.
// A self-verification here would return before either could run, leaving the
// full-size corrupt file at LocalPath.
func TestDownloadCBCStreamingDefersChecksumToCaller(t *testing.T) {
	// Large enough that verifyDecryptionQuick takes its normal 32-byte probe path.
	plaintext := bytes.Repeat([]byte("interlink"), 512)

	enc, err := encryption.NewCBCStreamingEncryptor()
	if err != nil {
		t.Fatalf("NewCBCStreamingEncryptor: %v", err)
	}
	ciphertext, err := enc.EncryptPart(plaintext, true)
	if err != nil {
		t.Fatalf("EncryptPart: %v", err)
	}

	mock := &mockCBCPartDownloader{ciphertext: ciphertext}
	mock.formatVersion = 2
	mock.partSize = int64(len(ciphertext)) // single part

	localPath := filepath.Join(t.TempDir(), "results.dat")

	// FileChecksums deliberately disagrees with the bytes actually downloaded.
	hash, err := NewDownloader(mock).Download(context.Background(), cloud.DownloadParams{
		RemotePath: "user/abc/results.dat",
		LocalPath:  localPath,
		FileInfo: &models.CloudFile{
			EncodedEncryptionKey: base64.StdEncoding.EncodeToString(enc.GetKey()),
			IV:                   base64.StdEncoding.EncodeToString(enc.GetInitialIV()),
			DecryptedSize:        int64(len(plaintext)),
			FileChecksums: []models.FileChecksum{
				{HashFunction: "sha512", FileHash: strings.Repeat("0", 128)},
			},
		},
	})
	if err != nil {
		t.Fatalf("Download returned %v; the v2 path must report the mismatch through the returned hash, not an early error", err)
	}

	want := sha512.Sum512(plaintext)
	if !strings.EqualFold(hash, hex.EncodeToString(want[:])) {
		t.Errorf("computed hash = %q, want %q", hash, hex.EncodeToString(want[:]))
	}

	got, readErr := os.ReadFile(localPath)
	if readErr != nil {
		t.Fatalf("read downloaded file: %v", readErr)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("downloaded %d bytes, want %d", len(got), len(plaintext))
	}
}

// mockHKDFPartDownloader serves an HKDF (v1) object one encrypted part at a
// time, and can refuse one of them so the concurrent path fails partway.
type mockHKDFPartDownloader struct {
	mockStreamingDownloader
	ciphertext []byte
	failFrom   int64 // refuse any range at or after this offset; -1 serves everything
}

func (m *mockHKDFPartDownloader) GetEncryptedSize(ctx context.Context, remotePath string) (int64, string, error) {
	return int64(len(m.ciphertext)), "", nil
}

func (m *mockHKDFPartDownloader) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, version string, progressCallback func(int64)) ([]byte, error) {
	if m.failFrom >= 0 && offset >= m.failFrom {
		return nil, fmt.Errorf("range at %d: connection reset by peer", offset)
	}
	end := offset + length
	if end > int64(len(m.ciphertext)) {
		end = int64(len(m.ciphertext))
	}
	out := make([]byte, end-offset)
	copy(out, m.ciphertext[offset:end])
	return out, nil
}

// hkdfObject builds a multi-part HKDF object the concurrent path can take apart:
// the parts are exactly partSize of plaintext each, so the encrypted part size
// the downloader computes lines up with the concatenated ciphertext.
func hkdfObject(t *testing.T, plaintext []byte, partSize int64) (ciphertext []byte, masterKey, fileID []byte) {
	t.Helper()

	enc, err := encryption.NewStreamingEncryptor(partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptor: %v", err)
	}

	for i, off := int64(0), int64(0); off < int64(len(plaintext)); i, off = i+1, off+partSize {
		end := off + partSize
		if end > int64(len(plaintext)) {
			end = int64(len(plaintext))
		}
		part, err := enc.EncryptPart(i, plaintext[off:end])
		if err != nil {
			t.Fatalf("EncryptPart(%d): %v", i, err)
		}
		ciphertext = append(ciphertext, part...)
	}

	return ciphertext, enc.GetMasterKey(), enc.GetFileId()
}

// The concurrent HKDF path used to download straight into LocalPath, and its
// first act was to pre-allocate that file to the full part span. A part that
// failed left the whole allocation behind: a file at the destination, the size
// the platform says the file has, holed with zeros where the parts never
// arrived. The daemon accepts an existing file whose size matches DecryptedSize,
// so the next run adopted the holed file instead of fetching it again. Writing
// to a .partial alongside it and renaming only once the download's own checks
// pass means a failure leaves nothing at the destination to adopt.
func TestDownloadStreamingConcurrentLeavesNothingBehindOnFailure(t *testing.T) {
	const partSize = int64(64)
	// A whole number of parts, so the pre-allocation is exactly DecryptedSize —
	// the size at which the daemon adopts a file it finds already there.
	plaintext := bytes.Repeat([]byte("0123456789abcdef"), 24)

	ciphertext, masterKey, fileID := hkdfObject(t, plaintext, partSize)

	localPath := filepath.Join(t.TempDir(), "results.dat")
	mock := &mockHKDFPartDownloader{ciphertext: ciphertext, failFrom: 0}
	mock.formatVersion = 1
	mock.partSize = partSize

	prep := &DownloadPrep{
		Params: cloud.DownloadParams{
			RemotePath: "user/abc/results.dat",
			LocalPath:  localPath,
			FileInfo:   &models.CloudFile{DecryptedSize: int64(len(plaintext))},
		},
		FormatVersion: 1,
		PartSize:      partSize,
		EncryptionKey: masterKey,
	}

	err := NewDownloader(mock).downloadStreamingConcurrent(context.Background(), prep, 4, mock, fileID)
	if err == nil {
		t.Fatal("expected the refused part to fail the download")
	}

	if info, statErr := os.Stat(localPath); statErr == nil {
		t.Errorf("failed download left a %d-byte file at the destination; the daemon adopts one whose size matches DecryptedSize (%d)",
			info.Size(), len(plaintext))
	} else if !os.IsNotExist(statErr) {
		t.Errorf("stat %s: %v", localPath, statErr)
	}

	if _, statErr := os.Stat(localPath + ".partial"); !os.IsNotExist(statErr) {
		t.Error("failed download left its .partial file behind; nothing reads it back, so it is litter")
	}
}

// The success half of the same contract: the bytes end up at LocalPath, whole,
// and the scratch file is gone.
func TestDownloadStreamingConcurrentRenamesIntoPlace(t *testing.T) {
	const partSize = int64(64)
	plaintext := bytes.Repeat([]byte("interlink"), 40) // 360 bytes: six parts, the last one short

	ciphertext, masterKey, fileID := hkdfObject(t, plaintext, partSize)

	localPath := filepath.Join(t.TempDir(), "results.dat")
	mock := &mockHKDFPartDownloader{ciphertext: ciphertext, failFrom: -1}
	mock.formatVersion = 1
	mock.partSize = partSize

	prep := &DownloadPrep{
		Params: cloud.DownloadParams{
			RemotePath: "user/abc/results.dat",
			LocalPath:  localPath,
			FileInfo:   &models.CloudFile{DecryptedSize: int64(len(plaintext))},
		},
		FormatVersion: 1,
		PartSize:      partSize,
		EncryptionKey: masterKey,
	}

	if err := NewDownloader(mock).downloadStreamingConcurrent(context.Background(), prep, 4, mock, fileID); err != nil {
		t.Fatalf("downloadStreamingConcurrent: %v", err)
	}

	got, readErr := os.ReadFile(localPath)
	if readErr != nil {
		t.Fatalf("read downloaded file: %v", readErr)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("downloaded %d bytes, want %d", len(got), len(plaintext))
	}

	if _, statErr := os.Stat(localPath + ".partial"); !os.IsNotExist(statErr) {
		t.Error("successful download left its .partial file behind")
	}

	want := sha512.Sum512(plaintext)
	if !strings.EqualFold(prep.ComputedHash, hex.EncodeToString(want[:])) {
		t.Errorf("computed hash = %q, want %q", prep.ComputedHash, hex.EncodeToString(want[:]))
	}
}

// stallingCBCPartDownloader serves a CBC (v2) object one range at a time and
// holds the range at offset 0 until the test releases it, which is the shape of
// a stalled or repeatedly retried first part. It records how many ranges were
// started so a test can see how far ahead of that part the download ran.
type stallingCBCPartDownloader struct {
	mockStreamingDownloader
	ciphertext []byte
	release    chan struct{}

	mu      sync.Mutex
	started int
}

func (m *stallingCBCPartDownloader) GetEncryptedSize(ctx context.Context, remotePath string) (int64, string, error) {
	return int64(len(m.ciphertext)), "", nil
}

func (m *stallingCBCPartDownloader) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, version string, progressCallback func(int64)) ([]byte, error) {
	m.mu.Lock()
	m.started++
	m.mu.Unlock()

	if offset == 0 {
		select {
		case <-m.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	end := offset + length
	if end > int64(len(m.ciphertext)) {
		end = int64(len(m.ciphertext))
	}
	out := make([]byte, end-offset)
	copy(out, m.ciphertext[offset:end])
	if progressCallback != nil {
		progressCallback(int64(len(out)))
	}
	return out, nil
}

func (m *stallingCBCPartDownloader) rangesStarted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// The v2 path decrypts in order because CBC chains, so a part that arrives
// before the one ahead of it waits in partBuffer. Every part used to be queued
// up front, which meant a stalled part 0 did not slow the other workers down at
// all: they fetched the rest of the object and the buffer held every one of
// those ranges at once, with nothing capping it. On a large object that is the
// whole file in RAM.
//
// Part 0 stalls here while the rest of the object is available. The fetchers
// must not run further ahead than the reorder window, and the download must
// still finish once part 0 is released.
func TestDownloadCBCStreamingBoundsTheReorderWindow(t *testing.T) {
	const partSize = int64(64)
	// 40 whole parts of plaintext, so the object is far larger than the window.
	plaintext := bytes.Repeat([]byte("0123456789abcdef"), 4*40)

	enc, err := encryption.NewCBCStreamingEncryptor()
	if err != nil {
		t.Fatalf("NewCBCStreamingEncryptor: %v", err)
	}
	// One sealed stream: the driver splits it into partSize ranges itself, and
	// CBC decryption chains across those boundaries.
	ciphertext, err := enc.EncryptPart(plaintext, true)
	if err != nil {
		t.Fatalf("EncryptPart: %v", err)
	}

	mock := &stallingCBCPartDownloader{ciphertext: ciphertext, release: make(chan struct{})}
	mock.formatVersion = 2
	mock.partSize = partSize

	localPath := filepath.Join(t.TempDir(), "results.dat")
	prep := &DownloadPrep{
		Params: cloud.DownloadParams{
			RemotePath: "user/abc/results.dat",
			LocalPath:  localPath,
			FileInfo:   &models.CloudFile{DecryptedSize: int64(len(plaintext))},
		},
		FormatVersion: 2,
		PartSize:      partSize,
		EncryptionKey: enc.GetKey(),
		IV:            enc.GetInitialIV(),
		EncryptedSize: int64(len(ciphertext)),
	}

	done := make(chan error, 1)
	go func() {
		done <- NewDownloader(mock).downloadCBCStreaming(context.Background(), prep)
	}()

	// No transfer handle means the default concurrency of 4, and the window is
	// twice that.
	const window = 8
	waitFor(t, "the fetchers to fill the reorder window",
		func() bool { return mock.rangesStarted() >= window })
	// Long enough for an unbounded dispatcher to fetch the rest of the object.
	time.Sleep(150 * time.Millisecond)
	if got := mock.rangesStarted(); got > window {
		t.Errorf("%d ranges were fetched while part 0 stalled, want at most the %d-part reorder window", got, window)
	}

	close(mock.release)
	if err := <-done; err != nil {
		t.Fatalf("downloadCBCStreaming after part 0 was released: %v", err)
	}

	got, readErr := os.ReadFile(localPath)
	if readErr != nil {
		t.Fatalf("read downloaded file: %v", readErr)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("downloaded %d bytes, want the original %d", len(got), len(plaintext))
	}
}

// replacedCBCPartDownloader serves a v2 object that is replaced under the
// download. Ranges below replacedTo come from a second, unrelated encryption of
// the same length — the shape of a re-upload under the same path. A range asked
// for under the version GetEncryptedSize reported answers the way a pinned
// provider answers; an unpinned one hands over the new object's bytes, the way
// this path answered when there was no version to compare against.
type replacedCBCPartDownloader struct {
	mockStreamingDownloader
	ciphertext  []byte // the version GetEncryptedSize measured
	replacement []byte // the object that took its place, same length
	replacedTo  int64
}

const cbcPinnedVersion = `"version-one"`

func (m *replacedCBCPartDownloader) GetEncryptedSize(ctx context.Context, remotePath string) (int64, string, error) {
	return int64(len(m.ciphertext)), cbcPinnedVersion, nil
}

func (m *replacedCBCPartDownloader) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, version string, progressCallback func(int64)) ([]byte, error) {
	source := m.ciphertext
	if offset < m.replacedTo {
		if version != "" {
			return nil, ErrObjectReplaced
		}
		source = m.replacement
	}

	end := offset + length
	if end > int64(len(source)) {
		end = int64(len(source))
	}
	out := make([]byte, end-offset)
	copy(out, source[offset:end])
	if progressCallback != nil {
		progressCallback(int64(len(out)))
	}
	return out, nil
}

// The v2 path reads the object's size once and then fetches its parts as
// independent ranges. Replace the object in between and every range fetched
// afterwards comes from the new one, and CBC does not notice: padding is only
// validated on the final part, so as long as that part still comes from the
// object the download started on, the mixture decrypts, lands at full length
// and is reported as a finished download. A per-file checksum is the only thing
// that would catch it and not every file carries one.
func TestDownloadCBCStreamingAbortsWhenTheObjectIsReplaced(t *testing.T) {
	const partSize = int64(64)
	// Six whole parts plus the padding block, so the replaced first part is
	// nowhere near the final one.
	plaintext := bytes.Repeat([]byte("0123456789abcdef"), 4*6)

	enc, err := encryption.NewCBCStreamingEncryptor()
	if err != nil {
		t.Fatalf("NewCBCStreamingEncryptor: %v", err)
	}
	ciphertext, err := enc.EncryptPart(plaintext, true)
	if err != nil {
		t.Fatalf("EncryptPart: %v", err)
	}

	// The object that takes its place: same length, its own key and IV, so its
	// bytes are unrelated ciphertext under the key this download decrypts with.
	other, err := encryption.NewCBCStreamingEncryptor()
	if err != nil {
		t.Fatalf("NewCBCStreamingEncryptor: %v", err)
	}
	replacement, err := other.EncryptPart(plaintext, true)
	if err != nil {
		t.Fatalf("EncryptPart: %v", err)
	}

	mock := &replacedCBCPartDownloader{ciphertext: ciphertext, replacement: replacement, replacedTo: partSize}
	mock.formatVersion = 2
	mock.partSize = partSize

	dir := t.TempDir()
	localPath := filepath.Join(dir, "results.dat")
	prep := &DownloadPrep{
		Params: cloud.DownloadParams{
			RemotePath: "user/abc/results.dat",
			LocalPath:  localPath,
			FileInfo:   &models.CloudFile{DecryptedSize: int64(len(plaintext))},
		},
		FormatVersion: 2,
		PartSize:      partSize,
		EncryptionKey: enc.GetKey(),
		IV:            enc.GetInitialIV(),
	}

	err = NewDownloader(mock).downloadCBCStreaming(context.Background(), prep)
	if !errors.Is(err, ErrObjectReplaced) {
		t.Fatalf("error = %v, want it to wrap ErrObjectReplaced", err)
	}

	// The v2 path writes straight to the destination, so an aborted download has
	// to leave something visibly short of the file that was asked for rather
	// than a full-length one the next run would adopt.
	got, readErr := os.ReadFile(localPath)
	if readErr != nil {
		t.Fatalf("read the destination: %v", readErr)
	}
	if len(got) >= len(plaintext) {
		t.Errorf("destination holds %d bytes of a %d-byte file: the aborted download left a full-length file behind",
			len(got), len(plaintext))
	}

	// The v2 path keeps no resume state, so nothing beside the destination may
	// be left claiming the parts this attempt got through.
	entries, dirErr := os.ReadDir(dir)
	if dirErr != nil {
		t.Fatalf("read the destination directory: %v", dirErr)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(localPath) {
		t.Errorf("destination directory holds %v, want only the destination file", entries)
	}
}

// cancellingHKDFPartDownloader serves an HKDF (v1) object and cancels the
// download's own context once the first range is in hand, which is what a user
// pressing Ctrl-C or a shutdown between two part requests looks like from
// inside the driver.
type cancellingHKDFPartDownloader struct {
	mockStreamingDownloader
	ciphertext []byte
	cancel     context.CancelFunc

	mu     sync.Mutex
	served int
}

func (m *cancellingHKDFPartDownloader) GetEncryptedSize(ctx context.Context, remotePath string) (int64, string, error) {
	return int64(len(m.ciphertext)), "", nil
}

func (m *cancellingHKDFPartDownloader) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, version string, progressCallback func(int64)) ([]byte, error) {
	end := offset + length
	if end > int64(len(m.ciphertext)) {
		end = int64(len(m.ciphertext))
	}
	out := make([]byte, end-offset)
	copy(out, m.ciphertext[offset:end])

	m.mu.Lock()
	m.served++
	first := m.served == 1
	m.mu.Unlock()
	if first {
		m.cancel()
	}
	return out, nil
}

func (m *cancellingHKDFPartDownloader) rangesServed() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.served
}

// The v1 workers return silently on a cancelled context, and the driver counted
// the results it received without ever checking the count. A cancellation
// between two part requests therefore ran straight on to truncation, hashing
// and the rename: a file of the right length, holed wherever a part never
// arrived, published as a finished download. Cancelling is not failing, but it
// is not succeeding either.
func TestDownloadStreamingConcurrentRefusesToCallCancellationSuccess(t *testing.T) {
	const partSize = int64(64)
	plaintext := bytes.Repeat([]byte("0123456789abcdef"), 4*8) // eight whole parts

	ciphertext, masterKey, fileID := hkdfObject(t, plaintext, partSize)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localPath := filepath.Join(t.TempDir(), "results.dat")
	mock := &cancellingHKDFPartDownloader{ciphertext: ciphertext, cancel: cancel}
	mock.formatVersion = 1
	mock.partSize = partSize

	prep := &DownloadPrep{
		Params: cloud.DownloadParams{
			RemotePath: "user/abc/results.dat",
			LocalPath:  localPath,
			FileInfo:   &models.CloudFile{DecryptedSize: int64(len(plaintext))},
		},
		FormatVersion: 1,
		PartSize:      partSize,
		EncryptionKey: masterKey,
	}

	err := NewDownloader(mock).downloadStreamingConcurrent(ctx, prep, 1, mock, fileID)
	if err == nil {
		t.Fatal("a cancelled download returned success, so a file with holes in it is now the download")
	}
	if served := mock.rangesServed(); int64(served) >= int64(len(plaintext))/partSize {
		t.Fatalf("test setup: all %d parts were served, so nothing was actually cut short", served)
	}

	if info, statErr := os.Stat(localPath); statErr == nil {
		t.Errorf("a cancelled download left a %d-byte file at the destination", info.Size())
	} else if !os.IsNotExist(statErr) {
		t.Errorf("stat %s: %v", localPath, statErr)
	}
}

// slowFailHKDFPartDownloader refuses every range, but only after a pause long
// enough for the producer to have filled the job queue and parked on a send it
// cannot complete — the state the leak this test is about needs.
type slowFailHKDFPartDownloader struct {
	mockStreamingDownloader
	ciphertext []byte
}

func (m *slowFailHKDFPartDownloader) GetEncryptedSize(ctx context.Context, remotePath string) (int64, string, error) {
	return int64(len(m.ciphertext)), "", nil
}

func (m *slowFailHKDFPartDownloader) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, version string, progressCallback func(int64)) ([]byte, error) {
	time.Sleep(50 * time.Millisecond)
	return nil, fmt.Errorf("range at %d: connection reset by peer", offset)
}

// The v1 driver's producer sent into the job queue without watching the
// context, and the wait group covered the workers only. A failed part cancelled
// the operation, the workers exited, and nothing was left to drain the queue —
// so the producer stayed parked on its send while the driver returned. Each
// retried attempt left another one, along with the buffers it held.
func TestDownloadStreamingConcurrentJoinsProducerOnFailure(t *testing.T) {
	const partSize = int64(64)
	// More parts than the queue is deep, so the producer is certain to be
	// blocked on a send when the first part's failure cancels the operation.
	plaintext := bytes.Repeat([]byte("0123456789abcdef"), 4*8)

	ciphertext, masterKey, fileID := hkdfObject(t, plaintext, partSize)

	localPath := filepath.Join(t.TempDir(), "results.dat")
	mock := &slowFailHKDFPartDownloader{ciphertext: ciphertext}
	mock.formatVersion = 1
	mock.partSize = partSize

	prep := &DownloadPrep{
		Params: cloud.DownloadParams{
			RemotePath: "user/abc/results.dat",
			LocalPath:  localPath,
			FileInfo:   &models.CloudFile{DecryptedSize: int64(len(plaintext))},
		},
		FormatVersion: 1,
		PartSize:      partSize,
		EncryptionKey: masterKey,
	}

	baseline := goroutineBaseline(t)

	err := NewDownloader(mock).downloadStreamingConcurrent(context.Background(), prep, 1, mock, fileID)
	if err == nil {
		t.Fatal("expected the refused range to fail the download")
	}

	waitForGoroutines(t, baseline)
}

// tickingHKDFPartDownloader serves every range at once except the last, which it
// holds until the progress ticker has been let into the callback. That is what
// puts the ticker goroutine inside the callback at the moment the download
// finishes, which is the only moment at which joining it can be observed.
type tickingHKDFPartDownloader struct {
	mockStreamingDownloader
	ciphertext []byte
	entered    chan struct{}
}

func (m *tickingHKDFPartDownloader) GetEncryptedSize(ctx context.Context, remotePath string) (int64, string, error) {
	return int64(len(m.ciphertext)), "", nil
}

func (m *tickingHKDFPartDownloader) DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, version string, progressCallback func(int64)) ([]byte, error) {
	end := offset + length
	if end >= int64(len(m.ciphertext)) {
		end = int64(len(m.ciphertext))
		select {
		case <-m.entered:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return nil, fmt.Errorf("the progress ticker never reached the callback")
		}
	}
	out := make([]byte, end-offset)
	copy(out, m.ciphertext[offset:end])
	return out, nil
}

// TestDownloadStreamingConcurrentJoinsTheProgressTicker is the F8 residual: the
// driver closed the ticker's stop channel on the way out without waiting for the
// goroutine to see it. The callback it calls belongs to the caller — a progress
// bar, a queue entry — and calling it after the download has returned reports
// progress for a transfer that is over, on a goroutine nothing is waiting for.
func TestDownloadStreamingConcurrentJoinsTheProgressTicker(t *testing.T) {
	const partSize = int64(64)
	plaintext := bytes.Repeat([]byte("interlink"), 64) // nine parts, the last one short
	ciphertext, masterKey, fileID := hkdfObject(t, plaintext, partSize)

	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once

	mock := &tickingHKDFPartDownloader{ciphertext: ciphertext, entered: entered}
	mock.formatVersion = 1
	mock.partSize = partSize

	prep := &DownloadPrep{
		Params: cloud.DownloadParams{
			RemotePath: "user/abc/results.dat",
			LocalPath:  filepath.Join(t.TempDir(), "results.dat"),
			FileInfo:   &models.CloudFile{DecryptedSize: int64(len(plaintext))},
			ProgressCallback: func(progress float64) {
				// Only the ticker reports a fraction of the way through; the
				// driver's own 0% and 100% reports must not be held up.
				if progress <= 0 || progress >= 1 {
					return
				}
				enterOnce.Do(func() { close(entered) })
				<-release
			},
		},
		FormatVersion: 1,
		PartSize:      partSize,
		EncryptionKey: masterKey,
	}

	done := make(chan error, 1)
	go func() {
		done <- NewDownloader(mock).downloadStreamingConcurrent(context.Background(), prep, 4, mock, fileID)
	}()

	select {
	case err := <-done:
		close(release)
		t.Fatalf("the download returned (err=%v) while its progress goroutine was still inside the callback", err)
	case <-time.After(500 * time.Millisecond):
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("download failed: %v", err)
	}

	got, err := os.ReadFile(prep.Params.LocalPath)
	if err != nil {
		t.Fatalf("reading the downloaded file: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("downloaded %d bytes, want the %d of the source", len(got), len(plaintext))
	}
}
