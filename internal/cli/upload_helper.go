package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/transfer"
)

// expandGlobPatterns expands glob patterns like *.zip, even when quoted
// Returns deduplicated list of file paths
func expandGlobPatterns(patterns []string) ([]string, error) {
	var expandedFiles []string
	seenFiles := make(map[string]bool)

	for _, pattern := range patterns {
		// Check if pattern contains glob characters
		hasGlob := strings.ContainsAny(pattern, "*?[]")

		if hasGlob {
			// Expand glob pattern
			matches, err := filepath.Glob(pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid pattern '%s': %w", pattern, err)
			}

			if len(matches) == 0 {
				return nil, fmt.Errorf("no files match pattern: %s", pattern)
			}

			// Add matches (deduplicated)
			for _, match := range matches {
				absPath, err := filepath.Abs(match)
				if err != nil {
					return nil, fmt.Errorf("failed to get absolute path for %s: %w", match, err)
				}

				if !seenFiles[absPath] {
					expandedFiles = append(expandedFiles, absPath)
					seenFiles[absPath] = true
				}
			}
		} else {
			// Not a glob pattern, use as-is
			absPath, err := filepath.Abs(pattern)
			if err != nil {
				return nil, fmt.Errorf("failed to get absolute path for %s: %w", pattern, err)
			}

			if !seenFiles[absPath] {
				expandedFiles = append(expandedFiles, absPath)
				seenFiles[absPath] = true
			}
		}
	}

	return expandedFiles, nil
}

// executeFileUpload - Common upload logic for both files upload and upload shortcut
// Now uses the unified UploadFilesWithIDs for concurrent uploads
func executeFileUpload(
	ctx context.Context,
	filePatterns []string,
	folderID string,
	maxConcurrent int,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	// Use the unified upload function which handles concurrency
	_, err := UploadFilesWithIDs(ctx, filePatterns, folderID, maxConcurrent, apiClient, logger, false)
	return err
}

// UploadFilesWithIDs uploads files concurrently and returns their file IDs.
// This is a shared helper for both 'files upload' and 'jobs submit --files'.
// Returns file IDs in the same order as input files.
func UploadFilesWithIDs(
	ctx context.Context,
	filePatterns []string,
	folderID string,
	maxConcurrent int,
	apiClient *api.Client,
	logger *logging.Logger,
	silent bool, // If true, skip summary output (for use in job submission)
) ([]string, error) {
	// Expand glob patterns
	filePaths, err := expandGlobPatterns(filePatterns)
	if err != nil {
		return nil, err
	}

	if !silent {
		logger.Info().
			Int("count", len(filePaths)).
			Msg("Starting file upload")
	}

	// Validate all files exist before starting upload
	for _, filePath := range filePaths {
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			logger.Error().Str("file", filePath).Msg("File not found")
			return nil, fmt.Errorf("file not found: %s", filePath)
		}

		// Check it's a file, not a directory
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to stat %s: %w", filePath, err)
		}
		if fileInfo.IsDir() {
			return nil, fmt.Errorf("'%s' is a directory, not a file. Use 'folders upload-dir' to upload directories", filePath)
		}
	}

	// Show target location
	if !silent {
		if folderID != "" {
			fmt.Printf("Uploading %d file(s) to folder ID: %s\n\n", len(filePaths), folderID)
		} else {
			fmt.Printf("Uploading %d file(s) to root (My Library)\n\n", len(filePaths))
		}
	}

	// Validate maxConcurrent
	if maxConcurrent < constants.MinMaxConcurrent || maxConcurrent > constants.MaxMaxConcurrent {
		return nil, fmt.Errorf("maxConcurrent must be between %d and %d, got %d",
			constants.MinMaxConcurrent, constants.MaxMaxConcurrent, maxConcurrent)
	}

	// Create UploadUI for professional progress bars
	uploadUI := progress.NewUploadUI(len(filePaths))

	// NOTE: Do NOT redirect zerolog through uploadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.
	// The mpb library handles rendering progress bars above stderr output automatically.

	defer uploadUI.Wait()

	// Pre-allocate results slice to maintain order
	uploadedFileIDs := make([]string, len(filePaths))

	// Create resource manager from global flags
	resourceMgr := CreateResourceManager()
	transferMgr := transfer.NewManager(resourceMgr)

	// Use semaphore to limit concurrent uploads
	semaphore := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	errChan := make(chan error, len(filePaths))

	// Upload each file concurrently
	for i, filePath := range filePaths {
		wg.Add(1)
		go func(idx int, fPath string) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			fileInfo, _ := os.Stat(fPath)

			// Show "Preparing..." message before fetching credentials
			if !silent {
				fmt.Fprintf(uploadUI.Writer(), "[%d/%d] Preparing to upload %s...\n", idx+1, len(filePaths), filepath.Base(fPath))
			}

			// Allocate transfer handle for this file
			transferHandle := transferMgr.AllocateTransfer(fileInfo.Size(), len(filePaths))

			// Create progress bar for this file (will be created just before upload starts)
			var fileBar *progress.FileBar
			var barOnce sync.Once

			// Upload with progress callback and transfer handle
			cloudFile, err := upload.UploadFileToFolderWithTransfer(ctx, fPath, folderID, apiClient, func(fraction float64) {
				// Create progress bar on first progress update (lazy initialization)
				barOnce.Do(func() {
					fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
				})
				if fileBar != nil {
					fileBar.UpdateProgress(fraction)
				}
			}, transferHandle,
				uploadUI.Writer())

			if err != nil {
				// Ensure progress bar exists before completing it
				if fileBar == nil {
					fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
				}
				fileBar.Complete("", err)

				// Check if resume state exists to provide helpful guidance
				if upload.ResumeStateExists(fPath) {
					fmt.Fprintf(os.Stderr, "\n💡 Resume state saved. To resume this upload, run the same command again:\n")
					fmt.Fprintf(os.Stderr, "   rescale-int files upload %s\n\n", fPath)
				}

				errChan <- fmt.Errorf("failed to upload %s: %w", fPath, err)
				return
			}

			// NOTE: Logger is redirected to uploadUI.Writer() at this point.
			// We skip logging here to avoid zerolog warnings from mixing structured JSON
			// with progress bar ANSI codes. The upload success is already shown via progress UI.

			// Ensure progress bar exists before completing it
			if fileBar == nil {
				fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
			}
			fileBar.Complete(cloudFile.ID, nil)

			// Store result in correct position to maintain order
			uploadedFileIDs[idx] = cloudFile.ID
		}(i, filePath)
	}

	// Wait for all uploads to complete
	wg.Wait()
	close(errChan)

	// Check for errors
	if len(errChan) > 0 {
		return nil, <-errChan // Return first error
	}

	// Summary
	if !silent {
		fmt.Printf("\n✓ Successfully uploaded %d file(s)\n", len(uploadedFileIDs))
		if len(uploadedFileIDs) > 0 {
			fmt.Println("\nFile IDs:")
			for i, fileID := range uploadedFileIDs {
				fmt.Printf("  [%d] %s\n", i+1, fileID)
			}
		}
	}

	return uploadedFileIDs, nil
}
