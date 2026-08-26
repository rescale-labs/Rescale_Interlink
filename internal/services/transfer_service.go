// Package services provides frontend-agnostic business logic for Rescale Interlink.
package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/events"
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/ratelimit"
	"github.com/rescale/rescale-int/internal/reporting"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/util/tags"
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

func NewTransferService(apiClient *api.Client, eventBus *events.EventBus, config TransferServiceConfig) *TransferService {
	if config.MaxConcurrent <= 0 {
		// Default to MaxMaxConcurrent (20) as the global cap.
		// Per-batch, adaptive concurrency selects the actual worker count.
		config.MaxConcurrent = constants.MaxMaxConcurrent
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
// retryNotice surfaces a storage retry on the Activity log. This path has no
// progress bar to label, so the event bus is the only place a GUI user can see
// that a transfer is struggling rather than stuck. label names the file, since
// the provider only knows its own operation ("UploadPart 3").
func (ts *TransferService) retryNotice(label string) func(cloud.RetryEvent) {
	return func(ev cloud.RetryEvent) {
		msg := ev.Notice()
		if msg == "" || ts.eventBus == nil {
			return
		}
		ts.eventBus.PublishLog(events.WarnLevel, fmt.Sprintf("%s: %s", label, msg), "transfer", "", nil)
	}
}

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
// preRegItem holds a request and its pre-registered task ID.
// Implements transfer.WorkItem for BatchExecutor compatibility.
type preRegItem struct {
	req    TransferRequest
	taskID string
}

// FileSize implements transfer.WorkItem.
func (p preRegItem) FileSize() int64 { return p.req.Size }

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

	// Synchronous proxy warmup before first API call
	inthttp.WarmupProxyIfNeeded(ctx, apiClient.GetConfig())

	// Warm credential cache in background to avoid blocking downloads.
	// NOTE: StartTransfers is called by transfer_bindings.go (GUI single-file transfers
	// from FileBrowser) which does NOT pre-warm — keep async warm as safety net.
	go ts.WarmCredentialCache(ctx)

	// Pre-register ALL tasks synchronously before launching async workers.
	// This ensures tasks are visible in the queue before StartTransfers() returns.
	var uploadItems, downloadItems []preRegItem
	for _, req := range requests {
		if req.Type == TransferTypeUpload {
			taskID := ts.registerUploadTask(req)
			uploadItems = append(uploadItems, preRegItem{req: req, taskID: taskID})
		} else {
			taskID := ts.registerDownloadTask(req)
			downloadItems = append(downloadItems, preRegItem{req: req, taskID: taskID})
		}
	}

	// Launch workers async (tasks already in queue)
	if len(uploadItems) > 0 {
		go ts.executePreRegisteredBatch(ctx, uploadItems, ts.uploadDirection())
	}
	if len(downloadItems) > 0 {
		go ts.executePreRegisteredBatch(ctx, downloadItems, ts.downloadDirection())
	}

	return nil
}

// WarmCredentialCache pre-warms the credential cache.
// Exported so callers (file_bindings.go) can invoke it synchronously
// before scan/download to eliminate credential lock contention on first download.
func (ts *TransferService) WarmCredentialCache(ctx context.Context) {
	// Best-effort async proxy warmup (redundant with synchronous warmup, harmless)
	ts.mu.RLock()
	if ts.apiClient != nil {
		inthttp.WarmupProxyIfNeeded(ctx, ts.apiClient.GetConfig())
	}
	ts.mu.RUnlock()

	ts.mu.Lock()
	if ts.credManager == nil && ts.apiClient != nil {
		ts.credManager = credentials.GetManager(ts.apiClient)
	}
	credManager := ts.credManager
	ts.mu.Unlock()

	if credManager != nil {
		credManager.WarmAll(ctx)
	}
}

// transferDirection carries what differs between the upload and download halves
// of the batch and task paths: the words the queue, the logs and the batch
// reporting use, and the per-file work itself. Everything else on those paths is
// shared.
type transferDirection struct {
	// name is the direction as the queue and the batch reporting spell it.
	name string

	// label is the direction as the [SLOT] and [BATCH] log lines spell it.
	label string

	// batchAbortMessage is logged when a batch has no API client to run against.
	batchAbortMessage string

	// register puts one request in the queue and returns its task ID.
	register func(req TransferRequest) string

	// fileName is the display name this direction gives a request in logs.
	fileName func(req TransferRequest) string

	// logSlotAcquired and logCredentialsChecked add a direction's own timing
	// line at those two points of the task lifecycle. Only download has them.
	logSlotAcquired       func(fileName string)
	logCredentialsChecked func(fileName string)

	// run performs the transfer once the shared lifecycle holds a semaphore
	// slot, has activated the task and has checked credentials.
	run func(x taskExecution)
}

func (ts *TransferService) uploadDirection() transferDirection {
	return transferDirection{
		name:              "upload",
		label:             "UPLOAD",
		batchAbortMessage: "Upload batch aborted: no API client",
		register:          ts.registerUploadTask,
		fileName: func(req TransferRequest) string {
			if req.Name != "" {
				return req.Name
			}
			return filepath.Base(req.Source)
		},
		run: ts.runUpload,
	}
}

func (ts *TransferService) downloadDirection() transferDirection {
	return transferDirection{
		name:              "download",
		label:             "DOWNLOAD",
		batchAbortMessage: "Download batch aborted: no API client",
		register:          ts.registerDownloadTask,
		fileName: func(req TransferRequest) string {
			if req.Name != "" {
				return req.Name
			}
			return req.Source
		},
		logSlotAcquired: func(fileName string) {
			log.Printf("[TIMING] DOWNLOAD %s: semaphore acquired, starting credential check", fileName)
		},
		logCredentialsChecked: func(fileName string) {
			log.Printf("[TIMING] DOWNLOAD %s: credential check complete, starting download", fileName)
		},
		run: ts.runDownload,
	}
}

// executePreRegisteredBatch dispatches pre-registered tasks to workers.
func (ts *TransferService) executePreRegisteredBatch(ctx context.Context, items []preRegItem, dir transferDirection) {
	ts.mu.RLock()
	apiClient := ts.apiClient
	ts.mu.RUnlock()

	if apiClient == nil {
		ts.logger.Error().Msg(dir.batchAbortMessage)
		return
	}

	cfg := transfer.BatchConfig{
		MaxWorkers:  cap(ts.semaphore),
		ResourceMgr: ts.resourceMgr,
		Label:       dir.label,
	}

	numWorkers := transfer.ComputedWorkers(items, cfg)

	transfer.RunBatch(ctx, items, cfg, func(ctx context.Context, item preRegItem) error {
		ts.executeTask(ctx, item.req, item.taskID, apiClient, numWorkers, dir)
		return nil // errors handled internally via queue.Fail
	})

	// Check for batch failures across all batch IDs in this execution.
	seen := make(map[string]bool)
	for _, item := range items {
		bid := item.req.BatchID
		if bid != "" && !seen[bid] {
			seen[bid] = true
			ts.checkBatchCompletion(bid, dir.name)
		}
	}
}

// StartStreamingDownloadBatch accepts a channel of TransferRequest and registers+dispatches
// them incrementally as they arrive. Downloads begin within seconds of scan start.
// The batchCtx is cancelled when CancelBatch is called (via batchCancelFuncs in queue).
// cancelFn parameter for atomic cancel registration — if non-nil, registered as
// the batch cancel function so CancelBatch() cancels the caller's context (which propagates
// to batchCtx). If nil, falls back to internal batchCancel.
func (ts *TransferService) StartStreamingDownloadBatch(
	ctx context.Context,
	requestCh <-chan TransferRequest,
	batchID, batchLabel, sourceLabel string,
	cancelFn context.CancelFunc,
) error {
	return ts.startStreamingBatch(ctx, requestCh, batchID, batchLabel, sourceLabel, cancelFn, ts.downloadDirection())
}

// StartStreamingUploadBatch accepts a channel of TransferRequest and registers+dispatches
// them incrementally as they arrive. Uploads begin as soon as their destination folder
// is created, without waiting for all folders to be created first.
// Symmetric to StartStreamingDownloadBatch. Used by pipelined folder upload.
func (ts *TransferService) StartStreamingUploadBatch(
	ctx context.Context,
	requestCh <-chan TransferRequest,
	batchID, batchLabel, sourceLabel string,
	cancelFn context.CancelFunc,
) error {
	return ts.startStreamingBatch(ctx, requestCh, batchID, batchLabel, sourceLabel, cancelFn, ts.uploadDirection())
}

// startStreamingBatch registers requests as they arrive on requestCh and
// dispatches each one as soon as it is registered, so transfers start while the
// caller is still producing requests.
func (ts *TransferService) startStreamingBatch(
	ctx context.Context,
	requestCh <-chan TransferRequest,
	batchID, batchLabel, sourceLabel string,
	cancelFn context.CancelFunc,
	dir transferDirection,
) error {
	ts.mu.RLock()
	apiClient := ts.apiClient
	ts.mu.RUnlock()

	if apiClient == nil {
		return fmt.Errorf("API client not configured")
	}

	// Create a cancellable batch context — CancelBatch() will cancel this
	batchCtx, batchCancel := context.WithCancel(ctx)

	// Register caller-provided cancelFn if given (cancels the caller's context
	// → batchCtx). This eliminates the race window between RegisterBatchCancel
	// and any external override.
	if cancelFn != nil {
		ts.queue.RegisterBatchCancel(batchID, cancelFn)
	} else {
		ts.queue.RegisterBatchCancel(batchID, batchCancel)
	}

	// PreRegisterBatch gives the UI something to show immediately, and
	// implicitly marks scan-in-progress so BatchStats reports TotalKnown=false
	// until registration finishes. Paired with MarkBatchScanInProgress(false)
	// in the registration goroutine — this is what WaitForBatch polls on.
	ts.queue.PreRegisterBatch(batchID, batchLabel, dir.name, sourceLabel)

	// Dispatch channel: registration goroutine → RunBatchFromChannel
	dispatchCh := make(chan preRegItem, constants.DispatchChannelBuffer)

	// Registration goroutine: reads from requestCh, registers tasks, sends to dispatch.
	// Lifecycle invariants preserved: RegisterBatchCancel, CleanupBatch, cancel propagation.
	go func() {
		defer close(dispatchCh)
		defer ts.queue.CleanupBatch(batchID)
		// Registration finished: flip TotalKnown=true so WaitForBatch callers
		// know the task count is final. Must fire before CleanupBatch. (The
		// GUI's OnOrchestratorDone also clears this, but the cancel and CLI
		// paths do not.)
		defer ts.queue.MarkBatchScanInProgress(batchID, false)

		for {
			select {
			case <-batchCtx.Done():
				return
			case req, ok := <-requestCh:
				if !ok {
					return // Channel closed — caller has queued everything
				}
				taskID := dir.register(req)
				select {
				case dispatchCh <- preRegItem{req: req, taskID: taskID}:
				case <-batchCtx.Done():
					return
				}
			}
		}
	}()

	// Worker goroutines via RunBatchFromChannel: adaptive concurrency from file sizes.
	var adaptive *transfer.AdaptiveWorkerCount
	cfg := transfer.BatchConfig{
		MaxWorkers:    cap(ts.semaphore),
		ResourceMgr:   ts.resourceMgr,
		Label:         dir.label + "-STREAM",
		AdaptiveCount: &adaptive,
	}

	go func() {
		defer batchCancel() // Ensure batchCtx resources are released when batch completes
		transfer.RunBatchFromChannel(batchCtx, dispatchCh, cfg, func(ctx context.Context, item preRegItem) error {
			wc := constants.DefaultMaxConcurrent
			if adaptive != nil {
				wc = adaptive.Load()
			}
			if wc < 1 {
				wc = 1
			}
			ts.executeTask(ctx, item.req, item.taskID, apiClient, wc, dir)
			return nil // errors handled internally via queue.Fail
		})
		log.Printf("[BATCH] Streaming %s batch complete: %s", dir.label, batchID)

		ts.checkBatchCompletion(batchID, dir.name)
	}()

	return nil
}

// RegisterSkipPlaceholderTask creates a single completed synthetic task to anchor
// a folder-upload batch whose walker skipped every entry (e.g. a Windows folder
// of junctions). Without this anchor, CleanupBatch would delete the pre-registered
// metadata and the Transfers row would vanish, hiding the upload transaction
// from the user.
//
// The placeholder task carries Size=0 and a name like "(skipped: Public)" so it
// renders distinctly from a real file transfer. Frontend code keys off Skipped>0
// and Total==1 to render the appropriate "0 files uploaded — N items skipped"
// summary.
func (ts *TransferService) RegisterSkipPlaceholderTask(batchID, displayName string, skipCount int) {
	ts.registerPlaceholderTask(batchID, displayName, fmt.Sprintf("(skipped: %s)", displayName), transfer.TaskTypeUpload)
}

// RegisterEmptyBatchPlaceholder anchors a streaming batch that completed with zero
// real tasks and no error — e.g. downloading a remote folder that contains no files,
// or uploading an empty local folder. Without an anchor, CleanupBatch deletes the
// pre-registered metadata and the Transfers row vanishes, so the user loses the record
// of a transfer that did happen (the operation succeeded; there was simply nothing to
// move). Every transfer, in either direction, should leave a persistent record.
//
// The placeholder carries Size=0 and the name "(no files)" so the frontend can render
// a distinct "No files to transfer" summary instead of a phantom file row. direction
// determines whether the row reads as an upload or a download.
func (ts *TransferService) RegisterEmptyBatchPlaceholder(batchID, displayName, direction string) {
	taskType := transfer.TaskTypeDownload
	if direction == "upload" {
		taskType = transfer.TaskTypeUpload
	}
	ts.registerPlaceholderTask(batchID, displayName, "(no files)", taskType)
}

// registerPlaceholderTask creates a single completed synthetic task to anchor a batch
// whose pre-registered metadata would otherwise be removed by CleanupBatch, leaving no
// trace in the Transfers tab. Shared by the skip-only and empty-batch cases.
func (ts *TransferService) registerPlaceholderTask(batchID, displayName, taskName string, taskType transfer.TaskType) {
	if batchID == "" {
		return
	}
	task := ts.queue.TrackTransferWithBatch(
		taskName,
		0,
		taskType,
		"",
		"",
		SourceLabelFileBrowser,
		batchID,
		displayName,
	)
	ts.queue.Complete(task.ID)
}

// registerUploadTask registers an upload task in the queue (starts as Queued).
// Returns the task ID. No context or cancel fn is set — that happens in executeUploadTask.
func (ts *TransferService) registerUploadTask(req TransferRequest) string {
	fileName := req.Name
	if fileName == "" {
		fileName = filepath.Base(req.Source)
	}

	sourceLabel := req.SourceLabel
	if sourceLabel == "" {
		sourceLabel = SourceLabelFileBrowser
	}

	var task *transfer.TransferTask
	if req.BatchID != "" {
		task = ts.queue.TrackTransferWithBatch(fileName, req.Size, transfer.TaskTypeUpload, req.Source, req.Dest, sourceLabel, req.BatchID, req.BatchLabel)
	} else {
		task = ts.queue.TrackTransferWithLabel(fileName, req.Size, transfer.TaskTypeUpload, req.Source, req.Dest, sourceLabel)
	}
	if len(req.Tags) > 0 {
		ts.queue.SetTaskTags(task.ID, req.Tags)
	}
	return task.ID
}

// taskExecution is the state a direction's run func gets once the shared task
// lifecycle has claimed a semaphore slot and activated the task.
type taskExecution struct {
	// ctx is the caller's context and taskCtx the cancellable one this task's
	// cancel fn is bound to. Work that must survive a cancel of the task
	// itself — applying tags to a file that already finished uploading — uses
	// ctx; the transfer uses taskCtx.
	ctx     context.Context
	taskCtx context.Context

	req         TransferRequest
	taskID      string
	fileName    string
	apiClient   *api.Client
	workerCount int
}

// executeTask runs an already-registered task in the given direction.
// Handles semaphore acquisition, atomic claim via Activate(), cancel cleanup,
// and ensures every early-return path after SetCancel() transitions the task
// to a terminal state if it isn't already terminal. The direction's run func
// performs the transfer itself and owns the terminal transition from there on.
func (ts *TransferService) executeTask(ctx context.Context, req TransferRequest, taskID string, apiClient *api.Client, workerCount int, dir transferDirection) {
	fileName := dir.fileName(req)

	// Create derived context for cancel support
	taskCtx, taskCancel := context.WithCancel(ctx)
	defer taskCancel()

	// Set cancel fn early — enables CancelBatch to cancel even while queued
	ts.queue.SetCancel(taskID, taskCancel)

	// Panic recovery — must transition to terminal after SetCancel
	defer func() {
		if r := recover(); r != nil {
			ts.logger.Error().Msgf("PANIC in %s for %s: %v", dir.name, fileName, r)
			ts.queue.FailIfNotTerminal(taskID, fmt.Errorf("panic: %v", r))
		}
	}()

	// Wait for semaphore slot
	slotsBefore := atomic.LoadInt32(&ts.activeSlots)
	log.Printf("[SLOT] %s %s: waiting (active=%d/%d)", dir.label, fileName, slotsBefore, cap(ts.semaphore))

	select {
	case ts.semaphore <- struct{}{}:
		// Acquired slot
	case <-taskCtx.Done():
		// Atomic terminal transition — CancelBatch may have set TaskCancelled,
		// but other cancellations (parent timeout, shutdown) won't.
		ts.queue.FailIfNotTerminal(taskID, taskCtx.Err())
		return
	}

	slotsNow := atomic.AddInt32(&ts.activeSlots, 1)
	log.Printf("[SLOT] %s %s: ACQUIRED (active=%d/%d)", dir.label, fileName, slotsNow, cap(ts.semaphore))
	if dir.logSlotAcquired != nil {
		dir.logSlotAcquired(fileName)
	}

	defer func() {
		<-ts.semaphore
		slotsAfter := atomic.AddInt32(&ts.activeSlots, -1)
		log.Printf("[SLOT] %s %s: RELEASED (active=%d/%d)", dir.label, fileName, slotsAfter, cap(ts.semaphore))
	}()

	// Atomic claim: only transition TaskQueued → TaskInitializing
	if !ts.queue.Activate(taskID) {
		// Task already terminal (e.g., CancelBatch ran while we waited for semaphore)
		ts.queue.ClearCancel(taskID)
		return
	}

	// Check cancellation after activation
	select {
	case <-taskCtx.Done():
		ts.queue.FailIfNotTerminal(taskID, taskCtx.Err())
		return
	default:
	}

	// Proactive credential freshness check before the transfer.
	// Lazy-init credManager synchronously if warmCredentialCache hasn't run yet.
	ts.mu.Lock()
	if ts.credManager == nil && ts.apiClient != nil {
		ts.credManager = credentials.GetManager(ts.apiClient)
	}
	cm := ts.credManager
	ts.mu.Unlock()
	if cm != nil {
		if err := cm.EnsureFresh(taskCtx); err != nil {
			log.Printf("[CRED] EnsureFresh failed before %s %s: %v", dir.name, fileName, err)
			// Non-fatal: retry path will handle credential refresh on failure
		}
	}
	if dir.logCredentialsChecked != nil {
		dir.logCredentialsChecked(fileName)
	}

	dir.run(taskExecution{
		ctx:         ctx,
		taskCtx:     taskCtx,
		req:         req,
		taskID:      taskID,
		fileName:    fileName,
		apiClient:   apiClient,
		workerCount: workerCount,
	})
}

// runUpload uploads one file for a task the shared lifecycle has already
// activated. It owns the task's terminal transition.
func (ts *TransferService) runUpload(x taskExecution) {
	// Get file info for transfer allocation
	fileInfo, err := os.Stat(x.req.Source)
	if err != nil {
		ts.queue.Fail(x.taskID, fmt.Errorf("failed to stat file: %w", err))
		return
	}

	// Allocate transfer handle — workerCount is the adaptive batch size so
	// resource manager divides the thread pool correctly across concurrent workers.
	transferHandle := ts.transferMgr.AllocateTransfer(fileInfo.Size(), x.workerCount)
	defer transferHandle.Complete()

	// Execute upload with progress callback
	cloudFile, err := upload.UploadFile(x.taskCtx, upload.UploadParams{
		LocalPath: x.req.Source,
		FolderID:  x.req.Dest,
		APIClient: x.apiClient,
		ProgressCallback: func(progress float64) {
			ts.queue.StartTransfer(x.taskID)
			ts.queue.UpdateProgress(x.taskID, progress)
		},
		OnRetry:        ts.retryNotice(filepath.Base(x.req.Source)),
		TransferHandle: transferHandle,
	})

	if err != nil {
		// Don't overwrite cancelled state with failed
		if errors.Is(err, context.Canceled) {
			ts.queue.FailIfNotTerminal(x.taskID, err)
			return
		}
		ts.queue.Fail(x.taskID, err)
		ts.logger.Error().Err(err).Str("path", x.req.Source).Msg("Upload failed")
		return
	}

	// Apply tags after successful upload (non-fatal)
	ts.applyTags(x.ctx, x.apiClient, cloudFile.ID, x.req.Tags, x.fileName)

	ts.queue.Complete(x.taskID)
	ts.logger.Info().Str("path", x.req.Source).Msg("File uploaded")
}

// UploadFileSync uploads a file synchronously with transfer queue visibility.
// Blocks until the upload completes and returns the result.
//
// Transfer handle ownership:
//   - If params.TransferHandle is provided: used for upload, NOT completed (caller owns)
//   - If params.TransferHandle is nil: allocated internally and completed after upload
func (ts *TransferService) UploadFileSync(ctx context.Context, req TransferRequest, params UploadFileSyncParams) (*models.CloudFile, error) {
	ts.mu.RLock()
	apiClient := ts.apiClient
	ts.mu.RUnlock()

	if apiClient == nil {
		return nil, fmt.Errorf("API client not configured")
	}

	fileName := req.Name
	if fileName == "" {
		fileName = filepath.Base(req.Source)
	}

	sourceLabel := req.SourceLabel
	if sourceLabel == "" {
		sourceLabel = SourceLabelFileBrowser
	}

	// Track in queue (immediately visible in Transfers tab)
	var task *transfer.TransferTask
	if req.BatchID != "" {
		task = ts.queue.TrackTransferWithBatch(fileName, req.Size, transfer.TaskTypeUpload, req.Source, req.Dest, sourceLabel, req.BatchID, req.BatchLabel)
	} else {
		task = ts.queue.TrackTransferWithLabel(fileName, req.Size, transfer.TaskTypeUpload, req.Source, req.Dest, sourceLabel)
	}
	taskID := task.ID
	if len(req.Tags) > 0 {
		ts.queue.SetTaskTags(taskID, req.Tags)
	}

	// Create derived context for cancel support
	uploadCtx, uploadCancel := context.WithCancel(ctx)
	defer uploadCancel()
	ts.queue.SetCancel(taskID, uploadCancel)

	// Acquire semaphore slot (unified concurrency with File Browser)
	select {
	case ts.semaphore <- struct{}{}:
	case <-uploadCtx.Done():
		ts.queue.FailIfNotTerminal(taskID, uploadCtx.Err())
		return nil, uploadCtx.Err()
	}
	atomic.AddInt32(&ts.activeSlots, 1)

	// Signal active transfer for sleep inhibition + coordinator keepalive.
	// UploadFileSync bypasses RunBatch/RunBatchFromChannel, so must signal directly.
	ratelimit.GlobalStore().BeginTransferActivity()
	defer ratelimit.GlobalStore().EndTransferActivity()

	defer func() {
		<-ts.semaphore
		atomic.AddInt32(&ts.activeSlots, -1)
	}()

	if !ts.queue.Activate(taskID) {
		ts.queue.ClearCancel(taskID)
		if err := uploadCtx.Err(); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}

	// Get file info for transfer handle allocation
	fileInfo, err := os.Stat(req.Source)
	if err != nil {
		ts.queue.Fail(taskID, fmt.Errorf("failed to stat file: %w", err))
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// Update task size from actual file (caller may not have passed it)
	if fileInfo.Size() > 0 {
		ts.queue.UpdateSize(taskID, fileInfo.Size())
	}

	// Transfer handle: allocate internally (UploadFile requires *transfer.Transfer).
	// If the caller also has a handle, it manages its own lifecycle independently.
	transferHandle := ts.transferMgr.AllocateTransfer(fileInfo.Size(), 1)
	defer transferHandle.Complete()

	// Dual progress callback: queue + external
	progressCallback := func(progress float64) {
		ts.queue.StartTransfer(taskID) // Idempotent
		ts.queue.UpdateProgress(taskID, progress)
		if params.ExtraProgressCallback != nil {
			params.ExtraProgressCallback(progress)
		}
	}

	cloudFile, err := upload.UploadFile(uploadCtx, upload.UploadParams{
		LocalPath:        req.Source,
		FolderID:         req.Dest,
		APIClient:        apiClient,
		ProgressCallback: progressCallback,
		OnRetry:          ts.retryNotice(filepath.Base(req.Source)),
		TransferHandle:   transferHandle,
	})

	if err != nil {
		if errors.Is(err, context.Canceled) {
			ts.queue.FailIfNotTerminal(taskID, err)
			return nil, err
		}
		ts.queue.Fail(taskID, err)
		return nil, err
	}

	// Apply tags after successful upload (non-fatal)
	ts.applyTags(ctx, apiClient, cloudFile.ID, req.Tags, fileName)

	ts.queue.Complete(taskID)
	return cloudFile, nil
}

// applyTags applies tags to a file after upload. Failures are logged as warnings.
func (ts *TransferService) applyTags(ctx context.Context, apiClient *api.Client, fileID string, rawTags []string, fileName string) {
	normalized := tags.NormalizeTags(rawTags)
	if len(normalized) == 0 {
		return
	}
	if err := apiClient.AddFileTags(ctx, fileID, normalized); err != nil {
		ts.logger.Warn().Err(err).
			Str("file", fileName).
			Str("fileID", fileID).
			Strs("tags", normalized).
			Msg("Failed to apply tags after upload (non-fatal)")
	} else {
		ts.logger.Info().
			Str("file", fileName).
			Strs("tags", normalized).
			Msg("Tags applied after upload")
	}
}

// registerDownloadTask registers a download task in the queue (starts as Queued).
// Returns the task ID. No context or cancel fn is set — that happens in executeDownloadTask.
func (ts *TransferService) registerDownloadTask(req TransferRequest) string {
	fileName := req.Name
	if fileName == "" {
		fileName = req.Source
		ts.logger.Warn().
			Str("file_id", req.Source).
			Msg("Download: req.Name is empty, using file ID as filename")
	}

	sourceLabel := req.SourceLabel
	if sourceLabel == "" {
		sourceLabel = SourceLabelFileBrowser
	}

	var task *transfer.TransferTask
	if req.BatchID != "" {
		task = ts.queue.TrackTransferWithBatch(fileName, req.Size, transfer.TaskTypeDownload, req.Source, req.Dest, sourceLabel, req.BatchID, req.BatchLabel)
	} else {
		task = ts.queue.TrackTransferWithLabel(fileName, req.Size, transfer.TaskTypeDownload, req.Source, req.Dest, sourceLabel)
	}
	return task.ID
}

// runDownload downloads one file for a task the shared lifecycle has already
// activated. It owns the task's terminal transition.
func (ts *TransferService) runDownload(x taskExecution) {
	// Allocate transfer handle — workerCount is the adaptive batch size so
	// resource manager divides the thread pool correctly across concurrent workers.
	transferHandle := ts.transferMgr.AllocateTransfer(x.req.Size, x.workerCount)
	defer transferHandle.Complete()

	// Ensure dest is a file path, not a directory
	localPath := x.req.Dest
	if info, err := os.Stat(localPath); err == nil && info.IsDir() {
		localPath = filepath.Join(localPath, x.fileName)
		ts.logger.Debug().
			Str("original_dest", x.req.Dest).
			Str("corrected_path", localPath).
			Msg("Dest was a directory, appending filename")
	}

	// Execute download with progress callback.
	// Pass pre-fetched FileInfo to skip GetFileInfo() API call when available.
	log.Printf("[TIMING] DOWNLOAD %s: DownloadFile() entered, provider init starting", x.fileName)
	err := download.DownloadFile(x.taskCtx, download.DownloadParams{
		FileID:    x.req.Source,
		FileInfo:  x.req.FileInfo,
		LocalPath: localPath,
		APIClient: x.apiClient,
		OnRetry:   ts.retryNotice(x.fileName),
		ProgressCallback: func(progress float64) {
			ts.queue.StartTransfer(x.taskID)
			ts.queue.UpdateProgress(x.taskID, progress)
		},
		TransferHandle: transferHandle,
	})

	if err != nil {
		// Don't overwrite cancelled state with failed
		if errors.Is(err, context.Canceled) {
			ts.queue.FailIfNotTerminal(x.taskID, err)
			return
		}
		ts.queue.Fail(x.taskID, err)
		ts.logger.Error().Err(err).Str("file_id", x.req.Source).Str("name", x.fileName).Msg("Download failed")
	} else {
		ts.queue.Complete(x.taskID)
		ts.logger.Info().Str("file_id", x.req.Source).Str("local_path", x.req.Dest).Msg("File downloaded")
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

	// Signal active transfer for sleep inhibition + coordinator keepalive.
	// Retry runs outside RunBatch/RunBatchFromChannel, so must signal directly.
	ratelimit.GlobalStore().BeginTransferActivity()
	defer ratelimit.GlobalStore().EndTransferActivity()

	ctx := context.Background()

	if task.Type == transfer.TaskTypeUpload {
		req := TransferRequest{
			Type:   TransferTypeUpload,
			Source: task.Source,
			Dest:   task.Dest,
			Name:   task.Name,
			Size:   task.Size,
			// Tags live on the task precisely so a retry still applies them.
			Tags: task.GetTags(),
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

// executeUploadRetry delegates to executeTask with the existing task ID.
// The task was already reset to TaskQueued by queue.Retry().
// workerCount=1 — retry is single-file outside batch, gets full thread pool.
func (ts *TransferService) executeUploadRetry(ctx context.Context, req TransferRequest, taskID string, apiClient *api.Client) {
	ts.executeTask(ctx, req, taskID, apiClient, 1, ts.uploadDirection())
}

// executeDownloadRetry delegates to executeTask with the existing task ID.
// The task was already reset to TaskQueued by queue.Retry().
// workerCount=1 — retry is single-file outside batch, gets full thread pool.
func (ts *TransferService) executeDownloadRetry(ctx context.Context, req TransferRequest, taskID string, apiClient *api.Client) {
	ts.executeTask(ctx, req, taskID, apiClient, 1, ts.downloadDirection())
}

func (ts *TransferService) CancelTransfer(taskID string) error {
	return ts.queue.Cancel(taskID)
}

func (ts *TransferService) CancelAll() {
	ts.queue.CancelAll()
}

// CancelBatch cancels all non-terminal tasks in a specific batch. Mirrors
// the GUI's per-batch cancel action. Used by the daemon's IPC cancel
// handler and by daemon shutdown.
func (ts *TransferService) CancelBatch(batchID string) error {
	// Capture the batch's identity first: cancelling a batch whose scan hasn't
	// registered any task yet drops its pre-registered metadata, and with no
	// task to anchor it the Transfers tab would show no record at all.
	var label, direction, sourceLabel string
	hadRow := false
	for _, bs := range ts.queue.GetAllBatchStats() {
		if bs.BatchID == batchID {
			hadRow, label, direction, sourceLabel = true, bs.BatchLabel, bs.Direction, bs.SourceLabel
			break
		}
	}
	if sourceLabel == "" {
		sourceLabel = SourceLabelFileBrowser
	}

	err := ts.queue.CancelBatch(batchID)

	if hadRow {
		anchored := false
		for _, bs := range ts.queue.GetAllBatchStats() {
			if bs.BatchID == batchID {
				anchored = true
				break
			}
		}
		if !anchored {
			taskType := transfer.TaskTypeDownload
			if direction == "upload" {
				taskType = transfer.TaskTypeUpload
			}
			// The queue's cancelled-batch marker lands this placeholder as
			// TaskCancelled, so the row reads Cancelled — not Complete like
			// the empty-batch placeholder.
			ts.queue.TrackTransferWithBatch(
				"(cancelled during scan)", 0, taskType, "", "",
				sourceLabel, batchID, label,
			)
		}
	}
	return err
}

// RetryFailedInBatch retries all failed tasks in a batch. Mirrors the GUI
// retry-failed action. Used by the daemon's IPC retry handler.
func (ts *TransferService) RetryFailedInBatch(batchID string) error {
	return ts.queue.RetryFailedInBatch(batchID)
}

// ErrBatchVanished reports that a batch the caller was waiting on holds no
// tasks and never will: registration ended without registering any, which
// happens when the batch is cancelled before its first task lands. Only
// returned by WaitForRegisteredBatch, whose caller guarantees registration is
// finished — otherwise a missing batch is just one that hasn't started.
var ErrBatchVanished = errors.New("transfer batch is no longer registered and has no tasks")

// WaitForBatch blocks until the batch's registration phase is finished
// (TotalKnown=true) AND all its tasks are in terminal state, or until ctx
// is cancelled. Correctly handles the empty-batch case (Total=0 with
// TotalKnown=true returns immediately after registration closes) and
// registration gaps (WaitForBatch never returns while the registration
// goroutine is still running).
//
// Used by the daemon to know when a download batch is finished so it can
// decide OutcomeDownloaded vs. OutcomePartialFailure and apply the
// downloaded tag.
func (ts *TransferService) WaitForBatch(ctx context.Context, batchID string) (transfer.BatchStats, error) {
	return ts.waitForBatch(ctx, batchID, false)
}

// WaitForRegisteredBatch is WaitForBatch for a caller that has already handed
// over every request for the batch and closed its request channel. Under that
// guarantee a batch the queue cannot resolve cannot resolve into anything the
// caller should still wait for: the entry that makes an in-flight batch findable
// is dropped only when registration returns. A cancelled batch may still
// register a late task — the queue lands those as already-cancelled — so there
// is nothing to wait for either way, and this fails with ErrBatchVanished
// instead of polling on.
//
// Waiting without that guarantee must use WaitForBatch: a batch whose first
// task has not registered yet is legitimately unfindable for a moment.
func (ts *TransferService) WaitForRegisteredBatch(ctx context.Context, batchID string) (transfer.BatchStats, error) {
	return ts.waitForBatch(ctx, batchID, true)
}

func (ts *TransferService) waitForBatch(ctx context.Context, batchID string, failIfMissing bool) (transfer.BatchStats, error) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return transfer.BatchStats{}, ctx.Err()
		case <-t.C:
			bs, ok := ts.queue.GetBatchStats(batchID)
			if !ok {
				if failIfMissing {
					return transfer.BatchStats{}, fmt.Errorf("%w: %s", ErrBatchVanished, batchID)
				}
				continue // batch not yet registered, or already cleaned up
			}
			if !bs.TotalKnown {
				continue // registration still running
			}
			nonTerminal := bs.Queued + bs.Active
			if nonTerminal == 0 {
				return bs, nil
			}
		}
	}
}

func (ts *TransferService) RetryTransfer(taskID string) (string, error) {
	return ts.queue.Retry(taskID)
}

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

func (ts *TransferService) GetTasks() []TransferTask {
	qTasks := ts.queue.GetTasks()
	tasks := make([]TransferTask, len(qTasks))
	// Use index-based access to avoid copying mutex in range variable
	for i := range qTasks {
		tasks[i] = TaskFromQueueTask(&qTasks[i])
	}
	return tasks
}

// TaskFromQueueTask converts a queue task into its service-layer view.
// Takes a pointer because transfer.TransferTask embeds a sync.RWMutex that
// must not be copied.
func TaskFromQueueTask(qt *transfer.TransferTask) TransferTask {
	return TransferTask{
		ID:          qt.ID,
		Type:        TransferType(qt.Type),
		State:       TransferState(qt.State),
		Name:        qt.Name,
		Source:      qt.Source,
		Dest:        qt.Dest,
		Size:        qt.Size,
		SourceLabel: qt.SourceLabel,
		BatchID:     qt.BatchID,
		BatchLabel:  qt.BatchLabel,
		Progress:    qt.Progress,
		Speed:       qt.Speed,
		Error:       qt.Error,
		CreatedAt:   qt.CreatedAt,
		StartedAt:   qt.StartedAt,
		CompletedAt: qt.CompletedAt,
	}
}

// checkBatchCompletion checks a completed batch for failures and publishes appropriate reports.
func (ts *TransferService) checkBatchCompletion(batchID, direction string) {
	batches := ts.queue.GetAllBatchStats()
	for _, bs := range batches {
		if bs.BatchID != batchID {
			continue
		}
		if bs.CancelRequested {
			// The user stopped this whole batch. Whatever the surviving tasks
			// report, nothing here is a failure worth an error report. A
			// per-task cancel deliberately does not suppress: cancelling one
			// file must not hide two hundred real failures in the same batch.
			return
		}
		if bs.Failed == 0 || bs.Total == 0 {
			return // No failures
		}

		if bs.Completed == 0 {
			// Total wipeout. Report a real per-task error, not a synthetic
			// "N/N failed" summary: that summary always classified as
			// ClassInternal, so it defeated every suppression rule and raised
			// the report modal for batches that failed on a protected download
			// folder, a full disk, or a dead network.
			ts.reportTotalBatchFailure(batchID, direction)
		} else {
			// Partial failure: some succeeded, some failed.
			// Batch context proves infrastructure works — per-error classification (e.g., ClassNetwork)
			// would wrongly suppress this. Build a report with batch context + representative failure.
			ts.reportPartialBatchFailure(batchID, direction, bs)
		}
		return
	}
}

// dominantFailure samples a batch's failed tasks and returns the most common
// error class together with a representative message of that class.
// ok is false when no failed task recorded an error to reason about.
func (ts *TransferService) dominantFailure(batchID string) (cls reporting.ErrorClass, representative string, ok bool) {
	failedTasks := ts.queue.GetFailedTaskErrors(batchID, 5)
	if len(failedTasks) == 0 {
		return "", "", false
	}

	classCounts := make(map[reporting.ErrorClass]int)
	for _, errMsg := range failedTasks {
		classCounts[reporting.ClassifyErrorClass(errMsg)]++
	}
	var maxCount int
	for c, count := range classCounts {
		if count > maxCount {
			cls = c
			maxCount = count
		}
	}

	for _, errMsg := range failedTasks {
		if reporting.ClassifyErrorClass(errMsg) == cls {
			return cls, errMsg, true
		}
	}
	return cls, failedTasks[0], true
}

// reportTotalBatchFailure classifies a representative real error from a batch
// where every transfer failed, so the standard reportability rules see the
// original error markers and can suppress user-fixable causes.
func (ts *TransferService) reportTotalBatchFailure(batchID, direction string) {
	_, representative, ok := ts.dominantFailure(batchID)
	if !ok {
		return // Nothing concrete to report
	}
	reporting.ClassifyAndPublish(ts.eventBus, errors.New(representative),
		reporting.CategoryTransfer, "folder_"+direction, "")
}

// reportPartialBatchFailure inspects failed tasks in a partial-failure batch, determines the
// dominant error class, and publishes a ReportableErrorEvent when batch context contradicts
// the per-error classification (e.g., network errors in a mostly-successful batch).
func (ts *TransferService) reportPartialBatchFailure(batchID, direction string, bs transfer.BatchStats) {
	dominantClass, representativeErr, ok := ts.dominantFailure(batchID)
	if !ok {
		return
	}

	// GATE: Explicit handling per error class.
	switch dominantClass {
	case reporting.ClassNetwork, reporting.ClassTimeout:
		// Override suppression: batch context contradicts per-error classification.
		// Network/timeout errors are "user-fixable" per IsReportable, but most files
		// succeeded on the same network -> transient infrastructure issue -> publish.
		// (Falls through to publish below)

	case reporting.ClassAuth, reporting.ClassDiskSpace, reporting.ClassClientError, reporting.ClassLocalFS:
		// DO NOT publish. Batch context does NOT contradict these:
		// - Auth: bad credentials affect all files
		// - Disk: local condition, not infrastructure
		// - Client: 400/404 = bad input for specific files
		// - LocalFS: the user's own filesystem refused specific files
		return

	case reporting.ClassServerError, reporting.ClassInternal:
		// Use normal reporting path — these are already reportable via IsReportable.
		// Re-classify using the representative error so the classifier sees the original markers.
		reporting.ClassifyAndPublish(ts.eventBus, errors.New(representativeErr),
			reporting.CategoryTransfer, "folder_"+direction, "")
		return
	}

	// Build message with batch context + representative failure (for network/timeout override)
	msg := fmt.Sprintf("batch %s partial failure: %d/%d succeeded, %d failed; representative error: %s",
		direction, bs.Completed, bs.Total, bs.Failed, representativeErr)

	classified := reporting.Classify(errors.New(msg), reporting.CategoryTransfer, "folder_"+direction, "")
	if classified == nil {
		return
	}

	var timeline []events.SanitizedTimelineEntry
	if ts.eventBus != nil {
		raw := ts.eventBus.RecentEvents()
		timeline = reporting.RedactTimeline(raw, 20)
	}

	ts.eventBus.Publish(&events.ReportableErrorEvent{
		BaseEvent: events.BaseEvent{
			EventType: events.EventReportableError,
			Time:      time.Now(),
		},
		ErrorID:      classified.ErrorID,
		Category:     string(classified.Category),
		Severity:     string(classified.Severity),
		Operation:    classified.Operation,
		Backend:      classified.Backend,
		ErrorMessage: classified.ErrorMessage,
		ErrorClass:   string(classified.ErrorClass),
		Timeline:     timeline,
	})
}

// ClearCompleted removes completed/failed/cancelled transfers from tracking.
func (ts *TransferService) ClearCompleted() {
	ts.queue.ClearCompleted()
}

// ClearBatchTerminalTasks drops one batch's terminal tasks, leaving every other
// batch's history intact. Used by the daemon to bound its queue over a
// long-running process. Returns the number of tasks removed.
func (ts *TransferService) ClearBatchTerminalTasks(batchID string) int {
	return ts.queue.ClearBatchTerminalTasks(batchID)
}
