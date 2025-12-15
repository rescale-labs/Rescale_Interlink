// Package encryption provides cryptographic functions for Rescale Interlink.
// This file contains unit tests for per-part streaming encryption.
//
// Version: 3.4.2
// Date: December 15, 2025
package encryption

import (
	"bytes"
	"crypto/aes"
	"testing"
)

// =============================================================================
// Key Derivation Tests (keyderive.go)
// =============================================================================

// TestGenerateFileId tests that file ID generation produces correct-length IDs
func TestGenerateFileId(t *testing.T) {
	fileId, err := GenerateFileId()
	if err != nil {
		t.Fatalf("GenerateFileId() failed: %v", err)
	}

	if len(fileId) != FileIdSize {
		t.Errorf("Expected file ID length %d, got %d", FileIdSize, len(fileId))
	}

	// Verify randomness: generate two file IDs, they should be different
	fileId2, err := GenerateFileId()
	if err != nil {
		t.Fatalf("GenerateFileId() second call failed: %v", err)
	}

	if bytes.Equal(fileId, fileId2) {
		t.Error("Two consecutive file ID generations produced identical IDs (highly unlikely!)")
	}
}

// TestDerivePartKeyIV tests HKDF-based key/IV derivation
func TestDerivePartKeyIV(t *testing.T) {
	masterKey, _ := GenerateKey()
	fileId, _ := GenerateFileId()

	// Test basic derivation
	key, iv, err := DerivePartKeyIV(masterKey, fileId, 0)
	if err != nil {
		t.Fatalf("DerivePartKeyIV() failed: %v", err)
	}

	if len(key) != KeySize {
		t.Errorf("Expected key length %d, got %d", KeySize, len(key))
	}
	if len(iv) != IVSize {
		t.Errorf("Expected IV length %d, got %d", IVSize, len(iv))
	}
}

// TestDerivePartKeyIV_Determinism tests that same inputs produce same outputs
func TestDerivePartKeyIV_Determinism(t *testing.T) {
	masterKey, _ := GenerateKey()
	fileId, _ := GenerateFileId()

	key1, iv1, err := DerivePartKeyIV(masterKey, fileId, 5)
	if err != nil {
		t.Fatalf("First DerivePartKeyIV() failed: %v", err)
	}

	key2, iv2, err := DerivePartKeyIV(masterKey, fileId, 5)
	if err != nil {
		t.Fatalf("Second DerivePartKeyIV() failed: %v", err)
	}

	if !bytes.Equal(key1, key2) {
		t.Error("Same inputs produced different keys (must be deterministic!)")
	}
	if !bytes.Equal(iv1, iv2) {
		t.Error("Same inputs produced different IVs (must be deterministic!)")
	}
}

// TestDerivePartKeyIV_UniquePerPart tests that different parts get different keys/IVs
func TestDerivePartKeyIV_UniquePerPart(t *testing.T) {
	masterKey, _ := GenerateKey()
	fileId, _ := GenerateFileId()

	key0, iv0, _ := DerivePartKeyIV(masterKey, fileId, 0)
	key1, iv1, _ := DerivePartKeyIV(masterKey, fileId, 1)
	key2, iv2, _ := DerivePartKeyIV(masterKey, fileId, 2)

	// All keys should be different
	if bytes.Equal(key0, key1) || bytes.Equal(key1, key2) || bytes.Equal(key0, key2) {
		t.Error("Different parts produced identical keys (security vulnerability!)")
	}

	// All IVs should be different
	if bytes.Equal(iv0, iv1) || bytes.Equal(iv1, iv2) || bytes.Equal(iv0, iv2) {
		t.Error("Different parts produced identical IVs (security vulnerability!)")
	}
}

// TestDerivePartKeyIV_UniquePerFile tests that different files get different keys/IVs
func TestDerivePartKeyIV_UniquePerFile(t *testing.T) {
	masterKey, _ := GenerateKey()
	fileId1, _ := GenerateFileId()
	fileId2, _ := GenerateFileId()

	key1, iv1, _ := DerivePartKeyIV(masterKey, fileId1, 0)
	key2, iv2, _ := DerivePartKeyIV(masterKey, fileId2, 0)

	if bytes.Equal(key1, key2) {
		t.Error("Different files with same part index produced identical keys")
	}
	if bytes.Equal(iv1, iv2) {
		t.Error("Different files with same part index produced identical IVs")
	}
}

// TestDerivePartKeyIV_InvalidInputs tests error handling for invalid inputs
func TestDerivePartKeyIV_InvalidInputs(t *testing.T) {
	validMasterKey, _ := GenerateKey()
	validFileId, _ := GenerateFileId()

	testCases := []struct {
		name      string
		masterKey []byte
		fileId    []byte
		partIndex int64
	}{
		{"nil_master_key", nil, validFileId, 0},
		{"short_master_key", make([]byte, 16), validFileId, 0},
		{"long_master_key", make([]byte, 64), validFileId, 0},
		{"nil_file_id", validMasterKey, nil, 0},
		{"short_file_id", validMasterKey, make([]byte, 16), 0},
		{"long_file_id", validMasterKey, make([]byte, 64), 0},
		{"negative_part_index", validMasterKey, validFileId, -1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := DerivePartKeyIV(tc.masterKey, tc.fileId, tc.partIndex)
			if err == nil {
				t.Error("Expected error for invalid input, got nil")
			}
		})
	}
}

// =============================================================================
// StreamingEncryptor Tests (streaming.go)
// =============================================================================

// TestNewStreamingEncryptor tests encryptor creation with generated keys
func TestNewStreamingEncryptor(t *testing.T) {
	partSize := int64(16 * 1024 * 1024) // 16MB

	enc, err := NewStreamingEncryptor(partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptor() failed: %v", err)
	}

	// Verify master key was generated
	masterKey := enc.GetMasterKey()
	if len(masterKey) != KeySize {
		t.Errorf("Expected master key length %d, got %d", KeySize, len(masterKey))
	}

	// Verify file ID was generated
	fileId := enc.GetFileId()
	if len(fileId) != FileIdSize {
		t.Errorf("Expected file ID length %d, got %d", FileIdSize, len(fileId))
	}

	// Verify part size
	if enc.GetPartSize() != partSize {
		t.Errorf("Expected part size %d, got %d", partSize, enc.GetPartSize())
	}
}

// TestNewStreamingEncryptor_InvalidPartSize tests error handling for invalid part size
func TestNewStreamingEncryptor_InvalidPartSize(t *testing.T) {
	testCases := []struct {
		name     string
		partSize int64
	}{
		{"zero", 0},
		{"negative", -1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewStreamingEncryptor(tc.partSize)
			if err == nil {
				t.Error("Expected error for invalid part size, got nil")
			}
		})
	}
}

// TestNewStreamingEncryptorWithKey tests encryptor creation with provided keys
func TestNewStreamingEncryptorWithKey(t *testing.T) {
	masterKey, _ := GenerateKey()
	fileId, _ := GenerateFileId()
	partSize := int64(16 * 1024 * 1024)

	enc, err := NewStreamingEncryptorWithKey(masterKey, fileId, partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptorWithKey() failed: %v", err)
	}

	// Verify it uses the provided values
	if !bytes.Equal(enc.GetMasterKey(), masterKey) {
		t.Error("Encryptor did not use provided master key")
	}
	if !bytes.Equal(enc.GetFileId(), fileId) {
		t.Error("Encryptor did not use provided file ID")
	}
	if enc.GetPartSize() != partSize {
		t.Errorf("Expected part size %d, got %d", partSize, enc.GetPartSize())
	}
}

// TestNewStreamingEncryptorWithKey_InvalidInputs tests error handling
func TestNewStreamingEncryptorWithKey_InvalidInputs(t *testing.T) {
	validMasterKey, _ := GenerateKey()
	validFileId, _ := GenerateFileId()
	validPartSize := int64(16 * 1024 * 1024)

	testCases := []struct {
		name      string
		masterKey []byte
		fileId    []byte
		partSize  int64
	}{
		{"nil_master_key", nil, validFileId, validPartSize},
		{"short_master_key", make([]byte, 16), validFileId, validPartSize},
		{"nil_file_id", validMasterKey, nil, validPartSize},
		{"short_file_id", validMasterKey, make([]byte, 16), validPartSize},
		{"zero_part_size", validMasterKey, validFileId, 0},
		{"negative_part_size", validMasterKey, validFileId, -1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewStreamingEncryptorWithKey(tc.masterKey, tc.fileId, tc.partSize)
			if err == nil {
				t.Error("Expected error for invalid input, got nil")
			}
		})
	}
}

// TestStreamingEncryptorWithKey_CopiesInputs tests that inputs are copied (not referenced)
func TestStreamingEncryptorWithKey_CopiesInputs(t *testing.T) {
	masterKey, _ := GenerateKey()
	fileId, _ := GenerateFileId()
	partSize := int64(16 * 1024 * 1024)

	enc, err := NewStreamingEncryptorWithKey(masterKey, fileId, partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptorWithKey() failed: %v", err)
	}

	// Modify original inputs
	originalMasterKey := make([]byte, len(masterKey))
	copy(originalMasterKey, masterKey)
	masterKey[0] ^= 0xFF

	originalFileId := make([]byte, len(fileId))
	copy(originalFileId, fileId)
	fileId[0] ^= 0xFF

	// Encryptor should still have original values
	if !bytes.Equal(enc.GetMasterKey(), originalMasterKey) {
		t.Error("Modifying original master key affected encryptor (should be copied)")
	}
	if !bytes.Equal(enc.GetFileId(), originalFileId) {
		t.Error("Modifying original file ID affected encryptor (should be copied)")
	}
}

// =============================================================================
// Encryption/Decryption Round-Trip Tests
// =============================================================================

// TestEncryptDecryptPart_RoundTrip tests basic encrypt/decrypt cycle
func TestEncryptDecryptPart_RoundTrip(t *testing.T) {
	partSize := int64(1024) // 1KB for testing

	testCases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"single_byte", []byte{0x42}},
		{"fifteen_bytes", make([]byte, 15)},
		{"one_block", make([]byte, 16)},
		{"one_block_plus_one", make([]byte, 17)},
		{"two_blocks", make([]byte, 32)},
		{"exact_part_size", make([]byte, 1024)},
		{"smaller_than_part", make([]byte, 500)},
	}

	// Initialize test data with patterns
	for i := range testCases {
		for j := range testCases[i].data {
			testCases[i].data[j] = byte(j % 256)
		}
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := NewStreamingEncryptor(partSize)
			if err != nil {
				t.Fatalf("NewStreamingEncryptor() failed: %v", err)
			}

			// Encrypt
			ciphertext, err := enc.EncryptPart(0, tc.data)
			if err != nil {
				t.Fatalf("EncryptPart() failed: %v", err)
			}

			// Verify ciphertext is a multiple of block size
			if len(ciphertext)%aes.BlockSize != 0 {
				t.Errorf("Ciphertext length %d is not a multiple of block size %d",
					len(ciphertext), aes.BlockSize)
			}

			// Create decryptor with same parameters
			dec, err := NewStreamingDecryptor(enc.GetMasterKey(), enc.GetFileId(), partSize)
			if err != nil {
				t.Fatalf("NewStreamingDecryptor() failed: %v", err)
			}

			// Decrypt
			plaintext, err := dec.DecryptPart(0, ciphertext)
			if err != nil {
				t.Fatalf("DecryptPart() failed: %v", err)
			}

			// Verify round-trip
			if !bytes.Equal(plaintext, tc.data) {
				t.Errorf("Decrypted data doesn't match original. Original: %d bytes, Decrypted: %d bytes",
					len(tc.data), len(plaintext))
			}
		})
	}
}

// TestEncryptDecryptPart_MultipleParts tests encrypting multiple parts independently
func TestEncryptDecryptPart_MultipleParts(t *testing.T) {
	partSize := int64(100)
	numParts := 5

	enc, err := NewStreamingEncryptor(partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptor() failed: %v", err)
	}

	// Generate test data for each part
	parts := make([][]byte, numParts)
	ciphertexts := make([][]byte, numParts)

	for i := 0; i < numParts; i++ {
		parts[i] = make([]byte, partSize)
		for j := range parts[i] {
			parts[i][j] = byte((i * 100) + (j % 256))
		}

		ciphertext, err := enc.EncryptPart(int64(i), parts[i])
		if err != nil {
			t.Fatalf("EncryptPart(%d) failed: %v", i, err)
		}
		ciphertexts[i] = ciphertext
	}

	// Create decryptor
	dec, err := NewStreamingDecryptor(enc.GetMasterKey(), enc.GetFileId(), partSize)
	if err != nil {
		t.Fatalf("NewStreamingDecryptor() failed: %v", err)
	}

	// Decrypt all parts and verify
	for i := 0; i < numParts; i++ {
		plaintext, err := dec.DecryptPart(int64(i), ciphertexts[i])
		if err != nil {
			t.Fatalf("DecryptPart(%d) failed: %v", i, err)
		}

		if !bytes.Equal(plaintext, parts[i]) {
			t.Errorf("Part %d: decrypted data doesn't match original", i)
		}
	}
}

// TestEncryptDecryptPart_PartIndependence tests that parts can be decrypted independently
func TestEncryptDecryptPart_PartIndependence(t *testing.T) {
	partSize := int64(100)

	enc, err := NewStreamingEncryptor(partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptor() failed: %v", err)
	}

	// Encrypt parts 0, 1, 2
	part0 := []byte("Part zero data here")
	part1 := []byte("Part one data here")
	part2 := []byte("Part two data here")

	cipher0, _ := enc.EncryptPart(0, part0)
	cipher1, _ := enc.EncryptPart(1, part1)
	cipher2, _ := enc.EncryptPart(2, part2)

	// Create decryptor
	dec, err := NewStreamingDecryptor(enc.GetMasterKey(), enc.GetFileId(), partSize)
	if err != nil {
		t.Fatalf("NewStreamingDecryptor() failed: %v", err)
	}

	// Decrypt in reverse order - should still work (part independence)
	plain2, err := dec.DecryptPart(2, cipher2)
	if err != nil {
		t.Fatalf("DecryptPart(2) failed: %v", err)
	}
	if !bytes.Equal(plain2, part2) {
		t.Error("Part 2 decryption failed when decrypted first")
	}

	plain0, err := dec.DecryptPart(0, cipher0)
	if err != nil {
		t.Fatalf("DecryptPart(0) failed: %v", err)
	}
	if !bytes.Equal(plain0, part0) {
		t.Error("Part 0 decryption failed when decrypted after part 2")
	}

	plain1, err := dec.DecryptPart(1, cipher1)
	if err != nil {
		t.Fatalf("DecryptPart(1) failed: %v", err)
	}
	if !bytes.Equal(plain1, part1) {
		t.Error("Part 1 decryption failed when decrypted last")
	}
}

// TestEncryptPart_Determinism tests that same inputs produce same outputs
func TestEncryptPart_Determinism(t *testing.T) {
	masterKey, _ := GenerateKey()
	fileId, _ := GenerateFileId()
	partSize := int64(100)

	plaintext := []byte("Deterministic encryption test data")

	// Create two encryptors with same parameters
	enc1, _ := NewStreamingEncryptorWithKey(masterKey, fileId, partSize)
	enc2, _ := NewStreamingEncryptorWithKey(masterKey, fileId, partSize)

	cipher1, _ := enc1.EncryptPart(5, plaintext)
	cipher2, _ := enc2.EncryptPart(5, plaintext)

	if !bytes.Equal(cipher1, cipher2) {
		t.Error("Same inputs produced different ciphertexts (must be deterministic for resume!)")
	}
}

// TestEncryptPart_UniqueCiphertext tests that ciphertext differs between parts
func TestEncryptPart_UniqueCiphertext(t *testing.T) {
	partSize := int64(100)

	enc, err := NewStreamingEncryptor(partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptor() failed: %v", err)
	}

	// Same plaintext for different parts
	plaintext := []byte("Same data for all parts")

	cipher0, _ := enc.EncryptPart(0, plaintext)
	cipher1, _ := enc.EncryptPart(1, plaintext)
	cipher2, _ := enc.EncryptPart(2, plaintext)

	// All ciphertexts should be different (different keys/IVs per part)
	if bytes.Equal(cipher0, cipher1) || bytes.Equal(cipher1, cipher2) || bytes.Equal(cipher0, cipher2) {
		t.Error("Same plaintext encrypted to same ciphertext for different parts (security vulnerability!)")
	}
}

// TestEncryptPart_NegativePartIndex tests error handling for negative part index
func TestEncryptPart_NegativePartIndex(t *testing.T) {
	partSize := int64(100)
	enc, _ := NewStreamingEncryptor(partSize)

	_, err := enc.EncryptPart(-1, []byte("test"))
	if err == nil {
		t.Error("Expected error for negative part index, got nil")
	}
}

// =============================================================================
// StreamingDecryptor Tests
// =============================================================================

// TestNewStreamingDecryptor_InvalidInputs tests error handling
func TestNewStreamingDecryptor_InvalidInputs(t *testing.T) {
	validMasterKey, _ := GenerateKey()
	validFileId, _ := GenerateFileId()
	validPartSize := int64(16 * 1024 * 1024)

	testCases := []struct {
		name      string
		masterKey []byte
		fileId    []byte
		partSize  int64
	}{
		{"nil_master_key", nil, validFileId, validPartSize},
		{"short_master_key", make([]byte, 16), validFileId, validPartSize},
		{"nil_file_id", validMasterKey, nil, validPartSize},
		{"short_file_id", validMasterKey, make([]byte, 16), validPartSize},
		{"zero_part_size", validMasterKey, validFileId, 0},
		{"negative_part_size", validMasterKey, validFileId, -1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewStreamingDecryptor(tc.masterKey, tc.fileId, tc.partSize)
			if err == nil {
				t.Error("Expected error for invalid input, got nil")
			}
		})
	}
}

// TestDecryptPart_InvalidInputs tests error handling for decrypt
func TestDecryptPart_InvalidInputs(t *testing.T) {
	masterKey, _ := GenerateKey()
	fileId, _ := GenerateFileId()
	partSize := int64(100)

	dec, _ := NewStreamingDecryptor(masterKey, fileId, partSize)

	testCases := []struct {
		name       string
		partIndex  int64
		ciphertext []byte
	}{
		{"negative_part_index", -1, make([]byte, 16)},
		{"empty_ciphertext", 0, []byte{}},
		{"non_block_aligned", 0, make([]byte, 15)}, // Not a multiple of 16
		{"non_block_aligned_larger", 0, make([]byte, 33)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := dec.DecryptPart(tc.partIndex, tc.ciphertext)
			if err == nil {
				t.Error("Expected error for invalid input, got nil")
			}
		})
	}
}

// TestDecryptPart_WrongKey tests that decryption with wrong key fails or produces garbage
func TestDecryptPart_WrongKey(t *testing.T) {
	partSize := int64(100)

	// Encrypt with one encryptor
	enc, _ := NewStreamingEncryptor(partSize)
	plaintext := []byte("Secret message that should not decrypt with wrong key")
	ciphertext, _ := enc.EncryptPart(0, plaintext)

	// Try to decrypt with different master key
	wrongMasterKey, _ := GenerateKey()
	dec, _ := NewStreamingDecryptor(wrongMasterKey, enc.GetFileId(), partSize)

	decrypted, err := dec.DecryptPart(0, ciphertext)
	if err == nil {
		// If decryption "succeeds", the data should be garbage
		if bytes.Equal(decrypted, plaintext) {
			t.Error("Decryption with wrong key produced correct plaintext (should be impossible!)")
		}
		// Garbage output is acceptable - PKCS7 unpadding may or may not catch it
	}
	// Error from padding validation is also acceptable
}

// TestDecryptPart_WrongFileId tests that decryption with wrong file ID fails
func TestDecryptPart_WrongFileId(t *testing.T) {
	partSize := int64(100)

	// Encrypt with one encryptor
	enc, _ := NewStreamingEncryptor(partSize)
	plaintext := []byte("Secret message")
	ciphertext, _ := enc.EncryptPart(0, plaintext)

	// Try to decrypt with different file ID
	wrongFileId, _ := GenerateFileId()
	dec, _ := NewStreamingDecryptor(enc.GetMasterKey(), wrongFileId, partSize)

	decrypted, err := dec.DecryptPart(0, ciphertext)
	if err == nil {
		if bytes.Equal(decrypted, plaintext) {
			t.Error("Decryption with wrong file ID produced correct plaintext (should be impossible!)")
		}
	}
}

// TestDecryptPart_WrongPartIndex tests that decrypting with wrong part index fails
func TestDecryptPart_WrongPartIndex(t *testing.T) {
	partSize := int64(100)

	// Encrypt
	enc, _ := NewStreamingEncryptor(partSize)
	plaintext := []byte("Part 0 data")
	ciphertext, _ := enc.EncryptPart(0, plaintext)

	// Create decryptor
	dec, _ := NewStreamingDecryptor(enc.GetMasterKey(), enc.GetFileId(), partSize)

	// Try to decrypt with wrong part index
	decrypted, err := dec.DecryptPart(1, ciphertext) // Using index 1 for data encrypted as index 0
	if err == nil {
		if bytes.Equal(decrypted, plaintext) {
			t.Error("Decryption with wrong part index produced correct plaintext (should be impossible!)")
		}
	}
}

// =============================================================================
// Size Calculation Tests
// =============================================================================

// TestCalculateEncryptedPartSize tests encrypted size calculation
func TestCalculateEncryptedPartSize(t *testing.T) {
	testCases := []struct {
		plaintextSize int64
		expectedSize  int64
	}{
		{0, 16},   // Empty -> 16 bytes padding
		{1, 16},   // 1 byte -> 15 padding = 16 total
		{15, 16},  // 15 bytes -> 1 padding = 16 total
		{16, 32},  // 16 bytes -> 16 padding (full block) = 32 total
		{17, 32},  // 17 bytes -> 15 padding = 32 total
		{31, 32},  // 31 bytes -> 1 padding = 32 total
		{32, 48},  // 32 bytes -> 16 padding = 48 total
		{100, 112}, // 100 bytes -> 12 padding = 112 total
	}

	for _, tc := range testCases {
		actual := CalculateEncryptedPartSize(tc.plaintextSize)
		if actual != tc.expectedSize {
			t.Errorf("CalculateEncryptedPartSize(%d): expected %d, got %d",
				tc.plaintextSize, tc.expectedSize, actual)
		}
	}
}

// TestEncryptedPartSize_Verification verifies that calculated size matches actual
func TestEncryptedPartSize_Verification(t *testing.T) {
	partSize := int64(100)
	enc, _ := NewStreamingEncryptor(partSize)

	testSizes := []int{0, 1, 15, 16, 17, 31, 32, 64, 100}

	for _, size := range testSizes {
		plaintext := make([]byte, size)
		ciphertext, err := enc.EncryptPart(0, plaintext)
		if err != nil {
			t.Fatalf("EncryptPart() failed for size %d: %v", size, err)
		}

		expected := CalculateEncryptedPartSize(int64(size))
		if int64(len(ciphertext)) != expected {
			t.Errorf("Size %d: calculated size %d doesn't match actual ciphertext size %d",
				size, expected, len(ciphertext))
		}
	}
}

// =============================================================================
// Large Data Tests
// =============================================================================

// TestEncryptDecrypt_LargePart tests encryption of larger data
func TestEncryptDecrypt_LargePart(t *testing.T) {
	partSize := int64(16 * 1024 * 1024) // 16MB part size

	// Test with 1MB of data
	dataSize := 1024 * 1024
	plaintext := make([]byte, dataSize)
	for i := range plaintext {
		plaintext[i] = byte(i % 256)
	}

	enc, err := NewStreamingEncryptor(partSize)
	if err != nil {
		t.Fatalf("NewStreamingEncryptor() failed: %v", err)
	}

	ciphertext, err := enc.EncryptPart(0, plaintext)
	if err != nil {
		t.Fatalf("EncryptPart() failed: %v", err)
	}

	dec, err := NewStreamingDecryptor(enc.GetMasterKey(), enc.GetFileId(), partSize)
	if err != nil {
		t.Fatalf("NewStreamingDecryptor() failed: %v", err)
	}

	decrypted, err := dec.DecryptPart(0, ciphertext)
	if err != nil {
		t.Fatalf("DecryptPart() failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Error("Large data round-trip failed")
	}
}

// TestEncryptDecrypt_ManyParts tests encrypting many parts
func TestEncryptDecrypt_ManyParts(t *testing.T) {
	partSize := int64(64) // Small part size for testing
	numParts := 100

	enc, _ := NewStreamingEncryptor(partSize)
	dec, _ := NewStreamingDecryptor(enc.GetMasterKey(), enc.GetFileId(), partSize)

	for i := 0; i < numParts; i++ {
		plaintext := make([]byte, partSize)
		for j := range plaintext {
			plaintext[j] = byte((i + j) % 256)
		}

		ciphertext, err := enc.EncryptPart(int64(i), plaintext)
		if err != nil {
			t.Fatalf("EncryptPart(%d) failed: %v", i, err)
		}

		decrypted, err := dec.DecryptPart(int64(i), ciphertext)
		if err != nil {
			t.Fatalf("DecryptPart(%d) failed: %v", i, err)
		}

		if !bytes.Equal(decrypted, plaintext) {
			t.Errorf("Part %d round-trip failed", i)
		}
	}
}

// TestLargePartIndex tests encryption with large part indices
func TestLargePartIndex(t *testing.T) {
	partSize := int64(64)
	enc, _ := NewStreamingEncryptor(partSize)

	// Test with large part index (simulating large file with many parts)
	largeIndex := int64(100000)
	plaintext := []byte("Data at large index")

	ciphertext, err := enc.EncryptPart(largeIndex, plaintext)
	if err != nil {
		t.Fatalf("EncryptPart() with large index failed: %v", err)
	}

	dec, _ := NewStreamingDecryptor(enc.GetMasterKey(), enc.GetFileId(), partSize)
	decrypted, err := dec.DecryptPart(largeIndex, ciphertext)
	if err != nil {
		t.Fatalf("DecryptPart() with large index failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Error("Large index round-trip failed")
	}
}

// =============================================================================
// Getter Tests (ensure they return copies)
// =============================================================================

// TestEncryptorGetters_ReturnCopies tests that getters return copies
func TestEncryptorGetters_ReturnCopies(t *testing.T) {
	enc, _ := NewStreamingEncryptor(int64(100))

	// Get and modify master key
	key1 := enc.GetMasterKey()
	originalKey := make([]byte, len(key1))
	copy(originalKey, key1)
	key1[0] ^= 0xFF

	// Get again - should be unchanged
	key2 := enc.GetMasterKey()
	if !bytes.Equal(key2, originalKey) {
		t.Error("GetMasterKey() returns reference instead of copy")
	}

	// Same test for file ID
	fileId1 := enc.GetFileId()
	originalFileId := make([]byte, len(fileId1))
	copy(originalFileId, fileId1)
	fileId1[0] ^= 0xFF

	fileId2 := enc.GetFileId()
	if !bytes.Equal(fileId2, originalFileId) {
		t.Error("GetFileId() returns reference instead of copy")
	}
}
