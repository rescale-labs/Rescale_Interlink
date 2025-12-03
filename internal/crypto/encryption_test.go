package encryption

import (
	"bytes"
	"crypto/aes"
	"os"
	"path/filepath"
	"testing"
)

// TestGenerateKey tests that key generation produces correct-length keys
func TestGenerateKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() failed: %v", err)
	}

	if len(key) != KeySize {
		t.Errorf("Expected key length %d, got %d", KeySize, len(key))
	}

	// Verify randomness: generate two keys, they should be different
	key2, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() second call failed: %v", err)
	}

	if bytes.Equal(key, key2) {
		t.Error("Two consecutive key generations produced identical keys (highly unlikely!)")
	}
}

// TestGenerateIV tests that IV generation produces correct-length IVs
func TestGenerateIV(t *testing.T) {
	iv, err := GenerateIV()
	if err != nil {
		t.Fatalf("GenerateIV() failed: %v", err)
	}

	if len(iv) != IVSize {
		t.Errorf("Expected IV length %d, got %d", IVSize, len(iv))
	}

	// Verify randomness: generate two IVs, they should be different
	iv2, err := GenerateIV()
	if err != nil {
		t.Fatalf("GenerateIV() second call failed: %v", err)
	}

	if bytes.Equal(iv, iv2) {
		t.Error("Two consecutive IV generations produced identical IVs (highly unlikely!)")
	}
}

// TestGenerateSecureRandomString tests secure random string generation
func TestGenerateSecureRandomString(t *testing.T) {
	testCases := []struct {
		name   string
		length int
	}{
		{"short", 10},
		{"medium", 22},
		{"long", 50},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			str, err := GenerateSecureRandomString(tc.length)
			if err != nil {
				t.Fatalf("GenerateSecureRandomString(%d) failed: %v", tc.length, err)
			}

			if len(str) != tc.length {
				t.Errorf("Expected string length %d, got %d", tc.length, len(str))
			}

			// Verify randomness
			str2, err := GenerateSecureRandomString(tc.length)
			if err != nil {
				t.Fatalf("GenerateSecureRandomString(%d) second call failed: %v", tc.length, err)
			}

			if str == str2 {
				t.Error("Two consecutive calls produced identical strings (highly unlikely!)")
			}
		})
	}
}

// TestPKCS7Padding tests PKCS7 padding and unpadding
func TestPKCS7Padding(t *testing.T) {
	testCases := []struct {
		name     string
		data     []byte
		expected int // expected padding bytes
	}{
		{"empty", []byte{}, 16},
		{"one_byte", []byte{0x01}, 15},
		{"fifteen_bytes", make([]byte, 15), 1},
		{"sixteen_bytes", make([]byte, 16), 16},
		{"seventeen_bytes", make([]byte, 17), 15},
		{"thirty_one_bytes", make([]byte, 31), 1},
		{"thirty_two_bytes", make([]byte, 32), 16},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Pad the data
			padded := pkcs7Pad(tc.data, aes.BlockSize)

			// Check padding length
			paddingAdded := len(padded) - len(tc.data)
			if paddingAdded != tc.expected {
				t.Errorf("Expected %d padding bytes, got %d", tc.expected, paddingAdded)
			}

			// Verify padding is correct (all padding bytes should have value equal to padding length)
			if len(padded) > 0 {
				paddingValue := int(padded[len(padded)-1])
				if paddingValue != tc.expected {
					t.Errorf("Padding value is %d, expected %d", paddingValue, tc.expected)
				}

				// Verify all padding bytes have the same value
				for i := len(padded) - tc.expected; i < len(padded); i++ {
					if int(padded[i]) != tc.expected {
						t.Errorf("Padding byte at position %d is %d, expected %d", i, padded[i], tc.expected)
					}
				}
			}

			// Unpad the data
			unpadded, err := pkcs7Unpad(padded)
			if err != nil {
				t.Fatalf("pkcs7Unpad() failed: %v", err)
			}

			// Verify unpadded data matches original
			if !bytes.Equal(unpadded, tc.data) {
				t.Errorf("Unpadded data doesn't match original. Original length: %d, Unpadded length: %d",
					len(tc.data), len(unpadded))
			}
		})
	}
}

// TestPKCS7UnpadInvalid tests that unpadding invalid data returns errors
func TestPKCS7UnpadInvalid(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"padding_too_large", []byte{0x01, 0x02, 0x03, 0x11}},      // Padding value 17 > 16
		{"padding_exceeds_length", []byte{0x01, 0x02, 0x03, 0x05}}, // Padding 5 but only 4 bytes total
		{"zero_padding", []byte{0x01, 0x02, 0x03, 0x00}},           // Padding value 0 is invalid
		{"invalid_padding_bytes", []byte{0x01, 0x02, 0x03, 0x04, 0x04, 0x04, 0x03, 0x04}}, // Last byte says 4, but 3rd-from-last is 0x03 not 0x04
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pkcs7Unpad(tc.data)
			if err == nil {
				t.Error("Expected error for invalid padding, got nil")
			}
		})
	}
}

// TestEncryptDecryptRoundTrip tests full encrypt/decrypt cycle
func TestEncryptDecryptRoundTrip(t *testing.T) {
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
		{"large", make([]byte, 1024*1024)}, // 1 MB
	}

	// Initialize test data with patterns
	for i := range testCases {
		for j := range testCases[i].data {
			testCases[i].data[j] = byte(j % 256)
		}
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create temp directory for test files
			tmpDir := t.TempDir()
			originalFile := filepath.Join(tmpDir, "original.dat")
			encryptedFile := filepath.Join(tmpDir, "encrypted.dat")
			decryptedFile := filepath.Join(tmpDir, "decrypted.dat")

			// Write original data
			if err := os.WriteFile(originalFile, tc.data, 0644); err != nil {
				t.Fatalf("Failed to write original file: %v", err)
			}

			// Generate key and IV
			key, err := GenerateKey()
			if err != nil {
				t.Fatalf("GenerateKey() failed: %v", err)
			}

			iv, err := GenerateIV()
			if err != nil {
				t.Fatalf("GenerateIV() failed: %v", err)
			}

			// Encrypt
			if err := EncryptFile(originalFile, encryptedFile, key, iv); err != nil {
				t.Fatalf("EncryptFile() failed: %v", err)
			}

			// Verify encrypted file exists and has data
			encryptedStat, err := os.Stat(encryptedFile)
			if err != nil {
				t.Fatalf("Encrypted file not created: %v", err)
			}

			// Encrypted file should be larger or equal due to padding
			// Minimum padding is 1 byte, maximum is 16 bytes (PKCS7)
			minExpectedSize := int64(len(tc.data)) + 1
			maxExpectedSize := int64(len(tc.data)) + 16
			if encryptedStat.Size() < minExpectedSize || encryptedStat.Size() > maxExpectedSize {
				t.Errorf("Encrypted file size %d not in expected range [%d, %d]",
					encryptedStat.Size(), minExpectedSize, maxExpectedSize)
			}

			// Decrypt
			if err := DecryptFile(encryptedFile, decryptedFile, key, iv); err != nil {
				t.Fatalf("DecryptFile() failed: %v", err)
			}

			// Read decrypted data
			decryptedData, err := os.ReadFile(decryptedFile)
			if err != nil {
				t.Fatalf("Failed to read decrypted file: %v", err)
			}

			// Verify decrypted data matches original
			if !bytes.Equal(decryptedData, tc.data) {
				t.Errorf("Decrypted data doesn't match original. Original: %d bytes, Decrypted: %d bytes",
					len(tc.data), len(decryptedData))
			}
		})
	}
}

// TestEncryptDecryptWithWrongKey tests that decryption with wrong key fails
func TestEncryptDecryptWithWrongKey(t *testing.T) {
	tmpDir := t.TempDir()
	originalFile := filepath.Join(tmpDir, "original.dat")
	encryptedFile := filepath.Join(tmpDir, "encrypted.dat")
	decryptedFile := filepath.Join(tmpDir, "decrypted.dat")

	// Create test data
	testData := []byte("Secret message that should not decrypt with wrong key")
	if err := os.WriteFile(originalFile, testData, 0644); err != nil {
		t.Fatalf("Failed to write original file: %v", err)
	}

	// Generate key and IV for encryption
	key1, _ := GenerateKey()
	iv, _ := GenerateIV()

	// Encrypt with key1
	if err := EncryptFile(originalFile, encryptedFile, key1, iv); err != nil {
		t.Fatalf("EncryptFile() failed: %v", err)
	}

	// Try to decrypt with different key
	key2, _ := GenerateKey()

	// Decryption should either fail or produce garbage
	err := DecryptFile(encryptedFile, decryptedFile, key2, iv)
	if err == nil {
		// If decryption "succeeds", the data should be garbage (not match original)
		decryptedData, _ := os.ReadFile(decryptedFile)
		if bytes.Equal(decryptedData, testData) {
			t.Error("Decryption with wrong key produced correct plaintext (should be impossible!)")
		}
	}
	// If decryption fails with error, that's also acceptable (padding validation may catch it)
}

// TestCalculateSHA512 tests SHA-512 hash calculation
func TestCalculateSHA512(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.dat")

	// Test with known data
	testData := []byte("Hello, World!")
	if err := os.WriteFile(testFile, testData, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	hash, err := CalculateSHA512(testFile)
	if err != nil {
		t.Fatalf("CalculateSHA512() failed: %v", err)
	}

	// SHA-512 produces a 512-bit (64-byte) hash, represented as 128 hex characters
	expectedLength := 128
	if len(hash) != expectedLength {
		t.Errorf("Expected hash length %d, got %d", expectedLength, len(hash))
	}

	// Verify consistency: same file should produce same hash
	hash2, err := CalculateSHA512(testFile)
	if err != nil {
		t.Fatalf("CalculateSHA512() second call failed: %v", err)
	}

	if hash != hash2 {
		t.Error("Two hash calculations of same file produced different results")
	}

	// Modify file and verify hash changes
	modifiedData := []byte("Hello, World! Modified")
	if err := os.WriteFile(testFile, modifiedData, 0644); err != nil {
		t.Fatalf("Failed to write modified file: %v", err)
	}

	hash3, err := CalculateSHA512(testFile)
	if err != nil {
		t.Fatalf("CalculateSHA512() on modified file failed: %v", err)
	}

	if hash == hash3 {
		t.Error("Hash did not change when file content changed")
	}
}

// TestBase64EncodeDecode tests base64 encoding and decoding
func TestBase64EncodeDecode(t *testing.T) {
	testCases := [][]byte{
		{},
		{0x00},
		{0x01, 0x02, 0x03},
		make([]byte, 100),
	}

	for i, tc := range testCases {
		// Encode
		encoded := EncodeBase64(tc)

		// Decode
		decoded, err := DecodeBase64(encoded)
		if err != nil {
			t.Errorf("Case %d: DecodeBase64() failed: %v", i, err)
			continue
		}

		// Verify round-trip
		if !bytes.Equal(decoded, tc) {
			t.Errorf("Case %d: Decoded data doesn't match original", i)
		}
	}
}

// TestEncryptFileInvalidKeySize tests that encryption fails with wrong key size
func TestEncryptFileInvalidKeySize(t *testing.T) {
	tmpDir := t.TempDir()
	inputFile := filepath.Join(tmpDir, "input.dat")
	outputFile := filepath.Join(tmpDir, "output.dat")

	// Create input file
	if err := os.WriteFile(inputFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write input file: %v", err)
	}

	// Try with wrong key size
	wrongKey := make([]byte, 16) // Should be 32
	iv, _ := GenerateIV()

	err := EncryptFile(inputFile, outputFile, wrongKey, iv)
	if err == nil {
		t.Error("Expected error with wrong key size, got nil")
	}
}

// TestEncryptFileInvalidIVSize tests that encryption fails with wrong IV size
func TestEncryptFileInvalidIVSize(t *testing.T) {
	tmpDir := t.TempDir()
	inputFile := filepath.Join(tmpDir, "input.dat")
	outputFile := filepath.Join(tmpDir, "output.dat")

	// Create input file
	if err := os.WriteFile(inputFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write input file: %v", err)
	}

	// Try with wrong IV size
	key, _ := GenerateKey()
	wrongIV := make([]byte, 8) // Should be 16

	err := EncryptFile(inputFile, outputFile, key, wrongIV)
	if err == nil {
		t.Error("Expected error with wrong IV size, got nil")
	}
}
