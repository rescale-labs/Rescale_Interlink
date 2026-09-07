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
	"github.com/rescale/rescale-int/internal/resources"
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

	// Optional: Called when a storage operation is retried, so the caller can
	// surface it (progress bar label, log line). Runs on a transfer goroutine.
	// When nil, the provider reports retries on its own (OutputWriter or stderr).
	OnRetry func(cloud.RetryEvent)

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

	cloud.TimingLog(params.OutputWriter, "File: %s (%s)", filepath.Base(params.LocalPath), cloud.FormatBytes(fileInfo.Size()))

	// Hash calculation is deferred until after upload completes — see hashTimer below.

	initTimer := cloud.StartTimer(params.OutputWriter, "Upload initialization")

	// Get the global credential manager (caches user profile, credentials, and folders)
	credManager := credentials.GetManager(params.APIClient)

	// Get user profile to determine storage type (cached for 5 minutes)
	profile, err := credManager.GetUserProfile(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	// Skip GetRootFolders() when caller provides FolderID (batch uploads always do).
	// GetRootFolders is only needed to resolve MyLibrary as default target.
	var targetFolder string
	if params.FolderID != "" {
		targetFolder = params.FolderID
	} else {
		folders, err := credManager.GetRootFolders(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get root folders: %w", err)
		}
		targetFolder = folders.MyLibrary
	}

	// Create provider using factory
	factory := providers.NewFactory()
	provider, err := factory.NewTransferFromStorageInfo(ctx, &profile.DefaultStorage, params.APIClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider: %w", err)
	}

	// Retries happen several layers down in the provider client; hand it the
	// caller's hooks so a stalled transfer is visible instead of silent.
	if setter, ok := provider.(cloud.RetryObserverSetter); ok {
		setter.SetRetryObserver(cloud.RetryObserver{
			Writer:  params.OutputWriter,
			OnRetry: params.OnRetry,
		})
	}

	initTimer.StopWithMessage("backend=%s", profile.DefaultStorage.StorageType)

	var result *cloud.UploadResult

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

	// Hash AFTER upload completes: the file is now in disk cache, so hashing is fast
	// (avoids disk I/O contention that caused long "Preparing" delays for large files).
	hashTimer := cloud.StartTimer(params.OutputWriter, "Hash calculation")
	fileHash, err := encryption.CalculateSHA512(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate file hash: %w", err)
	}
	hashTimer.StopWithThroughput(fileInfo.Size())
	if err := checkSourceUnchanged(params.LocalPath, fileInfo); err != nil {
		return nil, err
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

	overallTimer.StopWithThroughput(fileInfo.Size())

	return cloudFile, nil
}

// checkSourceUnchanged reports that the file moved under the upload.
//
// The registration carries the size from the stat taken before the transfer and
// a SHA-512 computed by re-reading the file after it. If the file changed in
// between, those two describe neither the uploaded bytes nor each other, and
// every later download of it fails verification with nothing to point at. So a
// source that moved fails the upload instead: a failed upload can be retried,
// while a bad registration is discovered much later by whoever downloads it.
//
// Size and modification time are what a stat can tell us. A rewrite that
// restores both would still slip through — catching that needs the hash to be
// computed from the bytes as they are uploaded, which this path does not do.
func checkSourceUnchanged(path string, before os.FileInfo) error {
	after, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to re-check %s after the upload: %w", filepath.Base(path), err)
	}

	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return fmt.Errorf("%s changed during the upload (size %d→%d, modified %s→%s); "+
			"not registering it, because the recorded size and checksum would not match the uploaded bytes",
			filepath.Base(path), before.Size(), after.Size(),
			before.ModTime().Format(time.RFC3339Nano), after.ModTime().Format(time.RFC3339Nano))
	}

	return nil
}

// progressInterpolator provides smooth progress updates at regular intervals (500ms),
// tracking real-time upload progress. This ensures the UI always shows responsive
// progress even when individual parts take seconds to upload.
type progressInterpolator struct {
	mu             sync.RWMutex
	callback       cloud.ProgressCallback
	totalBytes     int64
	confirmedBytes int64         // Bytes from completed parts
	inflightBytes  int64         // Bytes currently being uploaded (atomic)
	done           chan struct{} // Signal to stop the interpolator
	stopped        bool          // Prevent double-close
}

// newProgressInterpolator creates a progress interpolator that calls the callback
// at least every 500ms with estimated progress.
func newProgressInterpolator(callback cloud.ProgressCallback, totalBytes int64) *progressInterpolator {
	return &progressInterpolator{
		callback:   callback,
		totalBytes: totalBytes,
		done:       make(chan struct{}),
	}
}

// Start begins the interpolation goroutine. Call Stop() when done.
func (pi *progressInterpolator) Start() {
	go func() {
		ticker := time.NewTicker(constants.ProgressUpdateInterval)
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
func (pi *progressInterpolator) emitInterpolated() {
	pi.mu.RLock()
	defer pi.mu.RUnlock()

	if pi.callback == nil {
		return
	}

	// Use confirmed bytes + in-flight bytes for real-time progress (not just part completions)
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
func (pi *progressInterpolator) AddInflight(bytes int64) {
	pi.mu.Lock()
	pi.inflightBytes += bytes
	pi.mu.Unlock()
}

// ConfirmBytes records completed bytes and clears corresponding in-flight bytes.
// This is called when a part upload completes.
func (pi *progressInterpolator) ConfirmBytes(partSize int64) {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	if pi.inflightBytes >= partSize {
		pi.inflightBytes -= partSize
	} else {
		pi.inflightBytes = 0 // Safety: don't go negative
	}
	pi.confirmedBytes += partSize

	// No immediate callback here — the ticker (emitInterpolated) handles all progress emission.
	// Emitting from here would cause progress to jump backwards because the ticker uses
	// (confirmedBytes + inflightBytes) while this method only sees confirmedBytes.
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
type encryptedPart struct {
	partIndex  int64
	ciphertext []byte
	plainSize  int64 // Original plaintext size for accurate tracking
	err        error
}

// uploadResult holds the result of an upload worker (used for parallel uploads).
type uploadResult struct {
	partIndex int64
	result    *transfer.PartResult
	plainSize int64
	err       error
}

// planStreamingUpload sizes the pipeline and returns the release for whatever it
// reserved. With a transfer handle the memory comes out of the shared pool, so
// several large uploads at once cannot each budget against the whole machine;
// without one there is no pool to share and the plan is advisory.
func planStreamingUpload(handle *internaltransfer.Transfer, fileSize int64, threads int, limits resources.UploadLimits) (resources.UploadPlan, func(), error) {
	req := resources.UploadPlanRequest{
		FileSize: fileSize,
		Threads:  threads,
		Limits:   limits,
	}
	if handle == nil {
		plan, err := resources.PlanUpload(req)
		return plan, func() {}, err
	}
	plan, err := handle.PlanUpload(req)
	if err != nil {
		return resources.UploadPlan{}, func() {}, err
	}
	return plan, handle.ReleaseUploadPlan, nil
}

// openUploadSource opens the plaintext byte source for a streaming upload.
// It is a package-level function variable — the test-seam pattern used by the
// CLI download helpers — because a local *os.File never returns a short read,
// and short reads are precisely what the encrypt loop below has to survive.
// Overriding this lets a test deliver a real file's bytes in partial chunks.
var openUploadSource = func(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// uploadStreaming uses the StreamingConcurrentUploader interface for streaming uploads.
// Encryption is sequential (CBC constraint), but uploads happen in parallel.
func uploadStreaming(ctx context.Context, provider cloud.CloudTransfer, params UploadParams, fileSize int64) (*cloud.UploadResult, error) {
	// Cast to StreamingConcurrentUploader
	streamingUploader, ok := provider.(transfer.StreamingConcurrentUploader)
	if !ok {
		return nil, fmt.Errorf("provider does not support streaming upload")
	}

	sourceInfo, err := os.Stat(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// One invocation at a time owns this source's resume lifecycle. The state
	// beside it names a multipart upload on the backend, so a second invocation
	// that read it would fill the SAME object as this one and abort it on its
	// way out. The lock is taken before anything is loaded, restored or
	// abandoned, and held until the upload has been completed or given up on.
	uploadLock, err := state.AcquireUploadLock(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire upload lock: %w", err)
	}
	defer state.ReleaseUploadLock(uploadLock)

	streamInitTimer := cloud.StartTimer(params.OutputWriter, "Streaming upload init")

	concurrency := 4
	if params.TransferHandle != nil && params.TransferHandle.GetThreads() > 1 {
		concurrency = params.TransferHandle.GetThreads()
	}

	// Read what an interrupted attempt left behind before planning: a resumed
	// upload is stuck with the part size the first attempt chained through and
	// stamped into the object's metadata, so what this run would have planned
	// for is advisory once there is a state to continue.
	resumed := loadStreamingResume(ctx, streamingUploader, params, sourceInfo, fileSize)

	// Plan before anything is opened on the backend. A file too large for the
	// storage type, or a machine that cannot hold one working set, has to fail
	// here — the part-count limits are only hit after every earlier part has
	// already been transferred.
	plan, releasePlan, err := planStreamingUpload(params.TransferHandle, fileSize, concurrency, streamingUploader.UploadLimits())
	if err != nil {
		return nil, err
	}
	defer releasePlan()

	// Resuming means running with the interrupted upload's part size, whatever
	// this run would have chosen, so the pipeline is refitted to it before the
	// upload is reopened. A part size this run cannot afford to hold is the one
	// case where a resume costs more than it saves: a fresh object is always
	// correct, and only the bytes already sent are lost.
	fitted := plan
	if resumed.usable {
		var affordable bool
		fitted, affordable = fitPipelineToPartSize(plan, resumed.saved.PartSize)
		if !affordable {
			if params.OutputWriter != nil {
				fmt.Fprintf(params.OutputWriter, "Starting a fresh upload of %s: there is not enough transfer memory to continue it in %d MB parts\n",
					filepath.Base(params.LocalPath), resumed.saved.PartSize/constants.PartSizeAlignment)
			}
			abandonStreamingState(ctx, streamingUploader, params, resumed.saved)
			resumed = streamingResume{}
		}
	}

	var uploadState *transfer.StreamingUpload
	if resumed.usable {
		uploadState, err = reopenStreamingUpload(ctx, streamingUploader, params, resumed)
		if err != nil {
			return nil, err
		}
	}
	if uploadState != nil {
		plan = fitted
	}
	if concurrency > plan.WorkerCap {
		concurrency = plan.WorkerCap
	}

	if uploadState == nil {
		// Either there was nothing to resume, or what there was is gone from the
		// backend. Whatever this run uploads belongs to a new object, so no part
		// of the old attempt counts towards it.
		resumed = streamingResume{}

		initParams := transfer.StreamingUploadInitParams{
			LocalPath:    params.LocalPath,
			FileSize:     fileSize,
			OutputWriter: params.OutputWriter,
			Plan:         &plan,
		}

		uploadState, err = streamingUploader.InitStreamingUpload(ctx, initParams)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize streaming upload: %w", err)
		}
	}

	streamInitTimer.StopWithMessage("parts=%d part_size=%s", uploadState.TotalParts, cloud.FormatBytes(int64(uploadState.PartSize)))

	// The prefix a previous attempt got onto the backend. This run encrypts and
	// uploads only what comes after it, and the completion below counts both.
	startPart := int64(len(resumed.parts))
	checkpoint := &streamingCheckpointer{
		params:      params,
		upload:      uploadState,
		sourceInfo:  sourceInfo,
		storageType: streamingUploader.StorageType(),
		createdAt:   resumed.createdAt,
		recorded:    startPart > 0,
	}

	// Progress interpolator provides smooth updates every 500ms, ensuring responsive
	// feedback even when individual parts take seconds to upload.
	var progressInterp *progressInterpolator
	if params.ProgressCallback != nil {
		progressInterp = newProgressInterpolator(params.ProgressCallback, fileSize)
		progressInterp.Start()
		defer progressInterp.Stop()

		// Wire up real-time byte tracking: the progressReader in S3 provider calls
		// this as bytes are sent, enabling progress updates DURING part uploads.
		uploadState.ByteProgressCallback = func(bytesUploaded int64) {
			progressInterp.AddInflight(bytesUploaded)
		}

		// Report 0% progress immediately after init completes.
		// This changes GUI status from "Preparing" to "0.00%" so users know
		// the transfer has started, even before the first part completes.
		params.ProgressCallback(0.0)

		// A resumed upload starts part of the way through the file. Without
		// crediting what the previous attempt transferred, progress would begin
		// at zero and stop short of the whole file when this run finishes.
		if resumedBytes := resumedPlaintextBytes(startPart, uploadState.PartSize, fileSize); resumedBytes > 0 {
			progressInterp.ConfirmBytes(resumedBytes)
		}
	}

	file, err := openUploadSource(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	if startPart > 0 {
		if err := skipUploadedPrefix(file, startPart*uploadState.PartSize); err != nil {
			return nil, fmt.Errorf("failed to seek to part %d of %s: %w", startPart, filepath.Base(params.LocalPath), err)
		}
	}

	cloud.TimingLog(params.OutputWriter, "Upload workers: %d threads (max %d)", concurrency, plan.WorkerCap)

	// Depth comes from the plan so encryption can run ahead and keep all upload
	// workers busy without the queued parts outgrowing the memory budget.
	encryptedChan := make(chan encryptedPart, plan.QueueDepth)

	// Result channel for collecting upload results
	resultChan := make(chan uploadResult, concurrency*2)

	// Context with cancellation for error propagation
	// Note: cancelUpload is called explicitly in cleanup defer after scaler goroutine is started
	uploadCtx, cancelUpload := context.WithCancel(ctx)

	// Track first error for clean shutdown
	var firstErr error
	var errOnce sync.Once

	// chainIV[i] is where the CBC chain stood after part i was encrypted, which
	// is what a checkpoint covering parts 0..i has to record. It cannot be read
	// when that part's upload lands: encryption runs ahead of the uploads, so
	// the chain has already moved on by then. Only the encryption goroutine
	// writes it, and each entry reaches the collector below through the same
	// channel sends that carry the part it belongs to.
	//
	// recordChainIV is what keeps a source that grew under the upload from
	// indexing past the end of it. That case is caught properly by the
	// completeness check below and by checkSourceUnchanged after it; nothing
	// about it should be a panic in a goroutine.
	chainIV := make([][]byte, uploadState.TotalParts)
	recordChainIV := func(partIndex int64) {
		if partIndex >= 0 && partIndex < int64(len(chainIV)) {
			chainIV[partIndex] = currentChainIV(uploadState)
		}
	}

	// Encryption goroutine: reads file, encrypts parts, sends to channel
	// Must be sequential due to CBC chaining constraint
	go func() {
		defer close(encryptedChan)
		buffer := make([]byte, uploadState.PartSize)
		partIndex := startPart

		for {
			// Check for context cancellation
			select {
			case <-uploadCtx.Done():
				return
			default:
			}

			// io.ReadFull, never a bare Read: a Reader may legally return fewer
			// bytes than the buffer holds, and network-backed sources (NFS/HPS
			// mounts) do so routinely. Under a bare Read every short read became
			// its own part, so the emitted parts outnumbered the TotalParts the
			// planner committed to — which trips the completion guard below and
			// mis-places the CBC-padded final part, since the provider marks it
			// by index. ReadFull retries internally, so a partial fill here can
			// only mean the end of the data.
			n, readErr := io.ReadFull(file, buffer)
			if readErr == io.ErrUnexpectedEOF {
				// Buffer partially filled at end of data: the genuine final part.
				readErr = io.EOF
			}

			// Handle empty file: first read returns (0, io.EOF)
			// Emit one encrypted empty part so the pipeline completes correctly
			if n == 0 && readErr == io.EOF && partIndex == 0 {
				ciphertext, encErr := streamingUploader.EncryptStreamingPart(uploadCtx, uploadState, 0, []byte{})
				if encErr != nil {
					errOnce.Do(func() { firstErr = encErr })
					cancelUpload()
					return
				}
				recordChainIV(0)
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
				recordChainIV(partIndex)

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

	var workerCount int32 = int32(concurrency)

	// Upload worker function - shared by initial workers and dynamically spawned workers
	uploadWorker := func(workerID int) {
		for enc := range encryptedChan {
			// Check for cancellation
			select {
			case <-uploadCtx.Done():
				return
			default:
			}

			// Upload this encrypted part
			partResult, uploadErr := streamingUploader.UploadCiphertext(uploadCtx, uploadState, enc.partIndex, enc.ciphertext)

			if uploadErr != nil {
				errOnce.Do(func() { firstErr = uploadErr })
				cancelUpload()
				resultChan <- uploadResult{partIndex: enc.partIndex, err: uploadErr}
				return
			}

			// Send success result
			resultChan <- uploadResult{
				partIndex: enc.partIndex,
				result:    partResult,
				plainSize: enc.plainSize,
			}
		}
	}

	var uploadWg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		uploadWg.Add(1)
		go func(workerID int) {
			defer uploadWg.Done()
			uploadWorker(workerID)
		}(i)
	}

	// Background scaler - dynamically spawns additional workers when threads become available.
	// This handles the case where other concurrent transfers finish and release their threads.
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
				// Never grow past the plan's cap: every extra worker holds another
				// part-sized ciphertext, and the memory budget was reserved for at
				// most WorkerCap of them. Asking for only what fits also avoids
				// taking threads from the pool that could not be used.
				room := plan.WorkerCap - int(atomic.LoadInt32(&workerCount))
				if room <= 0 {
					continue
				}
				if room > constants.UploadScalerThreadsPerTick {
					room = constants.UploadScalerThreadsPerTick
				}
				acquired := params.TransferHandle.TryAcquireMore(room)
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

	// Cancel the context first (signals scaler to stop), then wait for it to finish.
	// This prevents goroutine leaks.
	defer func() {
		cancelUpload() // Cancel context to signal scaler and other goroutines to stop
		<-scalerDone   // Wait for scaler goroutine to finish
	}()

	// Close result channel when all workers finish
	go func() {
		uploadWg.Wait()
		close(resultChan)
	}()

	// Collect results - parts may arrive out of order due to parallel uploads.
	// The map starts holding whatever an interrupted attempt already got onto
	// the backend, so what it counts is always resumed plus this run.
	partsMap := make(map[int64]*transfer.PartResult)
	for _, part := range resumed.parts {
		partsMap[part.PartIndex] = part
	}

	// prefix is how many parts from 0 upwards are on the backend without a gap.
	// Parts finish out of order under concurrency, so the set of completed parts
	// is not somewhere an upload can be resumed from — only this prefix is,
	// because the chain IV names one boundary and the source is re-read from it.
	prefix := startPart

	for res := range resultChan {
		if res.err != nil {
			// Error already recorded in firstErr, just continue draining
			continue
		}

		// Update size to plaintext size for accurate tracking
		res.result.Size = res.plainSize
		partsMap[res.partIndex] = res.result

		if progressInterp != nil {
			progressInterp.ConfirmBytes(res.plainSize)
		}

		grown := false
		for partsMap[prefix] != nil {
			prefix++
			grown = true
		}
		// A provider that does not expose its chain position leaves chainIV
		// empty; its uploads run exactly as before, they just cannot be resumed,
		// because nothing would know where to pick the chain back up. A prefix
		// past the planned part count means the source grew under the upload,
		// which the completeness check below is what answers.
		if grown && prefix <= uploadState.TotalParts && chainIV[prefix-1] != nil {
			checkpoint.save(orderedParts(partsMap, prefix), chainIV[prefix-1])
		}
	}

	// An attempt that checkpointed nothing has left nothing behind that a retry
	// could find, so the parts it did upload are already unreachable: discard
	// the backend upload instead of leaving it to the backend's own expiry. A
	// cancelled upload is discarded whether or not it checkpointed — nobody is
	// coming back for one the caller stopped.
	discard := !checkpoint.recorded || ctx.Err() != nil

	// Check for errors
	if firstErr != nil {
		endStreamingUpload(ctx, streamingUploader, params, uploadState, discard)
		return nil, firstErr
	}

	// Also check for context cancellation which may have occurred without setting firstErr.
	// This can happen if the user cancels the upload or a timeout occurs.
	select {
	case <-ctx.Done():
		endStreamingUpload(ctx, streamingUploader, params, uploadState, true)
		return nil, fmt.Errorf("upload cancelled: %w", ctx.Err())
	default:
	}

	// Verify we hold ALL expected parts before completing — the ones this run
	// uploaded and the ones it resumed. This prevents truncated uploads from
	// being registered as complete files; both backends assemble whatever subset
	// of parts they are handed and report success.
	expectedParts := int(uploadState.TotalParts)
	if len(partsMap) != expectedParts {
		endStreamingUpload(ctx, streamingUploader, params, uploadState, discard)
		return nil, fmt.Errorf("upload incomplete: received %d of %d parts (upload was interrupted or cancelled)",
			len(partsMap), expectedParts)
	}

	// Convert map to ordered slice for completion
	parts := orderedParts(partsMap, uploadState.TotalParts)

	completeTimer := cloud.StartTimer(params.OutputWriter, "Streaming upload complete")

	// Complete upload
	result, err := streamingUploader.CompleteStreamingUpload(ctx, uploadState, parts)
	if err != nil {
		// The parts are all still on the backend and the checkpoint still
		// describes them, so a retry finishes from here rather than re-sending
		// the file.
		endStreamingUpload(ctx, streamingUploader, params, uploadState, discard)
		return nil, fmt.Errorf("failed to complete streaming upload: %w", err)
	}

	completeTimer.StopWithMessage("parts=%d", len(parts))

	// Verified completion: the object is assembled, so its checkpoint describes
	// an upload that no longer exists.
	state.DeleteUploadState(params.LocalPath)

	return result, nil
}

// orderedParts lays the first count parts out in index order, which is the order
// both backends assemble an object in.
func orderedParts(parts map[int64]*transfer.PartResult, count int64) []*transfer.PartResult {
	ordered := make([]*transfer.PartResult, count)
	for i := int64(0); i < count; i++ {
		ordered[i] = parts[i]
	}
	return ordered
}

// currentChainIV reports where the CBC chain stands, or nil for a provider that
// does not expose it — see StreamingUpload.EncryptState.
func currentChainIV(uploadState *transfer.StreamingUpload) []byte {
	if uploadState.EncryptState == nil {
		return nil
	}
	return uploadState.EncryptState.GetCurrentIV()
}

// resumedPlaintextBytes is how much of the file the parts before startPart hold.
func resumedPlaintextBytes(startPart, partSize, fileSize int64) int64 {
	bytes := startPart * partSize
	if bytes > fileSize {
		return fileSize
	}
	return bytes
}

// skipUploadedPrefix positions the source at the first part this attempt has to
// send. Seeking is the whole point of resuming: reading the earlier parts only
// to throw them away would cost as much I/O as uploading them again. The source
// arrives as a plain io.ReadCloser because the seam that supplies it exists so
// tests can feed the encrypt loop a reader that returns short reads, and such a
// reader cannot seek — hence the fallback that reads the prefix away.
func skipUploadedPrefix(file io.Reader, offset int64) error {
	if seeker, ok := file.(io.Seeker); ok {
		_, err := seeker.Seek(offset, io.SeekStart)
		return err
	}
	_, err := io.CopyN(io.Discard, file, offset)
	return err
}

// endStreamingUpload closes out an attempt that produced no object.
//
// discard=false is the ordinary failure: the parts stay on the backend and the
// checkpoint stays on disk, because together they are what lets the next
// attempt carry on instead of re-sending the file. discard=true is for an
// upload nothing will come back to — one the caller cancelled, or one that
// never got a checkpoint written — where the parts would otherwise sit on the
// backend until its own expiry swept them.
func endStreamingUpload(ctx context.Context, uploader transfer.StreamingConcurrentUploader, params UploadParams, uploadState *transfer.StreamingUpload, discard bool) {
	if !discard {
		return
	}
	// Not ctx: the usual reason to be here is that ctx was cancelled, and an
	// abort issued on a cancelled context never reaches the backend — which is
	// exactly the case the abort exists for.
	abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), constants.PartOperationTimeout)
	defer cancel()

	if err := uploader.AbortStreamingUpload(abortCtx, uploadState); err != nil {
		log.Printf("Warning: failed to abort the streaming upload of %s (%s): %v",
			filepath.Base(params.LocalPath), uploadState.StoragePath, err)
	}
	state.DeleteUploadState(params.LocalPath)
}

// inFlightBytes is the peak part-buffer memory a plan permits: the queued parts,
// the parts the workers hold, and the transient buffers the encrypt stage keeps
// outside both. It is the same formula the planner reserved against, which is
// what makes it the ceiling a refitted pipeline has to stay under.
func inFlightBytes(plan resources.UploadPlan) int64 {
	return (int64(plan.QueueDepth) + int64(plan.WorkerCap) + constants.UploadPipelineTransientParts) * plan.PartSize
}

// fitPipelineToPartSize narrows a plan to the part size a resumed upload is
// stuck with, and reports whether the memory the plan holds can carry it. Part
// size is fixed for the life of an object — CBC chains through it and the
// object's metadata states it — so a resumed attempt cannot adopt the one this
// run would have planned.
//
// Scaling only the queue and the workers is not enough: the transient buffers
// scale with the part size too, so a pipeline fitted that way can hold several
// times the memory that was reserved for it. What has to stay inside the
// reservation is the whole working set, and below one queued part and one worker
// there is no pipeline left — a saved part size that far past the reservation is
// one this run cannot continue at all.
func fitPipelineToPartSize(plan resources.UploadPlan, partSize int64) (resources.UploadPlan, bool) {
	if partSize <= 0 {
		return plan, false
	}
	if plan.PartSize <= 0 || partSize <= plan.PartSize {
		// Smaller parts only ever free memory.
		plan.PartSize = partSize
		return plan, true
	}

	// Parts of the saved size the reservation can hold in the queue and the
	// workers together, once the encrypt stage has taken its transients off the
	// top — the same subtraction the planner makes.
	affordable := inFlightBytes(plan)/partSize - constants.UploadPipelineTransientParts
	if affordable < 2 {
		return plan, false
	}

	// Squeeze the queue first and the workers only when that is not enough,
	// which is the order the planner squeezes them in.
	if int64(plan.QueueDepth+plan.WorkerCap) > affordable {
		if affordable-int64(plan.WorkerCap) >= 1 {
			plan.QueueDepth = int(affordable) - plan.WorkerCap
		} else {
			plan.QueueDepth = 1
			plan.WorkerCap = int(affordable) - 1
		}
	}
	plan.PartSize = partSize
	return plan, true
}

// backendUploadAborter is a provider that can discard an upload addressed only
// by what a resume state records about it — the remote path and, where the
// backend has one, the upload ID. Both providers implement it; the assertion is
// what keeps a provider that cannot from having to.
type backendUploadAborter interface {
	AbortUploadByID(ctx context.Context, uploadID, storagePath string) error
}

// streamingResume is what an interrupted streaming attempt left behind for this
// source: the object it was filling, the encryption chain at the boundary it
// reached, and the parts the backend accepted before it stopped.
type streamingResume struct {
	usable    bool
	saved     *state.UploadResumeState
	masterKey []byte
	initialIV []byte
	chainIV   []byte
	parts     []*transfer.PartResult
	createdAt time.Time
}

// loadStreamingResume recovers the object identity and encryption chain of an
// interrupted streaming upload of this exact source, so this run can continue
// the parts the backend already accepted instead of sending the file again.
//
// Anything it cannot fully match is abandoned rather than adapted: a checkpoint
// that no longer describes the file we are about to register can only be
// finished as an object nothing will ask for.
func loadStreamingResume(ctx context.Context, uploader transfer.StreamingConcurrentUploader, params UploadParams, sourceInfo os.FileInfo, fileSize int64) streamingResume {
	saved, err := state.LoadUploadState(params.LocalPath)
	if err != nil || saved == nil {
		return streamingResume{}
	}

	if reason := streamingResumeBlocker(saved, params.LocalPath, sourceInfo, uploader.StorageType(), fileSize); reason != "" {
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Starting a fresh upload of %s: %s\n",
				filepath.Base(params.LocalPath), reason)
		}
		abandonStreamingState(ctx, uploader, params, saved)
		return streamingResume{}
	}

	masterKey, keyErr := encryption.DecodeBase64(saved.MasterKey)
	initialIV, ivErr := encryption.DecodeBase64(saved.InitialIV)
	chainIV, chainErr := encryption.DecodeBase64(saved.ChainIV)
	if keyErr != nil || ivErr != nil || chainErr != nil {
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Starting a fresh upload of %s: the saved encryption chain cannot be read\n",
				filepath.Base(params.LocalPath))
		}
		abandonStreamingState(ctx, uploader, params, saved)
		return streamingResume{}
	}

	parts := make([]*transfer.PartResult, 0, len(saved.StreamingParts))
	for _, part := range saved.StreamingParts {
		parts = append(parts, &transfer.PartResult{
			PartIndex: part.PartIndex,
			// Both backends number a part one above its index; the state records
			// the index, because that is what the encryption chain counts in.
			PartNumber: int32(part.PartIndex + 1),
			ETag:       part.Handle,
		})
	}

	return streamingResume{
		usable:    true,
		saved:     saved,
		masterKey: masterKey,
		initialIV: initialIV,
		chainIV:   chainIV,
		parts:     parts,
		// The backend's own expiry runs from when the upload was opened, not
		// from the last checkpoint, so the age this state is judged on has to
		// survive every checkpoint and every resume.
		createdAt: saved.CreatedAt,
	}
}

// streamingResumeBlocker names the reason this state cannot be resumed, or ""
// when it can. Every check answers the same question: do the parts already on
// the backend still describe the file we are about to register?
//
// state.ValidateUploadState is deliberately not called here even though the
// checks overlap: its streaming branch demands the file_id of the HKDF format,
// which a CBC upload has never had, so it rejects every state this path writes.
func streamingResumeBlocker(saved *state.UploadResumeState, localPath string, sourceInfo os.FileInfo, storageType string, fileSize int64) string {
	if saved.FormatVersion != 1 {
		return "the saved state belongs to a pre-encrypt upload"
	}
	// The object identity in the state is one backend's; handing it to another
	// would name a different object and strand the first backend's parts.
	if saved.StorageType != "" && saved.StorageType != storageType {
		return "the interrupted upload was going to " + saved.StorageType
	}
	if saved.LocalPath != localPath {
		return "the saved state describes another file"
	}
	if saved.OriginalSize != fileSize {
		return fmt.Sprintf("the file has changed size since the interrupted upload (was %d, now %d)", saved.OriginalSize, fileSize)
	}
	// State written before v4.9.9 has no modification time, and size alone
	// cannot tell an edited file from the one those parts were cut from.
	if saved.SourceModTime.IsZero() {
		return "the saved state predates modification-time tracking"
	}
	if !saved.SourceModTime.Equal(sourceInfo.ModTime()) {
		return "the file has been modified since the interrupted upload"
	}
	// Both backends discard an unfinished upload after seven days, so a state
	// older than that describes parts that are no longer there to continue.
	if time.Since(saved.CreatedAt) > state.MaxResumeAge {
		return "the interrupted upload has expired"
	}
	if saved.ObjectKey == "" {
		return "the saved state does not name the object the interrupted upload was filling"
	}
	if saved.PartSize <= 0 {
		return "the saved state does not say what part size the interrupted upload used"
	}
	if saved.MasterKey == "" || saved.InitialIV == "" || saved.ChainIV == "" {
		return "the saved state does not carry the encryption chain of the interrupted upload"
	}
	// Decoded lengths, not just presence: base64 that decodes to the wrong
	// number of bytes gets all the way to the provider, where building the
	// cipher fails — and it fails identically on every later attempt, because
	// nothing on that path retires the state that caused it.
	if !decodesTo(saved.MasterKey, encryption.KeySize) {
		return "the saved encryption key of the interrupted upload is not a key"
	}
	if !decodesTo(saved.InitialIV, encryption.IVSize) || !decodesTo(saved.ChainIV, encryption.IVSize) {
		return "the saved encryption chain of the interrupted upload is not a chain position"
	}
	if len(saved.StreamingParts) == 0 {
		return "the interrupted upload has no completed parts to continue from"
	}
	// The chain IV describes one boundary, and the source is re-read from it, so
	// only an unbroken run of parts from the start can be continued. A list that
	// is anything else describes a different upload than the chain IV does.
	for i, part := range saved.StreamingParts {
		if part.PartIndex != int64(i) || part.Handle == "" {
			return "the saved parts of the interrupted upload are not an unbroken run from its start"
		}
	}
	if int64(len(saved.StreamingParts)) > transfer.CalculateTotalParts(fileSize, saved.PartSize) {
		return "the interrupted upload recorded more parts than the file has"
	}
	return ""
}

// abandonStreamingState retires a state that can no longer be resumed and
// discards the backend upload it was filling: those parts belong to an object
// nothing will ever ask for, and until they are aborted they occupy storage on
// the backend until its own seven-day expiry sweeps them.
//
// The upload is addressed by the identity the state records rather than by a
// rebuilt handle. Rebuilding one takes the encryption parameters a resume needs,
// which is exactly what a state damaged enough to be abandoned may not have —
// and a state whose part size is missing or whose key is the wrong length used
// to fail inside that rebuild rather than be retired by it. The identity is also
// all a pre-encrypt state has, and deleting its sidecar without using it is what
// stranded that mode's multipart uploads when a streaming attempt followed one.
func abandonStreamingState(ctx context.Context, uploader transfer.StreamingConcurrentUploader, params UploadParams, saved *state.UploadResumeState) {
	// Not ctx: abandonment often runs on the way out of a cancelled upload, and
	// an abort issued on a cancelled context never reaches the backend.
	abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), constants.PartOperationTimeout)
	defer cancel()

	// The object identity in the state is one backend's; handing it to another
	// would name a different object. A state that does not say which backend it
	// belongs to is left to that backend's own expiry.
	if aborter, ok := uploader.(backendUploadAborter); ok &&
		saved.ObjectKey != "" && saved.StorageType == uploader.StorageType() {
		if err := aborter.AbortUploadByID(abortCtx, saved.UploadID, saved.ObjectKey); err != nil {
			log.Printf("Warning: failed to abort the abandoned upload of %s (%s): %v",
				filepath.Base(params.LocalPath), saved.ObjectKey, err)
		}
	}

	if saved.FormatVersion != 1 {
		// A pre-encrypt state also names an encrypted copy on disk, which is
		// retired with it. Its own helper is what knows one of ours from
		// anything else a state file's path field might point at.
		abandonPreEncryptState(saved, params.LocalPath)
		return
	}

	state.DeleteUploadState(params.LocalPath)
}

// decodesTo reports whether base64 text decodes to exactly want bytes.
func decodesTo(encoded string, want int) bool {
	decoded, err := encryption.DecodeBase64(encoded)
	return err == nil && len(decoded) == want
}

// reopenStreamingUpload reopens the backend upload an interrupted attempt left
// behind.
//
// It returns (nil, nil) when that upload is no longer there to continue: the
// state is retired and the caller starts a fresh object, which always produces a
// correct upload and costs only the bytes already sent. It returns an error only
// when the backend could not be reached to find out — a credential blip is not a
// reason to throw away a resume, and the retry that follows will find the
// checkpoint still in place.
func reopenStreamingUpload(ctx context.Context, uploader transfer.StreamingConcurrentUploader, params UploadParams, resumed streamingResume) (*transfer.StreamingUpload, error) {
	saved := resumed.saved
	name := filepath.Base(params.LocalPath)

	exists, err := uploader.ValidateStreamingUploadExists(ctx, saved.UploadID, saved.ObjectKey)
	if err != nil {
		return nil, fmt.Errorf("failed to check the interrupted upload of %s: %w", name, err)
	}
	if !exists {
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Starting a fresh upload of %s: %s no longer holds the interrupted upload\n",
				name, saved.StorageType)
		}
		// Nothing to abort — the backend has already discarded it.
		state.DeleteUploadState(params.LocalPath)
		return nil, nil
	}

	uploadState, err := uploader.InitStreamingUploadFromState(ctx, transfer.StreamingUploadResumeParams{
		LocalPath:      params.LocalPath,
		FileSize:       saved.OriginalSize,
		StoragePath:    saved.ObjectKey,
		UploadID:       saved.UploadID,
		MasterKey:      resumed.masterKey,
		InitialIV:      resumed.initialIV,
		CurrentIV:      resumed.chainIV,
		PartSize:       saved.PartSize,
		RandomSuffix:   saved.RandomSuffix,
		CompletedParts: resumed.parts,
		OutputWriter:   params.OutputWriter,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to resume the interrupted upload of %s: %w", name, err)
	}

	if params.OutputWriter != nil {
		fmt.Fprintf(params.OutputWriter, "Continuing the upload of %s from part %d of %d\n",
			name, len(resumed.parts)+1, uploadState.TotalParts)
	}
	return uploadState, nil
}

// streamingCheckpointer writes where a streaming upload has got to, so a later
// attempt can pick the same object up rather than start another one.
type streamingCheckpointer struct {
	params      UploadParams
	upload      *transfer.StreamingUpload
	sourceInfo  os.FileInfo
	storageType string
	createdAt   time.Time

	// recorded reports that a checkpoint a retry could find exists — either one
	// this attempt wrote, or the one it resumed from.
	recorded bool
	warned   bool
}

// save records the contiguous prefix of parts the backend has accepted, along
// with the chain position at that boundary.
//
// Failing to write it does not fail the upload. The state file sits next to the
// source, which may be on a read-only or full filesystem, and a streaming upload
// has never had to write anything there to succeed; what is lost is only the
// ability to resume, which is why it is reported once rather than per part.
func (c *streamingCheckpointer) save(prefix []*transfer.PartResult, chainIV []byte) {
	if len(prefix) == 0 || len(chainIV) == 0 {
		return
	}
	if c.createdAt.IsZero() {
		c.createdAt = time.Now()
	}

	parts := make([]state.StreamingPart, 0, len(prefix))
	for _, part := range prefix {
		parts = append(parts, state.StreamingPart{PartIndex: part.PartIndex, Handle: part.ETag})
	}

	// Every part but the last is a whole part of ciphertext; only a prefix that
	// reaches the end of the file carries the padding CBC adds.
	uploaded := int64(len(prefix)) * c.upload.PartSize
	if int64(len(prefix)) == c.upload.TotalParts {
		uploaded = transfer.CiphertextSize(c.upload.TotalSize)
	}

	err := state.SaveUploadState(&state.UploadResumeState{
		LocalPath:      c.params.LocalPath,
		ObjectKey:      c.upload.StoragePath,
		UploadID:       c.upload.UploadID,
		OriginalSize:   c.upload.TotalSize,
		TotalSize:      transfer.CiphertextSize(c.upload.TotalSize),
		SourceModTime:  c.sourceInfo.ModTime(),
		UploadedBytes:  uploaded,
		RandomSuffix:   c.upload.RandomSuffix,
		CreatedAt:      c.createdAt,
		LastUpdate:     time.Now(),
		StorageType:    c.storageType,
		FormatVersion:  1,
		MasterKey:      encryption.EncodeBase64(c.upload.MasterKey),
		PartSize:       c.upload.PartSize,
		InitialIV:      encryption.EncodeBase64(c.upload.InitialIV),
		ChainIV:        encryption.EncodeBase64(chainIV),
		StreamingParts: parts,
		ProcessID:      os.Getpid(),
	}, c.params.LocalPath)

	if err != nil {
		if !c.warned {
			c.warned = true
			log.Printf("Warning: cannot record the progress of the upload of %s, so an interrupted attempt will start over: %v",
				filepath.Base(c.params.LocalPath), err)
		}
		return
	}
	c.recorded = true
}

// uploadPreEncrypt uses the PreEncryptUploader interface for pre-encrypted uploads.
func uploadPreEncrypt(ctx context.Context, provider cloud.CloudTransfer, params UploadParams, fileSize int64) (*cloud.UploadResult, error) {
	// Cast to PreEncryptUploader
	preEncryptUploader, ok := provider.(transfer.PreEncryptUploader)
	if !ok {
		return nil, fmt.Errorf("provider does not support pre-encrypt upload")
	}

	sourceInfo, err := os.Stat(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// Examining and retiring the artifacts of an interrupted attempt is the same
	// exclusive step as the transfer that follows it: without the lock, a second
	// invocation could delete the ciphertext and state of an upload that is
	// still running. The provider takes this same lock for the transfer itself
	// and it is not re-entrant, so it is handed over rather than nested.
	uploadLock, err := state.AcquireUploadLock(params.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire upload lock: %w", err)
	}
	releaseLock := sync.OnceFunc(func() { state.ReleaseUploadLock(uploadLock) })
	defer releaseLock()

	// Recovery belongs here, not in the providers: they can only compare the
	// object key they were handed against the one in the state, and every
	// attempt used to arrive with a freshly generated key, IV and suffix. That
	// made the state describe a DIFFERENT ciphertext by construction, so the
	// parts the backend had already accepted were always discarded.
	resumed := resumePreEncryptArtifacts(params, sourceInfo, preEncryptUploader.StorageType())

	encryptionKey, iv, randomSuffix, encryptedPath := resumed.encryptionKey, resumed.iv, resumed.randomSuffix, resumed.encryptedPath
	if !resumed.usable {
		encryptionKey, iv, randomSuffix, err = GenerateEncryptionParams()
		if err != nil {
			return nil, fmt.Errorf("failed to generate encryption params: %w", err)
		}

		encryptedPath, err = CreateEncryptedTempFile(params.LocalPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create temp file: %w", err)
		}
	}

	// The encrypted copy is the artifact a retry needs, so it outlives a failed
	// attempt — but only while a resume state names it, since nothing would
	// ever come back for one that is not recorded anywhere. A reused copy
	// starts out described by the state that produced it, so anything that goes
	// wrong before the upload even starts leaves it in place.
	keepEncrypted := resumed.usable
	defer func() {
		if !keepEncrypted {
			os.Remove(encryptedPath)
		}
	}()

	if !resumed.usable {
		encryptTimer := cloud.StartTimer(params.OutputWriter, "Pre-encryption")

		// Encrypt file
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Encrypting file (%s)...\n", filepath.Base(params.LocalPath))
		}
		if err := encryption.EncryptFile(params.LocalPath, encryptedPath, encryptionKey, iv); err != nil {
			return nil, fmt.Errorf("failed to encrypt file: %w", err)
		}

		encryptTimer.StopWithThroughput(fileSize)
	} else if params.OutputWriter != nil {
		fmt.Fprintf(params.OutputWriter, "Reusing the encrypted copy of %s from the interrupted upload\n",
			filepath.Base(params.LocalPath))
	}

	// Plan against the ciphertext, which is what the backend splits into parts.
	// Planning here rather than from the plaintext size keeps the part count
	// exact: CBC padding can push a file that divides evenly into the part limit
	// one byte over it.
	encryptedInfo, err := os.Stat(encryptedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat encrypted file: %w", err)
	}
	threads := 1
	if params.TransferHandle != nil && params.TransferHandle.GetThreads() > 1 {
		threads = params.TransferHandle.GetThreads()
	}
	plan, releasePlan, err := planStreamingUpload(params.TransferHandle, encryptedInfo.Size(), threads, preEncryptUploader.UploadLimits())
	if err != nil {
		return nil, err
	}
	defer releasePlan()

	// Build upload params (providers stat the encrypted file themselves)
	uploadParams := transfer.EncryptedFileUploadParams{
		LocalPath:        params.LocalPath,
		EncryptedPath:    encryptedPath,
		EncryptionKey:    encryptionKey,
		IV:               iv,
		RandomSuffix:     randomSuffix,
		OriginalSize:     fileSize,
		SourceModTime:    sourceInfo.ModTime(),
		ProgressCallback: params.ProgressCallback,
		TransferHandle:   params.TransferHandle,
		OutputWriter:     params.OutputWriter,
		Plan:             &plan,
	}

	uploadTimer := cloud.StartTimer(params.OutputWriter, "Pre-encrypt upload")

	// Upload encrypted file, which takes the lock this call has been holding.
	releaseLock()
	result, err := preEncryptUploader.UploadEncryptedFile(ctx, uploadParams)
	if err != nil {
		// Keep the ciphertext for the retry that the state file describes. An
		// attempt that failed before it checkpointed anything has nothing to
		// come back to, so its copy is not worth the disk.
		keepEncrypted = preEncryptStateNames(params.LocalPath, encryptedPath)
		return nil, fmt.Errorf("failed to upload encrypted file: %w", err)
	}

	uploadTimer.StopWithThroughput(fileSize)

	// Verified completion: the artifacts have nothing left to describe.
	keepEncrypted = false
	state.DeleteUploadState(params.LocalPath)

	return result, nil
}

// preEncryptResume is what an interrupted attempt left behind for this source.
type preEncryptResume struct {
	usable        bool
	encryptionKey []byte
	iv            []byte
	randomSuffix  string
	encryptedPath string
}

// resumePreEncryptArtifacts recovers the object identity, encryption parameters
// and encrypted copy of an interrupted upload of this exact source, so the
// provider can continue the parts it already accepted.
//
// Anything it cannot fully match is abandoned rather than adapted: state and
// ciphertext are deleted together, because a ciphertext whose identity no
// longer applies can only be finished as an object nothing will ask for.
func resumePreEncryptArtifacts(params UploadParams, sourceInfo os.FileInfo, storageType string) preEncryptResume {
	saved, err := state.LoadUploadState(params.LocalPath)
	if err != nil || saved == nil {
		return preEncryptResume{}
	}

	if reason := preEncryptResumeBlocker(saved, params.LocalPath, sourceInfo, storageType); reason != "" {
		if params.OutputWriter != nil {
			fmt.Fprintf(params.OutputWriter, "Starting a fresh upload of %s: %s\n",
				filepath.Base(params.LocalPath), reason)
		}
		abandonPreEncryptState(saved, params.LocalPath)
		return preEncryptResume{}
	}

	encryptionKey, keyErr := encryption.DecodeBase64(saved.EncryptionKey)
	iv, ivErr := encryption.DecodeBase64(saved.IV)
	if keyErr != nil || ivErr != nil {
		abandonPreEncryptState(saved, params.LocalPath)
		return preEncryptResume{}
	}

	return preEncryptResume{
		usable:        true,
		encryptionKey: encryptionKey,
		iv:            iv,
		randomSuffix:  saved.RandomSuffix,
		encryptedPath: saved.EncryptedPath,
	}
}

// preEncryptResumeBlocker names the reason this state cannot be resumed, or ""
// when it can. Every check answers the same question: do these saved bytes
// still describe the file we are about to register?
func preEncryptResumeBlocker(saved *state.UploadResumeState, localPath string, sourceInfo os.FileInfo, storageType string) string {
	if saved.FormatVersion != 0 {
		return "the saved state belongs to a streaming upload"
	}
	// The object identity in the state is one backend's; handing it to another
	// would name a different object and strand the first backend's parts.
	if saved.StorageType != "" && saved.StorageType != storageType {
		return "the interrupted upload was going to " + saved.StorageType
	}
	if err := state.ValidateUploadState(saved, localPath); err != nil {
		return err.Error()
	}
	if saved.EncryptionKey == "" || saved.IV == "" || saved.RandomSuffix == "" {
		return "the saved state does not carry the encryption parameters of the interrupted upload"
	}
	if !isEncryptedTempFile(saved.EncryptedPath, localPath) {
		return "the interrupted upload recorded no encrypted copy of its own"
	}
	// State written before v4.9.9 has no modification time, and size alone
	// cannot tell an edited file from the one the ciphertext describes.
	if saved.SourceModTime.IsZero() {
		return "the saved state predates modification-time tracking"
	}
	if !saved.SourceModTime.Equal(sourceInfo.ModTime()) {
		return "the file has been modified since the interrupted upload"
	}
	// ValidateUploadState only checks that the encrypted copy exists. A
	// truncated one would be uploaded as a short object and registered under
	// the whole file's checksum.
	encInfo, err := os.Stat(saved.EncryptedPath)
	if err != nil {
		return "the encrypted copy of the interrupted upload is gone"
	}
	if encInfo.Size() != saved.TotalSize {
		return "the encrypted copy of the interrupted upload is the wrong size"
	}
	return ""
}

// abandonPreEncryptState retires a state that can no longer be resumed, along
// with the ciphertext it named.
func abandonPreEncryptState(saved *state.UploadResumeState, localPath string) {
	if isEncryptedTempFile(saved.EncryptedPath, localPath) {
		os.Remove(saved.EncryptedPath)
	}
	state.DeleteUploadState(localPath)
}

// isEncryptedTempFile reports whether a path in a state file names a ciphertext
// this package made. CreateEncryptedTempFile is the only writer of that field
// and always names it "*.encrypted", so a state file that points anywhere else
// — at the source above all — must not be uploaded as ciphertext or deleted as
// scratch.
func isEncryptedTempFile(encryptedPath, localPath string) bool {
	return encryptedPath != "" &&
		encryptedPath != localPath &&
		strings.HasSuffix(encryptedPath, ".encrypted")
}

// preEncryptStateNames reports whether a resume state describes this ciphertext,
// which is what makes keeping the ciphertext worthwhile.
func preEncryptStateNames(localPath, encryptedPath string) bool {
	saved, err := state.LoadUploadState(localPath)
	return err == nil && saved != nil && saved.EncryptedPath == encryptedPath
}
