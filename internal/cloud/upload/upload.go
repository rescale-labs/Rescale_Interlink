// Package upload provides the canonical entry point for file uploads to Rescale cloud storage.
package upload

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/providers"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/transfer"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto"
	"github.com/rescale/rescale-int/internal/models"
	internaltransfer "github.com/rescale/rescale-int/internal/transfer"
)

// UploadParams consolidates all parameters for upload operations.
// This is the single canonical way to specify upload options.
type UploadParams struct {
	// Required: Path to the local file to upload
	LocalPath string

	// Optional: Target folder ID (empty = MyLibrary)
	FolderID string

	// Required: API client for Rescale operations
	APIClient *api.Client

	// Optional: Progress callback (receives values from 0.0 to 1.0)
	ProgressCallback cloud.ProgressCallback

	// Optional: Transfer handle for concurrent part uploads
	// If nil or threads <= 1, uses sequential upload
	TransferHandle *internaltransfer.Transfer

	// Optional: Output writer for status messages
	OutputWriter io.Writer

	// Optional: Encryption mode
	// false (default) = streaming encryption (no temp file, saves disk space)
	// true = pre-encryption (creates temp file, compatible with legacy clients)
	PreEncrypt bool
}

// UploadFile is THE ONLY canonical entry point for uploading files to Rescale cloud storage.
// It handles credential fetching, uploads the file with encryption, and registers it with Rescale.
//
// Default behavior (streaming mode):
//   - Encrypts on-the-fly without creating temp files
//   - Uses concurrent part uploads if TransferHandle has threads > 1
//   - Stores format metadata in cloud object metadata for later detection
//
// Pre-encrypted mode (when PreEncrypt=true):
//   - Creates encrypted temp file first
//   - Uses concurrent part uploads if TransferHandle has threads > 1
//   - Compatible with legacy Rescale clients (e.g., Python client)
//
// Returns the registered CloudFile on success, or an error on failure.
func UploadFile(ctx context.Context, params UploadParams) (*models.CloudFile, error) {
	// v3.6.2: Track overall upload timing
	overallTimer := cloud.StartTimer(params.OutputWriter, "Upload total")

	// Validate required parameters
	if params.LocalPath == "" {
		return nil, fmt.Errorf("local path is required")
	}
	if params.APIClient == nil {
		return nil, fmt.Errorf("API client is required")
	}

	// Validate file exists and is not a directory
	fileInfo, err := os.Stat(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	if fileInfo.IsDir() {
		return nil, fmt.Errorf("cannot upload a directory: %s", params.LocalPath)
	}

	// v3.6.2: Log file info
	cloud.TimingLog(params.OutputWriter, "File: %s (%s)", filepath.Base(params.LocalPath), cloud.FormatBytes(fileInfo.Size()))

	// v3.6.3: Hash calculation is DEFERRED until after upload completes.
	// Previously, hash ran concurrently with upload, causing disk I/O contention
	// that made "Preparing" phase take 60+ seconds for large files.
	// Now hash runs after upload when file is in disk cache -> much faster.

	// v3.6.3: Debug timing for upload initialization
	debugStart := time.Now()
	fileName := filepath.Base(params.LocalPath)

	// v3.6.2: Track initialization phase
	initTimer := cloud.StartTimer(params.OutputWriter, "Upload initialization")

	// Get the global credential manager (caches user profile, credentials, and folders)
	credManager := credentials.GetManager(params.APIClient)

	// Get user profile to determine storage type (cached for 5 minutes)
	t1 := time.Now()
	profile, err := credManager.GetUserProfile(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}
	log.Printf("[DEBUG] %s: GetUserProfile took %v", fileName, time.Since(t1))

	// Get root folders (for currentFolderId in file registration) (cached for 5 minutes)
	t2 := time.Now()
	folders, err := credManager.GetRootFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get root folders: %w", err)
	}
	log.Printf("[DEBUG] %s: GetRootFolders took %v", fileName, time.Since(t2))

	// Create provider using factory
	t3 := time.Now()
	factory := providers.NewFactory()
	provider, err := factory.NewTransferFromStorageInfo(ctx, &profile.DefaultStorage, params.APIClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider: %w", err)
	}
	log.Printf("[DEBUG] %s: CreateProvider took %v", fileName, time.Since(t3))
	log.Printf("[DEBUG] %s: Total init took %v", fileName, time.Since(debugStart))

	initTimer.StopWithMessage("backend=%s", profile.DefaultStorage.StorageType)

	var result *cloud.UploadResult

	// v3.6.2: Track upload transfer phase
	uploadTimer := cloud.StartTimer(params.OutputWriter, "Upload transfer")

	// Upload based on encryption mode
	if params.PreEncrypt {
		// Pre-encrypt mode: use PreEncryptUploader interface
		cloud.TimingLog(params.OutputWriter, "Mode: pre-encrypt (legacy compatible)")
		result, err = uploadPreEncrypt(ctx, provider, params, fileInfo.Size())
	} else {
		// Streaming mode: use StreamingConcurrentUploader interface
		cloud.TimingLog(params.OutputWriter, "Mode: streaming (concurrent)")
		result, err = uploadStreaming(ctx, provider, params, fileInfo.Size())
	}

	if err != nil {
		return nil, fmt.Errorf("%s upload failed: %w", profile.DefaultStorage.StorageType, err)
	}

	uploadTimer.StopWithThroughput(fileInfo.Size())

	// v3.6.3: Calculate hash AFTER upload completes.
	// The file is now in disk cache, so hashing is fast (no disk I/O contention).
	// This fixes the 60+ second "Preparing" delay for large files.
	hashTimer := cloud.StartTimer(params.OutputWriter, "Hash calculation")
	fileHash, err := encryption.CalculateSHA512(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate file hash: %w", err)
	}
	hashTimer.StopWithThroughput(fileInfo.Size())

	// Determine target folder
	targetFolder := folders.MyLibrary
	if params.FolderID != "" {
		targetFolder = params.FolderID
	}

	// Build file registration request
	filename := filepath.Base(params.LocalPath)
	fileReq := &models.CloudFileRequest{
		TypeID:               1, // INPUT_FILE
		Name:                 filename,
		CurrentFolderID:      targetFolder,
		EncodedEncryptionKey: encryption.EncodeBase64(result.EncryptionKey),
		PathParts: models.CloudFilePathParts{
			Container: profile.DefaultStorage.ConnectionSettings.Container,
			Path:      result.StoragePath,
		},
		Storage: models.CloudFileStorage{
			ID:             profile.DefaultStorage.ID,
			StorageType:    profile.DefaultStorage.StorageType,
			EncryptionType: profile.DefaultStorage.EncryptionType,
		},
		IsUploaded:    true,
		DecryptedSize: fileInfo.Size(),
		FileChecksums: []models.FileChecksum{
			{
				HashFunction: "sha512",
				FileHash:     fileHash,
			},
		},
	}

	// v3.6.2: Track file registration
	regTimer := cloud.StartTimer(params.OutputWriter, "File registration")

	// Register file with Rescale
	cloudFile, err := params.APIClient.RegisterFile(ctx, fileReq)
	if err != nil {
		// Provide helpful context based on error type
		fileName := filepath.Base(params.LocalPath)
		if strings.Contains(err.Error(), "TLS handshake timeout") {
			return nil, fmt.Errorf("failed to register file %s (connection pool exhausted - try reducing --max-concurrent): %w",
				fileName, err)
		}
		if strings.Contains(err.Error(), "rate limiter") {
			return nil, fmt.Errorf("failed to register file %s (rate limited - this is temporary): %w",
				fileName, err)
		}
		if strings.Contains(err.Error(), "timeout") {
			return nil, fmt.Errorf("failed to register file %s (API timeout - check network): %w",
				fileName, err)
		}
		return nil, fmt.Errorf("failed to register file %s: %w", fileName, err)
	}

	regTimer.StopWithMessage("file_id=%s", cloudFile.ID)

	// v3.6.2: Log overall upload completion
	overallTimer.StopWithThroughput(fileInfo.Size())

	return cloudFile, nil
}

// progressInterpolator provides smooth progress updates at regular intervals (500ms),
// tracking real-time upload progress. This ensures the UI always shows responsive
// progress even when individual parts take seconds to upload.
// v3.6.3: Now tracks in-flight bytes for real-time progress (not just part completions).
type progressInterpolator struct {
	mu             sync.RWMutex
	callback       cloud.ProgressCallback
	totalBytes     int64
	confirmedBytes int64         // Bytes from completed parts
	inflightBytes  int64         // Bytes currently being uploaded (atomic)
	startTime      time.Time     // When transfer started
	lastConfirmAt  time.Time     // When last part completed
	speed          float64       // Estimated speed (bytes/sec), EMA
	done           chan struct{} // Signal to stop the interpolator
	stopped        bool          // Prevent double-close
}

// newProgressInterpolator creates a progress interpolator that calls the callback
// at least every 500ms with estimated progress.
func newProgressInterpolator(callback cloud.ProgressCallback, totalBytes int64) *progressInterpolator {
	return &progressInterpolator{
		callback:      callback,
		totalBytes:    totalBytes,
		startTime:     time.Now(),
		lastConfirmAt: time.Now(),
		done:          make(chan struct{}),
	}
}

// Start begins the interpolation goroutine. Call Stop() when done.
func (pi *progressInterpolator) Start() {
	go func() {
		ticker := time.NewTicker(constants.ProgressUpdateInterval) // v4.0.4: use centralized constant
		defer ticker.Stop()

		for {
			select {
			case <-pi.done:
				return
			case <-ticker.C:
				pi.emitInterpolated()
			}
		}
	}()
}

// emitInterpolated calculates and emits progress using real-time byte tracking.
// This is called by the ticker every 500ms.
// v3.6.3: Now uses inflightBytes for immediate progress feedback.
func (pi *progressInterpolator) emitInterpolated() {
	pi.mu.RLock()
	defer pi.mu.RUnlock()

	if pi.callback == nil {
		return
	}

	// v3.6.3: Use real-time tracking: confirmed bytes + in-flight bytes
	// This gives immediate progress feedback during uploads, not just at part completions.
	currentBytes := pi.confirmedBytes + pi.inflightBytes

	var progress float64
	if pi.totalBytes == 0 {
		progress = 1.0 // Empty file: immediately complete
	} else {
		progress = float64(currentBytes) / float64(pi.totalBytes)
	}
	if progress > 1.0 {
		progress = 1.0
	}
	if progress < 0.0 {
		progress = 0.0
	}

	pi.callback(progress)
}

// AddInflight adds bytes to the in-flight counter.
// This is called as bytes are being uploaded in real-time.
// v3.6.3: Enables immediate progress feedback during part uploads.
func (pi *progressInterpolator) AddInflight(bytes int64) {
	pi.mu.Lock()
	pi.inflightBytes += bytes
	pi.mu.Unlock()
}

// ConfirmBytes records completed bytes and clears corresponding in-flight bytes.
// This is called when a part upload completes.
// v3.6.3: Now also clears inflightBytes for the completed part.
func (pi *progressInterpolator) ConfirmBytes(partSize int64) {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	elapsed := time.Since(pi.lastConfirmAt)

	// v3.6.3: Move bytes from inflight to confirmed
	// Clear inflightBytes for this part (it was tracked during upload)
	if pi.inflightBytes >= partSize {
		pi.inflightBytes -= partSize
	} else {
		pi.inflightBytes = 0 // Safety: don't go negative
	}
	pi.confirmedBytes += partSize
	pi.lastConfirmAt = time.Now()

	// Update speed estimate using exponential moving average (alpha = 0.3)
	// This gives more weight to recent measurements for responsiveness
	if elapsed.Seconds() > 0.01 { // Avoid division by near-zero
		instantSpeed := float64(partSize) / elapsed.Seconds()
		if pi.speed == 0 {
			pi.speed = instantSpeed
		} else {
			// EMA: new = alpha * current + (1-alpha) * old
			pi.speed = 0.3*instantSpeed + 0.7*pi.speed
		}
	}

	// v3.6.3: Removed immediate callback from ConfirmBytes.
	// The ticker (emitInterpolated) handles all progress emission.
	// This prevents progress jumping backwards because:
	// - emitInterpolated uses (confirmedBytes + inflightBytes)
	// - ConfirmBytes was using just confirmedBytes (lower value!)
	// Let the ticker provide consistent progress values every 500ms.
}

// Stop stops the interpolation goroutine.
func (pi *progressInterpolator) Stop() {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	if !pi.stopped {
		pi.stopped = true
		close(pi.done)
	}
}

// encryptedPart holds an encrypted part ready for upload (used for pipelining).
// v3.4.0: Enables encryption to run ahead of uploads.
type encryptedPart struct {
	partIndex  int64
	ciphertext []byte
	plainSize  int64 // Original plaintext size for accurate tracking
	err        error
}

// uploadResult holds the result of an upload worker (used for parallel uploads).
// v3.4.1: Enables parallel upload of encrypted parts.
type uploadResult struct {
	partIndex int64
	result    *transfer.PartResult
	plainSize int64
	err       error
}

// uploadStreaming uses the StreamingConcurrentUploader interface for streaming uploads.
// v3.4.1: Parallel upload architecture - encryption is sequential (CBC constraint),
// but uploads happen in parallel for 2-4x throughput improvement.
func uploadStreaming(ctx context.Context, provider cloud.CloudTransfer, params UploadParams, fileSize int64) (*cloud.UploadResult, error) {
	fileName := filepath.Base(params.LocalPath)
	streamStart := time.Now()

	// Cast to StreamingConcurrentUploader
	streamingUploader, ok := provider.(transfer.StreamingConcurrentUploader)
	if !ok {
		return nil, fmt.Errorf("provider does not support streaming upload")
	}

	// v3.6.2: Track streaming upload init
	streamInitTimer := cloud.StartTimer(params.OutputWriter, "Streaming upload init")

	// Initialize streaming upload
	initParams := transfer.StreamingUploadInitParams{
		LocalPath:    params.LocalPath,
		FileSize:     fileSize,
		OutputWriter: params.OutputWriter,
	}

	log.Printf("[DEBUG] %s: Starting InitStreamingUpload", fileName)
	t1 := time.Now()
	uploadState, err := streamingUploader.InitStreamingUpload(ctx, initParams)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize streaming upload: %w", err)
	}
	log.Printf("[DEBUG] %s: InitStreamingUpload took %v", fileName, time.Since(t1))

	streamInitTimer.StopWithMessage("parts=%d part_size=%s", uploadState.TotalParts, cloud.FormatBytes(int64(uploadState.PartSize)))
	log.Printf("[DEBUG] %s: Streaming init complete, starting transfer at %v since start", fileName, time.Since(streamStart))

	// v3.6.3: Create progress interpolator for smooth updates every 500ms.
	// This provides responsive progress feedback even when individual parts
	// take seconds to upload (e.g., on slow networks or large part sizes).
	var progressInterp *progressInterpolator
	if params.ProgressCallback != nil {
		progressInterp = newProgressInterpolator(params.ProgressCallback, fileSize)
		progressInterp.Start()
		defer progressInterp.Stop()

		// v3.6.3: Wire up real-time byte tracking from S3 uploads.
		// The progressReader in S3 provider calls this as bytes are sent.
		// This enables progress updates DURING part uploads, not just at completion.
		uploadState.ByteProgressCallback = func(bytesUploaded int64) {
			progressInterp.AddInflight(bytesUploaded)
		}

		// Report 0% progress immediately after init completes.
		// This changes GUI status from "Preparing" to "0.00%" so users know
		// the transfer has started, even before the first part completes.
		params.ProgressCallback(0.0)
	}

	// Open file
	file, err := os.Open(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// v3.4.1: Get upload concurrency from TransferHandle (default to 4)
	// Encryption is still sequential (CBC constraint), but uploads happen in parallel
	concurrency := 4
	if params.TransferHandle != nil && params.TransferHandle.GetThreads() > 1 {
		concurrency = params.TransferHandle.GetThreads()
	}

	// v3.6.2: Log concurrency
	cloud.TimingLog(params.OutputWriter, "Upload workers: %d threads", concurrency)

	// v3.4.1: Larger buffer to feed parallel uploaders (concurrency * 3 parts)
	// This allows encryption to run ahead and keep all upload workers busy
	encryptedChan := make(chan encryptedPart, concurrency*3)

	// Result channel for collecting upload results
	resultChan := make(chan uploadResult, concurrency*2)

	// Context with cancellation for error propagation
	// Note: cancelUpload is called explicitly in cleanup defer after scaler goroutine is started
	uploadCtx, cancelUpload := context.WithCancel(ctx)

	// Track first error for clean shutdown
	var firstErr error
	var errOnce sync.Once

	// Encryption goroutine: reads file, encrypts parts, sends to channel
	// Must be sequential due to CBC chaining constraint
	go func() {
		defer close(encryptedChan)
		buffer := make([]byte, uploadState.PartSize)
		var partIndex int64 = 0
		encryptFirstLogged := false

		for {
			// Check for context cancellation
			select {
			case <-uploadCtx.Done():
				return
			default:
			}

			n, readErr := file.Read(buffer)

			// Handle empty file: first read returns (0, io.EOF)
			// Emit one encrypted empty part so the pipeline completes correctly
			if n == 0 && readErr == io.EOF && partIndex == 0 {
				ciphertext, encErr := streamingUploader.EncryptStreamingPart(uploadCtx, uploadState, 0, []byte{})
				if encErr != nil {
					errOnce.Do(func() { firstErr = encErr })
					cancelUpload()
					return
				}
				select {
				case encryptedChan <- encryptedPart{
					partIndex:  0,
					ciphertext: ciphertext,
					plainSize:  0,
				}:
				case <-uploadCtx.Done():
				}
				return
			}

			if n > 0 {
				// Make copy of plaintext (buffer will be reused)
				plaintext := make([]byte, n)
				copy(plaintext, buffer[:n])

				// Encrypt this part (sequential, CBC constraint)
				ciphertext, encErr := streamingUploader.EncryptStreamingPart(uploadCtx, uploadState, partIndex, plaintext)
				if encErr != nil {
					errOnce.Do(func() { firstErr = encErr })
					cancelUpload()
					return
				}

				if !encryptFirstLogged {
					log.Printf("[DEBUG] %s: First encrypted part ready at %v since stream start (part 0, %d bytes)",
						fileName, time.Since(streamStart), len(ciphertext))
					encryptFirstLogged = true
				}

				// Send encrypted part to upload workers
				select {
				case encryptedChan <- encryptedPart{
					partIndex:  partIndex,
					ciphertext: ciphertext,
					plainSize:  int64(n),
				}:
					partIndex++
				case <-uploadCtx.Done():
					return
				}
			}

			if readErr == io.EOF {
				return
			}
			if readErr != nil {
				errOnce.Do(func() { firstErr = fmt.Errorf("failed to read file: %w", readErr) })
				cancelUpload()
				return
			}
		}
	}()

	// v3.4.2: Track worker count for dynamic scaling
	var workerCount int32 = int32(concurrency)
	var firstUploadStartLogged int32 = 0
	var firstUploadDoneLogged int32 = 0

	// Upload worker function - shared by initial workers and dynamically spawned workers
	uploadWorker := func(workerID int) {
		for enc := range encryptedChan {
			// Check for cancellation
			select {
			case <-uploadCtx.Done():
				return
			default:
			}

			// Log first upload start
			if atomic.CompareAndSwapInt32(&firstUploadStartLogged, 0, 1) {
				log.Printf("[DEBUG] %s: First upload STARTING at %v since stream start (part %d)",
					fileName, time.Since(streamStart), enc.partIndex)
			}

			// Upload this encrypted part
			partResult, uploadErr := streamingUploader.UploadCiphertext(uploadCtx, uploadState, enc.partIndex, enc.ciphertext)

			if uploadErr != nil {
				errOnce.Do(func() { firstErr = uploadErr })
				cancelUpload()
				resultChan <- uploadResult{partIndex: enc.partIndex, err: uploadErr}
				return
			}

			// Log first upload complete
			if atomic.CompareAndSwapInt32(&firstUploadDoneLogged, 0, 1) {
				log.Printf("[DEBUG] %s: First upload COMPLETE at %v since stream start (part %d)",
					fileName, time.Since(streamStart), enc.partIndex)
			}

			// Send success result
			resultChan <- uploadResult{
				partIndex: enc.partIndex,
				result:    partResult,
				plainSize: enc.plainSize,
			}
		}
	}

	// v3.4.1: Start initial upload worker pool - uploads encrypted parts in parallel
	var uploadWg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		uploadWg.Add(1)
		go func(workerID int) {
			defer uploadWg.Done()
			uploadWorker(workerID)
		}(i)
	}

	// v3.4.2: Background scaler - dynamically spawns additional workers when threads become available
	// This handles the case where other concurrent transfers finish and release their threads
	scalerDone := make(chan struct{})
	go func() {
		defer close(scalerDone)

		if params.TransferHandle == nil {
			return // No transfer handle, can't scale
		}

		ticker := time.NewTicker(100 * time.Millisecond) // Check every 100ms for faster responsiveness
		defer ticker.Stop()

		for {
			select {
			case <-uploadCtx.Done():
				return
			case <-ticker.C:
				// Try to acquire up to 4 more threads at a time
				acquired := params.TransferHandle.TryAcquireMore(4)
				if acquired > 0 {
					// Spawn additional workers
					for i := 0; i < acquired; i++ {
						newWorkerID := int(atomic.AddInt32(&workerCount, 1))
						uploadWg.Add(1)
						go func(wid int) {
							defer uploadWg.Done()
							uploadWorker(wid)
						}(newWorkerID)
					}
				}
			}
		}
	}()

	// v4.0.0: Ensure scaler goroutine is properly cleaned up on function exit.
	// Cancel the context first (signals scaler to stop), then wait for it to finish.
	// This prevents goroutine leaks.
	defer func() {
		cancelUpload()  // Cancel context to signal scaler and other goroutines to stop
		<-scalerDone    // Wait for scaler goroutine to finish
	}()

	// Close result channel when all workers finish
	go func() {
		uploadWg.Wait()
		close(resultChan)
	}()

	// Collect results - parts may arrive out of order due to parallel uploads
	partsMap := make(map[int64]*transfer.PartResult)
	completedCount := 0

	firstProgressLogged := false
	for res := range resultChan {
		if res.err != nil {
			// Error already recorded in firstErr, just continue draining
			continue
		}

		// Update size to plaintext size for accurate tracking
		res.result.Size = res.plainSize
		partsMap[res.partIndex] = res.result
		completedCount++

		// v3.6.3: Use progress interpolator for confirmed bytes.
		// This updates speed estimate and emits immediate progress callback.
		// The interpolator also provides updates every 500ms between confirmations.
		if progressInterp != nil {
			if !firstProgressLogged {
				log.Printf("[DEBUG] %s: FIRST part complete at %v since stream start (part %d/%d)",
					fileName, time.Since(streamStart), completedCount, uploadState.TotalParts)
				firstProgressLogged = true
			}
			progressInterp.ConfirmBytes(res.plainSize)
		}
	}

	// Check for errors
	if firstErr != nil {
		_ = streamingUploader.AbortStreamingUpload(ctx, uploadState)
		return nil, firstErr
	}

	// v4.0.0: Also check for context cancellation which may have occurred without setting firstErr.
	// This can happen if the user cancels the upload or a timeout occurs.
	select {
	case <-ctx.Done():
		_ = streamingUploader.AbortStreamingUpload(ctx, uploadState)
		return nil, fmt.Errorf("upload cancelled: %w", ctx.Err())
	default:
	}

	// v4.0.0 CRITICAL: Verify we received ALL expected parts before completing.
	// This prevents truncated uploads from being registered as complete files.
	// If any parts are missing (due to context cancellation, network issues, etc.),
	// we must abort to avoid creating corrupted files in cloud storage.
	expectedParts := int(uploadState.TotalParts)
	if len(partsMap) != expectedParts {
		_ = streamingUploader.AbortStreamingUpload(ctx, uploadState)
		return nil, fmt.Errorf("upload incomplete: received %d of %d parts (upload was interrupted or cancelled)",
			len(partsMap), expectedParts)
	}

	// Convert map to ordered slice for completion
	parts := make([]*transfer.PartResult, len(partsMap))
	for idx, part := range partsMap {
		parts[idx] = part
	}

	// v3.6.2: Track completion phase
	completeTimer := cloud.StartTimer(params.OutputWriter, "Streaming upload complete")

	// Complete upload
	result, err := streamingUploader.CompleteStreamingUpload(ctx, uploadState, parts)
	if err != nil {
		return nil, fmt.Errorf("failed to complete streaming upload: %w", err)
	}

	completeTimer.StopWithMessage("parts=%d", len(parts))

	return result, nil
}

// uploadPreEncrypt uses the PreEncryptUploader interface for pre-encrypted uploads.
func uploadPreEncrypt(ctx context.Context, provider cloud.CloudTransfer, params UploadParams, fileSize int64) (*cloud.UploadResult, error) {
	// Cast to PreEncryptUploader
	preEncryptUploader, ok := provider.(transfer.PreEncryptUploader)
	if !ok {
		return nil, fmt.Errorf("provider does not support pre-encrypt upload")
	}

	// Generate encryption key and IV
	encryptionKey, iv, randomSuffix, err := GenerateEncryptionParams()
	if err != nil {
		return nil, fmt.Errorf("failed to generate encryption params: %w", err)
	}

	// Create encrypted temp file
	encryptedPath, err := CreateEncryptedTempFile(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(encryptedPath)

	// v3.6.2: Track encryption phase
	encryptTimer := cloud.StartTimer(params.OutputWriter, "Pre-encryption")

	// Encrypt file
	if params.OutputWriter != nil {
		fmt.Fprintf(params.OutputWriter, "Encrypting file (%s)...\n", filepath.Base(params.LocalPath))
	}
	if err := encryption.EncryptFile(params.LocalPath, encryptedPath, encryptionKey, iv); err != nil {
		return nil, fmt.Errorf("failed to encrypt file: %w", err)
	}

	encryptTimer.StopWithThroughput(fileSize)

	// Build upload params (providers stat the encrypted file themselves)
	uploadParams := transfer.EncryptedFileUploadParams{
		LocalPath:        params.LocalPath,
		EncryptedPath:    encryptedPath,
		EncryptionKey:    encryptionKey,
		IV:               iv,
		RandomSuffix:     randomSuffix,
		OriginalSize:     fileSize,
		ProgressCallback: params.ProgressCallback,
		TransferHandle:   params.TransferHandle,
		OutputWriter:     params.OutputWriter,
	}

	// v3.6.2: Track upload phase
	uploadTimer := cloud.StartTimer(params.OutputWriter, "Pre-encrypt upload")

	// Upload encrypted file
	result, err := preEncryptUploader.UploadEncryptedFile(ctx, uploadParams)
	if err != nil {
		return nil, fmt.Errorf("failed to upload encrypted file: %w", err)
	}

	uploadTimer.StopWithThroughput(fileSize)

	// Clean up resume state
	state.DeleteUploadState(params.LocalPath)

	return result, nil
}

