package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
)

// DirectoryMapping tracks local path to remote folder ID
type DirectoryMapping struct {
	LocalPath      string
	RemoteFolderID string
	Created        bool
}

// UploadTask represents a file to upload
type UploadTask struct {
	LocalPath      string
	RelativePath   string // Relative to root
	RemoteFolderID string
	Size           int64
}

// UploadResult tracks what happened during upload
type UploadResult struct {
	FoldersCreated  int
	FilesUploaded   int
	FilesSkipped    int
	FilesIgnored    int
	TotalBytes      int64
	Errors          []UploadError
	SymlinksSkipped []string
	UploadedFileIDs []string // v4.7.4: Collected file IDs for post-upload tagging
}

// UploadError tracks failed uploads
type UploadError struct {
	FilePath string
	Error    error
}

// FolderReadyEvent signals that a folder has been created and is ready for file uploads
type FolderReadyEvent struct {
	LocalPath string
	RemoteID  string
	Depth     int
}

// FolderCache caches folder contents to minimize API calls
type FolderCache struct {
	cache map[string]*api.FolderContents // folderID -> contents
	mu    sync.RWMutex
}

// NewFolderCache creates a new folder cache
func NewFolderCache() *FolderCache {
	return &FolderCache{
		cache: make(map[string]*api.FolderContents),
	}
}

// Get retrieves folder contents from cache or fetches from API if not cached
func (fc *FolderCache) Get(ctx context.Context, apiClient *api.Client, folderID string) (*api.FolderContents, error) {
	// Check cache first (read lock)
	fc.mu.RLock()
	if contents, ok := fc.cache[folderID]; ok {
		fc.mu.RUnlock()
		return contents, nil
	}
	fc.mu.RUnlock()

	// Not in cache, fetch it (write lock)
	fc.mu.Lock()
	defer fc.mu.Unlock()

	// Double-check (another goroutine might have fetched it)
	if contents, ok := fc.cache[folderID]; ok {
		return contents, nil
	}

	// Fetch from API (all pages — critical for folders with >2000 items)
	contents, err := apiClient.ListFolderContentsAll(ctx, folderID)
	if err != nil {
		return nil, err
	}

	fc.cache[folderID] = contents
	return contents, nil
}

// Invalidate removes a folder from the cache, forcing a fresh fetch on next Get.
// v4.0.8: Used to handle stale cache when folder creation fails with "already exists".
func (fc *FolderCache) Invalidate(folderID string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	delete(fc.cache, folderID)
}

// BuildDirectoryTree walks a local directory and returns lists of directories, files, and symlinks.
// This function is exported for use by the GUI.
//
// v4.0.4: Refactored to use localfs.WalkCollect() for North Star alignment
// (shared code between CLI and GUI for local filesystem operations).
// Returns string slices for backward compatibility with existing callers.
func BuildDirectoryTree(rootPath string, includeHidden bool) ([]string, []string, []string, error) {
	// Use shared localfs.WalkCollect() for core directory walking
	result, err := localfs.WalkCollect(rootPath, localfs.WalkOptions{
		IncludeHidden:  includeHidden,
		SkipHiddenDirs: true, // Skip hidden directories entirely
	})
	if err != nil {
		return nil, nil, nil, err
	}

	// Convert FileEntry slices to string slices for backward compatibility
	directories := make([]string, len(result.Directories))
	for i, entry := range result.Directories {
		directories[i] = entry.Path
	}

	files := make([]string, len(result.Files))
	for i, entry := range result.Files {
		files[i] = entry.Path
	}

	symlinks := make([]string, len(result.Symlinks))
	for i, entry := range result.Symlinks {
		symlinks[i] = entry.Path
	}

	return directories, files, symlinks, nil
}

// CheckFolderExists checks if a folder with the given name exists in the parent folder
// Exported for GUI reuse
func CheckFolderExists(ctx context.Context, apiClient *api.Client, cache *FolderCache, parentID, name string) (string, bool, error) {
	// Get contents from cache (will fetch from API if not cached)
	contents, err := cache.Get(ctx, apiClient, parentID)
	if err != nil {
		return "", false, err
	}

	// Look for folder with matching name
	for _, folder := range contents.Folders {
		if folder.Name == name {
			return folder.ID, true, nil
		}
	}

	return "", false, nil
}

// checkFileExists checks if a file with the given name exists in the folder
func checkFileExists(ctx context.Context, apiClient *api.Client, cache *FolderCache, folderID, fileName string) (string, bool, error) {
	// Get contents from cache (will fetch from API if not cached)
	contents, err := cache.Get(ctx, apiClient, folderID)
	if err != nil {
		return "", false, err
	}

	for _, file := range contents.Files {
		if file.Name == fileName {
			return file.ID, true, nil
		}
	}

	return "", false, nil
}

// processFolderParams groups the shared parameters for per-folder processing.
// v4.8.5: Extracted to enable code reuse between CreateFolderStructure and
// CreateFolderStructureStreaming (North Star: maximum code reuse).
type processFolderParams struct {
	ctx                context.Context
	apiClient          *api.Client
	cache              *FolderCache
	folderConflictMode *ConflictAction
	logger             *logging.Logger
	progressWriter     io.Writer
	folderReadyChan    chan<- FolderReadyEvent
	mapping            map[string]string // localPath → remoteID
	mappingMu          *sync.RWMutex
	foldersCreated     *int32 // atomic counter
}

// processFolder creates or merges a single folder, updating the mapping.
// v4.8.5: Shared helper between CreateFolderStructure and CreateFolderStructureStreaming.
// Returns (remoteID, created, error). On skip, returns ("", false, nil).
func processFolder(p processFolderParams, dirPath string) (string, bool, error) {
	parentPath := filepath.Dir(dirPath)
	p.mappingMu.RLock()
	parentRemoteID, ok := p.mapping[parentPath]
	p.mappingMu.RUnlock()

	if !ok {
		return "", false, fmt.Errorf("parent folder not created: %s", parentPath)
	}

	folderName := filepath.Base(dirPath)

	// Check if exists (uses cache)
	existingID, exists, err := CheckFolderExists(p.ctx, p.apiClient, p.cache, parentRemoteID, folderName)
	if err != nil {
		return "", false, fmt.Errorf("failed to check if folder exists: %w", err)
	}

	if exists {
		action := *p.folderConflictMode
		if action == ConflictSkipOnce || action == ConflictMergeOnce {
			action, err = promptFolderConflict(folderName)
			if err != nil {
				return "", false, err
			}
			if action == ConflictSkipAll || action == ConflictMergeAll {
				*p.folderConflictMode = action
			}
		}

		switch action {
		case ConflictSkipOnce, ConflictSkipAll:
			if p.progressWriter != nil {
				fmt.Fprintf(p.progressWriter, "  ⏭  Skipping existing folder: %s\n", folderName)
			}
			return "", false, nil // Skip — don't add to mapping
		case ConflictMergeOnce, ConflictMergeAll:
			if p.progressWriter != nil {
				fmt.Fprintf(p.progressWriter, "  ♻️  Using existing folder: %s\n", folderName)
			}
			p.mappingMu.Lock()
			p.mapping[dirPath] = existingID
			p.mappingMu.Unlock()

			if p.folderReadyChan != nil {
				depth := strings.Count(dirPath, string(os.PathSeparator))
				select {
				case p.folderReadyChan <- FolderReadyEvent{LocalPath: dirPath, RemoteID: existingID, Depth: depth}:
				case <-p.ctx.Done():
					return "", false, p.ctx.Err()
				}
			}
			return existingID, false, nil
		case ConflictAbort:
			return "", false, fmt.Errorf("upload aborted by user")
		}
	}

	// Create new folder
	folderID, err := p.apiClient.CreateFolder(p.ctx, folderName, parentRemoteID)
	if err != nil {
		return "", false, fmt.Errorf("failed to create folder %s: %w", folderName, err)
	}

	// Populate cache for newly created folder
	if _, err := p.cache.Get(p.ctx, p.apiClient, folderID); err != nil {
		p.logger.Warn().Str("folder_id", folderID).Err(err).Msg("Failed to populate cache for new folder")
	}

	p.mappingMu.Lock()
	p.mapping[dirPath] = folderID
	p.mappingMu.Unlock()

	atomic.AddInt32(p.foldersCreated, 1)

	if p.progressWriter != nil {
		fmt.Fprintf(p.progressWriter, "  ✓ Created folder: %s (ID: %s)\n", folderName, folderID)
	}

	if p.folderReadyChan != nil {
		depth := strings.Count(dirPath, string(os.PathSeparator))
		select {
		case p.folderReadyChan <- FolderReadyEvent{LocalPath: dirPath, RemoteID: folderID, Depth: depth}:
		case <-p.ctx.Done():
			return "", false, p.ctx.Err()
		}
	}

	return folderID, true, nil
}

// CreateFolderStructure creates all folders recursively, handling conflicts.
// If folderReadyChan is provided, sends events as folders become ready for file uploads.
// v4.8.5: Refactored to use processFolder helper for North Star code reuse.
// Exported for GUI reuse.
func CreateFolderStructure(
	ctx context.Context,
	apiClient *api.Client,
	cache *FolderCache,
	rootPath string,
	directories []string,
	rootRemoteID string,
	folderConflictMode *ConflictAction,
	maxConcurrent int,
	logger *logging.Logger,
	folderReadyChan chan<- FolderReadyEvent,
	progressWriter io.Writer,
) (map[string]string, int, error) {
	mapping := make(map[string]string)
	mapping[rootPath] = rootRemoteID
	var foldersCreated int32
	var mappingMu sync.RWMutex

	// Send ready event for root folder if channel provided
	if folderReadyChan != nil {
		select {
		case folderReadyChan <- FolderReadyEvent{
			LocalPath: rootPath,
			RemoteID:  rootRemoteID,
			Depth:     strings.Count(rootPath, string(os.PathSeparator)),
		}:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}

	params := processFolderParams{
		ctx:                ctx,
		apiClient:          apiClient,
		cache:              cache,
		folderConflictMode: folderConflictMode,
		logger:             logger,
		progressWriter:     progressWriter,
		folderReadyChan:    folderReadyChan,
		mapping:            mapping,
		mappingMu:          &mappingMu,
		foldersCreated:     &foldersCreated,
	}

	// Sort directories by depth (create parents first)
	sort.Slice(directories, func(i, j int) bool {
		depthI := strings.Count(directories[i], string(os.PathSeparator))
		depthJ := strings.Count(directories[j], string(os.PathSeparator))
		return depthI < depthJ
	})

	// Group directories by depth level for concurrent processing
	depthGroups := make(map[int][]string)
	for _, dirPath := range directories {
		depth := strings.Count(dirPath, string(os.PathSeparator))
		depthGroups[depth] = append(depthGroups[depth], dirPath)
	}

	depths := make([]int, 0, len(depthGroups))
	for depth := range depthGroups {
		depths = append(depths, depth)
	}
	sort.Ints(depths)

	for _, depth := range depths {
		dirsAtDepth := depthGroups[depth]

		semaphore := make(chan struct{}, maxConcurrent)
		var wg sync.WaitGroup
		errChan := make(chan error, len(dirsAtDepth))

		for _, dirPath := range dirsAtDepth {
			wg.Add(1)
			go func(dp string) {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				_, _, err := processFolder(params, dp)
				if err != nil {
					errChan <- err
				}
			}(dirPath)
		}

		wg.Wait()
		close(errChan)

		for err := range errChan {
			return nil, 0, err
		}
	}

	return mapping, int(foldersCreated), nil
}

// CreateFolderStructureStreaming creates remote folders from a streaming directory channel.
// Unlike CreateFolderStructure which requires all directories upfront, this processes
// directories as they are discovered by WalkStream.
//
// v4.8.5: Uses parent-ready gating — a folder can be created as soon as its parent
// exists in the mapping. filepath.WalkDir guarantees parents are visited before
// children, so pending buffers stay small.
//
// Returns the mapping (localPath → remoteID), folders created count, and any error.
func CreateFolderStructureStreaming(
	ctx context.Context,
	apiClient *api.Client,
	cache *FolderCache,
	rootPath string,
	dirChan <-chan localfs.FileEntry,
	rootRemoteID string,
	folderConflictMode *ConflictAction,
	maxConcurrent int,
	logger *logging.Logger,
	folderReadyChan chan<- FolderReadyEvent,
	progressWriter io.Writer,
) (map[string]string, int, error) {
	mapping := make(map[string]string)
	mapping[rootPath] = rootRemoteID
	var foldersCreated int32
	var mappingMu sync.RWMutex

	// Send ready event for root folder
	if folderReadyChan != nil {
		select {
		case folderReadyChan <- FolderReadyEvent{
			LocalPath: rootPath,
			RemoteID:  rootRemoteID,
			Depth:     strings.Count(rootPath, string(os.PathSeparator)),
		}:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}

	params := processFolderParams{
		ctx:                ctx,
		apiClient:          apiClient,
		cache:              cache,
		folderConflictMode: folderConflictMode,
		logger:             logger,
		progressWriter:     progressWriter,
		folderReadyChan:    folderReadyChan,
		mapping:            mapping,
		mappingMu:          &mappingMu,
		foldersCreated:     &foldersCreated,
	}

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var pendingDirs []string
	var pendingMu sync.Mutex
	var firstErr error
	var errMu sync.Mutex

	setErr := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}

	// flushPending launches goroutines for any pending dirs whose parent is now ready.
	// Declared as var for recursive use from goroutines.
	// Must be called under pendingMu lock.
	var flushPending func()
	flushPending = func() {
		remaining := pendingDirs[:0]
		for _, dp := range pendingDirs {
			parentPath := filepath.Dir(dp)
			mappingMu.RLock()
			_, parentReady := mapping[parentPath]
			mappingMu.RUnlock()

			if parentReady {
				dpCopy := dp
				wg.Add(1)
				go func() {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()

					_, _, err := processFolder(params, dpCopy)
					if err != nil {
						setErr(err)
						return
					}
					// After adding to mapping, flush any pending children
					pendingMu.Lock()
					flushPending()
					pendingMu.Unlock()
				}()
			} else {
				remaining = append(remaining, dp)
			}
		}
		pendingDirs = remaining
	}

	// Process directories as they arrive from the walk
	for dir := range dirChan {
		errMu.Lock()
		hasErr := firstErr != nil
		errMu.Unlock()
		if hasErr {
			break
		}

		dirPath := dir.Path
		parentPath := filepath.Dir(dirPath)

		mappingMu.RLock()
		_, parentReady := mapping[parentPath]
		mappingMu.RUnlock()

		if parentReady {
			// Parent is ready — create immediately
			wg.Add(1)
			go func(dp string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				_, _, err := processFolder(params, dp)
				if err != nil {
					setErr(err)
					return
				}
				// Flush any pending children
				pendingMu.Lock()
				flushPending()
				pendingMu.Unlock()
			}(dirPath)
		} else {
			// Parent not yet created — buffer
			pendingMu.Lock()
			pendingDirs = append(pendingDirs, dirPath)
			pendingMu.Unlock()
		}
	}

	// Wait for all in-flight folder creations
	wg.Wait()

	// Final flush attempt for any remaining pending dirs
	pendingMu.Lock()
	flushPending()
	pendingMu.Unlock()
	wg.Wait()

	// Any remaining pending dirs are errors (parent was never created)
	pendingMu.Lock()
	if len(pendingDirs) > 0 && firstErr == nil {
		firstErr = fmt.Errorf("orphaned directories (parent never created): %d remaining, first: %s",
			len(pendingDirs), pendingDirs[0])
	}
	pendingMu.Unlock()

	return mapping, int(foldersCreated), firstErr
}

// cliPipelinedUploadItem implements transfer.WorkItem for uploadDirectoryPipelined.
// v4.8.6: Replaces hand-rolled worker pool with transfer.RunBatchFromChannel.
type cliPipelinedUploadItem struct {
	fpath          string
	remoteFolderID string
	relativePath   string
	size           int64
}

func (p cliPipelinedUploadItem) FileSize() int64 { return p.size }

// uploadDirectoryPipelined coordinates pipelined folder creation and file uploads
// Folders are created depth-by-depth, and files are uploaded as soon as their parent folder is ready
// v4.8.1: Added resourceMgr parameter — must be from CreateResourceManager().
// v4.8.6: Migrated file upload worker pool to transfer.RunBatchFromChannel.
func uploadDirectoryPipelined(
	ctx context.Context,
	apiClient *api.Client,
	cache *FolderCache,
	rootPath string,
	directories []string,
	rootRemoteID string,
	files []string,
	folderConcurrency int,
	fileConcurrency int,
	continueOnError bool,
	skipExisting bool,
	cfg *config.Config,
	logger *logging.Logger,
	resourceMgr *resources.Manager,
) (*UploadResult, int, error) {
	result := &UploadResult{}
	var resultMutex sync.Mutex
	foldersCreated := 0
	var foldersCreatedMutex sync.Mutex

	// Build map of files per directory
	filesPerDir := make(map[string][]string)
	for _, filePath := range files {
		dirPath := filepath.Dir(filePath)
		filesPerDir[dirPath] = append(filesPerDir[dirPath], filePath)
	}

	// Create folder ready channel (buffered to prevent blocking)
	folderReadyChan := make(chan FolderReadyEvent, constants.WorkChannelBuffer)

	// Create progress UI
	uploadUI := progress.NewUploadUI(len(files))

	// NOTE: Do NOT redirect zerolog through uploadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.
	// The mpb library handles rendering progress bars above stderr output automatically.

	defer uploadUI.Wait()

	// Pre-warm credentials and prepare for uploads
	logger.Debug().Msg("Pre-fetching storage credentials")
	credManager := credentials.GetManager(apiClient)
	_, err := credManager.GetS3Credentials(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to pre-fetch credentials, will fetch on demand")
	}

	// Shared state for conflict modes
	// Note: folderConflictMode is passed by pointer to CreateFolderStructure
	folderConflictMode := ConflictMergeOnce
	if skipExisting {
		folderConflictMode = ConflictMergeAll
	}
	// v4.8.1: Use shared ConflictResolver for file conflicts
	initialFileMode := FileOverwriteOnce
	if skipExisting {
		initialFileMode = FileSkipAll
	}
	fileConflictResolver := NewFileConflictResolver(initialFileMode)
	errorMode := ErrorContinueOnce

	// v4.8.1: Use passed-in resource manager (must be from CreateResourceManager())
	if resourceMgr == nil {
		panic("uploadDirectoryPipelined: resourceMgr is required (use CreateResourceManager())")
	}
	cliUploadTransferMgr := transfer.NewManager(resourceMgr)

	// v4.8.6: Use RunBatchFromChannel with AdaptiveCount for dynamic worker scaling.
	// Replaces manual ComputeBatchConcurrency + fixed worker pool.
	var adaptive *transfer.AdaptiveWorkerCount
	batchCfg := transfer.BatchConfig{
		MaxWorkers:    fileConcurrency,
		ResourceMgr:   resourceMgr,
		Label:         "CLI-PIPELINED-UPLOAD",
		AdaptiveCount: &adaptive,
	}

	// WaitGroup to track folder creation + event dispatcher
	var wg sync.WaitGroup

	// 1. Start folder creation goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(folderReadyChan) // Signal completion to file uploader

		_, created, err := CreateFolderStructure(
			ctx, apiClient, cache, rootPath, directories, rootRemoteID,
			&folderConflictMode, folderConcurrency, logger, folderReadyChan,
			uploadUI.Writer())

		if err != nil {
			logger.Error().Err(err).Msg("Folder creation failed")
			return
		}

		foldersCreatedMutex.Lock()
		foldersCreated = created
		foldersCreatedMutex.Unlock()

		logger.Info().Int("folders", created).Msg("Folder creation complete")
	}()

	// 2. Start RunBatchFromChannel for file uploads.
	// The event dispatcher sends cliPipelinedUploadItem to batchCh as folders become ready.
	// RunBatchFromChannel consumes from batchCh with adaptive worker scaling.
	batchCh := make(chan cliPipelinedUploadItem, constants.WorkChannelBuffer)

	var batchWg sync.WaitGroup
	batchWg.Add(1)
	go func() {
		defer batchWg.Done()
		// Note: We do NOT rely on BatchResult.Completed — many nil returns are
		// skips/conflicts. All real outcomes are tracked by manual counters.
		transfer.RunBatchFromChannel(ctx, batchCh, batchCfg,
			func(ctx context.Context, item cliPipelinedUploadItem) error {
				fpath := item.fpath
				remoteFolderID := item.remoteFolderID
				relativePath := item.relativePath
				fileName := filepath.Base(fpath)

				// Check if file exists
				existingFileID, exists, checkErr := checkFileExists(ctx, apiClient, cache, remoteFolderID, fileName)
				if checkErr != nil {
					if continueOnError || errorMode == ErrorContinueAll {
						resultMutex.Lock()
						result.Errors = append(result.Errors, UploadError{fpath, checkErr})
						resultMutex.Unlock()
						logger.Error().Str("file", fpath).Err(checkErr).Msg("Error checking file existence")
						return nil
					}
					logger.Error().Err(checkErr).Str("file", fpath).Msg("Failed to check if file exists")
					return nil
				}

				if exists {
					// v4.8.1: Use shared ConflictResolver instead of inline state machine
					action, promptErr := fileConflictResolver.Resolve(func() (FileConflictAction, error) {
						return promptFileConflict(fileName, relativePath)
					})
					if promptErr != nil {
						logger.Error().Err(promptErr).Str("file", fileName).Msg("Error prompting for file conflict")
						resultMutex.Lock()
						result.Errors = append(result.Errors, UploadError{fpath, promptErr})
						resultMutex.Unlock()
						return nil
					}

					switch action {
					case FileSkipOnce, FileSkipAll:
						logger.Debug().Str("file", fileName).Msg("Ignoring existing file")
						resultMutex.Lock()
						result.FilesIgnored++
						resultMutex.Unlock()
						return nil
					case FileOverwriteOnce, FileOverwriteAll:
						if delErr := apiClient.DeleteFile(ctx, existingFileID); delErr != nil {
							logger.Error().Str("file", fileName).Err(delErr).Msg("Failed to delete existing file")
						}
					case FileAbort:
						logger.Info().Msg("Upload aborted by user")
						return nil
					}
				}

				// Get file info for size
				fileInfo, statErr := os.Stat(fpath)
				if statErr != nil {
					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, statErr})
					resultMutex.Unlock()
					return nil
				}

				// Create progress bar
				fileBar := uploadUI.AddFileBar(fpath, remoteFolderID, fileInfo.Size())

				// v4.8.6: Use adaptive worker count from RunBatchFromChannel
				workerCount := constants.DefaultMaxConcurrent
				if adaptive != nil {
					workerCount = adaptive.Load()
				}
				transferHandle := cliUploadTransferMgr.AllocateTransfer(fileInfo.Size(), workerCount)

				// Upload file
				cloudFile, uploadErr := upload.UploadFile(ctx, upload.UploadParams{
					LocalPath: fpath,
					FolderID:  remoteFolderID,
					APIClient: apiClient,
					ProgressCallback: func(fraction float64) {
						fileBar.UpdateProgress(fraction)
					},
					OutputWriter:   uploadUI.Writer(),
					TransferHandle: transferHandle,
				})
				transferHandle.Complete()

				if uploadErr != nil {
					fileBar.Complete("", uploadErr)

					if state.UploadResumeStateExists(fpath) {
						fmt.Fprintf(os.Stderr, "\n💡 Resume state saved for %s. To resume, re-run the upload command.\n", filepath.Base(fpath))
					}

					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, uploadErr})
					resultMutex.Unlock()
					logger.Error().Str("file", fpath).Err(uploadErr).Msg("Failed to upload file")
					return nil
				}

				// Success
				fileBar.Complete(cloudFile.ID, nil)
				resultMutex.Lock()
				result.FilesUploaded++
				result.TotalBytes += fileInfo.Size()
				result.UploadedFileIDs = append(result.UploadedFileIDs, cloudFile.ID)
				resultMutex.Unlock()
				return nil
			})
	}()

	// Event dispatcher goroutine: reads folder ready events, sends work to batch channel
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(batchCh) // Signal RunBatchFromChannel to finish when all folders processed

		processedFolders := make(map[string]bool)

		for event := range folderReadyChan {
			filesInFolder, hasFiles := filesPerDir[event.LocalPath]
			if !hasFiles || len(filesInFolder) == 0 {
				continue
			}

			if processedFolders[event.LocalPath] {
				continue
			}
			processedFolders[event.LocalPath] = true

			relativePath, relErr := filepath.Rel(rootPath, event.LocalPath)
			if relErr != nil {
				relativePath = filepath.Base(event.LocalPath)
			} else if relativePath == "." {
				relativePath = filepath.Base(rootPath)
			}
			uploadUI.SetFolderPath(event.RemoteID, relativePath)

			for _, filePath := range filesInFolder {
				// Stat for FileSize() — used by RunBatchFromChannel for adaptive scaling
				var sz int64
				if info, statErr := os.Stat(filePath); statErr == nil {
					sz = info.Size()
				}
				batchCh <- cliPipelinedUploadItem{
					fpath:          filePath,
					remoteFolderID: event.RemoteID,
					relativePath:   relativePath,
					size:           sz,
				}
			}
		}
	}()

	// Wait for folder creation + event dispatcher to finish
	wg.Wait()
	// Wait for RunBatchFromChannel to drain and finish
	batchWg.Wait()

	return result, foldersCreated, nil
}

// cliUploadWorkItem implements transfer.WorkItem for uploadFiles RunBatch migration.
// v4.8.6: Replaces hand-rolled worker pool with transfer.RunBatch.
type cliUploadWorkItem struct {
	fpath string
	size  int64
}

func (u cliUploadWorkItem) FileSize() int64 { return u.size }

// uploadFiles uploads all files with progress tracking and conflict/error handling.
// v4.8.1: Uses ConflictResolver instead of raw pointer-based state machines.
// v4.8.1: Added resourceMgr parameter — must be from CreateResourceManager().
// v4.8.6: Migrated from hand-rolled worker pool to transfer.RunBatch.
func uploadFiles(
	ctx context.Context,
	rootPath string,
	files []string,
	mapping map[string]string,
	apiClient *api.Client,
	cache *FolderCache,
	uploadUI *progress.UploadUI,
	fileConflictResolver *ConflictResolver[FileConflictAction],
	errorResolver *ConflictResolver[ErrorAction],
	continueOnError bool,
	maxConcurrent int,
	cfg *config.Config,
	logger *logging.Logger,
	resourceMgr *resources.Manager,
) (*UploadResult, error) {
	result := &UploadResult{}
	var resultMutex sync.Mutex

	// NOTE: Do NOT redirect zerolog through uploadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.

	// Pre-warm credential cache before starting uploads
	// This eliminates the first-upload credential fetch delay
	logger.Debug().Msg("Pre-fetching storage credentials")
	credManager := credentials.GetManager(apiClient)
	_, err := credManager.GetS3Credentials(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to pre-fetch credentials, will fetch on demand")
		// Continue anyway - uploads will fetch credentials as needed
	}

	// v4.8.1: Use passed-in resource manager (must be from CreateResourceManager())
	if resourceMgr == nil {
		panic("uploadFiles: resourceMgr is required (use CreateResourceManager())")
	}
	seqTransferMgr := transfer.NewManager(resourceMgr)

	// v4.8.6: Build work items with file sizes for adaptive concurrency.
	items := make([]cliUploadWorkItem, len(files))
	for i, f := range files {
		var sz int64
		if info, statErr := os.Stat(f); statErr == nil {
			sz = info.Size()
		}
		items[i] = cliUploadWorkItem{fpath: f, size: sz}
	}

	// v4.8.6: Compute adaptive worker count for AllocateTransfer (same count RunBatch will use).
	batchCfg := transfer.BatchConfig{
		MaxWorkers:  maxConcurrent,
		ResourceMgr: resourceMgr,
		Label:       "CLI-UPLOAD",
	}
	adaptiveWorkers := transfer.ComputedWorkers(items, batchCfg)

	// v4.8.6: Use transfer.RunBatch instead of hand-rolled worker pool.
	// Note: We do NOT rely on BatchResult.Completed — many nil returns are
	// skips/conflicts. All real outcomes are tracked by manual counters.
	transfer.RunBatch(ctx, items, batchCfg, func(ctx context.Context, item cliUploadWorkItem) error {
		fpath := item.fpath

		// Get parent directory
		dirPath := filepath.Dir(fpath)
		remoteFolderID, ok := mapping[dirPath]
		if !ok {
			resultMutex.Lock()
			result.FilesSkipped++
			resultMutex.Unlock()
			logger.Info().Str("file", fpath).Msg("Skipping file (parent folder was skipped)")
			return nil
		}

		fileName := filepath.Base(fpath)
		relativePath, relErr := filepath.Rel(rootPath, fpath)
		if relErr != nil {
			relativePath = fileName
		}

		// SAFE MODE: Check if file exists before uploading (uses cache)
		var existingFileID string
		var exists bool
		var checkErr error

		if cfg.CheckConflictsBeforeUpload {
			existingFileID, exists, checkErr = checkFileExists(ctx, apiClient, cache, remoteFolderID, fileName)
		}

		if cfg.CheckConflictsBeforeUpload && checkErr != nil {
			if continueOnError {
				resultMutex.Lock()
				result.Errors = append(result.Errors, UploadError{fpath, checkErr})
				resultMutex.Unlock()
				logger.Error().Str("file", fpath).Err(checkErr).Msg("Error checking file existence")
				return nil
			}
			// v4.8.1: Use shared error resolver instead of inline state machine
			action, promptErr := errorResolver.Resolve(func() (ErrorAction, error) {
				return promptUploadError(fileName, checkErr)
			})
			if promptErr != nil {
				logger.Error().Err(promptErr).Msg("Error prompting user")
				return nil
			}
			if action == ErrorAbort {
				logger.Info().Msg("Upload aborted by user")
				return nil
			}
			// ErrorContinueOnce or ErrorContinueAll — record and continue
			resultMutex.Lock()
			result.Errors = append(result.Errors, UploadError{fpath, checkErr})
			resultMutex.Unlock()
			return nil
		}

		// SAFE MODE: Handle conflicts BEFORE upload
		if cfg.CheckConflictsBeforeUpload && exists {
			// v4.8.1: Use shared ConflictResolver instead of inline state machine
			action, promptErr := fileConflictResolver.Resolve(func() (FileConflictAction, error) {
				folderPath := filepath.Dir(relativePath)
				if folderPath == "." {
					folderPath = filepath.Base(rootPath)
				}
				return promptFileConflict(fileName, folderPath)
			})
			if promptErr != nil {
				logger.Error().Err(promptErr).Msg("Error prompting user")
				return nil
			}

			switch action {
			case FileSkipOnce, FileSkipAll:
				logger.Debug().Str("file", fileName).Msg("Ignoring existing file")
				fmt.Fprintf(uploadUI.Writer(), "  ⏭  Ignoring existing file: %s\n", fileName)
				resultMutex.Lock()
				result.FilesIgnored++
				resultMutex.Unlock()
				return nil
			case FileOverwriteOnce, FileOverwriteAll:
				logger.Info().Str("file", fileName).Str("file_id", existingFileID).Msg("Deleting existing file before overwrite")
				if err := apiClient.DeleteFile(ctx, existingFileID); err != nil {
					logger.Error().Str("file", fileName).Err(err).Msg("Failed to delete existing file")
				}
			case FileAbort:
				logger.Info().Msg("Upload aborted by user")
				return nil
			}
		}

		// Get file info
		fileInfo, statErr := os.Stat(fpath)
		if statErr != nil {
			if continueOnError {
				resultMutex.Lock()
				result.Errors = append(result.Errors, UploadError{fpath, statErr})
				resultMutex.Unlock()
				logger.Error().Str("file", fpath).Err(statErr).Msg("Error getting file info")
				return nil
			}
			action, promptErr := errorResolver.Resolve(func() (ErrorAction, error) {
				return promptUploadError(fileName, statErr)
			})
			if promptErr != nil {
				logger.Error().Err(promptErr).Msg("Error prompting user")
				return nil
			}
			if action == ErrorAbort {
				logger.Info().Msg("Upload aborted by user")
				return nil
			}
			resultMutex.Lock()
			result.Errors = append(result.Errors, UploadError{fpath, statErr})
			resultMutex.Unlock()
			return nil
		}

		// Create progress bar for this file
		fileBar := uploadUI.AddFileBar(fpath, remoteFolderID, fileInfo.Size())

		// v4.8.0: Allocate transfer handle for per-file multi-threading
		// v4.8.1: Pass adaptive worker count (was maxConcurrent, Bug #3)
		seqHandle := seqTransferMgr.AllocateTransfer(fileInfo.Size(), adaptiveWorkers)

		// Upload with progress callback
		cloudFile, uploadErr := upload.UploadFile(ctx, upload.UploadParams{
			LocalPath: fpath,
			FolderID:  remoteFolderID,
			APIClient: apiClient,
			ProgressCallback: func(prog float64) {
				fileBar.UpdateProgress(prog)
			},
			OutputWriter:   uploadUI.Writer(),
			TransferHandle: seqHandle,
		})
		seqHandle.Complete()

		fileID := ""
		if cloudFile != nil {
			fileID = cloudFile.ID
		}

		if uploadErr != nil {
			fileBar.Complete(fileID, uploadErr)

			if diskspace.IsInsufficientSpaceError(uploadErr) {
				resultMutex.Lock()
				result.Errors = append(result.Errors, UploadError{fpath, uploadErr})
				resultMutex.Unlock()
				logger.Error().Str("file", fpath).Err(uploadErr).Msg("Upload skipped - insufficient disk space")
				return nil
			}

			// FAST MODE: Handle conflict detected on upload
			if !cfg.CheckConflictsBeforeUpload && api.IsFileExistsError(uploadErr) {
				logger.Warn().Str("file", fileName).Msg("File already exists (detected on upload)")

				// Query folder to get existing file ID — use cache with stale fallback
				contents, queryErr := cache.Get(ctx, apiClient, remoteFolderID)
				if queryErr != nil {
					logger.Error().Err(queryErr).Msg("Failed to query folder after conflict")
					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, queryErr})
					resultMutex.Unlock()
					return nil
				}

				foundExisting := false
				for _, file := range contents.Files {
					if file.Name == fileName {
						existingFileID = file.ID
						foundExisting = true
						break
					}
				}

				// Stale-cache fallback: invalidate and retry once
				if !foundExisting {
					cache.Invalidate(remoteFolderID)
					contents, queryErr = cache.Get(ctx, apiClient, remoteFolderID)
					if queryErr == nil {
						for _, file := range contents.Files {
							if file.Name == fileName {
								existingFileID = file.ID
								foundExisting = true
								break
							}
						}
					}
				}

				if !foundExisting {
					logger.Error().Msg("File exists error but couldn't find existing file")
					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, uploadErr})
					resultMutex.Unlock()
					return nil
				}

				// v4.8.1: Use shared ConflictResolver for post-upload conflict
				action, promptErr := fileConflictResolver.Resolve(func() (FileConflictAction, error) {
					folderPath := filepath.Dir(relativePath)
					if folderPath == "." {
						folderPath = filepath.Base(rootPath)
					}
					return promptFileConflict(fileName, folderPath)
				})
				if promptErr != nil {
					logger.Error().Err(promptErr).Msg("Error prompting user")
					return nil
				}

				switch action {
				case FileSkipOnce, FileSkipAll:
					logger.Debug().Str("file", fileName).Msg("Ignoring existing file")
					fmt.Fprintf(uploadUI.Writer(), "  ⏭  Ignoring existing file: %s\n", fileName)
					resultMutex.Lock()
					result.FilesIgnored++
					resultMutex.Unlock()
					return nil

				case FileOverwriteOnce, FileOverwriteAll:
					logger.Info().Str("file", fileName).Str("file_id", existingFileID).Msg("Deleting existing file for overwrite")
					if delErr := apiClient.DeleteFile(ctx, existingFileID); delErr != nil {
						logger.Error().Err(delErr).Msg("Failed to delete existing file")
						resultMutex.Lock()
						result.Errors = append(result.Errors, UploadError{fpath, delErr})
						resultMutex.Unlock()
						return nil
					}

					logger.Info().Str("file", fileName).Msg("Retrying upload after deletion")
					cloudFile, retryErr := upload.UploadFile(ctx, upload.UploadParams{
						LocalPath: fpath,
						FolderID:  remoteFolderID,
						APIClient: apiClient,
						ProgressCallback: func(prog float64) {
							fileBar.UpdateProgress(prog)
						},
						OutputWriter: uploadUI.Writer(),
					})

					if retryErr != nil {
						fileBar.Complete("", retryErr)
						logger.Error().Err(retryErr).Msg("Upload failed after overwrite")
						resultMutex.Lock()
						result.Errors = append(result.Errors, UploadError{fpath, retryErr})
						resultMutex.Unlock()
						return nil
					}

					fileBar.Complete(cloudFile.ID, nil)
					resultMutex.Lock()
					result.FilesUploaded++
					result.TotalBytes += cloudFile.DecryptedSize
					resultMutex.Unlock()
					logger.Info().Str("file", fileName).Str("file_id", cloudFile.ID).Msg("Upload successful (after overwrite)")
					return nil

				case FileAbort:
					logger.Info().Msg("Upload aborted by user")
					return nil
				}

				return nil
			}

			// Handle other upload errors
			if continueOnError {
				resultMutex.Lock()
				result.Errors = append(result.Errors, UploadError{fpath, uploadErr})
				resultMutex.Unlock()
				logger.Error().Str("file", fpath).Err(uploadErr).Msg("Upload failed")
				return nil
			}
			action, promptErr := errorResolver.Resolve(func() (ErrorAction, error) {
				return promptUploadError(fileName, uploadErr)
			})
			if promptErr != nil {
				logger.Error().Err(promptErr).Msg("Error prompting user")
				return nil
			}
			if action == ErrorAbort {
				logger.Info().Msg("Upload aborted by user")
				return nil
			}
			resultMutex.Lock()
			result.Errors = append(result.Errors, UploadError{fpath, uploadErr})
			resultMutex.Unlock()
			return nil
		}

		// Mark upload as successful
		fileBar.Complete(fileID, nil)

		resultMutex.Lock()
		result.FilesUploaded++
		result.TotalBytes += fileInfo.Size()
		if fileID != "" {
			result.UploadedFileIDs = append(result.UploadedFileIDs, fileID)
		}
		resultMutex.Unlock()
		return nil
	})

	return result, nil
}
