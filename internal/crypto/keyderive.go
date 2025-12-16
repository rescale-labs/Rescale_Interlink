// Package encryption provides cryptographic functions for Rescale Interlink.
// This file implements HKDF-based key derivation for per-part streaming encryption.
package encryption

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

const (
	// FileIdSize is the size of the random file identifier (32 bytes)
	FileIdSize = 32
)

// GenerateFileId generates a random 32-byte file identifier for key derivation.
// The FileId is used with HKDF to derive unique per-part keys and IVs.
func GenerateFileId() ([]byte, error) {
	return GenerateKey() // Same size and randomness requirements as encryption key
}

// DerivePartKeyIV derives a unique key and IV for a specific part using HKDF-SHA256.
// This is FIPS 140-3 approved for key derivation.
//
// Parameters:
//   - masterKey: 32-byte master encryption key
//   - fileId: 32-byte random file identifier (unique per upload)
//   - partIndex: 0-based index of the part being encrypted
//
// Returns:
//   - key: 32-byte AES-256 key for this part
//   - iv: 16-byte IV for AES-CBC for this part
//   - error: if derivation fails
//
// Security properties:
//   - Each (masterKey, fileId, partIndex) tuple produces a unique (key, iv) pair
//   - Deterministic: same inputs always produce same outputs (required for resume)
//   - Different fileIds ensure different uploads of same file use different keys
func DerivePartKeyIV(masterKey, fileId []byte, partIndex int64) (key []byte, iv []byte, err error) {
	if len(masterKey) != KeySize {
		return nil, nil, fmt.Errorf("master key must be %d bytes, got %d", KeySize, len(masterKey))
	}
	if len(fileId) != FileIdSize {
		return nil, nil, fmt.Errorf("file ID must be %d bytes, got %d", FileIdSize, len(fileId))
	}
	if partIndex < 0 {
		return nil, nil, fmt.Errorf("part index must be non-negative, got %d", partIndex)
	}

	// Build info = fileId || partIndex (little-endian uint64)
	info := make([]byte, FileIdSize+8)
	copy(info[:FileIdSize], fileId)
	binary.LittleEndian.PutUint64(info[FileIdSize:], uint64(partIndex))

	// HKDF-SHA256: derive 32-byte key + 16-byte IV = 48 bytes total
	// Using masterKey as the secret, nil salt (empty salt is valid per RFC 5869),
	// and info as the context/application-specific info.
	// crypto/hkdf.Key is FIPS 140-3 compliant (part of Go standard library)
	derivedBytes, err := hkdf.Key(sha256.New, masterKey, nil, string(info), KeySize+IVSize)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to derive key material: %w", err)
	}

	key = derivedBytes[:KeySize]
	iv = derivedBytes[KeySize:]

	return key, iv, nil
}
