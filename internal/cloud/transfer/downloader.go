// Package transfer provides unified upload and download orchestration.
// This file implements the unified download orchestrator that works with any CloudTransfer provider.
//
// Version: 3.2.4 (CBC Streaming Download)
// Date: 2025-12-10
//
// Format versions supported:
//   - v0 (legacy): Download all → decrypt all, requires .encrypted temp file
//   - v1 (HKDF): Per-part key derivation, parallel decryption, no temp file
//   - v2 (CBC streaming): Sequential CBC decryption, no temp file (v3.2.4+)
package transfer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/storage"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/models"
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
}

// Download downloads and decrypts a file using the configured provider.
// The download mode (legacy vs streaming) is automatically detected from cloud metadata.
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
// Sprint F.2: Supports cross-storage downloads via FileInfoSetter interface.
func (d *Downloader) Download(ctx context.Context, params cloud.DownloadParams) error {
	// Validate required parameters
	if params.RemotePath == "" {
		return fmt.Errorf("remote path is required")
	}
	if params.LocalPath == "" {
		return fmt.Errorf("local path is required")
	}
	if params.FileInfo == nil {
		return fmt.Errorf("file info is required")
	}
	if params.FileInfo.EncodedEncryptionKey == "" {
		return fmt.Errorf("encryption key is required in file info")
	}

	// Sprint F.2: Set file info for cross-storage credential fetching
	// If the provider supports FileInfoSetter, call SetFileInfo before any operations.
	// This enables downloading files from storage different than user's default
	// (e.g., S3 user downloading Azure-stored job outputs).
	if fileInfoSetter, ok := d.provider.(cloud.FileInfoSetter); ok {
		fileInfoSetter.SetFileInfo(params.FileInfo)
	}

	// Decode encryption key from file info
	encryptionKey, err := encryption.DecodeBase64(params.FileInfo.EncodedEncryptionKey)
	if err != nil {
		return fmt.Errorf("failed to decode encryption key: %w", err)
	}

	// Decode IV if present (for legacy format)
	var iv []byte
	if params.FileInfo.IV != "" {
		iv, err = encryption.DecodeBase64(params.FileInfo.IV)
		if err != nil {
			return fmt.Errorf("failed to decode IV: %w", err)
		}
	}

	// Log download start if output writer is provided
	if params.OutputWriter != nil {
		filename := filepath.Base(params.LocalPath)
		threads := 1
		if params.TransferHandle != nil {
			threads = params.TransferHandle.GetThreads()
		}
		if threads > 1 {
			fmt.Fprintf(params.OutputWriter, "Starting download of %s with %d concurrent threads\n",
				filename, threads)
		}
	}

	// Check if provider supports format detection
	formatDetector, hasFormatDetection := d.provider.(StreamingConcurrentDownloader)
	if hasFormatDetection {
		// Detect format from cloud metadata
		// Phase 7H: DetectFormat now returns IV for legacy format (v0)
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
		} else if params.OutputWriter != nil {
			// v3.4.0: Show successful format detection result
			fmt.Fprintf(params.OutputWriter, "Format detection successful: version=%d\n", formatVersion)
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
			return d.downloadCBCStreaming(ctx, prep)
		case 1:
			return d.downloadStreaming(ctx, prep, formatDetector)
		default:
			return d.downloadLegacy(ctx, prep)
		}
	}

	// Provider doesn't support format detection - fall back to legacy behavior
	// Use IV presence to determine format
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
	return d.provider.Download(ctx, params)
}

// downloadLegacy downloads using legacy (v0) format.
// The entire file is downloaded encrypted, then decrypted.
// Phase 7C: Orchestrator handles disk space, temp file, and decryption.
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

	// Ensure cleanup of temp file on success or error
	defer func() {
		if removeErr := os.Remove(encryptedPath); removeErr != nil && !os.IsNotExist(removeErr) {
			if prep.Params.OutputWriter != nil {
				fmt.Fprintf(prep.Params.OutputWriter, "Warning: Failed to cleanup encrypted temp file %s: %v\n", encryptedPath, removeErr)
			}
		}
	}()

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

	if err := encryption.DecryptFile(encryptedPath, localPath, prep.EncryptionKey, prep.IV); err != nil {
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

	// Complete transfer handle if provided
	if prep.TransferHandle != nil {
		prep.TransferHandle.Complete()
	}

	return nil
}

// downloadCBCStreaming downloads using CBC chaining format (v2) with streaming decryption.
// v3.2.4: Files uploaded by rescale-int with `streamingformat: cbc` metadata can use this path
// to avoid creating a temp file. Parts are decrypted sequentially using CBCStreamingDecryptor.
//
// Key difference from legacy: Instead of download-all → decrypt-all, we download and decrypt
// each part sequentially, writing directly to the final file.
//
// The CBC decryption MUST be sequential because each part's IV is the last ciphertext block
// of the previous part. HKDF (v1) can decrypt parts in parallel because each part has its own
// derived key/IV, but CBC cannot.
func (d *Downloader) downloadCBCStreaming(ctx context.Context, prep *DownloadPrep) error {
	if prep.Params.OutputWriter != nil {
		fmt.Fprintf(prep.Params.OutputWriter, "Using CBC streaming format (v2) download - no temp file\n")
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
	defer outFile.Close()

	// Create CBC streaming decryptor
	decryptor, err := encryption.NewCBCStreamingDecryptor(prep.EncryptionKey, prep.IV)
	if err != nil {
		return fmt.Errorf("failed to create CBC decryptor: %w", err)
	}

	// Use standard chunk size for part boundaries
	// CBC streaming was uploaded with 16MB parts, each part is a multiple of 16 bytes
	partSize := int64(constants.ChunkSize) // 16MB

	// Calculate number of parts
	numParts := (encryptedSize + partSize - 1) / partSize

	// Track progress
	var downloadedBytes int64

	// Download and decrypt each part sequentially
	for partIndex := int64(0); partIndex < numParts; partIndex++ {
		// Calculate byte range for this part
		startByte := partIndex * partSize
		endByte := startByte + partSize
		if endByte > encryptedSize {
			endByte = encryptedSize
		}
		partLength := endByte - startByte

		// Track actual timing for accurate throughput recording
		partStartTime := time.Now()

		// Download this part's ciphertext
		ciphertext, err := partDownloader.DownloadEncryptedRange(ctx, prep.Params.RemotePath, startByte, partLength)
		if err != nil {
			return fmt.Errorf("failed to download part %d: %w", partIndex, err)
		}

		// Determine if this is the final part
		isFinal := (partIndex == numParts-1)

		// Decrypt this part (updates internal IV state for next part)
		plaintext, err := decryptor.DecryptPart(ciphertext, isFinal)
		if err != nil {
			return fmt.Errorf("failed to decrypt part %d: %w", partIndex, err)
		}

		// Write plaintext directly to output file
		if _, err := outFile.Write(plaintext); err != nil {
			return fmt.Errorf("failed to write part %d: %w", partIndex, err)
		}

		// Update progress
		downloadedBytes += int64(len(ciphertext))
		if prep.Params.ProgressCallback != nil && encryptedSize > 0 {
			prep.Params.ProgressCallback(float64(downloadedBytes) / float64(encryptedSize))
		}

		// Record actual throughput if transfer handle available
		if prep.TransferHandle != nil {
			elapsed := time.Since(partStartTime)
			if elapsed > 0 {
				// Calculate actual bytes per second from real timing
				bytesPerSec := float64(len(ciphertext)) / elapsed.Seconds()
				prep.TransferHandle.RecordThroughput(bytesPerSec)
			}
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
	defer outFile.Close()

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

	// Progress tracking
	var downloadedBytes int64
	progressTicker := time.NewTicker(300 * time.Millisecond)
	defer progressTicker.Stop()

	progressDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-progressTicker.C:
				if prep.Params.ProgressCallback != nil && encryptedSize > 0 {
					currentBytes := atomic.LoadInt64(&downloadedBytes)
					prep.Params.ProgressCallback(float64(currentBytes) / float64(encryptedSize))
				}
			case <-progressDone:
				return
			}
		}
	}()
	defer close(progressDone)

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

				// Track actual timing for accurate throughput recording
				partStartTime := time.Now()

				// Download encrypted part bytes
				ciphertext, err := partDownloader.DownloadEncryptedRange(
					opCtx,
					prep.Params.RemotePath,
					job.encryptedStart,
					job.encryptedEnd-job.encryptedStart,
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

				// Update progress
				atomic.AddInt64(&downloadedBytes, int64(len(ciphertext)))

				// Record actual throughput if transfer handle available
				if prep.TransferHandle != nil {
					elapsed := time.Since(partStartTime)
					if elapsed > 0 {
						// Calculate actual bytes per second from real timing
						bytesPerSec := float64(len(ciphertext)) / elapsed.Seconds()
						prep.TransferHandle.RecordThroughput(bytesPerSec)
					}
				}

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
	// Phase 7H: Added iv return value for legacy format support.
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
	DownloadEncryptedRange(ctx context.Context, remotePath string, offset, length int64) ([]byte, error)
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
