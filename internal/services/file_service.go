// Package services provides frontend-agnostic business logic for Rescale Interlink.
package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/transfer/folder"
	"github.com/rescale/rescale-int/internal/transfer/scan"
	"github.com/rescale/rescale-int/internal/util/paths"
	"github.com/rescale/rescale-int/internal/validation"
)

// FileService handles file and folder operations.
// It is frontend-agnostic: no Fyne imports, no framework-specific threading.
type FileService struct {
	apiClient *api.Client
	eventBus  *events.EventBus
	logger    *logging.Logger

	mu sync.RWMutex
}

func NewFileService(apiClient *api.Client, eventBus *events.EventBus) *FileService {
	return &FileService{
		apiClient: apiClient,
		eventBus:  eventBus,
		logger:    logging.NewLogger("file-service", nil),
	}
}

// SetAPIClient updates the API client (e.g., after credential change).
func (fs *FileService) SetAPIClient(client *api.Client) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.apiClient = client
}

// ListFolder returns the contents of a remote folder.
// If folderID is empty, returns the root folders.
func (fs *FileService) ListFolder(ctx context.Context, folderID string) (*FolderContents, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return nil, fmt.Errorf("API client not configured")
	}

	if folderID == "" {
		// List root folders
		return fs.listRootFolders(ctx, apiClient)
	}

	// List folder contents
	return fs.listFolderContents(ctx, apiClient, folderID)
}

// listRootFolders lists the root folders (My Library).
// First gets the MyLibrary folder ID, then lists its contents.
func (fs *FileService) listRootFolders(ctx context.Context, apiClient *api.Client) (*FolderContents, error) {
	// Get root folder IDs
	roots, err := apiClient.GetRootFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get root folders: %w", err)
	}

	// List contents of My Library root
	return fs.listFolderContents(ctx, apiClient, roots.MyLibrary)
}

// listFolderContents lists the contents of a specific folder.
func (fs *FileService) listFolderContents(ctx context.Context, apiClient *api.Client, folderID string) (*FolderContents, error) {
	contents, err := apiClient.ListFolderContents(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("failed to list folder contents: %w", err)
	}

	items := make([]FileItem, 0, len(contents.Folders)+len(contents.Files))

	// Add folders first
	for _, f := range contents.Folders {
		items = append(items, FileItem{
			ID:       f.ID,
			Name:     f.Name,
			IsFolder: true,
			ModTime:  f.DateUploaded, // FolderInfo uses DateUploaded
		})
	}

	// Add files
	for _, f := range contents.Files {
		items = append(items, FileItem{
			ID:       f.ID,
			Name:     f.Name,
			IsFolder: false,
			Size:     f.DecryptedSize,
			ModTime:  f.DateUploaded,
		})
	}

	return &FolderContents{
		FolderID:   folderID,
		Items:      items,
		HasMore:    contents.HasMore,
		NextCursor: contents.NextURL,
	}, nil
}

// ListLegacyFiles returns a flat list of all files (legacy mode).
// Pass pageSize=0 for API default.
func (fs *FileService) ListLegacyFiles(ctx context.Context, cursor string, pageSize int) (*FolderContents, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return nil, fmt.Errorf("API client not configured")
	}

	page, err := apiClient.ListFilesPage(ctx, cursor, pageSize)
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}

	items := make([]FileItem, 0, len(page.Files))
	for _, f := range page.Files {
		items = append(items, FileItem{
			ID:       f.ID,
			Name:     f.Name,
			IsFolder: false,
			Size:     f.DecryptedSize,
			ModTime:  f.DateUploaded,
		})
	}

	return &FolderContents{
		FolderID:   "",
		FolderPath: "Legacy Files",
		Items:      items,
		HasMore:    page.HasMore,
		NextCursor: page.NextURL,
	}, nil
}

func (fs *FileService) CreateFolder(ctx context.Context, name string, parentID string) (string, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return "", fmt.Errorf("API client not configured")
	}

	folderID, err := apiClient.CreateFolder(ctx, name, parentID)
	if err != nil {
		return "", fmt.Errorf("failed to create folder: %w", err)
	}

	return folderID, nil
}

func (fs *FileService) DeleteFile(ctx context.Context, fileID string) error {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return fmt.Errorf("API client not configured")
	}

	if err := apiClient.DeleteFile(ctx, fileID); err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	return nil
}

func (fs *FileService) DeleteFolder(ctx context.Context, folderID string) error {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return fmt.Errorf("API client not configured")
	}

	if err := apiClient.DeleteFolder(ctx, folderID); err != nil {
		return fmt.Errorf("failed to delete folder: %w", err)
	}

	return nil
}

// DeleteItems deletes multiple files and/or folders.
// Returns the count of successfully deleted items.
func (fs *FileService) DeleteItems(ctx context.Context, items []FileItem) (deleted int, failed int, err error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return 0, len(items), fmt.Errorf("API client not configured")
	}

	for _, item := range items {
		var itemErr error
		if item.IsFolder {
			itemErr = apiClient.DeleteFolder(ctx, item.ID)
		} else {
			itemErr = apiClient.DeleteFile(ctx, item.ID)
		}

		if itemErr != nil {
			fs.logger.Error().Err(itemErr).Str("id", item.ID).Str("name", item.Name).Msg("Delete failed")
			failed++
		} else {
			deleted++
		}
	}

	return deleted, failed, nil
}

// PrepareUploadFolder creates the remote folder structure for a local folder.
// Returns the mapping of local directories to remote folder IDs and the list of files to upload.
func (fs *FileService) PrepareUploadFolder(ctx context.Context, localPath string, remoteFolderID string) (*UploadFolderResult, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return nil, fmt.Errorf("API client not configured")
	}

	// Use folder package directly (service layer shouldn't import cli)
	cache := folder.NewFolderCache()

	// Step 1: Scan local directory
	directories, files, symlinks, err := folder.BuildDirectoryTree(localPath, false)
	if err != nil {
		return nil, fmt.Errorf("failed to scan directory: %w", err)
	}

	if len(symlinks) > 0 {
		fs.logger.Info().Int("count", len(symlinks)).Msg("Skipping symbolic links")
	}

	// Step 2: Create or get root remote folder
	folderName := filepath.Base(localPath)
	rootRemoteID, exists, err := folder.CheckFolderExists(ctx, apiClient, cache, remoteFolderID, folderName)
	if err != nil {
		return nil, fmt.Errorf("failed to check root folder: %w", err)
	}

	if !exists {
		rootRemoteID, err = apiClient.CreateFolder(ctx, folderName, remoteFolderID)
		if err != nil {
			return nil, fmt.Errorf("failed to create root folder: %w", err)
		}
		// Populate cache
		_, _ = cache.Get(ctx, apiClient, rootRemoteID)
	}

	// Step 3: Create remote folder structure
	conflictMode := folder.ConflictMergeAll // Auto-merge for service (no prompts)
	mapping, _, err := folder.CreateFolderStructure(
		ctx,
		apiClient,
		cache,
		localPath,
		directories,
		rootRemoteID,
		&conflictMode,
		constants.DefaultFolderConcurrency,
		fs.logger,
		nil, // folderReadyChan not needed
		nil, // progressWriter not needed
		nil, // conflictPrompt not needed (auto-merge)
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create folder structure: %w", err)
	}

	// Step 4: Build file list
	uploadFiles := make([]UploadFileSpec, 0, len(files))
	for _, filePath := range files {
		dirPath := filepath.Dir(filePath)
		destFolderID, ok := mapping[dirPath]
		if !ok {
			destFolderID = rootRemoteID
		}

		info, err := os.Stat(filePath)
		size := int64(0)
		if err == nil {
			size = info.Size()
		}

		uploadFiles = append(uploadFiles, UploadFileSpec{
			LocalPath:    filePath,
			DestFolderID: destFolderID,
			Size:         size,
		})
	}

	return &UploadFolderResult{
		LocalToRemoteMapping: mapping,
		RootFolderID:         rootRemoteID,
		FilesToUpload:        uploadFiles,
	}, nil
}

// PrepareDownloadFolder scans a remote folder and prepares download specs.
// Creates local directories and returns the list of files to download.
func (fs *FileService) PrepareDownloadFolder(ctx context.Context, remoteFolderID string, localPath string, folderName string) ([]DownloadFileSpec, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return nil, fmt.Errorf("API client not configured")
	}

	rootFolderName := folderName
	if rootFolderName == "" {
		rootFolderName = remoteFolderID
	}
	if err := validation.ValidateFilename(rootFolderName); err != nil {
		return nil, fmt.Errorf("invalid folder name %q: %w", rootFolderName, err)
	}
	localFolderPath := filepath.Join(localPath, rootFolderName)

	allFolders, allFiles, err := scan.ScanRemoteFolderRecursive(ctx, apiClient, remoteFolderID, "")
	if err != nil {
		return nil, fmt.Errorf("failed to scan remote folder: %w", err)
	}

	// Create root local folder
	if err := os.MkdirAll(localFolderPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create root folder: %w", err)
	}

	// Create local directories for all subfolders
	for _, folder := range allFolders {
		dirPath, err := validation.ResolvePathInDirectory(folder.RelativePath, localFolderPath)
		if err != nil {
			return nil, fmt.Errorf("invalid folder path %q: %w", folder.RelativePath, err)
		}
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create folder %s: %w", folder.RelativePath, err)
		}
	}

	// Build download file list
	downloadFiles := make([]DownloadFileSpec, 0, len(allFiles))
	for _, f := range allFiles {
		localFilePath, err := validation.ResolvePathInDirectory(f.RelativePath, localFolderPath)
		if err != nil {
			return nil, fmt.Errorf("invalid file path %q: %w", f.RelativePath, err)
		}
		downloadFiles = append(downloadFiles, DownloadFileSpec{
			FileID:    f.FileID,
			Name:      f.Name,
			LocalPath: localFilePath,
			Size:      f.Size,
		})
	}

	fs.logger.Info().
		Int("folders", len(allFolders)).
		Int("files", len(allFiles)).
		Str("local_path", localFolderPath).
		Msg("Folder structure prepared for download")

	return downloadFiles, nil
}

// ResolveDownloadCollisions detects and resolves filename collisions.
// Uses the shared paths utility for consistent behavior with CLI.
func (fs *FileService) ResolveDownloadCollisions(files []DownloadFileSpec) ([]DownloadFileSpec, int) {
	if len(files) == 0 {
		return files, 0
	}

	// Convert to paths.FileForDownload
	pathFiles := make([]paths.FileForDownload, len(files))
	for i, f := range files {
		pathFiles[i] = paths.FileForDownload{
			FileID:    f.FileID,
			Name:      f.Name,
			LocalPath: f.LocalPath,
			Size:      f.Size,
		}
	}

	// Resolve collisions
	pathFiles, collisionCount := paths.ResolveCollisions(pathFiles)

	// Convert back
	result := make([]DownloadFileSpec, len(pathFiles))
	for i, pf := range pathFiles {
		result[i] = DownloadFileSpec{
			FileID:    pf.FileID,
			Name:      pf.Name,
			LocalPath: pf.LocalPath,
			Size:      pf.Size,
		}
	}

	return result, collisionCount
}

// GetMyLibraryFolderID returns the MyLibrary root folder ID.
func (fs *FileService) GetMyLibraryFolderID(ctx context.Context) (string, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return "", fmt.Errorf("API client not configured")
	}

	roots, err := apiClient.GetRootFolders(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get root folders: %w", err)
	}

	return roots.MyLibrary, nil
}

// GetMyJobsFolderID returns the MyJobs root folder ID.
func (fs *FileService) GetMyJobsFolderID(ctx context.Context) (string, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return "", fmt.Errorf("API client not configured")
	}

	roots, err := apiClient.GetRootFolders(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get root folders: %w", err)
	}

	return roots.MyJobs, nil
}

// ListTrashBinPage returns a single page of trash-bin contents.
// Files in the result have SymlinkID populated (needed for recover/purge).
// Folder-like items use FileItem.ID (which is the folder id).
func (fs *FileService) ListTrashBinPage(ctx context.Context, cursor string, pageSize int) (*FolderContents, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return nil, fmt.Errorf("API client not configured")
	}

	contents, err := apiClient.ListTrashBinPage(ctx, cursor, pageSize)
	if err != nil {
		return nil, fmt.Errorf("failed to list trash: %w", err)
	}

	items := make([]FileItem, 0, len(contents.Folders)+len(contents.Files))

	for _, f := range contents.Folders {
		items = append(items, FileItem{
			ID:       f.ID,
			Name:     f.Name,
			IsFolder: true,
			ModTime:  f.DateUploaded,
		})
	}

	for _, f := range contents.Files {
		items = append(items, FileItem{
			ID:        f.ID,
			Name:      f.Name,
			IsFolder:  false,
			Size:      f.DecryptedSize,
			ModTime:   f.DateUploaded,
			SymlinkID: f.SymlinkID,
		})
	}

	return &FolderContents{
		FolderID:   "trash",
		FolderPath: "Trash",
		Items:      items,
		HasMore:    contents.HasMore,
		NextCursor: contents.NextURL,
	}, nil
}

// RecoverTrashItems restores a mix of trashed files and folders to their
// original locations via a single bulk POST. The endpoint is all-or-nothing.
func (fs *FileService) RecoverTrashItems(ctx context.Context, items []FileItem) (recovered int, failed int, err error) {
	return fs.postTrashAction(ctx, "recover", items)
}

// PurgeTrashItems permanently deletes a mix of trashed files and folders via
// a single bulk POST. This is irreversible. The endpoint is all-or-nothing.
func (fs *FileService) PurgeTrashItems(ctx context.Context, items []FileItem) (deleted int, failed int, err error) {
	return fs.postTrashAction(ctx, "delete", items)
}

func (fs *FileService) postTrashAction(ctx context.Context, action string, items []FileItem) (int, int, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return 0, len(items), fmt.Errorf("API client not configured")
	}
	if len(items) == 0 {
		return 0, 0, nil
	}

	fileSymlinkIDs := make([]string, 0, len(items))
	folderIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item.IsFolder {
			folderIDs = append(folderIDs, item.ID)
		} else {
			if item.SymlinkID == "" {
				return 0, len(items), fmt.Errorf("trash file item %q is missing SymlinkID", item.Name)
			}
			fileSymlinkIDs = append(fileSymlinkIDs, item.SymlinkID)
		}
	}

	if err := apiClient.PostTrashBinAction(ctx, action, fileSymlinkIDs, folderIDs); err != nil {
		fs.logger.Error().Err(err).Str("action", action).Int("count", len(items)).Msg("Trash bulk action failed")
		return 0, len(items), err
	}
	return len(items), 0, nil
}

// ListFolderPage returns a single page of folder contents with pagination support.
// Pass empty cursor for first page, or use NextCursor from previous response.
// Pass pageSize=0 for API default.
func (fs *FileService) ListFolderPage(ctx context.Context, folderID string, cursor string, pageSize int) (*FolderContents, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return nil, fmt.Errorf("API client not configured")
	}

	if folderID == "" {
		// For root, get MyLibrary folder first
		roots, err := apiClient.GetRootFolders(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get root folders: %w", err)
		}
		folderID = roots.MyLibrary
	}

	// Use the paginated API method
	contents, err := apiClient.ListFolderContentsPage(ctx, folderID, cursor, pageSize)
	if err != nil {
		return nil, fmt.Errorf("failed to list folder contents: %w", err)
	}

	items := make([]FileItem, 0, len(contents.Folders)+len(contents.Files))

	// Add folders first
	for _, f := range contents.Folders {
		items = append(items, FileItem{
			ID:       f.ID,
			Name:     f.Name,
			IsFolder: true,
			ModTime:  f.DateUploaded,
		})
	}

	// Add files
	for _, f := range contents.Files {
		items = append(items, FileItem{
			ID:       f.ID,
			Name:     f.Name,
			IsFolder: false,
			Size:     f.DecryptedSize,
			ModTime:  f.DateUploaded,
		})
	}

	return &FolderContents{
		FolderID:   folderID,
		Items:      items,
		HasMore:    contents.HasMore,
		NextCursor: contents.NextURL,
	}, nil
}
