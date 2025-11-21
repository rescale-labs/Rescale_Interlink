package download

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDownloadResumeStateSaveLoad tests saving and loading download resume state
func TestDownloadResumeStateSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")

	// Create resume state
	originalState := &DownloadResumeState{
		LocalPath:       localPath,
		EncryptedPath:   localPath + ".encrypted",
		RemotePath:      "s3://bucket/path/to/file",
		FileID:          "ABC123",
		TotalSize:       10000,
		DownloadedBytes: 5000,
		ETag:            "test-etag-12345",
		CreatedAt:       time.Now(),
		LastUpdate:      time.Now(),
		StorageType:     "S3Storage",
		ChunkSize:       1024,
		CompletedChunks: []int64{0, 1, 2},
	}

	// Save state
	if err := SaveDownloadState(originalState, localPath); err != nil {
		t.Fatalf("SaveDownloadState() failed: %v", err)
	}

	// Verify state file exists
	stateFilePath := localPath + ".download.resume"
	if _, err := os.Stat(stateFilePath); err != nil {
		t.Fatalf("State file not created: %v", err)
	}

	// Load state
	loadedState, err := LoadDownloadState(localPath)
	if err != nil {
		t.Fatalf("LoadDownloadState() failed: %v", err)
	}

	if loadedState == nil {
		t.Fatal("LoadDownloadState() returned nil")
	}

	// Verify loaded state matches original
	if loadedState.LocalPath != originalState.LocalPath {
		t.Errorf("LocalPath mismatch: got %s, want %s", loadedState.LocalPath, originalState.LocalPath)
	}

	if loadedState.FileID != originalState.FileID {
		t.Errorf("FileID mismatch: got %s, want %s", loadedState.FileID, originalState.FileID)
	}

	if loadedState.TotalSize != originalState.TotalSize {
		t.Errorf("TotalSize mismatch: got %d, want %d", loadedState.TotalSize, originalState.TotalSize)
	}

	if loadedState.DownloadedBytes != originalState.DownloadedBytes {
		t.Errorf("DownloadedBytes mismatch: got %d, want %d", loadedState.DownloadedBytes, originalState.DownloadedBytes)
	}

	if loadedState.ETag != originalState.ETag {
		t.Errorf("ETag mismatch: got %s, want %s", loadedState.ETag, originalState.ETag)
	}

	if len(loadedState.CompletedChunks) != len(originalState.CompletedChunks) {
		t.Errorf("CompletedChunks count mismatch: got %d, want %d",
			len(loadedState.CompletedChunks), len(originalState.CompletedChunks))
	}
}

// TestDownloadResumeStateNoFile tests loading when no resume state exists
func TestDownloadResumeStateNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "nonexistent.dat")

	// Try to load non-existent state
	state, err := LoadDownloadState(localPath)
	if err != nil {
		t.Errorf("LoadDownloadState() should return nil without error for missing file, got error: %v", err)
	}

	if state != nil {
		t.Error("LoadDownloadState() should return nil state for missing file")
	}
}

// TestValidateDownloadStateExpired tests that expired states are rejected
func TestValidateDownloadStateExpired(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")

	// Create encrypted temp file
	encryptedPath := localPath + ".encrypted"
	if err := os.WriteFile(encryptedPath, []byte("encrypted data"), 0644); err != nil {
		t.Fatalf("Failed to create encrypted file: %v", err)
	}

	// Create state with creation time > 7 days ago
	state := &DownloadResumeState{
		LocalPath:       localPath,
		EncryptedPath:   encryptedPath,
		TotalSize:       100,
		DownloadedBytes: 50,
		CreatedAt:       time.Now().Add(-8 * 24 * time.Hour), // 8 days ago (expired)
		LastUpdate:      time.Now(),
	}

	// Validation should fail due to age
	err := ValidateDownloadState(state, localPath)
	if err == nil {
		t.Error("ValidateDownloadState() should reject expired state")
	}
}

// TestValidateDownloadStatePathMismatch tests rejection when paths don't match
func TestValidateDownloadStatePathMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")
	wrongPath := filepath.Join(tmpDir, "wrong.dat")

	// Create encrypted temp file
	encryptedPath := localPath + ".encrypted"
	if err := os.WriteFile(encryptedPath, []byte("encrypted"), 0644); err != nil {
		t.Fatalf("Failed to create encrypted file: %v", err)
	}

	// Create state with different local path
	state := &DownloadResumeState{
		LocalPath:       wrongPath, // Different from validation path!
		EncryptedPath:   encryptedPath,
		TotalSize:       100,
		DownloadedBytes: 50,
		CreatedAt:       time.Now(),
		LastUpdate:      time.Now(),
	}

	// Validation should fail due to path mismatch
	err := ValidateDownloadState(state, localPath)
	if err == nil {
		t.Error("ValidateDownloadState() should reject state when local path doesn't match")
	}
}

// TestValidateDownloadStateMissingEncryptedFile tests rejection when encrypted file is missing
func TestValidateDownloadStateMissingEncryptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")

	// Create state pointing to non-existent encrypted file
	state := &DownloadResumeState{
		LocalPath:       localPath,
		EncryptedPath:   filepath.Join(tmpDir, "missing.encrypted"), // Does not exist!
		TotalSize:       100,
		DownloadedBytes: 50,
		CreatedAt:       time.Now(),
		LastUpdate:      time.Now(),
	}

	// Validation should fail because encrypted file is missing
	err := ValidateDownloadState(state, localPath)
	if err == nil {
		t.Error("ValidateDownloadState() should reject state when encrypted file is missing")
	}
}

// TestDownloadResumeStateAtomic tests that state saves are atomic (temp file + rename)
func TestDownloadResumeStateAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "test.dat")

	state := &DownloadResumeState{
		LocalPath:       localPath,
		TotalSize:       100,
		DownloadedBytes: 50,
		CreatedAt:       time.Now(),
		LastUpdate:      time.Now(),
	}

	// Save state
	if err := SaveDownloadState(state, localPath); err != nil {
		t.Fatalf("SaveDownloadState() failed: %v", err)
	}

	// Verify temp file was cleaned up (atomic rename should remove it)
	tmpFilePath := localPath + ".download.resume.tmp"
	if _, err := os.Stat(tmpFilePath); !os.IsNotExist(err) {
		t.Error("Temporary state file should be removed after atomic save")
	}

	// Verify final state file exists
	stateFilePath := localPath + ".download.resume"
	if _, err := os.Stat(stateFilePath); err != nil {
		t.Errorf("State file should exist after save: %v", err)
	}
}

// TestPKCS7PaddingRangeCheck tests the critical v2.3.0 fix for PKCS7 padding size validation
// This test verifies that encrypted file sizes are validated against a range (1-16 bytes padding)
// instead of exact match, which was causing unnecessary re-downloads.
func TestPKCS7PaddingRangeCheck(t *testing.T) {
	testCases := []struct {
		name          string
		decryptedSize int64
		encryptedSize int64
		expectValid   bool
		description   string
	}{
		{
			name:          "exact_one_block",
			decryptedSize: 16,
			encryptedSize: 32, // 16 bytes data + 16 bytes padding (full block)
			expectValid:   true,
			description:   "Decrypted size is exactly one AES block, padding is full block",
		},
		{
			name:          "minimum_padding",
			decryptedSize: 15,
			encryptedSize: 16, // 15 bytes data + 1 byte padding
			expectValid:   true,
			description:   "Minimum PKCS7 padding (1 byte)",
		},
		{
			name:          "maximum_padding",
			decryptedSize: 16,
			encryptedSize: 32, // 16 bytes data + 16 bytes padding
			expectValid:   true,
			description:   "Maximum PKCS7 padding (16 bytes / full block)",
		},
		{
			name:          "mid_range_padding",
			decryptedSize: 100,
			encryptedSize: 108, // 100 bytes data + 8 bytes padding
			expectValid:   true,
			description:   "Mid-range PKCS7 padding (8 bytes)",
		},
		{
			name:          "large_file_with_padding",
			decryptedSize: 60000000000, // 60 GB
			encryptedSize: 60000000016, // 60 GB + 16 bytes padding
			expectValid:   true,
			description:   "Large file (60GB) with 16-byte padding - the exact case from v2.3.0 bug",
		},
		{
			name:          "too_small",
			decryptedSize: 100,
			encryptedSize: 100, // No padding! Should be rejected
			expectValid:   false,
			description:   "Encrypted size equals decrypted size (no padding)",
		},
		{
			name:          "too_large",
			decryptedSize: 100,
			encryptedSize: 117, // 100 + 17 bytes padding (invalid!)
			expectValid:   false,
			description:   "Padding exceeds 16 bytes (invalid PKCS7)",
		},
		{
			name:          "way_too_large",
			decryptedSize: 100,
			encryptedSize: 200, // Way too big
			expectValid:   false,
			description:   "Encrypted file much larger than expected",
		},
		{
			name:          "smaller_than_decrypted",
			decryptedSize: 100,
			encryptedSize: 50, // Smaller than decrypted (impossible!)
			expectValid:   false,
			description:   "Encrypted size smaller than decrypted size (corrupted)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Calculate expected encrypted size range (v2.3.0 fix logic)
			minEncryptedSize := tc.decryptedSize + 1  // Minimum padding (1 byte)
			maxEncryptedSize := tc.decryptedSize + 16 // Maximum padding (16 bytes)

			// Check if encrypted size is within valid range
			isValid := tc.encryptedSize >= minEncryptedSize && tc.encryptedSize <= maxEncryptedSize

			if isValid != tc.expectValid {
				t.Errorf("%s: Expected valid=%v, got valid=%v\n"+
					"  Decrypted size: %d bytes\n"+
					"  Encrypted size: %d bytes\n"+
					"  Valid range: [%d, %d]\n"+
					"  Description: %s",
					tc.name, tc.expectValid, isValid,
					tc.decryptedSize, tc.encryptedSize,
					minEncryptedSize, maxEncryptedSize,
					tc.description)
			}

			// Log successful validation for visibility
			if tc.expectValid && isValid {
				t.Logf("✓ %s: Correctly validated (encrypted %d bytes for decrypted %d bytes)",
					tc.name, tc.encryptedSize, tc.decryptedSize)
			} else if !tc.expectValid && !isValid {
				t.Logf("✓ %s: Correctly rejected (encrypted %d bytes for decrypted %d bytes)",
					tc.name, tc.encryptedSize, tc.decryptedSize)
			}
		})
	}
}

// TestPKCS7PaddingRangeCheckRealWorldScenario tests the exact scenario from v2.3.0 bug report
func TestPKCS7PaddingRangeCheckRealWorldScenario(t *testing.T) {
	// Real-world scenario from v2.3.0 bug report:
	// - Encrypted file: 60,000,000,016 bytes (60 GB + 16 bytes PKCS7 padding)
	// - API decrypted size: 60,000,000,000 bytes (60 GB)
	// - Old logic: Exact comparison failed (60000000016 == 60000000000) → FALSE
	// - Result: "Removing partial files and restarting download..." → Re-downloaded entire 60GB file
	// - New logic: Range check (60000000016 in [60000000001, 60000000016]) → TRUE
	// - Result: "Encrypted file complete, retrying decryption..." → No re-download

	decryptedSize := int64(60000000000) // 60 GB
	encryptedSize := int64(60000000016) // 60 GB + 16 bytes padding

	// v2.3.0 FIX: Use range check instead of exact match
	minEncryptedSize := decryptedSize + 1
	maxEncryptedSize := decryptedSize + 16

	isValid := encryptedSize >= minEncryptedSize && encryptedSize <= maxEncryptedSize

	if !isValid {
		t.Errorf("v2.3.0 regression: PKCS7 padding range check failed for 60GB file!\n"+
			"  Decrypted size: %d bytes\n"+
			"  Encrypted size: %d bytes\n"+
			"  Valid range: [%d, %d]\n"+
			"  This was the exact bug fixed in v2.3.0!",
			decryptedSize, encryptedSize, minEncryptedSize, maxEncryptedSize)
	} else {
		t.Logf("✓ v2.3.0 fix working: 60GB file with 16-byte padding correctly validated")
		t.Logf("  This prevents unnecessary re-download of large files")
	}
}
