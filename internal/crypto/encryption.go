package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"os"

	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/util/buffers"
)

const (
	KeySize = 32 // 256-bit key for AES-256
	IVSize  = 16 // 128-bit IV for AES
)

// GenerateKey generates a random 256-bit encryption key
func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}
	return key, nil
}

// GenerateIV generates a random 128-bit initialization vector
func GenerateIV() ([]byte, error) {
	iv := make([]byte, IVSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("failed to generate IV: %w", err)
	}
	return iv, nil
}

// GenerateSecureRandomString generates a random string of the specified length
func GenerateSecureRandomString(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, length)
	for i := range result {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", fmt.Errorf("failed to generate random string: %w", err)
		}
		result[i] = charset[n.Int64()]
	}
	return string(result), nil
}

// CalculateSHA512 calculates the SHA-512 hash of a file
func CalculateSHA512(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	hash := sha512.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("failed to hash file: %w", err)
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// pkcs7Pad applies PKCS7 padding to the data
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padText := make([]byte, padding)
	for i := range padText {
		padText[i] = byte(padding)
	}
	return append(data, padText...)
}

// pkcs7Unpad removes PKCS7 padding from the data
// Verifies that all padding bytes have the correct value for defense-in-depth
func pkcs7Unpad(data []byte) ([]byte, error) {
	length := len(data)
	if length == 0 {
		return nil, fmt.Errorf("invalid padding: empty data")
	}
	padding := int(data[length-1])
	if padding > length || padding > aes.BlockSize || padding == 0 {
		return nil, fmt.Errorf("invalid padding size: %d", padding)
	}
	// Verify all padding bytes have the correct value
	for i := 0; i < padding; i++ {
		if data[length-1-i] != byte(padding) {
			return nil, fmt.Errorf("invalid padding byte at position %d: expected %d, got %d", i, padding, data[length-1-i])
		}
	}
	return data[:length-padding], nil
}

// EncryptFile encrypts a file using AES-256-CBC with PKCS7 padding
func EncryptFile(inputPath, outputPath string, key, iv []byte) error {
	if len(key) != KeySize {
		return fmt.Errorf("key must be %d bytes", KeySize)
	}
	if len(iv) != IVSize {
		return fmt.Errorf("IV must be %d bytes", IVSize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("failed to create cipher: %w", err)
	}

	inputFile, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("failed to open input file: %w", err)
	}
	defer inputFile.Close()

	outputFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outputFile.Close()

	mode := cipher.NewCBCEncrypter(block, iv)
	// Use pooled buffer to reduce allocations
	bufferPtr := buffers.GetSmallBuffer()
	defer buffers.PutSmallBuffer(bufferPtr)
	buffer := *bufferPtr
	var lastChunk []byte

	for {
		n, err := inputFile.Read(buffer)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read input file: %w", err)
		}

		chunk := buffer[:n]

		// If this might be the last chunk, hold it for padding
		if n < constants.EncryptionChunkSize {
			lastChunk = append(lastChunk, chunk...)
			break
		}

		// Process full blocks
		if len(chunk)%aes.BlockSize == 0 {
			encrypted := make([]byte, len(chunk))
			mode.CryptBlocks(encrypted, chunk)
			if _, err := outputFile.Write(encrypted); err != nil {
				return fmt.Errorf("failed to write encrypted data: %w", err)
			}
		} else {
			// Partial block - save for next iteration
			lastChunk = append(lastChunk, chunk...)

			// Process complete blocks from lastChunk
			completeBlocks := (len(lastChunk) / aes.BlockSize) * aes.BlockSize
			if completeBlocks > 0 {
				toEncrypt := lastChunk[:completeBlocks]
				encrypted := make([]byte, len(toEncrypt))
				mode.CryptBlocks(encrypted, toEncrypt)
				if _, err := outputFile.Write(encrypted); err != nil {
					return fmt.Errorf("failed to write encrypted data: %w", err)
				}
				lastChunk = lastChunk[completeBlocks:]
			}
		}
	}

	// Pad and encrypt the last chunk
	paddedChunk := pkcs7Pad(lastChunk, aes.BlockSize)
	encrypted := make([]byte, len(paddedChunk))
	mode.CryptBlocks(encrypted, paddedChunk)
	if _, err := outputFile.Write(encrypted); err != nil {
		return fmt.Errorf("failed to write final encrypted data: %w", err)
	}

	return nil
}

// DecryptFile decrypts a file using AES-256-CBC with PKCS7 padding
// Streams decryption to handle large files efficiently without loading entire file into memory
func DecryptFile(inputPath, outputPath string, key, iv []byte) error {
	if len(key) != KeySize {
		return fmt.Errorf("key must be %d bytes", KeySize)
	}
	if len(iv) != IVSize {
		return fmt.Errorf("IV must be %d bytes", IVSize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("failed to create cipher: %w", err)
	}

	inputFile, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("failed to open input file: %w", err)
	}
	defer inputFile.Close()

	// Get file size to know when we're at the last block (for unpadding)
	fileInfo, err := inputFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat input file: %w", err)
	}
	fileSize := fileInfo.Size()

	// Validate that encrypted file size is a multiple of AES block size
	// Properly encrypted files with PKCS7 padding are always block-aligned
	if fileSize%int64(aes.BlockSize) != 0 {
		return fmt.Errorf("encrypted file size (%d bytes) is not a multiple of AES block size (%d) - file may be corrupted or download was interrupted", fileSize, aes.BlockSize)
	}

	outputFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outputFile.Close()

	mode := cipher.NewCBCDecrypter(block, iv)
	// Use pooled buffer to reduce allocations
	bufferPtr := buffers.GetSmallBuffer()
	defer buffers.PutSmallBuffer(bufferPtr)
	buffer := *bufferPtr
	var pendingData []byte
	var totalRead int64

	for {
		n, err := inputFile.Read(buffer)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read input file: %w", err)
		}

		totalRead += int64(n)
		pendingData = append(pendingData, buffer[:n]...)

		// Process complete blocks, but save the last block for special handling (unpadding)
		isLastChunk := (totalRead == fileSize)

		var toProcess []byte
		if isLastChunk {
			// Last chunk - process everything (will unpad below)
			toProcess = pendingData
			pendingData = nil
		} else {
			// Not last chunk - process all complete blocks, keep partial block for next iteration
			completeBlocks := (len(pendingData) / aes.BlockSize) * aes.BlockSize
			if completeBlocks > 0 {
				toProcess = pendingData[:completeBlocks]
				pendingData = pendingData[completeBlocks:]
			}
		}

		if len(toProcess) > 0 {
			// Defensive check: ensure data is block-aligned before decryption
			// This should never fail due to the file size validation above, but provides
			// an extra safety net to prevent panics from cipher.CryptBlocks
			if len(toProcess)%aes.BlockSize != 0 {
				return fmt.Errorf("internal error: data not block-aligned (%d bytes) - this should not happen", len(toProcess))
			}

			// Decrypt the blocks
			decrypted := make([]byte, len(toProcess))
			mode.CryptBlocks(decrypted, toProcess)

			// If this is the last chunk, remove padding
			if isLastChunk {
				decrypted, err = pkcs7Unpad(decrypted)
				if err != nil {
					return fmt.Errorf("failed to unpad data: %w", err)
				}
			}

			// Write decrypted data immediately (streaming!)
			if _, err := outputFile.Write(decrypted); err != nil {
				return fmt.Errorf("failed to write decrypted data: %w", err)
			}
		}
	}

	return nil
}

// EncodeBase64 encodes bytes to base64 string
func EncodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// DecodeBase64 decodes base64 string to bytes
func DecodeBase64(data string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(data)
}
