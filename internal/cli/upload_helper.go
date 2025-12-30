package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/state"
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
	preEncrypt bool,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	// Use the unified upload function which handles concurrency
	_, err := UploadFilesWithIDs(ctx, filePatterns, folderID, maxConcurrent, preEncrypt, apiClient, logger, false)
	return err
}

// executeFileUploadWithDuplicateCheck handles file uploads with optional duplicate detection
func executeFileUploadWithDuplicateCheck(
	ctx context.Context,
	filePatterns []string,
	folderID string,
	maxConcurrent int,
	duplicateMode UploadDuplicateMode,
	dryRun bool,
	preEncrypt bool,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	// Expand glob patterns first
	filePaths, err := expandGlobPatterns(filePatterns)
	if err != nil {
		return err
	}

	// Validate all files exist before starting upload
	for _, filePath := range filePaths {
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			return fmt.Errorf("file not found: %s", filePath)
		}
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			return fmt.Errorf("failed to stat %s: %w", filePath, err)
		}
		if fileInfo.IsDir() {
			return fmt.Errorf("'%s' is a directory, not a file. Use 'folders upload-dir' to upload directories", filePath)
		}
	}

	// If not checking duplicates, use the fast path
	if duplicateMode == UploadDuplicateModeNoCheck {
		_, err := UploadFilesWithIDs(ctx, filePaths, folderID, maxConcurrent, preEncrypt, apiClient, logger, false)
		return err
	}

	// Get destination folder ID (resolve to My Library if empty)
	destFolderID := folderID
	if destFolderID == "" {
		folders, err := apiClient.GetRootFolders(ctx)
		if err != nil {
			return fmt.Errorf("failed to get root folders: %w", err)
		}
		destFolderID = folders.MyLibrary
	}

	// Get existing files in destination folder
	fmt.Println("📡 Checking for existing files in destination...")
	folderContents, err := apiClient.ListFolderContents(ctx, destFolderID)
	if err != nil {
		return fmt.Errorf("failed to list destination folder: %w", err)
	}

	// Build set of existing file names
	existingFiles := make(map[string]bool)
	for _, file := range folderContents.Files {
		existingFiles[file.Name] = true
	}

	fmt.Printf("✓ Found %d existing file(s) in destination\n\n", len(existingFiles))

	// Filter files based on duplicate mode
	var filesToUpload []string
	var filesSkipped int
	var conflictMode UploadConflictAction

	// Set initial conflict mode based on duplicate mode
	switch duplicateMode {
	case UploadDuplicateModeSkipAll:
		conflictMode = UploadSkipAll
	case UploadDuplicateModeUploadAll:
		conflictMode = UploadOverwriteAll
	default:
		conflictMode = UploadSkipOnce // Will prompt
	}

	for _, filePath := range filePaths {
		fileName := filepath.Base(filePath)

		if !existingFiles[fileName] {
			// No duplicate, upload it
			filesToUpload = append(filesToUpload, filePath)
			continue
		}

		// File exists - handle based on mode
		action := conflictMode
		if action == UploadSkipOnce || action == UploadOverwriteOnce {
			// Need to prompt for this file
			action, err = promptUploadConflict(fileName, "")
			if err != nil {
				return err
			}

			// Update mode if user chose "for all"
			if action == UploadSkipAll || action == UploadOverwriteAll {
				conflictMode = action
			}
		}

		switch action {
		case UploadSkipOnce, UploadSkipAll:
			fmt.Printf("⊘ Skipping duplicate: %s\n", fileName)
			filesSkipped++
		case UploadOverwriteOnce, UploadOverwriteAll:
			fmt.Printf("⊕ Uploading duplicate: %s\n", fileName)
			filesToUpload = append(filesToUpload, filePath)
		case UploadAbort:
			return fmt.Errorf("upload aborted by user")
		}
	}

	if filesSkipped > 0 {
		fmt.Printf("\n📊 Pre-upload summary: %d file(s) to upload, %d skipped as duplicates\n\n", len(filesToUpload), filesSkipped)
	}

	if len(filesToUpload) == 0 {
		fmt.Println("✓ No files to upload (all were duplicates)")
		return nil
	}

	// DRY-RUN MODE: Show what would happen without uploading
	if dryRun {
		fmt.Println("\n" + strings.Repeat("=", 60))
		fmt.Println("📊 Dry-Run Summary")
		fmt.Println(strings.Repeat("=", 60))

		// Calculate total size
		var totalBytes int64
		for _, filePath := range filesToUpload {
			if info, err := os.Stat(filePath); err == nil {
				totalBytes += info.Size()
			}
		}

		fmt.Printf("\n📄 Files:\n")
		fmt.Printf("  Would upload:     %d\n", len(filesToUpload))
		if filesSkipped > 0 {
			fmt.Printf("  Would skip:       %d (duplicates)\n", filesSkipped)
		}
		fmt.Printf("\n💾 Total data to upload: %.2f MB\n", float64(totalBytes)/(1024*1024))

		// Duplicate mode reminder
		fmt.Printf("\n🔧 Duplicate mode: ")
		switch duplicateMode {
		case UploadDuplicateModeNoCheck:
			fmt.Println("NO-CHECK (no duplicate checking)")
		case UploadDuplicateModeSkipAll:
			fmt.Println("SKIP-DUPLICATES (skip existing files)")
		case UploadDuplicateModeUploadAll:
			fmt.Println("ALLOW-DUPLICATES (upload even if exists)")
		default:
			fmt.Println("CHECK (prompt for each duplicate)")
		}

		fmt.Println(strings.Repeat("=", 60))
		fmt.Println("\n✅ Dry-run complete. No files were uploaded.")
		fmt.Println("   Remove --dry-run to perform the actual upload.")
		return nil
	}

	// Upload the filtered files
	_, err = UploadFilesWithIDs(ctx, filesToUpload, folderID, maxConcurrent, preEncrypt, apiClient, logger, false)
	return err
}

// UploadFilesWithIDs uploads files concurrently and returns their file IDs.
// This is a shared helper for both 'files upload' and 'jobs submit --files'.
// Returns file IDs in the same order as input files.
// If preEncrypt is true, uses legacy pre-encryption mode (creates temp file before upload).
// If preEncrypt is false (default), uses streaming encryption (encrypts on-the-fly, no temp file).
func UploadFilesWithIDs(
	ctx context.Context,
	filePatterns []string,
	folderID string,
	maxConcurrent int,
	preEncrypt bool,
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
			// Uses streaming encryption by default (preEncrypt=false), or legacy pre-encryption if preEncrypt=true
			cloudFile, err := upload.UploadFile(ctx, upload.UploadParams{
				LocalPath: fPath,
				FolderID:  folderID,
				APIClient: apiClient,
				ProgressCallback: func(fraction float64) {
					// Create progress bar on first progress update (lazy initialization)
					barOnce.Do(func() {
						fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
					})
					if fileBar != nil {
						fileBar.UpdateProgress(fraction)
					}
				},
				TransferHandle: transferHandle,
				OutputWriter:   uploadUI.Writer(),
				PreEncrypt:     preEncrypt,
			})

			if err != nil {
				// Ensure progress bar exists before completing it
				if fileBar == nil {
					fileBar = uploadUI.AddFileBar(fPath, folderID, fileInfo.Size())
				}
				fileBar.Complete("", err)

				// Check if resume state exists to provide helpful guidance
				if state.UploadResumeStateExists(fPath) {
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

	// Collect all errors (drain channel to prevent leaks and report all failures)
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	// Return first error but report count of all failures
	if len(errors) > 0 {
		if len(errors) == 1 {
			return nil, errors[0]
		}
		return nil, fmt.Errorf("upload failed: %d file(s) failed (first error: %v)", len(errors), errors[0])
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
