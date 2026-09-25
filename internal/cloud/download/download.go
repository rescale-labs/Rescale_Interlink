// Package download provides the canonical entry point for file downloads from Rescale cloud storage.
// This package consolidates all download functionality into a single entry point.
package download

import (
	"context"
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
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/validation"
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

	// Optional: Called when a storage operation is retried, so the caller can
	// surface it (progress bar label, log line). Runs on a transfer goroutine.
	// When nil, the provider reports retries on its own (OutputWriter or stderr).
	OnRetry func(cloud.RetryEvent)

	// Optional: Checksum handling
	// false (default) = strict mode - fail on checksum mismatch
	// true = skip mode - warn but don't fail on checksum mismatch
	SkipChecksum bool
}

// newProvider builds the S3 or Azure provider a download reads from. A test
// replaces it.
var newProvider = providers.NewFactory().NewTransferFromStorageInfo

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

	cloud.TimingLog(params.OutputWriter, "File: %s (%s)", validation.QuoteUnsafe(fileInfo.Name), cloud.FormatBytes(fileInfo.DecryptedSize))

	// Get the global credential manager (caches user profile and credentials)
	credManager := credentials.GetManager(params.APIClient)

	// Skip GetUserProfile() when scan provided storage metadata in FileInfo.
	// getStorageInfo() only needs profile as fallback when fileInfo.Storage is nil.
	// This eliminates a cache-lookup per file and avoids cache-miss latency after sleep/wake.
	var storageInfo *models.StorageInfo
	if fileInfo.Storage != nil && fileInfo.Storage.StorageType != "" {
		storageInfo = getStorageInfo(fileInfo, nil)
	} else {
		profile, err := credManager.GetUserProfile(ctx)
		if err != nil {
			return fmt.Errorf("failed to get user profile: %w", err)
		}
		storageInfo = getStorageInfo(fileInfo, profile)
	}

	// Create provider using factory (S3 or Azure based on storage type)
	provider, err := newProvider(ctx, storageInfo, params.APIClient)
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}

	// Retries happen several layers down in the provider client; hand it the
	// caller's hooks so a stalled transfer is visible instead of silent.
	if setter, ok := provider.(cloud.RetryObserverSetter); ok {
		setter.SetRetryObserver(cloud.RetryObserver{
			Writer:  params.OutputWriter,
			OnRetry: params.OnRetry,
		})
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

	transferTimer := cloud.StartTimer(params.OutputWriter, "Download transfer")

	// Get computed hash from download to avoid re-reading file for verification.
	// This eliminates the race condition where post-download verification re-reads
	// the file and may get stale cache data.
	computedHash, err := downloader.Download(ctx, downloadParams)
	if err != nil {
		return fmt.Errorf("%s download failed: %w", storageInfo.StorageType, err)
	}

	transferTimer.StopWithThroughput(fileInfo.DecryptedSize)

	// Verify file exists and has expected size before checksum verification.
	// This provides a clearer error message if the download failed silently (e.g., 0 bytes written).
	fi, err := os.Stat(params.LocalPath)
	if err != nil {
		return fmt.Errorf("failed to stat downloaded file: %w", err)
	}
	if err := verifyDownloadedSize(params.LocalPath, fi.Size(), fileInfo.DecryptedSize); err != nil {
		return err
	}

	checksumTimer := cloud.StartTimer(params.OutputWriter, "Checksum verification")
	// Through the progress display when one is live: a raw stderr write lands inside its frame.
	if err := checkDownloadChecksum(params.LocalPath, computedHash, fileInfo.FileChecksums, params.SkipChecksum, progress.SinkWriter(os.Stderr)); err != nil {
		return err
	}
	checksumTimer.StopWithThroughput(fileInfo.DecryptedSize)

	overallTimer.StopWithThroughput(fileInfo.DecryptedSize)

	// Clean up resume state file on successful download.
	// This prevents stale resume state from accumulating and ensures
	// future downloads of the same file don't erroneously attempt to resume.
	state.DeleteDownloadState(params.LocalPath)

	return nil
}

// checkDownloadChecksum holds a finished download to the SHA-512 the API
// reported for it. computedHash, when set, was taken while the file was written
// and saves reading it back. Integrity rests on this check alone (the CBC format
// is unauthenticated), so a file whose checksums include no SHA-512 is reported
// as unverified rather than passed in silence.
func checkDownloadChecksum(localPath, computedHash string, checksums models.FileChecksums, skip bool, warnings io.Writer) error {
	expectedHash := checksums.SHA512()
	if expectedHash == "" {
		if algorithms := checksums.Algorithms(); len(algorithms) > 0 {
			fmt.Fprintf(warnings, "Warning: %s was not verified: its checksums (%s) include no SHA-512\n",
				localPath, strings.Join(algorithms, ", "))
		}
		return nil
	}

	var checksumErr error
	if computedHash != "" {
		if !strings.EqualFold(computedHash, expectedHash) {
			checksumErr = fmt.Errorf("checksum mismatch: expected SHA-512=%s, got %s", expectedHash, computedHash)
		}
	} else {
		checksumErr = verifyChecksum(localPath, expectedHash)
	}
	if checksumErr == nil {
		return nil
	}
	if !skip {
		return quarantineCorruptFile(localPath, checksumErr)
	}
	fmt.Fprintf(warnings, "Warning: Checksum verification failed for %s: %v\n", localPath, checksumErr)
	fmt.Fprintf(warnings, "    Continuing because --skip-checksum flag is set\n")
	return nil
}

// corruptFileSuffix marks a downloaded file that failed checksum verification.
const corruptFileSuffix = ".corrupt"

// verifyDownloadedSize checks a finished download against the size the API
// reported for the file. An expected size of zero means the API reported none,
// so there is nothing to check.
//
// Size is the only completeness check that does not need the file to carry a
// checksum, and it is the check every downstream presence test already makes —
// the auto-download daemon's poll and the CLI's skip-existing modes both take a
// file of the right length for a finished download. So a file of the wrong
// length fails here and is moved aside like a corrupt one, rather than being
// left to masquerade as complete.
func verifyDownloadedSize(localPath string, actual, expected int64) error {
	if expected <= 0 || actual == expected {
		return nil
	}

	// An empty file is its own diagnosis and is not worth preserving.
	if actual == 0 {
		return fmt.Errorf("download failed: file is empty (0 bytes) - possible write error or filesystem issue")
	}

	return quarantineWrongSize(localPath, actual, expected)
}

// quarantineCorruptFile moves a download that failed checksum verification out
// of the way and returns the error to report to the caller.
func quarantineCorruptFile(localPath string, checksumErr error) error {
	return fmt.Errorf("checksum verification failed for %s: %w\n\n%s\n\nTo download despite checksum mismatch, use --skip-checksum flag (not recommended)",
		localPath, checksumErr, quarantineFile(localPath))
}

// quarantineWrongSize moves a download that ended up the wrong length out of the
// way and returns the error to report to the caller. There is no flag to skip
// this one: a short or long file is not a download that merely failed to verify.
func quarantineWrongSize(localPath string, got, want int64) error {
	return fmt.Errorf("download failed for %s: the file is %d bytes on disk but %d bytes were expected\n\n%s",
		localPath, got, want, quarantineFile(localPath))
}

// quarantineFile moves a bad download aside and reports what became of it.
//
// The bad bytes must not be left at localPath. Callers decide a file is already
// downloaded by comparing the on-disk size against the expected size (the
// auto-download daemon's poll, the CLI's skip-existing modes), and a file that
// fails its checksum is still full-size — leaving it in place makes the next poll
// report the download as a success. Renaming is preferred over deleting so the
// bytes stay available for diagnosis; deletion is the fallback, and when neither
// works the returned sentence says so.
func quarantineFile(localPath string) string {
	quarantinePath := localPath + corruptFileSuffix

	if renameErr := os.Rename(localPath, quarantinePath); renameErr != nil {
		if removeErr := os.Remove(localPath); removeErr != nil {
			return fmt.Sprintf("WARNING: the corrupt file is still at %s — it could not be moved aside (%v) or deleted (%v); delete it before retrying",
				localPath, renameErr, removeErr)
		}
		return "The corrupt file was deleted"
	}

	return fmt.Sprintf("The corrupt file was moved to %s", quarantinePath)
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
func verifyChecksum(localPath, expectedHash string) error {
	// Retry to handle transient filesystem cache issues.
	// On some systems (especially macOS), even after Sync()+Close(), the filesystem
	// cache may not be fully coherent for subsequent reads. Retrying with a small
	// delay usually resolves this.
	const maxRetries = 3
	var lastActualHash string

	for attempt := 1; attempt <= maxRetries; attempt++ {
		actualHash, err := encryption.CalculateSHA512(localPath)
		if err != nil {
			if attempt < maxRetries {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return fmt.Errorf("failed to calculate checksum after %d attempts: %w", maxRetries, err)
		}

		lastActualHash = actualHash

		// Compare checksums (case-insensitive)
		if strings.EqualFold(actualHash, expectedHash) {
			return nil // Success!
		}

		// Checksum mismatch - retry with delay
		if attempt < maxRetries {
			time.Sleep(100 * time.Millisecond)
		}
	}

	// All retries failed
	return fmt.Errorf("checksum mismatch: expected SHA-512=%s, got %s (after %d attempts)", expectedHash, lastActualHash, maxRetries)
}
