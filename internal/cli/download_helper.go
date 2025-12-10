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
	"github.com/rescale/rescale-int/internal/util/filter"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/validation"
)

// executeFileDownload - Common download logic for both files download and download shortcut
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

	fmt.Printf("Downloading %d file(s) to: %s\n\n", len(fileIDs), outputDir)

	// Create DownloadUI for professional progress bars
	downloadUI := progress.NewDownloadUI(len(fileIDs))

	// NOTE: Do NOT redirect zerolog through downloadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.

	defer downloadUI.Wait()

	downloadedFiles := make([]string, 0, len(fileIDs))
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
	errChan := make(chan error, len(fileIDs))

	// Download each file concurrently
	for i, fileID := range fileIDs {
		wg.Add(1)
		go func(idx int, fid string) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Get file metadata first
			fileInfo, err := apiClient.GetFileInfo(ctx, fid)
			if err != nil {
				errChan <- fmt.Errorf("failed to get file info for %s: %w", fid, err)
				return
			}

			// Validate filename from API to prevent path traversal
			if err := validation.ValidateFilename(fileInfo.Name); err != nil {
				errChan <- fmt.Errorf("invalid filename from API for file %s: %w", fid, err)
				return
			}

			outputPath := filepath.Join(outputDir, fileInfo.Name)

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
					action, err = promptDownloadConflict(fileInfo.Name, outputPath)
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
					minEncryptedSize := fileInfo.DecryptedSize + 1
					maxEncryptedSize := fileInfo.DecryptedSize + 16

					// If encrypted file exists and has size within expected range, skip download and retry decryption
					if encErr == nil && encryptedInfo.Size() >= minEncryptedSize && encryptedInfo.Size() <= maxEncryptedSize {
						fmt.Fprintf(downloadUI.Writer(), "✓ Encrypted file complete (%d bytes), retrying decryption for %s...\n",
							encryptedInfo.Size(), fileInfo.Name)
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
									fileInfo.Name, progress*100, resumeState.DownloadedBytes, resumeState.TotalSize)
								// Remove partial decrypted file if it exists (we'll re-decrypt after download completes)
								if outErr == nil {
									os.Remove(outputPath)
								}
								// Don't delete encrypted file or resume state - let downloader continue from where it left off
							} else {
								// Resume state exists but is invalid/expired - cleanup and restart
								fmt.Fprintf(downloadUI.Writer(), "Resume state invalid for %s (reason: %v). Starting fresh download...\n",
									fileInfo.Name, err)
								state.CleanupExpiredDownloadResume(resumeState, outputPath, false)
								os.Remove(outputPath)
							}
						} else {
							// No resume state - fresh start (encrypted file might be from a different/failed download)
							if encErr == nil {
								fmt.Fprintf(downloadUI.Writer(), "Encrypted file has unexpected size (%d bytes, expected %d-%d bytes). Starting fresh download for %s...\n",
									encryptedInfo.Size(), minEncryptedSize, maxEncryptedSize, fileInfo.Name)
								os.Remove(encryptedPath)
							}
							os.Remove(outputPath)
						}
					}
				}
			}

			// Show "Preparing..." message before fetching credentials
			fmt.Fprintf(downloadUI.Writer(), "[%d/%d] Preparing to download %s...\n", idx+1, len(fileIDs), fileInfo.Name)

			// Allocate transfer handle for this file
			transferHandle := transferMgr.AllocateTransfer(fileInfo.DecryptedSize, len(fileIDs))

			// Print thread info if multi-threaded
			if transferHandle.GetThreads() > 1 && fileInfo.DecryptedSize > 100*1024*1024 {
				fmt.Fprintf(downloadUI.Writer(), "Using %d concurrent threads for %s\n",
					transferHandle.GetThreads(), fileInfo.Name)
			}

			// Create progress bar for this file (will be created just before download starts)
			var fileBar *progress.DownloadFileBar
			var barOnce sync.Once

			// Download file with progress callback and transfer handle
			err = download.DownloadFile(ctx, download.DownloadParams{
				FileID:    fid,
				LocalPath: outputPath,
				APIClient: apiClient,
				ProgressCallback: func(fraction float64) {
					// Create progress bar on first progress update (lazy initialization)
					barOnce.Do(func() {
						fileBar = downloadUI.AddFileBar(idx+1, fid, fileInfo.Name, outputPath, fileInfo.DecryptedSize)
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
					fileBar = downloadUI.AddFileBar(idx+1, fid, fileInfo.Name, outputPath, fileInfo.DecryptedSize)
				}
				fileBar.Complete(err)

				// Check if resume state exists to provide helpful guidance
				if state.DownloadResumeStateExists(outputPath) {
					fmt.Fprintf(os.Stderr, "\n💡 Resume state saved for %s. To resume this download, run the same command again.\n", fileInfo.Name)
				}

				errChan <- fmt.Errorf("failed to download %s: %w", fid, err)
				return
			}

			logger.Info().
				Str("file_id", fid).
				Str("path", outputPath).
				Msg("File downloaded successfully")

			// Ensure progress bar exists before completing it
			if fileBar == nil {
				fileBar = downloadUI.AddFileBar(idx+1, fid, fileInfo.Name, outputPath, fileInfo.DecryptedSize)
			}
			fileBar.Complete(nil)

			downloadMutex.Lock()
			downloadedFiles = append(downloadedFiles, outputPath)
			downloadMutex.Unlock()
		}(i, fileID)
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

	// v3.2.2: Pre-compute output paths and detect filename collisions
	// When multiple files have the same name (e.g., from different job runs), we must
	// give them unique output paths to prevent concurrent download corruption.
	fileOutputPaths := buildJobFileOutputPaths(files, outputDir)

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

// buildJobFileOutputPaths pre-computes output paths for all job files, handling filename collisions.
// When multiple files have the same name (common when downloading outputs from parallel runs),
// the file ID is appended to disambiguate: "model.sim" -> "model_ABC123.sim"
// This prevents concurrent downloads from corrupting each other by writing to the same file.
//
// v3.2.2: Added to fix concurrent download corruption bug for same-name files.
func buildJobFileOutputPaths(files []models.JobFile, outputDir string) map[string]string {
	result := make(map[string]string, len(files))

	// First pass: count occurrences of each output path
	pathToFiles := make(map[string][]models.JobFile)

	for _, file := range files {
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
			// Validate filename
			if validation.ValidateFilename(file.Name) == nil {
				basePath = filepath.Join(outputDir, file.Name)
			} else {
				// Invalid filename - skip (will error during download)
				basePath = filepath.Join(outputDir, file.Name)
			}
		}
		pathToFiles[basePath] = append(pathToFiles[basePath], file)
	}

	// Second pass: assign unique paths
	duplicateCount := 0
	for basePath, fileList := range pathToFiles {
		if len(fileList) == 1 {
			// No collision - use original path
			result[fileList[0].ID] = basePath
		} else {
			// Collision detected - append file ID to each
			duplicateCount += len(fileList)
			for _, file := range fileList {
				// Insert file ID before extension: "model.sim" -> "model_ABC123.sim"
				ext := filepath.Ext(basePath)
				base := basePath[:len(basePath)-len(ext)]
				uniquePath := fmt.Sprintf("%s_%s%s", base, file.ID, ext)
				result[file.ID] = uniquePath
			}
		}
	}

	// Warn user about filename collisions
	if duplicateCount > 0 {
		fmt.Printf("⚠️  Found %d files with duplicate names. File IDs will be appended to ensure unique downloads.\n", duplicateCount)
	}

	return result
}
