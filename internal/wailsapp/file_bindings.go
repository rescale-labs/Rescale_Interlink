// Package wailsapp provides file-related Wails bindings.
package wailsapp

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/services"
)

// FileItemDTO is the JSON-safe version of services.FileItem.
type FileItemDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsFolder bool   `json:"isFolder"`
	Size     int64  `json:"size"`
	ModTime  string `json:"modTime"`
	Path     string `json:"path,omitempty"`
	ParentID string `json:"parentId,omitempty"`
}

// FolderContentsDTO is the JSON-safe version of services.FolderContents.
type FolderContentsDTO struct {
	FolderID   string        `json:"folderId"`
	FolderPath string        `json:"folderPath"`
	Items      []FileItemDTO `json:"items"`
	HasMore    bool          `json:"hasMore"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// DeleteResultDTO contains the result of a delete operation.
type DeleteResultDTO struct {
	Deleted int    `json:"deleted"`
	Failed  int    `json:"failed"`
	Error   string `json:"error,omitempty"`
}

// ListLocalDirectory returns the contents of a local directory.
func (a *App) ListLocalDirectory(path string) FolderContentsDTO {
	// Default to home directory if path is empty
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return FolderContentsDTO{FolderPath: "/", Items: []FileItemDTO{}}
		}
		path = home
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return FolderContentsDTO{
			FolderPath: path,
			Items:      []FileItemDTO{},
		}
	}

	items := make([]FileItemDTO, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		fullPath := filepath.Join(path, entry.Name())
		items = append(items, FileItemDTO{
			ID:       fullPath,
			Name:     entry.Name(),
			IsFolder: entry.IsDir(),
			Size:     info.Size(),
			ModTime:  info.ModTime().Format(time.RFC3339),
			Path:     fullPath,
		})
	}

	// Sort: folders first, then by name
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsFolder != items[j].IsFolder {
			return items[i].IsFolder
		}
		return items[i].Name < items[j].Name
	})

	return FolderContentsDTO{
		FolderID:   path,
		FolderPath: path,
		Items:      items,
		HasMore:    false,
	}
}

// GetHomeDirectory returns the user's home directory.
func (a *App) GetHomeDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return home
}

// ListRemoteFolder returns the contents of a remote folder.
func (a *App) ListRemoteFolder(folderID string) FolderContentsDTO {
	if a.engine == nil {
		return FolderContentsDTO{}
	}

	fs := a.engine.FileService()
	if fs == nil {
		return FolderContentsDTO{}
	}

	ctx := context.Background()
	contents, err := fs.ListFolder(ctx, folderID)
	if err != nil {
		return FolderContentsDTO{
			FolderID: folderID,
			Items:    []FileItemDTO{},
		}
	}

	return folderContentsToDTO(contents)
}

// ListRemoteLegacy returns a flat list of all files (legacy mode).
func (a *App) ListRemoteLegacy(cursor string) FolderContentsDTO {
	if a.engine == nil {
		return FolderContentsDTO{}
	}

	fs := a.engine.FileService()
	if fs == nil {
		return FolderContentsDTO{}
	}

	ctx := context.Background()
	contents, err := fs.ListLegacyFiles(ctx, cursor)
	if err != nil {
		return FolderContentsDTO{
			FolderPath: "Legacy Files",
			Items:      []FileItemDTO{},
		}
	}

	return folderContentsToDTO(contents)
}

// CreateRemoteFolder creates a new folder.
func (a *App) CreateRemoteFolder(name string, parentID string) (string, error) {
	if a.engine == nil {
		return "", ErrNoEngine
	}

	fs := a.engine.FileService()
	if fs == nil {
		return "", ErrNoFileService
	}

	ctx := context.Background()
	return fs.CreateFolder(ctx, name, parentID)
}

// DeleteRemoteItems deletes multiple files and/or folders.
func (a *App) DeleteRemoteItems(items []FileItemDTO) DeleteResultDTO {
	if a.engine == nil {
		return DeleteResultDTO{Error: ErrNoEngine.Error(), Failed: len(items)}
	}

	fs := a.engine.FileService()
	if fs == nil {
		return DeleteResultDTO{Error: ErrNoFileService.Error(), Failed: len(items)}
	}

	// Convert DTOs to service items
	serviceItems := make([]services.FileItem, len(items))
	for i, item := range items {
		serviceItems[i] = services.FileItem{
			ID:       item.ID,
			Name:     item.Name,
			IsFolder: item.IsFolder,
		}
	}

	ctx := context.Background()
	deleted, failed, err := fs.DeleteItems(ctx, serviceItems)

	result := DeleteResultDTO{
		Deleted: deleted,
		Failed:  failed,
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

// GetMyLibraryFolderID returns the MyLibrary root folder ID.
func (a *App) GetMyLibraryFolderID() (string, error) {
	if a.engine == nil {
		return "", ErrNoEngine
	}

	fs := a.engine.FileService()
	if fs == nil {
		return "", ErrNoFileService
	}

	ctx := context.Background()
	return fs.GetMyLibraryFolderID(ctx)
}

// GetMyJobsFolderID returns the MyJobs root folder ID.
func (a *App) GetMyJobsFolderID() (string, error) {
	if a.engine == nil {
		return "", ErrNoEngine
	}

	fs := a.engine.FileService()
	if fs == nil {
		return "", ErrNoFileService
	}

	ctx := context.Background()
	return fs.GetMyJobsFolderID(ctx)
}

// folderContentsToDTO converts a services.FolderContents to a DTO.
func folderContentsToDTO(contents *services.FolderContents) FolderContentsDTO {
	if contents == nil {
		return FolderContentsDTO{Items: []FileItemDTO{}}
	}

	items := make([]FileItemDTO, len(contents.Items))
	for i, item := range contents.Items {
		items[i] = FileItemDTO{
			ID:       item.ID,
			Name:     item.Name,
			IsFolder: item.IsFolder,
			Size:     item.Size,
			ModTime:  item.ModTime.Format(time.RFC3339),
			Path:     item.Path,
			ParentID: item.ParentID,
		}
	}

	return FolderContentsDTO{
		FolderID:   contents.FolderID,
		FolderPath: contents.FolderPath,
		Items:      items,
		HasMore:    contents.HasMore,
		NextCursor: contents.NextCursor,
	}
}

// FolderDownloadResultDTO is the JSON-safe version of cli.DownloadResult.
type FolderDownloadResultDTO struct {
	FoldersCreated  int    `json:"foldersCreated"`
	FilesDownloaded int    `json:"filesDownloaded"`
	FilesSkipped    int    `json:"filesSkipped"`
	FilesFailed     int    `json:"filesFailed"`
	TotalBytes      int64  `json:"totalBytes"`
	Error           string `json:"error,omitempty"`
}

// StartFolderDownload downloads a remote folder recursively to the local filesystem.
// v4.0.0: Implements folder download in GUI by reusing CLI infrastructure.
// Uses merge mode (skip existing files) and continues on error.
func (a *App) StartFolderDownload(folderID string, destPath string) FolderDownloadResultDTO {
	if a.engine == nil {
		return FolderDownloadResultDTO{Error: ErrNoEngine.Error()}
	}

	// Get API client from engine
	apiClient := a.engine.API()
	if apiClient == nil {
		return FolderDownloadResultDTO{Error: "API client not configured"}
	}

	// Create logger for this download
	logger := logging.NewLogger("folder-download", nil)

	// Use CLI's DownloadFolderRecursive with GUI-friendly defaults:
	// - mergeAll=true: skip existing files, merge into existing folders
	// - continueOnError=true: don't stop on individual file failures
	// - skipChecksum=false: verify checksums
	// - dryRun=false: actually download
	ctx := context.Background()
	result, err := cli.DownloadFolderRecursive(
		ctx,
		folderID,
		destPath,
		false,                            // overwriteAll
		false,                            // skipAll
		true,                             // mergeAll - GUI default: merge into existing folders
		true,                             // continueOnError - GUI default: don't abort on failures
		constants.DefaultMaxConcurrent,   // maxConcurrent
		false,                            // skipChecksum
		false,                            // dryRun
		apiClient,
		logger,
	)

	if err != nil {
		return FolderDownloadResultDTO{Error: err.Error()}
	}

	// Convert result to DTO
	dto := FolderDownloadResultDTO{
		FoldersCreated:  result.FoldersCreated,
		FilesDownloaded: result.FilesDownloaded,
		FilesSkipped:    result.FilesSkipped,
		FilesFailed:     result.FilesFailed,
		TotalBytes:      result.TotalBytes,
	}

	// Summarize errors if any
	if len(result.Errors) > 0 {
		dto.Error = result.Errors[0].Error.Error()
		if len(result.Errors) > 1 {
			dto.Error += " (and more errors)"
		}
	}

	return dto
}

// FolderUploadResultDTO is the JSON-safe version of cli.UploadResult.
type FolderUploadResultDTO struct {
	FoldersCreated int    `json:"foldersCreated"`
	FilesQueued    int    `json:"filesQueued"`
	TotalBytes     int64  `json:"totalBytes"`
	Error          string `json:"error,omitempty"`
}

// StartFolderUpload uploads a local folder recursively to the Rescale platform.
// v4.0.0: Implements folder upload in GUI by creating folder structure and
// queueing files to the TransferService for upload with progress events.
// Uses merge mode (reuse existing folders) and queues all files for upload.
func (a *App) StartFolderUpload(localPath string, destFolderID string) FolderUploadResultDTO {
	if a.engine == nil {
		return FolderUploadResultDTO{Error: ErrNoEngine.Error()}
	}

	// Get API client from engine
	apiClient := a.engine.API()
	if apiClient == nil {
		return FolderUploadResultDTO{Error: "API client not configured"}
	}

	// Get TransferService for queueing file uploads
	ts := a.engine.TransferService()
	if ts == nil {
		return FolderUploadResultDTO{Error: ErrNoTransferService.Error()}
	}

	// Validate local path
	fileInfo, err := os.Stat(localPath)
	if err != nil {
		return FolderUploadResultDTO{Error: "Failed to access directory: " + err.Error()}
	}
	if !fileInfo.IsDir() {
		return FolderUploadResultDTO{Error: "Path is not a directory: " + localPath}
	}

	// Create logger for this upload
	logger := logging.NewLogger("folder-upload", nil)
	ctx := context.Background()

	// Get parent folder ID (default to My Library if empty)
	parentID := destFolderID
	if parentID == "" {
		folders, err := apiClient.GetRootFolders(ctx)
		if err != nil {
			return FolderUploadResultDTO{Error: "Failed to get root folders: " + err.Error()}
		}
		parentID = folders.MyLibrary
	}

	// Build directory tree (include hidden files for completeness)
	directories, files, _, err := cli.BuildDirectoryTree(localPath, true)
	if err != nil {
		return FolderUploadResultDTO{Error: "Failed to scan directory: " + err.Error()}
	}

	// Initialize folder cache for API call optimization
	cache := cli.NewFolderCache()

	// Create or get root folder (merge mode: use existing if it exists)
	rootFolderName := filepath.Base(localPath)
	rootFolderID, exists, err := cli.CheckFolderExists(ctx, apiClient, cache, parentID, rootFolderName)
	if err != nil {
		return FolderUploadResultDTO{Error: "Failed to check root folder: " + err.Error()}
	}

	foldersCreated := 0
	if !exists {
		rootFolderID, err = apiClient.CreateFolder(ctx, rootFolderName, parentID)
		if err != nil {
			return FolderUploadResultDTO{Error: "Failed to create root folder: " + err.Error()}
		}
		foldersCreated++
	}

	// Populate cache for root folder
	_, _ = cache.Get(ctx, apiClient, rootFolderID)

	// Create folder structure with merge mode (skip existing folders)
	folderConflictMode := cli.ConflictMergeAll
	mapping, created, err := cli.CreateFolderStructure(
		ctx, apiClient, cache, localPath, directories, rootFolderID,
		&folderConflictMode, 3, logger, nil, nil, // 3 = default folder concurrency
	)
	if err != nil {
		return FolderUploadResultDTO{Error: "Failed to create folder structure: " + err.Error()}
	}
	foldersCreated += created

	// Queue files for upload using TransferService
	var totalBytes int64
	var transferRequests []services.TransferRequest

	for _, filePath := range files {
		// Get parent folder ID from mapping
		dirPath := filepath.Dir(filePath)
		remoteFolderID, ok := mapping[dirPath]
		if !ok {
			// Fallback to root if directory not in mapping
			remoteFolderID = mapping[localPath]
			if remoteFolderID == "" {
				remoteFolderID = rootFolderID
			}
		}

		// Get file info
		info, err := os.Stat(filePath)
		if err != nil {
			logger.Warn().Str("file", filePath).Err(err).Msg("Skipping file")
			continue
		}

		transferRequests = append(transferRequests, services.TransferRequest{
			Type:   services.TransferTypeUpload,
			Source: filePath,
			Dest:   remoteFolderID,
			Name:   filepath.Base(filePath),
			Size:   info.Size(),
		})
		totalBytes += info.Size()
	}

	// Start transfers
	if len(transferRequests) > 0 {
		if err := ts.StartTransfers(ctx, transferRequests); err != nil {
			return FolderUploadResultDTO{
				FoldersCreated: foldersCreated,
				Error:          "Failed to queue file uploads: " + err.Error(),
			}
		}
	}

	return FolderUploadResultDTO{
		FoldersCreated: foldersCreated,
		FilesQueued:    len(transferRequests),
		TotalBytes:     totalBytes,
	}
}

// =============================================================================
// v4.0.0: G1 - Local File Info Bindings for Single Job Input UX
// =============================================================================

// LocalFileInfoDTO contains information about a local file or directory.
type LocalFileInfoDTO struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	IsDir     bool   `json:"isDir"`
	Size      int64  `json:"size"`      // For files: file size; for dirs: total size of contents
	FileCount int    `json:"fileCount"` // For dirs: number of files inside; for files: 0
	ModTime   string `json:"modTime"`
	Error     string `json:"error,omitempty"`
}

// GetLocalFilesInfo returns file information for a list of paths.
// For directories, it calculates total size and file count recursively.
func (a *App) GetLocalFilesInfo(paths []string) []LocalFileInfoDTO {
	results := make([]LocalFileInfoDTO, len(paths))

	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			results[i] = LocalFileInfoDTO{
				Path:  path,
				Name:  filepath.Base(path),
				Error: err.Error(),
			}
			continue
		}

		dto := LocalFileInfoDTO{
			Path:    path,
			Name:    info.Name(),
			IsDir:   info.IsDir(),
			ModTime: info.ModTime().Format(time.RFC3339),
		}

		if info.IsDir() {
			// Calculate total size and file count for directories
			totalSize, fileCount := calculateDirStats(path)
			dto.Size = totalSize
			dto.FileCount = fileCount
		} else {
			dto.Size = info.Size()
			dto.FileCount = 0
		}

		results[i] = dto
	}

	return results
}

// calculateDirStats recursively calculates total size and file count for a directory.
func calculateDirStats(dirPath string) (totalSize int64, fileCount int) {
	filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Continue on error
		}
		if !info.IsDir() {
			totalSize += info.Size()
			fileCount++
		}
		return nil
	})
	return
}

// SelectDirectoryAndListFiles opens a directory dialog and returns all file paths within.
// This is useful for adding all files from a folder as job inputs.
func (a *App) SelectDirectoryAndListFiles(title string) ([]string, error) {
	// Select directory using Wails runtime
	dir, err := a.SelectDirectory(title)
	if err != nil || dir == "" {
		return nil, err
	}

	// List all files in the directory (non-recursive for now)
	var files []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}

	return files, nil
}

// SelectDirectoryRecursive opens a directory dialog and returns all file paths recursively.
// This includes files in subdirectories.
func (a *App) SelectDirectoryRecursive(title string) ([]string, error) {
	// Select directory using Wails runtime
	dir, err := a.SelectDirectory(title)
	if err != nil || dir == "" {
		return nil, err
	}

	// List all files recursively
	var files []string
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Continue on error
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return files, nil
}
