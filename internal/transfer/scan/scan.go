// Package scan provides recursive remote folder scanning for use by both CLI and GUI.
package scan

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/validation"
)

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
	CloudFile    *models.CloudFile
}

// ScanEvent represents a single discovery from the streaming scanner.
type ScanEvent struct {
	Folder *RemoteFolderInfo // Non-nil for folder discovery
	File   *RemoteFileTask  // Non-nil for file discovery
}

// ScanProgress reports cumulative scan progress.
type ScanProgress struct {
	FoldersFound int
	FilesFound   int
	BytesFound   int64
}

// ScanRemoteFolderRecursive recursively scans a remote folder structure.
// Exported for GUI reuse.
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
			CloudFile:    file.ToCloudFile(),
		})
	}

	return folders, files, nil
}

// ScanRemoteFolderRecursiveWithProgress is like ScanRemoteFolderRecursive but calls
// onProgress after each subfolder is scanned, enabling live scan feedback in CLI.
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

// ScanRemoteFolderStreaming scans a remote folder structure concurrently,
// emitting files and folders as they are discovered rather than waiting for
// the entire scan to complete. Downloads can begin within seconds.
//
// Returns a channel of ScanEvents (closed when scan completes) and an error channel.
// The error channel receives at most one error, then is closed.
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

					// Stream pages — emit files/folders as each API page arrives
					// instead of waiting for the entire folder to be enumerated.
					err := apiClient.ListFolderContentsStreaming(ctx, work.folderID,
						func(folders []api.FolderInfo, files []api.FileInfo) error {
							// Emit folders first (so parent dirs can be created before files)
							for _, folder := range folders {
								// Defense-in-depth — validate folder names from API
								if err := validation.ValidateFilename(folder.Name); err != nil {
									continue
								}
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
						// Don't report context cancellation as a scan error — it's a clean cancel
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
