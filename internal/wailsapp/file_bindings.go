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
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/pathutil"
	"github.com/rescale/rescale-int/internal/reporting"
	"github.com/rescale/rescale-int/internal/services"
	"github.com/rescale/rescale-int/internal/transfer/folder"
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

	// v4.5.8: Resolve junction points before listing to handle Windows cloud VM
	// environments where user folders are junction points to mapped network drives.
	// If resolution fails (junction target inaccessible), fall back to the raw path.
	resolvedPath, resolveErr := pathutil.ResolveAbsolutePath(path)
	if resolveErr != nil {
		resolvedPath = path // Fallback to original if resolution fails
	}

	// v4.0.4: Use shared localfs.ListDirectoryEx() for core directory reading
	// This handles timeout, hidden filtering, and parallel symlink resolution
	entries, err := localfs.ListDirectoryEx(ctx, resolvedPath, localfs.ListDirectoryExOptions{
		IncludeHidden:   includeHidden,
		ResolveSymlinks: true,
		SymlinkWorkers:  constants.SymlinkWorkerCount,
		Timeout:         constants.DirectoryReadTimeout,
	})

	// v4.5.8: If resolved path failed and it differs from original, try original path
	if err != nil && resolvedPath != path {
		entries, err = localfs.ListDirectoryEx(ctx, path, localfs.ListDirectoryExOptions{
			IncludeHidden:   includeHidden,
			ResolveSymlinks: true,
			SymlinkWorkers:  constants.SymlinkWorkerCount,
			Timeout:         constants.DirectoryReadTimeout,
		})
	}

	// Handle errors
	if err != nil {
		warning := err.Error()
		isSlowPath := false
		if err == context.DeadlineExceeded {
			warning = fmt.Sprintf("Timeout reading directory after %v", constants.DirectoryReadTimeout)
			isSlowPath = true
		} else if err == context.Canceled {
			warning = "Operation cancelled"
		} else if strings.Contains(warning, "mount point") || strings.Contains(warning, "reparse point") ||
			strings.Contains(warning, "untrusted") {
			// v4.5.8: Provide user-friendly error for junction/reparse-point failures
			warning = fmt.Sprintf("Cannot access directory (may be a junction to an inaccessible drive): %v", err)
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

// ValidateRemoteFolder checks that a remote folder exists and is accessible.
// v4.8.6: Lightweight preflight check — fetches a single item from the first page.
// Does NOT use FolderCache.Get() (which fetches all pages).
func (a *App) ValidateRemoteFolder(folderID string) error {
	if a.engine == nil {
		return ErrNoEngine
	}
	apiClient := a.engine.API()
	if apiClient == nil {
		return fmt.Errorf("API client not configured")
	}
	if folderID == "" {
		return fmt.Errorf("no destination folder specified")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := apiClient.ListFolderContentsPage(ctx, folderID, "", 1)
	if err != nil {
		return fmt.Errorf("folder not found or inaccessible: %w", err)
	}
	return nil
}

// ValidateLocalDirectory checks that a local directory exists and is a directory.
// v4.8.6: Lightweight preflight check for download destination.
func (a *App) ValidateLocalDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("directory not found: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", path)
	}
	return nil
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
// v4.7.7: Restructured enumeration events — emits EventEnumerationCompleted only after
// StartTransfers succeeds, with deferred completion on all error paths.
// folderName: the display name for the folder (used as the local folder name)
// v4.8.0: Rewritten for streaming scan+download — returns immediately, downloads begin within seconds.
func (a *App) StartFolderDownload(folderID string, folderName string, destPath string) FolderDownloadResultDTO {
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

	enumID := fmt.Sprintf("enum_dl_%d", time.Now().UnixNano())
	emitEnumeration := func(eventType events.EventType, foldersFound, filesFound int, bytesFound int64, isComplete bool, errMsg string, statusMessage string, phase string) {
		if a.engine != nil && a.engine.Events() != nil {
			a.engine.Events().Publish(&events.EnumerationEvent{
				BaseEvent:     events.BaseEvent{EventType: eventType, Time: time.Now()},
				ID:            enumID,
				FolderName:    displayName,
				Direction:     "download",
				FoldersFound:  foldersFound,
				FilesFound:    filesFound,
				BytesFound:    bytesFound,
				IsComplete:    isComplete,
				Error:         errMsg,
				StatusMessage: statusMessage,
				Phase:         phase,
			})
		}
	}

	// v4.8.3: Timing instrumentation for Issue #1 diagnosis
	startTime := time.Now()
	emitLog(events.InfoLevel, fmt.Sprintf("[TIMING] StartFolderDownload entry at %s — folder=%s dest=%s", startTime.Format("15:04:05.000"), displayName, destPath))

	if a.engine == nil {
		emitLog(events.ErrorLevel, "Engine not initialized")
		emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, ErrNoEngine.Error(), "", events.EnumPhaseError)
		return FolderDownloadResultDTO{Error: ErrNoEngine.Error()}
	}

	apiClient := a.engine.API()
	if apiClient == nil {
		emitLog(events.ErrorLevel, "API client not configured")
		emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, "API client not configured", "", events.EnumPhaseError)
		return FolderDownloadResultDTO{Error: "API client not configured"}
	}

	ts := a.engine.TransferService()
	if ts == nil {
		emitLog(events.ErrorLevel, "TransferService not available")
		emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, ErrNoTransferService.Error(), "", events.EnumPhaseError)
		return FolderDownloadResultDTO{Error: ErrNoTransferService.Error()}
	}

	// v4.8.2: Unified cancellable context — CancelBatch() → scanCancel() → stops everything
	scanCtx, scanCancel := context.WithCancel(context.Background())

	// Emit enumeration started
	emitEnumeration(events.EventEnumerationStarted, 0, 0, 0, false, "", "", events.EnumPhaseScanning)

	// v4.8.6: Validate destination path exists before creating subdirectories
	if info, err := os.Stat(destPath); err != nil {
		scanCancel()
		errMsg := fmt.Sprintf("Download destination not found: %s", err.Error())
		emitLog(events.ErrorLevel, errMsg)
		emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, errMsg, "", events.EnumPhaseError)
		return FolderDownloadResultDTO{Error: errMsg}
	} else if !info.IsDir() {
		scanCancel()
		errMsg := fmt.Sprintf("Download destination is not a directory: %s", destPath)
		emitLog(events.ErrorLevel, errMsg)
		emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, errMsg, "", events.EnumPhaseError)
		return FolderDownloadResultDTO{Error: errMsg}
	}

	// Create root output directory
	rootFolderName := folderName
	if rootFolderName == "" {
		rootFolderName = folderID
	}
	rootOutputDir := filepath.Join(destPath, rootFolderName)
	if err := os.MkdirAll(rootOutputDir, 0755); err != nil {
		scanCancel() // Release context resources on early return
		emitLog(events.ErrorLevel, fmt.Sprintf("Failed to create root folder: %s", err.Error()))
		emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, "Failed to create root folder: "+err.Error(), "", events.EnumPhaseError)
		return FolderDownloadResultDTO{Error: "Failed to create root folder: " + err.Error()}
	}

	// v4.8.2: Warm proxy before first API call
	inthttp.WarmupProxyIfNeeded(scanCtx, apiClient.GetConfig())

	// v4.8.0: Start streaming scan — files emitted as discovered
	emitLog(events.InfoLevel, fmt.Sprintf("Scanning folder '%s' for files to download...", displayName))
	// v4.8.2: No enumeration progress during scan — batch row handles progress display.
	// This eliminates phantom "Scanning" row flashing caused by EventEnumerationProgress
	// re-creating the enumeration after reconciliation removes it.
	emitLog(events.InfoLevel, fmt.Sprintf("[TIMING] Starting ScanRemoteFolderStreaming — elapsed=%s", time.Since(startTime)))
	scanEventCh, scanErrCh := cli.ScanRemoteFolderStreaming(scanCtx, apiClient, folderID, nil)

	// Create request channel for streaming batch
	requestCh := make(chan services.TransferRequest, constants.DispatchChannelBuffer)

	// Mark scan in progress for TotalKnown tracking
	ts.GetQueue().MarkBatchScanInProgress(enumID, true)

	// Start streaming download batch (workers start consuming immediately)
	// v4.8.2: Pass scanCancel so CancelBatch() → scanCancel() → cancels scanCtx → stops everything
	if err := ts.StartStreamingDownloadBatch(scanCtx, requestCh, enumID, displayName, "FileBrowser", scanCancel); err != nil {
		scanCancel() // Stop scan goroutines on batch start failure
		emitLog(events.ErrorLevel, fmt.Sprintf("Failed to start streaming batch: %s", err.Error()))
		close(requestCh)
		ts.GetQueue().MarkBatchScanInProgress(enumID, false)
		emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, err.Error(), "", events.EnumPhaseError)
		return FolderDownloadResultDTO{Error: err.Error()}
	}

	// v4.8.0: Scan-consumer goroutine — owns requestCh, closes it on all exit paths
	go func() {
		defer close(requestCh)

		var completionOnce sync.Once
		emitCompletion := func(folders, files int, bytes int64, errMsg string) {
			completionOnce.Do(func() {
				phase := events.EnumPhaseComplete
				if errMsg != "" {
					phase = events.EnumPhaseError
				}
				ts.GetQueue().MarkBatchScanInProgress(enumID, false)
				emitEnumeration(events.EventEnumerationCompleted, folders, files, bytes, true, errMsg, "", phase)
			})
		}
		// Safety net: ensure completion is emitted even on panic
		defer emitCompletion(0, 0, 0, "scan goroutine exited unexpectedly")

		var foldersCreated int
		var filesQueued int
		var totalBytes int64
		firstScanEvent := true
		firstFileQueued := true

		for event := range scanEventCh {
			if firstScanEvent {
				emitLog(events.InfoLevel, fmt.Sprintf("[TIMING] First scan event received — elapsed=%s", time.Since(startTime)))
				firstScanEvent = false
			}
			if event.Folder != nil {
				// Create local directory
				localPath := filepath.Join(rootOutputDir, event.Folder.RelativePath)
				if err := os.MkdirAll(localPath, 0755); err != nil {
					emitLog(events.WarnLevel, fmt.Sprintf("Failed to create folder %s: %s", localPath, err.Error()))
				} else {
					foldersCreated++
				}
			}
			if event.File != nil {
				localPath := filepath.Join(rootOutputDir, event.File.RelativePath)
				req := services.TransferRequest{
					Type:       services.TransferTypeDownload,
					Source:     event.File.FileID,
					Dest:       localPath,
					Name:       event.File.Name,
					Size:       event.File.Size,
					BatchID:    enumID,
					BatchLabel: displayName,
					FileInfo:   event.File.CloudFile,
				}
				select {
				case requestCh <- req:
					if firstFileQueued {
						emitLog(events.InfoLevel, fmt.Sprintf("[TIMING] First file queued — elapsed=%s", time.Since(startTime)))
						firstFileQueued = false
					}
					filesQueued++
					totalBytes += event.File.Size
				case <-scanCtx.Done():
					emitCompletion(foldersCreated, filesQueued, totalBytes, "")
					return
				}
			}
		}

		emitLog(events.InfoLevel, fmt.Sprintf("[TIMING] Scan loop exited — folders=%d files=%d elapsed=%s", foldersCreated, filesQueued, time.Since(startTime)))

		// Check for scan errors
		var scanErr error
		select {
		case scanErr = <-scanErrCh:
		default:
		}

		// v4.8.2: Only report as error if we weren't cancelled — cancel is clean termination
		if scanErr != nil && scanCtx.Err() == nil {
			emitLog(events.ErrorLevel, fmt.Sprintf("Scan error: %s", scanErr.Error()))
			emitCompletion(foldersCreated, filesQueued, totalBytes, scanErr.Error())
			// v4.8.7: Report scan failure if no files were queued (total failure)
			if filesQueued == 0 && a.reporter != nil {
				a.reporter.Report(scanErr, reporting.CategoryTransfer, "folder_download", "")
			}
			return
		}

		emitLog(events.InfoLevel, fmt.Sprintf("Scan complete: %d folders, %d files (%.2f MB). Downloads in progress.",
			foldersCreated, filesQueued, float64(totalBytes)/(1024*1024)))
		emitCompletion(foldersCreated, filesQueued, totalBytes, "")
	}()

	// Return immediately — scan and downloads proceed in background
	return FolderDownloadResultDTO{
		FoldersCreated:  1, // Root folder created synchronously
		FilesDownloaded: 0, // Streaming — files are being discovered/queued
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

	cache := folder.NewFolderCache()
	folderID, exists, err := folder.CheckFolderExists(ctx, apiClient, cache, parentFolderID, folderName)
	if err != nil {
		return FolderExistsCheckDTO{Error: "Failed to check folder: " + err.Error()}
	}

	return FolderExistsCheckDTO{
		Exists:   exists,
		FolderID: folderID,
	}
}

// CheckFoldersExistForUpload checks if multiple folders with the given names already exist
// in the destination folder. v4.8.5 bugfix: Uses a shared FolderCache so that parent folder
// contents are fetched once instead of once per folder, reducing the "Checking for existing
// folders..." delay from 10-20s to <1s.
func (a *App) CheckFoldersExistForUpload(folderNames []string, parentFolderID string) []FolderExistsCheckDTO {
	ctx := context.Background()
	results := make([]FolderExistsCheckDTO, len(folderNames))

	if a.engine == nil {
		for i := range results {
			results[i] = FolderExistsCheckDTO{Error: ErrNoEngine.Error()}
		}
		return results
	}

	apiClient := a.engine.API()
	if apiClient == nil {
		for i := range results {
			results[i] = FolderExistsCheckDTO{Error: "API client not configured"}
		}
		return results
	}

	// Single shared cache — parent folder contents fetched once for all checks
	cache := folder.NewFolderCache()
	for i, name := range folderNames {
		folderID, exists, err := folder.CheckFolderExists(ctx, apiClient, cache, parentFolderID, name)
		if err != nil {
			results[i] = FolderExistsCheckDTO{Error: "Failed to check folder: " + err.Error()}
		} else {
			results[i] = FolderExistsCheckDTO{
				Exists:   exists,
				FolderID: folderID,
			}
		}
	}

	return results
}

// folderProgressWriter is an io.Writer that counts folder creation lines
// and emits enumeration progress events to keep the UI updated during
// the CreateFolderStructure phase. v4.7.7: Bridges folder creation to enumeration events.
type folderProgressWriter struct {
	enumID       string
	folderName   string
	direction    string
	filesFound   int
	foldersFound int
	bytesFound   int64
	totalDirs    int
	dirsProcessed int
	eventBus     *events.EventBus
}

func (w *folderProgressWriter) Write(p []byte) (n int, err error) {
	w.dirsProcessed++
	// Emit progress every folder (or every 3rd if >100 dirs, to reduce event volume)
	if w.totalDirs <= 100 || w.dirsProcessed%3 == 0 || w.dirsProcessed == w.totalDirs {
		if w.eventBus != nil {
			w.eventBus.Publish(&events.EnumerationEvent{
				BaseEvent:      events.BaseEvent{EventType: events.EventEnumerationProgress, Time: time.Now()},
				ID:             w.enumID,
				FolderName:     w.folderName,
				Direction:      w.direction,
				FoldersFound:   w.foldersFound,
				FilesFound:     w.filesFound,
				BytesFound:     w.bytesFound,
				IsComplete:     false,
				StatusMessage:  fmt.Sprintf("Creating folders... (%d of %d)", w.dirsProcessed, w.totalDirs),
				Phase:          events.EnumPhaseCreatingFolders,
				FoldersTotal:   w.totalDirs,
				FoldersCreated: w.dirsProcessed,
			})
		}
	}
	return len(p), nil
}

// StartFolderUpload uploads a local folder recursively to the Rescale platform.
// v4.0.0: Implements folder upload in GUI by creating folder structure and
// queueing files to the TransferService for upload with progress events.
// v4.0.8: Added enumeration events for real-time scanning progress in Transfers tab.
// v4.7.4: Added tags parameter for post-upload tagging.
// v4.7.7: Restructured enumeration events — keeps enumeration row alive through folder
// creation phase, emits EventEnumerationCompleted only after StartTransfers succeeds.
// Uses merge mode (reuse existing folders) and queues all files for upload.
func (a *App) StartFolderUpload(localPath string, destFolderID string, uploadTags []string) FolderUploadResultDTO {
	displayName := filepath.Base(localPath)
	a.logInfo("folder-upload", fmt.Sprintf("Starting folder upload: %s", displayName))

	// v4.0.8: Helper to emit enumeration events
	enumID := fmt.Sprintf("enum_ul_%d", time.Now().UnixNano())
	emitEnumeration := func(eventType events.EventType, foldersFound, filesFound int, bytesFound int64, isComplete bool, errMsg string, statusMessage string, phase string) {
		if a.engine != nil && a.engine.Events() != nil {
			a.engine.Events().Publish(&events.EnumerationEvent{
				BaseEvent:     events.BaseEvent{EventType: eventType, Time: time.Now()},
				ID:            enumID,
				FolderName:    displayName,
				Direction:     "upload",
				FoldersFound:  foldersFound,
				FilesFound:    filesFound,
				BytesFound:    bytesFound,
				IsComplete:    isComplete,
				Error:         errMsg,
				StatusMessage: statusMessage,
				Phase:         phase,
			})
		}
	}

	emitLog := func(level events.LogLevel, msg string) {
		if a.engine != nil && a.engine.Events() != nil {
			a.engine.Events().Publish(&events.LogEvent{
				BaseEvent: events.BaseEvent{EventType: events.EventLog, Time: time.Now()},
				Level:     level,
				Message:   msg,
				Stage:     "folder-upload",
				JobName:   displayName,
			})
		}
	}

	// v4.7.7: Deferred completion — ensures EventEnumerationCompleted is emitted on ALL exit paths
	completionEmitted := false
	var deferredError string
	defer func() {
		if !completionEmitted {
			phase := events.EnumPhaseComplete
			if deferredError != "" {
				phase = events.EnumPhaseError
			}
			emitEnumeration(events.EventEnumerationCompleted, 0, 0, 0, true, deferredError, "", phase)
		}
	}()

	if a.engine == nil {
		a.logError("folder-upload", "Engine not initialized")
		deferredError = ErrNoEngine.Error()
		return FolderUploadResultDTO{Error: ErrNoEngine.Error()}
	}

	// Get API client from engine
	apiClient := a.engine.API()
	if apiClient == nil {
		deferredError = "API client not configured"
		return FolderUploadResultDTO{Error: "API client not configured"}
	}

	// Get TransferService for queueing file uploads
	ts := a.engine.TransferService()
	if ts == nil {
		deferredError = ErrNoTransferService.Error()
		return FolderUploadResultDTO{Error: ErrNoTransferService.Error()}
	}

	// Validate local path
	fileInfo, err := os.Stat(localPath)
	if err != nil {
		deferredError = "Failed to access directory: " + err.Error()
		return FolderUploadResultDTO{Error: deferredError}
	}
	if !fileInfo.IsDir() {
		deferredError = "Path is not a directory: " + localPath
		return FolderUploadResultDTO{Error: deferredError}
	}

	// Create logger for this upload
	logger := logging.NewLogger("folder-upload", nil)
	ctx := context.Background()

	// v4.8.2: Warm proxy before first API call
	inthttp.WarmupProxyIfNeeded(ctx, apiClient.GetConfig())

	// v4.0.8: Emit enumeration started event
	emitEnumeration(events.EventEnumerationStarted, 0, 0, 0, false, "", "", events.EnumPhaseScanning)

	// Get parent folder ID (default to My Library if empty)
	parentID := destFolderID
	if parentID == "" {
		folders, err := apiClient.GetRootFolders(ctx)
		if err != nil {
			deferredError = err.Error()
			// v4.8.7: Report pre-transfer API failure
			if a.reporter != nil {
				a.reporter.Report(err, reporting.CategoryTransfer, "folder_upload", "")
			}
			return FolderUploadResultDTO{Error: "Failed to get root folders: " + err.Error()}
		}
		parentID = folders.MyLibrary
	}

	// v4.8.6: Validate destination folder exists before starting heavy work
	if parentID != "" {
		valCtx, valCancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := apiClient.ListFolderContentsPage(valCtx, parentID, "", 1); err != nil {
			valCancel()
			deferredError = fmt.Sprintf("Destination folder not found or inaccessible: %s", err.Error())
			// v4.8.7: Report pre-transfer API failure
			if a.reporter != nil {
				a.reporter.Report(err, reporting.CategoryTransfer, "folder_upload", "")
			}
			return FolderUploadResultDTO{Error: deferredError}
		}
		valCancel()
	}

	// Initialize folder cache for API call optimization
	cache := folder.NewFolderCache()

	// Create or get root folder (merge mode: use existing if it exists)
	rootFolderName := filepath.Base(localPath)
	a.logInfo("folder-upload", fmt.Sprintf("Checking if folder '%s' exists in parent %s...", rootFolderName, parentID))
	rootFolderID, exists, err := folder.CheckFolderExists(ctx, apiClient, cache, parentID, rootFolderName)
	if err != nil {
		a.logError("folder-upload", fmt.Sprintf("Failed to check root folder: %v", err))
		deferredError = "Failed to check root folder: " + err.Error()
		return FolderUploadResultDTO{Error: deferredError}
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
				cache.Invalidate(parentID)
				existingID, found, findErr := folder.CheckFolderExists(ctx, apiClient, cache, parentID, rootFolderName)
				if findErr != nil {
					a.logError("folder-upload", fmt.Sprintf("Failed to find existing folder: %v", findErr))
					deferredError = "A folder named '" + rootFolderName + "' already exists but couldn't be accessed"
					return FolderUploadResultDTO{Error: "A folder named '" + rootFolderName + "' already exists but couldn't be accessed. Please check your Rescale Trash and permanently delete it, then try again."}
				}
				if found {
					rootFolderID = existingID
					mergedIntoFolder = rootFolderName
					a.logInfo("folder-upload", fmt.Sprintf("Found existing folder '%s' (ID: %s) - uploading files into it", rootFolderName, rootFolderID))
				} else {
					a.logError("folder-upload", fmt.Sprintf("Folder '%s' exists but is not visible - may be in Trash", rootFolderName))
					deferredError = "Folder exists but is not visible - may be in Trash"
					return FolderUploadResultDTO{Error: "A folder named '" + rootFolderName + "' already exists but is not visible. Please check your Rescale Trash and permanently delete it, then try again."}
				}
			} else {
				a.logError("folder-upload", fmt.Sprintf("Failed to create root folder: %v", err))
				deferredError = translateAPIError(err)
				return FolderUploadResultDTO{Error: "Failed to create folder: " + translateAPIError(err)}
			}
		} else {
			a.logInfo("folder-upload", fmt.Sprintf("Created root folder with ID: %s", rootFolderID))
			foldersCreated++
		}
	} else {
		mergedIntoFolder = rootFolderName
		a.logInfo("folder-upload", fmt.Sprintf("Found existing folder '%s' (ID: %s) - uploading files into it", rootFolderName, rootFolderID))
	}

	// Populate cache for root folder
	if _, err := cache.Get(ctx, apiClient, rootFolderID); err != nil {
		logger.Warn().Err(err).Str("folderID", rootFolderID).Msg("Failed to warm cache for root folder")
	}

	// v4.8.7 Plan 2b: Shared orchestrator replaces ~280 lines of inline pipeline.
	uploadCtx, uploadCancel := context.WithCancel(context.Background())

	requestCh := make(chan services.TransferRequest, constants.DispatchChannelBuffer)

	// Mark scan in progress + start streaming upload batch
	ts.GetQueue().MarkBatchScanInProgress(enumID, true)

	if err := ts.StartStreamingUploadBatch(uploadCtx, requestCh, enumID, displayName, "FileBrowser", uploadCancel); err != nil {
		uploadCancel()
		close(requestCh)
		ts.GetQueue().MarkBatchScanInProgress(enumID, false)
		deferredError = "Failed to start streaming batch: " + err.Error()
		return FolderUploadResultDTO{Error: deferredError}
	}

	var completionOnce sync.Once
	emitCompletion := func(foldersFound, filesFound int, bytesFound int64, errMsg string) {
		completionOnce.Do(func() {
			phase := events.EnumPhaseComplete
			if errMsg != "" {
				phase = events.EnumPhaseError
			}
			emitEnumeration(events.EventEnumerationCompleted,
				foldersFound, filesFound, bytesFound, true, errMsg, "", phase)
		})
	}

	_, _ = folder.RunOrchestrator(uploadCtx,
		folder.OrchestratorConfig{
			RootPath:          localPath,
			RootRemoteID:      rootFolderID,
			IncludeHidden:     true,
			FolderConcurrency: constants.DefaultFolderConcurrency,
			ConflictMode:      folder.ConflictMergeAll,
			ConflictPrompt:    nil, // GUI: always merge, no interactive prompt
			Logger:            logger,
			APIClient:         apiClient,
			Cache:             cache,
		},
		folder.OrchestratorCallbacks[services.TransferRequest]{
			OnFileDiscovered: func(snap folder.ProgressSnapshot) {
				// v4.8.5 bugfix: Update discovered totals on every file so the polling
				// path always has an accurate count.
				ts.GetQueue().UpdateBatchDiscovered(enumID, snap.TotalFiles, snap.TotalBytes)
				// Enumeration events emitted every 100 files to limit event volume.
				if snap.TotalFiles%100 == 0 || snap.TotalFiles == 1 {
					emitEnumeration(events.EventEnumerationProgress,
						snap.TotalDirs, snap.TotalFiles, snap.TotalBytes, false, "",
						fmt.Sprintf("Scanning local files... (%d found)", snap.TotalFiles),
						events.EnumPhaseScanning)
				}
			},
			OnFolderReady: func(snap folder.ProgressSnapshot, localPath, remoteID string) {
				// v4.8.5: Emit folder creation progress every 3 folders
				if snap.TotalDirs%3 == 0 || snap.TotalDirs == 1 {
					emitEnumeration(events.EventEnumerationProgress,
						snap.TotalDirs, snap.TotalFiles, snap.TotalBytes, false, "",
						fmt.Sprintf("Creating remote folders... (%d created)", snap.TotalDirs),
						events.EnumPhaseCreatingFolders)
				}
			},
			BuildItem: func(file localfs.FileEntry, remoteFolderID, rootPath string) services.TransferRequest {
				return services.TransferRequest{
					Type:        services.TransferTypeUpload,
					Source:      file.Path,
					Dest:        remoteFolderID,
					Name:        file.Name,
					Size:        file.Size,
					SourceLabel: services.SourceLabelFileBrowser,
					BatchID:     enumID,
					BatchLabel:  displayName,
					Tags:        uploadTags,
				}
			},
			OnUnmappedFiles: func(parentDir string, count int) {
				emitLog(events.WarnLevel, fmt.Sprintf("Skipping %d files in unmapped folder: %s", count, parentDir))
			},
			OnOrchestratorDone: func(r *folder.OrchestratorResult) {
				// v4.8.5 bugfix preserved: scan marked complete when discovery
				// count is final, BEFORE dispatcher drains (which blocks for minutes).
				ts.GetQueue().MarkBatchScanInProgress(enumID, false)

				// Final update of discovered totals
				ts.GetQueue().UpdateBatchDiscovered(enumID, r.DiscoveredFiles, r.DiscoveredBytes)

				errMsg := ""
				if r.WalkError != nil {
					emitLog(events.ErrorLevel, fmt.Sprintf("Walk error: %v", r.WalkError))
					errMsg = r.WalkError.Error()
				}
				if r.FolderError != nil {
					emitLog(events.ErrorLevel, fmt.Sprintf("Folder creation error: %v", r.FolderError))
					if errMsg == "" {
						errMsg = "Folder creation failed: " + r.FolderError.Error()
					}
				} else {
					emitLog(events.InfoLevel, fmt.Sprintf("Created %d remote folders", r.FoldersCreated))
				}
				emitLog(events.InfoLevel, fmt.Sprintf("All files queued for upload (%d discovered)", r.DiscoveredFiles))
				emitCompletion(r.DiscoveredDirs, r.DiscoveredFiles, r.DiscoveredBytes, errMsg)
			},
		},
		requestCh,
	)

	// Orchestrator owns completion via OnOrchestratorDone — prevent deferred emitter
	completionEmitted = true

	// v4.8.5: Return immediately with FilesQueued=0 — files are discovered asynchronously.
	// Frontend already handles !totalKnown state. Batch events drive UI updates.
	return FolderUploadResultDTO{
		FoldersCreated: foldersCreated,
		FilesQueued:    0,
		TotalBytes:     0,
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
