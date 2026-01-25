// Package state provides shared resume state types and I/O for upload/download operations.
// This package breaks the import cycle between upload/, download/, transfer/, and providers/.
//
// The cycle was: upload → providers → transfer → upload
// Now: upload → providers → transfer → state (no cycle)
package state

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"
)

// UploadResumeState tracks the state of an in-progress upload for resumption.
// Supports both legacy (FormatVersion=0) and streaming (FormatVersion=1) encryption.
type UploadResumeState struct {
	LocalPath      string          `json:"local_path"`      // Original source file path
	EncryptedPath  string          `json:"encrypted_path"`  // Encrypted temp file path (legacy v0 only)
	ObjectKey      string          `json:"object_key"`      // S3 object key or Azure blob path
	UploadID       string          `json:"upload_id"`       // S3 multipart upload ID (empty for Azure)
	TotalSize      int64           `json:"total_size"`      // Size of encrypted file
	OriginalSize   int64           `json:"original_size"`   // Size of original file (for validation)
	UploadedBytes  int64           `json:"uploaded_bytes"`  // Bytes uploaded so far
	CompletedParts []CompletedPart `json:"completed_parts"` // S3 parts
	BlockIDs       []string        `json:"block_ids"`       // Azure uncommitted block IDs
	EncryptionKey  string          `json:"encryption_key"`  // Base64-encoded encryption key (legacy v0)
	IV             string          `json:"iv"`              // Base64-encoded IV (legacy v0)
	RandomSuffix   string          `json:"random_suffix"`   // Random suffix for object name
	CreatedAt      time.Time       `json:"created_at"`
	LastUpdate     time.Time       `json:"last_update"`
	StorageType    string          `json:"storage_type"` // "S3Storage" or "AzureStorage"

	// Streaming encryption fields (FormatVersion=1)
	FormatVersion int    `json:"format_version"` // 0=legacy, 1=streaming
	MasterKey     string `json:"master_key"`     // Base64-encoded master key (v1 only)
	FileId        string `json:"file_id"`        // Base64-encoded file identifier (v1 only)
	PartSize      int64  `json:"part_size"`      // Bytes per plaintext part (v1 only)

	// Process locking fields
	ProcessID      int       `json:"process_id"`       // PID of owning process
	LockAcquiredAt time.Time `json:"lock_acquired_at"` // When lock was acquired
}

// CompletedPart represents a completed upload part (S3-specific).
type CompletedPart struct {
	PartNumber int32  `json:"part_number"`
	ETag       string `json:"etag"`
}

// MaxResumeAge is the maximum age of a resume state before it's considered expired.
// Aligned with AWS multipart upload expiry (7 days) and Azure uncommitted block expiry (7 days).
const MaxResumeAge = 7 * 24 * time.Hour

// LockStaleTimeout is how long a lock can be held before it's considered stale.
const LockStaleTimeout = 30 * time.Minute

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
	AcquiredAt   time.Time
}

type uploadLockState struct {
	ProcessID  int       `json:"process_id"`
	AcquiredAt time.Time `json:"acquired_at"`
	LocalPath  string    `json:"local_path"`
}

// AcquireUploadLock attempts to acquire an exclusive lock for uploading a file.
func AcquireUploadLock(localPath string) (*UploadLock, error) {
	lockFilePath := localPath + ".upload.lock"
	currentPID := os.Getpid()

	// Check existing lock
	if data, err := os.ReadFile(lockFilePath); err == nil {
		var existingLock uploadLockState
		if json.Unmarshal(data, &existingLock) == nil {
			lockAge := time.Since(existingLock.AcquiredAt)
			if lockAge < LockStaleTimeout && isProcessRunning(existingLock.ProcessID) && existingLock.ProcessID != currentPID {
				return nil, fmt.Errorf("upload locked by another process (PID %d)", existingLock.ProcessID)
			}
		}
		os.Remove(lockFilePath)
	}

	// Create new lock
	newLock := uploadLockState{
		ProcessID:  currentPID,
		AcquiredAt: time.Now(),
		LocalPath:  localPath,
	}

	data, _ := json.MarshalIndent(newLock, "", "  ")
	tmpFilePath := lockFilePath + ".tmp"
	if err := os.WriteFile(tmpFilePath, data, 0600); err != nil {
		return nil, fmt.Errorf("failed to write lock file: %w", err)
	}

	if err := os.Rename(tmpFilePath, lockFilePath); err != nil {
		os.Remove(tmpFilePath)
		return nil, fmt.Errorf("failed to create lock file: %w", err)
	}

	return &UploadLock{
		LockFilePath: lockFilePath,
		ProcessID:    currentPID,
		AcquiredAt:   newLock.AcquiredAt,
	}, nil
}

// ReleaseUploadLock releases an upload lock.
func ReleaseUploadLock(lock *UploadLock) {
	if lock == nil {
		return
	}
	if data, err := os.ReadFile(lock.LockFilePath); err == nil {
		var currentLock uploadLockState
		if json.Unmarshal(data, &currentLock) == nil && currentLock.ProcessID != lock.ProcessID {
			return // Lock taken by another process
		}
	}
	if err := os.Remove(lock.LockFilePath); err != nil && !os.IsNotExist(err) {
		log.Printf("Warning: Failed to release upload lock: %v", err)
	}
}

func isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
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
