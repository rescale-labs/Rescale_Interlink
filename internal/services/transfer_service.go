// Package services provides frontend-agnostic business logic for Rescale Interlink.
// v3.6.4: TransferService handles upload/download orchestration without framework dependencies.
package services

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
)

// TransferService handles upload and download orchestration.
// It is frontend-agnostic: no Fyne imports, no framework-specific threading.
// Progress and state changes are published via the EventBus.
type TransferService struct {
	apiClient *api.Client
	eventBus  *events.EventBus
	queue     *transfer.Queue
	logger    *logging.Logger

	// Concurrency control
	semaphore   chan struct{} // Limits concurrent transfers
	activeSlots int32         // Atomic counter for logging

	// Resource management
	resourceMgr *resources.Manager
	transferMgr *transfer.Manager

	// Credential manager (cached, shared across transfers)
	credManager *credentials.Manager

	mu sync.RWMutex
}

// TransferServiceConfig configures the TransferService.
type TransferServiceConfig struct {
	// MaxConcurrent is the maximum number of concurrent transfers.
	// Defaults to constants.DefaultMaxConcurrent (5).
	MaxConcurrent int
}

// NewTransferService creates a new TransferService.
func NewTransferService(apiClient *api.Client, eventBus *events.EventBus, config TransferServiceConfig) *TransferService {
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = constants.DefaultMaxConcurrent
	}

	queue := transfer.NewQueue(eventBus)
	resourceMgr := resources.NewManager(resources.Config{AutoScale: true})
	transferMgr := transfer.NewManager(resourceMgr)

	ts := &TransferService{
		apiClient:   apiClient,
		eventBus:    eventBus,
		queue:       queue,
		logger:      logging.NewLogger("transfer-service", nil),
		semaphore:   make(chan struct{}, config.MaxConcurrent),
		resourceMgr: resourceMgr,
		transferMgr: transferMgr,
	}

	// Set up retry executor
	queue.SetRetryExecutor(ts)

	return ts
}

// SetAPIClient updates the API client (e.g., after credential change).
func (ts *TransferService) SetAPIClient(client *api.Client) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.apiClient = client
	ts.credManager = nil // Clear cached credential manager
}

// GetQueue returns the underlying transfer queue.
// Used by GUI components that need direct queue access.
func (ts *TransferService) GetQueue() *transfer.Queue {
	return ts.queue
}

// GetSemaphore returns the transfer semaphore.
// Used for shared concurrency control across multiple batches.
func (ts *TransferService) GetSemaphore() chan struct{} {
	return ts.semaphore
}

// StartTransfers initiates one or more transfers.
// Returns immediately; progress is published via events.
// The function handles both uploads and downloads based on request type.
func (ts *TransferService) StartTransfers(ctx context.Context, requests []TransferRequest) error {
	if len(requests) == 0 {
		return nil
	}

	ts.mu.RLock()
	apiClient := ts.apiClient
	ts.mu.RUnlock()

	if apiClient == nil {
		return fmt.Errorf("API client not configured")
	}

	// v4.0.0: Warm credential cache in background to avoid blocking downloads.
	// Credentials are cached after first fetch; downloads can proceed immediately.
	// This fixes the 60+ second delay users experienced before downloads started.
	go ts.warmCredentialCache(ctx)

	// Separate uploads and downloads
	var uploads, downloads []TransferRequest
	for _, req := range requests {
		if req.Type == TransferTypeUpload {
			uploads = append(uploads, req)
		} else {
			downloads = append(downloads, req)
		}
	}

	// Start upload batch
	if len(uploads) > 0 {
		go ts.executeUploadBatch(ctx, uploads)
	}

	// Start download batch
	if len(downloads) > 0 {
		go ts.executeDownloadBatch(ctx, downloads)
	}

	return nil
}

// warmCredentialCache pre-warms the credential cache.
func (ts *TransferService) warmCredentialCache(ctx context.Context) {
	ts.mu.Lock()
	if ts.credManager == nil && ts.apiClient != nil {
		ts.credManager = credentials.GetManager(ts.apiClient)
	}
	credManager := ts.credManager
	ts.mu.Unlock()

	if credManager != nil {
		_, _ = credManager.GetUserProfile(ctx)
		_, _ = credManager.GetRootFolders(ctx)
	}
}

// executeUploadBatch handles a batch of upload requests.
func (ts *TransferService) executeUploadBatch(ctx context.Context, requests []TransferRequest) {
	ts.mu.RLock()
	apiClient := ts.apiClient
	ts.mu.RUnlock()

	if apiClient == nil {
		ts.logger.Error().Msg("Upload batch aborted: no API client")
		return
	}

	total := len(requests)
	currentSlots := atomic.LoadInt32(&ts.activeSlots)
	log.Printf("[BATCH] Starting UPLOAD batch: %d files (active=%d/%d)", total, currentSlots, cap(ts.semaphore))

	var wg sync.WaitGroup

	for _, req := range requests {
		wg.Add(1)
		go func(r TransferRequest) {
			defer wg.Done()
			ts.executeUpload(ctx, r, apiClient)
		}(req)
	}

	wg.Wait()
	log.Printf("[BATCH] UPLOAD batch complete: %d files", total)
}

// executeDownloadBatch handles a batch of download requests.
func (ts *TransferService) executeDownloadBatch(ctx context.Context, requests []TransferRequest) {
	ts.mu.RLock()
	apiClient := ts.apiClient
	ts.mu.RUnlock()

	if apiClient == nil {
		ts.logger.Error().Msg("Download batch aborted: no API client")
		return
	}

	total := len(requests)
	currentSlots := atomic.LoadInt32(&ts.activeSlots)
	log.Printf("[BATCH] Starting DOWNLOAD batch: %d files (active=%d/%d)", total, currentSlots, cap(ts.semaphore))

	var wg sync.WaitGroup

	for _, req := range requests {
		wg.Add(1)
		go func(r TransferRequest) {
			defer wg.Done()
			ts.executeDownload(ctx, r, apiClient)
		}(req)
	}

	wg.Wait()
	log.Printf("[BATCH] DOWNLOAD batch complete: %d files", total)
}

// executeUpload handles a single upload with semaphore and progress tracking.
func (ts *TransferService) executeUpload(ctx context.Context, req TransferRequest, apiClient *api.Client) {
	fileName := req.Name
	if fileName == "" {
		fileName = filepath.Base(req.Source)
	}

	// Track in queue (starts as Queued)
	task := ts.queue.TrackTransfer(fileName, req.Size, transfer.TaskTypeUpload, req.Source, req.Dest)
	taskID := task.ID

	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			ts.logger.Error().Msgf("PANIC in upload for %s: %v", fileName, r)
			ts.queue.Fail(taskID, fmt.Errorf("panic: %v", r))
		}
	}()

	// Log and wait for semaphore slot
	slotsBefore := atomic.LoadInt32(&ts.activeSlots)
	log.Printf("[SLOT] UPLOAD %s: waiting (active=%d/%d)", fileName, slotsBefore, cap(ts.semaphore))

	select {
	case ts.semaphore <- struct{}{}:
		// Acquired slot
	case <-ctx.Done():
		ts.queue.Fail(taskID, ctx.Err())
		return
	}

	slotsNow := atomic.AddInt32(&ts.activeSlots, 1)
	log.Printf("[SLOT] UPLOAD %s: ACQUIRED (active=%d/%d)", fileName, slotsNow, cap(ts.semaphore))

	defer func() {
		<-ts.semaphore
		slotsAfter := atomic.AddInt32(&ts.activeSlots, -1)
		log.Printf("[SLOT] UPLOAD %s: RELEASED (active=%d/%d)", fileName, slotsAfter, cap(ts.semaphore))
	}()

	// Mark as initializing
	ts.queue.Activate(taskID)

	// Check cancellation
	select {
	case <-ctx.Done():
		ts.queue.Fail(taskID, ctx.Err())
		return
	default:
	}

	// Get file info for transfer allocation
	fileInfo, err := os.Stat(req.Source)
	if err != nil {
		ts.queue.Fail(taskID, fmt.Errorf("failed to stat file: %w", err))
		return
	}

	// Allocate transfer handle
	transferHandle := ts.transferMgr.AllocateTransfer(fileInfo.Size(), 1)

	// Execute upload with progress callback
	_, err = upload.UploadFile(ctx, upload.UploadParams{
		LocalPath: req.Source,
		FolderID:  req.Dest,
		APIClient: apiClient,
		ProgressCallback: func(progress float64) {
			ts.queue.StartTransfer(taskID) // Idempotent transition to Active
			ts.queue.UpdateProgress(taskID, progress)
		},
		TransferHandle: transferHandle,
	})

	if err != nil {
		ts.queue.Fail(taskID, err)
		ts.logger.Error().Err(err).Str("path", req.Source).Msg("Upload failed")
	} else {
		ts.queue.Complete(taskID)
		ts.logger.Info().Str("path", req.Source).Msg("File uploaded")
	}
}

// executeDownload handles a single download with semaphore and progress tracking.
func (ts *TransferService) executeDownload(ctx context.Context, req TransferRequest, apiClient *api.Client) {
	// v4.5.9: Consistent empty-name fallback (matches retry path)
	fileName := req.Name
	if fileName == "" {
		fileName = req.Source
		ts.logger.Warn().
			Str("file_id", req.Source).
			Msg("Download: req.Name is empty, using file ID as filename")
	}

	// Track in queue (starts as Queued)
	task := ts.queue.TrackTransfer(fileName, req.Size, transfer.TaskTypeDownload, req.Source, req.Dest)
	taskID := task.ID

	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			ts.logger.Error().Msgf("PANIC in download for %s: %v", fileName, r)
			ts.queue.Fail(taskID, fmt.Errorf("panic: %v", r))
		}
	}()

	// Log and wait for semaphore slot
	slotsBefore := atomic.LoadInt32(&ts.activeSlots)
	log.Printf("[SLOT] DOWNLOAD %s: waiting (active=%d/%d)", fileName, slotsBefore, cap(ts.semaphore))

	select {
	case ts.semaphore <- struct{}{}:
		// Acquired slot
	case <-ctx.Done():
		ts.queue.Fail(taskID, ctx.Err())
		return
	}

	slotsNow := atomic.AddInt32(&ts.activeSlots, 1)
	log.Printf("[SLOT] DOWNLOAD %s: ACQUIRED (active=%d/%d)", fileName, slotsNow, cap(ts.semaphore))

	defer func() {
		<-ts.semaphore
		slotsAfter := atomic.AddInt32(&ts.activeSlots, -1)
		log.Printf("[SLOT] DOWNLOAD %s: RELEASED (active=%d/%d)", fileName, slotsAfter, cap(ts.semaphore))
	}()

	// Mark as initializing
	ts.queue.Activate(taskID)

	// Check cancellation
	select {
	case <-ctx.Done():
		ts.queue.Fail(taskID, ctx.Err())
		return
	default:
	}

	// Allocate transfer handle
	transferHandle := ts.transferMgr.AllocateTransfer(req.Size, 1)

	// Ensure dest is a file path, not a directory
	// If dest is a directory, append the filename
	localPath := req.Dest
	if info, err := os.Stat(localPath); err == nil && info.IsDir() {
		localPath = filepath.Join(localPath, fileName)
		ts.logger.Debug().
			Str("original_dest", req.Dest).
			Str("corrected_path", localPath).
			Msg("Dest was a directory, appending filename")
	}

	// Execute download with progress callback
	err := download.DownloadFile(ctx, download.DownloadParams{
		FileID:    req.Source, // For downloads, Source is the file ID
		LocalPath: localPath,  // For downloads, the full local file path
		APIClient: apiClient,
		ProgressCallback: func(progress float64) {
			ts.queue.StartTransfer(taskID) // Idempotent transition to Active
			ts.queue.UpdateProgress(taskID, progress)
		},
		TransferHandle: transferHandle,
	})

	if err != nil {
		ts.queue.Fail(taskID, err)
		ts.logger.Error().Err(err).Str("file_id", req.Source).Str("name", fileName).Msg("Download failed")
	} else {
		ts.queue.Complete(taskID)
		ts.logger.Info().Str("file_id", req.Source).Str("local_path", req.Dest).Msg("File downloaded")
	}
}

// ExecuteRetry implements transfer.RetryExecutor.
// Called by the queue when a user requests retry on a failed task.
func (ts *TransferService) ExecuteRetry(task *transfer.TransferTask) {
	ts.mu.RLock()
	apiClient := ts.apiClient
	ts.mu.RUnlock()

	if apiClient == nil {
		ts.queue.Fail(task.ID, fmt.Errorf("API client not configured"))
		return
	}

	ctx := context.Background()

	if task.Type == transfer.TaskTypeUpload {
		req := TransferRequest{
			Type:   TransferTypeUpload,
			Source: task.Source,
			Dest:   task.Dest,
			Name:   task.Name,
			Size:   task.Size,
		}
		ts.executeUploadRetry(ctx, req, task.ID, apiClient)
	} else {
		req := TransferRequest{
			Type:   TransferTypeDownload,
			Source: task.Source,
			Dest:   task.Dest,
			Name:   task.Name,
			Size:   task.Size,
		}
		ts.executeDownloadRetry(ctx, req, task.ID, apiClient)
	}
}

// executeUploadRetry is like executeUpload but uses an existing task ID.
func (ts *TransferService) executeUploadRetry(ctx context.Context, req TransferRequest, taskID string, apiClient *api.Client) {
	fileName := req.Name

	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			ts.logger.Error().Msgf("PANIC in upload retry for %s: %v", fileName, r)
			ts.queue.Fail(taskID, fmt.Errorf("panic: %v", r))
		}
	}()

	// Wait for semaphore slot
	select {
	case ts.semaphore <- struct{}{}:
	case <-ctx.Done():
		ts.queue.Fail(taskID, ctx.Err())
		return
	}
	atomic.AddInt32(&ts.activeSlots, 1)

	defer func() {
		<-ts.semaphore
		atomic.AddInt32(&ts.activeSlots, -1)
	}()

	ts.queue.Activate(taskID)

	fileInfo, err := os.Stat(req.Source)
	if err != nil {
		ts.queue.Fail(taskID, fmt.Errorf("failed to stat file: %w", err))
		return
	}

	transferHandle := ts.transferMgr.AllocateTransfer(fileInfo.Size(), 1)

	_, err = upload.UploadFile(ctx, upload.UploadParams{
		LocalPath: req.Source,
		FolderID:  req.Dest,
		APIClient: apiClient,
		ProgressCallback: func(progress float64) {
			ts.queue.StartTransfer(taskID)
			ts.queue.UpdateProgress(taskID, progress)
		},
		TransferHandle: transferHandle,
	})

	if err != nil {
		ts.queue.Fail(taskID, err)
	} else {
		ts.queue.Complete(taskID)
	}
}

// executeDownloadRetry is like executeDownload but uses an existing task ID.
func (ts *TransferService) executeDownloadRetry(ctx context.Context, req TransferRequest, taskID string, apiClient *api.Client) {
	// v4.5.9: Consistent empty-name fallback (matches first-attempt path)
	fileName := req.Name
	if fileName == "" {
		fileName = req.Source
		ts.logger.Warn().
			Str("file_id", req.Source).
			Msg("Retry: req.Name is empty, using file ID as filename")
	}

	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			ts.logger.Error().Msgf("PANIC in download retry for %s: %v", fileName, r)
			ts.queue.Fail(taskID, fmt.Errorf("panic: %v", r))
		}
	}()

	// Wait for semaphore slot
	select {
	case ts.semaphore <- struct{}{}:
	case <-ctx.Done():
		ts.queue.Fail(taskID, ctx.Err())
		return
	}
	atomic.AddInt32(&ts.activeSlots, 1)

	defer func() {
		<-ts.semaphore
		atomic.AddInt32(&ts.activeSlots, -1)
	}()

	ts.queue.Activate(taskID)

	transferHandle := ts.transferMgr.AllocateTransfer(req.Size, 1)

	// v4.5.9: Normalize dest-is-directory in retry path (matching first-attempt path).
	// Without this, retrying a download to a directory dest would create a file named
	// after the directory itself instead of appending the filename.

	localPath := req.Dest
	if info, err := os.Stat(localPath); err == nil && info.IsDir() {
		localPath = filepath.Join(localPath, fileName)
		ts.logger.Debug().
			Str("original_dest", req.Dest).
			Str("corrected_path", localPath).
			Msg("Retry: Dest was a directory, appending filename")
	}

	err := download.DownloadFile(ctx, download.DownloadParams{
		FileID:    req.Source,
		LocalPath: localPath,
		APIClient: apiClient,
		ProgressCallback: func(progress float64) {
			ts.queue.StartTransfer(taskID)
			ts.queue.UpdateProgress(taskID, progress)
		},
		TransferHandle: transferHandle,
	})

	if err != nil {
		ts.queue.Fail(taskID, err)
	} else {
		ts.queue.Complete(taskID)
	}
}

// CancelTransfer cancels an active transfer.
func (ts *TransferService) CancelTransfer(taskID string) error {
	return ts.queue.Cancel(taskID)
}

// CancelAll cancels all active transfers.
func (ts *TransferService) CancelAll() {
	ts.queue.CancelAll()
}

// RetryTransfer retries a failed or cancelled transfer.
func (ts *TransferService) RetryTransfer(taskID string) (string, error) {
	return ts.queue.Retry(taskID)
}

// GetStats returns current transfer statistics.
func (ts *TransferService) GetStats() TransferStats {
	qStats := ts.queue.GetStats()
	return TransferStats{
		Queued:       qStats.Queued,
		Initializing: qStats.Initializing,
		Active:       qStats.Active,
		Paused:       qStats.Paused,
		Completed:    qStats.Completed,
		Failed:       qStats.Failed,
		Cancelled:    qStats.Cancelled,
	}
}

// GetTasks returns all tracked transfers.
func (ts *TransferService) GetTasks() []TransferTask {
	qTasks := ts.queue.GetTasks()
	tasks := make([]TransferTask, len(qTasks))
	// v4.0.0: Use index-based access to avoid copying mutex in range variable
	for i := range qTasks {
		qt := &qTasks[i]
		tasks[i] = TransferTask{
			ID:          qt.ID,
			Type:        TransferType(qt.Type),
			State:       TransferState(qt.State),
			Name:        qt.Name,
			Source:      qt.Source,
			Dest:        qt.Dest,
			Size:        qt.Size,
			Progress:    qt.Progress,
			Speed:       qt.Speed,
			Error:       qt.Error,
			CreatedAt:   qt.CreatedAt,
			StartedAt:   qt.StartedAt,
			CompletedAt: qt.CompletedAt,
		}
	}
	return tasks
}

// ClearCompleted removes completed/failed/cancelled transfers from tracking.
func (ts *TransferService) ClearCompleted() {
	ts.queue.ClearCompleted()
}
