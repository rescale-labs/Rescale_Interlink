package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/transfer/folder"
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

// FolderReadyEvent, FolderCache, NewFolderCache, BuildDirectoryTree, CheckFolderExists,
// CreateFolderStructure, CreateFolderStructureStreaming, and related helpers moved to
// internal/transfer/folder/ (v4.8.7 Plan 2b). Aliases in folder_upload_compat.go
// preserve the cli.* API surface.

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

// cliPipelinedUploadItem implements transfer.WorkItem for uploadDirectoryPipelined.
// v4.8.6: Replaces hand-rolled worker pool with transfer.RunBatchFromChannel.
type cliPipelinedUploadItem struct {
	fpath          string
	remoteFolderID string
	relativePath   string
	size           int64
}

func (p cliPipelinedUploadItem) FileSize() int64 { return p.size }

// uploadDirectoryPipelined coordinates streaming pipelined folder creation and file uploads.
// v4.8.7: Uses WalkStream for streaming discovery — files start uploading before scan completes.
// v4.8.7 Plan 2b: Uses shared folder.RunOrchestrator for the three-part streaming pipeline.
// v4.8.1: Added resourceMgr parameter — must be from CreateResourceManager().
// v4.8.6: Migrated file upload worker pool to transfer.RunBatchFromChannel.
func uploadDirectoryPipelined(
	ctx context.Context,
	apiClient *api.Client,
	cache *FolderCache,
	rootPath string,
	rootRemoteID string,
	includeHidden bool,
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

	// v4.8.7: Streaming progress UI — total starts at 0, increments as files are discovered.
	uploadUI := progress.NewUploadUI(0)

	// NOTE: Do NOT redirect zerolog through uploadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.
	defer uploadUI.Wait()

	// v4.8.7: Unified credential warming — session + provider + metadata caches.
	logger.Debug().Msg("Pre-warming credential caches")
	credManager := credentials.GetManager(apiClient)
	credManager.WarmAll(ctx)

	// Shared state for conflict modes
	folderConflictMode := ConflictMergeOnce
	if skipExisting {
		folderConflictMode = ConflictMergeAll
	}
	initialFileMode := FileOverwriteOnce
	if skipExisting {
		initialFileMode = FileSkipAll
	}
	fileConflictResolver := NewFileConflictResolver(initialFileMode)
	errorMode := ErrorContinueOnce

	if resourceMgr == nil {
		panic("uploadDirectoryPipelined: resourceMgr is required (use CreateResourceManager())")
	}
	cliUploadTransferMgr := transfer.NewManager(resourceMgr)

	// v4.8.6: Use RunBatchFromChannel with AdaptiveCount for dynamic worker scaling.
	var adaptive *transfer.AdaptiveWorkerCount
	batchCfg := transfer.BatchConfig{
		MaxWorkers:    fileConcurrency,
		ResourceMgr:   resourceMgr,
		Label:         "CLI-PIPELINED-UPLOAD",
		AdaptiveCount: &adaptive,
	}

	// v4.8.7 Plan 2b: Shared orchestrator feeds batchCh for RunBatchFromChannel.
	batchCh := make(chan cliPipelinedUploadItem, constants.WorkChannelBuffer)

	// Start RunBatchFromChannel consumer (unchanged from pre-2b)
	var batchWg sync.WaitGroup
	batchWg.Add(1)
	go func() {
		defer batchWg.Done()
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

	// v4.8.7 Plan 2b: Shared orchestrator replaces inline Parts A/B/C.
	dispatchDone, orchResult := folder.RunOrchestrator(ctx,
		folder.OrchestratorConfig{
			RootPath:          rootPath,
			RootRemoteID:      rootRemoteID,
			IncludeHidden:     includeHidden,
			FolderConcurrency: folderConcurrency,
			ConflictMode:      folder.ConflictAction(folderConflictMode),
			ConflictPrompt: func(name string) (folder.ConflictAction, error) {
				return promptFolderConflict(name)
			},
			Logger:         logger,
			APIClient:      apiClient,
			Cache:          cache,
			ProgressWriter: uploadUI.Writer(),
		},
		folder.OrchestratorCallbacks[cliPipelinedUploadItem]{
			OnFileDiscovered: func(snap folder.ProgressSnapshot) {
				// v4.8.7: Streaming total increment
				uploadUI.IncrementTotal()
			},
			OnFolderReady:      nil, // CLI: no folder progress events
			OnOrchestratorDone: nil, // CLI: waits synchronously via <-dispatchDone
			BuildItem: func(file localfs.FileEntry, remoteFolderID, rootPath string) cliPipelinedUploadItem {
				relativePath, relErr := filepath.Rel(rootPath, filepath.Dir(file.Path))
				if relErr != nil {
					relativePath = filepath.Base(filepath.Dir(file.Path))
				} else if relativePath == "." {
					relativePath = filepath.Base(rootPath)
				}
				uploadUI.SetFolderPath(remoteFolderID, relativePath)
				return cliPipelinedUploadItem{
					fpath:          file.Path,
					remoteFolderID: remoteFolderID,
					relativePath:   relativePath,
					size:           file.Size,
				}
			},
			OnUnmappedFiles: func(parentDir string, count int) {
				logger.Warn().Int("count", count).Str("dir", parentDir).Msg("Skipping files in unmapped folder")
			},
		},
		batchCh,
	)

	// Wait for orchestrator + dispatcher to finish sending all items to batchCh
	<-dispatchDone

	if orchResult.WalkError != nil {
		logger.Error().Err(orchResult.WalkError).Msg("Walk error during streaming scan")
	}
	if orchResult.FolderError != nil {
		logger.Error().Err(orchResult.FolderError).Msg("Folder creation failed")
	}
	foldersCreatedMutex.Lock()
	foldersCreated = orchResult.FoldersCreated
	foldersCreatedMutex.Unlock()

	// Wait for RunBatchFromChannel to finish all uploads
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

	// v4.8.7: Unified credential warming — session + provider + metadata caches.
	logger.Debug().Msg("Pre-warming credential caches")
	credManager := credentials.GetManager(apiClient)
	credManager.WarmAll(ctx)

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
