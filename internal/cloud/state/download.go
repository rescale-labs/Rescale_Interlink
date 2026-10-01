// Package state provides shared resume state types and I/O for upload/download operations.
package state

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/rescale/rescale-int/internal/validation"
)

// DownloadResumeState tracks the state of an in-progress download for resumption.
type DownloadResumeState struct {
	LocalPath       string    `json:"local_path"`       // Destination file path
	EncryptedPath   string    `json:"encrypted_path"`   // Encrypted temp file path (.encrypted) - legacy v0 only
	RemotePath      string    `json:"remote_path"`      // S3 object key or Azure blob path
	TotalSize       int64     `json:"total_size"`       // Total encrypted file size
	DownloadedBytes int64     `json:"downloaded_bytes"` // Bytes downloaded so far
	ETag            string    `json:"etag"`             // ETag for validation
	CreatedAt       time.Time `json:"created_at"`
	LastUpdate      time.Time `json:"last_update"`
	StorageType     string    `json:"storage_type"` // "S3Storage" or "AzureStorage"

	// Concurrent download support
	ChunkSize       int64   `json:"chunk_size,omitempty"`       // Size of each chunk (0 for sequential)
	CompletedChunks []int64 `json:"completed_chunks,omitempty"` // List of completed chunk indices
}

// =============================================================================
// Basic I/O functions
// =============================================================================

// SaveDownloadState saves the download resume state to a sidecar file.
func SaveDownloadState(state *DownloadResumeState, localPath string) error {
	return writeSidecar(localPath+".download.resume", state)
}

// LoadDownloadState loads the download resume state from a sidecar file.
// Returns nil without error if no resume state exists.
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

// DeleteDownloadState deletes the download resume state file.
func DeleteDownloadState(localPath string) error {
	stateFilePath := localPath + ".download.resume"
	err := os.Remove(stateFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete state file: %w", err)
	}
	return nil
}

// DownloadResumeStateExists checks if a resume state file exists.
func DownloadResumeStateExists(localPath string) bool {
	_, err := os.Stat(localPath + ".download.resume")
	return err == nil
}

// samePath reports whether two paths name the same file. Records written
// before downloads used absolute paths hold relative ones, so both sides are
// compared as cleaned absolute paths.
func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

// =============================================================================
// Validation
// =============================================================================

// ValidateDownloadState validates that a resume state is still usable.
func ValidateDownloadState(state *DownloadResumeState, localPath string) error {
	if state == nil {
		return fmt.Errorf("state is nil")
	}

	if time.Since(state.CreatedAt) > MaxResumeAge {
		return fmt.Errorf("resume state expired")
	}

	if !samePath(state.LocalPath, localPath) {
		return fmt.Errorf("local path mismatch")
	}

	if state.EncryptedPath != "" {
		encInfo, err := os.Stat(state.EncryptedPath)
		if err != nil {
			return fmt.Errorf("encrypted temp file no longer exists")
		}
		// For concurrent downloads, file may have gaps, so just check it exists and isn't too big
		if state.ChunkSize > 0 {
			if encInfo.Size() > state.TotalSize {
				return fmt.Errorf("encrypted file size exceeds total size")
			}
			// A state is a claim about bytes in the file, so a file too short to
			// hold the last chunk it claims cannot back that claim: the file was
			// truncated, or the state update outlived the data it described.
			// Resuming would skip exactly the ranges that are missing, and the
			// resumed download's own pre-allocation restores the expected length
			// with zeros in their place — a hole no size check notices and only a
			// checksum would catch, which files without one do not carry.
			if claimed := state.claimedEnd(); claimed > encInfo.Size() {
				return fmt.Errorf("resume state claims %d bytes but the file holds %d", claimed, encInfo.Size())
			}
		} else {
			if encInfo.Size() != state.DownloadedBytes {
				return fmt.Errorf("encrypted file size mismatch")
			}
		}
	}

	return nil
}

// GetDownloadResumeProgress returns the resume progress as a percentage (0.0 to 1.0).
func GetDownloadResumeProgress(state *DownloadResumeState) float64 {
	if state == nil || state.TotalSize == 0 {
		return 0.0
	}
	return float64(state.DownloadedBytes) / float64(state.TotalSize)
}

// =============================================================================
// Chunk tracking helpers
// =============================================================================

// IsChunkCompleted checks if a specific chunk index has been completed.
func (s *DownloadResumeState) IsChunkCompleted(chunkIndex int64) bool {
	for _, idx := range s.CompletedChunks {
		if idx == chunkIndex {
			return true
		}
	}
	return false
}

// MarkChunkCompleted marks a chunk as completed and updates downloaded bytes.
func (s *DownloadResumeState) MarkChunkCompleted(chunkIndex int64, chunkSize int64) {
	if !s.IsChunkCompleted(chunkIndex) {
		s.CompletedChunks = append(s.CompletedChunks, chunkIndex)
		s.DownloadedBytes += chunkSize
	}
	s.LastUpdate = time.Now()
}

// claimedEnd returns the offset one past the last byte this state claims is on
// disk, taking the furthest of its completed chunks. A chunk's claim ends at the
// chunk boundary or at TotalSize, whichever comes first, because the last chunk
// of an object is short.
func (s *DownloadResumeState) claimedEnd() int64 {
	var end int64
	if s.ChunkSize > 0 {
		for _, idx := range s.CompletedChunks {
			chunkEnd := (idx + 1) * s.ChunkSize
			if s.TotalSize > 0 && chunkEnd > s.TotalSize {
				chunkEnd = s.TotalSize
			}
			if chunkEnd > end {
				end = chunkEnd
			}
		}
	}
	return end
}

// GetMissingChunks returns a list of chunk indices that still need to be downloaded.
func (s *DownloadResumeState) GetMissingChunks(totalChunks int64) []int64 {
	missing := make([]int64, 0)
	for i := int64(0); i < totalChunks; i++ {
		if !s.IsChunkCompleted(i) {
			missing = append(missing, i)
		}
	}
	return missing
}

// =============================================================================
// Cleanup functions
// =============================================================================

// CleanupExpiredDownloadResume safely deletes expired encrypted temp file and resume state.
//
// The sidecar is data on disk, so the path it names is removed only when it is
// the file the sidecar sits beside: the chunked download keys its sidecar by
// the very file it writes. Any other path is left alone.
func CleanupExpiredDownloadResume(state *DownloadResumeState, localPath string, verbose bool) {
	if state == nil {
		return
	}

	switch {
	case state.EncryptedPath == "":
	case !samePath(state.EncryptedPath, localPath):
		log.Printf("Not removing %s: the resume state for %s names it, but it is not that download's temp file",
			validation.Quote(state.EncryptedPath), validation.Quote(localPath))
	default:
		if _, err := os.Stat(localPath); err == nil {
			if verbose {
				log.Printf("Cleaning up expired download temp file: %s", localPath)
			}
			os.Remove(localPath)
		}
	}

	DeleteDownloadState(localPath)
}
