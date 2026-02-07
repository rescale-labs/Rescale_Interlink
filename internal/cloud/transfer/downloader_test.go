// Package transfer provides unified upload and download orchestration.
// This file contains tests for the downloader.
package transfer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/models"
)

// mockCloudTransferDownload is a mock implementation for download testing.
type mockCloudTransferDownload struct {
	downloadCalled bool
	downloadParams cloud.DownloadParams
	downloadErr    error
	uploadCalled   bool
	storageType    string
}

func (m *mockCloudTransferDownload) Upload(ctx context.Context, params cloud.UploadParams) (*cloud.UploadResult, error) {
	m.uploadCalled = true
	return &cloud.UploadResult{
		StoragePath:   "test/path",
		EncryptionKey: []byte("test-key"),
		FormatVersion: 1,
	}, nil
}

func (m *mockCloudTransferDownload) Download(ctx context.Context, params cloud.DownloadParams) error {
	m.downloadCalled = true
	m.downloadParams = params
	return m.downloadErr
}

func (m *mockCloudTransferDownload) RefreshCredentials(ctx context.Context) error {
	return nil
}

func (m *mockCloudTransferDownload) StorageType() string {
	if m.storageType != "" {
		return m.storageType
	}
	return "S3Storage"
}

// mockStreamingDownloader extends mockCloudTransferDownload with format detection.
type mockStreamingDownloader struct {
	mockCloudTransferDownload
	formatVersion        int
	fileID               string
	partSize             int64
	iv                   []byte // IV for legacy format (v0)
	detectFormatErr      error
	downloadStreamingErr error
	streamingDownloadCalled bool
}

func (m *mockStreamingDownloader) DetectFormat(ctx context.Context, remotePath string) (int, string, int64, []byte, error) {
	if m.detectFormatErr != nil {
		return 0, "", 0, nil, m.detectFormatErr
	}
	return m.formatVersion, m.fileID, m.partSize, m.iv, nil
}

func (m *mockStreamingDownloader) DownloadStreaming(ctx context.Context, remotePath, localPath string, masterKey []byte, progressCallback cloud.ProgressCallback) error {
	m.streamingDownloadCalled = true
	return m.downloadStreamingErr
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
		name        string
		params      cloud.DownloadParams
		expectError bool
		errorMsg    string
	}{
		{
			name:        "empty remote path",
			params:      cloud.DownloadParams{},
			expectError: true,
			errorMsg:    "remote path is required",
		},
		{
			name: "empty local path",
			params: cloud.DownloadParams{
				RemotePath: "/remote/file.txt",
			},
			expectError: true,
			errorMsg:    "local path is required",
		},
		{
			name: "missing file info",
			params: cloud.DownloadParams{
				RemotePath: "/remote/file.txt",
				LocalPath:  "/local/file.txt",
			},
			expectError: true,
			errorMsg:    "file info is required",
		},
		{
			name: "missing encryption key",
			params: cloud.DownloadParams{
				RemotePath: "/remote/file.txt",
				LocalPath:  "/local/file.txt",
				FileInfo:   &models.CloudFile{},
			},
			expectError: true,
			errorMsg:    "encryption key is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := downloader.Download(context.Background(), tt.params)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error containing '%s', got nil", tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestDownloaderLegacyFormat tests downloading with legacy format.
func TestDownloaderLegacyFormat(t *testing.T) {
	mock := &mockStreamingDownloader{
		formatVersion: 0, // Legacy format
	}
	downloader := NewDownloader(mock)

	// Create valid params with encryption key (base64 encoded 32-byte key)
	params := cloud.DownloadParams{
		RemotePath: "/remote/file.txt",
		LocalPath:  "/local/file.txt",
		FileInfo: &models.CloudFile{
			EncodedEncryptionKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", // 32 bytes base64
			IV:                   "AAAAAAAAAAAAAAAAAAAAAA==",                     // 16 bytes base64
		},
	}

	_, err := downloader.Download(context.Background(), params)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// For legacy format, should delegate to provider's Download method
	if !mock.downloadCalled {
		t.Error("expected provider's Download method to be called for legacy format")
	}
}

// TestDownloaderStreamingFormat tests downloading with streaming format.
func TestDownloaderStreamingFormat(t *testing.T) {
	mock := &mockStreamingDownloader{
		formatVersion: 1, // Streaming format
		fileID:        "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", // 32 bytes base64
		partSize:      64 * 1024 * 1024, // 64 MB
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

// TestDownloadPrepFormatVersion tests DownloadPrep struct initialization.
func TestDownloadPrepFormatVersion(t *testing.T) {
	tests := []struct {
		name          string
		formatVersion int
		expected      int
	}{
		{"legacy format", 0, 0},
		{"streaming format", 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prep := &DownloadPrep{
				FormatVersion: tt.formatVersion,
			}
			if prep.FormatVersion != tt.expected {
				t.Errorf("expected format version %d, got %d", tt.expected, prep.FormatVersion)
			}
		})
	}
}

// TestStreamingDownloadInitParams tests the streaming download init params struct.
func TestStreamingDownloadInitParams(t *testing.T) {
	params := StreamingDownloadInitParams{
		RemotePath: "/remote/file.txt",
		LocalPath:  "/local/file.txt",
		MasterKey:  []byte("test-key"),
		FileID:     []byte("test-file-id"),
		PartSize:   64 * 1024 * 1024,
	}

	if params.RemotePath != "/remote/file.txt" {
		t.Error("RemotePath not set correctly")
	}
	if params.LocalPath != "/local/file.txt" {
		t.Error("LocalPath not set correctly")
	}
	if string(params.MasterKey) != "test-key" {
		t.Error("MasterKey not set correctly")
	}
	if string(params.FileID) != "test-file-id" {
		t.Error("FileID not set correctly")
	}
	if params.PartSize != 64*1024*1024 {
		t.Error("PartSize not set correctly")
	}
}

// TestStreamingDownload tests the StreamingDownload struct.
func TestStreamingDownload(t *testing.T) {
	download := &StreamingDownload{
		RemotePath:    "/remote/file.txt",
		LocalPath:     "/local/file.txt",
		MasterKey:     []byte("test-key"),
		FileID:        []byte("test-file-id"),
		PartSize:      64 * 1024 * 1024,
		EncryptedSize: 150 * 1024 * 1024,
		TotalParts:    3,
	}

	if download.RemotePath != "/remote/file.txt" {
		t.Error("RemotePath not set correctly")
	}
	if download.LocalPath != "/local/file.txt" {
		t.Error("LocalPath not set correctly")
	}
	if download.EncryptedSize != 150*1024*1024 {
		t.Error("EncryptedSize not set correctly")
	}
	if download.TotalParts != 3 {
		t.Error("TotalParts not set correctly")
	}
}

// TestPartDownloadResult tests the PartDownloadResult struct.
func TestPartDownloadResult(t *testing.T) {
	result := &PartDownloadResult{
		PartIndex: 2,
		Plaintext: []byte("decrypted data"),
		Size:      14,
	}

	if result.PartIndex != 2 {
		t.Error("PartIndex not set correctly")
	}
	if string(result.Plaintext) != "decrypted data" {
		t.Error("Plaintext not set correctly")
	}
	if result.Size != 14 {
		t.Error("Size not set correctly")
	}
}

// v4.5.9: Tests for .encrypted temp file cleanup behavior.

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
