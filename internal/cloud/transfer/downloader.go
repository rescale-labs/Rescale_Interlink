// Package transfer provides unified upload and download orchestration.
// This file implements the unified download orchestrator that works with any CloudTransfer provider.
//
// Format versions supported:
//   - v0 (legacy): Download all → decrypt all, requires .encrypted temp file
//   - v1 (HKDF): Per-part key derivation, parallel decryption, no temp file
//   - v2 (CBC streaming): Parallel download + sequential CBC decryption, no temp file (v3.4.1+)
package transfer

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/storage"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
)

// Downloader orchestrates file downloads using a CloudTransfer provider.
// It automatically detects encryption format (legacy v0 or streaming v1),
// supports resume for both formats, and enables concurrent chunk downloads
// when a TransferHandle is provided.
type Downloader struct {
	provider cloud.CloudTransfer
}

// NewDownloader creates a new Downloader with the given provider.
func NewDownloader(provider cloud.CloudTransfer) *Downloader {
	return &Downloader{
		provider: provider,
	}
}

// DownloadPrep contains prepared download parameters and detected format info.
type DownloadPrep struct {
	Params         cloud.DownloadParams
	TransferHandle *transfer.Transfer

	// Format detection results
	FormatVersion int    // 0 = legacy, 1 = streaming
	FileID        string // For streaming format (base64 encoded)
	PartSize      int64  // For streaming format

	// Encryption key and IV (decoded from file info)
	EncryptionKey []byte
	IV            []byte // For legacy format only

	// v4.4.2: Hash computed during download to avoid re-reading file for verification
	ComputedHash string
}

// Download downloads and decrypts a file using the configured provider.
// The download mode (legacy vs streaming) is automatically detected from cloud metadata.
//
// v4.4.2: Returns the computed SHA-512 hash of the downloaded file along with any error.
// This eliminates the race condition where post-download verification re-reads the file
// and may get stale cache data. The caller should use this hash for verification instead
// of re-reading the file.
//
// Default behavior:
//   - Automatically detects encryption format (legacy v0 or streaming v1)
//   - Resumes from last completed chunk if resume state exists
//   - Uses concurrent chunk downloads if TransferHandle has threads > 1
//
// Legacy format (v0):
//   - Downloads encrypted file, then decrypts as a whole
//   - Uses single IV from metadata
//
// Streaming format (v1):
//   - Downloads and decrypts part by part
//   - Uses per-part key derivation from master key + file ID
//
// Supports cross-storage downloads via FileInfoSetter interface.
func (d *Downloader) Download(ctx context.Context, params cloud.DownloadParams) (string, error) {
	// Validate required parameters
	if params.RemotePath == "" {
		return "", fmt.Errorf("remote path is required")
	}
	if params.LocalPath == "" {
		return "", fmt.Errorf("local path is required")
	}
	if params.FileInfo == nil {
		return "", fmt.Errorf("file info is required")
	}
	if params.FileInfo.EncodedEncryptionKey == "" {
		return "", fmt.Errorf("encryption key is required in file info")
	}

	// Set file info for cross-storage credential fetching.
	// If the provider supports FileInfoSetter, call SetFileInfo before any operations.
	// This enables downloading files from storage different than user's default
	// (e.g., S3 user downloading Azure-stored job outputs).
	if fileInfoSetter, ok := d.provider.(cloud.FileInfoSetter); ok {
		fileInfoSetter.SetFileInfo(params.FileInfo)
	}

	// Decode encryption key from file info
	encryptionKey, err := encryption.DecodeBase64(params.FileInfo.EncodedEncryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to decode encryption key: %w", err)
	}

	// Decode IV if present (for legacy format)
	var iv []byte
	if params.FileInfo.IV != "" {
		iv, err = encryption.DecodeBase64(params.FileInfo.IV)
		if err != nil {
			return "", fmt.Errorf("failed to decode IV: %w", err)
		}
	}

	// v3.6.2: Log thread allocation
	threads := 1
	if params.TransferHandle != nil {
		threads = params.TransferHandle.GetThreads()
	}
	cloud.TimingLog(params.OutputWriter, "Download threads: %d", threads)

	// Log download start if output writer is provided
	if params.OutputWriter != nil {
		filename := filepath.Base(params.LocalPath)
		if threads > 1 {
			fmt.Fprintf(params.OutputWriter, "Starting download of %s with %d concurrent threads\n",
				filename, threads)
		}
	}

	// v3.6.2: Track format detection
	formatTimer := cloud.StartTimer(params.OutputWriter, "Format detection")

	// Check if provider supports format detection
	formatDetector, hasFormatDetection := d.provider.(StreamingConcurrentDownloader)
	if hasFormatDetection {
		// Detect format from cloud metadata
		// DetectFormat returns IV for legacy format (v0)
		formatVersion, fileID, partSize, metadataIV, err := formatDetector.DetectFormat(ctx, params.RemotePath)
		if err != nil {
			// Fall back to using IV presence to detect format
			if iv != nil {
				formatVersion = 0 // Legacy format
			} else {
				// If no IV and no format detected, assume streaming
				formatVersion = 1
			}
			if params.OutputWriter != nil {
				fmt.Fprintf(params.OutputWriter, "Note: Format detection failed (%v), inferred format version %d\n", err, formatVersion)
			}
			formatTimer.StopWithMessage("fallback version=%d", formatVersion)
		} else {
			// v3.4.0: Show successful format detection result
			if params.OutputWriter != nil {
				fmt.Fprintf(params.OutputWriter, "Format detection successful: version=%d\n", formatVersion)
			}
			formatTimer.StopWithMessage("version=%d part_size=%s", formatVersion, cloud.FormatBytes(partSize))
		}

		// Use IV from metadata if available (for legacy format), otherwise use from FileInfo
		effectiveIV := iv
		if metadataIV != nil && len(metadataIV) > 0 {
			effectiveIV = metadataIV
		}

		// Prepare download with detected format
		prep := &DownloadPrep{
			Params:         params,
			TransferHandle: params.TransferHandle,
			FormatVersion:  formatVersion,
			FileID:         fileID,
			PartSize:       partSize,
			EncryptionKey:  encryptionKey,
			IV:             effectiveIV,
		}

		// Route to appropriate download method based on format version
		// v0: Legacy (download all → decrypt all → temp file)
		// v1: HKDF streaming (per-part keys, parallel decryption possible)
		// v2: CBC streaming (sequential decryption, no temp file) - v3.2.4+
		// v3.4.0: Debug logging for download path selection
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Format detection: version=%d (0=legacy, 1=HKDF, 2=CBC streaming)\n", formatVersion)
		}
		switch formatVersion {
		case 2:
			// v4.0.0: CBC streaming download with fast verification.
			// 1. Quick 32-byte probe verifies key/IV work BEFORE full download
			// 2. If probe fails, return clear error immediately (no wasted bandwidth)
			// 3. If probe passes, proceed with streaming download
			// 4. On chunk size errors, use probe to find correct size (fast)
			// 5. Fall back to legacy only if streaming isn't supported

			// Check if provider supports part downloads (needed for verification)
			partDownloader, hasPartDownload := d.provider.(StreamingPartDownloader)
			if hasPartDownload {
				// Get encrypted size for verification probe
				encryptedSize, sizeErr := partDownloader.GetEncryptedSize(ctx, params.RemotePath)
				if sizeErr == nil && encryptedSize > 0 {
					// Run quick 32-byte verification probe
					verifyErr := d.verifyDecryptionQuick(ctx, partDownloader, params.RemotePath, encryptedSize, effectiveIV, encryptionKey)
					if verifyErr != nil {
						// Verification failed - file cannot be decrypted with this key/IV
						// Return early with clear error message
						return "", verifyErr
					}
				}
			}

			// Verification passed (or skipped) - proceed with download
			err := d.downloadCBCStreaming(ctx, prep)
			if err != nil {
				errStr := err.Error()
				isDecryptionError := strings.Contains(errStr, "padding") ||
					strings.Contains(errStr, "decrypt") ||
					strings.Contains(errStr, "chunk size")

				if isDecryptionError {
					// CBC streaming failed - try legacy as fallback
					// (CBC streaming produces identical ciphertext to legacy)
					if params.OutputWriter != nil {
						fmt.Fprintf(params.OutputWriter, "CBC streaming failed, falling back to legacy download...\n")
					}
					if outFile, openErr := os.Create(params.LocalPath); openErr == nil {
						outFile.Close()
					}
					// v4.4.2: Return hash from legacy fallback
					if legacyErr := d.downloadLegacy(ctx, prep); legacyErr != nil {
						return "", legacyErr
					}
					return prep.ComputedHash, nil
				}
				return "", err
			}
			return prep.ComputedHash, nil
		case 1:
			if err := d.downloadStreaming(ctx, prep, formatDetector); err != nil {
				return "", err
			}
			return prep.ComputedHash, nil
		default:
			if err := d.downloadLegacy(ctx, prep); err != nil {
				return "", err
			}
			return prep.ComputedHash, nil
		}
	}

	// Provider doesn't support format detection - fall back to legacy behavior
	// Use IV presence to determine format
	formatTimer.StopWithMessage("no format detection support, using IV presence")

	prep := &DownloadPrep{
		Params:         params,
		TransferHandle: params.TransferHandle,
		FormatVersion:  0, // Assume legacy
		EncryptionKey:  encryptionKey,
		IV:             iv,
	}

	if iv == nil {
		// No IV suggests streaming format, but without detection we can't proceed
		// The provider's Download method will handle this
		prep.FormatVersion = 1
	}

	// Delegate to provider's Download method
	// v4.4.2: Provider's Download doesn't return hash, so return empty string
	// (This legacy path is rarely used - most providers support format detection)
	if err := d.provider.Download(ctx, params); err != nil {
		return "", err
	}
	return "", nil // No computed hash available from legacy provider path
}

// downloadLegacy downloads using legacy (v0) format.
// The entire file is downloaded encrypted, then decrypted.
// Orchestrator handles disk space, temp file, and decryption.
func (d *Downloader) downloadLegacy(ctx context.Context, prep *DownloadPrep) error {
	if prep.Params.OutputWriter != nil {
		fmt.Fprintf(prep.Params.OutputWriter, "Using legacy format (v0) download\n")
	}

	// Check if provider implements LegacyDownloader interface
	legacyProvider, ok := d.provider.(LegacyDownloader)
	if !ok {
		// Fall back to provider's Download method (old code path)
		return d.provider.Download(ctx, prep.Params)
	}

	// Orchestrator handles: disk space, temp file, decryption
	localPath := prep.Params.LocalPath
	targetDir := filepath.Dir(localPath)
	encryptedPath := localPath + ".encrypted"

	// Get DECRYPTED file size from file info for disk space calculation
	// Note: The encrypted file in storage is slightly larger due to PKCS7 padding
	// DecryptedSize is what we'll end up with after decryption
	fileSize := prep.Params.FileInfo.DecryptedSize

	// IMPORTANT: Pass 0 as FileSize to let the provider fetch the actual encrypted
	// blob size from storage. Using DecryptedSize would truncate the encrypted file
	// incorrectly since encrypted size = DecryptedSize + PKCS7 padding (1-16 bytes).
	// This was causing "input not full blocks" panics when the truncated encrypted
	// file wasn't a multiple of AES block size (16 bytes).

	// CHECK DISK SPACE before download (with 15% safety buffer for both encrypted + decrypted)
	// Need space for: encrypted file + decrypted file during transition
	if err := diskspace.CheckAvailableSpace(localPath, fileSize*2, 1.15); err != nil {
		return &diskspace.InsufficientSpaceError{
			Path:           localPath,
			RequiredBytes:  fileSize * 2,
			AvailableBytes: diskspace.GetAvailableSpace(targetDir),
		}
	}

	// v4.5.9: Retry cleanup of temp file with backoff for Windows file locking.
	// Log the actual error even when OutputWriter is nil.
	defer func() {
		var lastErr error
		for i := 0; i < 3; i++ {
			if lastErr = os.Remove(encryptedPath); lastErr == nil || os.IsNotExist(lastErr) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		if prep.Params.OutputWriter != nil {
			fmt.Fprintf(prep.Params.OutputWriter, "Warning: Failed to cleanup encrypted temp file %s after 3 attempts: %v\n", encryptedPath, lastErr)
		}
		log.Printf("Warning: Failed to cleanup encrypted temp file %s after 3 attempts: %v", encryptedPath, lastErr)
	}()

	// v4.0.0: Report 0% progress immediately before download starts.
	// This matches upload behavior and shows users the transfer has started.
	if prep.Params.ProgressCallback != nil {
		prep.Params.ProgressCallback(0.0)
	}

	// Download encrypted file via provider
	// FileSize set to 0 so provider fetches actual encrypted blob size from storage
	downloadParams := LegacyDownloadParams{
		RemotePath:       prep.Params.RemotePath,
		EncryptedPath:    encryptedPath,
		EncryptionKey:    prep.EncryptionKey,
		IV:               prep.IV,
		FileSize:         0, // Let provider fetch actual encrypted size from storage
		FileInfo:         prep.Params.FileInfo,
		TransferHandle:   prep.TransferHandle,
		ProgressCallback: prep.Params.ProgressCallback,
		OutputWriter:     prep.Params.OutputWriter,
	}

	if err := legacyProvider.DownloadEncryptedFile(ctx, downloadParams); err != nil {
		// Convert disk full errors to standard type
		if storage.IsDiskFullError(err) {
			return &diskspace.InsufficientSpaceError{
				Path:           encryptedPath,
				RequiredBytes:  fileSize,
				AvailableBytes: diskspace.GetAvailableSpace(targetDir),
			}
		}
		return fmt.Errorf("download failed: %w", err)
	}

	// Decrypt the file
	if prep.Params.OutputWriter != nil {
		fmt.Fprintf(prep.Params.OutputWriter, "Decrypting %s...\n", filepath.Base(localPath))
	}

	// v4.4.2: Use DecryptFileWithHash to compute hash during decryption.
	// This avoids the race condition where post-download verification re-reads
	// the file and may get stale cache data.
	computedHash, err := encryption.DecryptFileWithHash(encryptedPath, localPath, prep.EncryptionKey, prep.IV)
	if err != nil {
		// Check for disk full during decryption
		if storage.IsDiskFullError(err) {
			return &diskspace.InsufficientSpaceError{
				Path:           localPath,
				RequiredBytes:  fileSize,
				AvailableBytes: diskspace.GetAvailableSpace(targetDir),
			}
		}
		return fmt.Errorf("decryption failed: %w", err)
	}
	prep.ComputedHash = computedHash

	// Complete transfer handle if provided
	if prep.TransferHandle != nil {
		prep.TransferHandle.Complete()
	}

	return nil
}

// downloadJob represents a part download task for the worker pool.
type downloadJob struct {
	partIndex int64
	startByte int64
	length    int64
}

// downloadResult holds the result of a download worker.
type downloadResult struct {
	partIndex  int64
	ciphertext []byte
	err        error
}

// v4.0.4: Removed dead code - probeChunkSize() and formatSizeList() were replaced
// by the deterministic ChunkSizeFromFileSize() approach in v3.2.4. See line 672.

// verifyDecryptionQuick performs a fast 32-byte probe to verify the key/IV will produce
// valid decrypted output. This catches wrong keys, corrupted files, or incompatible
// encryption BEFORE downloading the full file.
//
// v4.0.0: Added to fail fast on undecryptable files without wasting bandwidth.
//
// Returns nil if decryption verification passes, error otherwise.
func (d *Downloader) verifyDecryptionQuick(
	ctx context.Context,
	partDownloader StreamingPartDownloader,
	remotePath string,
	encryptedSize int64,
	initialIV []byte,
	encryptionKey []byte,
) error {
	// We need the last 32 bytes: 16 bytes IV + 16 bytes last block
	// For files < 32 bytes, use initial IV
	var probeStart int64
	var probeSize int64

	if encryptedSize <= 16 {
		// Very small file (empty or near-empty after encryption)
		// Just download all and verify
		probeStart = 0
		probeSize = encryptedSize
	} else if encryptedSize < 32 {
		// Small file - download all, use initial IV
		probeStart = 0
		probeSize = encryptedSize
	} else {
		// Normal case - download last 32 bytes
		probeStart = encryptedSize - 32
		probeSize = 32
	}

	// Download probe bytes
	probeData, err := partDownloader.DownloadEncryptedRange(ctx, remotePath, probeStart, probeSize, nil)
	if err != nil {
		return fmt.Errorf("verification probe failed: %w", err)
	}

	// Extract IV and last block
	var iv, lastBlock []byte
	if len(probeData) < 16 {
		return fmt.Errorf("encrypted file too small: %d bytes", len(probeData))
	}

	if len(probeData) == 16 {
		// Only one block - use initial IV
		iv = initialIV
		lastBlock = probeData
	} else if len(probeData) < 32 {
		// Use initial IV, last block is the last 16 bytes
		iv = initialIV
		lastBlock = probeData[len(probeData)-16:]
	} else {
		// Normal case: IV is bytes 0-15, last block is bytes 16-31
		iv = probeData[0:16]
		lastBlock = probeData[16:32]
	}

	// Try to decrypt last block
	decryptor, err := encryption.NewCBCStreamingDecryptor(encryptionKey, iv)
	if err != nil {
		return fmt.Errorf("failed to create decryptor for verification: %w", err)
	}

	// Decrypt as final part (will validate PKCS7 padding)
	_, err = decryptor.DecryptPart(lastBlock, true)
	if err != nil {
		return fmt.Errorf("decryption verification failed - the file cannot be decrypted with the provided key. "+
			"This may indicate: (1) the file was uploaded by a different tool, (2) the encryption metadata is incorrect, "+
			"(3) the file is corrupted, or (4) the file upload was incomplete. Original error: %w", err)
	}

	return nil
}

// getExpectedSHA512 extracts the expected SHA-512 hash from FileChecksums.
// Returns empty string if no SHA-512 checksum is available.
// v4.4.1: Helper for checksum-during-write feature.
func getExpectedSHA512(fileInfo *models.CloudFile) string {
	if fileInfo == nil || len(fileInfo.FileChecksums) == 0 {
		return ""
	}
	for _, cs := range fileInfo.FileChecksums {
		switch cs.HashFunction {
		case "sha512", "SHA-512", "SHA512":
			return cs.FileHash
		}
	}
	return ""
}

// downloadCBCStreaming downloads using CBC chaining format (v2) with parallel fetch.
// v3.4.1: Parallel download architecture - downloads happen in parallel, but decryption
// is sequential (CBC constraint) for 2-4x throughput improvement.
//
// The CBC decryption MUST be sequential because each part's IV is the last ciphertext block
// of the previous part. However, downloading CAN be parallelized - we download ahead and
// buffer parts, then decrypt in order as they become available.
func (d *Downloader) downloadCBCStreaming(ctx context.Context, prep *DownloadPrep) error {
	if prep.Params.OutputWriter != nil {
		fmt.Fprintf(prep.Params.OutputWriter, "Using CBC streaming format (v2) download with parallel fetch - no temp file\n")
	}

	// Check if provider supports part-level downloads
	partDownloader, ok := d.provider.(StreamingPartDownloader)
	if !ok {
		// Fall back to legacy if provider doesn't support part downloads
		if prep.Params.OutputWriter != nil {
			fmt.Fprintf(prep.Params.OutputWriter, "Note: Provider doesn't support part downloads, falling back to legacy\n")
		}
		return d.downloadLegacy(ctx, prep)
	}

	// Get encrypted size from cloud
	encryptedSize, err := partDownloader.GetEncryptedSize(ctx, prep.Params.RemotePath)
	if err != nil {
		return fmt.Errorf("failed to get encrypted size: %w", err)
	}

	// Create output file directly (no temp file!)
	outFile, err := os.Create(prep.Params.LocalPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	// v4.4.0: Track whether we successfully closed the file.
	// We DON'T use defer outFile.Close() because that causes a race condition:
	// the deferred Close() executes AFTER the function returns, but verifyChecksum()
	// is called IMMEDIATELY after this function returns. On Windows, this can cause
	// verification to read an empty file while Close() is still pending.
	// Instead, we close explicitly after Sync() on success, and use this cleanup
	// function to ensure the file is closed on error paths.
	fileClosed := false
	defer func() {
		if !fileClosed {
			outFile.Close()
		}
	}()

	// Create CBC streaming decryptor
	decryptor, err := encryption.NewCBCStreamingDecryptor(prep.EncryptionKey, prep.IV)
	if err != nil {
		return fmt.Errorf("failed to create CBC decryptor: %w", err)
	}

	// v4.4.1: Create hasher to compute checksum during write.
	// This eliminates the race condition where post-download verification
	// could read stale data from filesystem cache.
	hasher := sha512.New()
	expectedHash := getExpectedSHA512(prep.Params.FileInfo)

	// v4.0.0: Use part size from metadata, or probe to detect correct size for old files.
	// Files uploaded before v4.0.0 don't have partsize metadata, and the chunk size used
	// during upload may vary based on file size and memory constraints at upload time.
	// Wrong chunk size = wrong part boundaries = CBC decryption failure.
	decryptedSize := int64(0)
	if prep.Params.FileInfo != nil {
		decryptedSize = prep.Params.FileInfo.DecryptedSize
	}

	partSize := prep.PartSize
	if partSize == 0 {
		// v4.0.0: No partsize in metadata - use calculated size based on file size.
		// This is FAST (no probe download needed) and works for 99%+ of files.
		// The probe approach was slow (downloaded 64MB before starting) and unreliable
		// (couldn't handle memory-constrained uploads with non-standard chunk sizes).
		partSize = resources.ChunkSizeFromFileSize(decryptedSize)
		if prep.Params.OutputWriter != nil {
			fmt.Fprintf(prep.Params.OutputWriter, "Using calculated chunk size: %s (file missing partsize metadata)\n",
				cloud.FormatBytes(partSize))
		}
	}

	// v4.0.0 FIX: Calculate number of parts using encryptedSize (from S3), NOT decryptedSize!
	// The API's decryptedSize may not match the actual encrypted data size (e.g., 832 MiB vs 800 MiB),
	// especially if the file was compressed or if the metadata is stale.
	// We must download what's actually IN S3, not what the API says "should" be there.
	numParts := (encryptedSize + partSize - 1) / partSize
	if numParts < 1 {
		numParts = 1
	}

	// v3.4.1: Get download concurrency from TransferHandle (default to 4)
	concurrency := 4
	if prep.TransferHandle != nil && prep.TransferHandle.GetThreads() > 1 {
		concurrency = prep.TransferHandle.GetThreads()
	}

	// Context with cancellation for error propagation
	// Note: cancelDownload is deferred AFTER scaler goroutine is created (see below)
	// to ensure proper cleanup order: cancel context, then wait for scaler to finish.
	downloadCtx, cancelDownload := context.WithCancel(ctx)

	// Track first error for clean shutdown
	var firstErr error
	var errOnce sync.Once

	// Job channel for download workers
	jobChan := make(chan downloadJob, numParts)

	// Result channel for downloaded parts
	resultChan := make(chan downloadResult, concurrency*2)

	// Populate job queue
	for i := int64(0); i < numParts; i++ {
		startByte := i * partSize
		length := partSize
		if startByte+length > encryptedSize {
			length = encryptedSize - startByte
		}
		jobChan <- downloadJob{partIndex: i, startByte: startByte, length: length}
	}
	close(jobChan)

	// v3.4.2: Track worker count for dynamic scaling
	var workerCount int32 = int32(concurrency)

	// v4.0.0: Track download progress - declared before worker so closure can capture it
	var downloadedBytes int64 // Bytes downloaded from cloud (updated via streaming callback)

	// Download worker function - shared by initial workers and dynamically spawned workers
	downloadWorker := func(workerID int) {
		for job := range jobChan {
			// Check for cancellation
			select {
			case <-downloadCtx.Done():
				return
			default:
			}

			// v4.0.0: Progress callback updates downloadedBytes atomically during the download.
			// This provides smooth, byte-level progress (like uploads) instead of jumpy
			// per-part progress updates.
			progressCallback := func(bytesRead int64) {
				atomic.AddInt64(&downloadedBytes, bytesRead)
			}

			// Download this part (with streaming progress callback)
			ciphertext, downloadErr := partDownloader.DownloadEncryptedRange(
				downloadCtx, prep.Params.RemotePath, job.startByte, job.length, progressCallback)

			if downloadErr != nil {
				errOnce.Do(func() { firstErr = fmt.Errorf("failed to download part %d: %w", job.partIndex, downloadErr) })
				cancelDownload()
				resultChan <- downloadResult{partIndex: job.partIndex, err: downloadErr}
				return
			}

			// Send downloaded part to result channel
			select {
			case resultChan <- downloadResult{partIndex: job.partIndex, ciphertext: ciphertext}:
			case <-downloadCtx.Done():
				return
			}
		}
	}

	// v3.4.1: Start initial download worker pool - downloads parts in parallel
	var downloadWg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		downloadWg.Add(1)
		go func(workerID int) {
			defer downloadWg.Done()
			downloadWorker(workerID)
		}(i)
	}

	// v3.4.2: Background scaler - dynamically spawns additional workers when threads become available
	// This handles the case where other concurrent transfers finish and release their threads
	scalerDone := make(chan struct{})
	go func() {
		defer close(scalerDone)

		if prep.TransferHandle == nil {
			return // No transfer handle, can't scale
		}

		ticker := time.NewTicker(constants.ProgressUpdateInterval) // v4.0.4: use centralized constant
		defer ticker.Stop()

		for {
			select {
			case <-downloadCtx.Done():
				return
			case <-ticker.C:
				// Try to acquire up to 4 more threads at a time
				acquired := prep.TransferHandle.TryAcquireMore(4)
				if acquired > 0 {
					// Spawn additional workers
					for i := 0; i < acquired; i++ {
						newWorkerID := int(atomic.AddInt32(&workerCount, 1))
						downloadWg.Add(1)
						go func(wid int) {
							defer downloadWg.Done()
							downloadWorker(wid)
						}(newWorkerID)
					}
				}
			}
		}
	}()

	// v4.0.8: Ensure scaler goroutine is properly cleaned up on function exit.
	// Cancel the context first (signals scaler to stop), then wait for it to finish.
	// This prevents goroutine leaks, matching the upload pattern in upload.go.
	defer func() {
		cancelDownload()
		<-scalerDone
	}()

	// Close result channel when all workers finish
	go func() {
		downloadWg.Wait()
		close(resultChan)
	}()

	// Buffer for out-of-order parts (download may complete in any order)
	partBuffer := make(map[int64][]byte)
	var bufferMu sync.Mutex

	// Track progress
	// Note: downloadedBytes is declared earlier (before worker function) for closure capture
	var decryptedBytes int64 // Bytes actually written to disk
	decryptedParts := int64(0)
	nextPartToDecrypt := int64(0)

	// v4.0.0: Progress ticker for smooth updates (v4.0.4: use centralized constant).
	// Uses downloadedBytes (encrypted bytes downloaded) for smooth, byte-level progress.
	// This matches upload behavior where progress reflects actual network I/O.
	progressTicker := time.NewTicker(constants.ProgressUpdateInterval)
	progressDone := make(chan struct{})
	go func() {
		defer progressTicker.Stop()
		for {
			select {
			case <-progressTicker.C:
				if prep.Params.ProgressCallback != nil && encryptedSize > 0 {
					// v4.0.0: Progress = bytes downloaded / total encrypted bytes
					// downloadedBytes is updated in real-time via streaming callback,
					// providing smooth progress like uploads.
					currentBytes := atomic.LoadInt64(&downloadedBytes)
					progress := float64(currentBytes) / float64(encryptedSize)
					if progress > 1.0 {
						progress = 1.0
					}
					prep.Params.ProgressCallback(progress)
				}
			case <-progressDone:
				return
			}
		}
	}()
	defer close(progressDone)

	// v4.0.0: Report 0% progress immediately after init completes.
	// This matches upload behavior and shows users the transfer has started,
	// even before the first part is downloaded and decrypted.
	if prep.Params.ProgressCallback != nil {
		prep.Params.ProgressCallback(0.0)
	}

	// Process downloaded parts - decrypt in order
	for result := range resultChan {
		if result.err != nil {
			// Error already recorded, continue draining
			continue
		}

		// Buffer this part
		// v4.0.0: downloadedBytes is now updated via streaming progress callback,
		// so we don't add here (would double-count).
		bufferMu.Lock()
		partBuffer[result.partIndex] = result.ciphertext

		// Decrypt all consecutive parts starting from nextPartToDecrypt
		for {
			ciphertext, exists := partBuffer[nextPartToDecrypt]
			if !exists {
				bufferMu.Unlock()
				break
			}

			// Remove from buffer to free memory
			delete(partBuffer, nextPartToDecrypt)
			bufferMu.Unlock()

			// Determine if this is the final part
			isFinal := (nextPartToDecrypt == numParts-1)

			// Decrypt this part (sequential - CBC constraint)
			plaintext, decryptErr := decryptor.DecryptPart(ciphertext, isFinal)
			if decryptErr != nil {
				errOnce.Do(func() { firstErr = fmt.Errorf("failed to decrypt part %d: %w", nextPartToDecrypt, decryptErr) })
				cancelDownload()
				break
			}

			// Write plaintext directly to output file AND hasher
			// v4.4.1: Use MultiWriter to compute checksum during write
			bytesWritten, writeErr := outFile.Write(plaintext)
			if writeErr != nil {
				errOnce.Do(func() { firstErr = fmt.Errorf("failed to write part %d: %w", nextPartToDecrypt, writeErr) })
				cancelDownload()
				break
			}
			// v4.4.1: Also write to hasher for checksum-during-write
			hasher.Write(plaintext)

			// v3.6.3: Track actual bytes written for smooth progress
			atomic.AddInt64(&decryptedBytes, int64(bytesWritten))
			decryptedParts++
			nextPartToDecrypt++

			// v3.6.3: Removed per-part progress callback that caused jumpy progress.
			// Progress is now reported only by the ticker using decryptedBytes.
			// This prevents conflicts between two progress sources.

			bufferMu.Lock()
		}

	}

	// Check for errors
	if firstErr != nil {
		return firstErr
	}

	// Verify all parts were processed
	if decryptedParts != numParts {
		return fmt.Errorf("incomplete download: processed %d of %d parts", decryptedParts, numParts)
	}

	// v4.4.2: Store computed hash for caller (eliminates need to re-read file).
	// This hash was computed during write, so it's authoritative.
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	prep.ComputedHash = actualHash

	// v4.4.1: Verify checksum using hash computed during write.
	// This eliminates the race condition where post-download verification
	// could read stale data from filesystem cache.
	if expectedHash != "" {
		if !strings.EqualFold(actualHash, expectedHash) {
			return fmt.Errorf("checksum mismatch (computed during write): expected SHA-512=%s, got %s", expectedHash, actualHash)
		}
	}

	// Report 100% at end
	if prep.Params.ProgressCallback != nil {
		prep.Params.ProgressCallback(1.0)
	}

	// Complete transfer handle if provided
	if prep.TransferHandle != nil {
		prep.TransferHandle.Complete()
	}

	// v4.3.7: Sync file to disk before returning to ensure all data is written
	// before checksum verification. Fixes sporadic checksum failures where file
	// was read as empty due to OS buffer not being flushed.
	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync file to disk: %w", err)
	}

	// v4.4.0: Explicit Close() BEFORE returning to prevent race condition.
	// verifyChecksum() is called immediately after this function returns,
	// so we must ensure the file handle is fully closed before returning.
	// This fixes sporadic checksum failures on Windows where the deferred Close()
	// was still pending when verification tried to read the file.
	if err := outFile.Close(); err != nil {
		return fmt.Errorf("failed to close file: %w", err)
	}
	fileClosed = true

	return nil
}

// downloadStreaming downloads using streaming (v1) format.
// Parts are downloaded and decrypted individually.
func (d *Downloader) downloadStreaming(ctx context.Context, prep *DownloadPrep, streamingProvider StreamingConcurrentDownloader) error {
	if prep.Params.OutputWriter != nil {
		fmt.Fprintf(prep.Params.OutputWriter, "Using streaming format (v1) download\n")
	}

	// Determine concurrency
	threads := 1
	if prep.TransferHandle != nil {
		threads = prep.TransferHandle.GetThreads()
	}

	// Decode fileId for streaming decryption
	fileId, err := encryption.DecodeBase64(prep.FileID)
	if err != nil {
		return fmt.Errorf("failed to decode file ID: %w", err)
	}

	// For concurrent streaming download, use the provider's concurrent method
	if threads > 1 {
		return d.downloadStreamingConcurrent(ctx, prep, threads, streamingProvider, fileId)
	}

	// Sequential streaming download
	return d.downloadStreamingSequential(ctx, prep, streamingProvider, fileId)
}

// downloadStreamingSequential performs a sequential streaming download.
func (d *Downloader) downloadStreamingSequential(
	ctx context.Context,
	prep *DownloadPrep,
	streamingProvider StreamingConcurrentDownloader,
	fileId []byte,
) error {
	// Use the provider's streaming download method
	return streamingProvider.DownloadStreaming(
		ctx,
		prep.Params.RemotePath,
		prep.Params.LocalPath,
		prep.EncryptionKey,
		prep.Params.ProgressCallback,
	)
}

// downloadStreamingConcurrent performs a concurrent streaming download.
// Parts are downloaded and decrypted in parallel using worker goroutines.
// Each part is independently encrypted with a key derived from (masterKey, fileId, partIndex),
// enabling true parallel decryption without dependencies between parts.
func (d *Downloader) downloadStreamingConcurrent(
	ctx context.Context,
	prep *DownloadPrep,
	threads int,
	streamingProvider StreamingConcurrentDownloader,
	fileId []byte,
) error {
	// Check if provider supports part-level downloads
	partDownloader, ok := streamingProvider.(StreamingPartDownloader)
	if !ok {
		// Fall back to sequential if provider doesn't support part downloads
		if prep.Params.OutputWriter != nil {
			fmt.Fprintf(prep.Params.OutputWriter, "Note: Provider doesn't support concurrent streaming, using sequential mode\n")
		}
		return d.downloadStreamingSequential(ctx, prep, streamingProvider, fileId)
	}

	if prep.Params.OutputWriter != nil {
		fmt.Fprintf(prep.Params.OutputWriter, "Using concurrent streaming download with %d threads\n", threads)
	}

	// Ensure transfer handle is completed when done
	if prep.TransferHandle != nil {
		defer prep.TransferHandle.Complete()
	}

	// Get streaming metadata from provider
	encryptedSize, err := partDownloader.GetEncryptedSize(ctx, prep.Params.RemotePath)
	if err != nil {
		return fmt.Errorf("failed to get encrypted file size: %w", err)
	}

	// Calculate encrypted part size (plaintext partSize + PKCS7 padding = multiple of 16)
	encryptedPartSize := encryption.CalculateEncryptedPartSize(prep.PartSize)

	// Calculate number of parts
	numParts := (encryptedSize + encryptedPartSize - 1) / encryptedPartSize

	// Calculate total plaintext size for file pre-allocation
	// Last part may be smaller, so we calculate based on part count
	var totalPlaintextSize int64
	for partIdx := int64(0); partIdx < numParts; partIdx++ {
		encStart := partIdx * encryptedPartSize
		encEnd := encStart + encryptedPartSize
		if encEnd > encryptedSize {
			encEnd = encryptedSize
		}
		partEncryptedSize := encEnd - encStart
		// Remove PKCS7 padding (1-16 bytes per part)
		// Worst case: each part has 16 bytes of padding, best case: 1 byte
		// For accurate size, we'd need to download and decrypt, so estimate
		// The file will be truncated to correct size after last part
		if partIdx == numParts-1 {
			// Last part: estimate based on encrypted size minus padding
			totalPlaintextSize += partEncryptedSize - 1 // At least 1 byte padding
		} else {
			// Full parts: plaintext = partSize exactly
			totalPlaintextSize += prep.PartSize
		}
	}

	// Create output file and pre-allocate
	outFile, err := os.OpenFile(prep.Params.LocalPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	// v4.4.0: Track whether we successfully closed the file (see downloadCBCStreaming for explanation)
	fileClosed := false
	defer func() {
		if !fileClosed {
			outFile.Close()
		}
	}()

	// Pre-allocate file to maximum possible size (will truncate later)
	maxPossibleSize := numParts * prep.PartSize
	if err := outFile.Truncate(maxPossibleSize); err != nil {
		return fmt.Errorf("failed to pre-allocate file: %w", err)
	}

	// Create streaming decryptor (thread-safe for per-part decryption)
	decryptor, err := encryption.NewStreamingDecryptor(prep.EncryptionKey, fileId, prep.PartSize)
	if err != nil {
		return fmt.Errorf("failed to create streaming decryptor: %w", err)
	}

	// Set up concurrent download
	type partJob struct {
		partIndex         int64
		encryptedStart    int64
		encryptedEnd      int64
		plaintextOffset   int64
	}

	type partResult struct {
		partIndex       int64
		plaintextSize   int64
		err             error
	}

	// Create channels
	jobChan := make(chan partJob, threads*2)
	resultChan := make(chan partResult, numParts)

	// Context with cancellation for error handling
	opCtx, cancelOp := context.WithCancel(ctx)
	defer cancelOp()

	// Error handling
	var firstError error
	var errorMu sync.Mutex
	var errorOnce sync.Once
	setError := func(err error) {
		errorOnce.Do(func() {
			errorMu.Lock()
			firstError = err
			errorMu.Unlock()
			cancelOp()
		})
	}

	// Progress tracking - v3.6.3: Track decrypted bytes for accurate progress
	var decryptedBytes int64
	progressTicker := time.NewTicker(300 * time.Millisecond)
	defer progressTicker.Stop()

	decryptedSize := int64(0)
	if prep.Params.FileInfo != nil {
		decryptedSize = prep.Params.FileInfo.DecryptedSize
	}

	progressDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-progressTicker.C:
				if prep.Params.ProgressCallback != nil && decryptedSize > 0 {
					// v3.6.3: Use decrypted bytes for accurate progress
					currentBytes := atomic.LoadInt64(&decryptedBytes)
					prep.Params.ProgressCallback(float64(currentBytes) / float64(decryptedSize))
				}
			case <-progressDone:
				return
			}
		}
	}()
	defer close(progressDone)

	// v4.0.0: Report 0% progress immediately after init completes.
	// This matches upload behavior and shows users the transfer has started,
	// even before the first part is downloaded and decrypted.
	if prep.Params.ProgressCallback != nil {
		prep.Params.ProgressCallback(0.0)
	}

	// File write mutex (for WriteAt operations)
	var fileMu sync.Mutex

	// Start worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for job := range jobChan {
				select {
				case <-opCtx.Done():
					return
				default:
				}

				// Download encrypted part bytes
				// Note: For HKDF format, progress callback is nil since this legacy format
				// uses decryptedBytes for progress. CBC format (v2) uses streaming progress.
				ciphertext, err := partDownloader.DownloadEncryptedRange(
					opCtx,
					prep.Params.RemotePath,
					job.encryptedStart,
					job.encryptedEnd-job.encryptedStart,
					nil, // v4.0.0: Progress callback - nil for legacy HKDF format
				)
				if err != nil {
					setError(fmt.Errorf("failed to download part %d: %w", job.partIndex, err))
					resultChan <- partResult{partIndex: job.partIndex, err: err}
					return
				}

				// Decrypt this part (uses per-part key derivation)
				plaintext, err := decryptor.DecryptPart(job.partIndex, ciphertext)
				if err != nil {
					setError(fmt.Errorf("failed to decrypt part %d: %w", job.partIndex, err))
					resultChan <- partResult{partIndex: job.partIndex, err: err}
					return
				}

				// Write plaintext to correct offset in file
				fileMu.Lock()
				_, writeErr := outFile.WriteAt(plaintext, job.plaintextOffset)
				fileMu.Unlock()

				if writeErr != nil {
					setError(fmt.Errorf("failed to write part %d: %w", job.partIndex, writeErr))
					resultChan <- partResult{partIndex: job.partIndex, err: writeErr}
					return
				}

				// v3.6.3: Update progress using decrypted (plaintext) bytes for accuracy
				atomic.AddInt64(&decryptedBytes, int64(len(plaintext)))

				resultChan <- partResult{
					partIndex:     job.partIndex,
					plaintextSize: int64(len(plaintext)),
					err:           nil,
				}
			}
		}(i)
	}

	// Queue jobs
	go func() {
		defer close(jobChan)

		var plaintextOffset int64 = 0
		for partIdx := int64(0); partIdx < numParts; partIdx++ {
			select {
			case <-opCtx.Done():
				return
			default:
			}

			encStart := partIdx * encryptedPartSize
			encEnd := encStart + encryptedPartSize
			if encEnd > encryptedSize {
				encEnd = encryptedSize
			}

			jobChan <- partJob{
				partIndex:       partIdx,
				encryptedStart:  encStart,
				encryptedEnd:    encEnd,
				plaintextOffset: plaintextOffset,
			}

			// Calculate plaintext offset for next part
			// For non-last parts, this is exactly partSize
			// For last part, it doesn't matter (no next part)
			if partIdx < numParts-1 {
				plaintextOffset += prep.PartSize
			}
		}
	}()

	// Collect results and track final file size
	var finalFileSize int64
	var resultsReceived int64
	var lastPartPlaintextSize int64

	// Wait for workers to finish
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		if result.err != nil {
			// Error already set via setError
			continue
		}

		resultsReceived++

		// Track the last part's plaintext size for accurate file truncation
		if result.partIndex == numParts-1 {
			lastPartPlaintextSize = result.plaintextSize
		}
	}

	// Check for errors
	errorMu.Lock()
	if firstError != nil {
		err := firstError
		errorMu.Unlock()
		return err
	}
	errorMu.Unlock()

	// Calculate and set final file size
	if numParts > 0 {
		finalFileSize = (numParts-1)*prep.PartSize + lastPartPlaintextSize
	}

	// Truncate file to exact size
	if err := outFile.Truncate(finalFileSize); err != nil {
		return fmt.Errorf("failed to truncate file to final size: %w", err)
	}

	// v4.4.2: Compute hash after all parts written (still open file handle).
	// HKDF uses WriteAt for parallel writes, so we read sequentially to hash.
	// This avoids the race condition where post-download verification re-reads
	// the file and may get stale cache data.
	if _, err := outFile.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek to beginning for hash: %w", err)
	}
	hasher := sha512.New()
	if _, err := io.Copy(hasher, outFile); err != nil {
		return fmt.Errorf("failed to compute hash: %w", err)
	}
	prep.ComputedHash = hex.EncodeToString(hasher.Sum(nil))

	// v4.3.7: Sync file to disk before returning to ensure all data is written
	// before checksum verification. Fixes sporadic checksum failures where file
	// was read as empty due to OS buffer not being flushed.
	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync file to disk: %w", err)
	}

	// v4.4.0: Explicit Close() BEFORE returning (see downloadCBCStreaming for explanation)
	if err := outFile.Close(); err != nil {
		return fmt.Errorf("failed to close file: %w", err)
	}
	fileClosed = true

	// Report 100% progress
	if prep.Params.ProgressCallback != nil {
		prep.Params.ProgressCallback(1.0)
	}

	return nil
}

// StreamingConcurrentDownloader extends CloudTransfer with concurrent streaming download support.
// Providers that support format detection and streaming downloads implement this interface.
type StreamingConcurrentDownloader interface {
	cloud.CloudTransfer

	// DetectFormat detects the encryption format from cloud storage metadata.
	// Returns: formatVersion (0=legacy, 1=streaming), fileId (base64), partSize, iv (for legacy), error
	// For legacy format (v0): fileId and partSize will be empty/zero, iv is populated from metadata.
	// For streaming format (v1): fileId and partSize are populated, iv will be empty.
	// Returns iv for legacy format support.
	DetectFormat(ctx context.Context, remotePath string) (formatVersion int, fileId string, partSize int64, iv []byte, err error)

	// DownloadStreaming downloads and decrypts a file using streaming format (v1).
	// masterKey is the encryption key from Rescale API.
	// Format metadata (fileId, partSize) is read from cloud storage metadata.
	DownloadStreaming(ctx context.Context, remotePath, localPath string, masterKey []byte, progressCallback cloud.ProgressCallback) error
}

// StreamingPartDownloader extends StreamingConcurrentDownloader with methods needed
// for concurrent part-level downloads. Providers implement this to enable true parallel
// streaming downloads where each part can be downloaded and decrypted independently.
type StreamingPartDownloader interface {
	StreamingConcurrentDownloader

	// GetEncryptedSize returns the total encrypted size of the file in cloud storage.
	// This is needed to calculate the number of parts for concurrent download.
	GetEncryptedSize(ctx context.Context, remotePath string) (int64, error)

	// DownloadEncryptedRange downloads a specific byte range of the encrypted file.
	// Used by the concurrent download orchestrator to download individual parts.
	// Returns the raw encrypted bytes for the specified range.
	// v4.0.0: progressCallback (optional) is called with bytes downloaded for smooth progress.
	// Pass nil if progress tracking is not needed (e.g., during chunk size probing).
	DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64, progressCallback func(int64)) ([]byte, error)
}

// StreamingDownloadInitParams contains parameters for initializing a streaming download.
// Used by concurrent streaming download implementation (v3.2.0).
type StreamingDownloadInitParams struct {
	RemotePath   string    // Cloud storage path
	LocalPath    string    // Where to save the decrypted file
	MasterKey    []byte    // Master encryption key
	FileID       []byte    // File identifier for key derivation
	PartSize     int64     // Size of each encrypted part
	OutputWriter io.Writer // Optional output for status messages
}

// StreamingDownload represents an in-progress streaming download.
// Used by concurrent streaming download implementation (v3.2.0).
type StreamingDownload struct {
	// Download identifiers
	RemotePath  string // Path in cloud storage
	LocalPath   string // Local destination path

	// Decryption state
	MasterKey []byte // Master encryption key
	FileID    []byte // File identifier for key derivation
	PartSize  int64  // Size of each plaintext part

	// File info
	EncryptedSize int64 // Total encrypted size
	TotalParts    int64 // Number of parts

	// Provider-specific data
	ProviderData interface{}
}

// PartDownloadResult contains the result of downloading a single part.
// Used by concurrent streaming download implementation (v3.2.0).
type PartDownloadResult struct {
	PartIndex int64  // 0-based part index
	Plaintext []byte // Decrypted data for this part
	Size      int64  // Size of decrypted data
}

// LegacyDownloader extends CloudTransfer with legacy format (v0) download support.
// Providers that support legacy pre-encrypted downloads implement this interface.
// The orchestrator handles disk space checks, resume state, and decryption coordination.
type LegacyDownloader interface {
	cloud.CloudTransfer

	// DownloadEncryptedFile downloads an encrypted file (legacy v0 format) to a local path.
	// This downloads the encrypted file; the orchestrator handles decryption.
	// The params contain all information needed for download including resume state.
	DownloadEncryptedFile(ctx context.Context, params LegacyDownloadParams) error
}

// LegacyDownloadParams contains parameters for downloading a legacy (v0) format file.
type LegacyDownloadParams struct {
	RemotePath       string                 // Cloud storage path
	EncryptedPath    string                 // Local path to save encrypted file
	EncryptionKey    []byte                 // Encryption key for decryption
	IV               []byte                 // IV from cloud metadata
	FileSize         int64                  // Total file size
	FileInfo         *models.CloudFile      // File metadata (for cross-storage credential fetching)
	TransferHandle   *transfer.Transfer     // For concurrency control
	ProgressCallback cloud.ProgressCallback // Progress reporting
	OutputWriter     io.Writer              // Status messages
}
