package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/util/filter"
	"github.com/rescale/rescale-int/internal/utils/paths"
	"github.com/rescale/rescale-int/internal/validation"
)

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
	var conflictMode DownloadConflictAction = DownloadSkipOnce

	// Set initial conflict mode from flags
	if overwriteAll {
		conflictMode = DownloadOverwriteAll
	} else if skipAll {
		conflictMode = DownloadSkipAll
	} else if resumeAll {
		conflictMode = DownloadResumeAll
	}
	var conflictMutex sync.Mutex

	// Create resource manager from global flags
	resourceMgr := CreateResourceManager()
	transferMgr := transfer.NewManager(resourceMgr)

	// Use semaphore to limit concurrent downloads
	semaphore := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	errChan := make(chan error, len(validFiles))

	// PHASE 3: Download each file concurrently using resolved paths
	for i, df := range downloadFiles {
		wg.Add(1)
		go func(idx int, fileDownload paths.FileForDownload) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			meta := fileIDToMeta[fileDownload.FileID]
			outputPath := fileDownload.LocalPath

			// Check if file exists and handle conflict
			if _, err := os.Stat(outputPath); err == nil {
				conflictMutex.Lock()
				currentMode := conflictMode
				conflictMutex.Unlock()

				var action DownloadConflictAction

				switch currentMode {
				case DownloadSkipAll:
					action = DownloadSkipAll
				case DownloadOverwriteAll:
					action = DownloadOverwriteAll
				case DownloadResumeAll:
					action = DownloadResumeAll
				default:
					// Prompt user (serialize prompts)
					conflictMutex.Lock()
					var err error
					action, err = promptDownloadConflict(meta.Name, outputPath)
					if err != nil {
						conflictMutex.Unlock()
						errChan <- fmt.Errorf("conflict prompt failed: %w", err)
						return
					}

					// Update mode if user chose "all"
					if action == DownloadSkipAll || action == DownloadOverwriteAll || action == DownloadResumeAll {
						conflictMode = action
					}
					conflictMutex.Unlock()
				}

				// Handle the action
				switch action {
				case DownloadSkipOnce, DownloadSkipAll:
					downloadMutex.Lock()
					skippedFiles = append(skippedFiles, outputPath)
					downloadMutex.Unlock()
					return
				case DownloadAbort:
					errChan <- fmt.Errorf("download aborted by user")
					return
				case DownloadOverwriteOnce, DownloadOverwriteAll:
					// Remove existing file to overwrite
					if err := os.Remove(outputPath); err != nil {
						errChan <- fmt.Errorf("failed to remove existing file: %w", err)
						return
					}
				case DownloadResumeOnce, DownloadResumeAll:
					// Resume logic: Check if encrypted file exists and is complete, or if we can resume from partial
					encryptedPath := outputPath + ".encrypted"
					encryptedInfo, encErr := os.Stat(encryptedPath)
					_, outErr := os.Stat(outputPath)

					// Calculate expected encrypted size (decrypted size + 1-16 bytes PKCS7 padding)
					minEncryptedSize := meta.DecryptedSize + 1
					maxEncryptedSize := meta.DecryptedSize + 16

					// If encrypted file exists and has size within expected range, skip download and retry decryption
					if encErr == nil && encryptedInfo.Size() >= minEncryptedSize && encryptedInfo.Size() <= maxEncryptedSize {
						fmt.Fprintf(downloadUI.Writer(), "✓ Encrypted file complete (%d bytes), retrying decryption for %s...\n",
							encryptedInfo.Size(), meta.Name)
						// Remove partial decrypted file if it exists
						if outErr == nil {
							os.Remove(outputPath)
						}
						// Skip to decryption (download will be skipped below)
					} else {
						// Check if we have valid resume state for byte-offset resume
						resumeState, _ := state.LoadDownloadState(outputPath)
						if resumeState != nil {
							if err := state.ValidateDownloadState(resumeState, outputPath); err == nil {
								// Valid resume state exists - let the downloader handle byte-offset resume
								resumeProgress := state.GetDownloadResumeProgress(resumeState)
								fmt.Fprintf(downloadUI.Writer(), "↻ Resuming download for %s from %.1f%% (%d/%d bytes)...\n",
									meta.Name, resumeProgress*100, resumeState.DownloadedBytes, resumeState.TotalSize)
								// Remove partial decrypted file if it exists (we'll re-decrypt after download completes)
								if outErr == nil {
									os.Remove(outputPath)
								}
								// Don't delete encrypted file or resume state - let downloader continue from where it left off
							} else {
								// Resume state exists but is invalid/expired - cleanup and restart
								fmt.Fprintf(downloadUI.Writer(), "Resume state invalid for %s (reason: %v). Starting fresh download...\n",
									meta.Name, err)
								state.CleanupExpiredDownloadResume(resumeState, outputPath, false)
								os.Remove(outputPath)
							}
						} else {
							// No resume state - fresh start (encrypted file might be from a different/failed download)
							if encErr == nil {
								fmt.Fprintf(downloadUI.Writer(), "Encrypted file has unexpected size (%d bytes, expected %d-%d bytes). Starting fresh download for %s...\n",
									encryptedInfo.Size(), minEncryptedSize, maxEncryptedSize, meta.Name)
								os.Remove(encryptedPath)
							}
							os.Remove(outputPath)
						}
					}
				}
			}

			// Show "Preparing..." message before fetching credentials
			fmt.Fprintf(downloadUI.Writer(), "[%d/%d] Preparing to download %s...\n", idx+1, len(downloadFiles), meta.Name)

			// Allocate transfer handle for this file
			transferHandle := transferMgr.AllocateTransfer(meta.DecryptedSize, len(downloadFiles))

			// Print thread info if multi-threaded
			if transferHandle.GetThreads() > 1 && meta.DecryptedSize > 100*1024*1024 {
				fmt.Fprintf(downloadUI.Writer(), "Using %d concurrent threads for %s\n",
					transferHandle.GetThreads(), meta.Name)
			}

			// Create progress bar for this file (will be created just before download starts)
			var fileBar *progress.DownloadFileBar
			var barOnce sync.Once

			// Download file with progress callback and transfer handle
			err := download.DownloadFile(ctx, download.DownloadParams{
				FileID:    fileDownload.FileID,
				LocalPath: outputPath,
				APIClient: apiClient,
				ProgressCallback: func(fraction float64) {
					// Create progress bar on first progress update (lazy initialization)
					barOnce.Do(func() {
						fileBar = downloadUI.AddFileBar(idx+1, fileDownload.FileID, meta.Name, outputPath, meta.DecryptedSize)
					})
					if fileBar != nil {
						fileBar.UpdateProgress(fraction)
					}
				},
				TransferHandle: transferHandle,
				SkipChecksum:   skipChecksum,
			})

			if err != nil {
				// Ensure progress bar exists before completing it
				if fileBar == nil {
					fileBar = downloadUI.AddFileBar(idx+1, fileDownload.FileID, meta.Name, outputPath, meta.DecryptedSize)
				}
				fileBar.Complete(err)

				// Check if resume state exists to provide helpful guidance
				if state.DownloadResumeStateExists(outputPath) {
					fmt.Fprintf(os.Stderr, "\n💡 Resume state saved for %s. To resume this download, run the same command again.\n", meta.Name)
				}

				errChan <- fmt.Errorf("failed to download %s: %w", fileDownload.FileID, err)
				return
			}

			logger.Info().
				Str("file_id", fileDownload.FileID).
				Str("path", outputPath).
				Msg("File downloaded successfully")

			// Ensure progress bar exists before completing it
			if fileBar == nil {
				fileBar = downloadUI.AddFileBar(idx+1, fileDownload.FileID, meta.Name, outputPath, meta.DecryptedSize)
			}
			fileBar.Complete(nil)

			downloadMutex.Lock()
			downloadedFiles = append(downloadedFiles, outputPath)
			downloadMutex.Unlock()
		}(i, df)
	}

	// Wait for all downloads
	wg.Wait()
	close(errChan)

	// Collect all errors
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

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
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	// List all job output files
	logger.Info().Str("job_id", jobID).Msg("Listing job output files")

	allFiles, err := apiClient.ListJobFiles(ctx, jobID)
	if err != nil {
		return fmt.Errorf("failed to list job files: %w", err)
	}

	if len(allFiles) == 0 {
		fmt.Println("No output files found for this job")
		return nil
	}

	// Apply filters if any are specified
	files := allFiles
	if len(filterPatterns) > 0 || len(excludePatterns) > 0 || len(searchTerms) > 0 {
		filterCfg := filter.Config{
			Include: filterPatterns,
			Exclude: excludePatterns,
			Search:  searchTerms,
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
	var conflictMode DownloadConflictAction = DownloadSkipOnce

	// Set initial conflict mode from flags
	if overwriteAll {
		conflictMode = DownloadOverwriteAll
	} else if skipAll {
		conflictMode = DownloadSkipAll
	} else if resumeAll {
		conflictMode = DownloadResumeAll
	}
	var conflictMutex sync.Mutex

	// Create resource manager from global flags
	resourceMgr := CreateResourceManager()
	transferMgr := transfer.NewManager(resourceMgr)

	// Use semaphore to limit concurrent downloads
	semaphore := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	errChan := make(chan error, len(files))

	// Download each file concurrently
	for i, file := range files {
		wg.Add(1)
		go func(idx int, jobFile models.JobFile) {
			defer wg.Done()

			// Acquire semaphore slot
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// v3.2.2: Use pre-computed output path (handles filename collisions)
			outputPath := fileOutputPaths[jobFile.ID]
			if outputPath == "" {
				// Fallback (should never happen)
				outputPath = filepath.Join(outputDir, jobFile.Name)
			}

			// Ensure directory exists
			if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
				errChan <- fmt.Errorf("failed to create directory for %s: %w", jobFile.Name, err)
				return
			}

			// Check if file already exists
			if _, err := os.Stat(outputPath); err == nil {
				// File exists, handle conflict
				conflictMutex.Lock()
				currentMode := conflictMode
				conflictMutex.Unlock()

				switch currentMode {
				case DownloadSkipOnce, DownloadSkipAll:
					fmt.Fprintf(downloadUI.Writer(), "⊘ Skipping existing file: %s\n", jobFile.Name)
					downloadMutex.Lock()
					skippedFiles = append(skippedFiles, outputPath)
					downloadMutex.Unlock()
					return
				case DownloadAbort:
					errChan <- fmt.Errorf("download aborted by user")
					return
				case DownloadOverwriteOnce, DownloadOverwriteAll:
					// Remove existing file to overwrite
					if err := os.Remove(outputPath); err != nil {
						errChan <- fmt.Errorf("failed to remove existing file: %w", err)
						return
					}
				case DownloadResumeOnce, DownloadResumeAll:
					// Resume logic: Check if encrypted file exists and is complete, or if we can resume from partial
					encryptedPath := outputPath + ".encrypted"
					encryptedInfo, encErr := os.Stat(encryptedPath)
					_, outErr := os.Stat(outputPath)

					// Calculate expected encrypted size (decrypted size + 1-16 bytes PKCS7 padding)
					minEncryptedSize := int64(jobFile.DecryptedSize) + 1
					maxEncryptedSize := int64(jobFile.DecryptedSize) + 16

					// If encrypted file exists and has size within expected range, skip download and retry decryption
					if encErr == nil && encryptedInfo.Size() >= minEncryptedSize && encryptedInfo.Size() <= maxEncryptedSize {
						fmt.Fprintf(downloadUI.Writer(), "✓ Encrypted file complete (%d bytes), retrying decryption for %s...\n",
							encryptedInfo.Size(), jobFile.Name)
						// Remove partial decrypted file if it exists
						if outErr == nil {
							os.Remove(outputPath)
						}
						// Skip to decryption (download will be skipped below)
					} else {
						// Check if we have valid resume state for byte-offset resume
						resumeState, _ := state.LoadDownloadState(outputPath)
						if resumeState != nil {
							if err := state.ValidateDownloadState(resumeState, outputPath); err == nil {
								// Valid resume state exists - let the downloader handle byte-offset resume
								progress := state.GetDownloadResumeProgress(resumeState)
								fmt.Fprintf(downloadUI.Writer(), "↻ Resuming download for %s from %.1f%% (%d/%d bytes)...\n",
									jobFile.Name, progress*100, resumeState.DownloadedBytes, resumeState.TotalSize)
								// Remove partial decrypted file if it exists (we'll re-decrypt after download completes)
								if outErr == nil {
									os.Remove(outputPath)
								}
								// Don't delete encrypted file or resume state - let downloader continue from where it left off
							} else {
								// Resume state exists but is invalid/expired - cleanup and restart
								fmt.Fprintf(downloadUI.Writer(), "Resume state invalid for %s (reason: %v). Starting fresh download...\n",
									jobFile.Name, err)
								state.CleanupExpiredDownloadResume(resumeState, outputPath, false)
								os.Remove(outputPath)
							}
						} else {
							// No resume state - fresh start (encrypted file might be from a different/failed download)
							if encErr == nil {
								fmt.Fprintf(downloadUI.Writer(), "Encrypted file has unexpected size (%d bytes, expected %d-%d bytes). Starting fresh download for %s...\n",
									encryptedInfo.Size(), minEncryptedSize, maxEncryptedSize, jobFile.Name)
								os.Remove(encryptedPath)
							}
							os.Remove(outputPath)
						}
					}
				}
			}

			// Allocate transfer handle for this file
			transferHandle := transferMgr.AllocateTransfer(jobFile.DecryptedSize, len(files))

			// Print thread info if multi-threaded
			if transferHandle.GetThreads() > 1 && jobFile.DecryptedSize > 100*1024*1024 {
				fmt.Fprintf(downloadUI.Writer(), "Using %d concurrent threads for %s\n",
					transferHandle.GetThreads(), jobFile.Name)
			}

			// Create progress bar for this file (will be created just before download starts)
			var fileBar *progress.DownloadFileBar
			var barOnce sync.Once

			// Convert JobFile to CloudFile (no API call needed - we already have the metadata!)
			// 2025-11-20: This eliminates GetFileInfo API call, saving ~3 minutes for 289 files
			cloudFile := jobFile.ToCloudFile()

			// Download file with progress callback and transfer handle using metadata
			err = download.DownloadFile(ctx, download.DownloadParams{
				FileInfo:  cloudFile,
				LocalPath: outputPath,
				APIClient: apiClient,
				ProgressCallback: func(fraction float64) {
					// Create progress bar on first progress update (lazy initialization)
					barOnce.Do(func() {
						fileBar = downloadUI.AddFileBar(idx+1, jobFile.ID, jobFile.Name, outputPath, jobFile.DecryptedSize)
					})
					if fileBar != nil {
						fileBar.UpdateProgress(fraction)
					}
				},
				TransferHandle: transferHandle,
				SkipChecksum:   skipChecksum,
			})

			if err != nil {
				// Ensure progress bar exists before completing it
				if fileBar == nil {
					fileBar = downloadUI.AddFileBar(idx+1, jobFile.ID, jobFile.Name, outputPath, jobFile.DecryptedSize)
				}
				fileBar.Complete(err)

				// Check if resume state exists to provide helpful guidance
				if state.DownloadResumeStateExists(outputPath) {
					fmt.Fprintf(os.Stderr, "\n💡 Resume state saved for %s. To resume this download, run the same command again.\n", jobFile.Name)
				}

				errChan <- fmt.Errorf("failed to download %s: %w", jobFile.ID, err)
				return
			}

			logger.Info().
				Str("file_id", jobFile.ID).
				Str("path", outputPath).
				Msg("File downloaded successfully")

			// Ensure progress bar exists before completing it
			if fileBar == nil {
				fileBar = downloadUI.AddFileBar(idx+1, jobFile.ID, jobFile.Name, outputPath, jobFile.DecryptedSize)
			}
			fileBar.Complete(nil)

			downloadMutex.Lock()
			downloadedFiles = append(downloadedFiles, outputPath)
			downloadMutex.Unlock()
		}(i, file)
	}

	// Wait for all downloads
	wg.Wait()
	close(errChan)

	// Collect all errors
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

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

// NOTE: buildJobFileOutputPaths was removed in v3.2.3.
// Collision detection is now handled by the shared paths.ResolveCollisions() utility
// in internal/utils/paths/collision.go for consistency across CLI and GUI.
