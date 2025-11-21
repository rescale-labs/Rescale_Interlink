package download

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/transfer"
)

// getStorageInfo determines the correct storage configuration for a file
// Uses fileInfo.Storage if available (for job outputs or files in different storage)
// Falls back to profile.DefaultStorage if fileInfo.Storage is nil (backwards compatibility)
func getStorageInfo(fileInfo *models.CloudFile, profile *models.UserProfile) *models.StorageInfo {
	if fileInfo.Storage != nil && fileInfo.Storage.StorageType != "" {
		// File has specific storage metadata - use it (e.g., job outputs in platform S3)
		connSettings := fileInfo.Storage.ConnectionSettings

		// For file-specific storage, the container/bucket name comes from pathParts, not ConnectionSettings
		// API returns region in ConnectionSettings but container in pathParts
		if fileInfo.PathParts != nil && fileInfo.PathParts.Container != "" {
			connSettings.Container = fileInfo.PathParts.Container
		}

		return &models.StorageInfo{
			ID:                 fileInfo.Storage.ID,
			StorageType:        fileInfo.Storage.StorageType,
			EncryptionType:     fileInfo.Storage.EncryptionType,
			ConnectionSettings: connSettings,
		}
	}
	// Fall back to user's default storage (e.g., user-uploaded files)
	return &profile.DefaultStorage
}

// DownloadFile downloads and decrypts a file from Rescale cloud storage
// Returns error if download or decryption fails
// skipChecksum controls whether to fail on checksum mismatch (false = strict, true = allow mismatch with warning)
func DownloadFile(ctx context.Context, fileID, localPath string, apiClient *api.Client, skipChecksum bool) error {
	return DownloadFileWithProgress(ctx, fileID, localPath, apiClient, nil, skipChecksum)
}

// DownloadFileWithProgress downloads and decrypts a file with progress callback
// progressCallback receives progress as a float64 between 0.0 and 1.0
// skipChecksum controls whether to fail on checksum mismatch (false = strict, true = allow mismatch with warning)
func DownloadFileWithProgress(ctx context.Context, fileID, localPath string, apiClient *api.Client, progressCallback func(float64), skipChecksum bool) error {
	// Get the global credential manager (caches user profile and credentials)
	credManager := credentials.GetManager(apiClient)

	// Get file metadata from Rescale API (must be per-file, not cached)
	fileInfo, err := apiClient.GetFileInfo(ctx, fileID)
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	// Get user profile (cached for 5 minutes)
	profile, err := credManager.GetUserProfile(ctx)
	if err != nil {
		return fmt.Errorf("failed to get user profile: %w", err)
	}

	// Determine storage type: use file's storage if available, otherwise user's default
	// This allows downloading job outputs from platform storage (e.g., S3) even if user has Azure
	storageInfo := getStorageInfo(fileInfo, profile)

	// Get storage credentials for this specific file's storage type
	// For job outputs in different storage (e.g., S3 job outputs on Azure account),
	// we request file-specific credentials by passing fileInfo to the API
	var s3Creds *models.S3Credentials
	var azureCreds *models.AzureCredentials

	// Request credentials for the file's specific storage
	s3Creds, azureCreds, err = apiClient.GetStorageCredentials(ctx, fileInfo)
	if err != nil {
		return fmt.Errorf("failed to get storage credentials: %w", err)
	}

	// Decode encryption key
	encryptionKey, err := encryption.DecodeBase64(fileInfo.EncodedEncryptionKey)
	if err != nil {
		return fmt.Errorf("failed to decode encryption key: %w", err)
	}

	// Try to get IV from file metadata, but it's OK if not present
	// The downloader will retrieve it from cloud storage metadata
	var iv []byte
	if fileInfo.IV != "" {
		iv, err = encryption.DecodeBase64(fileInfo.IV)
		if err != nil {
			return fmt.Errorf("failed to decode IV: %w", err)
		}
	}

	// Download based on storage type
	var downloadErr error
	switch storageInfo.StorageType {
	case "S3Storage":
		if s3Creds == nil {
			return fmt.Errorf("S3 credentials not provided")
		}
		downloader, err := NewS3Downloader(storageInfo, s3Creds, apiClient, fileInfo)
		if err != nil {
			return fmt.Errorf("failed to create S3 downloader: %w", err)
		}
		downloadErr = downloader.DownloadAndDecryptWithProgress(ctx, fileInfo.Path, localPath, encryptionKey, iv, progressCallback)

	case "AzureStorage":
		if azureCreds == nil {
			return fmt.Errorf("Azure credentials not provided")
		}
		downloader, err := NewAzureDownloader(storageInfo, azureCreds, apiClient)
		if err != nil {
			return fmt.Errorf("failed to create Azure downloader: %w", err)
		}
		// For Azure: use pathParts.path directly (Python pattern: blob=cloud_file.path_parts.path)
		// pathParts.path already contains the correct blob name without container prefix
		// because pathPartsBase is typically "" for Azure
		blobPath := fileInfo.Path
		if fileInfo.PathParts != nil {
			blobPath = fileInfo.PathParts.Path
		}
		downloadErr = downloader.DownloadAndDecryptWithProgress(ctx, blobPath, localPath, encryptionKey, iv, progressCallback)

	default:
		return fmt.Errorf("unsupported storage type: %s", storageInfo.StorageType)
	}

	// Return early if download failed
	if downloadErr != nil {
		return downloadErr
	}

	// Verify checksum if available
	if err := verifyChecksum(localPath, fileInfo.FileChecksums); err != nil {
		if skipChecksum {
			// Skip mode: warn but don't fail
			fmt.Fprintf(os.Stderr, "⚠️  Warning: Checksum verification failed for %s: %v\n", localPath, err)
			fmt.Fprintf(os.Stderr, "    Continuing because --skip-checksum flag is set\n")
		} else {
			// Strict mode (default): fail on checksum mismatch
			return fmt.Errorf("checksum verification failed for %s: %w\n\nTo download despite checksum mismatch, use --skip-checksum flag (not recommended)", localPath, err)
		}
	}

	return nil
}

// verifyChecksum verifies the SHA-512 checksum of a downloaded file
// Returns an error if the checksum verification fails
// Note: This is called AFTER decryption, so it verifies the decrypted file
func verifyChecksum(localPath string, checksums []models.FileChecksum) error {
	if len(checksums) == 0 {
		return nil // No checksums to verify
	}

	// Find SHA-512 checksum (we prioritize SHA-512, but could fall back to other algorithms)
	var expectedHash string
	var hashAlgorithm string

	for _, cs := range checksums {
		switch cs.HashFunction {
		case "sha512", "SHA-512", "SHA512":
			expectedHash = cs.FileHash
			hashAlgorithm = "SHA-512"
			break
		}
	}

	if expectedHash == "" {
		// No SHA-512 checksum found, check for other algorithms
		for _, cs := range checksums {
			if cs.HashFunction != "" && cs.FileHash != "" {
				return fmt.Errorf("file has checksum with algorithm %s, but SHA-512 verification is not implemented for this algorithm", cs.HashFunction)
			}
		}
		return nil // No recognized checksum algorithm
	}

	// Open the downloaded file
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open file for checksum verification: %w", err)
	}
	defer file.Close()

	// Calculate SHA-512 hash
	hash := sha512.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("failed to calculate checksum: %w", err)
	}

	actualHash := hex.EncodeToString(hash.Sum(nil))

	// Compare checksums (case-insensitive)
	if !equalIgnoreCase(actualHash, expectedHash) {
		return fmt.Errorf("checksum mismatch: expected %s=%s, got %s", hashAlgorithm, expectedHash, actualHash)
	}

	return nil
}

// equalIgnoreCase compares two strings case-insensitively
func equalIgnoreCase(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca := a[i]
		cb := b[i]
		// Convert to lowercase if uppercase
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// DownloadFileWithProgressAndTransfer downloads and decrypts a file with concurrent support using a transfer handle
// If transferHandle is nil, uses sequential download (same as DownloadFileWithProgress)
// If transferHandle specifies multiple threads, uses concurrent chunk downloads
// skipChecksum controls whether to fail on checksum mismatch (false = strict, true = allow mismatch with warning)
// DownloadFileWithMetadata downloads a file using provided metadata (no GetFileInfo call).
// This is optimized for job downloads where we already have full file metadata from ListJobFiles.
// 2025-11-20: Added to eliminate GetFileInfo API call for job downloads (saves ~3 min for 289 files)
func DownloadFileWithMetadata(ctx context.Context, fileInfo *models.CloudFile, localPath string, apiClient *api.Client, progressCallback func(float64), transferHandle *transfer.Transfer, skipChecksum bool) error {
	// Get the global credential manager (caches user profile and credentials)
	credManager := credentials.GetManager(apiClient)

	// Get user profile (cached for 5 minutes)
	profile, err := credManager.GetUserProfile(ctx)
	if err != nil {
		return fmt.Errorf("failed to get user profile: %w", err)
	}

	// Determine storage type: use file's storage if available, otherwise user's default
	// This allows downloading job outputs from platform storage (e.g., S3) even if user has Azure
	storageInfo := getStorageInfo(fileInfo, profile)

	// Get storage credentials for this specific file's storage type
	// For job outputs in different storage (e.g., S3 job outputs on Azure account),
	// we request file-specific credentials by passing fileInfo to the API
	var s3Creds *models.S3Credentials
	var azureCreds *models.AzureCredentials

	// Request credentials for the file's specific storage
	s3Creds, azureCreds, err = apiClient.GetStorageCredentials(ctx, fileInfo)
	if err != nil {
		return fmt.Errorf("failed to get storage credentials: %w", err)
	}

	// Decode encryption key
	encryptionKey, err := encryption.DecodeBase64(fileInfo.EncodedEncryptionKey)
	if err != nil {
		return fmt.Errorf("failed to decode encryption key: %w", err)
	}

	// Try to get IV from file metadata, but it's OK if not present
	// The downloader will retrieve it from cloud storage metadata
	var iv []byte
	if fileInfo.IV != "" {
		iv, err = encryption.DecodeBase64(fileInfo.IV)
		if err != nil {
			return fmt.Errorf("failed to decode IV: %w", err)
		}
	}

	// Download based on storage type
	var downloadErr error
	switch storageInfo.StorageType {
	case "S3Storage":
		if s3Creds == nil {
			return fmt.Errorf("S3 credentials not provided")
		}
		downloader, err := NewS3Downloader(storageInfo, s3Creds, apiClient, fileInfo)
		if err != nil {
			return fmt.Errorf("failed to create S3 downloader: %w", err)
		}
		downloadErr = downloader.DownloadAndDecryptWithTransfer(ctx, fileInfo.Path, localPath, encryptionKey, iv, progressCallback, transferHandle)

	case "AzureStorage":
		if azureCreds == nil {
			return fmt.Errorf("Azure credentials not provided")
		}
		downloader, err := NewAzureDownloader(storageInfo, azureCreds, apiClient)
		if err != nil {
			return fmt.Errorf("failed to create Azure downloader: %w", err)
		}
		// For Azure: use pathParts.path directly
		blobPath := fileInfo.Path
		if fileInfo.PathParts != nil {
			blobPath = fileInfo.PathParts.Path
		}
		downloadErr = downloader.DownloadAndDecryptWithTransfer(ctx, blobPath, localPath, encryptionKey, iv, progressCallback, transferHandle)

	default:
		return fmt.Errorf("unsupported storage type: %s", storageInfo.StorageType)
	}

	// Return early if download failed
	if downloadErr != nil {
		return downloadErr
	}

	// Verify checksum if available
	if err := verifyChecksum(localPath, fileInfo.FileChecksums); err != nil {
		if skipChecksum {
			// Skip mode: warn but don't fail
			fmt.Fprintf(os.Stderr, "⚠️  Warning: Checksum verification failed for %s: %v\n", localPath, err)
			fmt.Fprintf(os.Stderr, "    Continuing because --skip-checksum flag is set\n")
		} else {
			// Strict mode (default): fail on checksum mismatch
			return fmt.Errorf("checksum verification failed for %s: %w\n\nTo download despite checksum mismatch, use --skip-checksum flag (not recommended)", localPath, err)
		}
	}

	return nil
}

func DownloadFileWithProgressAndTransfer(ctx context.Context, fileID, localPath string, apiClient *api.Client, progressCallback func(float64), transferHandle *transfer.Transfer, skipChecksum bool) error {
	// Get file metadata from Rescale API (must be per-file, not cached)
	fileInfo, err := apiClient.GetFileInfo(ctx, fileID)
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	// Use the metadata-based download function
	return DownloadFileWithMetadata(ctx, fileInfo, localPath, apiClient, progressCallback, transferHandle, skipChecksum)
}
