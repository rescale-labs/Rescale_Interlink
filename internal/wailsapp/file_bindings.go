// Package wailsapp provides file-related Wails bindings.
package wailsapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/localfs"
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
// v4.0.3: Added IsSlowPath and Warning fields for robustness feedback.
type FolderContentsDTO struct {
	FolderID   string        `json:"folderId"`
	FolderPath string        `json:"folderPath"`
	Items      []FileItemDTO `json:"items"`
	HasMore    bool          `json:"hasMore"`
	NextCursor string        `json:"nextCursor,omitempty"`
	IsSlowPath bool          `json:"isSlowPath,omitempty"` // v4.0.3: True if directory took >5s to read
	Warning    string        `json:"warning,omitempty"`    // v4.0.3: Timeout or error message
}

// DeleteResultDTO contains the result of a delete operation.
type DeleteResultDTO struct {
	Deleted int    `json:"deleted"`
	Failed  int    `json:"failed"`
	Error   string `json:"error,omitempty"`
}

// localDirCancelMu protects localDirCancelFunc and localDirGeneration for concurrent access.
var localDirCancelMu sync.Mutex

// localDirCancelFunc stores the cancel function for the current local directory operation.
var localDirCancelFunc context.CancelFunc

// localDirGeneration tracks the generation of the current directory operation.
// Used to avoid clearing a newer operation's cancel function.
var localDirGeneration int64

// localEntryInfo holds information about a local directory entry.
// Used during directory listing with symlink resolution.
type localEntryInfo struct {
	entry    os.DirEntry
	fullPath string
	info     os.FileInfo
	isLink   bool
}

// ListLocalDirectory returns the contents of a local directory.
// v4.0.3: Now calls ListLocalDirectoryEx with default options (includeHidden=false).
func (a *App) ListLocalDirectory(path string) FolderContentsDTO {
	return a.ListLocalDirectoryEx(path, false)
}

// ListLocalDirectoryEx returns the contents of a local directory with options.
// v4.0.3: Added robustness features:
//   - Timeout protection: 30 second timeout prevents UI freeze on hung mounts
//   - Hidden file filtering: Pass includeHidden=false to filter dot files (server-side)
//   - Cancellation support: Previous operation is cancelled when new one starts
//   - Parallel symlink resolution: Symlinks are resolved in parallel (8 workers)
func (a *App) ListLocalDirectoryEx(path string, includeHidden bool) FolderContentsDTO {
	// Default to home directory if path is empty
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return FolderContentsDTO{FolderPath: "/", Items: []FileItemDTO{}}
		}
		path = home
	}

	// Cancel any previous directory operation
	localDirCancelMu.Lock()
	if localDirCancelFunc != nil {
		localDirCancelFunc()
	}
	// Create new context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), constants.DirectoryReadTimeout)
	localDirCancelFunc = cancel
	localDirGeneration++
	myGeneration := localDirGeneration
	localDirCancelMu.Unlock()

	defer func() {
		localDirCancelMu.Lock()
		// Only clear if we're still the current operation
		if localDirGeneration == myGeneration {
			localDirCancelFunc = nil
		}
		localDirCancelMu.Unlock()
		cancel()
	}()

	// Track start time for slow path detection
	startTime := time.Now()

	// Read directory in goroutine for timeout protection
	type readResult struct {
		entries []os.DirEntry
		err     error
	}
	resultChan := make(chan readResult, 1)

	go func() {
		entries, err := os.ReadDir(path)
		resultChan <- readResult{entries: entries, err: err}
	}()

	// Wait for result or timeout/cancellation
	var entries []os.DirEntry
	var readErr error
	select {
	case result := <-resultChan:
		entries = result.entries
		readErr = result.err
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return FolderContentsDTO{
				FolderPath: path,
				Items:      []FileItemDTO{},
				Warning:    fmt.Sprintf("Timeout reading directory after %v", constants.DirectoryReadTimeout),
				IsSlowPath: true,
			}
		}
		// Context was cancelled (user navigated away)
		return FolderContentsDTO{
			FolderPath: path,
			Items:      []FileItemDTO{},
			Warning:    "Operation cancelled",
		}
	}

	if readErr != nil {
		return FolderContentsDTO{
			FolderPath: path,
			Items:      []FileItemDTO{},
			Warning:    readErr.Error(),
		}
	}

	// First pass: filter entries and identify symlinks
	var filteredEntries []localEntryInfo
	var symlinks []int // Indices of symlinks that need resolution

	for _, entry := range entries {
		// Hidden file filtering (server-side)
		if !includeHidden && localfs.IsHiddenName(entry.Name()) {
			continue
		}

		fullPath := filepath.Join(path, entry.Name())
		ei := localEntryInfo{
			entry:    entry,
			fullPath: fullPath,
			isLink:   entry.Type()&os.ModeSymlink != 0,
		}

		// Get cached info from DirEntry (fast, no syscall)
		info, err := entry.Info()
		if err != nil {
			// Skip entries we can't stat
			continue
		}
		ei.info = info

		if ei.isLink {
			symlinks = append(symlinks, len(filteredEntries))
		}

		filteredEntries = append(filteredEntries, ei)
	}

	// Second pass: parallel symlink resolution if we have symlinks
	if len(symlinks) > 0 {
		resolveSymlinks(ctx, filteredEntries, symlinks)
	}

	// Check for slow path (>5s)
	elapsed := time.Since(startTime)
	isSlowPath := elapsed > constants.SlowPathWarningThreshold

	// Build result items
	items := make([]FileItemDTO, 0, len(filteredEntries))
	for _, ei := range filteredEntries {
		isDir := ei.entry.IsDir()
		size := ei.info.Size()
		modTime := ei.info.ModTime()

		// For resolved symlinks, use the target info
		if ei.isLink && ei.info != nil {
			isDir = ei.info.IsDir()
			size = ei.info.Size()
			modTime = ei.info.ModTime()
		}

		items = append(items, FileItemDTO{
			ID:       ei.fullPath,
			Name:     ei.entry.Name(),
			IsFolder: isDir,
			Size:     size,
			ModTime:  modTime.Format(time.RFC3339),
			Path:     ei.fullPath,
		})
	}

	// Sort: folders first, then by name
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsFolder != items[j].IsFolder {
			return items[i].IsFolder
		}
		return items[i].Name < items[j].Name
	})

	result := FolderContentsDTO{
		FolderID:   path,
		FolderPath: path,
		Items:      items,
		HasMore:    false,
		IsSlowPath: isSlowPath,
	}

	if isSlowPath {
		result.Warning = fmt.Sprintf("Directory listing took %.1fs", elapsed.Seconds())
	}

	return result
}

// resolveSymlinks resolves symlinks in parallel using a worker pool.
// Updates the info field of localEntryInfo in-place for symlinks.
func resolveSymlinks(ctx context.Context, entries []localEntryInfo, symlinkIndices []int) {
	if len(symlinkIndices) == 0 {
		return
	}

	// Determine worker count
	workerCount := constants.SymlinkWorkerCount
	if len(symlinkIndices) < workerCount {
		workerCount = len(symlinkIndices)
	}

	// Create job channel
	jobs := make(chan int, len(symlinkIndices))
	for _, idx := range symlinkIndices {
		jobs <- idx
	}
	close(jobs)

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case idx, ok := <-jobs:
					if !ok {
						return
					}
					// Resolve symlink target with os.Stat (follows symlinks)
					info, err := os.Stat(entries[idx].fullPath)
					if err == nil {
						entries[idx].info = info
					}
					// On error, keep original cached info (shows as broken symlink)
				}
			}
		}()
	}

	wg.Wait()
}

// CancelLocalDirectoryRead cancels the current local directory read operation.
// Call this when the user navigates away before the directory listing completes.
// v4.0.3: Exposed for frontend cancellation support.
func (a *App) CancelLocalDirectoryRead() {
	localDirCancelMu.Lock()
	defer localDirCancelMu.Unlock()
	if localDirCancelFunc != nil {
		localDirCancelFunc()
		localDirCancelFunc = nil
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

// ListRemoteFolder returns the contents of a remote folder (first page only).
// Deprecated: Use ListRemoteFolderPage for paginated access.
func (a *App) ListRemoteFolder(folderID string) FolderContentsDTO {
	return a.ListRemoteFolderPage(folderID, "", 0)
}

// ListRemoteFolderPage returns a single page of remote folder contents.
// Pass empty cursor for first page, or use nextCursor from previous response.
// v4.0.2: Added for proper server-side pagination in File Browser.
// v4.0.3: Added pageSize parameter - pass 0 for API default.
func (a *App) ListRemoteFolderPage(folderID string, cursor string, pageSize int) FolderContentsDTO {
	if a.engine == nil {
		return FolderContentsDTO{}
	}

	fs := a.engine.FileService()
	if fs == nil {
		return FolderContentsDTO{}
	}

	ctx := context.Background()
	contents, err := fs.ListFolderPage(ctx, folderID, cursor, pageSize)
	if err != nil {
		return FolderContentsDTO{
			FolderID: folderID,
			Items:    []FileItemDTO{},
		}
	}

	return folderContentsToDTO(contents)
}

// ListRemoteLegacy returns a flat list of all files (legacy mode).
// v4.0.3: Added pageSize parameter - pass 0 for API default.
func (a *App) ListRemoteLegacy(cursor string, pageSize int) FolderContentsDTO {
	if a.engine == nil {
		return FolderContentsDTO{}
	}

	fs := a.engine.FileService()
	if fs == nil {
		return FolderContentsDTO{}
	}

	ctx := context.Background()
	contents, err := fs.ListLegacyFiles(ctx, cursor, pageSize)
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
// v4.0.0: Implements folder download in GUI using TransferService for progress tracking.
// Scans remote folder, creates local structure, queues files to TransferService.
// folderName: the display name for the folder (used as the local folder name)
func (a *App) StartFolderDownload(folderID string, folderName string, destPath string) FolderDownloadResultDTO {
	// Helper to emit log events to Activity tab
	displayName := folderName
	if displayName == "" {
		displayName = folderID
	}
	emitLog := func(level events.LogLevel, msg string) {
		if a.engine != nil && a.engine.Events() != nil {
			a.engine.Events().Publish(&events.LogEvent{
				BaseEvent: events.BaseEvent{EventType: events.EventLog, Time: time.Now()},
				Level:     level,
				Message:   msg,
				Stage:     "folder-download",
				JobName:   displayName,
			})
		}
	}

	emitLog(events.InfoLevel, fmt.Sprintf("Starting folder download: %s to %s", displayName, destPath))

	if a.engine == nil {
		emitLog(events.ErrorLevel, "Engine not initialized")
		return FolderDownloadResultDTO{Error: ErrNoEngine.Error()}
	}

	// Get API client from engine
	apiClient := a.engine.API()
	if apiClient == nil {
		emitLog(events.ErrorLevel, "API client not configured")
		return FolderDownloadResultDTO{Error: "API client not configured"}
	}

	// Get TransferService for queueing file downloads
	ts := a.engine.TransferService()
	if ts == nil {
		emitLog(events.ErrorLevel, "TransferService not available")
		return FolderDownloadResultDTO{Error: ErrNoTransferService.Error()}
	}

	ctx := context.Background()

	// Scan remote folder structure using shared CLI function
	emitLog(events.DebugLevel, "Scanning remote folder structure...")
	allFolders, allFiles, err := cli.ScanRemoteFolderRecursive(ctx, apiClient, folderID, "")
	if err != nil {
		emitLog(events.ErrorLevel, fmt.Sprintf("Failed to scan folder: %s", err.Error()))
		return FolderDownloadResultDTO{Error: "Failed to scan remote folder: " + err.Error()}
	}

	emitLog(events.InfoLevel, fmt.Sprintf("Found %d folders, %d files", len(allFolders), len(allFiles)))

	// Determine root folder name
	rootFolderName := folderName
	if rootFolderName == "" {
		rootFolderName = folderID
	}
	rootOutputDir := filepath.Join(destPath, rootFolderName)

	// Create root folder
	if err := os.MkdirAll(rootOutputDir, 0755); err != nil {
		emitLog(events.ErrorLevel, fmt.Sprintf("Failed to create root folder: %s", err.Error()))
		return FolderDownloadResultDTO{Error: "Failed to create root folder: " + err.Error()}
	}
	foldersCreated := 1

	// Create local directory structure
	for _, folder := range allFolders {
		localPath := filepath.Join(rootOutputDir, folder.RelativePath)
		if err := os.MkdirAll(localPath, 0755); err != nil {
			emitLog(events.WarnLevel, fmt.Sprintf("Failed to create folder %s: %s", localPath, err.Error()))
			continue
		}
		foldersCreated++
	}
	emitLog(events.DebugLevel, fmt.Sprintf("Created %d local directories", foldersCreated))

	// Build TransferRequests for each file
	var totalBytes int64
	var transferRequests []services.TransferRequest

	for _, file := range allFiles {
		localPath := filepath.Join(rootOutputDir, file.RelativePath)
		transferRequests = append(transferRequests, services.TransferRequest{
			Type:   services.TransferTypeDownload,
			Source: file.FileID,    // Remote file ID
			Dest:   localPath,      // Local file path
			Name:   file.Name,
			Size:   file.Size,
		})
		totalBytes += file.Size
	}

	// Queue downloads through TransferService
	if len(transferRequests) > 0 {
		if err := ts.StartTransfers(ctx, transferRequests); err != nil {
			emitLog(events.ErrorLevel, fmt.Sprintf("Failed to queue downloads: %s", err.Error()))
			return FolderDownloadResultDTO{
				FoldersCreated: foldersCreated,
				Error:          "Failed to queue file downloads: " + err.Error(),
			}
		}
	}

	emitLog(events.InfoLevel, fmt.Sprintf("Queued %d files for download (%.2f MB)", len(transferRequests), float64(totalBytes)/(1024*1024)))

	return FolderDownloadResultDTO{
		FoldersCreated:  foldersCreated,
		FilesDownloaded: len(transferRequests), // FilesQueued, will update as they complete
		TotalBytes:      totalBytes,
	}
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
