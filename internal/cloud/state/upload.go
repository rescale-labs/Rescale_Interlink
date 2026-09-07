// Package state provides shared resume state types and I/O for upload/download operations.
// This package breaks the import cycle between upload/, download/, transfer/, and providers/.
//
// The cycle was: upload → providers → transfer → upload
// Now: upload → providers → transfer → state (no cycle)
package state

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// ErrUploadLockUnavailable reports that there is no lock file and the directory
// refuses to take one — read-only, full, unwritable or gone. Nothing holds the
// upload in that case; there is simply nowhere to record that we do, which is a
// different answer from the contention errors and one a caller may choose to
// carry on past. It never covers a lock file that exists: one we cannot read,
// decode or clear may have a live owner behind it, and there is no answer to
// carry on past there.
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
		lock, err := createLockFile(lockFilePath, localPath, newLock, data)
		if err != nil {
			return nil, err
		}
		if lock != nil {
			return lock, nil
		}

		// Someone else got there first. Only clear it if its owner is gone.
		lock, err = clearAbandonedLock(lockFilePath, localPath, newLock, data)
		if err != nil {
			return nil, err
		}
		if lock != nil {
			return lock, nil
		}
	}

	return nil, fmt.Errorf("could not acquire upload lock for %s: it kept being retaken", localPath)
}

// createLockFile creates the lock file and records the owner in it. A nil lock
// with a nil error means the file is already there.
func createLockFile(lockFilePath, localPath string, newLock uploadLockState, data []byte) (*UploadLock, error) {
	file, err := os.OpenFile(lockFilePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil, nil
		}
		return nil, lockCreationError(localPath, "create", err)
	}
	if writeErr := writeAndClose(file, data); writeErr != nil {
		// A lock nobody can read is worse than no lock: remove it so the next
		// attempt is not blocked by our own half-written file.
		os.Remove(lockFilePath)
		return nil, lockCreationError(localPath, "write", writeErr)
	}
	return &UploadLock{
		LockFilePath: lockFilePath,
		ProcessID:    newLock.ProcessID,
		OwnerToken:   newLock.OwnerToken,
		AcquiredAt:   newLock.AcquiredAt,
	}, nil
}

// lockCreationError reports a failure to put a new lock file in place. It is
// only ErrUploadLockUnavailable when the filesystem itself refused to hold the
// file, because that is the one class of failure that also establishes there is
// no lock — and so no owner — at the path. Anything else is a plain refusal:
// the caller may carry on without a lock it could not create, never without one
// whose absence it could not establish.
func lockCreationError(localPath, verb string, err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission), errors.Is(err, fs.ErrNotExist),
		errors.Is(err, syscall.EROFS), errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return fmt.Errorf("%w: failed to %s lock file: %w", ErrUploadLockUnavailable, verb, err)
	default:
		return fmt.Errorf("cannot %s the upload lock of %s: %w", verb, localPath, err)
	}
}

func writeAndClose(file *os.File, data []byte) error {
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// clearAbandonedLock takes an existing lock file over when nothing owns it any
// more, and reports an error when something does. A nil lock with a nil error
// means the caller should race for the create again — not that it owns
// anything.
func clearAbandonedLock(lockFilePath, localPath string, owner uploadLockState, data []byte) (*UploadLock, error) {
	judged, err := os.ReadFile(lockFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Released while we looked; try to create it again.
		}
		return nil, inspectionError(localPath, err)
	}

	var existing uploadLockState
	if json.Unmarshal(judged, &existing) != nil || existing.ProcessID <= 0 {
		young, statErr := youngerThanGrace(lockFilePath)
		if statErr != nil {
			return nil, inspectionError(localPath, statErr)
		}
		if young {
			return nil, fmt.Errorf("upload of %s is locked by an owner that has not identified itself yet", localPath)
		}
	} else if existing.ProcessID != owner.ProcessID && isProcessRunning(existing.ProcessID) {
		return nil, fmt.Errorf("upload locked by another process (PID %d) since %s",
			existing.ProcessID, existing.AcquiredAt.Format(time.RFC3339))
	}

	// Nothing owns it. A lock naming our own PID cannot belong to a live owner
	// other than us, and a live one of ours would have been caught by the
	// in-process claim before we got here: either it is ours and released, or
	// the OS gave us a dead process's PID, and refusing would wedge every retry
	// after a crash.
	takeoverStep(takeoverJudged, owner)
	return takeAbandonedLock(lockFilePath, localPath, owner, data, judged)
}

// inspectionError reports that an existing lock could not be read or examined.
// It is deliberately not ErrUploadLockUnavailable: a lock we cannot inspect may
// have a live owner behind it, and that is the one case which must never be
// read as "nothing holds this upload, carry on without a lock".
func inspectionError(localPath string, err error) error {
	return fmt.Errorf("cannot inspect the existing upload lock of %s: %w", localPath, err)
}

// youngerThanGrace reports whether a file was last written inside the grace
// window. It is how both a lock that names no owner and a marker left by a
// crashed taker are judged: neither carries an owner whose liveness could be
// checked, so elapsed time is the only evidence there is.
func youngerThanGrace(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return time.Since(info.ModTime()) < lockOwnerlessGrace, nil
}

// lockTakeoverStep runs at the points of an abandoned-lock takeover where the
// interleaving of a second acquirer decides whether two of them can end up
// owning one upload. Only a test sets it.
var lockTakeoverStep func(phase string, owner uploadLockState)

const (
	// takeoverJudged: the existing record has been judged abandoned and nothing
	// has been taken yet.
	takeoverJudged = "judged"
	// takeoverMarked: this acquirer holds the takeover marker, so it is the only
	// one that may act on its judgement.
	takeoverMarked = "marked"
	// takeoverCleared: the abandoned lock is gone and the fresh one is not in
	// place yet, so the pathname is free for anyone to create.
	takeoverCleared = "cleared"
)

// takeoverMarkerSuffix names the file that serializes taking over an abandoned
// lock, alongside the lock it clears.
const takeoverMarkerSuffix = ".takeover"

func takeoverStep(phase string, owner uploadLockState) {
	if lockTakeoverStep != nil {
		lockTakeoverStep(phase, owner)
	}
}

// takeAbandonedLock clears a lock file the caller has judged abandoned and puts
// the caller's own in its place, without letting two judgements of the same
// record both take effect.
//
// Nothing here renames or moves the existing lock. A rename replaces whatever
// is at its destination, so an acquirer that moved a lock aside and put it back
// would overwrite any lock created while the pathname was free, and the two
// owners it left would share one resume state. The takeover marker is what
// serializes instead: it is created with O_EXCL, so only one of the acquirers
// that judged a record abandoned may act on that judgement, and it is held
// until the fresh lock has been written, so the pathname is never free outside
// the marker's cover. Losing the create at the end means someone else took the
// free pathname, which the caller finds by judging the lock again.
//
// A nil lock with a nil error means exactly that: judge the lock again.
func takeAbandonedLock(lockFilePath, localPath string, owner uploadLockState, data, judged []byte) (*UploadLock, error) {
	markerPath := lockFilePath + takeoverMarkerSuffix
	if err := acquireTakeoverMarker(markerPath, localPath, owner, data); err != nil {
		return nil, err
	}
	defer os.Remove(markerPath)
	takeoverStep(takeoverMarked, owner)

	// The record has to still be the one that was judged abandoned: the lock may
	// have been released and retaken while we reached for the marker.
	current, err := os.ReadFile(lockFilePath)
	switch {
	case err == nil:
		if !bytes.Equal(current, judged) {
			return nil, nil
		}
	case os.IsNotExist(err):
		return nil, nil
	default:
		return nil, inspectionError(localPath, err)
	}

	if err := os.Remove(lockFilePath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("cannot clear the abandoned upload lock of %s: %w", localPath, err)
	}
	takeoverStep(takeoverCleared, owner)

	return createLockFile(lockFilePath, localPath, owner, data)
}

// acquireTakeoverMarker claims the right to clear one abandoned lock, and
// reports a refusal when another acquirer already holds it. The marker carries
// the same record its holder is about to install, so a marker left behind by a
// taker that died names the process to check for liveness.
func acquireTakeoverMarker(markerPath, localPath string, owner uploadLockState, data []byte) error {
	// Two passes: one to find the marker, one to claim it after clearing a
	// marker whose creator is gone.
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			if writeErr := writeAndClose(file, data); writeErr != nil {
				os.Remove(markerPath)
				return fmt.Errorf("cannot clear the abandoned upload lock of %s: %w", localPath, writeErr)
			}
			return nil
		}
		if !os.IsExist(err) {
			return fmt.Errorf("cannot clear the abandoned upload lock of %s: %w", localPath, err)
		}

		cleared, err := clearAbandonedTakeoverMarker(markerPath, owner)
		if err != nil {
			return inspectionError(localPath, err)
		}
		if !cleared {
			return fmt.Errorf("another process is clearing the lock of %s", localPath)
		}
	}
	return fmt.Errorf("another process is clearing the lock of %s", localPath)
}

// clearAbandonedTakeoverMarker removes a marker whose creator died mid-takeover,
// reporting whether the marker is gone. A live taker's marker is honoured
// however old it is; a dead one is only cleared once it is past the grace
// window, so a marker still being written is never mistaken for one nobody will
// finish.
func clearAbandonedTakeoverMarker(markerPath string, owner uploadLockState) (bool, error) {
	data, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil // The taker finished while we looked.
		}
		return false, err
	}

	var taker uploadLockState
	if json.Unmarshal(data, &taker) == nil && taker.ProcessID > 0 &&
		taker.ProcessID != owner.ProcessID && isProcessRunning(taker.ProcessID) {
		return false, nil
	}
	young, err := youngerThanGrace(markerPath)
	if err != nil {
		return false, err
	}
	if young {
		return false, nil
	}
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
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
