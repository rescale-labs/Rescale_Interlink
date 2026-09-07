// Package transfer provides unified upload and download orchestration.
// This file provides streaming encryption helpers for multipart uploads.
package transfer

import (
	"crypto/aes"
	"fmt"

	"github.com/rescale/rescale-int/internal/crypto"
)

// StreamingEncryptionState wraps the crypto.CBCStreamingEncryptor with additional helpers.
// This provides a convenient interface for use in streaming upload operations.
// Uses CBC chaining (Rescale-compatible) instead of HKDF per-part derivation.
type StreamingEncryptionState struct {
	encryptor *encryption.CBCStreamingEncryptor
}

// NewStreamingEncryptionState creates encryption state for a new upload.
// partSize is the size of each plaintext part in bytes.
// Uses CBC chaining for Rescale platform compatibility.
func NewStreamingEncryptionState(partSize int64) (*StreamingEncryptionState, error) {
	encryptor, err := encryption.NewCBCStreamingEncryptor()
	if err != nil {
		return nil, fmt.Errorf("failed to create CBC streaming encryptor: %w", err)
	}

	return &StreamingEncryptionState{encryptor: encryptor}, nil
}

// NewStreamingEncryptionStateFromKey creates encryption state for resuming an upload.
// Uses existing key, initialIV, and currentIV from resume state.
// Uses CBC chaining with resume support.
func NewStreamingEncryptionStateFromKey(key, initialIV, currentIV []byte, partSize int64) (*StreamingEncryptionState, error) {
	encryptor, err := encryption.NewCBCStreamingEncryptorWithKey(key, initialIV, currentIV)
	if err != nil {
		return nil, fmt.Errorf("failed to create CBC streaming encryptor with key: %w", err)
	}

	return &StreamingEncryptionState{encryptor: encryptor}, nil
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
	return s.encryptor.EncryptPart(plaintext, isFinal)
}

// GetKey returns the encryption key (for Rescale API storage).
func (s *StreamingEncryptionState) GetKey() []byte {
	return s.encryptor.GetKey()
}

// GetInitialIV returns the initial IV (for cloud metadata storage).
// IV is stored in metadata for Rescale compatibility.
func (s *StreamingEncryptionState) GetInitialIV() []byte {
	return s.encryptor.GetInitialIV()
}

// GetCurrentIV returns the chaining position: the IV the next part will be
// encrypted under. An upload checkpoints this alongside the parts the backend
// has accepted, because it is the only thing that lets a later attempt continue
// the chain instead of restarting it — see CBCStreamingEncryptor.GetCurrentIV.
func (s *StreamingEncryptionState) GetCurrentIV() []byte {
	return s.encryptor.GetCurrentIV()
}

// CalculateTotalParts calculates the number of parts needed for a file.
func CalculateTotalParts(fileSize, partSize int64) int64 {
	if fileSize == 0 {
		return 1 // Empty files still have one part
	}
	return (fileSize + partSize - 1) / partSize
}

// CiphertextSize is how long the object a streaming upload assembles from a
// file of fileSize plaintext bytes will be.
//
// CBC neither pads nor grows a part that is already a whole number of blocks,
// and every part but the last is one — EncryptPart refuses a non-final part
// that is not. So the only growth is the PKCS7 padding on the final part, which
// is between 1 and 16 bytes and is always there, including for an empty file.
func CiphertextSize(fileSize int64) int64 {
	return fileSize + aes.BlockSize - fileSize%aes.BlockSize
}
