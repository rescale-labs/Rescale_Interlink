package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/transfer/scan"
	"github.com/rescale/rescale-int/internal/validation"
)

// DownloadResult tracks what happened during download
type DownloadResult struct {
	FoldersCreated  int
	FilesDownloaded int
	FilesSkipped    int
	FilesFailed     int
	FilesNotStarted int // a cancel, an Abort, or a failure without --continue-on-error, stopped the download first
	TotalBytes      int64
	Errors          []DownloadError
}

// A folder download's questions at a terminal and its removal of a local file
// it replaces, as variables so that a test can answer them and make the removal
// fail on any system.
var (
	askFolderDownloadMode = func() (FolderDownloadMode, error) {
		if !IsTerminal() {
			return FolderDownloadModePrompt, fmt.Errorf("conflict handling mode required in non-interactive mode: use --skip, --overwrite, or --merge")
		}
		return promptFolderDownloadMode()
	}
	promptFolderDownloadConflictFn = promptFolderDownloadConflict
	promptDownloadConflictFn       = promptDownloadConflict
	removeFile                     = os.Remove
)

// replaceStep runs when a worker has decided to replace an existing file,
// before it checks that the download is still going. Only a test sets it.
var replaceStep = func(context.Context) {}

// DownloadError tracks failed downloads
type DownloadError struct {
	FilePath string
	FileID   string
	Error    error
}

// DownloadFolderRecursive recursively downloads a folder and all its contents.
// Exported for GUI reuse.
// folderName: optional name for the downloaded folder. If empty, uses folderID.
func DownloadFolderRecursive(
	ctx context.Context,
	folderID string,
	folderName string,
	outputDir string,
	overwriteAll bool,
	skipAll bool,
	mergeAll bool,
	continueOnError bool,
	maxConcurrent int,
	skipChecksum bool,
	dryRun bool,
	apiClient *api.Client,
	logger *logging.Logger,
	resourceMgr *resources.Manager,
) (*DownloadResult, error) {
	result := &DownloadResult{
		Errors: make([]DownloadError, 0),
	}

	// Use provided folder name, or fall back to folder ID
	rootFolderName := folderName
	if rootFolderName == "" {
		rootFolderName = folderID
	}
	rootOutputDir := filepath.Join(outputDir, rootFolderName)

	logger.Info().
		Str("folder_id", folderID).
		Str("output_dir", rootOutputDir).
		Msg("Starting recursive folder download")

	// Determine conflict handling mode
	var initialFileMode DownloadConflictAction
	var initialFolderMode FolderDownloadConflictAction

	// Count how many conflict flags are set
	flagsSet := 0
	if overwriteAll {
		flagsSet++
	}
	if skipAll {
		flagsSet++
	}
	if mergeAll {
		flagsSet++
	}

	if flagsSet == 0 {
		// No flags specified - prompt user for mode selection
		mode, err := askFolderDownloadMode()
		if err != nil {
			return nil, err
		}
		switch mode {
		case FolderDownloadModeSkip:
			skipAll = true
		case FolderDownloadModeOverwrite:
			overwriteAll = true
		case FolderDownloadModeMerge:
			mergeAll = true
		case FolderDownloadModePrompt:
			// Will prompt for each conflict individually
		}
	}

	// Set initial conflict modes based on flags
	if overwriteAll {
		initialFileMode = DownloadOverwriteAll
		initialFolderMode = FolderDownloadMergeAll // Overwrite files but merge into folders
	} else if skipAll {
		initialFileMode = DownloadSkipAll
		initialFolderMode = FolderDownloadSkipAll
	} else if mergeAll {
		initialFileMode = DownloadSkipAll // Merge = use existing folders, skip existing files
		initialFolderMode = FolderDownloadMergeAll
	} else {
		initialFileMode = DownloadSkipOnce // Will prompt
		initialFolderMode = FolderDownloadSkipOnce
	}

	fileConflictResolver := NewDownloadConflictResolver(initialFileMode)
	folderConflictResolver := NewFolderDownloadConflictResolver(initialFolderMode)

	// Scan the remote folder structure with live progress
	fmt.Println("📡 Scanning remote folder structure...")
	allFolders, allFiles, err := scan.ScanRemoteFolderRecursiveWithProgress(ctx, apiClient, folderID, "",
		func(foldersFound, filesFound int, bytesFound int64) {
			fmt.Fprintf(os.Stderr, "\r  Scanning: %d folders, %d files (%.1f MB)...",
				foldersFound, filesFound, float64(bytesFound)/(1024*1024))
		},
	)
	fmt.Fprintf(os.Stderr, "\r%80s\r", "") // Clear the progress line
	if err != nil {
		return nil, fmt.Errorf("failed to scan remote folder: %w", err)
	}

	fmt.Printf("\n📊 Scan complete:\n")
	fmt.Printf("  Folders: %d\n", len(allFolders))
	fmt.Printf("  Files: %d\n", len(allFiles))
	fmt.Println()

	// DRY-RUN MODE: Show what would happen without downloading
	if dryRun {
		return performDryRunAnalysis(rootOutputDir, allFolders, allFiles, overwriteAll, skipAll, mergeAll)
	}

	// Check if root output directory already exists
	if info, err := os.Stat(rootOutputDir); err == nil && info.IsDir() {
		action, err := folderConflictResolver.Resolve(func() (FolderDownloadConflictAction, error) {
			return promptFolderDownloadConflictFn(rootFolderName, rootOutputDir)
		})
		if err != nil {
			return nil, err
		}
		// Cascade folder "All" decision to file conflict mode; --overwrite still
		// replaces the files of a folder it merges into.
		if !overwriteAll && (action == FolderDownloadSkipAll || action == FolderDownloadMergeAll) {
			fileConflictResolver.SetMode(DownloadSkipAll)
		}

		switch action {
		case FolderDownloadSkipOnce, FolderDownloadSkipAll:
			fmt.Println("⊘ Skipping download - folder already exists")
			return result, nil
		case FolderDownloadAbort:
			return nil, fmt.Errorf("download aborted by user")
		case FolderDownloadMergeOnce, FolderDownloadMergeAll:
			fmt.Printf("⊕ Merging into existing folder: %s\n", rootOutputDir)
			// Continue with download, existing folder will be used
		}
	} else {
		// Root folder doesn't exist - create it
		if err := os.MkdirAll(rootOutputDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create root directory %s: %w", rootOutputDir, err)
		}
		fmt.Printf("✓ Created root folder: %s\n", rootOutputDir)
		result.FoldersCreated++
	}

	// Create local directory structure
	fmt.Println("📂 Creating local directory structure...")
	foldersCreated, foldersMerged, foldersSkipped := 0, 0, 0
	for _, folder := range allFolders {
		// Validate folder path to prevent escaping output directory
		if err := validation.ValidatePathInDirectory(folder.RelativePath, rootOutputDir); err != nil {
			return nil, fmt.Errorf("invalid folder path from API: %w", err)
		}
		localPath := filepath.Join(rootOutputDir, folder.RelativePath)

		// Check if folder already exists
		if info, statErr := os.Stat(localPath); statErr == nil && info.IsDir() {
			action, err := folderConflictResolver.Resolve(func() (FolderDownloadConflictAction, error) {
				return promptFolderDownloadConflictFn(folder.Name, localPath)
			})
			if err != nil {
				return nil, err
			}

			switch action {
			case FolderDownloadSkipOnce, FolderDownloadSkipAll:
				foldersSkipped++
				continue // Skip this folder
			case FolderDownloadAbort:
				return nil, fmt.Errorf("download aborted by user")
			case FolderDownloadMergeOnce, FolderDownloadMergeAll:
				foldersMerged++
				continue
			}
		}

		// Create new folder
		if err := os.MkdirAll(localPath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", localPath, err)
		}
		foldersCreated++
	}
	result.FoldersCreated += foldersCreated
	if foldersMerged+foldersSkipped > 0 {
		fmt.Printf("✓ Handled %d directories (%d created, %d merged, %d skipped)\n",
			foldersCreated+foldersMerged+foldersSkipped, foldersCreated, foldersMerged, foldersSkipped)
	} else {
		fmt.Printf("✓ Created %d local directories\n", foldersCreated)
	}

	// Download all files
	fmt.Println("\n📥 Downloading files...")
	downloadUI := progress.NewDownloadUI(len(allFiles))

	// Route this command's logs through the bars — see executeFileUpload for why.
	if downloadUI.IsTerminal() {
		logger = logger.WithOutput(downloadUI.Writer())
	}

	defer downloadUI.Wait()

	var downloadMutex sync.Mutex

	errChan := make(chan DownloadError, len(allFiles))

	// Create cancelable context for stopping on error when !continueOnError.
	// It is cancelled under downloadMutex, which replace holds.
	downloadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cancelDownload := func() {
		downloadMutex.Lock()
		cancel()
		downloadMutex.Unlock()
	}

	if resourceMgr == nil {
		panic("DownloadFolderRecursive: resourceMgr is required (use CreateResourceManager())")
	}
	cliTransferMgr := transfer.NewManager(resourceMgr)

	// Build work items for BatchExecutor. An entry the scan refused fails here:
	// it is known before any transfer starts, so it stops nothing else, with or
	// without --continue-on-error.
	items := make([]folderDownloadWorkItem, 0, len(allFiles))
	for i, fileTask := range allFiles {
		if fileTask.Err != nil {
			fmt.Fprintf(downloadUI.Writer(), "✗ %v\n", fileTask.Err)
			errChan <- DownloadError{FilePath: filepath.Join(rootOutputDir, fileTask.RelativePath), FileID: fileTask.FileID, Error: fileTask.Err}
			continue
		}
		items = append(items, folderDownloadWorkItem{idx: i, task: fileTask})
	}

	cfg := transfer.BatchConfig{
		MaxWorkers:  maxConcurrent,
		ResourceMgr: resourceMgr,
		Label:       "FOLDER-DOWNLOAD",
	}
	numWorkers := transfer.ComputedWorkers(items, cfg)
	fmt.Printf("  Workers: %d (adaptive, based on file sizes)\n", numWorkers)

	// fail counts a file that was not downloaded. Without --continue-on-error
	// the first one stops the rest of the download, and an Abort always does.
	fail := func(localPath, fileID string, err error, abort bool) error {
		errChan <- DownloadError{FilePath: localPath, FileID: fileID, Error: err}
		if abort || !continueOnError {
			cancelDownload()
		}
		return nil
	}
	// replace removes an existing file to download it again, unless the
	// download has stopped: it checks and removes under the lock the download
	// is cancelled under, so once one worker stops it no other replaces a file.
	replace := func(ctx context.Context, localPath string) (stopped bool, err error) {
		replaceStep(ctx)
		downloadMutex.Lock()
		defer downloadMutex.Unlock()
		if ctx.Err() != nil {
			return true, nil
		}
		return false, removeFile(localPath)
	}

	batchResult := transfer.RunBatch(downloadCtx, items, cfg, func(ctx context.Context, item folderDownloadWorkItem) error {
		task := item.task

		if ctx.Err() != nil {
			return nil // not started
		}

		localPath := filepath.Join(rootOutputDir, task.RelativePath)

		// Check if path exists as a directory (name collision with folder)
		if info, statErr := os.Lstat(localPath); statErr == nil && info.IsDir() {
			originalPath := localPath
			localPath = localPath + ".file"
			logger.Warn().
				Str("original_path", originalPath).
				Str("renamed_to", localPath).
				Msg("File name conflicts with existing directory, renaming file")
		}
		// Before any conflict handling, which would remove, follow or keep it.
		if err := validation.ValidateDownloadTarget(localPath); err != nil {
			fmt.Fprintf(downloadUI.Writer(), "✗ %v\n", err)
			return fail(localPath, task.FileID, err, false)
		}

		// Check if file exists and handle conflict
		if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
			// A conflict it cannot settle has no bar to say why it failed.
			conflictFailed := func(err error, abort bool) error {
				fail(localPath, task.FileID, err, abort) // stop before a write that can block
				fmt.Fprintf(downloadUI.Writer(), "✗ %s: %v\n", task.Name, err)
				return nil
			}
			// Asked under the resolver's lock, where other workers wait to ask:
			// an answer that stops the download cancels it before they can, and
			// a worker that finds it stopped asks nothing.
			action, err := fileConflictResolver.Resolve(func() (DownloadConflictAction, error) {
				if ctx.Err() != nil {
					return DownloadSkipOnce, nil
				}
				action, err := promptDownloadConflictFn(task.Name, localPath)
				if err == nil && action == DownloadAbort || err != nil && !continueOnError {
					cancelDownload()
				}
				return action, err
			})
			if errors.Is(err, io.EOF) {
				err = errors.New("no answer (end of input)")
			}
			if err != nil {
				return conflictFailed(err, false)
			}
			if action == DownloadAbort {
				return conflictFailed(errors.New("download aborted by user"), true)
			}
			if ctx.Err() != nil {
				return nil // stopped while it waited: not started, and nothing removed
			}

			switch action {
			case DownloadSkipOnce, DownloadSkipAll:
				// Reached via --merge, the folder-level skip cascade, or an
				// interactive Skip answer (bare --skip returns at the root-folder
				// check before any file is examined). Existence alone is not
				// proof the file is already in hand: an interrupted earlier run
				// leaves a short file at the same path, and counting it as
				// skipped hands the user a truncated result while reporting
				// success. Same size gate as resolveDownloadConflict and the
				// auto-download daemon.
				if !existingFileIsComplete(info, task.Size) {
					if stopped, rmErr := replace(ctx, localPath); stopped {
						return nil
					} else if rmErr != nil {
						return conflictFailed(fmt.Errorf("failed to remove incomplete existing file: %w", rmErr), false)
					}
					fmt.Fprintf(downloadUI.Writer(), "⚠️  Existing file %s is %d bytes, expected %d — re-downloading\n",
						task.Name, info.Size(), task.Size)
					break // leave the switch and download the file
				}
				downloadMutex.Lock()
				result.FilesSkipped++
				downloadMutex.Unlock()
				return nil
			case DownloadOverwriteOnce, DownloadOverwriteAll:
				if stopped, err := replace(ctx, localPath); stopped {
					return nil
				} else if err != nil {
					return conflictFailed(fmt.Errorf("failed to remove existing file: %w", err), false)
				}
			}
		}

		fileBar := downloadUI.AddFileBar(item.idx+1, task.FileID, task.Name, localPath, task.Size)

		transferHandle := cliTransferMgr.AllocateTransfer(task.Size, numWorkers)

		err := downloadFileFn(ctx, download.DownloadParams{
			FileID:         task.FileID,
			FileInfo:       task.CloudFile,
			LocalPath:      localPath,
			APIClient:      apiClient,
			TransferHandle: transferHandle,
			ProgressCallback: func(fraction float64) {
				fileBar.UpdateProgress(fraction)
			},
			OnRetry:      retryReporter(fileBar, downloadUI.Writer()),
			SkipChecksum: skipChecksum,
		})
		transferHandle.Complete()

		if err != nil {
			fileBar.Complete(err)

			if state.DownloadResumeStateExists(localPath) {
				fmt.Fprintf(downloadUI.Writer(), "\n💡 Resume state saved for %s. To resume, re-run the download command.\n", filepath.Base(localPath))
			}
			return fail(localPath, task.FileID, err, false)
		}

		logger.Info().
			Str("file_id", task.FileID).
			Str("path", localPath).
			Msg("File downloaded successfully")

		fileBar.Complete(nil)

		downloadMutex.Lock()
		result.FilesDownloaded++
		result.TotalBytes += task.Size
		downloadMutex.Unlock()
		return nil
	})
	_ = batchResult // errors collected via errChan for DownloadError tracking

	close(errChan)
	for downloadErr := range errChan {
		result.Errors = append(result.Errors, downloadErr)
	}
	// Each error is a file that failed, a conflict it could not settle included,
	// and RunBatch starts no file once the download is cancelled.
	result.FilesFailed = len(result.Errors)
	result.FilesNotStarted = len(allFiles) - result.FilesDownloaded - result.FilesSkipped - result.FilesFailed

	return result, nil
}

// folderDownloadWorkItem wraps scan.RemoteFileTask with index for BatchExecutor.
// Implements transfer.WorkItem.
type folderDownloadWorkItem struct {
	idx  int
	task scan.RemoteFileTask
}

// FileSize implements transfer.WorkItem.
func (f folderDownloadWorkItem) FileSize() int64 { return f.task.Size }

// performDryRunAnalysis analyzes what would happen during download without actually downloading
func performDryRunAnalysis(
	rootOutputDir string,
	allFolders []scan.RemoteFolderInfo,
	allFiles []scan.RemoteFileTask,
	overwriteAll bool,
	skipAll bool,
	mergeAll bool,
) (*DownloadResult, error) {
	result := &DownloadResult{
		Errors: make([]DownloadError, 0),
	}

	fmt.Println("🔍 DRY-RUN MODE - Analyzing what would happen...")
	fmt.Println()

	// Track statistics
	var foldersToCreate, foldersExisting, foldersSkipped int
	var filesToDownload, filesExisting, filesSkipped, filesToOverwrite int
	var totalBytes int64

	// Check root folder
	if info, err := os.Stat(rootOutputDir); err == nil && info.IsDir() {
		foldersExisting++
		if skipAll {
			fmt.Printf("⊘ Would SKIP root folder (exists): %s\n", rootOutputDir)
			fmt.Println("\n" + strings.Repeat("=", 60))
			fmt.Println("📊 Dry-Run Summary (NOTHING WOULD BE DOWNLOADED)")
			fmt.Println(strings.Repeat("=", 60))
			fmt.Println("  Root folder would be skipped - entire download cancelled")
			fmt.Println(strings.Repeat("=", 60))
			return result, nil
		} else if mergeAll {
			fmt.Printf("⊕ Would MERGE into root folder: %s\n", rootOutputDir)
		} else if overwriteAll {
			fmt.Printf("⊕ Would MERGE into root folder: %s (overwrite files)\n", rootOutputDir)
		}
	} else {
		foldersToCreate++
		fmt.Printf("✓ Would CREATE root folder: %s\n", rootOutputDir)
	}

	// Check folders
	for _, folder := range allFolders {
		localPath := filepath.Join(rootOutputDir, folder.RelativePath)
		if info, err := os.Stat(localPath); err == nil && info.IsDir() {
			foldersExisting++
			if skipAll {
				foldersSkipped++
				// Don't print every skipped folder, just count them
			}
			// Merge/overwrite modes will use existing folder
		} else {
			foldersToCreate++
		}
	}

	// Check files
	for _, file := range allFiles {
		if file.Err != nil {
			fmt.Printf("✗ Would fail: %v\n", file.Err)
			result.Errors = append(result.Errors, DownloadError{FilePath: filepath.Join(rootOutputDir, file.RelativePath), FileID: file.FileID, Error: file.Err})
			continue
		}
		localPath := filepath.Join(rootOutputDir, file.RelativePath)
		if _, err := os.Stat(localPath); err == nil {
			filesExisting++
			if skipAll || mergeAll {
				filesSkipped++
			} else if overwriteAll {
				filesToOverwrite++
				filesToDownload++
				totalBytes += file.Size
			}
		} else {
			filesToDownload++
			totalBytes += file.Size
		}
	}

	// Print summary
	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("📊 Dry-Run Summary")
	fmt.Println(strings.Repeat("=", 60))

	// Folders
	fmt.Printf("\n📁 Folders:\n")
	fmt.Printf("  Would create:     %d\n", foldersToCreate)
	if foldersExisting > 0 {
		if skipAll {
			fmt.Printf("  Would skip:       %d (already exist)\n", foldersExisting)
		} else {
			fmt.Printf("  Would merge into: %d (already exist)\n", foldersExisting)
		}
	}

	// Files
	fmt.Printf("\n📄 Files:\n")
	fmt.Printf("  Would download:   %d\n", filesToDownload)
	if filesSkipped > 0 {
		fmt.Printf("  Would skip:       %d (already exist)\n", filesSkipped)
	}
	if filesToOverwrite > 0 {
		fmt.Printf("  Would overwrite:  %d (already exist)\n", filesToOverwrite)
	}
	if len(result.Errors) > 0 {
		fmt.Printf("  Would fail:       %d (the download would exit 1; a dry run exits 0)\n", len(result.Errors))
	}

	// Size
	if totalBytes > 0 {
		fmt.Printf("\n💾 Total data to download: %.2f MB\n", float64(totalBytes)/(1024*1024))
	}

	// Conflict mode reminder
	fmt.Printf("\n🔧 Conflict mode: ")
	if skipAll {
		fmt.Println("SKIP (skip existing files/folders)")
	} else if mergeAll {
		fmt.Println("MERGE (merge folders, skip existing files)")
	} else if overwriteAll {
		fmt.Println("OVERWRITE (merge folders, overwrite existing files)")
	} else {
		fmt.Println("PROMPT (would ask for each conflict)")
	}

	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("\n✅ Dry-run complete. No files were downloaded.")
	fmt.Println("   Remove --dry-run to perform the actual download.")

	// Populate result for consistency
	result.FoldersCreated = foldersToCreate
	result.FilesDownloaded = 0 // Dry-run doesn't download
	result.FilesSkipped = filesSkipped
	result.FilesFailed = len(result.Errors) // predicted, with their reasons in Errors
	result.TotalBytes = 0

	return result, nil
}
