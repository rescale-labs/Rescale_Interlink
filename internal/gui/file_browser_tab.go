// Package gui provides the graphical user interface for rescale-int.
// File browser tab implementation - two-pane layout with local and remote browsers.
package gui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
)

// Note: DefaultMaxConcurrent is defined in internal/constants/app.go

// FileTransferProgress tracks progress of a single file transfer
type FileTransferProgress struct {
	Name         string
	Size         int64
	Progress     float64   // 0.0 to 1.0 (real progress from callback)
	Interpolated float64   // 0.0 to 1.0 (interpolated progress for smooth UI)
	Status       string    // "pending", "transferring", "complete", "error"
	Error        error
	StartTime    time.Time // When transfer started (for rate calculation)
	LastUpdate   time.Time // Last progress update time
	LastBytes    int64     // Bytes transferred at last update (for rate smoothing)
	BytesPerSec  float64   // Estimated transfer speed (bytes/sec)
}

// FileBrowserTab manages the two-pane file browser interface
type FileBrowserTab struct {
	engine *core.Engine
	window fyne.Window

	// Browsers
	localBrowser  *LocalBrowser
	remoteBrowser *RemoteBrowser

	// Transfer buttons
	uploadBtn   *widget.Button
	downloadBtn *widget.Button

	// Delete buttons
	deleteLocalBtn  *widget.Button
	deleteRemoteBtn *widget.Button

	// Progress tracking
	mu             sync.RWMutex
	isTransferring bool
	cancelFunc     context.CancelFunc

	// Progress UI components
	progressPanel        *fyne.Container
	overallProgressBar   *widget.ProgressBar
	overallProgressLabel *widget.Label
	fileProgressList     *fyne.Container // Container holding per-file progress bars
	fileProgressScroll   *container.Scroll // Scrollable container for file progress
	fileProgressBars     map[string]*widget.ProgressBar
	fileProgressLabels   map[string]*widget.Label
	cancelBtn            *widget.Button
	clearBtn             *widget.Button // Shown when transfer completes

	// Transfer tracking
	totalFiles      int32
	completedFiles  int32
	activeTransfers map[string]*FileTransferProgress
	stopInterpolate chan struct{} // Signal to stop progress interpolation ticker

	// Status
	statusBar *StatusBar

	// Logger
	logger *logging.Logger
}

// NewFileBrowserTab creates a new file browser tab
func NewFileBrowserTab(engine *core.Engine, window fyne.Window) *FileBrowserTab {
	return &FileBrowserTab{
		engine:           engine,
		window:           window,
		logger:           logging.NewLogger("file-browser", nil),
		fileProgressBars:  make(map[string]*widget.ProgressBar),
		fileProgressLabels: make(map[string]*widget.Label),
		activeTransfers:   make(map[string]*FileTransferProgress),
	}
}

// Build creates the file browser tab UI
func (fbt *FileBrowserTab) Build() fyne.CanvasObject {
	// Create local browser (left pane)
	fbt.localBrowser = NewLocalBrowser(fbt.window)
	fbt.localBrowser.OnSelectionChanged = func(selected []FileItem) {
		fbt.updateTransferButtons()
	}

	// Create remote browser (right pane)
	fbt.remoteBrowser = NewRemoteBrowser(fbt.engine, fbt.window)
	fbt.remoteBrowser.OnSelectionChanged = func(selected []FileItem) {
		fbt.updateTransferButtons()
	}

	// Upload button (in left pane header)
	fbt.uploadBtn = widget.NewButtonWithIcon("Upload →", theme.UploadIcon(), fbt.onUpload)
	fbt.uploadBtn.Importance = widget.HighImportance
	fbt.uploadBtn.Disable()

	// Download button (in right pane header)
	fbt.downloadBtn = widget.NewButtonWithIcon("← Download", theme.DownloadIcon(), fbt.onDownload)
	fbt.downloadBtn.Importance = widget.HighImportance
	fbt.downloadBtn.Disable()

	// Delete buttons (danger styling)
	fbt.deleteLocalBtn = widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), fbt.onDeleteLocal)
	fbt.deleteLocalBtn.Importance = widget.DangerImportance
	fbt.deleteLocalBtn.Disable()

	fbt.deleteRemoteBtn = widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), fbt.onDeleteRemote)
	fbt.deleteRemoteBtn.Importance = widget.DangerImportance
	fbt.deleteRemoteBtn.Disable()

	// Left pane: Title + Upload/Delete buttons at top, then LocalBrowser
	localTitle := widget.NewLabelWithStyle("Local Files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	// Add spacing between buttons and from edge
	localButtons := container.NewHBox(
		fbt.deleteLocalBtn,
		HorizontalSpacer(8), // Buffer between delete and upload
		fbt.uploadBtn,
		HorizontalSpacer(4), // Buffer from right edge
	)
	localHeaderRow := container.NewBorder(
		nil, nil,
		container.NewHBox(HorizontalSpacer(4), localTitle), // Buffer from left edge
		localButtons,
		nil,
	)
	// Add vertical padding above and below header
	localHeader := container.NewVBox(
		VerticalSpacer(6), // Buffer from top
		localHeaderRow,
		VerticalSpacer(4), // Buffer below header
	)
	leftPane := container.NewBorder(localHeader, nil, nil, nil, fbt.localBrowser)

	// Right pane: Title + Download/Delete buttons at top, then RemoteBrowser
	remoteTitle := widget.NewLabelWithStyle("Rescale Files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	// Add spacing between buttons and from edge
	remoteButtons := container.NewHBox(
		fbt.downloadBtn,
		HorizontalSpacer(8), // Buffer between download and delete
		fbt.deleteRemoteBtn,
		HorizontalSpacer(4), // Buffer from right edge
	)
	remoteHeaderRow := container.NewBorder(
		nil, nil,
		container.NewHBox(HorizontalSpacer(4), remoteTitle), // Buffer from left edge
		remoteButtons,
		nil,
	)
	// Add vertical padding above and below header
	remoteHeader := container.NewVBox(
		VerticalSpacer(6), // Buffer from top
		remoteHeaderRow,
		VerticalSpacer(4), // Buffer below header
	)
	rightPane := container.NewBorder(remoteHeader, nil, nil, nil, fbt.remoteBrowser)

	// Two-pane layout using HSplit (NO middle section - direct split)
	mainContent := container.NewHSplit(leftPane, rightPane)
	mainContent.SetOffset(0.5) // Equal 50/50 split

	// Progress panel (hidden by default)
	fbt.overallProgressBar = widget.NewProgressBar()
	fbt.overallProgressLabel = widget.NewLabel("")
	fbt.fileProgressList = container.NewVBox() // Will hold per-file progress bars
	fbt.cancelBtn = widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), fbt.cancelTransfer)
	fbt.cancelBtn.Importance = widget.DangerImportance

	// Clear button for when transfer is complete
	clearBtn := widget.NewButtonWithIcon("Clear", theme.DeleteIcon(), func() {
		fyne.Do(func() {
			fbt.progressPanel.Hide()
			fbt.fileProgressList.RemoveAll()
			fbt.overallProgressBar.SetValue(0)
			fbt.overallProgressLabel.SetText("")
		})
	})
	clearBtn.Importance = widget.HighImportance
	clearBtn.Hide() // Hidden initially, shown when transfer completes
	fbt.clearBtn = clearBtn

	// Overall progress section: label on its own line, then progress bar with buttons
	// This prevents the label from being cut off on narrow windows
	overallBarRow := container.NewBorder(
		nil, nil,
		nil, // No left element - label is on separate line above
		container.NewHBox(fbt.cancelBtn, clearBtn),
		fbt.overallProgressBar,
	)
	overallRow := container.NewVBox(
		fbt.overallProgressLabel,
		overallBarRow,
	)

	// File progress area (scrollable) - default 100px, resized dynamically based on file count
	fbt.fileProgressScroll = container.NewVScroll(fbt.fileProgressList)
	fbt.fileProgressScroll.SetMinSize(fyne.NewSize(0, 100)) // Default small, expanded when files known

	fbt.progressPanel = container.NewVBox(
		overallRow,
		widget.NewSeparator(),
		fbt.fileProgressScroll,
	)
	fbt.progressPanel.Hide()

	// Status bar - unified status display with level-based icons
	fbt.statusBar = NewStatusBar()
	fbt.statusBar.SetInfo("Select files, then use Upload/Download")

	// Footer with progress and status - more compact
	footer := container.NewVBox(
		fbt.progressPanel,
		fbt.statusBar,
	)

	// Final layout - DON'T use TabContent() because it doesn't expand vertically
	// Use Border container which expands the center content to fill available space
	content := container.NewBorder(
		nil,          // top
		footer,       // bottom (compact footer)
		nil, nil,     // left, right
		mainContent,  // center (expands to fill all available space)
	)

	return content
}

// updateTransferButtons enables/disables transfer buttons based on selection
// Thread-safe: wraps UI updates in fyne.Do for safe calling from any goroutine
func (fbt *FileBrowserTab) updateTransferButtons() {
	fyne.Do(func() {
		fbt.updateTransferButtonsInternal()
	})
}

// updateTransferButtonsInternal is the internal implementation that MUST be called
// from the main thread (either directly or within fyne.Do)
func (fbt *FileBrowserTab) updateTransferButtonsInternal() {
	localCount := fbt.localBrowser.GetSelectedCount()
	remoteCount := fbt.remoteBrowser.GetSelectedCount()

	fbt.mu.RLock()
	isTransferring := fbt.isTransferring
	fbt.mu.RUnlock()

	if isTransferring {
		fbt.uploadBtn.Disable()
		fbt.downloadBtn.Disable()
		fbt.deleteLocalBtn.Disable()
		fbt.deleteRemoteBtn.Disable()
		return
	}

	// Upload and local delete buttons
	if localCount > 0 {
		fbt.uploadBtn.Enable()
		fbt.uploadBtn.SetText(fmt.Sprintf("Upload %d →", localCount))
		fbt.deleteLocalBtn.Enable()
	} else {
		fbt.uploadBtn.Disable()
		fbt.uploadBtn.SetText("Upload →")
		fbt.deleteLocalBtn.Disable()
	}

	// Download and remote delete buttons
	if remoteCount > 0 {
		fbt.downloadBtn.Enable()
		fbt.downloadBtn.SetText(fmt.Sprintf("← Download %d", remoteCount))
		fbt.deleteRemoteBtn.Enable()
	} else {
		fbt.downloadBtn.Disable()
		fbt.downloadBtn.SetText("← Download")
		fbt.deleteRemoteBtn.Disable()
	}
}

// onUpload handles the upload button click
func (fbt *FileBrowserTab) onUpload() {
	selected := fbt.localBrowser.GetSelectedItems()
	if len(selected) == 0 {
		return
	}

	// Get destination folder ID
	destFolderID := fbt.remoteBrowser.GetCurrentFolderID()
	if destFolderID == "" {
		dialog.ShowError(fmt.Errorf("no destination folder selected on Rescale"), fbt.window)
		return
	}

	destPath := fbt.remoteBrowser.GetBreadcrumbPath()

	// Confirm upload
	message := fmt.Sprintf("Upload %d item(s) to:\n%s", len(selected), destPath)
	dialog.ShowConfirm("Confirm Upload", message, func(confirmed bool) {
		if !confirmed {
			return
		}
		go fbt.executeUploadConcurrent(selected, destFolderID)
	}, fbt.window)
}

// onDownload handles the download button click
func (fbt *FileBrowserTab) onDownload() {
	selected := fbt.remoteBrowser.GetSelectedItems()
	if len(selected) == 0 {
		return
	}

	// Get destination path
	destPath := fbt.localBrowser.GetCurrentPath()
	if destPath == "" {
		dialog.ShowError(fmt.Errorf("no destination folder selected locally"), fbt.window)
		return
	}

	// Confirm download
	message := fmt.Sprintf("Download %d item(s) to:\n%s", len(selected), destPath)
	dialog.ShowConfirm("Confirm Download", message, func(confirmed bool) {
		if !confirmed {
			return
		}
		go fbt.executeDownloadConcurrent(selected, destPath)
	}, fbt.window)
}

// onDeleteLocal handles the local delete button click
func (fbt *FileBrowserTab) onDeleteLocal() {
	selected := fbt.localBrowser.GetSelectedItems()
	if len(selected) == 0 {
		return
	}

	message := fmt.Sprintf("Delete %d local item(s)?\n\nThis cannot be undone.", len(selected))
	dialog.ShowConfirm("Confirm Delete", message, func(confirmed bool) {
		if !confirmed {
			return
		}
		go fbt.executeDeleteLocal(selected)
	}, fbt.window)
}

// onDeleteRemote handles the remote delete button click
func (fbt *FileBrowserTab) onDeleteRemote() {
	selected := fbt.remoteBrowser.GetSelectedItems()
	if len(selected) == 0 {
		return
	}

	message := fmt.Sprintf("Delete %d item(s) from Rescale?\n\nThis cannot be undone.", len(selected))
	dialog.ShowConfirm("Confirm Delete", message, func(confirmed bool) {
		if !confirmed {
			return
		}
		go fbt.executeDeleteRemote(selected)
	}, fbt.window)
}

// executeDeleteLocal deletes local files/folders with progress feedback
func (fbt *FileBrowserTab) executeDeleteLocal(items []FileItem) {
	total := len(items)
	if total == 0 {
		return
	}

	// Create progress dialog
	progressLabel := widget.NewLabel(fmt.Sprintf("Deleting 0 of %d items...", total))
	progressBar := widget.NewProgressBar()
	progressBar.Min = 0
	progressBar.Max = float64(total)

	content := container.NewVBox(progressLabel, progressBar)
	progressDialog := dialog.NewCustomWithoutButtons("Deleting Files", content, fbt.window)
	progressDialog.Show()

	var deleted, failed int
	for i, item := range items {
		// Update progress on main thread
		current := i + 1
		fyne.Do(func() {
			progressLabel.SetText(fmt.Sprintf("Deleting %d of %d items...", current, total))
			progressBar.SetValue(float64(current))
		})

		var err error
		if item.IsFolder {
			err = os.RemoveAll(item.ID) // ID is path for local
		} else {
			err = os.Remove(item.ID)
		}
		if err != nil {
			failed++
			fbt.logger.Error().Err(err).Str("path", item.ID).Msg("Delete failed")
		} else {
			deleted++
		}
	}

	// Close dialog and refresh
	fyne.Do(func() {
		progressDialog.Hide()
	})

	fbt.localBrowser.Refresh()
	fbt.localBrowser.ClearSelection()

	if failed > 0 {
		fbt.setStatus(fmt.Sprintf("Deleted %d item(s), %d failed", deleted, failed))
	} else {
		fbt.setStatus(fmt.Sprintf("Deleted %d item(s)", deleted))
	}
}

// executeDeleteRemote deletes remote files/folders via API with progress feedback
func (fbt *FileBrowserTab) executeDeleteRemote(items []FileItem) {
	total := len(items)
	if total == 0 {
		return
	}

	// Use timeout to prevent indefinite hangs if API is unresponsive
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	apiClient := fbt.engine.API()
	if apiClient == nil {
		fbt.showError("Not connected to Rescale")
		return
	}

	// Create progress dialog
	progressLabel := widget.NewLabel(fmt.Sprintf("Deleting 0 of %d items from Rescale...", total))
	progressBar := widget.NewProgressBar()
	progressBar.Min = 0
	progressBar.Max = float64(total)

	content := container.NewVBox(progressLabel, progressBar)
	progressDialog := dialog.NewCustomWithoutButtons("Deleting Remote Files", content, fbt.window)
	fyne.Do(func() {
		progressDialog.Show()
	})

	var deleted, failed int
	for i, item := range items {
		// Update progress on main thread
		current := i + 1
		fyne.Do(func() {
			progressLabel.SetText(fmt.Sprintf("Deleting %d of %d items from Rescale...", current, total))
			progressBar.SetValue(float64(current))
		})

		var err error
		if item.IsFolder {
			err = apiClient.DeleteFolder(ctx, item.ID)
		} else {
			err = apiClient.DeleteFile(ctx, item.ID)
		}
		if err != nil {
			failed++
			fbt.logger.Error().Err(err).Str("id", item.ID).Str("name", item.Name).Msg("Delete failed")
		} else {
			deleted++
		}
	}

	// Close dialog and refresh
	fyne.Do(func() {
		progressDialog.Hide()
	})

	fbt.remoteBrowser.ClearCache()
	fbt.remoteBrowser.Refresh()
	fbt.remoteBrowser.ClearSelection()

	if failed > 0 {
		fbt.setStatus(fmt.Sprintf("Deleted %d item(s), %d failed", deleted, failed))
	} else {
		fbt.setStatus(fmt.Sprintf("Deleted %d item(s)", deleted))
	}
}

// executeUploadConcurrent uploads files concurrently using CLI patterns
func (fbt *FileBrowserTab) executeUploadConcurrent(items []FileItem, destFolderID string) {
	fbt.setTransferring(true)
	defer fbt.setTransferring(false)

	apiClient := fbt.engine.API()
	if apiClient == nil {
		fbt.showError("Not connected to Rescale")
		return
	}

	// Flatten folder contents to get all files
	allFiles := fbt.flattenForUpload(items)
	total := len(allFiles)

	// No overall timeout - per-part timeouts (10 min) handle stuck operations.
	// If parts are completing, the upload is healthy and should continue indefinitely.
	// This matches CLI behavior which also has no overall timeout.
	ctx, cancel := context.WithCancel(context.Background())
	fbt.mu.Lock()
	fbt.cancelFunc = cancel
	fbt.mu.Unlock()
	defer cancel()

	atomic.StoreInt32(&fbt.totalFiles, int32(total))
	atomic.StoreInt32(&fbt.completedFiles, 0)

	// Initialize progress UI and PRE-CREATE all progress bars on main thread
	// This prevents GUI locking from concurrent container modifications
	fbt.initProgressUIWithFiles(total, "upload", allFiles)

	// Create resource manager and transfer manager (like CLI)
	// Use default config with auto-scaling enabled
	resourceMgr := resources.NewManager(resources.Config{AutoScale: true})
	transferMgr := transfer.NewManager(resourceMgr)

	// Use semaphore for concurrent uploads (like CLI)
	semaphore := make(chan struct{}, constants.DefaultMaxConcurrent)
	var wg sync.WaitGroup
	errChan := make(chan error, total)

	successCount := int32(0)
	errorCount := int32(0)

	for _, file := range allFiles {
		wg.Add(1)
		go func(f uploadFileInfo) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Check for cancellation
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Get file info for transfer allocation
			fileInfo, err := os.Stat(f.LocalPath)
			if err != nil {
				fbt.updateFileProgress(f.LocalPath, 1.0, "error", err)
				atomic.AddInt32(&errorCount, 1)
				errChan <- fmt.Errorf("failed to stat %s: %w", f.LocalPath, err)
				atomic.AddInt32(&fbt.completedFiles, 1)
				fbt.updateOverallProgress()
				return
			}

			// Allocate transfer handle (for multi-threaded large file uploads)
			transferHandle := transferMgr.AllocateTransfer(fileInfo.Size(), total)

			// Show "preparing" status immediately so user sees feedback
			fbt.updateFileProgress(f.LocalPath, 0, "preparing", nil)

			// Upload with progress callback using new canonical API (Sprint 6)
			_, err = upload.UploadFile(ctx, upload.UploadParams{
				LocalPath: f.LocalPath,
				FolderID:  f.DestFolderID,
				APIClient: apiClient,
				ProgressCallback: func(progress float64) {
					fbt.updateFileProgress(f.LocalPath, progress, "transferring", nil)
					fbt.updateOverallProgress()
				},
				TransferHandle: transferHandle,
			})

			if err != nil {
				fbt.updateFileProgress(f.LocalPath, 1.0, "error", err)
				atomic.AddInt32(&errorCount, 1)
				errChan <- fmt.Errorf("failed to upload %s: %w", filepath.Base(f.LocalPath), err)
				fbt.logger.Error().Err(err).Str("path", f.LocalPath).Msg("Upload failed")
			} else {
				fbt.updateFileProgress(f.LocalPath, 1.0, "complete", nil)
				atomic.AddInt32(&successCount, 1)
				fbt.logger.Info().Str("path", f.LocalPath).Msg("File uploaded")
			}

			atomic.AddInt32(&fbt.completedFiles, 1)
			fbt.updateOverallProgress()
		}(file)
	}

	// Wait for all uploads
	wg.Wait()
	close(errChan)

	// Stop progress interpolation ticker
	fbt.stopProgressInterpolation()

	// Clear cache and refresh remote browser
	fbt.remoteBrowser.ClearCache()
	fbt.remoteBrowser.Refresh()

	// Clear local selection
	fbt.localBrowser.ClearSelection()

	// Show final status
	success := atomic.LoadInt32(&successCount)
	errors := atomic.LoadInt32(&errorCount)
	if errors > 0 {
		fbt.setStatus(fmt.Sprintf("Upload complete: %d succeeded, %d failed", success, errors))
	} else {
		fbt.setStatus(fmt.Sprintf("Upload complete: %d file(s) uploaded successfully", success))
	}
}

// uploadFileInfo contains info for a single file upload
type uploadFileInfo struct {
	LocalPath    string
	DestFolderID string
	Size         int64
}

// flattenForUpload expands folders into individual files for upload
func (fbt *FileBrowserTab) flattenForUpload(items []FileItem) []uploadFileInfo {
	var result []uploadFileInfo

	apiClient := fbt.engine.API()
	if apiClient == nil {
		return result
	}

	ctx, cancel := context.WithTimeout(context.Background(), constants.GUIOperationTimeout)
	defer cancel()

	currentRemoteFolderID := fbt.remoteBrowser.GetCurrentFolderID()

	for _, item := range items {
		if item.IsFolder {
			// Use CLI folder upload logic - REUSE existing well-tested code!
			files, err := fbt.uploadFolderWithCLILogic(ctx, apiClient, item.ID, currentRemoteFolderID)
			if err != nil {
				fbt.logger.Error().Err(err).Str("folder", item.Name).Msg("Failed to process folder")
				continue
			}
			result = append(result, files...)
		} else {
			info, err := os.Stat(item.ID)
			size := int64(0)
			if err == nil {
				size = info.Size()
			}
			result = append(result, uploadFileInfo{
				LocalPath:    item.ID,
				DestFolderID: currentRemoteFolderID,
				Size:         size,
			})
		}
	}

	return result
}

// uploadFolderWithCLILogic uses the CLI's folder upload logic to preserve directory structure
// This REUSES the battle-tested CLI code instead of reimplementing
func (fbt *FileBrowserTab) uploadFolderWithCLILogic(
	ctx context.Context,
	apiClient *api.Client,
	localFolderPath string,
	remoteParentID string,
) ([]uploadFileInfo, error) {
	// Import the CLI package to use its functions
	cache := cli.NewFolderCache()

	// Step 1: Scan local directory using CLI logic
	directories, files, symlinks, err := cli.BuildDirectoryTree(localFolderPath, false)
	if err != nil {
		return nil, fmt.Errorf("failed to scan directory: %w", err)
	}

	if len(symlinks) > 0 {
		fbt.logger.Info().Int("count", len(symlinks)).Msg("Skipping symbolic links")
	}

	// Step 2: Create or get root remote folder
	folderName := filepath.Base(localFolderPath)
	rootRemoteID, exists, err := cli.CheckFolderExists(ctx, apiClient, cache, remoteParentID, folderName)
	if err != nil {
		return nil, fmt.Errorf("failed to check root folder: %w", err)
	}

	if !exists {
		// Create root folder
		rootRemoteID, err = apiClient.CreateFolder(ctx, folderName, remoteParentID)
		if err != nil {
			return nil, fmt.Errorf("failed to create root folder: %w", err)
		}
		// Populate cache
		_, _ = cache.Get(ctx, apiClient, rootRemoteID)
	}

	// Step 3: Use CLI's CreateFolderStructure to create remote folder tree
	// This handles all the complexity: conflict resolution, caching, etc.
	conflictMode := cli.ConflictMergeAll // Auto-merge for GUI (no prompts)
	mapping, _, err := cli.CreateFolderStructure(
		ctx,
		apiClient,
		cache,
		localFolderPath,
		directories,
		rootRemoteID,
		&conflictMode,
		15, // maxConcurrent folders
		fbt.logger,
		nil, // folderReadyChan not needed for sequential mode
		nil, // progressWriter not needed
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create folder structure: %w", err)
	}

	// Step 4: Build upload file list using the mapping
	var uploadFiles []uploadFileInfo
	for _, filePath := range files {
		// Find the containing directory
		dirPath := filepath.Dir(filePath)
		remoteFolderID, ok := mapping[dirPath]
		if !ok {
			// File is in the root folder
			remoteFolderID = rootRemoteID
		}

		info, err := os.Stat(filePath)
		size := int64(0)
		if err == nil {
			size = info.Size()
		}

		uploadFiles = append(uploadFiles, uploadFileInfo{
			LocalPath:    filePath,
			DestFolderID: remoteFolderID,
			Size:         size,
		})
	}

	return uploadFiles, nil
}

// executeDownloadConcurrent downloads files concurrently using CLI patterns
func (fbt *FileBrowserTab) executeDownloadConcurrent(items []FileItem, destPath string) {
	fbt.setTransferring(true)
	defer fbt.setTransferring(false)

	apiClient := fbt.engine.API()
	if apiClient == nil {
		fbt.showError("Not connected to Rescale")
		return
	}

	// Use a short-lived context for flattening (API operations only)
	flattenCtx, flattenCancel := context.WithTimeout(context.Background(), constants.GUIOperationTimeout)
	allFiles := fbt.flattenForDownload(flattenCtx, items, destPath, apiClient)
	flattenCancel()
	total := len(allFiles)

	if total == 0 {
		fbt.setStatus("No files to download")
		return
	}

	// No overall timeout - per-part timeouts handle stuck operations.
	// This matches CLI behavior.
	ctx, cancel := context.WithCancel(context.Background())
	fbt.mu.Lock()
	fbt.cancelFunc = cancel
	fbt.mu.Unlock()
	defer cancel()

	atomic.StoreInt32(&fbt.totalFiles, int32(total))
	atomic.StoreInt32(&fbt.completedFiles, 0)

	// Initialize progress UI and PRE-CREATE all progress bars on main thread
	// This prevents GUI locking from concurrent container modifications
	fbt.initProgressUIForDownloads(total, allFiles)

	// Create resource manager and transfer manager (like CLI)
	// Use default config with auto-scaling enabled
	resourceMgr := resources.NewManager(resources.Config{AutoScale: true})
	transferMgr := transfer.NewManager(resourceMgr)

	// Use semaphore for concurrent downloads (like CLI)
	semaphore := make(chan struct{}, constants.DefaultMaxConcurrent)
	var wg sync.WaitGroup
	errChan := make(chan error, total)

	successCount := int32(0)
	errorCount := int32(0)

	for _, file := range allFiles {
		wg.Add(1)
		go func(f downloadFileInfo) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Check for cancellation
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Allocate transfer handle (for multi-threaded large file downloads)
			transferHandle := transferMgr.AllocateTransfer(f.Size, total)

			// Download with progress callback using new canonical API (Sprint 6)
			err := download.DownloadFile(ctx, download.DownloadParams{
				FileID:    f.FileID,
				LocalPath: f.LocalPath,
				APIClient: apiClient,
				ProgressCallback: func(progress float64) {
					fbt.updateFileProgress(f.FileID, progress, "transferring", nil)
					fbt.updateOverallProgress()
				},
				TransferHandle: transferHandle,
			})

			if err != nil {
				fbt.updateFileProgress(f.FileID, 1.0, "error", err)
				atomic.AddInt32(&errorCount, 1)
				errChan <- fmt.Errorf("failed to download %s: %w", f.Name, err)
				fbt.logger.Error().Err(err).Str("file_id", f.FileID).Str("name", f.Name).Msg("Download failed")
			} else {
				fbt.updateFileProgress(f.FileID, 1.0, "complete", nil)
				atomic.AddInt32(&successCount, 1)
				fbt.logger.Info().Str("file_id", f.FileID).Str("local_path", f.LocalPath).Msg("File downloaded")
			}

			atomic.AddInt32(&fbt.completedFiles, 1)
			fbt.updateOverallProgress()
		}(file)
	}

	// Wait for all downloads
	wg.Wait()
	close(errChan)

	// Stop progress interpolation ticker
	fbt.stopProgressInterpolation()

	// Refresh local browser
	fbt.localBrowser.Refresh()

	// Clear remote selection
	fbt.remoteBrowser.ClearSelection()

	// Show final status
	success := atomic.LoadInt32(&successCount)
	errors := atomic.LoadInt32(&errorCount)
	if errors > 0 {
		fbt.setStatus(fmt.Sprintf("Download complete: %d succeeded, %d failed", success, errors))
	} else {
		fbt.setStatus(fmt.Sprintf("Download complete: %d file(s) downloaded successfully", success))
	}
}

// downloadFileInfo contains info for a single file download
type downloadFileInfo struct {
	FileID    string
	Name      string
	LocalPath string
	Size      int64
}

// flattenForDownload expands folders into individual files for download
// Uses CLI's scan logic for folders to leverage battle-tested code
func (fbt *FileBrowserTab) flattenForDownload(ctx context.Context, items []FileItem, destPath string, apiClient *api.Client) []downloadFileInfo {
	var result []downloadFileInfo

	for _, item := range items {
		if item.IsFolder {
			// Use CLI logic for folders - benefits: path validation, efficient scanning
			files, err := fbt.downloadFolderWithCLILogic(ctx, apiClient, item.ID, destPath, item.Name)
			if err != nil {
				fbt.logger.Error().Err(err).Str("folder", item.Name).Msg("Failed to process folder for download")
				continue
			}
			result = append(result, files...)
		} else {
			result = append(result, downloadFileInfo{
				FileID:    item.ID,
				Name:      item.Name,
				LocalPath: filepath.Join(destPath, item.Name),
				Size:      item.Size,
			})
		}
	}

	return result
}

// downloadFolderWithCLILogic uses CLI's recursive folder scan with GUI-appropriate defaults
// This REUSES the battle-tested CLI code instead of reimplementing folder traversal
func (fbt *FileBrowserTab) downloadFolderWithCLILogic(
	ctx context.Context,
	apiClient *api.Client,
	remoteFolderID string,
	localParentPath string,
	folderName string,
) ([]downloadFileInfo, error) {
	localFolderPath := filepath.Join(localParentPath, folderName)

	// Use CLI's scan function to get complete structure
	allFolders, allFiles, err := cli.ScanRemoteFolderRecursive(ctx, apiClient, remoteFolderID, "")
	if err != nil {
		return nil, fmt.Errorf("failed to scan remote folder: %w", err)
	}

	// Create root local folder
	if err := os.MkdirAll(localFolderPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create root folder: %w", err)
	}

	// Create local directories for all subfolders
	for _, folder := range allFolders {
		dirPath := filepath.Join(localFolderPath, folder.RelativePath)
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create folder %s: %w", folder.RelativePath, err)
		}
	}

	// Build download file list from CLI's scan results
	var files []downloadFileInfo
	for _, f := range allFiles {
		files = append(files, downloadFileInfo{
			FileID:    f.FileID,
			Name:      f.Name,
			LocalPath: filepath.Join(localFolderPath, f.RelativePath),
			Size:      f.Size,
		})
	}

	fbt.logger.Info().
		Int("folders", len(allFolders)).
		Int("files", len(allFiles)).
		Str("local_path", localFolderPath).
		Msg("Folder structure scanned via CLI code")

	return files, nil
}

// Progress UI methods

// Progress scroll area height constants for dynamic sizing
const (
	progressRowHeight    float32 = 30  // Approximate row height (label + progress bar)
	maxScrollAreaHeight  float32 = 250 // Max scroll area height
	minScrollAreaHeight  float32 = 50  // Min scroll area height
	progressLabelWidth   float32 = 280 // Fixed width for per-file progress labels
	maxFilenameLen       int     = 25  // Max filename length before truncation
)

// truncateName truncates a filename to maxFilenameLen chars with ellipsis if needed
func truncateName(name string) string {
	if len(name) <= maxFilenameLen {
		return name
	}
	return name[:maxFilenameLen-3] + "..."
}

// initProgressUIWithFiles initializes progress UI and pre-creates all progress bars for uploads.
// Uses compact progress bars with fixed-width labels, dynamic scroll height, and progress interpolation.
func (fbt *FileBrowserTab) initProgressUIWithFiles(totalFiles int, _ string, files []uploadFileInfo) {
	// Initialize data structures (not UI operations)
	fbt.mu.Lock()
	fbt.fileProgressBars = make(map[string]*widget.ProgressBar)
	fbt.fileProgressLabels = make(map[string]*widget.Label)
	fbt.activeTransfers = make(map[string]*FileTransferProgress)
	fbt.mu.Unlock()

	// Pre-create all widgets (creating widgets is safe off main thread)
	type fileWidgets struct {
		bar   *widget.ProgressBar
		label *widget.Label
		row   *fyne.Container
	}
	widgetList := make([]fileWidgets, 0, len(files))

	for _, f := range files {
		bar := widget.NewProgressBar()
		bar.SetValue(0)

		name := filepath.Base(f.LocalPath)
		displayName := truncateName(name) // Truncate long filenames
		sizeStr := FormatFileSize(f.Size)
		labelText := fmt.Sprintf("↑ %s (%s)", displayName, sizeStr)
		label := widget.NewLabel(labelText)

		fbt.mu.Lock()
		fbt.fileProgressBars[f.LocalPath] = bar
		fbt.fileProgressLabels[f.LocalPath] = label
		fbt.activeTransfers[f.LocalPath] = &FileTransferProgress{
			Name:         name, // Store full name for internal use
			Size:         f.Size,
			Progress:     0,
			Interpolated: 0,
			Status:       "pending",
			StartTime:    time.Now(),
			LastUpdate:   time.Now(),
			LastBytes:    0,
			BytesPerSec:  0,
		}
		fbt.mu.Unlock()

		// Use fixed-width container for label to prevent pushing progress bar off screen
		labelContainer := container.NewGridWrap(fyne.NewSize(progressLabelWidth, 20), label)
		row := container.NewBorder(nil, nil, labelContainer, nil, bar)
		widgetList = append(widgetList, fileWidgets{bar: bar, label: label, row: row})
	}

	// Calculate dynamic scroll area height based on file count
	scrollHeight := float32(len(files)) * progressRowHeight
	if scrollHeight < minScrollAreaHeight {
		scrollHeight = minScrollAreaHeight
	}
	if scrollHeight > maxScrollAreaHeight {
		scrollHeight = maxScrollAreaHeight
	}

	// All UI modifications must be on main thread via fyne.Do()
	fyne.Do(func() {
		// Clear existing file progress items
		fbt.fileProgressList.Objects = nil

		// Update overall label
		fbt.overallProgressLabel.SetText(fmt.Sprintf("Uploading 0/%d files...", totalFiles))
		fbt.overallProgressBar.SetValue(0)

		// Add all pre-created widgets to the container
		for _, w := range widgetList {
			fbt.fileProgressList.Add(w.row)
		}

		// Set dynamic scroll area height
		if fbt.fileProgressScroll != nil {
			fbt.fileProgressScroll.SetMinSize(fyne.NewSize(0, scrollHeight))
		}

		fbt.progressPanel.Show()
		fbt.fileProgressList.Refresh()
	})

	// Start progress interpolation ticker for smooth updates
	fbt.startProgressInterpolation()
}

// initProgressUIForDownloads initializes progress UI and pre-creates all progress bars for downloads.
// Uses compact progress bars with fixed-width labels, dynamic scroll height, and progress interpolation.
func (fbt *FileBrowserTab) initProgressUIForDownloads(totalFiles int, files []downloadFileInfo) {
	// Initialize data structures (not UI operations)
	fbt.mu.Lock()
	fbt.fileProgressBars = make(map[string]*widget.ProgressBar)
	fbt.fileProgressLabels = make(map[string]*widget.Label)
	fbt.activeTransfers = make(map[string]*FileTransferProgress)
	fbt.mu.Unlock()

	// Pre-create all widgets (creating widgets is safe off main thread)
	type fileWidgets struct {
		bar   *widget.ProgressBar
		label *widget.Label
		row   *fyne.Container
	}
	widgetList := make([]fileWidgets, 0, len(files))

	for _, f := range files {
		bar := widget.NewProgressBar()
		bar.SetValue(0)

		displayName := truncateName(f.Name) // Truncate long filenames
		sizeStr := FormatFileSize(f.Size)
		labelText := fmt.Sprintf("↓ %s (%s)", displayName, sizeStr)
		label := widget.NewLabel(labelText)

		fbt.mu.Lock()
		fbt.fileProgressBars[f.FileID] = bar
		fbt.fileProgressLabels[f.FileID] = label
		fbt.activeTransfers[f.FileID] = &FileTransferProgress{
			Name:         f.Name, // Store full name for internal use
			Size:         f.Size,
			Progress:     0,
			Interpolated: 0,
			Status:       "pending",
			StartTime:    time.Now(),
			LastUpdate:   time.Now(),
			LastBytes:    0,
			BytesPerSec:  0,
		}
		fbt.mu.Unlock()

		// Use fixed-width container for label to prevent pushing progress bar off screen
		labelContainer := container.NewGridWrap(fyne.NewSize(progressLabelWidth, 20), label)
		row := container.NewBorder(nil, nil, labelContainer, nil, bar)
		widgetList = append(widgetList, fileWidgets{bar: bar, label: label, row: row})
	}

	// Calculate dynamic scroll area height based on file count
	scrollHeight := float32(len(files)) * progressRowHeight
	if scrollHeight < minScrollAreaHeight {
		scrollHeight = minScrollAreaHeight
	}
	if scrollHeight > maxScrollAreaHeight {
		scrollHeight = maxScrollAreaHeight
	}

	// All UI modifications must be on main thread via fyne.Do()
	fyne.Do(func() {
		// Clear existing file progress items
		fbt.fileProgressList.Objects = nil

		// Update overall label
		fbt.overallProgressLabel.SetText(fmt.Sprintf("Downloading 0/%d files...", totalFiles))
		fbt.overallProgressBar.SetValue(0)

		// Add all pre-created widgets to the list
		for _, w := range widgetList {
			fbt.fileProgressList.Add(w.row)
		}

		// Set dynamic scroll area height
		if fbt.fileProgressScroll != nil {
			fbt.fileProgressScroll.SetMinSize(fyne.NewSize(0, scrollHeight))
		}

		fbt.progressPanel.Show()
		fbt.fileProgressList.Refresh()
	})

	// Start progress interpolation ticker for smooth updates
	fbt.startProgressInterpolation()
}

// updateFileProgress updates the progress for a specific file.
// Calculates BytesPerSec for progress interpolation and uses truncated filenames for display.
// Must use fyne.Do() for all widget updates since this is called from goroutines.
func (fbt *FileBrowserTab) updateFileProgress(id string, progress float64, status string, err error) {
	// Read references with lock
	fbt.mu.RLock()
	bar := fbt.fileProgressBars[id]
	label := fbt.fileProgressLabels[id]
	transfer := fbt.activeTransfers[id]
	fbt.mu.RUnlock()

	// Compute label text outside fyne.Do() to minimize time on main thread
	var labelText string

	if label != nil && transfer != nil {
		now := time.Now()

		// Update transfer state (brief write lock)
		fbt.mu.Lock()
		// Reset start time on first actual progress (when transfer begins)
		if transfer.Progress == 0 && progress > 0 {
			transfer.StartTime = now
			transfer.LastUpdate = now
			transfer.BytesPerSec = 0
		}

		// Calculate instantaneous transfer rate for interpolation
		// Use progress delta since last update for more accurate rate
		// Only calculate if we have a previous progress point (skip first callback)
		if transfer.Progress > 0 && progress > transfer.Progress && transfer.Size > 0 {
			elapsed := now.Sub(transfer.LastUpdate).Seconds()
			if elapsed > 0.1 { // Need at least 100ms between updates
				bytesDelta := float64(transfer.Size) * (progress - transfer.Progress)
				newRate := bytesDelta / elapsed
				// v3.2.2: Smooth the rate with exponential moving average
				// Using alpha=0.25 gives 25% weight to new value, 75% to previous
				// This provides much smoother speed display while remaining responsive
				const speedSmoothingAlpha = 0.25
				if transfer.BytesPerSec > 0 {
					transfer.BytesPerSec = speedSmoothingAlpha*newRate + (1-speedSmoothingAlpha)*transfer.BytesPerSec
				} else {
					transfer.BytesPerSec = newRate
				}
			}
		}

		transfer.Progress = progress
		transfer.Interpolated = progress // Reset interpolated to real progress
		transfer.Status = status
		transfer.Error = err
		name := truncateName(transfer.Name) // Truncate for display
		size := transfer.Size
		startTime := transfer.StartTime
		transfer.LastUpdate = now
		fbt.mu.Unlock()

		sizeStr := FormatFileSize(size)

		switch status {
		case "complete":
			// Show final transfer rate
			elapsed := now.Sub(startTime).Seconds()
			if elapsed > 0 && size > 0 {
				rate := float64(size) / elapsed
				rateStr := FormatTransferRate(rate)
				labelText = fmt.Sprintf("✓ %s (%s) @ %s", name, sizeStr, rateStr)
			} else {
				labelText = fmt.Sprintf("✓ %s (%s)", name, sizeStr)
			}
		case "preparing":
			labelText = fmt.Sprintf("⏳ %s (%s) Preparing...", name, sizeStr)
		case "error":
			errMsg := "failed"
			if err != nil {
				errMsg = err.Error()
				if len(errMsg) > 30 {
					errMsg = errMsg[:30] + "..."
				}
			}
			labelText = fmt.Sprintf("✗ %s: %s", name, errMsg)
		default:
			// v3.2.2: Always show 2 decimal places for consistent percentage display
			pct := progress * 100
			// Calculate transfer rate
			elapsed := now.Sub(startTime).Seconds()
			bytesTransferred := int64(float64(size) * progress)
			rateStr := ""
			if elapsed > 0.5 && bytesTransferred > 0 { // Wait 0.5s before showing rate
				rate := float64(bytesTransferred) / elapsed
				rateStr = fmt.Sprintf(" @ %s", FormatTransferRate(rate))
			}
			labelText = fmt.Sprintf("⟳ %s (%s) %.2f%%%s", name, sizeStr, pct, rateStr)
		}
	}

	// All widget updates must be on main thread (Fyne 2.5+ requirement)
	fyne.Do(func() {
		if bar != nil {
			bar.SetValue(progress)
		}
		if label != nil && labelText != "" {
			label.SetText(labelText)
		}
	})
}

// startProgressInterpolation starts a goroutine that interpolates progress
// between real callbacks for smoother UI updates (2-3 times per second)
func (fbt *FileBrowserTab) startProgressInterpolation() {
	// Stop any existing interpolation ticker
	fbt.stopProgressInterpolation()

	fbt.mu.Lock()
	fbt.stopInterpolate = make(chan struct{})
	stopCh := fbt.stopInterpolate
	fbt.mu.Unlock()

	go func() {
		ticker := time.NewTicker(250 * time.Millisecond) // 4 updates per second for smoother UI
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				fbt.interpolateProgress()
			}
		}
	}()
}

// stopProgressInterpolation stops the interpolation ticker
func (fbt *FileBrowserTab) stopProgressInterpolation() {
	fbt.mu.Lock()
	if fbt.stopInterpolate != nil {
		close(fbt.stopInterpolate)
		fbt.stopInterpolate = nil
	}
	fbt.mu.Unlock()
}

// interpolateProgress estimates and updates progress bars between real callbacks
func (fbt *FileBrowserTab) interpolateProgress() {
	now := time.Now()

	// Collect updates to make (minimize lock time)
	type barUpdate struct {
		bar         *widget.ProgressBar
		label       *widget.Label
		progress    float64
		labelText   string
	}
	var updates []barUpdate

	fbt.mu.Lock()
	for id, transfer := range fbt.activeTransfers {
		// Only interpolate for active transfers
		if transfer.Status != "transferring" || transfer.BytesPerSec <= 0 {
			continue
		}

		// Calculate estimated progress since last real update
		elapsed := now.Sub(transfer.LastUpdate).Seconds()
		if elapsed < 0.01 || elapsed > 30 { // Skip if too recent (10ms) or too stale
			continue
		}

		// Estimate new progress
		bytesAdded := transfer.BytesPerSec * elapsed
		progressDelta := bytesAdded / float64(transfer.Size)
		newProgress := transfer.Progress + progressDelta

		// Cap at 99.5% to leave room for real progress (never show 100% until complete)
		if newProgress > 0.995 {
			newProgress = 0.995
		}

		// Only update if it's an increase
		if newProgress > transfer.Interpolated {
			transfer.Interpolated = newProgress

			// Get widget references
			bar := fbt.fileProgressBars[id]
			label := fbt.fileProgressLabels[id]

			if bar != nil {
				// Build label text with interpolated percentage (1 decimal for smoother visual feedback)
				pct := newProgress * 100
				sizeStr := FormatFileSize(transfer.Size)
				rateStr := FormatTransferRate(transfer.BytesPerSec)
				displayName := truncateName(transfer.Name) // Truncate for display
				labelText := fmt.Sprintf("⟳ %s (%s) %.1f%% @ %s", displayName, sizeStr, pct, rateStr)

				updates = append(updates, barUpdate{
					bar:       bar,
					label:     label,
					progress:  newProgress,
					labelText: labelText,
				})
			}
		}
	}
	fbt.mu.Unlock()

	// Apply UI updates on main thread
	if len(updates) > 0 {
		fyne.Do(func() {
			for _, u := range updates {
				u.bar.SetValue(u.progress)
				u.bar.Refresh() // Force redraw
				if u.label != nil {
					u.label.SetText(u.labelText)
				}
			}
		})
	}
}

// updateOverallProgress updates the overall progress bar
// Must use fyne.Do() for all widget updates since this is called from goroutines
func (fbt *FileBrowserTab) updateOverallProgress() {
	completed := atomic.LoadInt32(&fbt.completedFiles)
	total := atomic.LoadInt32(&fbt.totalFiles)

	if total > 0 {
		progress := float64(completed) / float64(total)

		// All widget updates must be on main thread (Fyne 2.5+ requirement)
		fyne.Do(func() {
			fbt.overallProgressBar.SetValue(progress)

			// Detect direction from label text
			direction := "Transferring"
			if fbt.overallProgressLabel != nil {
				text := fbt.overallProgressLabel.Text
				if len(text) > 0 {
					if text[0] == 'U' {
						direction = "Uploading"
					} else if text[0] == 'D' {
						direction = "Downloading"
					}
				}
				fbt.overallProgressLabel.SetText(fmt.Sprintf("%s %d/%d files...", direction, completed, total))
			}
		})
	}
}

// cancelTransfer cancels the current transfer operation
func (fbt *FileBrowserTab) cancelTransfer() {
	fbt.mu.Lock()
	if fbt.cancelFunc != nil {
		fbt.cancelFunc()
	}
	fbt.mu.Unlock()
}

// setTransferring sets the transfer state
func (fbt *FileBrowserTab) setTransferring(transferring bool) {
	fbt.mu.Lock()
	fbt.isTransferring = transferring
	completed := atomic.LoadInt32(&fbt.completedFiles)
	total := atomic.LoadInt32(&fbt.totalFiles)
	fbt.mu.Unlock()

	fyne.Do(func() {
		if transferring {
			fbt.progressPanel.Show()
			fbt.cancelBtn.Show()
			fbt.clearBtn.Hide()
		} else {
			// Transfer finished - show completion message
			fbt.cancelBtn.Hide()
			fbt.clearBtn.Show()

			// Update overall progress label with completion message
			if total > 0 {
				fbt.overallProgressLabel.SetText(fmt.Sprintf("✓ Transfer complete: %d/%d files", completed, total))
				fbt.overallProgressBar.SetValue(1.0)
			}
		}
		// Update transfer buttons within fyne.Do to ensure thread safety
		fbt.updateTransferButtonsInternal()
	})
}

// setStatus sets the status message (defaults to info level)
func (fbt *FileBrowserTab) setStatus(status string) {
	if fbt.statusBar != nil {
		fbt.statusBar.SetInfo(status)
	}
}

// showError shows an error dialog
func (fbt *FileBrowserTab) showError(message string) {
	dialog.ShowError(fmt.Errorf("%s", message), fbt.window)
}
