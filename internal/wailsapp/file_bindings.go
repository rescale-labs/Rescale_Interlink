// Package wailsapp provides file-related Wails bindings.
package wailsapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/services"
)

// translateAPIError converts common API errors to user-friendly messages.
// v4.0.8: Unified error translation for better UX across CLI and GUI.
func translateAPIError(err error) string {
	if err == nil {
		return ""
	}

	errStr := err.Error()
	errLower := strings.ToLower(errStr)

	// Common error patterns and their user-friendly messages
	switch {
	case strings.Contains(errLower, "duplicate") || strings.Contains(errLower, "already exists"):
		return "Item already exists with that name"
	case strings.Contains(errLower, "401") || strings.Contains(errLower, "unauthorized"):
		return "API key is invalid or expired - please update your API key"
	case strings.Contains(errLower, "403") || strings.Contains(errLower, "forbidden"):
		return "Access denied - you don't have permission for this operation"
	case strings.Contains(errLower, "404") || strings.Contains(errLower, "not found"):
		return "Item not found - it may have been deleted or moved"
	case strings.Contains(errLower, "429") || strings.Contains(errLower, "rate limit"):
		return "Rate limit exceeded - please wait a moment and try again"
	case strings.Contains(errLower, "500") || strings.Contains(errLower, "internal server"):
		return "Server error - please try again later"
	case strings.Contains(errLower, "timeout") || strings.Contains(errLower, "deadline exceeded"):
		return "Request timed out - check your network connection"
	case strings.Contains(errLower, "connection refused") || strings.Contains(errLower, "no such host"):
		return "Cannot connect to server - check your network connection"
	default:
		// Pass through the original error for uncommon errors
		return errStr
	}
}

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

// v4.0.4: localEntryInfo and resolveSymlinks were moved to internal/localfs/browser.go
// for North Star alignment (shared code between CLI and GUI).

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
//
// v4.0.4: Refactored to use localfs.ListDirectoryEx() for North Star alignment
// (shared code between CLI and GUI for local filesystem operations).
func (a *App) ListLocalDirectoryEx(path string, includeHidden bool) FolderContentsDTO {
	// Default to home directory if path is empty
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return FolderContentsDTO{FolderPath: "/", Items: []FileItemDTO{}}
		}
		path = home
	}

	// Cancel any previous directory operation (GUI-specific)
	localDirCancelMu.Lock()
	if localDirCancelFunc != nil {
		localDirCancelFunc()
	}
	// Create new context for this operation
	ctx, cancel := context.WithCancel(context.Background())
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

	// Track start time for slow path detection (GUI-specific)
	startTime := time.Now()

	// v4.0.4: Use shared localfs.ListDirectoryEx() for core directory reading
	// This handles timeout, hidden filtering, and parallel symlink resolution
	entries, err := localfs.ListDirectoryEx(ctx, path, localfs.ListDirectoryExOptions{
		IncludeHidden:   includeHidden,
		ResolveSymlinks: true,
		SymlinkWorkers:  constants.SymlinkWorkerCount,
		Timeout:         constants.DirectoryReadTimeout,
	})

	// Handle errors
	if err != nil {
		warning := err.Error()
		isSlowPath := false
		if err == context.DeadlineExceeded {
			warning = fmt.Sprintf("Timeout reading directory after %v", constants.DirectoryReadTimeout)
			isSlowPath = true
		} else if err == context.Canceled {
			warning = "Operation cancelled"
		}
		return FolderContentsDTO{
			FolderPath: path,
			Items:      []FileItemDTO{},
			Warning:    warning,
			IsSlowPath: isSlowPath,
		}
	}

	// Check for slow path (>5s) (GUI-specific warning)
	elapsed := time.Since(startTime)
	isSlowPath := elapsed > constants.SlowPathWarningThreshold

	// Convert localfs.FileEntry to FileItemDTO
	items := make([]FileItemDTO, 0, len(entries))
	for _, entry := range entries {
		items = append(items, FileItemDTO{
			ID:       entry.Path,
			Name:     entry.Name,
			IsFolder: entry.IsDir,
			Size:     entry.Size,
			ModTime:  entry.ModTime.Format(time.RFC3339),
			Path:     entry.Path,
		})
	}

	// Sort: folders first, then by name (GUI-specific ordering)
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
// v4.0.8: Added enumeration events for real-time scanning progress in Transfers tab.
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

	// v4.0.8: Helper to emit enumeration events
	enumID := fmt.Sprintf("enum_dl_%d", time.Now().UnixNano())
	emitEnumeration := func(eventType events.EventType, foldersFound, filesFound int, bytesFound int64, isComplete bool, errMsg string) {
		if a.engine != nil && a.engine.Events() != nil {
			a.engine.Events().Publish(&events.EnumerationEvent{
				BaseEvent:    events.BaseEvent{EventType: eventType, Time: time.Now()},
				ID:           enumID,
				FolderName:   displayName,
				Direction:    "download",
				FoldersFound: foldersFound,
				FilesFound:   filesFound,
				BytesFound:   bytesFound,
				IsComplete:   isComplete,
				Error:        errMsg,
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

	// v4.0.8: Emit enumeration started event
	emitEnumeration(events.EventEnumerationStarted, 0, 0, 0, false, "")

	// Scan remote folder structure using shared CLI function
	// v4.0.5: Changed to InfoLevel so users see scanning progress (issue #19)
	emitLog(events.InfoLevel, fmt.Sprintf("Scanning folder '%s' for files to download...", displayName))
	allFolders, allFiles, err := cli.ScanRemoteFolderRecursive(ctx, apiClient, folderID, "")
	if err != nil {
		emitLog(events.ErrorLevel, fmt.Sprintf("Failed to scan folder: %s", err.Error()))
		emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, err.Error())
		return FolderDownloadResultDTO{Error: "Failed to scan remote folder: " + err.Error()}
	}

	// v4.0.8: Calculate total bytes and emit enumeration completed event
	var scanTotalBytes int64
	for _, file := range allFiles {
		scanTotalBytes += file.Size
	}
	emitEnumeration(events.EventEnumerationCompleted, len(allFolders), len(allFiles), scanTotalBytes, true, "")

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
	MergedInto     string `json:"mergedInto,omitempty"` // v4.0.8: Name of existing folder we merged into (empty if new folder created)
	Error          string `json:"error,omitempty"`
}

// FolderExistsCheckDTO returns info about whether a folder with the given name exists.
// v4.0.8: Used for pre-upload check to prompt user about merge behavior.
type FolderExistsCheckDTO struct {
	Exists   bool   `json:"exists"`            // True if a visible folder with this name exists
	FolderID string `json:"folderId,omitempty"` // ID of existing folder (if found)
	Error    string `json:"error,omitempty"`   // Error message if check failed
}

// CheckFolderExistsForUpload checks if a folder with the given name already exists
// in the destination folder. v4.0.8: Used to show merge confirmation dialog before upload.
func (a *App) CheckFolderExistsForUpload(folderName string, parentFolderID string) FolderExistsCheckDTO {
	ctx := context.Background()

	if a.engine == nil {
		return FolderExistsCheckDTO{Error: ErrNoEngine.Error()}
	}

	apiClient := a.engine.API()
	if apiClient == nil {
		return FolderExistsCheckDTO{Error: "API client not configured"}
	}

	cache := cli.NewFolderCache()
	folderID, exists, err := cli.CheckFolderExists(ctx, apiClient, cache, parentFolderID, folderName)
	if err != nil {
		return FolderExistsCheckDTO{Error: "Failed to check folder: " + err.Error()}
	}

	return FolderExistsCheckDTO{
		Exists:   exists,
		FolderID: folderID,
	}
}

// StartFolderUpload uploads a local folder recursively to the Rescale platform.
// v4.0.0: Implements folder upload in GUI by creating folder structure and
// queueing files to the TransferService for upload with progress events.
// v4.0.8: Added enumeration events for real-time scanning progress in Transfers tab.
// Uses merge mode (reuse existing folders) and queues all files for upload.
func (a *App) StartFolderUpload(localPath string, destFolderID string) FolderUploadResultDTO {
	displayName := filepath.Base(localPath)
	a.logInfo("folder-upload", fmt.Sprintf("Starting folder upload: %s", displayName))

	// v4.0.8: Helper to emit enumeration events
	enumID := fmt.Sprintf("enum_ul_%d", time.Now().UnixNano())
	emitEnumeration := func(eventType events.EventType, foldersFound, filesFound int, bytesFound int64, isComplete bool, errMsg string) {
		if a.engine != nil && a.engine.Events() != nil {
			a.engine.Events().Publish(&events.EnumerationEvent{
				BaseEvent:    events.BaseEvent{EventType: eventType, Time: time.Now()},
				ID:           enumID,
				FolderName:   displayName,
				Direction:    "upload",
				FoldersFound: foldersFound,
				FilesFound:   filesFound,
				BytesFound:   bytesFound,
				IsComplete:   isComplete,
				Error:        errMsg,
			})
		}
	}

	if a.engine == nil {
		a.logError("folder-upload", "Engine not initialized")
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

	// v4.0.8: Emit enumeration started event
	emitEnumeration(events.EventEnumerationStarted, 0, 0, 0, false, "")

	// Get parent folder ID (default to My Library if empty)
	parentID := destFolderID
	if parentID == "" {
		folders, err := apiClient.GetRootFolders(ctx)
		if err != nil {
			emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, err.Error())
			return FolderUploadResultDTO{Error: "Failed to get root folders: " + err.Error()}
		}
		parentID = folders.MyLibrary
	}

	// Build directory tree (include hidden files for completeness)
	directories, files, _, err := cli.BuildDirectoryTree(localPath, true)
	if err != nil {
		emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, err.Error())
		return FolderUploadResultDTO{Error: "Failed to scan directory: " + err.Error()}
	}

	// v4.0.8: Calculate total bytes from scanned files for enumeration event
	var scanTotalBytes int64
	for _, filePath := range files {
		if info, err := os.Stat(filePath); err == nil {
			scanTotalBytes += info.Size()
		}
	}
	emitEnumeration(events.EventEnumerationCompleted, len(directories), len(files), scanTotalBytes, true, "")
	a.logInfo("folder-upload", fmt.Sprintf("Scan complete: %d folders, %d files", len(directories), len(files)))

	// Initialize folder cache for API call optimization
	cache := cli.NewFolderCache()

	// Create or get root folder (merge mode: use existing if it exists)
	rootFolderName := filepath.Base(localPath)
	a.logInfo("folder-upload", fmt.Sprintf("Checking if folder '%s' exists in parent %s...", rootFolderName, parentID))
	rootFolderID, exists, err := cli.CheckFolderExists(ctx, apiClient, cache, parentID, rootFolderName)
	if err != nil {
		a.logError("folder-upload", fmt.Sprintf("Failed to check root folder: %v", err))
		return FolderUploadResultDTO{Error: "Failed to check root folder: " + err.Error()}
	}
	a.logInfo("folder-upload", fmt.Sprintf("Folder check complete: exists=%v, id=%s", exists, rootFolderID))

	foldersCreated := 0
	mergedIntoFolder := "" // v4.0.8: Track if we merged into existing folder
	if !exists {
		a.logInfo("folder-upload", fmt.Sprintf("Creating root folder '%s'...", rootFolderName))
		rootFolderID, err = apiClient.CreateFolder(ctx, rootFolderName, parentID)
		if err != nil {
			// v4.0.8: Handle "folder already exists" error with clear user guidance
			if api.IsFileExistsError(err) {
				a.logWarn("folder-upload", fmt.Sprintf("Folder '%s' already exists, checking if visible...", rootFolderName))
				// Invalidate cache and re-check to see if we can find it
				cache.Invalidate(parentID)
				existingID, found, findErr := cli.CheckFolderExists(ctx, apiClient, cache, parentID, rootFolderName)
				if findErr != nil {
					a.logError("folder-upload", fmt.Sprintf("Failed to find existing folder: %v", findErr))
					return FolderUploadResultDTO{Error: "A folder named '" + rootFolderName + "' already exists but couldn't be accessed. Please check your Rescale Trash and permanently delete it, then try again."}
				}
				if found {
					// Folder exists and is visible - use it (merge mode)
					rootFolderID = existingID
					mergedIntoFolder = rootFolderName
					a.logInfo("folder-upload", fmt.Sprintf("Found existing folder '%s' (ID: %s) - uploading files into it", rootFolderName, rootFolderID))
				} else {
					// Folder exists (duplicate error) but not visible - likely in Trash
					a.logError("folder-upload", fmt.Sprintf("Folder '%s' exists but is not visible - may be in Trash", rootFolderName))
					return FolderUploadResultDTO{Error: "A folder named '" + rootFolderName + "' already exists but is not visible. Please check your Rescale Trash and permanently delete it, then try again."}
				}
			} else {
				a.logError("folder-upload", fmt.Sprintf("Failed to create root folder: %v", err))
				return FolderUploadResultDTO{Error: "Failed to create folder: " + translateAPIError(err)}
			}
		} else {
			a.logInfo("folder-upload", fmt.Sprintf("Created root folder with ID: %s", rootFolderID))
			foldersCreated++
		}
	} else {
		// Folder already exists and was found - use it (merge mode)
		mergedIntoFolder = rootFolderName
		a.logInfo("folder-upload", fmt.Sprintf("Found existing folder '%s' (ID: %s) - uploading files into it", rootFolderName, rootFolderID))
	}

	// Populate cache for root folder (v4.0.4: log warning if cache warming fails)
	if _, err := cache.Get(ctx, apiClient, rootFolderID); err != nil {
		logger.Warn().Err(err).Str("folderID", rootFolderID).Msg("Failed to warm cache for root folder")
	}

	// Create folder structure with merge mode (skip existing folders)
	folderConflictMode := cli.ConflictMergeAll
	a.logInfo("folder-upload", "Creating folder structure on remote...")
	mapping, created, err := cli.CreateFolderStructure(
		ctx, apiClient, cache, localPath, directories, rootFolderID,
		&folderConflictMode, 3, logger, nil, nil, // 3 = default folder concurrency
	)
	if err != nil {
		a.logError("folder-upload", fmt.Sprintf("Failed to create folders: %v", err))
		return FolderUploadResultDTO{Error: "Failed to create folder structure: " + err.Error()}
	}
	foldersCreated += created
	a.logInfo("folder-upload", fmt.Sprintf("Created %d folders on remote", foldersCreated))

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
		a.logInfo("folder-upload", fmt.Sprintf("Queueing %d files for upload...", len(transferRequests)))
		if err := ts.StartTransfers(ctx, transferRequests); err != nil {
			a.logError("folder-upload", fmt.Sprintf("Failed to queue uploads: %v", err))
			return FolderUploadResultDTO{
				FoldersCreated: foldersCreated,
				Error:          "Failed to queue file uploads: " + err.Error(),
			}
		}
		a.logInfo("folder-upload", fmt.Sprintf("Queued %d files (%.2f MB) for upload", len(transferRequests), float64(totalBytes)/(1024*1024)))
	} else {
		a.logWarn("folder-upload", "No files to upload in folder")
	}

	return FolderUploadResultDTO{
		FoldersCreated: foldersCreated,
		FilesQueued:    len(transferRequests),
		TotalBytes:     totalBytes,
		MergedInto:     mergedIntoFolder,
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
	files := []string{}
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
	files := []string{}
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
