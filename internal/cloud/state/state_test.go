// Package state tests
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestUploadState_FilePermissions verifies that upload state files are created with secure permissions (0600).
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
		IV:            "dGVzdC1pdi1iYXNlNjQ=",                 // Simulated base64 IV
		MasterKey:     "dGVzdC1tYXN0ZXIta2V5LWJhc2U2NA==",     // Simulated base64 master key
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
		StreamingFileId: "dGVzdC1maWxlLWlk",                 // Simulated base64 file ID
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

// =============================================================================
// Upload lock ownership (F9)
// =============================================================================

// withProcessLiveness swaps the liveness probe for the duration of a test, so a
// lock can be owned by a PID that is definitely alive or definitely gone
// without the test having to find real ones.
func withProcessLiveness(t *testing.T, probe func(int) bool) {
	t.Helper()
	previous := isProcessRunning
	isProcessRunning = probe
	t.Cleanup(func() { isProcessRunning = previous })
}

// writeLockFile plants a lock file the way another owner would have left it.
func writeLockFile(t *testing.T, localPath string, lock uploadLockState) {
	t.Helper()
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		t.Fatalf("marshal lock: %v", err)
	}
	if err := os.WriteFile(localPath+".upload.lock", data, 0600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}
}

func readLockFile(t *testing.T, localPath string) uploadLockState {
	t.Helper()
	data, err := os.ReadFile(localPath + ".upload.lock")
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	var lock uploadLockState
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("unmarshal lock file: %v", err)
	}
	return lock
}

// TestAcquireUploadLock_ConcurrentAcquirersGetExactlyOne is the race the
// check-then-write acquisition lost: every acquirer saw no lock, then each
// wrote and renamed its own over the others'. Two transfers that both believe
// they own the file share one resume-state path, and the later one aborts the
// earlier one's upload as stale.
func TestAcquireUploadLock_ConcurrentAcquirersGetExactlyOne(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "testfile.bin")

	const acquirers = 8
	var wg sync.WaitGroup
	locks := make([]*UploadLock, acquirers)
	errs := make([]error, acquirers)
	start := make(chan struct{})

	wg.Add(acquirers)
	for i := 0; i < acquirers; i++ {
		go func(idx int) {
			defer wg.Done()
			<-start
			locks[idx], errs[idx] = AcquireUploadLock(localPath)
		}(i)
	}
	close(start)
	wg.Wait()

	granted := 0
	for i := range locks {
		if errs[i] == nil && locks[i] != nil {
			granted++
		}
	}
	if granted != 1 {
		t.Fatalf("%d of %d acquirers got the lock, want exactly 1", granted, acquirers)
	}

	// The winner's lock file must survive the losers: an acquirer that clears
	// the lock before writing its own leaves the holder owning nothing.
	if got := readLockFile(t, localPath); got.ProcessID != os.Getpid() || got.OwnerToken != processLockToken {
		t.Errorf("lock file names PID %d token %q, want this process", got.ProcessID, got.OwnerToken)
	}

	for i := range locks {
		if errs[i] == nil {
			ReleaseUploadLock(locks[i])
		}
	}
}

// TestAcquireUploadLock_RefusesSecondTransferInSameProcess covers the explicit
// same-PID bypass: two transfers of one file inside one process were both
// allowed to own its lock.
func TestAcquireUploadLock_RefusesSecondTransferInSameProcess(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "testfile.bin")

	first, err := AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("first AcquireUploadLock failed: %v", err)
	}
	defer ReleaseUploadLock(first)

	second, err := AcquireUploadLock(localPath)
	if err == nil {
		ReleaseUploadLock(second)
		t.Fatal("a second transfer in this process acquired the same lock")
	}
}

// TestAcquireUploadLock_KeepsLiveOwnerRegardlessOfAge pins the age rule: an
// upload that has held its lock for longer than the old 30-minute staleness
// window is still running, and a large file routinely takes longer than that.
func TestAcquireUploadLock_KeepsLiveOwnerRegardlessOfAge(t *testing.T) {
	const ownerPID = 424242
	withProcessLiveness(t, func(pid int) bool { return pid == ownerPID })

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  ownerPID,
		OwnerToken: "owner-of-a-running-upload",
		AcquiredAt: time.Now().Add(-2 * time.Hour),
		LocalPath:  localPath,
	})

	lock, err := AcquireUploadLock(localPath)
	if err == nil {
		ReleaseUploadLock(lock)
		t.Fatal("took the lock from an owner that is still running")
	}

	if got := readLockFile(t, localPath); got.ProcessID != ownerPID {
		t.Errorf("lock file now names PID %d, want the live owner %d", got.ProcessID, ownerPID)
	}
}

// TestAcquireUploadLock_TakesOverLockOfDeadOwner is the other half: a lock left
// behind by a process that died must not block the retry, however recently it
// was written.
func TestAcquireUploadLock_TakesOverLockOfDeadOwner(t *testing.T) {
	const deadPID = 424243
	withProcessLiveness(t, func(pid int) bool { return pid != deadPID })

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  deadPID,
		OwnerToken: "owner-that-crashed",
		AcquiredAt: time.Now(),
		LocalPath:  localPath,
	})

	lock, err := AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("refused a lock whose owner is gone: %v", err)
	}
	defer ReleaseUploadLock(lock)

	if got := readLockFile(t, localPath); got.ProcessID != os.Getpid() {
		t.Errorf("lock file names PID %d, want this process (%d)", got.ProcessID, os.Getpid())
	}
}

// TestValidateDownloadStateRejectsClaimsPastEOF covers the half of the sidecar
// check that was missing. Validation rejected a partial file LARGER than the
// object, but accepted one smaller than the bytes the sidecar claimed were in
// it — the shape a crash leaves when the state update outlives the data, or
// when the partial file is truncated between attempts while its sidecar
// survives. Resuming from such a state skips ranges that are not on disk, and
// the pre-allocation puts zeros in their place: a hole that only a checksum
// would ever catch, and files without one carry no such check.
func TestValidateDownloadStateRejectsClaimsPastEOF(t *testing.T) {
	const chunkSize = int64(8)
	const totalSize = int64(32) // four chunks

	tests := []struct {
		name       string
		fileSize   int64
		chunks     []int64
		ranges     []ByteRange
		wantReject bool
	}{
		{
			name:     "claims are covered by the file",
			fileSize: totalSize,
			chunks:   []int64{0, 1},
		},
		{
			name:       "a chunk claim reaches past the end of the file",
			fileSize:   chunkSize, // only chunk 0 could be in here
			chunks:     []int64{0, 1},
			wantReject: true,
		},
		{
			name:       "a byte range claim reaches past the end of the file",
			fileSize:   chunkSize,
			ranges:     []ByteRange{{Start: 0, End: 24}},
			wantReject: true,
		},
		{
			name:     "an empty claim needs nothing on disk",
			fileSize: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localPath := filepath.Join(t.TempDir(), "results.dat")
			if err := os.WriteFile(localPath, make([]byte, tt.fileSize), 0600); err != nil {
				t.Fatalf("seed partial file: %v", err)
			}

			now := time.Now()
			st := &DownloadResumeState{
				LocalPath:       localPath,
				EncryptedPath:   localPath,
				RemotePath:      "bucket/results.dat",
				TotalSize:       totalSize,
				ChunkSize:       chunkSize,
				CompletedChunks: tt.chunks,
				CompletedRanges: tt.ranges,
				CreatedAt:       now,
				LastUpdate:      now,
			}

			err := ValidateDownloadState(st, localPath)
			if tt.wantReject && err == nil {
				t.Fatalf("state claiming bytes the %d-byte file does not hold was accepted", tt.fileSize)
			}
			if !tt.wantReject && err != nil {
				t.Fatalf("ValidateDownloadState: %v", err)
			}
		})
	}
}
