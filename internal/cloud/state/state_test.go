// Package state tests
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
)

// TestMain keeps the guards these tests create out of the state directory of
// whoever is running them.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "upload-lock-guards-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create a guard directory for the tests: %v\n", err)
		os.Exit(1)
	}
	guardDirectory = func() (string, error) { return dir, nil }
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// withGuardDirectory points the guards at a directory of the test's choosing.
func withGuardDirectory(t *testing.T, dir string) {
	t.Helper()
	previous := guardDirectory
	guardDirectory = func() (string, error) { return dir, nil }
	t.Cleanup(func() { guardDirectory = previous })
}

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

// acquireAs acquires the on-disk lock as an owner this process is not, which is
// how a test stands in for a second process: AcquireUploadLock would be stopped
// by the in-process claim long before the file is consulted.
func acquireAs(localPath string, pid int, token string) (*UploadLock, error) {
	return acquireLockFile(localPath+".upload.lock", localPath, uploadLockState{
		ProcessID:  pid,
		OwnerToken: token,
		AcquiredAt: time.Now(),
		LocalPath:  localPath,
	})
}

type lockOutcome struct {
	lock *UploadLock
	err  error
}

// parkTakeoverAt runs park when the named owner reaches a phase of the takeover,
// so a test can decide what another acquirer does inside that window.
func parkTakeoverAt(t *testing.T, phase, token string, park func()) {
	t.Helper()
	lockTakeoverStep = func(atPhase string, owner uploadLockState) {
		if atPhase == phase && owner.OwnerToken == token {
			park()
		}
	}
	t.Cleanup(func() { lockTakeoverStep = nil })
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

// TestAcquireUploadLock_TakeoverCannotEvictTheWinner is the cross-process
// takeover race. Two acquirers read the same abandoned record; the first clears
// it and starts its upload; the second then reaches the clearing step it had
// already decided on and removes the lock the first one is holding. Both own
// the same resume state, and the later one aborts the earlier one's multipart
// upload as stale. The acquirers are driven below AcquireUploadLock because the
// in-process claim is what stands in for the second process.
func TestAcquireUploadLock_TakeoverCannotEvictTheWinner(t *testing.T) {
	const deadPID = 424244
	withProcessLiveness(t, func(pid int) bool { return pid != deadPID })

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	if err := os.WriteFile(localPath, []byte("x"), 0600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  deadPID,
		OwnerToken: "owner-that-crashed",
		AcquiredAt: time.Now(),
		LocalPath:  localPath,
	})

	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	parkTakeoverAt(t, takeoverJudged, "late-taker", func() {
		once.Do(func() {
			close(parked)
			<-release
		})
	})

	lateDone := make(chan lockOutcome, 1)
	go func() {
		lock, err := acquireAs(localPath, 900002, "late-taker")
		lateDone <- lockOutcome{lock, err}
	}()

	<-parked

	early, err := acquireAs(localPath, 900001, "early-taker")
	if err != nil {
		t.Fatalf("the first acquirer could not clear the abandoned lock: %v", err)
	}
	close(release)

	late := <-lateDone
	if late.err == nil {
		t.Errorf("both acquirers own the lock: the late one took it from PID %d", early.ProcessID)
	} else if !strings.Contains(late.err.Error(), "another process") {
		t.Errorf("the late acquirer's refusal %q does not name the live owner", late.err)
	}
	if got := readLockFile(t, localPath); got.OwnerToken != "early-taker" {
		t.Errorf("lock file names owner %q, want the acquirer that won it", got.OwnerToken)
	}
}

// plantAbandonedLock leaves a lock file whose owner has died, which is the one
// state an acquirer is allowed to take over.
func plantAbandonedLock(t *testing.T, deadPID int) string {
	t.Helper()
	withProcessLiveness(t, func(pid int) bool { return pid != deadPID })

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	if err := os.WriteFile(localPath, []byte("x"), 0600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  deadPID,
		OwnerToken: "owner-that-crashed",
		AcquiredAt: time.Now(),
		LocalPath:  localPath,
	})
	return localPath
}

// TestAcquireUploadLock_TakeoverCannotStrandTwoOwners is the three-party
// takeover sequence. One acquirer clears the dead owner and installs its own
// lock; a second, still holding the judgement it made of that dead record,
// reaches the clearing step it had already decided on and frees the pathname —
// and a third creates the lock while it is free. Whatever the second one then
// puts back lands on top of the third's lock, and two acquirers are left
// believing they own the same upload and the same resume state.
func TestAcquireUploadLock_TakeoverCannotStrandTwoOwners(t *testing.T) {
	localPath := plantAbandonedLock(t, 424245)

	judged := make(chan struct{})
	release := make(chan struct{})
	gapDone := make(chan lockOutcome, 1)
	var judgedOnce, gapOnce sync.Once
	// The acquirer that creates the lock while the pathname is free. It runs
	// inside the clearing window when there is one, and after it otherwise.
	fillTheGap := func() {
		gapOnce.Do(func() {
			lock, err := acquireAs(localPath, 900003, "gap-filler")
			gapDone <- lockOutcome{lock, err}
		})
	}
	lockTakeoverStep = func(phase string, owner uploadLockState) {
		if owner.OwnerToken != "second-taker" {
			return
		}
		switch phase {
		case takeoverJudged:
			judgedOnce.Do(func() {
				close(judged)
				<-release
			})
		case takeoverCleared:
			fillTheGap()
		}
	}
	t.Cleanup(func() { lockTakeoverStep = nil })

	secondDone := make(chan lockOutcome, 1)
	go func() {
		lock, err := acquireAs(localPath, 900002, "second-taker")
		secondDone <- lockOutcome{lock, err}
	}()
	<-judged

	first := lockOutcome{}
	first.lock, first.err = acquireAs(localPath, 900001, "first-taker")
	if first.err != nil {
		t.Fatalf("the first acquirer could not take over the abandoned lock: %v", first.err)
	}
	close(release)
	second := <-secondDone
	fillTheGap()
	gap := <-gapDone

	owners := map[string]bool{}
	for token, got := range map[string]lockOutcome{"first-taker": first, "second-taker": second, "gap-filler": gap} {
		if got.err == nil && got.lock != nil {
			owners[token] = true
		}
	}
	if len(owners) != 1 {
		t.Fatalf("%d acquirers own the upload (%v), want exactly 1", len(owners), owners)
	}
	if got := readLockFile(t, localPath); !owners[got.OwnerToken] {
		t.Errorf("lock file names owner %q, which is not the acquirer that was granted the lock (%v)", got.OwnerToken, owners)
	}
}

// TestAcquireUploadLock_DoesNotTakeOverAnUnwrittenLock is the pre-write half of
// the same race. The acquirer that took the abandoned lock over has created its
// replacement file with O_EXCL but has not written the record into it yet. A
// second acquirer, still holding its judgement of the dead owner, must not read
// that empty file as one more thing to clear: clearing it unlinks a lock whose
// owner goes on writing into a file that is no longer at the path, and reports
// success — while the pathname it no longer holds is free to be taken.
func TestAcquireUploadLock_DoesNotTakeOverAnUnwrittenLock(t *testing.T) {
	localPath := plantAbandonedLock(t, 424246)
	lockFilePath := localPath + ".upload.lock"

	judged := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	parkTakeoverAt(t, takeoverJudged, "late-taker", func() {
		once.Do(func() {
			close(judged)
			<-release
		})
	})

	lateDone := make(chan lockOutcome, 1)
	go func() {
		lock, err := acquireAs(localPath, 900002, "late-taker")
		lateDone <- lockOutcome{lock, err}
	}()
	<-judged

	// The winner's replacement file: created, not yet written. It replaces the
	// abandoned lock rather than truncating it, which is what an acquirer that
	// clears a pathname and creates a lock there actually leaves behind.
	if err := os.Remove(lockFilePath); err != nil {
		t.Fatalf("clear the abandoned lock: %v", err)
	}
	plantOwnerlessLock(t, lockFilePath, time.Now())
	close(release)

	late := <-lateDone
	if late.err == nil {
		ReleaseUploadLock(late.lock)
		t.Fatal("an acquirer took over a lock file whose owner had not written it yet")
	}
	if _, err := os.Stat(lockFilePath); err != nil {
		t.Errorf("the unwritten lock was cleared from under its owner: %v", err)
	}
}

// TestAcquireUploadLock_DoesNotClearALockThatReplacedTheJudgedOne is the
// identity half of the takeover. A lock whose writer died before recording who
// it is, past the grace window, is judged abandoned; before the taker acts, that
// file is released and another acquirer creates its own lock at the pathname and
// has not written it yet. Both files are empty, so equal bytes say the record is
// unchanged — and the taker deletes a lock whose owner is alive and goes on
// writing into a file that is no longer at the path.
func TestAcquireUploadLock_DoesNotClearALockThatReplacedTheJudgedOne(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	lockFilePath := localPath + ".upload.lock"
	plantOwnerlessLock(t, lockFilePath, time.Now().Add(-time.Hour))

	var once sync.Once
	var replacement os.FileInfo
	parkTakeoverAt(t, takeoverJudged, "taker", func() {
		once.Do(func() {
			// The owner-less file is released and a fresh lock, not yet written,
			// takes the pathname: a different file with the same bytes.
			if err := os.Remove(lockFilePath); err != nil {
				t.Errorf("release the owner-less lock: %v", err)
				return
			}
			plantOwnerlessLock(t, lockFilePath, time.Now())
			var err error
			if replacement, err = os.Stat(lockFilePath); err != nil {
				t.Errorf("stat the replacement lock: %v", err)
			}
		})
	})

	taker, err := acquireAs(localPath, 900001, "taker")
	if err == nil {
		ReleaseUploadLock(taker)
		t.Fatal("the taker owns a lock that had replaced the one it judged abandoned")
	}

	current, statErr := os.Stat(lockFilePath)
	if statErr != nil {
		t.Fatalf("the replacement lock was cleared from under its owner: %v", statErr)
	}
	if !os.SameFile(replacement, current) {
		t.Error("the file at the lock pathname is not the replacement the taker had to leave alone")
	}
}

// plantOwnerlessLock leaves a lock file its writer never recorded an owner in,
// dated so the caller can decide whether it is still inside the grace window.
func plantOwnerlessLock(t *testing.T, lockFilePath string, written time.Time) {
	t.Helper()
	if err := os.WriteFile(lockFilePath, nil, 0600); err != nil {
		t.Fatalf("plant an owner-less lock: %v", err)
	}
	if err := os.Chtimes(lockFilePath, written, written); err != nil {
		t.Fatalf("date the owner-less lock: %v", err)
	}
}

// TestAcquireUploadLock_ProceedsPastTheGuardOfACrashedTaker covers what a taker
// that dies mid-reclamation leaves behind. Every file-based exclusion this
// protocol tried before needed a rule for clearing its own leftovers, and the
// leftover then blocked the next taker until that rule fired. The OS drops the
// guard's lock when the holder's descriptors close — which is what dying does —
// so the file it leaves holds nothing back.
func TestAcquireUploadLock_ProceedsPastTheGuardOfACrashedTaker(t *testing.T) {
	localPath := plantAbandonedLock(t, 424247)
	guardPath, err := guardPathFor(lockFilePathFor(localPath))
	if err != nil {
		t.Fatalf("name the guard: %v", err)
	}

	crashed, err := os.OpenFile(guardPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open the crashed taker's guard: %v", err)
	}
	if err := lockGuardFile(crashed); err != nil {
		t.Fatalf("the crashed taker could not take its guard: %v", err)
	}
	// Its descriptors close, as they do for any process that ends. Nothing
	// removes the file.
	if err := crashed.Close(); err != nil {
		t.Fatalf("close the crashed taker's guard: %v", err)
	}

	lock, err := AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("a guard its holder died on still blocks the lock: %v", err)
	}
	defer ReleaseUploadLock(lock)

	if got := readLockFile(t, localPath); got.ProcessID != os.Getpid() {
		t.Errorf("lock file names PID %d, want this process (%d)", got.ProcessID, os.Getpid())
	}
}

// TestAcquireUploadLock_RefusesReclamationWithoutAnOSLock pins the interim
// answer on a filesystem that carries no lock at all. Creating a lock still
// excludes everyone — O_EXCL is the filesystem's own guarantee — but clearing
// one cannot be made exclusive, and clearing it anyway is how two owners appear.
func TestAcquireUploadLock_RefusesReclamationWithoutAnOSLock(t *testing.T) {
	withoutGuardLock := func(t *testing.T) {
		t.Helper()
		previous := lockGuard
		lockGuard = func(*os.File) error { return errGuardLockUnsupported }
		t.Cleanup(func() { lockGuard = previous })
	}

	t.Run("creating a lock still works", func(t *testing.T) {
		withoutGuardLock(t)
		localPath := filepath.Join(t.TempDir(), "testfile.bin")

		lock, err := AcquireUploadLock(localPath)
		if err != nil {
			t.Fatalf("refused a lock nothing else holds: %v", err)
		}
		defer ReleaseUploadLock(lock)

		if got := readLockFile(t, localPath); got.ProcessID != os.Getpid() {
			t.Errorf("lock file names PID %d, want this process (%d)", got.ProcessID, os.Getpid())
		}
	})

	t.Run("reclaiming an abandoned lock is refused", func(t *testing.T) {
		localPath := plantAbandonedLock(t, 424251)
		lockFilePath := localPath + ".upload.lock"
		withoutGuardLock(t)

		lock, err := AcquireUploadLock(localPath)
		if err == nil {
			ReleaseUploadLock(lock)
			t.Fatal("cleared an abandoned lock on a filesystem that cannot serialize clearing it")
		}
		if errors.Is(err, ErrUploadLockUnavailable) {
			t.Errorf("a lock that exists is reported as no lock at all: %v", err)
		}
		if !strings.Contains(err.Error(), lockFilePath) {
			t.Errorf("the refusal %q does not name the file to delete (%s)", err, lockFilePath)
		}
		if got := readLockFile(t, localPath); got.OwnerToken != "owner-that-crashed" {
			t.Errorf("the abandoned lock was cleared anyway; it now names %q", got.OwnerToken)
		}
	})
}

// TestAcquireUploadLock_SerializesTakersOfOneAbandonedLock pins the rule the
// guard exists for: several acquirers can judge one dead record abandoned, and
// only one of them may act on that judgement.
func TestAcquireUploadLock_SerializesTakersOfOneAbandonedLock(t *testing.T) {
	t.Run("a taker that is clearing the lock excludes the others", func(t *testing.T) {
		localPath := plantAbandonedLock(t, 424248)

		guarded := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		parkTakeoverAt(t, takeoverGuarded, "holder", func() {
			once.Do(func() {
				close(guarded)
				<-release
			})
		})

		holderDone := make(chan lockOutcome, 1)
		go func() {
			lock, err := acquireAs(localPath, 900001, "holder")
			holderDone <- lockOutcome{lock, err}
		}()
		<-guarded

		// The other taker has judged the same record and reaches for the guard.
		// It must get no further while the holder is inside it: the judgement it
		// is carrying is exactly what stops being true in there.
		otherDone := make(chan lockOutcome, 1)
		go func() {
			lock, err := acquireAs(localPath, 900002, "other-taker")
			otherDone <- lockOutcome{lock, err}
		}()
		select {
		case got := <-otherDone:
			if got.err == nil {
				ReleaseUploadLock(got.lock)
			}
			t.Fatalf("a second taker got through the guard while it was held: %v", got.err)
		case <-time.After(100 * time.Millisecond):
		}
		close(release)

		holder := <-holderDone
		if holder.err != nil {
			t.Fatalf("the taker holding the guard did not get the lock: %v", holder.err)
		}
		other := <-otherDone
		if other.err == nil {
			ReleaseUploadLock(other.lock)
			t.Fatal("both takers own the upload")
		}
		if !strings.Contains(other.err.Error(), "another process") {
			t.Errorf("the second taker's refusal %q does not name the live owner", other.err)
		}
		if got := readLockFile(t, localPath); got.OwnerToken != "holder" {
			t.Errorf("lock file names owner %q, want the taker that held the guard", got.OwnerToken)
		}
	})

	t.Run("a lock created while the taker was clearing it wins", func(t *testing.T) {
		localPath := plantAbandonedLock(t, 424249)

		var once sync.Once
		gap := lockOutcome{}
		parkTakeoverAt(t, takeoverCleared, "taker", func() {
			once.Do(func() { gap.lock, gap.err = acquireAs(localPath, 900002, "gap-filler") })
		})

		taker, err := acquireAs(localPath, 900001, "taker")
		if err == nil {
			ReleaseUploadLock(taker)
			t.Fatal("the taker owns a lock another acquirer created while it was clearing the pathname")
		}
		if gap.err != nil {
			t.Fatalf("the acquirer that found the pathname free did not get the lock: %v", gap.err)
		}
		if got := readLockFile(t, localPath); got.OwnerToken != "gap-filler" {
			t.Errorf("lock file names owner %q, want the acquirer that created it", got.OwnerToken)
		}
	})

	t.Run("concurrent takers", func(t *testing.T) {
		localPath := plantAbandonedLock(t, 424249)

		const takers = 8
		var wg sync.WaitGroup
		results := make([]lockOutcome, takers)
		start := make(chan struct{})
		wg.Add(takers)
		for i := 0; i < takers; i++ {
			go func(idx int) {
				defer wg.Done()
				<-start
				lock, err := acquireAs(localPath, 900100+idx, fmt.Sprintf("taker-%d", idx))
				results[idx] = lockOutcome{lock, err}
			}(i)
		}
		close(start)
		wg.Wait()

		granted := ""
		for _, got := range results {
			if got.err == nil && got.lock != nil {
				if granted != "" {
					t.Fatalf("two takers own the abandoned lock: %q and %q", granted, got.lock.OwnerToken)
				}
				granted = got.lock.OwnerToken
			}
		}
		if granted == "" {
			t.Fatal("no taker reclaimed a lock whose owner is gone")
		}
		if got := readLockFile(t, localPath); got.OwnerToken != granted {
			t.Errorf("lock file names owner %q, want the taker that was granted the lock %q", got.OwnerToken, granted)
		}
	})
}

// TestAcquireUploadLock_UnavailableOnlyWhenNoLockCanBeCreated pins what the
// caller is told apart. Reporting an unavailable lock means nothing holds this
// upload and the caller may run without one; that is only true when there is no
// lock file and the directory refuses to take one. A lock file that exists but
// cannot be inspected may have a live owner behind it, and reading it as "no
// lock at all" is what puts two transfers on one upload.
func TestAcquireUploadLock_UnavailableOnlyWhenNoLockCanBeCreated(t *testing.T) {
	t.Run("no lock and a directory that refuses one", func(t *testing.T) {
		dir := denyingDirectory(t)
		lock, err := AcquireUploadLock(filepath.Join(dir, "testfile.bin"))
		if err == nil {
			ReleaseUploadLock(lock)
			t.Fatal("acquired a lock in a directory that cannot hold one")
		}
		if !errors.Is(err, ErrUploadLockUnavailable) {
			t.Errorf("a directory that will not hold a lock reports %q, want an unavailable lock", err)
		}
	})

	t.Run("an existing lock whose guard cannot be created", func(t *testing.T) {
		localPath := plantAbandonedLock(t, 424252)
		withGuardDirectory(t, denyingDirectory(t))

		lock, err := AcquireUploadLock(localPath)
		if err == nil {
			ReleaseUploadLock(lock)
			t.Fatal("cleared an abandoned lock with nothing to guard the clearing")
		}
		// The lock file is right there. Reporting that nothing holds the upload
		// is what the sentinel means, and it is not true here.
		if errors.Is(err, ErrUploadLockUnavailable) {
			t.Errorf("an existing lock that cannot be guarded is reported as no lock at all: %v", err)
		}
		if !strings.Contains(err.Error(), "cannot clear the abandoned upload lock") {
			t.Errorf("the refusal %q is not the one that reaches for the guard", err)
		}
	})

	t.Run("an existing lock with no directory to guard it", func(t *testing.T) {
		localPath := plantAbandonedLock(t, 424254)
		// A state directory that cannot be created is refused the way a
		// filesystem with no lock is: the lock is still there, and only its
		// owner or a hand-deletion can release it.
		withGuardDirectory(t, filepath.Join(denyingDirectory(t), guardDirName))
		lockFilePath := localPath + ".upload.lock"

		lock, err := AcquireUploadLock(localPath)
		if err == nil {
			ReleaseUploadLock(lock)
			t.Fatal("cleared an abandoned lock with nowhere to keep the guard")
		}
		if errors.Is(err, ErrUploadLockUnavailable) {
			t.Errorf("an existing lock that cannot be guarded is reported as no lock at all: %v", err)
		}
		if !strings.Contains(err.Error(), lockFilePath) {
			t.Errorf("the refusal %q does not name the file to delete (%s)", err, lockFilePath)
		}
		if got := readLockFile(t, localPath); got.OwnerToken != "owner-that-crashed" {
			t.Errorf("the abandoned lock was cleared anyway; it now names %q", got.OwnerToken)
		}
	})

	t.Run("an existing lock that cannot be read", func(t *testing.T) {
		localPath := filepath.Join(t.TempDir(), "testfile.bin")
		// A lock file whose contents no reader can get at. Whether an owner is
		// behind it is exactly what cannot be established here.
		if err := os.Mkdir(localPath+".upload.lock", 0700); err != nil {
			t.Fatalf("plant an unreadable lock: %v", err)
		}

		lock, err := AcquireUploadLock(localPath)
		if err == nil {
			ReleaseUploadLock(lock)
			t.Fatal("acquired an upload lock that could not be inspected")
		}
		if errors.Is(err, ErrUploadLockUnavailable) {
			t.Errorf("an existing lock that cannot be inspected is reported as no lock at all: %v", err)
		}
	})
}

// denyingDirectory returns a directory that will not accept a new file.
func denyingDirectory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatalf("create read-only directory: %v", err)
	}
	denyNewFilesIn(t, dir)
	return dir
}

// denyNewFilesIn stops a directory accepting new files, whatever it already
// holds, so a test can stand in for a directory that will hold no guard.
func denyNewFilesIn(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not deny file creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores directory permissions")
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("deny new files in %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
}

// TestAcquireUploadLock_RefusesAliasOfHeldPath pins path identity. Exclusion
// keyed on the caller's spelling gives every alias of one file its own
// in-process key, and the two spellings then meet again on the one lock file
// they share — where the same-PID branch reads this process's own live lock and
// clears it. Two transfers of one file end up owning one resume state.
func TestAcquireUploadLock_RefusesAliasOfHeldPath(t *testing.T) {
	t.Run("relative spelling", func(t *testing.T) {
		dir := t.TempDir()
		localPath := filepath.Join(dir, "testfile.bin")
		if err := os.WriteFile(localPath, []byte("x"), 0600); err != nil {
			t.Fatalf("write source file: %v", err)
		}

		held, err := AcquireUploadLock(localPath)
		if err != nil {
			t.Fatalf("first AcquireUploadLock failed: %v", err)
		}
		defer ReleaseUploadLock(held)

		t.Chdir(dir)
		alias, err := AcquireUploadLock("testfile.bin")
		if err == nil {
			ReleaseUploadLock(alias)
			t.Fatal("a relative spelling of the same file acquired a second lock")
		}
	})

	t.Run("symlinked directory", func(t *testing.T) {
		dir := t.TempDir()
		realDir := filepath.Join(dir, "real")
		if err := os.Mkdir(realDir, 0700); err != nil {
			t.Fatalf("create directory: %v", err)
		}
		localPath := filepath.Join(realDir, "testfile.bin")
		if err := os.WriteFile(localPath, []byte("x"), 0600); err != nil {
			t.Fatalf("write source file: %v", err)
		}
		linkDir := filepath.Join(dir, "link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Skipf("this platform will not create a symlink: %v", err)
		}

		held, err := AcquireUploadLock(localPath)
		if err != nil {
			t.Fatalf("first AcquireUploadLock failed: %v", err)
		}
		defer ReleaseUploadLock(held)

		alias, err := AcquireUploadLock(filepath.Join(linkDir, "testfile.bin"))
		if err == nil {
			ReleaseUploadLock(alias)
			t.Fatal("a symlinked spelling of the same file acquired a second lock")
		}
	})
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

// =============================================================================
// Shipped-format compatibility
// =============================================================================

// The literals below are the sidecars the shipped release writes, field for
// field: the JSON names come from the struct tags at round4-base. Editing a
// state this version generated would not test the same thing, because the
// fields this version added would be there to remove rather than never written.
// Their substitution slots stand where a JSON string goes, quotes and all —
// see fillShippedFixture.

// fillShippedFixture substitutes values into a shipped-format fixture. Each one
// is JSON-encoded rather than dropped between quotes in the literal: the
// backslashes in a Windows path are escape sequences to a JSON reader, so a
// fixture built that way either stops parsing or carries a path that is not the
// one the test wrote.
func fillShippedFixture(t *testing.T, template string, values ...string) string {
	t.Helper()
	encoded := make([]any, len(values))
	for i, value := range values {
		quoted, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode fixture value %q: %v", value, err)
		}
		encoded[i] = string(quoted)
	}
	return fmt.Sprintf(template, encoded...)
}

const shippedPreEncryptUploadState = `{
  "local_path": %s,
  "encrypted_path": %s,
  "object_key": "uploads/testfile.bin-abc123",
  "upload_id": "shipped-upload-id",
  "total_size": 12,
  "original_size": 12,
  "uploaded_bytes": 4,
  "completed_parts": [
    {
      "part_number": 1,
      "etag": "shipped-etag"
    }
  ],
  "block_ids": null,
  "encryption_key": "dGVzdC1lbmNyeXB0aW9uLWtleQ==",
  "iv": "dGVzdC1pdg==",
  "random_suffix": "abc123",
  "created_at": %s,
  "last_update": %s,
  "storage_type": "S3Storage",
  "format_version": 0,
  "master_key": "",
  "file_id": "",
  "part_size": 0,
  "process_id": 4242,
  "lock_acquired_at": %s
}`

const shippedStreamingUploadState = `{
  "local_path": %s,
  "encrypted_path": "",
  "object_key": "uploads/testfile.bin-abc123",
  "upload_id": "shipped-upload-id",
  "total_size": 12,
  "original_size": 12,
  "uploaded_bytes": 4,
  "completed_parts": null,
  "block_ids": null,
  "encryption_key": "",
  "iv": "",
  "random_suffix": "abc123",
  "created_at": %s,
  "last_update": %s,
  "storage_type": "S3Storage",
  "format_version": 1,
  "master_key": "dGVzdC1tYXN0ZXIta2V5",
  "file_id": "dGVzdC1maWxlLWlk",
  "part_size": 1048576,
  "process_id": 4242,
  "lock_acquired_at": %s
}`

// TestSourceModTimeIsWrittenEvenWhenZero pins what an unset modification time
// actually costs on disk. encoding/json has no empty case for a struct, so the
// zero time.Time is always written out; what makes such a state unresumable is
// the loader's IsZero check, not a missing key.
func TestSourceModTimeIsWrittenEvenWhenZero(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "testfile.bin")
	if err := os.WriteFile(localPath, []byte("test content"), 0600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	if err := SaveUploadState(&UploadResumeState{LocalPath: localPath}, localPath); err != nil {
		t.Fatalf("SaveUploadState failed: %v", err)
	}
	raw, err := os.ReadFile(localPath + ".upload.resume")
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("the sidecar is not an object: %v", err)
	}

	value, present := fields["source_mod_time"]
	if !present {
		t.Fatal("source_mod_time is missing from the sidecar")
	}
	if string(value) != `"0001-01-01T00:00:00Z"` {
		t.Errorf("an unset source_mod_time is written as %s", value)
	}

	loaded, err := LoadUploadState(localPath)
	if err != nil || loaded == nil {
		t.Fatalf("LoadUploadState failed: %v", err)
	}
	if !loaded.SourceModTime.IsZero() {
		t.Error("the loader read a modification time back out of a state that carries none")
	}
}

// TestShippedUploadStateLoadsAndCarriesNoResumePoint pins what a sidecar from
// the shipped release means to this version. It has to parse — a state that
// failed to load would be reported as a corrupt file rather than a fresh upload
// — and it has to carry none of the fields a resume is decided on, so nothing
// downstream can reconstruct a part geometry or an encryption position from it.
func TestShippedUploadStateLoadsAndCarriesNoResumePoint(t *testing.T) {
	tests := []struct {
		name     string
		template string
		v1       bool
	}{
		{name: "pre-encrypt", template: shippedPreEncryptUploadState},
		{name: "streaming", template: shippedStreamingUploadState, v1: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			localPath := filepath.Join(dir, "testfile.bin")
			encryptedPath := filepath.Join(dir, "testfile.bin.encrypted")
			if err := os.WriteFile(localPath, []byte("test content"), 0600); err != nil {
				t.Fatalf("write source file: %v", err)
			}
			if err := os.WriteFile(encryptedPath, []byte("test content"), 0600); err != nil {
				t.Fatalf("write encrypted file: %v", err)
			}

			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			var shipped string
			if tt.v1 {
				shipped = fillShippedFixture(t, tt.template, localPath, stamp, stamp, stamp)
			} else {
				shipped = fillShippedFixture(t, tt.template, localPath, encryptedPath, stamp, stamp, stamp)
			}
			if err := os.WriteFile(localPath+".upload.resume", []byte(shipped), 0600); err != nil {
				t.Fatalf("write shipped state: %v", err)
			}

			loaded, err := LoadUploadState(localPath)
			if err != nil {
				t.Fatalf("a sidecar from the shipped release no longer loads: %v", err)
			}
			if loaded == nil {
				t.Fatal("LoadUploadState returned nil for an existing sidecar")
			}
			if loaded.ObjectKey != "uploads/testfile.bin-abc123" || loaded.UploadID != "shipped-upload-id" {
				t.Errorf("shipped fields did not survive the load: %+v", loaded)
			}
			if err := ValidateUploadState(loaded, localPath); err != nil {
				t.Errorf("a shipped sidecar is reported as unusable rather than unresumable: %v", err)
			}

			// Nothing here can place a resumed attempt.
			if !loaded.SourceModTime.IsZero() {
				t.Error("the shipped format carries no source modification time")
			}
			if !tt.v1 && loaded.PartSize != 0 {
				t.Error("the shipped pre-encrypt format records no part size")
			}
			if loaded.InitialIV != "" || loaded.ChainIV != "" || len(loaded.StreamingParts) != 0 {
				t.Errorf("the shipped format carries no chain position: initial_iv=%q chain_iv=%q parts=%d",
					loaded.InitialIV, loaded.ChainIV, len(loaded.StreamingParts))
			}
		})
	}
}

const shippedSequentialDownloadState = `{
  "local_path": %s,
  "encrypted_path": %s,
  "remote_path": "uploads/testfile.bin",
  "file_id": "file-123",
  "total_size": 32,
  "downloaded_bytes": 16,
  "etag": "shipped-etag",
  "created_at": %s,
  "last_update": %s,
  "storage_type": "S3Storage",
  "format_version": 0
}`

const shippedConcurrentDownloadState = `{
  "local_path": %s,
  "encrypted_path": %s,
  "remote_path": "uploads/testfile.bin",
  "file_id": "file-123",
  "total_size": 32,
  "downloaded_bytes": 16,
  "etag": "shipped-etag",
  "created_at": %s,
  "last_update": %s,
  "storage_type": "S3Storage",
  "chunk_size": 8,
  "completed_chunks": [0, 1],
  "format_version": 0
}`

// TestShippedDownloadSidecarStillValidates is the other half: the download
// sidecar gained no required field, so one written by the shipped release has to
// validate exactly as it did. The concurrent shape is the one at risk — it
// records completed chunks and no byte ranges, and the claim check this version
// added reads the chunk list.
func TestShippedDownloadSidecarStillValidates(t *testing.T) {
	tests := []struct {
		name          string
		template      string
		encryptedSize int
	}{
		// Sequential downloads write exactly what they claim.
		{name: "sequential", template: shippedSequentialDownloadState, encryptedSize: 16},
		// Concurrent downloads pre-allocate the whole file and fill it in.
		{name: "concurrent", template: shippedConcurrentDownloadState, encryptedSize: 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			localPath := filepath.Join(dir, "testfile.bin")
			encryptedPath := filepath.Join(dir, "testfile.bin.encrypted")
			if err := os.WriteFile(encryptedPath, make([]byte, tt.encryptedSize), 0600); err != nil {
				t.Fatalf("write encrypted file: %v", err)
			}

			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			shipped := fillShippedFixture(t, tt.template, localPath, encryptedPath, stamp, stamp)
			if err := os.WriteFile(localPath+".download.resume", []byte(shipped), 0600); err != nil {
				t.Fatalf("write shipped state: %v", err)
			}

			loaded, err := LoadDownloadState(localPath)
			if err != nil {
				t.Fatalf("a sidecar from the shipped release no longer loads: %v", err)
			}
			if loaded == nil {
				t.Fatal("LoadDownloadState returned nil for an existing sidecar")
			}
			if err := ValidateDownloadState(loaded, localPath); err != nil {
				t.Errorf("a shipped download sidecar no longer validates: %v", err)
			}
			if loaded.ETag != "shipped-etag" || loaded.DownloadedBytes != 16 {
				t.Errorf("shipped fields did not survive the load: %+v", loaded)
			}
		})
	}
}

// TestShippedFixturesCarryTheirPathsOnEveryPlatform pins the fixtures
// themselves rather than the loader. They are literal JSON with the test's paths
// substituted in, and on Windows those paths are full of backslashes: a fixture
// that drops them between quotes in the literal stops parsing, and the
// compatibility tests then fail for a reason that has nothing to do with
// compatibility.
func TestShippedFixturesCarryTheirPathsOnEveryPlatform(t *testing.T) {
	const windowsPath = `C:\Users\rescale\Uploads\testfile.bin`
	stamp := time.Now().UTC().Format(time.RFC3339Nano)

	tests := []struct {
		name   string
		filled string
	}{
		{"pre-encrypt upload", fillShippedFixture(t, shippedPreEncryptUploadState, windowsPath, windowsPath+".encrypted", stamp, stamp, stamp)},
		{"streaming upload", fillShippedFixture(t, shippedStreamingUploadState, windowsPath, stamp, stamp, stamp)},
		{"sequential download", fillShippedFixture(t, shippedSequentialDownloadState, windowsPath, windowsPath+".encrypted", stamp, stamp)},
		{"concurrent download", fillShippedFixture(t, shippedConcurrentDownloadState, windowsPath, windowsPath+".encrypted", stamp, stamp)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fields struct {
				LocalPath     string `json:"local_path"`
				EncryptedPath string `json:"encrypted_path"`
			}
			if err := json.Unmarshal([]byte(tt.filled), &fields); err != nil {
				t.Fatalf("the fixture does not parse with a Windows source path: %v", err)
			}
			if fields.LocalPath != windowsPath {
				t.Errorf("the fixture carries local_path %q, want %q", fields.LocalPath, windowsPath)
			}
			if fields.EncryptedPath != "" && fields.EncryptedPath != windowsPath+".encrypted" {
				t.Errorf("the fixture carries encrypted_path %q, want %q", fields.EncryptedPath, windowsPath+".encrypted")
			}
		})
	}
}

// TestGuardsLiveInTheApplicationsOwnDirectory pins the other half of that: the
// place they go instead. It is the per-user directory the application already
// keeps its own files in, so a guard is never left in the user's data and never
// depends on the source's filesystem carrying a lock.
func TestGuardsLiveInTheApplicationsOwnDirectory(t *testing.T) {
	dir, err := applicationGuardDirectory()
	if err != nil {
		t.Skipf("this environment has no per-user directory: %v", err)
	}
	if want := filepath.Join(filepath.Dir(config.ReportDirectory()), guardDirName); dir != want {
		t.Errorf("the guards go to %q, want %q — where the application keeps its other per-user state", dir, want)
	}
}

// TestAcquireUploadLock_LeavesNothingBesideTheSource pins where the guard
// lives. It is never removed — that is what keeps every acquirer locking the
// same file — so a guard beside the source is a permanent empty file in the
// user's own directory, and one a later folder upload would enumerate as
// something to transfer.
func TestAcquireUploadLock_LeavesNothingBesideTheSource(t *testing.T) {
	localPath := plantAbandonedLock(t, 424253)

	lock, err := AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("reclaim an abandoned lock: %v", err)
	}
	ReleaseUploadLock(lock)

	entries, err := os.ReadDir(filepath.Dir(localPath))
	if err != nil {
		t.Fatalf("read the source directory: %v", err)
	}
	remaining := make([]string, 0, len(entries))
	for _, entry := range entries {
		remaining = append(remaining, entry.Name())
	}
	if len(remaining) != 1 || remaining[0] != filepath.Base(localPath) {
		t.Errorf("the reclamation left %v beside the source, want only %q",
			remaining, filepath.Base(localPath))
	}

	// It is in the application's own directory instead, under the name every
	// acquirer of this source resolves to.
	guardPath, err := guardPathFor(lockFilePathFor(localPath))
	if err != nil {
		t.Fatalf("name the guard: %v", err)
	}
	if _, err := os.Stat(guardPath); err != nil {
		t.Errorf("the guard the reclamation held is not in the lock directory: %v", err)
	}
}
