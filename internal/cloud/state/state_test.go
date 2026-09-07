// Package state tests
package state

import (
	"crypto/sha256"
	"encoding/hex"
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
)

// withPIDDomain stands in for the identity the operating system gives this
// process, which is how a test is another machine or another PID namespace
// without being one. It is the same seam production reads through; nothing
// about it depends on the platform the test runs on.
func withPIDDomain(t *testing.T, domain string) {
	t.Helper()
	replacePIDDomain(t, func() (string, error) { return domain, nil })
}

// withoutAPIDDomain leaves the process unable to say which processes its PID is
// numbered among, which is what a system with no machine identifier — or, on
// Linux, no readable PID namespace — presents from here.
func withoutAPIDDomain(t *testing.T) {
	t.Helper()
	replacePIDDomain(t, func() (string, error) {
		return "", errors.New("this system names no PID domain")
	})
}

func replacePIDDomain(t *testing.T, read func() (string, error)) {
	t.Helper()
	previous := pidDomain
	pidDomain = read
	t.Cleanup(func() { pidDomain = previous })
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
// lock can be owned by a PID that is definitely alive, definitely gone, or one
// the system will not answer about, without the test having to find real ones.
func withProcessLiveness(t *testing.T, probe func(int) (processLiveness, error)) {
	t.Helper()
	previous := probeProcessLiveness
	probeProcessLiveness = probe
	t.Cleanup(func() { probeProcessLiveness = previous })
}

// livenessOf answers the probe from a plain "is this one alive" predicate: a
// system that answers at all, which is what every test but the undecided one
// wants.
func livenessOf(alive func(int) bool) func(int) (processLiveness, error) {
	return func(pid int) (processLiveness, error) {
		if alive(pid) {
			return livenessAlive, nil
		}
		return livenessDead, nil
	}
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
// how a test stands in for a second process of this installation:
// AcquireUploadLock would be stopped by the in-process claim long before the
// file is consulted. It resolves the lock path the way acquisition does.
func acquireAs(localPath string, pid int, token string) (*UploadLock, error) {
	return acquireLockFile(lockFilePathFor(localPath), localPath, uploadLockState{
		ProcessID:  pid,
		OwnerToken: token,
		Host:       lockHost,
		Owner:      lockOwner,
		PIDDomain:  currentPIDDomain(),
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
	withProcessLiveness(t, livenessOf(func(pid int) bool { return pid == ownerPID }))

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  ownerPID,
		OwnerToken: "owner-of-a-running-upload",
		Host:       lockHost,
		Owner:      lockOwner,
		PIDDomain:  currentPIDDomain(),
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
	withProcessLiveness(t, livenessOf(func(pid int) bool { return pid != deadPID }))

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  deadPID,
		OwnerToken: "owner-that-crashed",
		Host:       lockHost,
		Owner:      lockOwner,
		PIDDomain:  currentPIDDomain(),
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

// TestAcquireUploadLock_TakesOverADeadLockOfAnotherLogin pins where the domain
// boundary is drawn, which is not where the previous identifier drew it. PIDs
// are numbered per kernel, not per account: a lock left by another user of this
// machine names a PID this process can test, and one the kernel says is gone is
// gone for both of them. Whether it may be deleted is then the directory's
// business, not this rule's.
func TestAcquireUploadLock_TakesOverADeadLockOfAnotherLogin(t *testing.T) {
	const deadPID = 424255
	withProcessLiveness(t, livenessOf(func(pid int) bool { return pid != deadPID }))

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  deadPID,
		OwnerToken: "another-logins-upload-that-crashed",
		Host:       lockHost,
		Owner:      lockOwner + "-someone-else",
		PIDDomain:  currentPIDDomain(),
		AcquiredAt: time.Now(),
		LocalPath:  localPath,
	})

	lock, err := AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("refused a lock whose owner is gone on this very machine: %v", err)
	}
	defer ReleaseUploadLock(lock)

	if got := readLockFile(t, localPath); got.ProcessID != os.Getpid() {
		t.Errorf("lock file names PID %d, want this process (%d)", got.ProcessID, os.Getpid())
	}
}

// TestAcquireUploadLock_TakeoverCannotEvictTheWinner is the reclamation race
// that decides the protocol. Two acquirers read the same stale record; one of
// them replaces it and starts its upload; the other is still carrying a
// judgement of a record that is no longer there. Acting on that judgement — by
// removing the pathname, or by moving it aside, which takes whatever is at it —
// evicts the live owner, and both then own the same resume state: the later one
// aborts the earlier one's multipart upload as stale. The claim a reclaimer
// takes names the record it judged, so the one that arrives late finds that
// record gone and judges what is there instead. The acquirers are driven below
// AcquireUploadLock because the in-process claim is what stands in for the
// second process.
func TestAcquireUploadLock_TakeoverCannotEvictTheWinner(t *testing.T) {
	localPath := plantAbandonedLock(t, 424244)

	parked := make(chan struct{})
	release := make(chan struct{})
	var parkOnce, releaseOnce sync.Once
	releaseTheTaker := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseTheTaker()
	parkTakeoverAt(t, takeoverJudged, "late-taker", func() {
		parkOnce.Do(func() {
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

	// The other reclaimer judges the same record and goes all the way through:
	// it takes the claim, clears the stale lock, creates its own and records
	// itself in it. Only then does the one carrying the stale judgement resume.
	early, err := acquireAs(localPath, 900001, "early-taker")
	if err != nil {
		t.Fatalf("the reclaimer that got there first did not get the lock: %v", err)
	}
	defer ReleaseUploadLock(early)
	releaseTheTaker()

	late := <-lateDone
	if late.err == nil {
		ReleaseUploadLock(late.lock)
		t.Errorf("both acquirers own the lock: the late one took it from PID %d", early.ProcessID)
	} else if !strings.Contains(late.err.Error(), "another process") {
		t.Errorf("the late acquirer's refusal %q does not name the live owner", late.err)
	}
	if got := readLockFile(t, localPath); got.OwnerToken != "early-taker" {
		t.Errorf("lock file names owner %q, want the acquirer that won it", got.OwnerToken)
	}
}

// plantAbandonedLock leaves a lock file whose owner has died, which is the one
// state an acquirer is allowed to take over: this PID domain, and a PID inside
// it that is gone. A record from another domain is refused however dead its PID
// looks from here.
func plantAbandonedLock(t *testing.T, deadPID int) string {
	t.Helper()
	withProcessLiveness(t, livenessOf(func(pid int) bool { return pid != deadPID }))

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	if err := os.WriteFile(localPath, []byte("x"), 0600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  deadPID,
		OwnerToken: "owner-that-crashed",
		Host:       lockHost,
		Owner:      lockOwner,
		PIDDomain:  currentPIDDomain(),
		AcquiredAt: time.Now(),
		LocalPath:  localPath,
	})
	return localPath
}

// TestAcquireUploadLock_TakeoverCannotStrandTwoOwners is the three-party
// sequence. A reclaimer has cleared the stale lock, and for the instant before
// it creates its own the pathname is free to anyone: a fresh acquirer creates
// one there and owns the upload. Neither the reclaimer whose create then finds
// the pathname taken, nor a third acquirer still carrying its own judgement of
// the record that is now gone, may put anything back over that lock — either of
// them doing so leaves two acquirers believing they own the same upload and the
// same resume state.
func TestAcquireUploadLock_TakeoverCannotStrandTwoOwners(t *testing.T) {
	localPath := plantAbandonedLock(t, 424245)

	judged := make(chan struct{})
	release := make(chan struct{})
	var parkOnce, gapOnce, releaseOnce sync.Once
	releaseTheStraggler := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseTheStraggler()
	gap := lockOutcome{}
	lockTakeoverStep = func(phase string, owner uploadLockState) {
		switch {
		case phase == takeoverJudged && owner.OwnerToken == "straggler":
			parkOnce.Do(func() {
				close(judged)
				<-release
			})
		case phase == takeoverCleared && owner.OwnerToken == "reclaimer":
			gapOnce.Do(func() {
				gap.lock, gap.err = acquireAs(localPath, 900003, "gap-filler")
			})
		}
	}
	t.Cleanup(func() { lockTakeoverStep = nil })

	stragglerDone := make(chan lockOutcome, 1)
	go func() {
		lock, err := acquireAs(localPath, 900002, "straggler")
		stragglerDone <- lockOutcome{lock, err}
	}()
	<-judged

	reclaimer, reclaimErr := acquireAs(localPath, 900001, "reclaimer")
	releaseTheStraggler()
	straggler := <-stragglerDone

	owners := map[string]bool{}
	for token, got := range map[string]lockOutcome{
		"reclaimer": {reclaimer, reclaimErr}, "straggler": straggler, "gap-filler": gap,
	} {
		if got.err == nil && got.lock != nil {
			owners[token] = true
		}
	}
	if len(owners) != 1 {
		t.Fatalf("%d acquirers own the upload (%v), want exactly 1", len(owners), owners)
	}
	if !owners["gap-filler"] {
		t.Errorf("the acquirer that created the lock on a free pathname is not the owner (%v)", owners)
	}
	if got := readLockFile(t, localPath); got.OwnerToken != "gap-filler" {
		t.Errorf("lock file names owner %q, want the acquirer that created it", got.OwnerToken)
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
// identity half of the takeover. A dead owner's lock is judged stale; before the
// taker acts, that file is released and another acquirer creates its own at the
// pathname and has not written it yet. What the taker holds a claim on is the
// record it judged, and the file at the pathname is no longer that record, so
// there is nothing here for it to clear.
func TestAcquireUploadLock_DoesNotClearALockThatReplacedTheJudgedOne(t *testing.T) {
	localPath := plantAbandonedLock(t, 424250)
	lockFilePath := localPath + ".upload.lock"

	var once sync.Once
	var replacement os.FileInfo
	parkTakeoverAt(t, takeoverJudged, "taker", func() {
		once.Do(func() {
			// The judged file is released and a fresh lock, not yet written,
			// takes the pathname: a different file with different bytes from
			// the record that was judged.
			if err := os.Remove(lockFilePath); err != nil {
				t.Errorf("release the judged lock: %v", err)
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

// TestAcquireUploadLock_RefusesReclamationWithoutAPIDDomain pins the answer
// when the system will not say which processes this one's PID is numbered
// among. Creating a lock still excludes everyone — O_EXCL is the filesystem's
// own guarantee — but with no domain to compare against there is nothing that
// makes another record's owner provably gone.
func TestAcquireUploadLock_RefusesReclamationWithoutAPIDDomain(t *testing.T) {
	t.Run("creating a lock still works", func(t *testing.T) {
		withoutAPIDDomain(t)
		localPath := filepath.Join(t.TempDir(), "testfile.bin")

		lock, err := AcquireUploadLock(localPath)
		if err != nil {
			t.Fatalf("refused a lock nothing else holds: %v", err)
		}
		defer ReleaseUploadLock(lock)

		got := readLockFile(t, localPath)
		if got.ProcessID != os.Getpid() {
			t.Errorf("lock file names PID %d, want this process (%d)", got.ProcessID, os.Getpid())
		}
		// And it names no domain, so nothing reclaims it later either.
		if got.PIDDomain != "" {
			t.Errorf("lock file names PID domain %q, want none to have been established", got.PIDDomain)
		}
	})

	t.Run("reclaiming a stale lock is refused", func(t *testing.T) {
		localPath := plantAbandonedLock(t, 424251)
		lockFilePath := localPath + ".upload.lock"
		withoutAPIDDomain(t)

		lock, err := AcquireUploadLock(localPath)
		if err == nil {
			ReleaseUploadLock(lock)
			t.Fatal("cleared a lock without establishing which processes its PID is numbered among")
		}
		if errors.Is(err, ErrUploadLockUnavailable) {
			t.Errorf("a lock that exists is reported as no lock at all: %v", err)
		}
		if !strings.Contains(err.Error(), lockFilePath) {
			t.Errorf("the refusal %q does not name the file to delete (%s)", err, lockFilePath)
		}
		if got := readLockFile(t, localPath); got.OwnerToken != "owner-that-crashed" {
			t.Errorf("the stale lock was cleared anyway; it now names %q", got.OwnerToken)
		}
	})

	t.Run("a record written by such a process is refused later too", func(t *testing.T) {
		// The refusal is symmetric, and deliberately so: a record naming no
		// domain cannot be told apart from one written on another machine, so a
		// process that can name its own must not adopt it. This is the record's
		// full v4.9.9 shape — host, user, token, everything but the domain.
		const deadPID = 424254
		withProcessLiveness(t, livenessOf(func(pid int) bool { return pid != deadPID }))
		localPath := filepath.Join(t.TempDir(), "testfile.bin")

		withoutAPIDDomain(t)
		writeLockFile(t, localPath, uploadLockState{
			ProcessID:  deadPID,
			OwnerToken: "owner-that-crashed",
			Host:       lockHost,
			Owner:      lockOwner,
			PIDDomain:  currentPIDDomain(),
			AcquiredAt: time.Now(),
			LocalPath:  localPath,
		})
		withPIDDomain(t, "machine-1cd67aa9 pid:[4026531836]")

		lock, err := AcquireUploadLock(localPath)
		if err == nil {
			ReleaseUploadLock(lock)
			t.Fatal("adopted a record that names no PID domain")
		}
		if got := readLockFile(t, localPath); got.OwnerToken != "owner-that-crashed" {
			t.Errorf("the record was cleared anyway; it now names %q", got.OwnerToken)
		}
	})

	t.Run("two processes that cannot name a domain do not adopt each other's records", func(t *testing.T) {
		// A record naming no domain and a process naming none are not thereby
		// in one domain: they can be two machines that both could not say,
		// sharing one home, and the PID in the record is a live process on
		// the other one. "No domain" must equal nothing, not itself.
		const deadPID = 424256
		withProcessLiveness(t, livenessOf(func(pid int) bool { return pid != deadPID }))
		localPath := filepath.Join(t.TempDir(), "testfile.bin")

		withoutAPIDDomain(t)
		writeLockFile(t, localPath, uploadLockState{
			ProcessID:  deadPID,
			OwnerToken: "owner-that-crashed",
			Host:       lockHost,
			Owner:      lockOwner,
			PIDDomain:  currentPIDDomain(),
			AcquiredAt: time.Now().Add(-time.Hour),
			LocalPath:  localPath,
		})

		lock, err := AcquireUploadLock(localPath)
		if err == nil {
			ReleaseUploadLock(lock)
			t.Fatal("adopted a record naming no PID domain while naming none itself")
		}
		if got := readLockFile(t, localPath); got.OwnerToken != "owner-that-crashed" {
			t.Errorf("the record was cleared anyway; it now names %q", got.OwnerToken)
		}
	})
}

// claimPathOf names the claim a reclaimer of the lock beside localPath takes on
// the record that is in it: the record's own digest, so that every reclaimer of
// one record reaches for one name.
func claimPathOf(t *testing.T, localPath string) string {
	t.Helper()
	lockFilePath := localPath + ".upload.lock"
	record, err := os.ReadFile(lockFilePath)
	if err != nil {
		t.Fatalf("read the lock to name its claim: %v", err)
	}
	digest := sha256.Sum256(record)
	return lockFilePath + staleSuffix + hex.EncodeToString(digest[:8])
}

// TestAcquireUploadLock_SerializesTakersOfOneAbandonedLock pins the rule the
// claim exists for: several acquirers can judge one stale record, and only one
// of them may act on that judgement.
func TestAcquireUploadLock_SerializesTakersOfOneAbandonedLock(t *testing.T) {
	t.Run("a taker that holds the claim excludes the others", func(t *testing.T) {
		localPath := plantAbandonedLock(t, 424248)
		// What a taker in the middle of reclaiming this record has in place.
		claimPath := claimPathOf(t, localPath)
		if err := os.WriteFile(claimPath, nil, 0600); err != nil {
			t.Fatalf("plant the other taker's claim: %v", err)
		}

		lock, err := AcquireUploadLock(localPath)
		if err == nil {
			ReleaseUploadLock(lock)
			t.Fatal("a second taker reclaimed a record another one holds the claim on")
		}
		if !strings.Contains(err.Error(), claimPath) {
			t.Errorf("the refusal %q does not name the claim (%s) a taker that died would leave", err, claimPath)
		}
		if errors.Is(err, ErrUploadLockUnavailable) {
			t.Errorf("a lock that exists is reported as no lock at all: %v", err)
		}
		if got := readLockFile(t, localPath); got.OwnerToken != "owner-that-crashed" {
			t.Errorf("the stale lock was cleared anyway; it now names %q", got.OwnerToken)
		}
	})

	t.Run("a lock created while the taker was clearing it wins", func(t *testing.T) {
		localPath := plantAbandonedLock(t, 424249)

		var once sync.Once
		gap := lockOutcome{}
		parkTakeoverAt(t, takeoverCleared, "taker", func() {
			once.Do(func() {
				// The pathname is free for as long as the taker is between
				// removing and creating. Whoever creates there owns the upload,
				// and the taker must then find a live owner rather than a free
				// pathname of its own.
				gap.lock, gap.err = acquireAs(localPath, 900002, "gap-filler")
			})
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
		// Whatever the takers raced over, the claim each one takes is its own
		// to remove: nothing of it is left beside the source.
		if left := siblingsOf(t, localPath); len(left) != 2 {
			t.Errorf("the takers left %v beside the source, want only it and its lock", left)
		}
	})
}

// siblingsOf lists what the directory holding localPath contains.
func siblingsOf(t *testing.T, localPath string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(localPath))
	if err != nil {
		t.Fatalf("read the source directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
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

	t.Run("an existing lock whose claim cannot be created", func(t *testing.T) {
		// The source directory holds the lock but will take nothing more, so the
		// claim that decides who may reclaim it cannot be created. The lock file
		// is right there; reporting that nothing holds the upload — which is what
		// the sentinel means — is not true of it.
		localPath := plantAbandonedLock(t, 424252)
		denyNewFilesIn(t, filepath.Dir(localPath))

		lock, err := AcquireUploadLock(localPath)
		if err == nil {
			ReleaseUploadLock(lock)
			t.Fatal("cleared a stale lock with nothing to decide which taker may clear it")
		}
		if errors.Is(err, ErrUploadLockUnavailable) {
			t.Errorf("an existing lock that cannot be claimed is reported as no lock at all: %v", err)
		}
		if !strings.Contains(err.Error(), "cannot clear the abandoned upload lock") {
			t.Errorf("the refusal %q is not the one that reaches for the claim", err)
		}
		if got := readLockFile(t, localPath); got.OwnerToken != "owner-that-crashed" {
			t.Errorf("the stale lock was cleared anyway; it now names %q", got.OwnerToken)
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
// holds, so a test can stand in for a directory that will take nothing more.
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

// TestAcquireUploadLock_RecordsWhoItBelongsTo pins the fields a refusal and a
// reclamation are decided and worded from. Without them a record says only
// "PID 4711", which is a different upload on every machine.
func TestAcquireUploadLock_RecordsWhoItBelongsTo(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "testfile.bin")

	lock, err := AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("AcquireUploadLock failed: %v", err)
	}
	defer ReleaseUploadLock(lock)

	got := readLockFile(t, localPath)
	if got.Host == "" || got.Host != lockHost {
		t.Errorf("lock file names host %q, want this machine %q", got.Host, lockHost)
	}
	if got.Owner == "" || got.Owner != lockOwner {
		t.Errorf("lock file names owner %q, want this user %q", got.Owner, lockOwner)
	}
	// The PID domain is the one field a reclamation is decided on, because it
	// is the only one that says a PID here means anything.
	if got.PIDDomain == "" || got.PIDDomain != currentPIDDomain() {
		t.Errorf("lock file names PID domain %q, want this one %q", got.PIDDomain, currentPIDDomain())
	}

	// And it is on disk under that name: the key is the format, and a record
	// whose domain arrived under some other one reads back as naming none.
	record, err := os.ReadFile(localPath + ".upload.lock")
	if err != nil {
		t.Fatalf("read the lock file: %v", err)
	}
	if !strings.Contains(string(record), `"pid_domain"`) {
		t.Errorf("the lock file does not carry a pid_domain key:\n%s", record)
	}
	if strings.Contains(string(record), `"install_id"`) || strings.Contains(string(record), `"guard"`) {
		t.Errorf("the lock file still carries an identity this version does not decide on:\n%s", record)
	}
}

// TestAcquireUploadLock_RefusesALockItsCreatorHasNotWrittenYet is the
// delayed-creator sequence. An acquirer wins the create and stalls before
// writing the record, and the empty file it holds is now older than any window
// an unidentified lock could be given. Nothing in it names an owner whose
// liveness could be tested, so there is no age at which taking it over is
// anything but a guess — and the guess is wrong exactly when its creator is
// alive, which is when it costs two owners of one upload.
func TestAcquireUploadLock_RefusesALockItsCreatorHasNotWrittenYet(t *testing.T) {
	const creatorPID = 900001
	withProcessLiveness(t, livenessOf(func(pid int) bool { return pid == creatorPID }))

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	lockFilePath := localPath + ".upload.lock"

	created := make(chan struct{})
	release := make(chan struct{})
	var parkOnce, releaseOnce sync.Once
	releaseTheCreator := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseTheCreator()
	parkTakeoverAt(t, takeoverCreated, "creator", func() {
		parkOnce.Do(func() {
			close(created)
			<-release
		})
	})

	creatorDone := make(chan lockOutcome, 1)
	go func() {
		lock, err := acquireAs(localPath, creatorPID, "creator")
		creatorDone <- lockOutcome{lock, err}
	}()
	<-created

	// The creator holds a file it has not written. Age it well past the grace
	// an unidentified lock used to be given, which is what a stalled creator
	// presents to whoever arrives next.
	aged := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lockFilePath, aged, aged); err != nil {
		t.Fatalf("age the unwritten lock: %v", err)
	}

	reclaimer, err := acquireAs(localPath, 900002, "reclaimer")
	if err == nil {
		ReleaseUploadLock(reclaimer)
		t.Fatal("a reclaimer took a lock whose creator had not written its record yet")
	}
	if _, statErr := os.Stat(lockFilePath); statErr != nil {
		t.Errorf("the unwritten lock was cleared from under its creator: %v", statErr)
	}
	releaseTheCreator()

	creator := <-creatorDone
	if creator.err != nil {
		t.Fatalf("the creator did not get the lock it created: %v", creator.err)
	}
	if got := readLockFile(t, localPath); got.OwnerToken != "creator" {
		t.Errorf("lock file names owner %q, want the creator that wrote it", got.OwnerToken)
	}
	ReleaseUploadLock(creator.lock)
}

// TestAcquireUploadLock_TakesOverItsOwnPIDsLock pins the same-PID rule for a
// record that names our PID and another run's token. A live owner other than
// us cannot have written it, and this run's own lock is refused before the rule
// is reached because its token is ours, so what is left is a lock of an earlier
// run of this process, or one the OS has since handed our PID to. Refusing it
// would wedge every retry after a crash.
func TestAcquireUploadLock_TakesOverItsOwnPIDsLock(t *testing.T) {
	withProcessLiveness(t, livenessOf(func(int) bool { return true }))

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  os.Getpid(),
		OwnerToken: "a-run-of-this-process-that-crashed",
		Host:       lockHost,
		Owner:      lockOwner,
		PIDDomain:  currentPIDDomain(),
		AcquiredAt: time.Now().Add(-time.Hour),
		LocalPath:  localPath,
	})

	lock, err := AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("refused a lock left by an earlier run of this process: %v", err)
	}
	defer ReleaseUploadLock(lock)

	if got := readLockFile(t, localPath); got.OwnerToken != processLockToken {
		t.Errorf("lock file names owner %q, want this run of the process", got.OwnerToken)
	}
}

// TestAcquireUploadLock_RefusesTheLiveLockOfThisRun is the same-process alias
// sequence. Two spellings of one source file get two keys in the in-process map
// and meet on the one lock file they share, so the second acquisition reads a
// record naming our own PID and our own token. That is this run's live lock,
// however the map missed it, and clearing it puts two uploads of one file in one
// process.
func TestAcquireUploadLock_RefusesTheLiveLockOfThisRun(t *testing.T) {
	withProcessLiveness(t, livenessOf(func(int) bool { return true }))

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  os.Getpid(),
		OwnerToken: processLockToken,
		Host:       lockHost,
		Owner:      lockOwner,
		PIDDomain:  currentPIDDomain(),
		AcquiredAt: time.Now().Add(-time.Minute),
		LocalPath:  localPath,
	})
	before := readLockBytes(t, localPath)

	lock, err := AcquireUploadLock(localPath)
	if err == nil {
		ReleaseUploadLock(lock)
		t.Fatal("cleared a lock this very run of the process still holds")
	}
	if !strings.Contains(err.Error(), "already in progress in this process") {
		t.Errorf("the refusal %q does not say the upload is already running here", err)
	}
	if after := readLockBytes(t, localPath); after != before {
		t.Errorf("the lock file was rewritten:\n%s", after)
	}
	if left := siblingsOf(t, localPath); len(left) != 1 {
		t.Errorf("the refusal left %v beside the source, want only the lock it would not take", left)
	}
}

// TestAcquireUploadLock_RefusesACaseAliasOfItsOwnLock is that sequence on a real
// volume rather than a planted record: macOS volumes are case-insensitive by
// default, and the symlink resolver keeps the caller's spelling, so data.bin and
// DATA.BIN are two in-process keys naming one lock file.
func TestAcquireUploadLock_RefusesACaseAliasOfItsOwnLock(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("volumes are case-insensitive by default on macOS")
	}
	dir := t.TempDir()
	spelled, alias := filepath.Join(dir, "data.bin"), filepath.Join(dir, "DATA.BIN")
	if err := os.WriteFile(spelled, []byte("x"), 0600); err != nil {
		t.Fatalf("write the source file: %v", err)
	}
	one, err := os.Stat(spelled)
	if err != nil {
		t.Fatalf("stat the source file: %v", err)
	}
	other, err := os.Stat(alias)
	if err != nil || !os.SameFile(one, other) {
		t.Skip("this volume is case-sensitive, so the two spellings are two files")
	}

	lock, err := AcquireUploadLock(spelled)
	if err != nil {
		t.Fatalf("could not acquire the first lock: %v", err)
	}
	defer ReleaseUploadLock(lock)
	before, err := os.ReadFile(lock.LockFilePath)
	if err != nil {
		t.Fatalf("read the lock record: %v", err)
	}

	second, err := AcquireUploadLock(alias)
	if err == nil {
		ReleaseUploadLock(second)
		t.Fatal("a second spelling of the source took the lock the first one holds")
	}
	after, err := os.ReadFile(lock.LockFilePath)
	if err != nil {
		t.Fatalf("read the lock record after the refusal: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("the alias rewrote the live lock:\n%s", after)
	}
	var got uploadLockState
	if err := json.Unmarshal(after, &got); err != nil {
		t.Fatalf("parse the lock record: %v", err)
	}
	if got.LocalPath != spelled {
		t.Errorf("the lock names %q, want the spelling that acquired it %q", got.LocalPath, spelled)
	}
}

// readLockBytes reads a lock file as written, for the refusals that must leave
// it exactly as they found it.
func readLockBytes(t *testing.T, localPath string) string {
	t.Helper()
	data, err := os.ReadFile(localPath + ".upload.lock")
	if err != nil {
		t.Fatalf("read the lock file: %v", err)
	}
	return string(data)
}

// TestAcquireUploadLock_RefusesAnOwnerTheSystemWillNotAnswerAbout covers the
// probe that cannot decide. Windows answers ERROR_ACCESS_DENIED for a live
// process of another login, an elevated one or a protected one, and reading
// that as "no such process" lets the next acquirer take a running upload's
// lock. Only positive evidence of absence may reclaim.
func TestAcquireUploadLock_RefusesAnOwnerTheSystemWillNotAnswerAbout(t *testing.T) {
	const ownerPID = 424277
	withProcessLiveness(t, func(pid int) (processLiveness, error) {
		if pid == ownerPID {
			return livenessUnknown, errors.New("cannot open the process with PID 424277: Access is denied.")
		}
		return livenessAlive, nil
	})

	localPath := filepath.Join(t.TempDir(), "testfile.bin")
	lockFilePath := localPath + ".upload.lock"
	acquired := time.Now().Add(-time.Hour)
	writeLockFile(t, localPath, uploadLockState{
		ProcessID:  ownerPID,
		OwnerToken: "an-owner-this-login-cannot-open",
		Host:       lockHost,
		Owner:      lockOwner + "-someone-else",
		PIDDomain:  currentPIDDomain(),
		AcquiredAt: acquired,
		LocalPath:  localPath,
	})
	before := readLockBytes(t, localPath)

	lock, err := AcquireUploadLock(localPath)
	if err == nil {
		ReleaseUploadLock(lock)
		t.Fatal("cleared a lock whose owner the system would not answer about")
	}
	if errors.Is(err, ErrUploadLockUnavailable) {
		t.Errorf("a lock that exists is reported as no lock at all: %v", err)
	}
	for _, want := range []string{
		fmt.Sprintf("%d", ownerPID), lockHost, lockOwner + "-someone-else",
		acquired.Format(time.RFC3339), lockFilePath, "Access is denied.",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}
	if after := readLockBytes(t, localPath); after != before {
		t.Errorf("the lock file was rewritten:\n%s", after)
	}
}

// TestAcquireUploadLock_RefusesALockFromAnotherPIDDomain covers the locks this
// acquisition may not clear on its own. A PID is only meaningful among the
// processes the same kernel numbered: two machines can be configured with one
// hostname and can carry one uid, and one home directory can be mounted on both
// of them, so a record whose every string matches this acquirer's can still name
// a process running on the other machine — where nothing here can see it, and
// where "no such process" is the answer this machine gives about a live upload.
// So a record naming another domain — and one from before this version, or from
// an interim build, which name none — is refused, with what an operator needs to
// decide whether to delete it.
func TestAcquireUploadLock_RefusesALockFromAnotherPIDDomain(t *testing.T) {
	const deadPID = 424260
	// This machine, as the operating system named it, and the same machine
	// running the containers that share it.
	const thisDomain = "machine-1cd67aa9 pid:[4026531836]"
	const anotherNamespaceHere = "machine-1cd67aa9 pid:[4026532210]"
	acquired := time.Now().Add(-time.Hour).Round(time.Second)

	cases := []struct {
		name  string
		plant func(t *testing.T, localPath string)
		names []string
	}{
		{
			name: "another machine that answers to this machine's name",
			plant: func(t *testing.T, localPath string) {
				// Same hostname, same uid, same mount spelling, and — with one
				// home directory between them — the same of anything either of
				// them writes down. The PID still belongs to a process on the
				// other machine.
				writeLockFile(t, localPath, uploadLockState{
					ProcessID: deadPID, OwnerToken: "owner-elsewhere",
					Host: lockHost, Owner: lockOwner,
					PIDDomain:  "machine-8f0142bc pid:[4026531836]",
					AcquiredAt: acquired, LocalPath: localPath,
				})
			},
			names: []string{lockHost, lockOwner},
		},
		{
			name: "another PID namespace on this machine",
			plant: func(t *testing.T, localPath string) {
				// Two containers on one host: one machine identifier, one
				// mounted source, and two independent runs of PID numbering.
				writeLockFile(t, localPath, uploadLockState{
					ProcessID: deadPID, OwnerToken: "owner-elsewhere",
					Host: lockHost, Owner: lockOwner,
					PIDDomain:  anotherNamespaceHere,
					AcquiredAt: acquired, LocalPath: localPath,
				})
			},
			names: []string{lockHost, lockOwner},
		},
		{
			name: "another machine and another login",
			plant: func(t *testing.T, localPath string) {
				writeLockFile(t, localPath, uploadLockState{
					ProcessID: deadPID, OwnerToken: "owner-elsewhere",
					Host: "another-host", Owner: lockOwner + "-someone-else",
					PIDDomain:  "machine-8f0142bc pid:[4026531836]",
					AcquiredAt: acquired, LocalPath: localPath,
				})
			},
			names: []string{"another-host", lockOwner + "-someone-else"},
		},
		{
			name: "a lock written while an installation identifier decided it",
			plant: func(t *testing.T, localPath string) {
				// The interim format of this release: an identifier kept in the
				// user's configuration directory, which two machines mounting
				// one home read identically. A record carrying it names no PID
				// domain — refused once, by hand, rather than cleared. No build
				// that wrote one was released.
				interim := fmt.Sprintf("{\n  \"process_id\": %d,\n  \"owner_token\": %q,\n  \"host\": %q,\n  \"owner\": %q,\n  \"install_id\": %q,\n  \"acquired_at\": %q,\n  \"local_path\": %q\n}",
					deadPID, "owner-elsewhere", lockHost, lockOwner,
					"c9d2f0a17b4e6538c9d2f0a17b4e6538",
					acquired.Format(time.RFC3339Nano), localPath)
				if err := os.WriteFile(localPath+".upload.lock", []byte(interim), 0600); err != nil {
					t.Fatalf("plant an interim-format lock: %v", err)
				}
			},
			names: []string{lockHost, lockOwner},
		},
		{
			name: "a lock written while a guard file decided it",
			plant: func(t *testing.T, localPath string) {
				// The other interim format: host, user and the guard whose OS
				// lock its writer held.
				interim := fmt.Sprintf("{\n  \"process_id\": %d,\n  \"owner_token\": %q,\n  \"host\": %q,\n  \"owner\": %q,\n  \"guard\": %q,\n  \"acquired_at\": %q,\n  \"local_path\": %q\n}",
					deadPID, "owner-elsewhere", lockHost, lockOwner,
					filepath.Join(t.TempDir(), "elsewhere.guard"),
					acquired.Format(time.RFC3339Nano), localPath)
				if err := os.WriteFile(localPath+".upload.lock", []byte(interim), 0600); err != nil {
					t.Fatalf("plant an interim-format lock: %v", err)
				}
			},
			names: []string{lockHost, lockOwner},
		},
		{
			name: "a record written before this version",
			plant: func(t *testing.T, localPath string) {
				t.Helper()
				// The shipped format, literally: process_id, owner_token,
				// acquired_at, local_path and no identity of any kind.
				shipped := fmt.Sprintf("{\n  \"process_id\": %d,\n  \"owner_token\": %q,\n  \"acquired_at\": %q,\n  \"local_path\": %q\n}",
					deadPID, "owner-elsewhere", acquired.Format(time.RFC3339Nano), localPath)
				if err := os.WriteFile(localPath+".upload.lock", []byte(shipped), 0600); err != nil {
					t.Fatalf("plant a shipped-format lock: %v", err)
				}
			},
			names: []string{"unknown"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// The owner is gone as far as this machine can tell, which is the
			// judgement that used to be enough to clear it.
			withProcessLiveness(t, livenessOf(func(pid int) bool { return pid != deadPID }))
			withPIDDomain(t, thisDomain)
			localPath := filepath.Join(t.TempDir(), "testfile.bin")
			lockFilePath := localPath + ".upload.lock"
			testCase.plant(t, localPath)

			lock, err := AcquireUploadLock(localPath)
			if err == nil {
				ReleaseUploadLock(lock)
				t.Fatal("cleared a lock whose PID was numbered somewhere this process cannot see into")
			}
			if errors.Is(err, ErrUploadLockUnavailable) {
				t.Errorf("a lock that exists is reported as no lock at all: %v", err)
			}
			wanted := append([]string{
				fmt.Sprintf("%d", deadPID), acquired.Format(time.RFC3339), lockFilePath,
			}, testCase.names...)
			for _, want := range wanted {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal %q does not name %q", err, want)
				}
			}
			if got := readLockFile(t, localPath); got.OwnerToken != "owner-elsewhere" {
				t.Errorf("the foreign lock was cleared anyway; it now names %q", got.OwnerToken)
			}
			if left := siblingsOf(t, localPath); len(left) != 1 {
				t.Errorf("the refusal left %v beside the source, want only the lock it would not take", left)
			}
		})
	}
}

// TestAcquireUploadLock_RefusesALockThatNamesNoOwner is the last shape a record
// can take: a file its creator never wrote into. There is no domain, user
// or PID in it to judge, and no age at which that changes — a creator between
// its O_EXCL and its write presents exactly this, and so does a file left by
// something that is not this program at all. Age alone used to be enough to
// clear it.
func TestAcquireUploadLock_RefusesALockThatNamesNoOwner(t *testing.T) {
	ages := map[string]time.Duration{
		"written a moment ago": 0,
		"written an hour ago":  time.Hour,
		"written a week ago":   7 * 24 * time.Hour,
	}
	for name, age := range ages {
		t.Run(name, func(t *testing.T) {
			localPath := filepath.Join(t.TempDir(), "testfile.bin")
			lockFilePath := localPath + ".upload.lock"
			written := time.Now().Add(-age).Round(time.Second)
			plantOwnerlessLock(t, lockFilePath, written)

			lock, err := AcquireUploadLock(localPath)
			if err == nil {
				ReleaseUploadLock(lock)
				t.Fatal("cleared a lock file whose creator is unknown")
			}
			if errors.Is(err, ErrUploadLockUnavailable) {
				t.Errorf("a lock that exists is reported as no lock at all: %v", err)
			}
			for _, want := range []string{written.Format(time.RFC3339), lockFilePath} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal %q does not name %q", err, want)
				}
			}
			if _, err := os.Stat(lockFilePath); err != nil {
				t.Errorf("the lock was cleared anyway: %v", err)
			}
		})
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

// TestAcquireUploadLock_LeavesNothingBesideTheSource pins what a reclamation is
// allowed to put in the user's own directory. The claim that decides which taker
// may clear a record has to live where every taker of that record can see it,
// which is beside the lock; it is removed on the way out, whichever way the
// reclamation went, because a file that stayed there would be one a later folder
// upload enumerates as something to transfer — and one that blocks the next
// reclamation of the same record for good.
func TestAcquireUploadLock_LeavesNothingBesideTheSource(t *testing.T) {
	localPath := plantAbandonedLock(t, 424253)
	if err := SaveUploadState(&UploadResumeState{LocalPath: localPath}, localPath); err != nil {
		t.Fatalf("write the resume state the interrupted attempt left: %v", err)
	}

	lock, err := AcquireUploadLock(localPath)
	if err != nil {
		t.Fatalf("reclaim a stale lock: %v", err)
	}
	defer ReleaseUploadLock(lock)

	base := filepath.Base(localPath)
	want := map[string]bool{
		base:                    true,
		base + ".upload.lock":   true,
		base + ".upload.resume": true,
	}
	got := siblingsOf(t, localPath)
	if len(got) != len(want) {
		t.Errorf("the reclamation left %v beside the source, want the source, its lock and its resume record", got)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("the reclamation left %q beside the source", name)
		}
	}
}
