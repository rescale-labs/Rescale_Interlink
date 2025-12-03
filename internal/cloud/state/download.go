// Package state provides shared resume state types and I/O for upload/download operations.
//
// Version: 3.2.0 (Sprint 7H - Import Cycle Resolution)
// Date: 2025-11-29
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ByteRange represents a completed byte range in the output file.
type ByteRange struct {
	Start int64 `json:"start"` // Starting byte offset (inclusive)
	End   int64 `json:"end"`   // Ending byte offset (exclusive)
}

// DownloadResumeState tracks the state of an in-progress download for resumption.
// Supports both legacy (FormatVersion=0) and streaming (FormatVersion=1) encryption.
type DownloadResumeState struct {
	LocalPath       string    `json:"local_path"`       // Destination file path
	EncryptedPath   string    `json:"encrypted_path"`   // Encrypted temp file path (.encrypted) - legacy v0 only
	RemotePath      string    `json:"remote_path"`      // S3 object key or Azure blob path
	FileID          string    `json:"file_id"`          // Rescale file ID
	TotalSize       int64     `json:"total_size"`       // Total encrypted file size (v0) or original plaintext size (v1)
	DownloadedBytes int64     `json:"downloaded_bytes"` // Bytes downloaded so far
	ETag            string    `json:"etag"`             // ETag for validation
	CreatedAt       time.Time `json:"created_at"`
	LastUpdate      time.Time `json:"last_update"`
	StorageType     string    `json:"storage_type"` // "S3Storage" or "AzureStorage"

	// Concurrent download support
	ChunkSize       int64   `json:"chunk_size,omitempty"`       // Size of each chunk (0 for sequential)
	CompletedChunks []int64 `json:"completed_chunks,omitempty"` // List of completed chunk indices

	// Byte range tracking for accurate resume
	CompletedRanges []ByteRange `json:"completed_ranges,omitempty"` // Exact byte ranges written to disk

	// Streaming decryption fields (FormatVersion=1)
	FormatVersion   int     `json:"format_version"`               // 0=legacy, 1=streaming
	MasterKey       string  `json:"master_key,omitempty"`         // Base64-encoded master key (v1 only)
	StreamingFileId string  `json:"streaming_file_id,omitempty"`  // Base64-encoded file ID from metadata (v1 only)
	PartSize        int64   `json:"part_size,omitempty"`          // Plaintext part size (v1 only)
	CompletedParts  []int64 `json:"completed_parts,omitempty"`    // Completed part indices (v1 only)
}

// =============================================================================
// Basic I/O functions
// =============================================================================

// SaveDownloadState saves the download resume state to a sidecar file.
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

	if state.LocalPath != localPath {
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
// Byte range tracking helpers
// =============================================================================

// AddCompletedRange adds a completed byte range to the state.
// Merges overlapping/adjacent ranges for efficiency.
func (s *DownloadResumeState) AddCompletedRange(start, end int64) {
	newRange := ByteRange{Start: start, End: end}

	if len(s.CompletedRanges) == 0 {
		s.CompletedRanges = []ByteRange{newRange}
		return
	}

	// Try to merge with existing ranges
	merged := false
	for i := range s.CompletedRanges {
		r := &s.CompletedRanges[i]
		if newRange.Start <= r.End && newRange.End >= r.Start {
			if newRange.Start < r.Start {
				r.Start = newRange.Start
			}
			if newRange.End > r.End {
				r.End = newRange.End
			}
			merged = true
			break
		}
	}

	if !merged {
		s.CompletedRanges = append(s.CompletedRanges, newRange)
	}

	s.CompletedRanges = mergeOverlappingRanges(s.CompletedRanges)
}

// IsByteRangeComplete checks if a specific byte range is completely downloaded.
func (s *DownloadResumeState) IsByteRangeComplete(start, end int64) bool {
	for _, r := range s.CompletedRanges {
		if r.Start <= start && r.End >= end {
			return true
		}
	}

	// Fall back to CompletedChunks
	if s.ChunkSize > 0 && len(s.CompletedChunks) > 0 {
		chunkIndex := start / s.ChunkSize
		return s.IsChunkCompleted(chunkIndex)
	}

	return false
}

// GetTotalCompletedBytes calculates the total bytes completed from ranges.
func (s *DownloadResumeState) GetTotalCompletedBytes() int64 {
	if len(s.CompletedRanges) > 0 {
		var total int64
		for _, r := range s.CompletedRanges {
			total += r.End - r.Start
		}
		return total
	}
	return s.DownloadedBytes
}

// GetMissingRanges returns byte ranges that have not been downloaded yet.
func (s *DownloadResumeState) GetMissingRanges() []ByteRange {
	if s.TotalSize == 0 {
		return nil
	}

	if len(s.CompletedRanges) == 0 {
		return []ByteRange{{Start: 0, End: s.TotalSize}}
	}

	merged := mergeOverlappingRanges(s.CompletedRanges)
	var missing []ByteRange
	var lastEnd int64

	for _, r := range merged {
		if r.Start > lastEnd {
			missing = append(missing, ByteRange{Start: lastEnd, End: r.Start})
		}
		lastEnd = r.End
	}

	if lastEnd < s.TotalSize {
		missing = append(missing, ByteRange{Start: lastEnd, End: s.TotalSize})
	}

	return missing
}

func mergeOverlappingRanges(ranges []ByteRange) []ByteRange {
	if len(ranges) <= 1 {
		return ranges
	}

	// Sort by start offset
	for i := 0; i < len(ranges)-1; i++ {
		for j := i + 1; j < len(ranges); j++ {
			if ranges[j].Start < ranges[i].Start {
				ranges[i], ranges[j] = ranges[j], ranges[i]
			}
		}
	}

	// Merge overlapping/adjacent ranges
	result := []ByteRange{ranges[0]}
	for _, r := range ranges[1:] {
		last := &result[len(result)-1]
		if r.Start <= last.End {
			if r.End > last.End {
				last.End = r.End
			}
		} else {
			result = append(result, r)
		}
	}

	return result
}

// =============================================================================
// Cleanup functions
// =============================================================================

// CleanupExpiredDownloadResume safely deletes expired encrypted temp file and resume state.
func CleanupExpiredDownloadResume(state *DownloadResumeState, localPath string, verbose bool) {
	if state == nil {
		return
	}

	// Clean up encrypted temp file if it exists
	if state.EncryptedPath != "" {
		if _, err := os.Stat(state.EncryptedPath); err == nil {
			if verbose {
				fmt.Printf("Cleaning up expired download temp file: %s\n", state.EncryptedPath)
			}
			os.Remove(state.EncryptedPath)
		}
	}

	DeleteDownloadState(localPath)
}
