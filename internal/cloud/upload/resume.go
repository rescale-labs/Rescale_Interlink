package upload

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// UploadResumeState tracks the state of an in-progress upload for resumption
type UploadResumeState struct {
	LocalPath      string          `json:"local_path"`     // Original source file path
	EncryptedPath  string          `json:"encrypted_path"` // Encrypted temp file path (for reuse)
	ObjectKey      string          `json:"object_key"`     // S3 object key or Azure blob path
	UploadID       string          `json:"upload_id"`      // S3 multipart upload ID (empty for Azure)
	TotalSize      int64           `json:"total_size"`     // Size of encrypted file
	OriginalSize   int64           `json:"original_size"`  // Size of original file (for validation)
	UploadedBytes  int64           `json:"uploaded_bytes"`
	CompletedParts []CompletedPart `json:"completed_parts"` // S3 parts
	BlockIDs       []string        `json:"block_ids"`       // Azure uncommitted block IDs
	EncryptionKey  string          `json:"encryption_key"`  // Base64-encoded encryption key (for reuse)
	IV             string          `json:"iv"`              // Base64-encoded IV (for reuse)
	RandomSuffix   string          `json:"random_suffix"`   // Random suffix for object name (for reuse)
	CreatedAt      time.Time       `json:"created_at"`
	LastUpdate     time.Time       `json:"last_update"`
	StorageType    string          `json:"storage_type"` // "S3Storage" or "AzureStorage"
}

// CompletedPart represents a completed upload part (S3-specific)
type CompletedPart struct {
	PartNumber int32  `json:"part_number"`
	ETag       string `json:"etag"`
}

const (
	// MaxResumeAge is the maximum age of a resume state before it's considered expired
	// Aligned with AWS multipart upload expiry (7 days) and Azure uncommitted block expiry (7 days)
	MaxResumeAge = 7 * 24 * time.Hour
)

// SaveUploadState saves the upload resume state to a sidecar file
// The state file is saved atomically using a temporary file + rename
func SaveUploadState(state *UploadResumeState, localPath string) error {
	stateFilePath := localPath + ".upload.resume"
	tmpFilePath := stateFilePath + ".tmp"

	// Marshal state to JSON with indentation for readability
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal upload state: %w", err)
	}

	// Write to temporary file
	if err := os.WriteFile(tmpFilePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp state file: %w", err)
	}

	// Atomic rename (on POSIX systems)
	if err := os.Rename(tmpFilePath, stateFilePath); err != nil {
		os.Remove(tmpFilePath) // Clean up temp file on failure
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// LoadUploadState loads the upload resume state from a sidecar file
func LoadUploadState(localPath string) (*UploadResumeState, error) {
	stateFilePath := localPath + ".upload.resume"

	data, err := os.ReadFile(stateFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No resume state exists, return nil without error
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state UploadResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state file: %w", err)
	}

	return &state, nil
}

// ValidateUploadState validates that a resume state is still usable
func ValidateUploadState(state *UploadResumeState, localPath string) error {
	if state == nil {
		return fmt.Errorf("state is nil")
	}

	// Check that source file exists
	fileInfo, err := os.Stat(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source file no longer exists")
		}
		return fmt.Errorf("failed to stat source file: %w", err)
	}

	// Check that ORIGINAL file size matches (not encrypted file size)
	expectedSize := state.OriginalSize
	if expectedSize == 0 {
		// Backwards compatibility: if OriginalSize not set, fall back to TotalSize
		expectedSize = state.TotalSize
	}
	if fileInfo.Size() != expectedSize {
		return fmt.Errorf("source file size changed (was %d, now %d)", expectedSize, fileInfo.Size())
	}

	// Check that state is not too old (7 days)
	age := time.Since(state.CreatedAt)
	if age > MaxResumeAge {
		return fmt.Errorf("resume state expired (age: %s, max: %s)", age, MaxResumeAge)
	}

	// Check that local path matches
	if state.LocalPath != localPath {
		return fmt.Errorf("local path mismatch (state: %s, actual: %s)", state.LocalPath, localPath)
	}

	// Check that encrypted temp file still exists (if specified)
	if state.EncryptedPath != "" {
		if _, err := os.Stat(state.EncryptedPath); err != nil {
			return fmt.Errorf("encrypted temp file no longer exists: %s", state.EncryptedPath)
		}
	}

	return nil
}

// DeleteUploadState deletes the upload resume state file
func DeleteUploadState(localPath string) error {
	stateFilePath := localPath + ".upload.resume"

	err := os.Remove(stateFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete state file: %w", err)
	}

	return nil
}

// GetResumeFilePath returns the path to the resume state file for a given local file
func GetResumeFilePath(localPath string) string {
	return localPath + ".upload.resume"
}

// ResumeStateExists checks if a resume state file exists for the given local path
func ResumeStateExists(localPath string) bool {
	stateFilePath := GetResumeFilePath(localPath)
	_, err := os.Stat(stateFilePath)
	return err == nil
}

// ConvertToSDKParts converts our CompletedPart slice to AWS SDK types.CompletedPart slice
func ConvertToSDKParts(parts []CompletedPart) []types.CompletedPart {
	if parts == nil {
		return nil
	}

	sdkParts := make([]types.CompletedPart, len(parts))
	for i, part := range parts {
		// Create a new string for ETag to avoid pointer issues
		etagCopy := part.ETag
		partNumCopy := part.PartNumber

		sdkParts[i] = types.CompletedPart{
			ETag:       &etagCopy,
			PartNumber: &partNumCopy,
		}
	}

	return sdkParts
}

// ConvertFromSDKParts converts AWS SDK types.CompletedPart slice to our CompletedPart slice
func ConvertFromSDKParts(sdkParts []types.CompletedPart) []CompletedPart {
	if sdkParts == nil {
		return nil
	}

	parts := make([]CompletedPart, len(sdkParts))
	for i, sdkPart := range sdkParts {
		etag := ""
		if sdkPart.ETag != nil {
			etag = *sdkPart.ETag
		}

		partNum := int32(0)
		if sdkPart.PartNumber != nil {
			partNum = *sdkPart.PartNumber
		}

		parts[i] = CompletedPart{
			PartNumber: partNum,
			ETag:       etag,
		}
	}

	return parts
}

// GetResumeProgress returns the resume progress as a percentage (0.0 to 1.0)
func GetResumeProgress(state *UploadResumeState) float64 {
	if state == nil || state.TotalSize == 0 {
		return 0.0
	}
	return float64(state.UploadedBytes) / float64(state.TotalSize)
}

// FormatResumeMessage returns a human-readable message about resume status
func FormatResumeMessage(state *UploadResumeState) string {
	if state == nil {
		return "No resume state available"
	}

	progress := GetResumeProgress(state) * 100.0
	partsCount := len(state.CompletedParts)
	blocksCount := len(state.BlockIDs)

	var details string
	if state.StorageType == "S3Storage" && partsCount > 0 {
		details = fmt.Sprintf("%d parts uploaded", partsCount)
	} else if state.StorageType == "AzureStorage" && blocksCount > 0 {
		details = fmt.Sprintf("%d blocks uploaded", blocksCount)
	} else {
		details = "starting upload"
	}

	age := time.Since(state.LastUpdate)
	ageStr := ""
	if age < time.Minute {
		ageStr = fmt.Sprintf("%ds ago", int(age.Seconds()))
	} else if age < time.Hour {
		ageStr = fmt.Sprintf("%dm ago", int(age.Minutes()))
	} else {
		ageStr = fmt.Sprintf("%dh ago", int(age.Hours()))
	}

	return fmt.Sprintf("Resuming upload from %.1f%% (%s, last updated %s)",
		progress, details, ageStr)
}

// ShouldPromptForResume determines if we should prompt the user about resuming
// Returns true if a valid resume state exists and is recent enough to be useful
func ShouldPromptForResume(localPath string) (bool, *UploadResumeState, error) {
	state, err := LoadUploadState(localPath)
	if err != nil {
		return false, nil, err
	}

	if state == nil {
		return false, nil, nil
	}

	// Validate the state
	if err := ValidateUploadState(state, localPath); err != nil {
		// Invalid state - don't prompt, just clean it up
		DeleteUploadState(localPath)
		return false, nil, nil
	}

	// Only prompt if we have meaningful progress (> 5%)
	progress := GetResumeProgress(state)
	if progress < 0.05 {
		return false, state, nil
	}

	return true, state, nil
}

// CleanupExpiredResume safely deletes expired encrypted temp file and resume state
// Only deletes files that:
// 1. Are explicitly referenced in the resume state
// 2. Match our naming pattern (.FILENAME-DIGITS.encrypted)
// 3. Are in the expected directory
func CleanupExpiredResume(state *UploadResumeState, originalPath string, outputWriter io.Writer) {
	if state == nil {
		return
	}

	// Try to delete encrypted temp file if specified
	if state.EncryptedPath != "" {
		// Safety checks before deletion
		expectedDir := filepath.Dir(originalPath)
		actualDir := filepath.Dir(state.EncryptedPath)

		if expectedDir != actualDir {
			// Encrypted file in unexpected directory - don't delete
			if outputWriter != nil {
				fmt.Fprintf(outputWriter, "Warning: Encrypted file in unexpected location, skipping cleanup: %s\n",
					state.EncryptedPath)
			}
			DeleteUploadState(originalPath)
			return
		}

		baseName := filepath.Base(state.EncryptedPath)
		if !strings.HasPrefix(baseName, ".") || !strings.HasSuffix(baseName, ".encrypted") {
			// Doesn't match our pattern - don't delete
			if outputWriter != nil {
				fmt.Fprintf(outputWriter, "Warning: Encrypted file doesn't match pattern, skipping cleanup: %s\n",
					state.EncryptedPath)
			}
			DeleteUploadState(originalPath)
			return
		}

		// Safe to delete
		if _, err := os.Stat(state.EncryptedPath); err == nil {
			if outputWriter != nil {
				age := time.Since(state.CreatedAt)
				fmt.Fprintf(outputWriter, "Cleaning up expired encrypted temp file (age: %dd): %s\n",
					int(age.Hours()/24), filepath.Base(state.EncryptedPath))
			}
			if err := os.Remove(state.EncryptedPath); err != nil {
				if outputWriter != nil {
					fmt.Fprintf(outputWriter, "Warning: Failed to delete encrypted file: %v\n", err)
				}
			}
		}
	}

	// Delete resume state
	DeleteUploadState(originalPath)
}

// CleanupExpiredResumesInDirectory scans a directory for expired resume states
// and cleans them up. This is called opportunistically when uploading from a directory.
// verbose=false for silent cleanup during normal uploads
// verbose=true for explicit cleanup commands
func CleanupExpiredResumesInDirectory(dir string, verbose bool) {
	// Find all resume state files in this directory
	pattern := filepath.Join(dir, "*.upload.resume")
	resumeFiles, err := filepath.Glob(pattern)
	if err != nil || len(resumeFiles) == 0 {
		return // No resume files or error
	}

	for _, resumeFile := range resumeFiles {
		// Load the resume state
		data, err := os.ReadFile(resumeFile)
		if err != nil {
			continue // Skip unreadable files
		}

		var state UploadResumeState
		if err := json.Unmarshal(data, &state); err != nil {
			continue // Skip corrupted state files
		}

		// Check if expired (7+ days)
		age := time.Since(state.CreatedAt)
		if age <= MaxResumeAge {
			continue // Not expired, skip
		}

		// Expired! Clean it up
		originalPath := strings.TrimSuffix(resumeFile, ".upload.resume")
		var outputWriter io.Writer
		if verbose {
			outputWriter = os.Stdout
		}
		CleanupExpiredResume(&state, originalPath, outputWriter)
	}
}
