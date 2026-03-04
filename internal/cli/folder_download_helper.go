package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/validation"
)

// DownloadResult tracks what happened during download
type DownloadResult struct {
	FoldersCreated  int
	FilesDownloaded int
	FilesSkipped    int
	FilesFailed     int
	TotalBytes      int64
	Errors          []DownloadError
}

// DownloadError tracks failed downloads
type DownloadError struct {
	FilePath string
	FileID   string
	Error    error
}

// DownloadFolderRecursive recursively downloads a folder and all its contents
// Exported for GUI reuse
// folderName: optional name for the downloaded folder. If empty, uses folderID.
// v4.8.1: Added resourceMgr parameter — must be created via CreateResourceManager()
// at the command entrypoint, not constructed internally. Passing nil will panic.
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
		// No flags specified - check if we can prompt
		if !IsTerminal() {
			return nil, fmt.Errorf("conflict handling mode required in non-interactive mode: use --skip, --overwrite, or --merge")
		}
		// Prompt user for mode selection
		mode, err := promptFolderDownloadMode()
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

	// v4.8.1: Use shared ConflictResolvers instead of inline state machines
	fileConflictResolver := NewDownloadConflictResolver(initialFileMode)
	folderConflictResolver := NewFolderDownloadConflictResolver(initialFolderMode)

	// Scan the remote folder structure with live progress
	fmt.Println("📡 Scanning remote folder structure...")
	allFolders, allFiles, err := ScanRemoteFolderRecursiveWithProgress(ctx, apiClient, folderID, "",
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
			return promptFolderDownloadConflict(rootFolderName, rootOutputDir)
		})
		if err != nil {
			return nil, err
		}
		// Cascade folder "All" decision to file conflict mode
		if action == FolderDownloadSkipAll || action == FolderDownloadMergeAll {
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
	foldersCreated := 0
	foldersSkipped := 0
	for _, folder := range allFolders {
		// Validate folder path to prevent escaping output directory
		if err := validation.ValidatePathInDirectory(folder.RelativePath, rootOutputDir); err != nil {
			return nil, fmt.Errorf("invalid folder path from API: %w", err)
		}
		localPath := filepath.Join(rootOutputDir, folder.RelativePath)

		// Check if folder already exists
		if info, statErr := os.Stat(localPath); statErr == nil && info.IsDir() {
			action, err := folderConflictResolver.Resolve(func() (FolderDownloadConflictAction, error) {
				return promptFolderDownloadConflict(folder.Name, localPath)
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
				// Folder exists and we're merging - just continue (folder already there)
				foldersCreated++ // Count as "handled"
				continue
			}
		}

		// Create new folder
		if err := os.MkdirAll(localPath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", localPath, err)
		}
		foldersCreated++
	}
	result.FoldersCreated = foldersCreated
	if foldersSkipped > 0 {
		fmt.Printf("✓ Handled %d directories (%d created, %d merged/skipped)\n", foldersCreated+foldersSkipped, foldersCreated-foldersSkipped, foldersSkipped)
	} else {
		fmt.Printf("✓ Created %d local directories\n", foldersCreated)
	}

	// Download all files
	fmt.Println("\n📥 Downloading files...")
	downloadUI := progress.NewDownloadUI(len(allFiles))

	// NOTE: Do NOT redirect zerolog through downloadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.

	defer downloadUI.Wait()

	var downloadMutex sync.Mutex

	errChan := make(chan DownloadError, len(allFiles))

	// Create cancelable context for stopping on error when !continueOnError
	downloadCtx, cancelDownload := context.WithCancel(ctx)
	defer cancelDownload()
	var cancelled atomic.Bool

	// v4.8.1: Use passed-in resource manager (must be from CreateResourceManager())
	if resourceMgr == nil {
		panic("DownloadFolderRecursive: resourceMgr is required (use CreateResourceManager())")
	}
	cliTransferMgr := transfer.NewManager(resourceMgr)

	// Build work items for BatchExecutor
	items := make([]folderDownloadWorkItem, len(allFiles))
	for i, fileTask := range allFiles {
		items[i] = folderDownloadWorkItem{idx: i, task: fileTask}
	}

	cfg := transfer.BatchConfig{
		MaxWorkers:  maxConcurrent,
		ResourceMgr: resourceMgr,
		Label:       "FOLDER-DOWNLOAD",
	}
	numWorkers := transfer.ComputedWorkers(items, cfg)
	fmt.Printf("  Workers: %d (adaptive, based on file sizes)\n", numWorkers)

	batchResult := transfer.RunBatch(downloadCtx, items, cfg, func(ctx context.Context, item folderDownloadWorkItem) error {
		task := item.task

		// Check if download was cancelled
		select {
		case <-ctx.Done():
			downloadMutex.Lock()
			result.FilesSkipped++
			downloadMutex.Unlock()
			return nil
		default:
		}

		localPath := filepath.Join(rootOutputDir, task.RelativePath)

		// Check if path exists as a directory (name collision with folder)
		if info, statErr := os.Stat(localPath); statErr == nil && info.IsDir() {
			originalPath := localPath
			localPath = localPath + ".file"
			logger.Warn().
				Str("original_path", originalPath).
				Str("renamed_to", localPath).
				Msg("File name conflicts with existing directory, renaming file")
		}

		// Check if file exists and handle conflict
		if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
			action, err := fileConflictResolver.Resolve(func() (DownloadConflictAction, error) {
				return promptDownloadConflict(task.Name, localPath)
			})
			if err != nil {
				errChan <- DownloadError{FilePath: localPath, FileID: task.FileID, Error: err}
				return nil
			}

			switch action {
			case DownloadSkipOnce, DownloadSkipAll:
				downloadMutex.Lock()
				result.FilesSkipped++
				downloadMutex.Unlock()
				return nil
			case DownloadAbort:
				errChan <- DownloadError{FilePath: localPath, FileID: task.FileID, Error: fmt.Errorf("download aborted by user")}
				return nil
			case DownloadOverwriteOnce, DownloadOverwriteAll:
				if err := os.Remove(localPath); err != nil {
					errChan <- DownloadError{FilePath: localPath, FileID: task.FileID, Error: fmt.Errorf("failed to remove existing file: %w", err)}
					return nil
				}
			}
		}

		fileBar := downloadUI.AddFileBar(item.idx+1, task.FileID, task.Name, localPath, task.Size)

		// v4.8.1: Pass adaptive worker count for correct per-file thread allocation
		transferHandle := cliTransferMgr.AllocateTransfer(task.Size, numWorkers)

		err := download.DownloadFile(ctx, download.DownloadParams{
			FileID:         task.FileID,
			FileInfo:       task.CloudFile,
			LocalPath:      localPath,
			APIClient:      apiClient,
			TransferHandle: transferHandle,
			ProgressCallback: func(fraction float64) {
				fileBar.UpdateProgress(fraction)
			},
			SkipChecksum: skipChecksum,
		})
		transferHandle.Complete()

		if err != nil {
			fileBar.Complete(err)

			if state.DownloadResumeStateExists(localPath) {
				fmt.Fprintf(os.Stderr, "\n💡 Resume state saved for %s. To resume, re-run the download command.\n", filepath.Base(localPath))
			}

			downloadMutex.Lock()
			result.FilesFailed++
			downloadMutex.Unlock()
			errChan <- DownloadError{FilePath: localPath, FileID: task.FileID, Error: err}

			if !continueOnError {
				if !cancelled.Swap(true) {
					cancelDownload()
				}
			}
			return nil
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

	return result, nil
}

// RemoteFolderInfo represents a folder in the remote structure
type RemoteFolderInfo struct {
	FolderID     string
	Name         string
	RelativePath string
}

// RemoteFileTask represents a file to download
type RemoteFileTask struct {
	FileID       string
	Name         string
	RelativePath string
	Size         int64
	CloudFile    *models.CloudFile // v4.8.0: Pre-fetched full metadata (nil = fallback to GetFileInfo)
}

// folderDownloadWorkItem wraps RemoteFileTask with index for BatchExecutor.
// Implements transfer.WorkItem.
type folderDownloadWorkItem struct {
	idx  int
	task RemoteFileTask
}

// FileSize implements transfer.WorkItem.
func (f folderDownloadWorkItem) FileSize() int64 { return f.task.Size }

// ScanRemoteFolderRecursive recursively scans a remote folder structure
// Exported for GUI reuse
func ScanRemoteFolderRecursive(
	ctx context.Context,
	apiClient *api.Client,
	folderID string,
	relativePath string,
) ([]RemoteFolderInfo, []RemoteFileTask, error) {
	folders := make([]RemoteFolderInfo, 0)
	files := make([]RemoteFileTask, 0)

	// Get folder contents (all pages — critical for folders with >2000 items)
	contents, err := apiClient.ListFolderContentsAll(ctx, folderID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list folder contents: %w", err)
	}

	// Process subfolders
	for _, folder := range contents.Folders {
		folderRelPath := filepath.Join(relativePath, folder.Name)
		folders = append(folders, RemoteFolderInfo{
			FolderID:     folder.ID,
			Name:         folder.Name,
			RelativePath: folderRelPath,
		})

		// Recursively scan subfolder
		subFolders, subFiles, err := ScanRemoteFolderRecursive(ctx, apiClient, folder.ID, folderRelPath)
		if err != nil {
			return nil, nil, err
		}
		folders = append(folders, subFolders...)
		files = append(files, subFiles...)
	}

	// Process files
	for _, file := range contents.Files {
		// Validate filename from API to prevent path traversal
		if err := validation.ValidateFilename(file.Name); err != nil {
			return nil, nil, fmt.Errorf("invalid filename from API: %w", err)
		}
		fileRelPath := filepath.Join(relativePath, file.Name)
		files = append(files, RemoteFileTask{
			FileID:       file.ID,
			Name:         file.Name,
			RelativePath: fileRelPath,
			Size:         file.DecryptedSize,
			CloudFile:    file.ToCloudFile(), // v4.8.0: Pre-fetched metadata (nil if incomplete)
		})
	}

	return folders, files, nil
}

// ScanRemoteFolderRecursiveWithProgress is like ScanRemoteFolderRecursive but calls
// onProgress after each subfolder is scanned, enabling live scan feedback in CLI.
// v4.8.0: Added for scan progress feedback (Phase 6).
func ScanRemoteFolderRecursiveWithProgress(
	ctx context.Context,
	apiClient *api.Client,
	folderID string,
	relativePath string,
	onProgress func(foldersFound, filesFound int, bytesFound int64),
) ([]RemoteFolderInfo, []RemoteFileTask, error) {
	return scanRemoteFolderRecursiveImpl(ctx, apiClient, folderID, relativePath, onProgress)
}

// scanRemoteFolderRecursiveImpl is the shared implementation for both scan variants.
func scanRemoteFolderRecursiveImpl(
	ctx context.Context,
	apiClient *api.Client,
	folderID string,
	relativePath string,
	onProgress func(foldersFound, filesFound int, bytesFound int64),
) ([]RemoteFolderInfo, []RemoteFileTask, error) {
	folders := make([]RemoteFolderInfo, 0)
	files := make([]RemoteFileTask, 0)

	contents, err := apiClient.ListFolderContentsAll(ctx, folderID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list folder contents: %w", err)
	}

	// Process subfolders
	for _, folder := range contents.Folders {
		folderRelPath := filepath.Join(relativePath, folder.Name)
		folders = append(folders, RemoteFolderInfo{
			FolderID:     folder.ID,
			Name:         folder.Name,
			RelativePath: folderRelPath,
		})

		subFolders, subFiles, err := scanRemoteFolderRecursiveImpl(ctx, apiClient, folder.ID, folderRelPath, onProgress)
		if err != nil {
			return nil, nil, err
		}
		folders = append(folders, subFolders...)
		files = append(files, subFiles...)
	}

	// Process files
	for _, file := range contents.Files {
		if err := validation.ValidateFilename(file.Name); err != nil {
			return nil, nil, fmt.Errorf("invalid filename from API: %w", err)
		}
		fileRelPath := filepath.Join(relativePath, file.Name)
		files = append(files, RemoteFileTask{
			FileID:       file.ID,
			Name:         file.Name,
			RelativePath: fileRelPath,
			Size:         file.DecryptedSize,
			CloudFile:    file.ToCloudFile(),
		})
	}

	// Report progress after processing this folder
	if onProgress != nil {
		var totalBytes int64
		for _, f := range files {
			totalBytes += f.Size
		}
		onProgress(len(folders), len(files), totalBytes)
	}

	return folders, files, nil
}

// ScanEvent represents a single discovery from the streaming scanner.
// v4.8.0: Used by ScanRemoteFolderStreaming for incremental file discovery.
type ScanEvent struct {
	Folder *RemoteFolderInfo // Non-nil for folder discovery
	File   *RemoteFileTask  // Non-nil for file discovery
}

// ScanProgress reports cumulative scan progress.
// v4.8.0: Used by streaming scanner progress callback.
type ScanProgress struct {
	FoldersFound int
	FilesFound   int
	BytesFound   int64
}

// ScanRemoteFolderStreaming scans a remote folder structure concurrently,
// emitting files and folders as they are discovered rather than waiting for
// the entire scan to complete. Downloads can begin within seconds.
//
// Returns a channel of ScanEvents (closed when scan completes) and an error channel.
// The error channel receives at most one error, then is closed.
// v4.8.0: Streaming scan architecture for immediate download start.
func ScanRemoteFolderStreaming(
	ctx context.Context,
	apiClient *api.Client,
	folderID string,
	onProgress func(ScanProgress),
) (<-chan ScanEvent, <-chan error) {
	eventCh := make(chan ScanEvent, constants.DispatchChannelBuffer)
	errCh := make(chan error, 1)

	go func() {
		defer close(eventCh)
		defer close(errCh)

		var progress ScanProgress
		var mu sync.Mutex // protects progress

		// Work queue for subfolder scanning
		type scanWork struct {
			folderID     string
			relativePath string
		}

		workCh := make(chan scanWork, constants.DispatchChannelBuffer)
		var wg sync.WaitGroup

		// Seed with root folder
		wg.Add(1)
		workCh <- scanWork{folderID: folderID, relativePath: ""}

		// Bounded subfolder workers (8 concurrent scanners)
		const numScanWorkers = 8
		scanErrOnce := sync.Once{}

		for i := 0; i < numScanWorkers; i++ {
			go func() {
				for work := range workCh {
					// Check for cancellation
					select {
					case <-ctx.Done():
						wg.Done()
						continue
					default:
					}

					// v4.8.2: Stream pages — emit files/folders as each API page arrives
					// instead of waiting for the entire folder to be enumerated.
					err := apiClient.ListFolderContentsStreaming(ctx, work.folderID,
						func(folders []api.FolderInfo, files []api.FileInfo) error {
							// Emit folders first (so parent dirs can be created before files)
							for _, folder := range folders {
								folderRelPath := filepath.Join(work.relativePath, folder.Name)
								info := RemoteFolderInfo{
									FolderID:     folder.ID,
									Name:         folder.Name,
									RelativePath: folderRelPath,
								}

								select {
								case eventCh <- ScanEvent{Folder: &info}:
								case <-ctx.Done():
									return ctx.Err()
								}

								mu.Lock()
								progress.FoldersFound++
								mu.Unlock()

								// Enqueue subfolder for scanning
								wg.Add(1)
								select {
								case workCh <- scanWork{folderID: folder.ID, relativePath: folderRelPath}:
								case <-ctx.Done():
									wg.Add(-1) // Undo the Add since we won't process it
									return ctx.Err()
								}
							}

							// Emit files
							for _, file := range files {
								if err := validation.ValidateFilename(file.Name); err != nil {
									continue // Skip invalid filenames
								}
								fileRelPath := filepath.Join(work.relativePath, file.Name)
								task := RemoteFileTask{
									FileID:       file.ID,
									Name:         file.Name,
									RelativePath: fileRelPath,
									Size:         file.DecryptedSize,
									CloudFile:    file.ToCloudFile(),
								}

								select {
								case eventCh <- ScanEvent{File: &task}:
								case <-ctx.Done():
									return ctx.Err()
								}

								mu.Lock()
								progress.FilesFound++
								progress.BytesFound += file.DecryptedSize
								mu.Unlock()
							}

							return nil
						},
					)
					if err != nil {
						// v4.8.2: Don't report context cancellation as a scan error — it's a clean cancel
						if ctx.Err() != nil {
							wg.Done()
							continue
						}
						scanErrOnce.Do(func() {
							errCh <- fmt.Errorf("failed to list folder %s: %w", work.folderID, err)
						})
						wg.Done()
						continue
					}

					// Report progress after this folder
					if onProgress != nil {
						mu.Lock()
						p := progress
						mu.Unlock()
						onProgress(p)
					}

					wg.Done()
				}
			}()
		}

		// Wait for all folder scanning to complete, then close work channel
		wg.Wait()
		close(workCh)
	}()

	return eventCh, errCh
}

// performDryRunAnalysis analyzes what would happen during download without actually downloading
func performDryRunAnalysis(
	rootOutputDir string,
	allFolders []RemoteFolderInfo,
	allFiles []RemoteFileTask,
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
	result.TotalBytes = 0

	return result, nil
}
