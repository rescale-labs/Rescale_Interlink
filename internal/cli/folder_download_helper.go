package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/progress"
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

// downloadFolderRecursive recursively downloads a folder and all its contents
func downloadFolderRecursive(
	ctx context.Context,
	folderID string,
	outputDir string,
	overwriteAll bool,
	skipAll bool,
	continueOnError bool,
	maxConcurrent int,
	skipChecksum bool,
	apiClient *api.Client,
	logger *logging.Logger,
) (*DownloadResult, error) {
	result := &DownloadResult{
		Errors: make([]DownloadError, 0),
	}

	// Use folder ID as the root folder name (user can rename after download)
	rootFolderName := folderID
	rootOutputDir := filepath.Join(outputDir, rootFolderName)

	logger.Info().
		Str("folder_id", folderID).
		Str("output_dir", rootOutputDir).
		Msg("Starting recursive folder download")

	// Scan the remote folder structure
	fmt.Println("📡 Scanning remote folder structure...")
	allFolders, allFiles, err := scanRemoteFolderRecursive(ctx, apiClient, folderID, "")
	if err != nil {
		return nil, fmt.Errorf("failed to scan remote folder: %w", err)
	}

	fmt.Printf("\n📊 Scan complete:\n")
	fmt.Printf("  Folders: %d\n", len(allFolders))
	fmt.Printf("  Files: %d\n", len(allFiles))
	fmt.Println()

	// Create local directory structure
	fmt.Println("📂 Creating local directory structure...")
	foldersCreated := 0
	for _, folder := range allFolders {
		// Validate folder path to prevent escaping output directory
		if err := validation.ValidatePathInDirectory(folder.RelativePath, rootOutputDir); err != nil {
			return nil, fmt.Errorf("invalid folder path from API: %w", err)
		}
		localPath := filepath.Join(rootOutputDir, folder.RelativePath)
		if err := os.MkdirAll(localPath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", localPath, err)
		}
		foldersCreated++
	}
	result.FoldersCreated = foldersCreated
	fmt.Printf("✓ Created %d local directories\n", foldersCreated)

	// Download all files
	fmt.Println("\n📥 Downloading files...")
	downloadUI := progress.NewDownloadUI(len(allFiles))

	// NOTE: Do NOT redirect zerolog through downloadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.

	defer downloadUI.Wait()

	var downloadMutex sync.Mutex
	var conflictMode DownloadConflictAction = DownloadSkipOnce

	// Set initial conflict mode from flags
	if overwriteAll {
		conflictMode = DownloadOverwriteAll
	} else if skipAll {
		conflictMode = DownloadSkipAll
	}
	var conflictMutex sync.Mutex

	// Use semaphore to limit concurrent downloads
	semaphore := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	errChan := make(chan DownloadError, len(allFiles))

	// Download each file concurrently
	for i, fileTask := range allFiles {
		wg.Add(1)
		go func(idx int, task RemoteFileTask) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			localPath := filepath.Join(rootOutputDir, task.RelativePath)

			// Check if file exists and handle conflict
			if _, err := os.Stat(localPath); err == nil {
				conflictMutex.Lock()
				currentMode := conflictMode
				conflictMutex.Unlock()

				var action DownloadConflictAction

				switch currentMode {
				case DownloadSkipAll:
					action = DownloadSkipAll
				case DownloadOverwriteAll:
					action = DownloadOverwriteAll
				default:
					// Prompt user (serialize prompts)
					conflictMutex.Lock()
					action, err = promptDownloadConflict(task.Name, localPath)
					if err != nil {
						conflictMutex.Unlock()
						errChan <- DownloadError{FilePath: localPath, FileID: task.FileID, Error: err}
						return
					}

					// Update mode if user chose "all"
					if action == DownloadSkipAll || action == DownloadOverwriteAll {
						conflictMode = action
					}
					conflictMutex.Unlock()
				}

				// Handle the action
				switch action {
				case DownloadSkipOnce, DownloadSkipAll:
					downloadMutex.Lock()
					result.FilesSkipped++
					downloadMutex.Unlock()
					return
				case DownloadAbort:
					errChan <- DownloadError{FilePath: localPath, FileID: task.FileID, Error: fmt.Errorf("download aborted by user")}
					return
				case DownloadOverwriteOnce, DownloadOverwriteAll:
					// Remove existing file to overwrite
					if err := os.Remove(localPath); err != nil {
						errChan <- DownloadError{FilePath: localPath, FileID: task.FileID, Error: fmt.Errorf("failed to remove existing file: %w", err)}
						return
					}
				}
			}

			// Create progress bar for this file
			fileBar := downloadUI.AddFileBar(idx+1, task.FileID, task.Name, localPath, task.Size)

			// Download file with progress callback
			err := download.DownloadFileWithProgress(ctx, task.FileID, localPath, apiClient, func(fraction float64) {
				fileBar.UpdateProgress(fraction)
			}, skipChecksum)

			if err != nil {
				fileBar.Complete(err)

				// Check if resume state exists to provide helpful guidance
				if download.ResumeStateExists(localPath) {
					fmt.Fprintf(os.Stderr, "\n💡 Resume state saved for %s. To resume, re-run the download command.\n", filepath.Base(localPath))
				}

				downloadMutex.Lock()
				result.FilesFailed++
				downloadMutex.Unlock()
				errChan <- DownloadError{FilePath: localPath, FileID: task.FileID, Error: err}

				if !continueOnError {
					// TODO: Signal other goroutines to stop
				}
				return
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
		}(i, fileTask)
	}

	// Wait for all downloads
	wg.Wait()
	close(errChan)

	// Collect errors
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
}

// scanRemoteFolderRecursive recursively scans a remote folder structure
func scanRemoteFolderRecursive(
	ctx context.Context,
	apiClient *api.Client,
	folderID string,
	relativePath string,
) ([]RemoteFolderInfo, []RemoteFileTask, error) {
	folders := make([]RemoteFolderInfo, 0)
	files := make([]RemoteFileTask, 0)

	// Get folder contents
	contents, err := apiClient.ListFolderContents(ctx, folderID)
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
		subFolders, subFiles, err := scanRemoteFolderRecursive(ctx, apiClient, folder.ID, folderRelPath)
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
		})
	}

	return folders, files, nil
}
