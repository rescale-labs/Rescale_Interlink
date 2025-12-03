// Package download provides the canonical entry point for file downloads from Rescale cloud storage.
// This package consolidates all download functionality into a single entry point.
//
// Phase 7H: Uses provider factory instead of old S3Downloader/AzureDownloader classes.
//
// Version: 3.2.0 (Sprint 7H - Entry Point Consolidation)
// Date: 2025-11-29
package download

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/providers"
	cloudtransfer "github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/transfer"
)

// ProgressCallback is called during download to report progress (0.0 to 1.0)
type ProgressCallback func(progress float64)

// DownloadParams consolidates all parameters for download operations.
// This is the single canonical way to specify download options.
type DownloadParams struct {
	// One of these is required (FileID or FileInfo):
	// FileID - The Rescale file ID to download (will fetch metadata from API)
	FileID string

	// FileInfo - Pre-fetched file metadata (skips GetFileInfo API call)
	// Use this for job downloads where we already have full file metadata
	FileInfo *models.CloudFile

	// Required: Local path to save the decrypted file
	LocalPath string

	// Required: API client for Rescale operations
	APIClient *api.Client

	// Optional: Progress callback (receives values from 0.0 to 1.0)
	ProgressCallback ProgressCallback

	// Optional: Transfer handle for concurrent chunk downloads
	// If nil or threads <= 1, uses sequential download
	TransferHandle *transfer.Transfer

	// Optional: Output writer for status messages
	OutputWriter io.Writer

	// Optional: Checksum handling
	// false (default) = strict mode - fail on checksum mismatch
	// true = skip mode - warn but don't fail on checksum mismatch
	SkipChecksum bool
}

// DownloadFile is THE ONLY canonical entry point for downloading files from Rescale cloud storage.
// It handles credential fetching, downloads the file with decryption, and verifies checksum.
//
// Default behavior:
//   - Automatically detects encryption format (legacy v0 or streaming v1)
//   - Uses concurrent chunk downloads if TransferHandle has threads > 1
//   - Supports resume from partial downloads
//   - Verifies SHA-512 checksum after download (unless SkipChecksum=true)
//
// Returns nil on success, or an error on failure.
func DownloadFile(ctx context.Context, params DownloadParams) error {
	// Validate required parameters
	if params.LocalPath == "" {
		return fmt.Errorf("local path is required")
	}
	if params.APIClient == nil {
		return fmt.Errorf("API client is required")
	}
	if params.FileID == "" && params.FileInfo == nil {
		return fmt.Errorf("either FileID or FileInfo is required")
	}

	// Get file metadata (if not already provided)
	fileInfo := params.FileInfo
	if fileInfo == nil {
		var err error
		fileInfo, err = params.APIClient.GetFileInfo(ctx, params.FileID)
		if err != nil {
			return fmt.Errorf("failed to get file info: %w", err)
		}
	}

	// Get the global credential manager (caches user profile and credentials)
	credManager := credentials.GetManager(params.APIClient)

	// Get user profile (cached for 5 minutes)
	profile, err := credManager.GetUserProfile(ctx)
	if err != nil {
		return fmt.Errorf("failed to get user profile: %w", err)
	}

	// Determine storage type: use file's storage if available, otherwise user's default
	// This allows downloading job outputs from platform storage (e.g., S3) even if user has Azure
	storageInfo := getStorageInfo(fileInfo, profile)

	// Create provider using factory
	// Phase 7H: Provider factory creates S3 or Azure provider based on storage type
	factory := providers.NewFactory()
	provider, err := factory.NewTransferFromStorageInfo(ctx, storageInfo, params.APIClient)
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}

	// Determine the remote path for download
	remotePath := fileInfo.Path
	if fileInfo.PathParts != nil && fileInfo.PathParts.Path != "" {
		remotePath = fileInfo.PathParts.Path
	}

	// Create download orchestrator and execute download
	downloader := cloudtransfer.NewDownloader(provider)

	// Convert ProgressCallback type (same signature, different types)
	var cloudProgressCallback cloud.ProgressCallback
	if params.ProgressCallback != nil {
		cloudProgressCallback = cloud.ProgressCallback(params.ProgressCallback)
	}

	downloadParams := cloud.DownloadParams{
		RemotePath:       remotePath,
		LocalPath:        params.LocalPath,
		FileInfo:         fileInfo,
		TransferHandle:   params.TransferHandle,
		ProgressCallback: cloudProgressCallback,
		OutputWriter:     params.OutputWriter,
	}

	if err := downloader.Download(ctx, downloadParams); err != nil {
		return fmt.Errorf("%s download failed: %w", storageInfo.StorageType, err)
	}

	// Verify checksum if available
	if err := verifyChecksum(params.LocalPath, fileInfo.FileChecksums); err != nil {
		if params.SkipChecksum {
			// Skip mode: warn but don't fail
			fmt.Fprintf(os.Stderr, "Warning: Checksum verification failed for %s: %v\n", params.LocalPath, err)
			fmt.Fprintf(os.Stderr, "    Continuing because --skip-checksum flag is set\n")
		} else {
			// Strict mode (default): fail on checksum mismatch
			return fmt.Errorf("checksum verification failed for %s: %w\n\nTo download despite checksum mismatch, use --skip-checksum flag (not recommended)", params.LocalPath, err)
		}
	}

	return nil
}

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

checksumLoop:
	for _, cs := range checksums {
		switch cs.HashFunction {
		case "sha512", "SHA-512", "SHA512":
			expectedHash = cs.FileHash
			hashAlgorithm = "SHA-512"
			break checksumLoop
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

