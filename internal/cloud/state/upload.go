// Package state provides shared resume state types and I/O for upload/download operations.
// This package breaks the import cycle between upload/, download/, transfer/, and providers/.
//
// The cycle was: upload → providers → transfer → upload
// Now: upload → providers → transfer → state (no cycle)
package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// UploadResumeState tracks the state of an in-progress upload for resumption.
// Supports both legacy (FormatVersion=0) and streaming (FormatVersion=1) encryption.
type UploadResumeState struct {
	LocalPath     string `json:"local_path"`     // Original source file path
	EncryptedPath string `json:"encrypted_path"` // Encrypted temp file path (legacy v0 only)
	ObjectKey     string `json:"object_key"`     // S3 object key or Azure blob path
	UploadID      string `json:"upload_id"`      // S3 multipart upload ID (empty for Azure)
	TotalSize     int64  `json:"total_size"`     // Size of encrypted file
	OriginalSize  int64  `json:"original_size"`  // Size of original file (for validation)
	// SourceModTime is the source file's modification time when the encrypted
	// copy was made. Size alone cannot tell an edited file from the one this
	// ciphertext describes, and resuming across such an edit would upload the
	// old bytes under a registration describing the new ones. State written
	// before v4.9.9 has no such key at all, and this version writes the zero
	// time when it has none to record; either way the loader reads back a zero
	// value, which is why such a state is not resumed.
	SourceModTime  time.Time       `json:"source_mod_time"`
	UploadedBytes  int64           `json:"uploaded_bytes"`  // Bytes uploaded so far
	CompletedParts []CompletedPart `json:"completed_parts"` // S3 parts
	BlockIDs       []string        `json:"block_ids"`       // Azure uncommitted block IDs
	EncryptionKey  string          `json:"encryption_key"`  // Base64-encoded encryption key (legacy v0)
	IV             string          `json:"iv"`              // Base64-encoded IV (legacy v0)
	RandomSuffix   string          `json:"random_suffix"`   // Random suffix for object name
	CreatedAt      time.Time       `json:"created_at"`
	LastUpdate     time.Time       `json:"last_update"`
	StorageType    string          `json:"storage_type"` // "S3Storage" or "AzureStorage"
	// StorageID and Container name the destination the interrupted attempt was
	// filling. This file and the upload lock both key on the local path alone,
	// so one source uploaded to two destinations shares one sidecar — and the
	// object key and upload ID recorded here belong to whichever destination
	// wrote them. Absent in state written before v4.9.9.
	StorageID string `json:"storage_id,omitempty"`
	Container string `json:"container,omitempty"`

	// Streaming encryption fields (FormatVersion=1)
	FormatVersion int    `json:"format_version"` // 0=legacy, 1=streaming
	MasterKey     string `json:"master_key"`     // Base64-encoded master key (v1 only)
	FileId        string `json:"file_id"`        // Base64-encoded file identifier (v1 only)
	// PartSize is the size the attempt cut its parts to: plaintext parts for a
	// streaming (v1) upload, ciphertext parts or Azure blocks for a pre-encrypt
	// (v0) one. Both resumes need it — the plan is recomputed on every attempt,
	// and parts cut to a different size line up with nothing the backend holds.
	// Absent in state written before v4.9.9, which is why such a state is not
	// resumed.
	PartSize int64 `json:"part_size"`

	// InitialIV is the base64 IV the object's metadata carries, which is where
	// a download starts the chain. It is separate from IV above, which belongs
	// to the pre-encrypt (v0) format and describes a whole encrypted file.
	InitialIV string `json:"initial_iv,omitempty"`
	// ChainIV is the base64 CBC chaining position at the checkpoint boundary:
	// the last ciphertext block of StreamingParts' final part. Without it the
	// key and initial IV only place an encryptor at part 0, so a resumed
	// attempt would produce different bytes for every part already accepted.
	// Absent in any state this version did not write, which is why a v1 state
	// without it is abandoned rather than guessed at.
	ChainIV string `json:"chain_iv,omitempty"`
	// StreamingParts is the CONTIGUOUS prefix of parts the backend has
	// accepted, in index order. Parts finish out of order under concurrency, so
	// a set of completed parts is not a resume point: only a prefix is, because
	// the chain IV names one boundary and the source is re-read from it.
	StreamingParts []StreamingPart `json:"streaming_parts,omitempty"`

	// Process locking fields
	ProcessID      int       `json:"process_id"`       // PID of owning process
	LockAcquiredAt time.Time `json:"lock_acquired_at"` // When lock was acquired
}

// CompletedPart represents a completed upload part (S3-specific).
type CompletedPart struct {
	PartNumber int32  `json:"part_number"`
	ETag       string `json:"etag"`
}

// StreamingPart is one part of a streaming (v1) upload that the backend has
// accepted, named by the index the encryption chain gives it rather than by its
// position in a list — a resumed attempt has to line its own parts up with
// these, and a list that only carried order could not survive being read back
// out of order.
//
// Handle is whatever the backend needs to assemble that part later: the S3
// ETag, or the Azure block ID the attempt staged the block under. Both
// backends already report it in the same field of a part result, so one list
// serves both.
type StreamingPart struct {
	PartIndex int64  `json:"part_index"`
	Handle    string `json:"handle"`
}

// MaxResumeAge is the maximum age of a resume state before it's considered expired.
// Aligned with AWS multipart upload expiry (7 days) and Azure uncommitted block expiry (7 days).
const MaxResumeAge = 7 * 24 * time.Hour

// lockOwnerlessGrace is how long a lock file that names no owner is honoured
// before it is treated as abandoned. Such a file only exists when its writer
// died between creating it and recording who it is, so there is no owner whose
// liveness could be checked; the only alternative to a time bound here is a
// lock that nothing can ever clear. An identified owner is never evicted on
// elapsed time — a large upload holds its lock for as long as it takes.
const lockOwnerlessGrace = 30 * time.Second

// lockTakeoverAttempts bounds how many times acquisition will clear an
// abandoned lock and race for the create again, so a pathological loop of
// owners appearing and dying cannot spin here forever.
const lockTakeoverAttempts = 8

// ErrUploadLockUnavailable reports that the lock file could not be created,
// written or read at all — a read-only or full source directory. Nothing holds
// the upload in that case; there is simply nowhere to record that we do, which
// is a different answer from the contention errors and one a caller may choose
// to carry on past.
var ErrUploadLockUnavailable = errors.New("the source directory cannot hold an upload lock")

// =============================================================================
// Basic I/O functions - these are the core operations needed everywhere
// =============================================================================

// SaveUploadState saves the upload resume state to a sidecar file.
// The state file is saved atomically using a temporary file + rename.
func SaveUploadState(state *UploadResumeState, localPath string) error {
	stateFilePath := localPath + ".upload.resume"
	tmpFilePath := stateFilePath + ".tmp"

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal upload state: %w", err)
	}

	if err := os.WriteFile(tmpFilePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp state file: %w", err)
	}

	if err := os.Rename(tmpFilePath, stateFilePath); err != nil {
		os.Remove(tmpFilePath)
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// LoadUploadState loads the upload resume state from a sidecar file.
// Returns nil without error if no resume state exists.
func LoadUploadState(localPath string) (*UploadResumeState, error) {
	stateFilePath := localPath + ".upload.resume"

	data, err := os.ReadFile(stateFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state UploadResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state file: %w", err)
	}

	return &state, nil
}

// DeleteUploadState deletes the upload resume state file.
func DeleteUploadState(localPath string) error {
	stateFilePath := localPath + ".upload.resume"
	err := os.Remove(stateFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete state file: %w", err)
	}
	return nil
}

// UploadResumeStateExists checks if a resume state file exists.
func UploadResumeStateExists(localPath string) bool {
	_, err := os.Stat(localPath + ".upload.resume")
	return err == nil
}

// =============================================================================
// Validation - checks if resume state is usable
// =============================================================================

// ValidateUploadState validates that a resume state is still usable.
func ValidateUploadState(state *UploadResumeState, localPath string) error {
	if state == nil {
		return fmt.Errorf("state is nil")
	}

	// Check source file exists and size matches
	fileInfo, err := os.Stat(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source file no longer exists")
		}
		return fmt.Errorf("failed to stat source file: %w", err)
	}

	expectedSize := state.OriginalSize
	if expectedSize == 0 {
		expectedSize = state.TotalSize // Backwards compatibility
	}
	if fileInfo.Size() != expectedSize {
		return fmt.Errorf("source file size changed (was %d, now %d)", expectedSize, fileInfo.Size())
	}

	// Check age
	if time.Since(state.CreatedAt) > MaxResumeAge {
		return fmt.Errorf("resume state expired")
	}

	// Check path matches
	if state.LocalPath != localPath {
		return fmt.Errorf("local path mismatch")
	}

	// Check encrypted temp file exists (legacy v0 only)
	if state.FormatVersion == 0 && state.EncryptedPath != "" {
		if _, err := os.Stat(state.EncryptedPath); err != nil {
			return fmt.Errorf("encrypted temp file no longer exists")
		}
	}

	// Validate streaming format fields
	if state.FormatVersion == 1 {
		if state.MasterKey == "" || state.FileId == "" || state.PartSize <= 0 {
			return fmt.Errorf("streaming format missing required fields")
		}
	}

	// Validate bytes
	if state.UploadedBytes > state.TotalSize {
		return fmt.Errorf("uploaded bytes exceeds total size - state corrupted")
	}

	return nil
}

// =============================================================================
// Upload locking - prevents concurrent uploads of the same file
// =============================================================================

// UploadLock represents an acquired upload lock.
type UploadLock struct {
	LockFilePath string
	ProcessID    int
	OwnerToken   string
	AcquiredAt   time.Time
}

type uploadLockState struct {
	ProcessID  int       `json:"process_id"`
	OwnerToken string    `json:"owner_token,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
	LocalPath  string    `json:"local_path"`
}

// processLockToken tells this run of the process apart from any other owner
// that ever wrote a lock file. A PID cannot do that on its own: the OS hands a
// dead process's PID to a new one, so a lock left behind by a crashed run can
// name the PID of the run that finds it.
var processLockToken = newLockToken()

// heldLocks records the lock files this process currently owns. The file alone
// cannot exclude a second transfer of the same path here: our own PID is by
// definition alive, so an on-disk check that honoured it would deadlock every
// retry after a crash, and one that ignored it would let two transfers in this
// process share one resume state.
var (
	heldLocksMu sync.Mutex
	heldLocks   = make(map[string]struct{})
)

func newLockToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// Only reachable if the system entropy source is broken. Time and PID
		// are a weaker token but still distinguish this run from a lock file
		// written by an earlier one, which is all the token is for.
		return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// claimLocalLock reserves a lock path for this process, reporting whether the
// caller got it. The reservation is released by releaseLocalLock.
func claimLocalLock(lockFilePath string) bool {
	key := localLockKey(lockFilePath)
	heldLocksMu.Lock()
	defer heldLocksMu.Unlock()
	if _, held := heldLocks[key]; held {
		return false
	}
	heldLocks[key] = struct{}{}
	return true
}

func releaseLocalLock(lockFilePath string) {
	key := localLockKey(lockFilePath)
	heldLocksMu.Lock()
	delete(heldLocks, key)
	heldLocksMu.Unlock()
}

// lockFilePathFor derives the lock file for a source path, resolved so that two
// spellings of one file name one lock: relative against absolute, or through a
// symlinked directory. Honouring the caller's spelling gave each alias its own
// in-process key, and the aliases then met on the single lock file they share —
// where the same-PID branch reads this process's own live lock and clears it.
func lockFilePathFor(localPath string) string {
	resolved := filepath.Clean(localPath)
	if abs, err := filepath.Abs(resolved); err == nil {
		resolved = abs
	}
	// Only when the path exists; a source that is about to be created has no
	// links to follow and Abs is as canonical as it gets.
	if evaluated, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = evaluated
	}
	return resolved + ".upload.lock"
}

// localLockKey is the identity heldLocks files a lock under. Windows paths that
// differ only in case name the same file, so a key that did not fold case there
// would let one spelling claim a lock this process already holds under another.
func localLockKey(lockFilePath string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(lockFilePath)
	}
	return lockFilePath
}

// AcquireUploadLock attempts to acquire an exclusive lock for uploading a file.
//
// Ownership is established by creating the lock file with O_EXCL, which is the
// only step here that two acquirers cannot both win. Writing a temporary file
// and renaming it cannot exclude anyone: rename replaces whatever is at the
// destination, so both acquirers would succeed and both would believe they own
// the upload — and two owners of one resume-state path means the second aborts
// the first's multipart upload as stale.
//
// An existing lock is only taken over when its owner is provably gone, never
// because it is old: a multi-hour upload is still an owner.
func AcquireUploadLock(localPath string) (*UploadLock, error) {
	lockFilePath := lockFilePathFor(localPath)

	if !claimLocalLock(lockFilePath) {
		return nil, fmt.Errorf("upload of %s is already in progress in this process", localPath)
	}

	lock, err := acquireLockFile(lockFilePath, localPath, uploadLockState{
		ProcessID:  os.Getpid(),
		OwnerToken: processLockToken,
		AcquiredAt: time.Now(),
		LocalPath:  localPath,
	})
	if err != nil {
		releaseLocalLock(lockFilePath)
		return nil, err
	}
	return lock, nil
}

func acquireLockFile(lockFilePath, localPath string, newLock uploadLockState) (*UploadLock, error) {
	data, err := json.MarshalIndent(newLock, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode lock file: %w", err)
	}

	for attempt := 0; attempt < lockTakeoverAttempts; attempt++ {
		file, err := os.OpenFile(lockFilePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			writeErr := writeAndClose(file, data)
			if writeErr != nil {
				// A lock nobody can read is worse than no lock: remove it so the
				// next attempt is not blocked by our own half-written file.
				os.Remove(lockFilePath)
				return nil, fmt.Errorf("%w: failed to write lock file: %w", ErrUploadLockUnavailable, writeErr)
			}
			return &UploadLock{
				LockFilePath: lockFilePath,
				ProcessID:    newLock.ProcessID,
				OwnerToken:   newLock.OwnerToken,
				AcquiredAt:   newLock.AcquiredAt,
			}, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("%w: failed to create lock file: %w", ErrUploadLockUnavailable, err)
		}

		// Someone else got there first. Only clear it if its owner is gone.
		if err := clearAbandonedLock(lockFilePath, newLock); err != nil {
			return nil, err
		}
	}

	return nil, fmt.Errorf("could not acquire upload lock for %s: it kept being retaken", localPath)
}

func writeAndClose(file *os.File, data []byte) error {
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// clearAbandonedLock removes an existing lock file when nothing owns it any
// more, and reports an error when something does. Returning nil means the
// caller should race for the create again — not that the caller owns anything.
func clearAbandonedLock(lockFilePath string, owner uploadLockState) error {
	data, err := os.ReadFile(lockFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Released while we looked; try to create it again.
		}
		return fmt.Errorf("%w: failed to read upload lock: %w", ErrUploadLockUnavailable, err)
	}

	var existing uploadLockState
	if json.Unmarshal(data, &existing) != nil || existing.ProcessID <= 0 {
		if info, statErr := os.Stat(lockFilePath); statErr == nil && time.Since(info.ModTime()) < lockOwnerlessGrace {
			return fmt.Errorf("upload of %s is locked by an owner that has not identified itself yet", existing.LocalPath)
		}
	} else if existing.ProcessID != owner.ProcessID && isProcessRunning(existing.ProcessID) {
		return fmt.Errorf("upload locked by another process (PID %d) since %s",
			existing.ProcessID, existing.AcquiredAt.Format(time.RFC3339))
	}

	// Nothing owns it. A lock naming our own PID cannot belong to a live owner
	// other than us, and a live one of ours would have been caught by the
	// in-process claim before we got here: either it is ours and released, or
	// the OS gave us a dead process's PID, and refusing would wedge every retry
	// after a crash.
	if beforeLockTakeover != nil {
		beforeLockTakeover(owner)
	}
	return takeAbandonedLock(lockFilePath, existing)
}

// beforeLockTakeover runs between reading the record that judged a lock
// abandoned and clearing that file. Only a test sets it: that window is where
// two acquirers which both read the same dead owner have to be serialized.
var beforeLockTakeover func(owner uploadLockState)

// takeAbandonedLock clears a lock file the caller has judged abandoned, without
// letting two judgements of the same record both take effect. Removing the path
// is not that step: two acquirers that read the same dead owner both remove,
// and the second removes whatever the first put there — the first one's live
// lock. Renaming is, because the source stops existing the moment the winner's
// rename lands, so the loser either finds nothing to move or finds a different
// record under the same name.
func takeAbandonedLock(lockFilePath string, observed uploadLockState) error {
	stalePath := lockFilePath + ".stale-" + newLockToken()
	if err := os.Rename(lockFilePath, stalePath); err != nil {
		if os.IsNotExist(err) {
			return nil // Another acquirer moved it first; race for the create.
		}
		return fmt.Errorf("%w: failed to clear abandoned upload lock: %w", ErrUploadLockUnavailable, err)
	}

	var moved uploadLockState
	if data, readErr := os.ReadFile(stalePath); readErr == nil && json.Unmarshal(data, &moved) == nil &&
		(moved.ProcessID != observed.ProcessID || moved.OwnerToken != observed.OwnerToken) {
		// The lock was retaken between the read that judged it abandoned and
		// this rename, so what we moved aside belongs to whoever took it. Put it
		// back and let the caller judge the lock again.
		if restoreErr := os.Rename(stalePath, lockFilePath); restoreErr != nil {
			// Not ErrUploadLockUnavailable: something does own this upload, and
			// carrying on without a lock is exactly what must not happen there.
			return fmt.Errorf("upload lock was retaken while being cleared and could not be restored: %w", restoreErr)
		}
		return nil
	}

	if err := os.Remove(stalePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: failed to clear abandoned upload lock: %w", ErrUploadLockUnavailable, err)
	}
	return nil
}

// ReleaseUploadLock releases an upload lock.
func ReleaseUploadLock(lock *UploadLock) {
	if lock == nil {
		return
	}
	defer releaseLocalLock(lock.LockFilePath)

	if data, err := os.ReadFile(lock.LockFilePath); err == nil {
		var currentLock uploadLockState
		// The token, not just the PID: a lock retaken by a later process that
		// happens to have our PID is not ours to delete.
		if json.Unmarshal(data, &currentLock) == nil &&
			(currentLock.ProcessID != lock.ProcessID || currentLock.OwnerToken != lock.OwnerToken) {
			return // Lock taken by another owner
		}
	}
	if err := os.Remove(lock.LockFilePath); err != nil && !os.IsNotExist(err) {
		log.Printf("Warning: Failed to release upload lock: %v", err)
	}
}

// isProcessRunning is a variable so a test can decide which PIDs are alive:
// a lock's owner has to be a process the test cannot create or kill portably.
var isProcessRunning = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		// Unix never fails here. On Windows this is OpenProcess failing, which
		// means there is no such process.
		return false
	}

	err = process.Signal(syscall.Signal(0))
	switch {
	case err == nil:
		return true
	case errors.Is(err, os.ErrProcessDone), errors.Is(err, syscall.ESRCH):
		return false
	case errors.Is(err, syscall.EPERM):
		return true // Alive, just owned by another user.
	default:
		// Windows refuses signal 0 outright, so the only evidence there is the
		// handle FindProcess opened above — and it only opens for a process
		// that exists. Reading that as "dead", which is what comparing the
		// error against nil did, let any second process take a live owner's
		// lock on Windows.
		return true
	}
}

// =============================================================================
// Path building helpers
// =============================================================================

// BuildObjectKey constructs an S3 object key or Azure blob path.
// For S3: "{pathBase}/{filename}-{randomSuffix}"
// For Azure with empty pathBase: "{filename}-{randomSuffix}"
func BuildObjectKey(pathBase, filename, randomSuffix string) string {
	objectName := fmt.Sprintf("%s-%s", filename, randomSuffix)
	if pathBase != "" {
		return fmt.Sprintf("%s/%s", pathBase, objectName)
	}
	return objectName
}
