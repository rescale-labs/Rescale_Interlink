// Package download provides the canonical entry point for file downloads from Rescale cloud storage.
// This package consolidates all download functionality into a single entry point.
package download

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/providers"
	"github.com/rescale/rescale-int/internal/cloud/state"
	cloudtransfer "github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/transfer"
)

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
	ProgressCallback cloud.ProgressCallback

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
	// v3.6.2: Track overall download timing
	overallTimer := cloud.StartTimer(params.OutputWriter, "Download total")

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

	// v3.6.2: Track initialization phase
	initTimer := cloud.StartTimer(params.OutputWriter, "Download initialization")

	// Get file metadata (if not already provided)
	fileInfo := params.FileInfo
	if fileInfo == nil {
		var err error
		fileInfo, err = params.APIClient.GetFileInfo(ctx, params.FileID)
		if err != nil {
			return fmt.Errorf("failed to get file info: %w", err)
		}
	}

	// v3.6.2: Log file info
	cloud.TimingLog(params.OutputWriter, "File: %s (%s)", fileInfo.Name, cloud.FormatBytes(fileInfo.DecryptedSize))

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

	// Create provider using factory (S3 or Azure based on storage type)
	factory := providers.NewFactory()
	provider, err := factory.NewTransferFromStorageInfo(ctx, storageInfo, params.APIClient)
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}

	initTimer.StopWithMessage("backend=%s", storageInfo.StorageType)

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

	// v3.6.2: Track download transfer phase
	transferTimer := cloud.StartTimer(params.OutputWriter, "Download transfer")

	// v4.4.2: Get computed hash from download to avoid re-reading file for verification.
	// This eliminates the race condition where post-download verification re-reads
	// the file and may get stale cache data.
	computedHash, err := downloader.Download(ctx, downloadParams)
	if err != nil {
		return fmt.Errorf("%s download failed: %w", storageInfo.StorageType, err)
	}

	transferTimer.StopWithThroughput(fileInfo.DecryptedSize)

	// v4.4.0: Verify file exists and has expected size before checksum verification.
	// This provides a clearer error message if the download failed silently (e.g., 0 bytes written).
	fi, err := os.Stat(params.LocalPath)
	if err != nil {
		return fmt.Errorf("failed to stat downloaded file: %w", err)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("download failed: file is empty (0 bytes) - possible write error or filesystem issue")
	}

	// v3.6.2: Track checksum verification phase
	checksumTimer := cloud.StartTimer(params.OutputWriter, "Checksum verification")

	// v4.4.2: Use computed hash from download if available (eliminates cache race condition).
	// Only fall back to file re-read verification if no computed hash is available.
	expectedHash := getExpectedSHA512(fileInfo.FileChecksums)

	var checksumErr error
	if computedHash != "" && expectedHash != "" {
		// v4.4.2: Compare using hash computed during download - no file re-read needed!
		if !strings.EqualFold(computedHash, expectedHash) {
			checksumErr = fmt.Errorf("checksum mismatch: expected SHA-512=%s, got %s", expectedHash, computedHash)
		}
	} else if expectedHash != "" {
		// Fallback: No computed hash available, use traditional file re-read verification
		checksumErr = verifyChecksum(params.LocalPath, fileInfo.FileChecksums)
	}
	// If expectedHash is empty, skip verification (no checksum to verify against)

	if checksumErr != nil {
		if params.SkipChecksum {
			// Skip mode: warn but don't fail
			fmt.Fprintf(os.Stderr, "Warning: Checksum verification failed for %s: %v\n", params.LocalPath, checksumErr)
			fmt.Fprintf(os.Stderr, "    Continuing because --skip-checksum flag is set\n")
		} else {
			// Strict mode (default): fail on checksum mismatch
			return fmt.Errorf("checksum verification failed for %s: %w\n\nTo download despite checksum mismatch, use --skip-checksum flag (not recommended)", params.LocalPath, checksumErr)
		}
	}

	checksumTimer.StopWithThroughput(fileInfo.DecryptedSize)

	// v3.6.2: Log overall completion
	overallTimer.StopWithThroughput(fileInfo.DecryptedSize)

	// v4.0.0: Clean up resume state file on successful download.
	// This prevents stale resume state from accumulating and ensures
	// future downloads of the same file don't erroneously attempt to resume.
	state.DeleteDownloadState(params.LocalPath)

	return nil
}

// getExpectedSHA512 extracts the expected SHA-512 hash from checksums.
// Returns empty string if no SHA-512 checksum is available.
// v4.4.2: Helper for computed hash comparison.
func getExpectedSHA512(checksums []models.FileChecksum) string {
	for _, cs := range checksums {
		switch cs.HashFunction {
		case "sha512", "SHA-512", "SHA512":
			return cs.FileHash
		}
	}
	return ""
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
// v4.4.1: Added retry logic to handle transient filesystem cache issues
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

	// v4.4.1: Retry logic to handle transient filesystem cache issues.
	// On some systems (especially macOS), even after Sync()+Close(), the filesystem
	// cache may not be fully coherent for subsequent reads. Retrying with a small
	// delay usually resolves this.
	const maxRetries = 3
	var lastActualHash string

	for attempt := 1; attempt <= maxRetries; attempt++ {
		actualHash, err := computeFileChecksum(localPath)
		if err != nil {
			if attempt < maxRetries {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return fmt.Errorf("failed to calculate checksum after %d attempts: %w", maxRetries, err)
		}

		lastActualHash = actualHash

		// Compare checksums (case-insensitive)
		if equalIgnoreCase(actualHash, expectedHash) {
			return nil // Success!
		}

		// Checksum mismatch - retry with delay
		if attempt < maxRetries {
			time.Sleep(100 * time.Millisecond)
		}
	}

	// All retries failed
	return fmt.Errorf("checksum mismatch: expected %s=%s, got %s (after %d attempts)", hashAlgorithm, expectedHash, lastActualHash, maxRetries)
}

// computeFileChecksum opens a file and computes its SHA-512 hash.
// v4.4.1: Extracted to support retry logic in verifyChecksum.
func computeFileChecksum(localPath string) (string, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	hash := sha512.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
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

