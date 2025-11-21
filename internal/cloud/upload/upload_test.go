package upload

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestUploadResumeStateSaveLoad tests saving and loading upload resume state
func TestUploadResumeStateSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")

	// Create test file
	testData := []byte("test data for upload")
	if err := os.WriteFile(localPath, testData, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create resume state
	originalState := &UploadResumeState{
		LocalPath:     localPath,
		EncryptedPath: localPath + ".encrypted",
		ObjectKey:     "test/object/key",
		UploadID:      "test-upload-id-12345",
		TotalSize:     12345,
		OriginalSize:  int64(len(testData)),
		UploadedBytes: 6789,
		CompletedParts: []CompletedPart{
			{PartNumber: 1, ETag: "etag-1"},
			{PartNumber: 2, ETag: "etag-2"},
		},
		EncryptionKey: "base64-encoded-key",
		IV:            "base64-encoded-iv",
		RandomSuffix:  "abc123",
		CreatedAt:     time.Now(),
		LastUpdate:    time.Now(),
		StorageType:   "S3Storage",
	}

	// Save state
	if err := SaveUploadState(originalState, localPath); err != nil {
		t.Fatalf("SaveUploadState() failed: %v", err)
	}

	// Verify state file exists
	stateFilePath := localPath + ".upload.resume"
	if _, err := os.Stat(stateFilePath); err != nil {
		t.Fatalf("State file not created: %v", err)
	}

	// Load state
	loadedState, err := LoadUploadState(localPath)
	if err != nil {
		t.Fatalf("LoadUploadState() failed: %v", err)
	}

	if loadedState == nil {
		t.Fatal("LoadUploadState() returned nil")
	}

	// Verify loaded state matches original
	if loadedState.LocalPath != originalState.LocalPath {
		t.Errorf("LocalPath mismatch: got %s, want %s", loadedState.LocalPath, originalState.LocalPath)
	}

	if loadedState.UploadID != originalState.UploadID {
		t.Errorf("UploadID mismatch: got %s, want %s", loadedState.UploadID, originalState.UploadID)
	}

	if loadedState.TotalSize != originalState.TotalSize {
		t.Errorf("TotalSize mismatch: got %d, want %d", loadedState.TotalSize, originalState.TotalSize)
	}

	if loadedState.UploadedBytes != originalState.UploadedBytes {
		t.Errorf("UploadedBytes mismatch: got %d, want %d", loadedState.UploadedBytes, originalState.UploadedBytes)
	}

	if len(loadedState.CompletedParts) != len(originalState.CompletedParts) {
		t.Errorf("CompletedParts count mismatch: got %d, want %d",
			len(loadedState.CompletedParts), len(originalState.CompletedParts))
	}
}

// TestUploadResumeStateNoFile tests loading when no resume state exists
func TestUploadResumeStateNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "nonexistent.dat")

	// Try to load non-existent state
	state, err := LoadUploadState(localPath)
	if err != nil {
		t.Errorf("LoadUploadState() should return nil without error for missing file, got error: %v", err)
	}

	if state != nil {
		t.Error("LoadUploadState() should return nil state for missing file")
	}
}

// TestValidateUploadStateExpired tests that expired states are rejected
func TestValidateUploadStateExpired(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")

	// Create test file
	if err := os.WriteFile(localPath, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create encrypted temp file
	encryptedPath := localPath + ".encrypted"
	if err := os.WriteFile(encryptedPath, []byte("encrypted"), 0644); err != nil {
		t.Fatalf("Failed to create encrypted file: %v", err)
	}

	// Create state with creation time > 7 days ago
	state := &UploadResumeState{
		LocalPath:     localPath,
		EncryptedPath: encryptedPath,
		TotalSize:     100,
		OriginalSize:  100,
		CreatedAt:     time.Now().Add(-8 * 24 * time.Hour), // 8 days ago (expired)
		LastUpdate:    time.Now(),
	}

	// Validation should fail due to age
	err := ValidateUploadState(state, localPath)
	if err == nil {
		t.Error("ValidateUploadState() should reject expired state")
	}
}

// TestValidateUploadStateFileSizeChange tests that state is rejected if file size changed
func TestValidateUploadStateFileSizeChange(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")

	// Create test file
	if err := os.WriteFile(localPath, []byte("test data"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create encrypted temp file
	encryptedPath := localPath + ".encrypted"
	if err := os.WriteFile(encryptedPath, []byte("encrypted"), 0644); err != nil {
		t.Fatalf("Failed to create encrypted file: %v", err)
	}

	// Create state with wrong original size
	state := &UploadResumeState{
		LocalPath:     localPath,
		EncryptedPath: encryptedPath,
		TotalSize:     100,
		OriginalSize:  5, // Wrong! Actual file is 9 bytes
		CreatedAt:     time.Now(),
		LastUpdate:    time.Now(),
	}

	// Validation should fail due to size mismatch
	err := ValidateUploadState(state, localPath)
	if err == nil {
		t.Error("ValidateUploadState() should reject state when file size changed")
	}
}

// TestValidateUploadStateMissingEncryptedFile tests rejection when encrypted temp file is missing
func TestValidateUploadStateMissingEncryptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")

	// Create test file
	testData := []byte("test data")
	if err := os.WriteFile(localPath, testData, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create state pointing to non-existent encrypted file
	state := &UploadResumeState{
		LocalPath:     localPath,
		EncryptedPath: filepath.Join(tmpDir, "missing.encrypted"), // Does not exist!
		TotalSize:     100,
		OriginalSize:  int64(len(testData)),
		CreatedAt:     time.Now(),
		LastUpdate:    time.Now(),
	}

	// Validation should fail because encrypted file is missing
	err := ValidateUploadState(state, localPath)
	if err == nil {
		t.Error("ValidateUploadState() should reject state when encrypted file is missing")
	}
}

// TestUploadResumeStateAtomic tests that state saves are atomic (temp file + rename)
func TestUploadResumeStateAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")

	// Create test file
	if err := os.WriteFile(localPath, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	state := &UploadResumeState{
		LocalPath:    localPath,
		TotalSize:    100,
		OriginalSize: 4,
		CreatedAt:    time.Now(),
		LastUpdate:   time.Now(),
	}

	// Save state
	if err := SaveUploadState(state, localPath); err != nil {
		t.Fatalf("SaveUploadState() failed: %v", err)
	}

	// Verify temp file was cleaned up (atomic rename should remove it)
	tmpFilePath := localPath + ".upload.resume.tmp"
	if _, err := os.Stat(tmpFilePath); !os.IsNotExist(err) {
		t.Error("Temporary state file should be removed after atomic save")
	}

	// Verify final state file exists
	stateFilePath := localPath + ".upload.resume"
	if _, err := os.Stat(stateFilePath); err != nil {
		t.Errorf("State file should exist after save: %v", err)
	}
}
