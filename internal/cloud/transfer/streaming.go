// Package transfer provides unified upload and download orchestration.
// This file provides streaming encryption helpers for multipart uploads.
//
// Version: 3.2.4
// Date: 2025-12-10
package transfer

import (
	"fmt"

	"github.com/rescale/rescale-int/internal/crypto"
)

// StreamingEncryptionState wraps the crypto.CBCStreamingEncryptor with additional helpers.
// This provides a convenient interface for use in streaming upload operations.
//
// v3.2.0: Uses CBC chaining (Rescale-compatible) instead of HKDF per-part derivation.
type StreamingEncryptionState struct {
	encryptor *encryption.CBCStreamingEncryptor
	partSize  int64
	partCount int64 // Track how many parts have been encrypted (for isFinal detection)
}

// NewStreamingEncryptionState creates encryption state for a new upload.
// partSize is the size of each plaintext part in bytes.
//
// v3.2.0: Uses CBC chaining for Rescale platform compatibility.
func NewStreamingEncryptionState(partSize int64) (*StreamingEncryptionState, error) {
	encryptor, err := encryption.NewCBCStreamingEncryptor()
	if err != nil {
		return nil, fmt.Errorf("failed to create CBC streaming encryptor: %w", err)
	}

	return &StreamingEncryptionState{
		encryptor: encryptor,
		partSize:  partSize,
		partCount: 0,
	}, nil
}

// NewStreamingEncryptionStateFromKey creates encryption state for resuming an upload.
// Uses existing key, initialIV, and currentIV from resume state.
//
// v3.2.0: Uses CBC chaining with resume support.
func NewStreamingEncryptionStateFromKey(key, initialIV, currentIV []byte, partSize int64) (*StreamingEncryptionState, error) {
	encryptor, err := encryption.NewCBCStreamingEncryptorWithKey(key, initialIV, currentIV)
	if err != nil {
		return nil, fmt.Errorf("failed to create CBC streaming encryptor with key: %w", err)
	}

	return &StreamingEncryptionState{
		encryptor: encryptor,
		partSize:  partSize,
		partCount: 0,
	}, nil
}

// EncryptPart encrypts a single part with CBC chaining.
// Parts MUST be encrypted sequentially (0, 1, 2, ...).
//
// Parameters:
//   - plaintext: raw data for this part
//   - isFinal: true if this is the last part
//
// Note: Unlike the legacy HKDF-based encryption, this does NOT accept partIndex
// because CBC chaining requires sequential encryption and tracks state internally.
func (s *StreamingEncryptionState) EncryptPart(plaintext []byte, isFinal bool) ([]byte, error) {
	ciphertext, err := s.encryptor.EncryptPart(plaintext, isFinal)
	if err != nil {
		return nil, err
	}
	s.partCount++
	return ciphertext, nil
}

// GetKey returns the encryption key (for Rescale API storage).
// v3.2.0: Renamed from GetMasterKey for clarity.
func (s *StreamingEncryptionState) GetKey() []byte {
	return s.encryptor.GetKey()
}

// GetMasterKey returns the encryption key (for backward compatibility).
//
// Deprecated: Use GetKey instead.
func (s *StreamingEncryptionState) GetMasterKey() []byte {
	return s.encryptor.GetKey()
}

// GetInitialIV returns the initial IV (for cloud metadata storage).
// v3.2.0: New method - IV is stored in metadata for Rescale compatibility.
func (s *StreamingEncryptionState) GetInitialIV() []byte {
	return s.encryptor.GetInitialIV()
}

// GetCurrentIV returns the current IV (for resume state storage).
// This is the last ciphertext block from the most recent encrypted part.
// v3.2.0: New method for resume support with CBC chaining.
func (s *StreamingEncryptionState) GetCurrentIV() []byte {
	return s.encryptor.GetCurrentIV()
}

// GetFileId returns nil for CBC streaming format.
// Kept for interface compatibility but returns nil.
//
// Deprecated: CBC format doesn't use FileId, only IV.
func (s *StreamingEncryptionState) GetFileId() []byte {
	return nil
}

// GetPartSize returns the part size in bytes.
func (s *StreamingEncryptionState) GetPartSize() int64 {
	return s.partSize
}

// CalculateTotalParts calculates the number of parts needed for a file.
func CalculateTotalParts(fileSize, partSize int64) int64 {
	if fileSize == 0 {
		return 1 // Empty files still have one part
	}
	return (fileSize + partSize - 1) / partSize
}
