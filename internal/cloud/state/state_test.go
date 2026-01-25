// Package state tests
package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestUploadState_FilePermissions verifies that upload state files are created with secure permissions (0600).
// v4.4.2: Security fix - state files contain encryption keys and must be owner-readable only.
func TestUploadState_FilePermissions(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "upload-state-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	localPath := filepath.Join(tmpDir, "testfile.bin")

	// Create a test file
	if err := os.WriteFile(localPath, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create and save upload state with sensitive data (encryption keys)
	state := &UploadResumeState{
		LocalPath:     localPath,
		ObjectKey:     "test/object",
		EncryptionKey: "dGVzdC1lbmNyeXB0aW9uLWtleS1iYXNlNjQ=", // Simulated base64 key
		IV:            "dGVzdC1pdi1iYXNlNjQ=",                   // Simulated base64 IV
		MasterKey:     "dGVzdC1tYXN0ZXIta2V5LWJhc2U2NA==",       // Simulated base64 master key
		CreatedAt:     time.Now(),
		LastUpdate:    time.Now(),
		TotalSize:     1024,
		OriginalSize:  1024,
	}

	if err := SaveUploadState(state, localPath); err != nil {
		t.Fatalf("SaveUploadState failed: %v", err)
	}

	// Check file permissions
	stateFile := localPath + ".upload.resume"
	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("Failed to stat state file: %v", err)
	}

	// Permissions should be 0600 (owner read/write only)
	perm := info.Mode().Perm()
	expectedPerm := os.FileMode(0600)

	if perm != expectedPerm {
		t.Errorf("Upload state file permissions should be %o, got %o", expectedPerm, perm)
	}
}

// TestDownloadState_FilePermissions verifies that download state files are created with secure permissions (0600).
// v4.4.2: Security fix - state files contain sensitive metadata and must be owner-readable only.
func TestDownloadState_FilePermissions(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "download-state-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	localPath := filepath.Join(tmpDir, "testfile.bin")

	// Create and save download state with sensitive data
	state := &DownloadResumeState{
		LocalPath:       localPath,
		RemotePath:      "test/object",
		MasterKey:       "dGVzdC1tYXN0ZXIta2V5LWJhc2U2NA==", // Simulated base64 master key
		StreamingFileId: "dGVzdC1maWxlLWlk",                  // Simulated base64 file ID
		CreatedAt:       time.Now(),
		LastUpdate:      time.Now(),
		TotalSize:       1024,
	}

	if err := SaveDownloadState(state, localPath); err != nil {
		t.Fatalf("SaveDownloadState failed: %v", err)
	}

	// Check file permissions
	stateFile := localPath + ".download.resume"
	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("Failed to stat state file: %v", err)
	}

	// Permissions should be 0600 (owner read/write only)
	perm := info.Mode().Perm()
	expectedPerm := os.FileMode(0600)

	if perm != expectedPerm {
		t.Errorf("Download state file permissions should be %o, got %o", expectedPerm, perm)
	}
}

// TestUploadLock_FilePermissions verifies that upload lock files are created with secure permissions (0600).
// v4.4.2: Security fix - lock files contain process metadata and should be owner-readable only.
func TestUploadLock_FilePermissions(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "upload-lock-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	localPath := filepath.Join(tmpDir, "testfile.bin")

	// Acquire an upload lock
	lock, err := AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("AcquireUploadLock failed: %v", err)
	}
	defer ReleaseUploadLock(lock)

	// Check file permissions
	info, err := os.Stat(lock.LockFilePath)
	if err != nil {
		t.Fatalf("Failed to stat lock file: %v", err)
	}

	// Permissions should be 0600 (owner read/write only)
	perm := info.Mode().Perm()
	expectedPerm := os.FileMode(0600)

	if perm != expectedPerm {
		t.Errorf("Upload lock file permissions should be %o, got %o", expectedPerm, perm)
	}
}

// TestUploadState_RoundTrip tests save/load functionality works correctly.
func TestUploadState_RoundTrip(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "upload-state-roundtrip-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	localPath := filepath.Join(tmpDir, "testfile.bin")

	// Create test file (needed for validation)
	if err := os.WriteFile(localPath, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create state
	original := &UploadResumeState{
		LocalPath:      localPath,
		ObjectKey:      "bucket/key",
		UploadID:       "test-upload-id",
		TotalSize:      12,
		OriginalSize:   12,
		UploadedBytes:  0,
		EncryptionKey:  "enc-key",
		IV:             "iv-value",
		RandomSuffix:   "abc123",
		CreatedAt:      time.Now().Truncate(time.Second),
		LastUpdate:     time.Now().Truncate(time.Second),
		StorageType:    "S3Storage",
		FormatVersion:  1,
		MasterKey:      "master-key",
		FileId:         "file-id",
		PartSize:       1024 * 1024,
		ProcessID:      os.Getpid(),
		LockAcquiredAt: time.Now().Truncate(time.Second),
	}

	// Save
	if err := SaveUploadState(original, localPath); err != nil {
		t.Fatalf("SaveUploadState failed: %v", err)
	}

	// Load
	loaded, err := LoadUploadState(localPath)
	if err != nil {
		t.Fatalf("LoadUploadState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadUploadState returned nil")
	}

	// Verify fields
	if loaded.LocalPath != original.LocalPath {
		t.Errorf("LocalPath: expected %q, got %q", original.LocalPath, loaded.LocalPath)
	}
	if loaded.ObjectKey != original.ObjectKey {
		t.Errorf("ObjectKey: expected %q, got %q", original.ObjectKey, loaded.ObjectKey)
	}
	if loaded.EncryptionKey != original.EncryptionKey {
		t.Errorf("EncryptionKey: expected %q, got %q", original.EncryptionKey, loaded.EncryptionKey)
	}
	if loaded.MasterKey != original.MasterKey {
		t.Errorf("MasterKey: expected %q, got %q", original.MasterKey, loaded.MasterKey)
	}
	if loaded.FormatVersion != original.FormatVersion {
		t.Errorf("FormatVersion: expected %d, got %d", original.FormatVersion, loaded.FormatVersion)
	}
}

// TestDownloadState_RoundTrip tests save/load functionality works correctly.
func TestDownloadState_RoundTrip(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "download-state-roundtrip-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	localPath := filepath.Join(tmpDir, "testfile.bin")

	// Create state
	original := &DownloadResumeState{
		LocalPath:       localPath,
		RemotePath:      "bucket/key",
		FileID:          "file-123",
		TotalSize:       1024,
		DownloadedBytes: 512,
		ETag:            "etag-value",
		CreatedAt:       time.Now().Truncate(time.Second),
		LastUpdate:      time.Now().Truncate(time.Second),
		StorageType:     "S3Storage",
		FormatVersion:   1,
		MasterKey:       "master-key",
		StreamingFileId: "streaming-file-id",
		PartSize:        64 * 1024,
	}

	// Save
	if err := SaveDownloadState(original, localPath); err != nil {
		t.Fatalf("SaveDownloadState failed: %v", err)
	}

	// Load
	loaded, err := LoadDownloadState(localPath)
	if err != nil {
		t.Fatalf("LoadDownloadState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadDownloadState returned nil")
	}

	// Verify fields
	if loaded.LocalPath != original.LocalPath {
		t.Errorf("LocalPath: expected %q, got %q", original.LocalPath, loaded.LocalPath)
	}
	if loaded.RemotePath != original.RemotePath {
		t.Errorf("RemotePath: expected %q, got %q", original.RemotePath, loaded.RemotePath)
	}
	if loaded.MasterKey != original.MasterKey {
		t.Errorf("MasterKey: expected %q, got %q", original.MasterKey, loaded.MasterKey)
	}
	if loaded.StreamingFileId != original.StreamingFileId {
		t.Errorf("StreamingFileId: expected %q, got %q", original.StreamingFileId, loaded.StreamingFileId)
	}
	if loaded.FormatVersion != original.FormatVersion {
		t.Errorf("FormatVersion: expected %d, got %d", original.FormatVersion, loaded.FormatVersion)
	}
}
