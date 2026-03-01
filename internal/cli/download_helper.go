package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/util/filter"
	"github.com/rescale/rescale-int/internal/util/paths"
	"github.com/rescale/rescale-int/internal/validation"
)

// Test seams for CLI-level integration testing.
// These are package-level function variables that default to the real implementations
// but can be overridden in tests.
var (
	listJobFilesFn = func(ctx context.Context, apiClient *api.Client, jobID string) ([]models.JobFile, error) {
		return apiClient.ListJobFiles(ctx, jobID)
	}
	downloadFileFn = func(ctx context.Context, params download.DownloadParams) error {
		return download.DownloadFile(ctx, params)
	}
)

// cliDownloadItem wraps a file for download with index info.
// Implements transfer.WorkItem for BatchExecutor.
type cliDownloadItem struct {
	idx        int    // 0-based index in the batch
	fileID     string // Rescale file ID
	name       string // display name
	size       int64  // decrypted size
	localPath  string // resolved output path
	cloudFile  *models.CloudFile
	jobFile    *models.JobFile // non-nil for job downloads
}

// FileSize implements transfer.WorkItem.
func (d cliDownloadItem) FileSize() int64 { return d.size }

// executeFileDownload - Common download logic for both files download and download shortcut
//
// v3.2.3: Restructured to fix filename collision bug. Now fetches all file metadata
// first, resolves collisions using shared paths.ResolveCollisions(), then downloads.
// This ensures multiple files with the same name don't corrupt each other.
func executeFileDownload(
	ctx context.Context,
	fileIDs []string,
	outputDir string,
	maxConcurrent int,
	overwriteAll bool,
	skipAll bool,
	resumeAll bool,
	skipChecksum bool,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	if len(fileIDs) == 0 {
		return fmt.Errorf("at least one file ID is required")
	}

	if outputDir == "" {
		outputDir = "."
	}

	logger.Info().
		Int("count", len(fileIDs)).
		Str("outdir", outputDir).
		Msg("Starting file download")

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	fmt.Printf("Fetching metadata for %d file(s)...\n", len(fileIDs))

	// PHASE 1: Fetch all file metadata first (v3.2.3 collision fix)
	// This allows us to detect filename collisions before downloading.
	type fileMetadata struct {
		ID            string
		Name          string
		DecryptedSize int64
		CloudFile     *models.CloudFile
	}
	fileMetadataList := make([]fileMetadata, len(fileIDs))
	metadataErrors := make([]error, len(fileIDs))

	// Use semaphore to limit concurrent metadata fetches
	metaSemaphore := make(chan struct{}, maxConcurrent)
	var metaWg sync.WaitGroup

	for i, fileID := range fileIDs {
		metaWg.Add(1)
		go func(idx int, fid string) {
			defer metaWg.Done()

			// Acquire semaphore
			metaSemaphore <- struct{}{}
			defer func() { <-metaSemaphore }()

			// Get file metadata
			fileInfo, err := apiClient.GetFileInfo(ctx, fid)
			if err != nil {
				metadataErrors[idx] = fmt.Errorf("failed to get file info for %s: %w", fid, err)
				return
			}

			// Validate filename from API to prevent path traversal
			if err := validation.ValidateFilename(fileInfo.Name); err != nil {
				metadataErrors[idx] = fmt.Errorf("invalid filename from API for file %s: %w", fid, err)
				return
			}

			fileMetadataList[idx] = fileMetadata{
				ID:            fid,
				Name:          fileInfo.Name,
				DecryptedSize: fileInfo.DecryptedSize,
				CloudFile:     fileInfo,
			}
		}(i, fileID)
	}
	metaWg.Wait()

	// Check for metadata fetch errors
	var validFiles []fileMetadata
	for i, meta := range fileMetadataList {
		if metadataErrors[i] != nil {
			fmt.Printf("⚠️  %v\n", metadataErrors[i])
			continue
		}
		if meta.ID != "" {
			validFiles = append(validFiles, meta)
		}
	}

	if len(validFiles) == 0 {
		return fmt.Errorf("no valid files to download")
	}

	// PHASE 2: Build file list and resolve collisions using shared utility
	downloadFiles := make([]paths.FileForDownload, len(validFiles))
	for i, meta := range validFiles {
		downloadFiles[i] = paths.FileForDownload{
			FileID:    meta.ID,
			Name:      meta.Name,
			LocalPath: filepath.Join(outputDir, meta.Name),
			Size:      meta.DecryptedSize,
		}
	}

	// Resolve filename collisions (v3.2.3: uses shared utility for consistency with GUI)
	downloadFiles, collisionCount := paths.ResolveCollisions(downloadFiles)
	if collisionCount > 0 {
		fmt.Printf("⚠️  Found %d files with duplicate names. File IDs will be appended to ensure unique downloads.\n", collisionCount)
	}

	// Build map from file ID to resolved path
	fileIDToPath := make(map[string]string)
	fileIDToMeta := make(map[string]fileMetadata)
	for i, df := range downloadFiles {
		fileIDToPath[df.FileID] = df.LocalPath
		fileIDToMeta[df.FileID] = validFiles[i]
	}

	fmt.Printf("Downloading %d file(s) to: %s\n\n", len(validFiles), outputDir)

	// Create DownloadUI for professional progress bars
	downloadUI := progress.NewDownloadUI(len(validFiles))

	// NOTE: Do NOT redirect zerolog through downloadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.

	defer downloadUI.Wait()

	downloadedFiles := make([]string, 0, len(validFiles))
	skippedFiles := make([]string, 0)
	var downloadMutex sync.Mutex

	// v4.8.1: Use shared ConflictResolver instead of inline state machine
	initialConflictMode := DownloadSkipOnce
	if overwriteAll {
		initialConflictMode = DownloadOverwriteAll
	} else if skipAll {
		initialConflictMode = DownloadSkipAll
	} else if resumeAll {
		initialConflictMode = DownloadResumeAll
	}
	conflictResolver := NewDownloadConflictResolver(initialConflictMode)

	// Create resource manager from global flags
	resourceMgr := CreateResourceManager()
	transferMgr := transfer.NewManager(resourceMgr)

	// v4.8.1: Build work items for BatchExecutor
	items := make([]cliDownloadItem, len(downloadFiles))
	for i, df := range downloadFiles {
		meta := fileIDToMeta[df.FileID]
		items[i] = cliDownloadItem{
			idx:       i,
			fileID:    df.FileID,
			name:      meta.Name,
			size:      meta.DecryptedSize,
			localPath: df.LocalPath,
			cloudFile: meta.CloudFile,
		}
	}

	cfg := transfer.BatchConfig{
		MaxWorkers:  maxConcurrent,
		ResourceMgr: resourceMgr,
		Label:       "FILE-DOWNLOAD",
	}
	numWorkers := transfer.ComputedWorkers(items, cfg)

	// PHASE 3: Download each file concurrently using BatchExecutor
	batchResult := transfer.RunBatch(ctx, items, cfg, func(ctx context.Context, item cliDownloadItem) error {
		outputPath := item.localPath

		// Check if path exists as a directory (name collision with folder)
		if info, statErr := os.Stat(outputPath); statErr == nil && info.IsDir() {
			originalPath := outputPath
			outputPath = outputPath + ".file"
			fmt.Fprintf(downloadUI.Writer(), "⚠️  File '%s' conflicts with directory, downloading as '%s'\n",
				filepath.Base(originalPath), filepath.Base(outputPath))
		}

		// Check if file exists and handle conflict
		if info, err := os.Stat(outputPath); err == nil && !info.IsDir() {
			action, err := conflictResolver.Resolve(func() (DownloadConflictAction, error) {
				return promptDownloadConflict(item.name, outputPath)
			})
			if err != nil {
				return fmt.Errorf("conflict prompt failed: %w", err)
			}

			switch action {
			case DownloadSkipOnce, DownloadSkipAll:
				downloadMutex.Lock()
				skippedFiles = append(skippedFiles, outputPath)
				downloadMutex.Unlock()
				return nil
			case DownloadAbort:
				return fmt.Errorf("download aborted by user")
			case DownloadOverwriteOnce, DownloadOverwriteAll:
				if err := os.Remove(outputPath); err != nil {
					return fmt.Errorf("failed to remove existing file: %w", err)
				}
			case DownloadResumeOnce, DownloadResumeAll:
				encryptedPath := outputPath + ".encrypted"
				encryptedInfo, encErr := os.Stat(encryptedPath)
				_, outErr := os.Stat(outputPath)

				minEncryptedSize := item.size + 1
				maxEncryptedSize := item.size + 16

				if encErr == nil && encryptedInfo.Size() >= minEncryptedSize && encryptedInfo.Size() <= maxEncryptedSize {
					fmt.Fprintf(downloadUI.Writer(), "✓ Encrypted file complete (%d bytes), retrying decryption for %s...\n",
						encryptedInfo.Size(), item.name)
					if outErr == nil {
						os.Remove(outputPath)
					}
				} else {
					resumeState, _ := state.LoadDownloadState(outputPath)
					if resumeState != nil {
						if err := state.ValidateDownloadState(resumeState, outputPath); err == nil {
							resumeProgress := state.GetDownloadResumeProgress(resumeState)
							fmt.Fprintf(downloadUI.Writer(), "↻ Resuming download for %s from %.1f%% (%d/%d bytes)...\n",
								item.name, resumeProgress*100, resumeState.DownloadedBytes, resumeState.TotalSize)
							if outErr == nil {
								os.Remove(outputPath)
							}
						} else {
							fmt.Fprintf(downloadUI.Writer(), "Resume state invalid for %s (reason: %v). Starting fresh download...\n",
								item.name, err)
							state.CleanupExpiredDownloadResume(resumeState, outputPath, false)
							os.Remove(outputPath)
						}
					} else {
						if encErr == nil {
							fmt.Fprintf(downloadUI.Writer(), "Encrypted file has unexpected size (%d bytes, expected %d-%d bytes). Starting fresh download for %s...\n",
								encryptedInfo.Size(), minEncryptedSize, maxEncryptedSize, item.name)
							os.Remove(encryptedPath)
						}
						os.Remove(outputPath)
					}
				}
			}
		}

		fmt.Fprintf(downloadUI.Writer(), "[%d/%d] Preparing to download %s...\n", item.idx+1, len(downloadFiles), item.name)

		// v4.8.1: Pass adaptive worker count for correct per-file thread allocation
		transferHandle := transferMgr.AllocateTransfer(item.size, numWorkers)

		if transferHandle.GetThreads() > 1 && item.size > 100*1024*1024 {
			fmt.Fprintf(downloadUI.Writer(), "Using %d concurrent threads for %s\n",
				transferHandle.GetThreads(), item.name)
		}

		var fileBar *progress.DownloadFileBar
		var barOnce sync.Once

		err := downloadFileFn(ctx, download.DownloadParams{
			FileID:    item.fileID,
			LocalPath: outputPath,
			APIClient: apiClient,
			ProgressCallback: func(fraction float64) {
				barOnce.Do(func() {
					fileBar = downloadUI.AddFileBar(item.idx+1, item.fileID, item.name, outputPath, item.size)
				})
				if fileBar != nil {
					fileBar.UpdateProgress(fraction)
				}
			},
			TransferHandle: transferHandle,
			SkipChecksum:   skipChecksum,
		})

		if err != nil {
			if fileBar == nil {
				fileBar = downloadUI.AddFileBar(item.idx+1, item.fileID, item.name, outputPath, item.size)
			}
			fileBar.Complete(err)

			if state.DownloadResumeStateExists(outputPath) {
				fmt.Fprintf(os.Stderr, "\n💡 Resume state saved for %s. To resume this download, run the same command again.\n", item.name)
			}

			storageType := "unknown"
			if item.cloudFile != nil && item.cloudFile.Storage != nil {
				storageType = item.cloudFile.Storage.StorageType
			}
			logger.Debug().Str("error", sanitizeErrorString(err.Error())).Str("file_id", item.fileID).Str("file_name", item.name).Msg("download failed - full error chain for debugging")
			return formatDownloadError(item.name, item.fileID, "", storageType, err)
		}

		logger.Info().
			Str("file_id", item.fileID).
			Str("path", outputPath).
			Msg("File downloaded successfully")

		if fileBar == nil {
			fileBar = downloadUI.AddFileBar(item.idx+1, item.fileID, item.name, outputPath, item.size)
		}
		fileBar.Complete(nil)

		downloadMutex.Lock()
		downloadedFiles = append(downloadedFiles, outputPath)
		downloadMutex.Unlock()
		return nil
	})

	// Collect errors from batch result
	errors := batchResult.Errors

	// Print summary
	if len(errors) > 0 {
		fmt.Printf("\n✓ Successfully downloaded %d file(s)\n", len(downloadedFiles))
		if len(skippedFiles) > 0 {
			fmt.Printf("⊘ Skipped %d file(s)\n", len(skippedFiles))
		}
		fmt.Printf("✗ Failed to download %d file(s)\n", len(errors))
		// Return first error but continue with others (per project objectives)
		return errors[0]
	}

	fmt.Printf("\n✓ Successfully downloaded %d file(s)\n", len(downloadedFiles))
	if len(skippedFiles) > 0 {
		fmt.Printf("⊘ Skipped %d file(s)\n", len(skippedFiles))
	}
	return nil
}

// executeJobDownload - Common download logic for job output files
//
// Performance Characteristics (v2.4.8):
//   - Concurrent downloads: maxConcurrent workers (default: 5)
//   - API calls per job: 1 (ListJobFiles only, GetStorageCredentials cached 10min)
//   - API calls per file: 0 (uses metadata from ListJobFiles, no GetFileInfo needed!)
//
// Rate Limiting (v2.4.8):
//   - ListJobFiles: GET /api/v2/jobs/{id}/files/ (jobs-usage scope: 20 req/sec target)
//   - 12.5x faster than v3 endpoint (was 1.6 req/sec in v2.4.6)
//   - GetStorageCredentials: POST /api/v3/credentials/ (user scope, but cached 10min)
//   - GetFileInfo: ELIMINATED (no longer called!)
//
// Performance Improvements over v2.4.6:
//   - v2.4.7: Switched ListJobFiles to v2 endpoint (jobs-usage scope, 12.5x faster)
//   - v2.4.8: Eliminated GetFileInfo calls entirely (saves 289 API calls for 289 files)
//   - Combined improvement: ~3+ minutes saved for 289 files
//   - ListJobFiles: instant (was 8+ seconds)
//   - GetFileInfo: eliminated (was ~180 seconds = 289 calls ÷ 1.6 req/sec)
//   - Total download time now limited primarily by S3/Azure transfer speed, not API calls!
//
// 2025-11-20: Switched ListJobFiles to v2 endpoint + eliminated GetFileInfo calls
// 2025-12-09: Fixed filename collision bug - files with same name now get unique paths
//
// This mirrors executeFileDownload but fetches files from a job instead
func executeJobDownload(
	ctx context.Context,
	jobID string,
	outputDir string,
	maxConcurrent int,
	overwriteAll bool,
	skipAll bool,
	resumeAll bool,
	skipChecksum bool,
	filterPatterns []string,
	excludePatterns []string,
	searchTerms []string,
	pathFilterPatterns []string,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	// List all job output files
	fmt.Printf("Fetching output files for job %s...\n", jobID)
	logger.Info().Str("job_id", jobID).Msg("Listing job output files")

	allFiles, err := listJobFilesFn(ctx, apiClient, jobID)
	if err != nil {
		return fmt.Errorf("failed to list job files: %w", err)
	}

	if len(allFiles) == 0 {
		fmt.Println("No output files found for this job")
		return nil
	}

	// Apply filters if any are specified
	files := allFiles
	if len(filterPatterns) > 0 || len(excludePatterns) > 0 || len(searchTerms) > 0 || len(pathFilterPatterns) > 0 {
		filterCfg := filter.Config{
			Include:     filterPatterns,
			Exclude:     excludePatterns,
			Search:      searchTerms,
			PathInclude: pathFilterPatterns,
		}
		files = filter.ApplyToJobFiles(allFiles, filterCfg)

		if len(files) == 0 {
			fmt.Println("No files match the specified filters")
			return nil
		}

		if len(files) < len(allFiles) {
			fmt.Printf("Filtered: %d of %d files match filters\n", len(files), len(allFiles))
		}
	}

	if outputDir == "" {
		outputDir = "."
	}

	// v3.2.3: Pre-compute output paths and detect filename collisions
	// Using shared paths.ResolveCollisions() utility for consistency with GUI and CLI.
	// When multiple files have the same name (e.g., from different job runs), we must
	// give them unique output paths to prevent concurrent download corruption.
	downloadFiles := make([]paths.FileForDownload, len(files))
	for i, file := range files {
		var basePath string
		if file.RelativePath != "" {
			// Validate relative path to prevent escaping output directory
			if validation.ValidatePathInDirectory(file.RelativePath, outputDir) == nil {
				basePath = filepath.Join(outputDir, file.RelativePath)
			} else {
				// Invalid path - use name only
				basePath = filepath.Join(outputDir, file.Name)
			}
		} else {
			basePath = filepath.Join(outputDir, file.Name)
		}
		downloadFiles[i] = paths.FileForDownload{
			FileID:    file.ID,
			Name:      file.Name,
			LocalPath: basePath,
			Size:      file.DecryptedSize,
		}
	}

	// Resolve filename collisions using shared utility
	downloadFiles, collisionCount := paths.ResolveCollisions(downloadFiles)
	if collisionCount > 0 {
		fmt.Printf("⚠️  Found %d files with duplicate names. File IDs will be appended to ensure unique downloads.\n", collisionCount)
	}

	// Build map from file ID to resolved path
	fileOutputPaths := make(map[string]string, len(downloadFiles))
	for _, df := range downloadFiles {
		fileOutputPaths[df.FileID] = df.LocalPath
	}

	logger.Info().
		Int("count", len(files)).
		Str("job_id", jobID).
		Str("outdir", outputDir).
		Msg("Starting job file download")

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	fmt.Printf("Downloading %d file(s) from job %s to: %s\n\n", len(files), jobID, outputDir)

	// Create DownloadUI for professional progress bars
	downloadUI := progress.NewDownloadUI(len(files))

	// NOTE: Do NOT redirect zerolog through downloadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.

	defer downloadUI.Wait()

	downloadedFiles := make([]string, 0, len(files))
	skippedFiles := make([]string, 0)
	var downloadMutex sync.Mutex

	// v4.8.1: Use shared ConflictResolver instead of inline state machine
	initialConflictMode := DownloadSkipOnce
	if overwriteAll {
		initialConflictMode = DownloadOverwriteAll
	} else if skipAll {
		initialConflictMode = DownloadSkipAll
	} else if resumeAll {
		initialConflictMode = DownloadResumeAll
	}
	conflictResolver := NewDownloadConflictResolver(initialConflictMode)

	// Create resource manager from global flags
	resourceMgr := CreateResourceManager()
	transferMgr := transfer.NewManager(resourceMgr)

	// v4.8.1: Build work items for BatchExecutor
	items := make([]cliDownloadItem, len(files))
	for i, file := range files {
		outputPath := fileOutputPaths[file.ID]
		if outputPath == "" {
			outputPath = filepath.Join(outputDir, file.Name)
		}
		jf := file // capture loop variable
		items[i] = cliDownloadItem{
			idx:       i,
			fileID:    file.ID,
			name:      file.Name,
			size:      file.DecryptedSize,
			localPath: outputPath,
			jobFile:   &jf,
		}
	}

	cfg := transfer.BatchConfig{
		MaxWorkers:  maxConcurrent,
		ResourceMgr: resourceMgr,
		Label:       "JOB-DOWNLOAD",
	}
	numWorkers := transfer.ComputedWorkers(items, cfg)

	// Download each file concurrently via BatchExecutor
	batchResult := transfer.RunBatch(ctx, items, cfg, func(ctx context.Context, item cliDownloadItem) error {
		outputPath := item.localPath

		// Ensure directory exists
		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", item.name, err)
		}

		// Check if path exists as a directory (name collision with folder)
		if info, statErr := os.Stat(outputPath); statErr == nil && info.IsDir() {
			originalPath := outputPath
			outputPath = outputPath + ".file"
			fmt.Fprintf(downloadUI.Writer(), "⚠️  File '%s' conflicts with directory, downloading as '%s'\n",
				filepath.Base(originalPath), filepath.Base(outputPath))
		}

		// Check if file already exists
		if info, err := os.Stat(outputPath); err == nil && !info.IsDir() {
			switch conflictResolver.Mode() {
			case DownloadSkipOnce, DownloadSkipAll:
				fmt.Fprintf(downloadUI.Writer(), "⊘ Skipping existing file: %s\n", item.name)
				downloadMutex.Lock()
				skippedFiles = append(skippedFiles, outputPath)
				downloadMutex.Unlock()
				return nil
			case DownloadAbort:
				return fmt.Errorf("download aborted by user")
			case DownloadOverwriteOnce, DownloadOverwriteAll:
				if err := os.Remove(outputPath); err != nil {
					return fmt.Errorf("failed to remove existing file: %w", err)
				}
			case DownloadResumeOnce, DownloadResumeAll:
				encryptedPath := outputPath + ".encrypted"
				encryptedInfo, encErr := os.Stat(encryptedPath)
				_, outErr := os.Stat(outputPath)

				minEncryptedSize := item.size + 1
				maxEncryptedSize := item.size + 16

				if encErr == nil && encryptedInfo.Size() >= minEncryptedSize && encryptedInfo.Size() <= maxEncryptedSize {
					fmt.Fprintf(downloadUI.Writer(), "✓ Encrypted file complete (%d bytes), retrying decryption for %s...\n",
						encryptedInfo.Size(), item.name)
					if outErr == nil {
						os.Remove(outputPath)
					}
				} else {
					resumeState, _ := state.LoadDownloadState(outputPath)
					if resumeState != nil {
						if err := state.ValidateDownloadState(resumeState, outputPath); err == nil {
							resumeProgress := state.GetDownloadResumeProgress(resumeState)
							fmt.Fprintf(downloadUI.Writer(), "↻ Resuming download for %s from %.1f%% (%d/%d bytes)...\n",
								item.name, resumeProgress*100, resumeState.DownloadedBytes, resumeState.TotalSize)
							if outErr == nil {
								os.Remove(outputPath)
							}
						} else {
							fmt.Fprintf(downloadUI.Writer(), "Resume state invalid for %s (reason: %v). Starting fresh download...\n",
								item.name, err)
							state.CleanupExpiredDownloadResume(resumeState, outputPath, false)
							os.Remove(outputPath)
						}
					} else {
						if encErr == nil {
							fmt.Fprintf(downloadUI.Writer(), "Encrypted file has unexpected size (%d bytes, expected %d-%d bytes). Starting fresh download for %s...\n",
								encryptedInfo.Size(), minEncryptedSize, maxEncryptedSize, item.name)
							os.Remove(encryptedPath)
						}
						os.Remove(outputPath)
					}
				}
			}
		}

		// v4.8.1: Pass adaptive worker count for correct per-file thread allocation
		transferHandle := transferMgr.AllocateTransfer(item.size, numWorkers)

		if transferHandle.GetThreads() > 1 && item.size > 100*1024*1024 {
			fmt.Fprintf(downloadUI.Writer(), "Using %d concurrent threads for %s\n",
				transferHandle.GetThreads(), item.name)
		}

		var fileBar *progress.DownloadFileBar
		var barOnce sync.Once

		cloudFile := item.jobFile.ToCloudFile()

		err = downloadFileFn(ctx, download.DownloadParams{
			FileInfo:  cloudFile,
			LocalPath: outputPath,
			APIClient: apiClient,
			ProgressCallback: func(fraction float64) {
				barOnce.Do(func() {
					fileBar = downloadUI.AddFileBar(item.idx+1, item.fileID, item.name, outputPath, item.size)
				})
				if fileBar != nil {
					fileBar.UpdateProgress(fraction)
				}
			},
			TransferHandle: transferHandle,
			SkipChecksum:   skipChecksum,
		})

		if err != nil {
			if fileBar == nil {
				fileBar = downloadUI.AddFileBar(item.idx+1, item.fileID, item.name, outputPath, item.size)
			}
			fileBar.Complete(err)

			if state.DownloadResumeStateExists(outputPath) {
				fmt.Fprintf(os.Stderr, "\n💡 Resume state saved for %s. To resume this download, run the same command again.\n", item.name)
			}

			storageType := "unknown"
			if item.jobFile.Storage != nil {
				storageType = item.jobFile.Storage.StorageType
			}
			logger.Debug().Str("error", sanitizeErrorString(err.Error())).Str("file_id", item.fileID).Str("file_name", item.name).Str("job_id", jobID).Msg("download failed - full error chain for debugging")
			return formatDownloadError(item.name, item.fileID, jobID, storageType, err)
		}

		logger.Info().
			Str("file_id", item.fileID).
			Str("path", outputPath).
			Msg("File downloaded successfully")

		if fileBar == nil {
			fileBar = downloadUI.AddFileBar(item.idx+1, item.fileID, item.name, outputPath, item.size)
		}
		fileBar.Complete(nil)

		downloadMutex.Lock()
		downloadedFiles = append(downloadedFiles, outputPath)
		downloadMutex.Unlock()
		return nil
	})

	// Collect errors from batch result
	dlErrors := batchResult.Errors

	// Print summary
	if len(dlErrors) > 0 {
		fmt.Printf("\n✓ Successfully downloaded %d file(s)\n", len(downloadedFiles))
		if len(skippedFiles) > 0 {
			fmt.Printf("⊘ Skipped %d file(s)\n", len(skippedFiles))
		}
		fmt.Printf("✗ Failed to download %d file(s)\n", len(dlErrors))
		// Return first error but continue with others (per project objectives)
		return dlErrors[0]
	}

	fmt.Printf("\n✓ Successfully downloaded %d file(s)\n", len(downloadedFiles))
	if len(skippedFiles) > 0 {
		fmt.Printf("⊘ Skipped %d file(s)\n", len(skippedFiles))
	}
	return nil
}

// sanitizeErrorString removes secrets (SAS tokens, access keys, session tokens)
// from error messages to prevent leakage in logs and user-facing output.
func sanitizeErrorString(s string) string {
	// Redact SAS token query parameters (sig=..., se=..., sp=..., sv=..., sr=...)
	// These appear in Azure SAS URLs embedded in error messages
	sasPattern := regexp.MustCompile(`(sig|se|sp|sv|sr|spr|sip|srt|ss)=[^&\s"')]+`)
	s = sasPattern.ReplaceAllString(s, "$1=REDACTED")

	// Redact AWS-style keys
	s = regexp.MustCompile(`(?i)(access.?key|secret.?key|session.?token)=\S+`).ReplaceAllString(s, "$1=REDACTED")

	// Redact Azure connection string account keys
	s = regexp.MustCompile(`(?i)AccountKey=[^;&\s"']+`).ReplaceAllString(s, "AccountKey=REDACTED")

	// Redact Bearer/JWT tokens (preserving the scheme prefix)
	s = regexp.MustCompile(`(?i)(Authorization:\s*)?((Bearer|Token)\s+)[A-Za-z0-9._\-/+=]+`).ReplaceAllString(s, "${1}${2}REDACTED")

	// Redact AWS access key IDs
	s = regexp.MustCompile(`AKIA[A-Z0-9]{16}`).ReplaceAllString(s, "[REDACTED_AWS_KEY]")

	return s
}

// classifyDownloadStep inspects the error chain to identify which download step failed.
func classifyDownloadStep(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "failed to list job files"):
		return "listing job files"
	case strings.Contains(s, "credentials") || strings.Contains(s, "credential"):
		return "fetching storage credentials"
	case strings.Contains(s, "failed to get Azure client") || strings.Contains(s, "failed to create"):
		return "creating storage client"
	case strings.Contains(s, "download failed") || strings.Contains(s, "file size"):
		return "downloading from storage"
	case strings.Contains(s, "checksum"):
		return "verifying checksum"
	case strings.Contains(s, "decrypt"):
		return "decrypting file"
	default:
		return "downloading"
	}
}

// formatDownloadError creates a user-friendly error for download failures.
// Collapses the internal error chain to the root cause, includes context
// (file name, IDs, storage type), classifies the failed step, and provides
// actionable guidance. Avoids leaking Go internals or secrets.
func formatDownloadError(fileName, fileID, jobID, storageType string, err error) error {
	step := classifyDownloadStep(err)

	// Extract root cause
	rootCause := err
	for {
		unwrapped := errors.Unwrap(rootCause)
		if unwrapped == nil {
			break
		}
		rootCause = unwrapped
	}

	// Sanitize: remove Go struct/field references from root cause
	rootMsg := rootCause.Error()
	if strings.Contains(rootMsg, "Go struct field") || strings.Contains(rootMsg, "json:") {
		rootMsg = "unexpected credential response format"
	}
	rootMsg = sanitizeErrorString(rootMsg)

	// Build context string
	errCtx := fmt.Sprintf("file %s", fileID)
	if jobID != "" {
		errCtx = fmt.Sprintf("file %s, job %s", fileID, jobID)
	}

	return fmt.Errorf("download failed for %q (%s, storage: %s)\n  Step: %s\n  Cause: %s\n  Try: rerun with --debug for details, or verify you have access to this job",
		fileName, errCtx, storageType, step, rootMsg)
}
