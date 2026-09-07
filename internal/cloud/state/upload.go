// Package state provides shared resume state types and I/O for upload/download operations.
// This package breaks the import cycle between upload/, download/, transfer/, and providers/.
//
// The cycle was: upload → providers → transfer → upload
// Now: upload → providers → transfer → state (no cycle)
package state

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rescale/rescale-int/internal/config"
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
	ProcessID  int    `json:"process_id"`
	OwnerToken string `json:"owner_token,omitempty"`
	// Host and Owner name the machine and the user the owning process runs as,
	// and InstallID the installation it belongs to. A PID is only meaningful
	// inside one installation: two machines can be configured with the same
	// hostname and can carry the same uid, so host and user are what an
	// operator reads and the installation identifier is what decides whether
	// the PID may be tested for liveness at all. A record carrying no
	// installation identifier was written before v4.9.9 — or by a v4.9.9
	// process that had nowhere to keep one — and is never reclaimed
	// automatically.
	Host       string    `json:"host,omitempty"`
	Owner      string    `json:"owner,omitempty"`
	InstallID  string    `json:"install_id,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
	LocalPath  string    `json:"local_path"`
}

// processLockToken tells this run of the process apart from any other owner
// that ever wrote a lock file. A PID cannot do that on its own: the OS hands a
// dead process's PID to a new one, so a lock left behind by a crashed run can
// name the PID of the run that finds it.
var processLockToken = newLockToken()

// lockHost and lockOwner name the machine and the user account a lock record is
// written by. Nothing is decided on them — an operator reads them to judge
// whether the upload they name is still running. They are variables so a test
// can stand in for another machine or another login.
var (
	lockHost  = currentLockHost()
	lockOwner = currentLockOwner()
)

// currentLockHost names this machine, or nothing when the OS will not say.
func currentLockHost() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

// currentLockOwner names the user this process runs as, as one stable string:
// the numeric uid where the OS has one, and the account itself on Windows,
// where os.Getuid reports -1.
func currentLockOwner() string {
	if uid := os.Getuid(); uid >= 0 {
		return strconv.Itoa(uid)
	}
	if current, err := user.Current(); err == nil {
		if current.Username != "" {
			return current.Username
		}
		return current.Uid
	}
	return os.Getenv("USERNAME")
}

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
// because it is old: a multi-hour upload is still an owner — and only when that
// owner belongs to this installation, because that is the only place a PID can
// be tested for liveness. Anything else — another installation, a record written
// before this version, a file its creator never wrote a record into — is refused
// with the file to delete rather than taken.
func AcquireUploadLock(localPath string) (*UploadLock, error) {
	lockFilePath := lockFilePathFor(localPath)

	if !claimLocalLock(lockFilePath) {
		return nil, fmt.Errorf("upload of %s is already in progress in this process", localPath)
	}

	lock, err := acquireLockFile(lockFilePath, localPath, uploadLockState{
		ProcessID:  os.Getpid(),
		OwnerToken: processLockToken,
		Host:       lockHost,
		Owner:      lockOwner,
		InstallID:  currentInstallID(),
		AcquiredAt: time.Now(),
		LocalPath:  localPath,
	})
	if err != nil {
		releaseLocalLock(lockFilePath)
		return nil, err
	}
	return lock, nil
}

// currentInstallID names the installation this process belongs to, or nothing
// when there is nowhere to keep the identifier. Nothing is lost for creation:
// only reclamation consults it, so a process without one takes locks as usual
// and reclaims none — and the records it writes are reclaimed by nobody either,
// which is the safe direction for both.
func currentInstallID() string {
	id, err := config.InstallID()
	if err != nil {
		return ""
	}
	return id
}

// acquireLockFile creates the lock file, or reclaims one whose owner is gone and
// then creates it. Creating with O_EXCL is the whole cross-process exclusion:
// it is the one step of this two acquirers cannot both win, and everything else
// here exists to decide whether the pathname may be freed for it.
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
		lock, err = reclaimStaleLock(lockFilePath, localPath, newLock, data)
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
	takeoverStep(takeoverCreated, newLock)
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

// reclaimStaleLock takes an existing lock file over when nothing owns it any
// more, and reports an error when something does — or when nothing here can
// establish that nothing does. Judging is a read and takes nothing: every
// refusal below leaves the file exactly as it was found. A nil lock with a nil
// error means the caller should judge the pathname again — not that it owns
// anything.
func reclaimStaleLock(lockFilePath, localPath string, owner uploadLockState, data []byte) (*UploadLock, error) {
	judged, record, err := readLockRecord(lockFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Released while we looked; try to create it again.
		}
		return nil, inspectionError(localPath, err)
	}

	var existing uploadLockState
	switch {
	case json.Unmarshal(record, &existing) != nil || existing.ProcessID <= 0:
		// A file its creator has not written a record into yet, or one written
		// by something else entirely. There is no owner in it to test and no
		// age at which that changes: a creator between its O_EXCL and its write
		// presents exactly this, and waiting out a grace period only turns a
		// live upload into one two processes run.
		return nil, unidentifiedLockError(localPath, lockFilePath, judged)
	case !inThisInstallation(existing, owner.InstallID):
		return nil, foreignLockError(localPath, lockFilePath, existing)
	case existing.ProcessID != owner.ProcessID && isProcessRunning(existing.ProcessID):
		return nil, fmt.Errorf("upload locked by another process (PID %d) since %s",
			existing.ProcessID, existing.AcquiredAt.Format(time.RFC3339))
	}

	// Nothing owns it, and it was written by this installation — so the PID it
	// names is one this machine could test. A lock naming our own PID cannot
	// belong to a live owner other than us, and a live one of ours would have
	// been caught by the in-process claim before we got here: either it is ours
	// and released, or the OS gave us a dead process's PID, and refusing would
	// wedge every retry after a crash.
	takeoverStep(takeoverJudged, owner)
	return takeStaleLock(lockFilePath, localPath, owner, data, record)
}

// inThisInstallation reports whether a record was written by the installation
// this process belongs to, which is the only place its PID means anything. Host
// and user do not establish that: two machines can be configured with one
// hostname and can carry one uid, and a PID recorded on one of them names a
// different process — or none — on the other. A record naming no installation
// was written before v4.9.9, when there was none to name.
func inThisInstallation(existing uploadLockState, installID string) bool {
	return installID != "" && existing.InstallID == installID
}

// foreignLockError refuses a lock that this process must not clear on its own.
// A PID recorded by another installation names a process of ours or none at all,
// so its liveness establishes nothing, and two installations reclaiming one
// record could each remove the other's replacement. What is left is to say who
// holds it and leave the decision to whoever can make it.
func foreignLockError(localPath, lockFilePath string, existing uploadLockState) error {
	return fmt.Errorf("upload of %s is locked by PID %d on host %s as user %s since %s; "+
		"if that upload is not running, delete %s to release it",
		localPath, existing.ProcessID, orUnknown(existing.Host), orUnknown(existing.Owner),
		existing.AcquiredAt.Format(time.RFC3339), lockFilePath)
}

// unidentifiedLockError refuses a lock file that names no owner at all: there is
// no installation, user or PID in it to judge, so nothing here can establish
// that clearing it is safe, however long ago it was written.
func unidentifiedLockError(localPath, lockFilePath string, judged os.FileInfo) error {
	return fmt.Errorf("upload of %s is locked by a record that names no owner, written %s; "+
		"if no upload of that file is running, delete %s to release it",
		localPath, judged.ModTime().Format(time.RFC3339), lockFilePath)
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// readLockRecord reads a lock file and the identity of the file it read from,
// through one handle. A stat taken separately from the read can describe a
// different file from the record — the pathname is exactly what acquirers hand
// to each other — and the takeover that follows would then clear a file it never
// judged. The handle also has to be what the identity comes from: os.Stat of a
// pathname on Windows leaves the file index to be fetched later, by reopening
// that pathname, so an identity taken that way would resolve to whichever file
// the comparison finds there and never report a replacement at all.
func readLockRecord(lockFilePath string) (os.FileInfo, []byte, error) {
	file, err := os.Open(lockFilePath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	record, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, err
	}
	return info, record, nil
}

// inspectionError reports that an existing lock could not be read or examined.
// It is deliberately not ErrUploadLockUnavailable: a lock we cannot inspect may
// have a live owner behind it, and that is the one case which must never be
// read as "nothing holds this upload, carry on without a lock".
func inspectionError(localPath string, err error) error {
	return fmt.Errorf("cannot inspect the existing upload lock of %s: %w", localPath, err)
}

// lockTakeoverStep runs at the points of an acquisition where the interleaving
// of a second acquirer decides whether two of them can end up owning one upload.
// Only a test sets it.
var lockTakeoverStep func(phase string, owner uploadLockState)

// The phases in the order acquisition reaches them: created for an acquirer that
// finds the pathname free, or judged and cleared and then created for one that
// has to reclaim it first.
const (
	// takeoverJudged: the existing record has been judged stale and nothing has
	// been taken yet, so the judgement can still stop being true.
	takeoverJudged = "judged"
	// takeoverCleared: the stale lock is gone and the fresh one is not in place
	// yet, so the pathname is free to anyone.
	takeoverCleared = "cleared"
	// takeoverCreated: the lock file exists and the record naming its owner is
	// not written into it yet, which is the window a stalled creator sits in.
	takeoverCreated = "created"
)

func takeoverStep(phase string, owner uploadLockState) {
	if lockTakeoverStep != nil {
		lockTakeoverStep(phase, owner)
	}
}

// staleSuffix begins the name of the claim a reclaimer takes on the record it
// has judged stale. The rest of the name is that record's own digest, so every
// reclaimer of one record reaches for one name and no two records share one.
const staleSuffix = ".stale-"

// takeStaleLock replaces a lock file the caller has judged stale with the
// caller's own.
//
// Judging is a read and replacing is a write, and the pathname alone keeps no
// second reclaimer out in between. Every earlier attempt at closing that gap
// failed for the same reason: moving the lock out of the way takes whatever is
// at the pathname, including the live lock a faster reclaimer has already put
// there, and a guard file placed anywhere the system will keep it is a file the
// system also clears, which splits the exclusion in two. What decides it here is
// an O_EXCL create of a name derived from the judged record: exactly one
// reclaimer of that record can hold it, it is taken before the lock file is
// touched at all, and a reclaimer that loses has changed nothing.
//
// Holding it, the record cannot change under us. The only processes that remove
// a lock file are its own owner — which this one has established is gone — and
// the reclaimer holding this claim. So the re-read below either finds the record
// that was judged, and it is ours to clear, or finds that another acquirer has
// already been through here, and the caller judges what it left instead.
//
// A nil lock with a nil error means exactly that: judge the pathname again.
func takeStaleLock(lockFilePath, localPath string, owner uploadLockState, data, judged []byte) (*UploadLock, error) {
	digest := sha256.Sum256(judged)
	claimPath := lockFilePath + staleSuffix + hex.EncodeToString(digest[:8])

	claim, err := os.OpenFile(claimPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil, reclamationInProgressError(localPath, claimPath)
		}
		return nil, fmt.Errorf("cannot clear the abandoned upload lock of %s: %w", localPath, err)
	}
	_ = claim.Close()
	defer os.Remove(claimPath)

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

// reclamationInProgressError refuses to reclaim a lock another acquirer is
// already reclaiming. The claim is held for the microseconds between judging a
// record and replacing it, so this is ordinarily a transient answer; one an
// acquirer died holding is not, which is why the file is named.
func reclamationInProgressError(localPath, claimPath string) error {
	return fmt.Errorf("the upload lock of %s is being reclaimed by another transfer; "+
		"if none is running, delete %s to release it", localPath, claimPath)
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
