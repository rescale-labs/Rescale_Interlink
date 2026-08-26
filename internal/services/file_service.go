// Package services provides frontend-agnostic business logic for Rescale Interlink.
package services

import (
	"context"
	"fmt"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
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

// ListLegacyFiles returns a flat list of all files (legacy mode).
// Pass pageSize=0 for API default.
func (fs *FileService) ListLegacyFiles(ctx context.Context, cursor string, pageSize int) (*FolderContents, error) {
	return fs.ListLegacyFilesWithOptions(ctx, cursor, pageSize, nil)
}

// ListLegacyFilesWithOptions returns a flat list of files with optional filtering.
// Pass pageSize=0 for API default.
// options: optional filters (owner, search), pass nil for no filters.
func (fs *FileService) ListLegacyFilesWithOptions(ctx context.Context, cursor string, pageSize int, options *api.FileListOptions) (*FolderContents, error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return nil, fmt.Errorf("API client not configured")
	}

	page, err := apiClient.ListFilesPageWithOptions(ctx, cursor, pageSize, options)
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}

	items := make([]FileItem, 0, len(page.Files))
	for _, f := range page.Files {
		items = append(items, FileItem{
			ID:           f.ID,
			Name:         f.Name,
			IsFolder:     false,
			Size:         f.DecryptedSize,
			ModTime:      f.DateUploaded,
			TypeCode:     f.TypeCode,
			Owner:        f.Owner,
			DateInserted: f.DateInserted,
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

// ArchiveItems moves files and/or folders to the user's Trash (soft delete),
// matching the web UI's delete behavior. parentFolderID is the folder the items
// currently live in (the archive endpoint is folder-scoped and rejects a
// mismatched parent). Items can be recovered from the Trash view afterward.
//
// This is the default delete path for user-initiated deletes. Permanent
// deletion uses DeleteFile/DeleteFolder.
func (fs *FileService) ArchiveItems(ctx context.Context, parentFolderID string, items []FileItem) (archived int, failed int, err error) {
	fs.mu.RLock()
	apiClient := fs.apiClient
	fs.mu.RUnlock()

	if apiClient == nil {
		return 0, len(items), fmt.Errorf("API client not configured")
	}
	if parentFolderID == "" {
		return 0, len(items), fmt.Errorf("a parent folder is required to move items to Trash")
	}

	fileIDs := make([]string, 0, len(items))
	folderIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item.IsFolder {
			folderIDs = append(folderIDs, item.ID)
		} else {
			fileIDs = append(fileIDs, item.ID)
		}
	}

	if err := apiClient.ArchiveContents(ctx, parentFolderID, fileIDs, folderIDs); err != nil {
		fs.logger.Error().Err(err).Str("parentFolder", parentFolderID).Int("count", len(items)).Msg("Move to Trash failed")
		// The archive endpoint is all-or-nothing, so on error none were archived.
		return 0, len(items), err
	}

	return len(items), 0, nil
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
			Size:     f.Size,
			ModTime:  f.DateUploaded,
			Owner:    f.Owner,
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

// SearchFolderContents searches within a folder for files/folders matching the query.
// cursor: pass "" for the first page, or NextCursor from a previous response.
// Returns paginated results similar to ListFolderPage.
func (fs *FileService) SearchFolderContents(ctx context.Context, folderID string, searchQuery string, cursor string, pageSize int) (*FolderContents, error) {
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

	// Use the search API method
	contents, err := apiClient.SearchFolderContents(ctx, folderID, searchQuery, cursor, pageSize)
	if err != nil {
		return nil, fmt.Errorf("failed to search folder contents: %w", err)
	}

	items := make([]FileItem, 0, len(contents.Folders)+len(contents.Files))

	// Add folders first
	for _, f := range contents.Folders {
		items = append(items, FileItem{
			ID:       f.ID,
			Name:     f.Name,
			IsFolder: true,
			Size:     f.Size,
			ModTime:  f.DateUploaded,
			Owner:    f.Owner,
		})
	}

	// Add files
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
		FolderID:   folderID,
		Items:      items,
		HasMore:    contents.HasMore,
		NextCursor: contents.NextURL,
	}, nil
}
