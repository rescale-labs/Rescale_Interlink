package download

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DownloadResumeState tracks the state of an in-progress download for resumption
type DownloadResumeState struct {
	LocalPath       string    `json:"local_path"`       // Destination file path
	EncryptedPath   string    `json:"encrypted_path"`   // Encrypted temp file path (.encrypted)
	RemotePath      string    `json:"remote_path"`      // S3 object key or Azure blob path
	FileID          string    `json:"file_id"`          // Rescale file ID
	TotalSize       int64     `json:"total_size"`       // Total encrypted file size
	DownloadedBytes int64     `json:"downloaded_bytes"` // Bytes downloaded so far (sequential) or sum of completed chunks (concurrent)
	ETag            string    `json:"etag"`             // ETag for validation
	CreatedAt       time.Time `json:"created_at"`
	LastUpdate      time.Time `json:"last_update"`
	StorageType     string    `json:"storage_type"` // "S3Storage" or "AzureStorage"

	// Concurrent download support (added for multi-threaded downloads)
	ChunkSize       int64   `json:"chunk_size,omitempty"`       // Size of each chunk (0 for sequential)
	CompletedChunks []int64 `json:"completed_chunks,omitempty"` // List of completed chunk indices
}

const (
	// MaxResumeAge is the maximum age of a resume state before it's considered expired
	MaxResumeAge = 7 * 24 * time.Hour
)

// SaveDownloadState saves the download resume state to a sidecar file
func SaveDownloadState(state *DownloadResumeState, localPath string) error {
	stateFilePath := localPath + ".download.resume"
	tmpFilePath := stateFilePath + ".tmp"

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal download state: %w", err)
	}

	if err := os.WriteFile(tmpFilePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp state file: %w", err)
	}

	if err := os.Rename(tmpFilePath, stateFilePath); err != nil {
		os.Remove(tmpFilePath)
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// LoadDownloadState loads the download resume state from a sidecar file
func LoadDownloadState(localPath string) (*DownloadResumeState, error) {
	stateFilePath := localPath + ".download.resume"

	data, err := os.ReadFile(stateFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state DownloadResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state file: %w", err)
	}

	return &state, nil
}

// ValidateDownloadState validates that a resume state is still usable
func ValidateDownloadState(state *DownloadResumeState, localPath string) error {
	if state == nil {
		return fmt.Errorf("state is nil")
	}

	age := time.Since(state.CreatedAt)
	if age > MaxResumeAge {
		return fmt.Errorf("resume state expired")
	}

	if state.LocalPath != localPath {
		return fmt.Errorf("local path mismatch")
	}

	if state.EncryptedPath != "" {
		encInfo, err := os.Stat(state.EncryptedPath)
		if err != nil {
			return fmt.Errorf("encrypted temp file no longer exists")
		}

		// For concurrent downloads (ChunkSize > 0), chunks are written at specific offsets
		// so file size may not match DownloadedBytes. Just verify file exists and is <= TotalSize.
		// For sequential downloads (ChunkSize == 0), file size should match DownloadedBytes exactly.
		if state.ChunkSize > 0 {
			// Concurrent download: file should not exceed total size
			if encInfo.Size() > state.TotalSize {
				return fmt.Errorf("encrypted file size exceeds total size")
			}
		} else {
			// Sequential download: file size must match downloaded bytes
			if encInfo.Size() != state.DownloadedBytes {
				return fmt.Errorf("encrypted file size mismatch")
			}
		}
	}

	return nil
}

// DeleteDownloadState deletes the download resume state file
func DeleteDownloadState(localPath string) error {
	stateFilePath := localPath + ".download.resume"
	err := os.Remove(stateFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete state file: %w", err)
	}
	return nil
}

// GetResumeFilePath returns the path to the resume state file
func GetResumeFilePath(localPath string) string {
	return localPath + ".download.resume"
}

// ResumeStateExists checks if a resume state file exists
func ResumeStateExists(localPath string) bool {
	_, err := os.Stat(GetResumeFilePath(localPath))
	return err == nil
}

// GetResumeProgress returns the resume progress as a percentage
func GetResumeProgress(state *DownloadResumeState) float64 {
	if state == nil || state.TotalSize == 0 {
		return 0.0
	}
	return float64(state.DownloadedBytes) / float64(state.TotalSize)
}

// CleanupExpiredResume safely deletes expired encrypted temp file and resume state
func CleanupExpiredResume(state *DownloadResumeState, localPath string, verbose bool) {
	if state == nil {
		return
	}

	if state.EncryptedPath != "" {
		expectedDir := filepath.Dir(localPath)
		actualDir := filepath.Dir(state.EncryptedPath)

		if expectedDir == actualDir && strings.HasSuffix(state.EncryptedPath, ".encrypted") {
			if _, err := os.Stat(state.EncryptedPath); err == nil {
				if verbose {
					fmt.Printf("Cleaning up expired download temp file: %s\n", filepath.Base(state.EncryptedPath))
				}
				os.Remove(state.EncryptedPath)
			}
		}
	}

	DeleteDownloadState(localPath)
}

// CleanupExpiredResumesInDirectory scans for expired download resume states
func CleanupExpiredResumesInDirectory(dir string, verbose bool) {
	pattern := filepath.Join(dir, "*.download.resume")
	resumeFiles, err := filepath.Glob(pattern)
	if err != nil || len(resumeFiles) == 0 {
		return
	}

	for _, resumeFile := range resumeFiles {
		data, err := os.ReadFile(resumeFile)
		if err != nil {
			continue
		}

		var state DownloadResumeState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}

		if time.Since(state.CreatedAt) > MaxResumeAge {
			originalPath := strings.TrimSuffix(resumeFile, ".download.resume")
			CleanupExpiredResume(&state, originalPath, verbose)
		}
	}
}

// IsChunkCompleted checks if a specific chunk index has been completed
func (s *DownloadResumeState) IsChunkCompleted(chunkIndex int64) bool {
	if s.CompletedChunks == nil {
		return false
	}
	for _, idx := range s.CompletedChunks {
		if idx == chunkIndex {
			return true
		}
	}
	return false
}

// MarkChunkCompleted marks a chunk as completed and updates downloaded bytes
func (s *DownloadResumeState) MarkChunkCompleted(chunkIndex int64, chunkSize int64) {
	if s.CompletedChunks == nil {
		s.CompletedChunks = make([]int64, 0)
	}

	// Only add if not already present
	if !s.IsChunkCompleted(chunkIndex) {
		s.CompletedChunks = append(s.CompletedChunks, chunkIndex)
		s.DownloadedBytes += chunkSize
	}

	s.LastUpdate = time.Now()
}

// GetMissingChunks returns a list of chunk indices that still need to be downloaded
func (s *DownloadResumeState) GetMissingChunks(totalChunks int64) []int64 {
	missing := make([]int64, 0)
	for i := int64(0); i < totalChunks; i++ {
		if !s.IsChunkCompleted(i) {
			missing = append(missing, i)
		}
	}
	return missing
}
