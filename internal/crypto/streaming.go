// Package encryption provides cryptographic functions for Rescale Interlink.
// This file implements streaming encryption for multipart uploads.
//
// Design (Rescale-Compatible CBC Chaining):
//   - Single key + IV stored in metadata (compatible with Rescale platform)
//   - CBC chains across parts: Part N's IV = last 16 bytes of Part N-1's ciphertext
//   - PKCS7 padding applied ONLY to the final part
//   - Sequential part encryption required (maintains CBC state)
//   - Rescale platform can decrypt files with key + IV
//
// Legacy Design (HKDF per-part, format version 1; decryption only, for files
// uploaded in that format):
//   - Per-part keys and IVs derived using HKDF-SHA256
//   - PKCS7 padding applied to each part independently
//   - Not compatible with Rescale platform decryption
package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

// =============================================================================
// NEW: CBC Chaining Streaming Encryption (Rescale-Compatible)
// =============================================================================

// CBCStreamingEncryptor provides streaming encryption with CBC chaining.
// This format is compatible with Rescale platform decryption.
//
// Key properties:
//   - Single key + IV (stored in cloud metadata)
//   - CBC chains across parts (sequential encryption required)
//   - PKCS7 padding only on final part
//   - Combined ciphertext is identical to whole-file AES-256-CBC encryption
type CBCStreamingEncryptor struct {
	key       []byte // 32-byte AES-256 key
	initialIV []byte // 16-byte initial IV (stored in metadata)
	currentIV []byte // Current IV for next encryption (last ciphertext block)
	block     cipher.Block
}

// NewCBCStreamingEncryptor creates an encryptor with CBC chaining.
// Generates random key and IV for a new upload.
//
// The key should be stored in Rescale API (encodedEncryptionKey).
// The IV should be stored in cloud object metadata (iv field).
func NewCBCStreamingEncryptor() (*CBCStreamingEncryptor, error) {
	key, err := GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}

	iv, err := GenerateIV()
	if err != nil {
		return nil, fmt.Errorf("failed to generate IV: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Make copies for safety
	keyCopy := make([]byte, KeySize)
	copy(keyCopy, key)

	ivCopy := make([]byte, IVSize)
	copy(ivCopy, iv)

	currentIV := make([]byte, IVSize)
	copy(currentIV, iv)

	return &CBCStreamingEncryptor{
		key:       keyCopy,
		initialIV: ivCopy,
		currentIV: currentIV,
		block:     block,
	}, nil
}

// NewCBCStreamingEncryptorWithKey creates an encryptor for resuming uploads.
// Uses existing key, initial IV, and current IV (from resume state).
//
// Parameters:
//   - key: 32-byte encryption key
//   - initialIV: 16-byte initial IV (for metadata)
//   - currentIV: 16-byte current IV (last ciphertext block from previous part)
func NewCBCStreamingEncryptorWithKey(key, initialIV, currentIV []byte) (*CBCStreamingEncryptor, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	if len(initialIV) != IVSize {
		return nil, fmt.Errorf("initial IV must be %d bytes, got %d", IVSize, len(initialIV))
	}
	if len(currentIV) != IVSize {
		return nil, fmt.Errorf("current IV must be %d bytes, got %d", IVSize, len(currentIV))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Make copies
	keyCopy := make([]byte, KeySize)
	copy(keyCopy, key)

	initialIVCopy := make([]byte, IVSize)
	copy(initialIVCopy, initialIV)

	currentIVCopy := make([]byte, IVSize)
	copy(currentIVCopy, currentIV)

	return &CBCStreamingEncryptor{
		key:       keyCopy,
		initialIV: initialIVCopy,
		currentIV: currentIVCopy,
		block:     block,
	}, nil
}

// EncryptPart encrypts a part using CBC with the current IV.
// Parts MUST be encrypted sequentially (0, 1, 2, ...) because CBC chains.
//
// Parameters:
//   - plaintext: raw data for this part
//   - isFinal: true if this is the last part (applies PKCS7 padding)
//
// Returns ciphertext and updates internal state for next part.
// The next part's IV will be the last 16 bytes of this ciphertext.
func (e *CBCStreamingEncryptor) EncryptPart(plaintext []byte, isFinal bool) ([]byte, error) {
	var dataToEncrypt []byte

	if isFinal {
		// Apply PKCS7 padding to final part
		dataToEncrypt = pkcs7Pad(plaintext, aes.BlockSize)
	} else {
		// Non-final parts: must be a multiple of block size
		// The upload orchestrator ensures each part (except last) is partSize bytes
		// partSize is a multiple of 16 (e.g., 16MB = 16777216 bytes)
		// An empty one leaves no last block to chain the next part from.
		if len(plaintext) == 0 || len(plaintext)%aes.BlockSize != 0 {
			return nil, fmt.Errorf("non-final part must be a non-empty multiple of %d bytes, got %d", aes.BlockSize, len(plaintext))
		}
		dataToEncrypt = plaintext
	}

	// Create encrypter with current IV
	mode := cipher.NewCBCEncrypter(e.block, e.currentIV)

	// Encrypt
	ciphertext := make([]byte, len(dataToEncrypt))
	mode.CryptBlocks(ciphertext, dataToEncrypt)

	// Update current IV to last ciphertext block for next part
	copy(e.currentIV, ciphertext[len(ciphertext)-aes.BlockSize:])

	return ciphertext, nil
}

// GetKey returns the encryption key (for Rescale API storage).
func (e *CBCStreamingEncryptor) GetKey() []byte {
	result := make([]byte, KeySize)
	copy(result, e.key)
	return result
}

// GetInitialIV returns the initial IV (for cloud metadata storage).
func (e *CBCStreamingEncryptor) GetInitialIV() []byte {
	result := make([]byte, IVSize)
	copy(result, e.initialIV)
	return result
}

// GetCurrentIV returns the chaining position: the IV the next part will be
// encrypted under, which is the last ciphertext block of the part just
// encrypted.
//
// This is what an interrupted upload has to record to be resumable.
// NewCBCStreamingEncryptorWithKey can pick the chain back up from any part
// boundary, but only if something saved the IV at that boundary — the key and
// the initial IV alone place an encryptor at part 0, and re-encrypting from
// there would produce different bytes for every part already on the backend.
func (e *CBCStreamingEncryptor) GetCurrentIV() []byte {
	result := make([]byte, IVSize)
	copy(result, e.currentIV)
	return result
}

// CBCStreamingDecryptor provides streaming decryption with CBC chaining.
// Used for decrypting files uploaded with CBCStreamingEncryptor.
type CBCStreamingDecryptor struct {
	key       []byte
	currentIV []byte
	block     cipher.Block
}

// NewCBCStreamingDecryptor creates a decryptor for CBC-chained files.
//
// Parameters:
//   - key: 32-byte encryption key (from Rescale API)
//   - iv: 16-byte IV (from cloud metadata)
func NewCBCStreamingDecryptor(key, iv []byte) (*CBCStreamingDecryptor, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	if len(iv) != IVSize {
		return nil, fmt.Errorf("IV must be %d bytes, got %d", IVSize, len(iv))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	keyCopy := make([]byte, KeySize)
	copy(keyCopy, key)

	ivCopy := make([]byte, IVSize)
	copy(ivCopy, iv)

	return &CBCStreamingDecryptor{
		key:       keyCopy,
		currentIV: ivCopy,
		block:     block,
	}, nil
}

// DecryptPart decrypts a part using CBC with the current IV.
// Parts MUST be decrypted sequentially (0, 1, 2, ...).
//
// Parameters:
//   - ciphertext: encrypted data for this part (must be multiple of 16 bytes)
//   - isFinal: true if this is the last part (removes PKCS7 padding)
//
// Returns plaintext and updates internal state for next part.
func (d *CBCStreamingDecryptor) DecryptPart(ciphertext []byte, isFinal bool) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("ciphertext cannot be empty")
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length (%d) must be multiple of %d", len(ciphertext), aes.BlockSize)
	}

	// Save last ciphertext block before decryption (will be IV for next part)
	lastBlock := make([]byte, aes.BlockSize)
	copy(lastBlock, ciphertext[len(ciphertext)-aes.BlockSize:])

	// Decrypt
	mode := cipher.NewCBCDecrypter(d.block, d.currentIV)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// Update IV for next part
	copy(d.currentIV, lastBlock)

	if isFinal {
		// Remove PKCS7 padding from final part
		unpadded, err := pkcs7Unpad(plaintext)
		if err != nil {
			return nil, fmt.Errorf("failed to remove padding: %w", err)
		}
		return unpadded, nil
	}

	return plaintext, nil
}

// =============================================================================
// LEGACY: HKDF Per-Part Streaming Decryption, for files uploaded in format
// version 1. Nothing writes this format any more.
// =============================================================================

// StreamingDecryptor provides per-part decryption for streaming downloads.
// Used for files uploaded with the legacy HKDF format (format version 1).
// It derives the same key/IV for each part that was used during encryption.
type StreamingDecryptor struct {
	masterKey []byte
	fileId    []byte
	partSize  int64
}

// NewStreamingDecryptor creates a decryptor from metadata and master key.
// Used for files uploaded with the legacy HKDF format (format version 1).
//
// Parameters:
//   - masterKey: 32-byte encryption key (from Rescale API)
//   - fileId: 32-byte file identifier (from cloud object metadata)
//   - partSize: size of each plaintext part in bytes (from cloud object metadata)
func NewStreamingDecryptor(masterKey, fileId []byte, partSize int64) (*StreamingDecryptor, error) {
	if len(masterKey) != KeySize {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", KeySize, len(masterKey))
	}
	if len(fileId) != FileIdSize {
		return nil, fmt.Errorf("file ID must be %d bytes, got %d", FileIdSize, len(fileId))
	}
	if partSize <= 0 {
		return nil, fmt.Errorf("part size must be positive, got %d", partSize)
	}

	// Make copies to prevent external modification
	keyCopy := make([]byte, KeySize)
	copy(keyCopy, masterKey)

	idCopy := make([]byte, FileIdSize)
	copy(idCopy, fileId)

	return &StreamingDecryptor{
		masterKey: keyCopy,
		fileId:    idCopy,
		partSize:  partSize,
	}, nil
}

// DecryptPart decrypts a single part using AES-256-CBC and removes PKCS7 padding.
// Used for files uploaded with the legacy HKDF format (format version 1).
//
// The key and IV for this part are derived deterministically from (masterKey, fileId, partIndex).
//
// Parameters:
//   - partIndex: 0-based index of the part
//   - ciphertext: the encrypted data for this part (must be a multiple of 16 bytes)
//
// Returns:
//   - plaintext: decrypted data with padding removed
//   - error: if decryption fails (including invalid padding)
func (sd *StreamingDecryptor) DecryptPart(partIndex int64, ciphertext []byte) ([]byte, error) {
	if partIndex < 0 {
		return nil, fmt.Errorf("part index must be non-negative, got %d", partIndex)
	}
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("ciphertext cannot be empty")
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length (%d) must be a multiple of %d", len(ciphertext), aes.BlockSize)
	}

	// Derive key and IV for this specific part
	key, iv, err := DerivePartKeyIV(sd.masterKey, sd.fileId, partIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to derive key for part %d: %w", partIndex, err)
	}

	// Create AES cipher
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher for part %d: %w", partIndex, err)
	}

	// Decrypt with CBC mode
	paddedPlaintext := make([]byte, len(ciphertext))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(paddedPlaintext, ciphertext)

	// Remove PKCS7 padding
	plaintext, err := pkcs7Unpad(paddedPlaintext)
	if err != nil {
		return nil, fmt.Errorf("failed to unpad part %d: %w", partIndex, err)
	}

	return plaintext, nil
}

// =============================================================================
// Helper Functions
// =============================================================================

// CalculateEncryptedPartSize calculates the ciphertext size for a given plaintext size.
// Due to PKCS7 padding on the final part, the total ciphertext is slightly larger.
func CalculateEncryptedPartSize(plaintextSize int64) int64 {
	// PKCS7 always adds at least 1 byte of padding, up to 16 bytes
	// If plaintext is already a multiple of 16, a full 16-byte padding block is added
	padding := int64(aes.BlockSize) - (plaintextSize % int64(aes.BlockSize))
	return plaintextSize + padding
}
