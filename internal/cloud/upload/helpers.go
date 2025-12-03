// Package upload provides helpers for file upload operations.
//
// Version: 3.2.0 (Sprint E - Dead Code Cleanup)
// Date: 2025-11-29
package upload

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/rescale/rescale-int/internal/crypto"
)

// GenerateEncryptionParams generates new encryption key, IV, and random suffix.
// Returns (encryptionKey, iv, randomSuffix, error).
func GenerateEncryptionParams() ([]byte, []byte, string, error) {
	encryptionKey, err := encryption.GenerateKey()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to generate encryption key: %w", err)
	}

	iv, err := encryption.GenerateIV()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to generate IV: %w", err)
	}

	randomSuffix, err := encryption.GenerateSecureRandomString(22)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to generate random suffix: %w", err)
	}

	return encryptionKey, iv, randomSuffix, nil
}

// CreateEncryptedTempFile creates a temporary encrypted file path.
// Tries system temp directory first, falls back to source directory if needed.
// Returns the encrypted file path and an error if creation fails.
// NOTE: This does NOT check disk space - call CheckDiskSpaceForEncryption separately.
func CreateEncryptedTempFile(localPath string) (string, error) {
	filename := filepath.Base(localPath)
	sourceDir := filepath.Dir(localPath)

	// First attempt: system temp directory (better for cleanup)
	tempDir := os.TempDir()
	encryptedFile, err := os.CreateTemp(tempDir, fmt.Sprintf("%s-*.encrypted", filename))
	if err == nil {
		encryptedPath := encryptedFile.Name()
		if closeErr := encryptedFile.Close(); closeErr != nil {
			log.Printf("Warning: failed to close temp file %s: %v", encryptedPath, closeErr)
		}
		return encryptedPath, nil
	}

	// Fallback: create temp file in same directory as source file
	encryptedFile, err = os.CreateTemp(sourceDir, fmt.Sprintf(".%s-*.encrypted", filename))
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	encryptedPath := encryptedFile.Name()
	if closeErr := encryptedFile.Close(); closeErr != nil {
		log.Printf("Warning: failed to close temp file %s: %v", encryptedPath, closeErr)
	}

	return encryptedPath, nil
}
