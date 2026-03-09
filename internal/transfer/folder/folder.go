// Package folder provides shared folder-upload primitives for both CLI and GUI paths.
// v4.8.7 Plan 2b: Extracted from internal/cli/folder_upload_helper.go to fix layering
// violation (GUI was importing internal/cli directly).
package folder

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/transfer"
)

// FolderReadyEvent signals that a folder has been created and is ready for file uploads
type FolderReadyEvent struct {
	LocalPath string
	RemoteID  string
	Depth     int
}

// FolderCache caches folder contents to minimize API calls
type FolderCache struct {
	cache map[string]*api.FolderContents // folderID -> contents
	mu    sync.RWMutex
}

// NewFolderCache creates a new folder cache
func NewFolderCache() *FolderCache {
	return &FolderCache{
		cache: make(map[string]*api.FolderContents),
	}
}

// Get retrieves folder contents from cache or fetches from API if not cached
func (fc *FolderCache) Get(ctx context.Context, apiClient *api.Client, folderID string) (*api.FolderContents, error) {
	// Check cache first (read lock)
	fc.mu.RLock()
	if contents, ok := fc.cache[folderID]; ok {
		fc.mu.RUnlock()
		return contents, nil
	}
	fc.mu.RUnlock()

	// Not in cache, fetch it (write lock)
	fc.mu.Lock()
	defer fc.mu.Unlock()

	// Double-check (another goroutine might have fetched it)
	if contents, ok := fc.cache[folderID]; ok {
		return contents, nil
	}

	// Fetch from API (all pages — critical for folders with >2000 items)
	contents, err := apiClient.ListFolderContentsAll(ctx, folderID)
	if err != nil {
		return nil, err
	}

	fc.cache[folderID] = contents
	return contents, nil
}

// Invalidate removes a folder from the cache, forcing a fresh fetch on next Get.
// v4.0.8: Used to handle stale cache when folder creation fails with "already exists".
func (fc *FolderCache) Invalidate(folderID string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	delete(fc.cache, folderID)
}

// BuildDirectoryTree walks a local directory and returns lists of directories, files, and symlinks.
// This function is exported for use by the GUI.
//
// v4.0.4: Refactored to use localfs.WalkCollect() for North Star alignment
// (shared code between CLI and GUI for local filesystem operations).
// Returns string slices for backward compatibility with existing callers.
func BuildDirectoryTree(rootPath string, includeHidden bool) ([]string, []string, []string, error) {
	// Use shared localfs.WalkCollect() for core directory walking
	result, err := localfs.WalkCollect(rootPath, localfs.WalkOptions{
		IncludeHidden:  includeHidden,
		SkipHiddenDirs: true, // Skip hidden directories entirely
	})
	if err != nil {
		return nil, nil, nil, err
	}

	// Convert FileEntry slices to string slices for backward compatibility
	directories := make([]string, len(result.Directories))
	for i, entry := range result.Directories {
		directories[i] = entry.Path
	}

	files := make([]string, len(result.Files))
	for i, entry := range result.Files {
		files[i] = entry.Path
	}

	symlinks := make([]string, len(result.Symlinks))
	for i, entry := range result.Symlinks {
		symlinks[i] = entry.Path
	}

	return directories, files, symlinks, nil
}

// CheckFolderExists checks if a folder with the given name exists in the parent folder
// Exported for GUI reuse
func CheckFolderExists(ctx context.Context, apiClient *api.Client, cache *FolderCache, parentID, name string) (string, bool, error) {
	// Get contents from cache (will fetch from API if not cached)
	contents, err := cache.Get(ctx, apiClient, parentID)
	if err != nil {
		return "", false, err
	}

	// Look for folder with matching name
	for _, f := range contents.Folders {
		if f.Name == name {
			return f.ID, true, nil
		}
	}

	return "", false, nil
}

// processFolderParams groups the shared parameters for per-folder processing.
// v4.8.5: Extracted to enable code reuse between CreateFolderStructure and
// CreateFolderStructureStreaming (North Star: maximum code reuse).
type processFolderParams struct {
	ctx                context.Context
	apiClient          *api.Client
	cache              *FolderCache
	folderConflictMode *ConflictAction
	conflictPrompt     ConflictPrompt // nil = no interactive prompting (GUI path)
	logger             *logging.Logger
	progressWriter     io.Writer
	folderReadyChan    chan<- FolderReadyEvent
	mapping            map[string]string // localPath → remoteID
	mappingMu          *sync.RWMutex
	foldersCreated     *int32 // atomic counter
}

// processFolder creates or merges a single folder, updating the mapping.
// v4.8.5: Shared helper between CreateFolderStructure and CreateFolderStructureStreaming.
// v4.8.7: Uses conflictPrompt callback instead of hardcoded promptFolderConflict.
// Returns (remoteID, created, error). On skip, returns ("", false, nil).
func processFolder(p processFolderParams, dirPath string) (string, bool, error) {
	parentPath := filepath.Dir(dirPath)
	p.mappingMu.RLock()
	parentRemoteID, ok := p.mapping[parentPath]
	p.mappingMu.RUnlock()

	if !ok {
		return "", false, fmt.Errorf("parent folder not created: %s", parentPath)
	}

	folderName := filepath.Base(dirPath)

	// Check if exists (uses cache)
	existingID, exists, err := CheckFolderExists(p.ctx, p.apiClient, p.cache, parentRemoteID, folderName)
	if err != nil {
		return "", false, fmt.Errorf("failed to check if folder exists: %w", err)
	}

	if exists {
		action := *p.folderConflictMode
		if action == ConflictSkipOnce || action == ConflictMergeOnce {
			if p.conflictPrompt != nil {
				action, err = p.conflictPrompt(folderName)
				if err != nil {
					return "", false, err
				}
			} else {
				// No interactive prompt — upgrade to All variant
				if action == ConflictSkipOnce {
					action = ConflictSkipAll
				}
				if action == ConflictMergeOnce {
					action = ConflictMergeAll
				}
			}
			if action == ConflictSkipAll || action == ConflictMergeAll {
				*p.folderConflictMode = action
			}
		}

		switch action {
		case ConflictSkipOnce, ConflictSkipAll:
			if p.progressWriter != nil {
				fmt.Fprintf(p.progressWriter, "  ⏭  Skipping existing folder: %s\n", folderName)
			}
			return "", false, nil // Skip — don't add to mapping
		case ConflictMergeOnce, ConflictMergeAll:
			if p.progressWriter != nil {
				fmt.Fprintf(p.progressWriter, "  ♻️  Using existing folder: %s\n", folderName)
			}
			p.mappingMu.Lock()
			p.mapping[dirPath] = existingID
			p.mappingMu.Unlock()

			if p.folderReadyChan != nil {
				depth := strings.Count(dirPath, string(os.PathSeparator))
				select {
				case p.folderReadyChan <- FolderReadyEvent{LocalPath: dirPath, RemoteID: existingID, Depth: depth}:
				case <-p.ctx.Done():
					return "", false, p.ctx.Err()
				}
			}
			return existingID, false, nil
		case ConflictAbort:
			return "", false, fmt.Errorf("upload aborted by user")
		}
	}

	// Create new folder
	folderID, err := p.apiClient.CreateFolder(p.ctx, folderName, parentRemoteID)
	if err != nil {
		return "", false, fmt.Errorf("failed to create folder %s: %w", folderName, err)
	}

	// Populate cache for newly created folder
	if _, err := p.cache.Get(p.ctx, p.apiClient, folderID); err != nil {
		p.logger.Warn().Str("folder_id", folderID).Err(err).Msg("Failed to populate cache for new folder")
	}

	p.mappingMu.Lock()
	p.mapping[dirPath] = folderID
	p.mappingMu.Unlock()

	atomic.AddInt32(p.foldersCreated, 1)

	if p.progressWriter != nil {
		fmt.Fprintf(p.progressWriter, "  ✓ Created folder: %s (ID: %s)\n", folderName, folderID)
	}

	if p.folderReadyChan != nil {
		depth := strings.Count(dirPath, string(os.PathSeparator))
		select {
		case p.folderReadyChan <- FolderReadyEvent{LocalPath: dirPath, RemoteID: folderID, Depth: depth}:
		case <-p.ctx.Done():
			return "", false, p.ctx.Err()
		}
	}

	return folderID, true, nil
}

// CreateFolderStructure creates all folders recursively, handling conflicts.
// If folderReadyChan is provided, sends events as folders become ready for file uploads.
// v4.8.5: Refactored to use processFolder helper for North Star code reuse.
// v4.8.7: Added conflictPrompt parameter to decouple from CLI prompting.
func CreateFolderStructure(
	ctx context.Context,
	apiClient *api.Client,
	cache *FolderCache,
	rootPath string,
	directories []string,
	rootRemoteID string,
	folderConflictMode *ConflictAction,
	maxConcurrent int,
	logger *logging.Logger,
	folderReadyChan chan<- FolderReadyEvent,
	progressWriter io.Writer,
	conflictPrompt ConflictPrompt,
) (map[string]string, int, error) {
	mapping := make(map[string]string)
	mapping[rootPath] = rootRemoteID
	var foldersCreated int32
	var mappingMu sync.RWMutex

	// Send ready event for root folder if channel provided
	if folderReadyChan != nil {
		select {
		case folderReadyChan <- FolderReadyEvent{
			LocalPath: rootPath,
			RemoteID:  rootRemoteID,
			Depth:     strings.Count(rootPath, string(os.PathSeparator)),
		}:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}

	params := processFolderParams{
		ctx:                ctx,
		apiClient:          apiClient,
		cache:              cache,
		folderConflictMode: folderConflictMode,
		conflictPrompt:     conflictPrompt,
		logger:             logger,
		progressWriter:     progressWriter,
		folderReadyChan:    folderReadyChan,
		mapping:            mapping,
		mappingMu:          &mappingMu,
		foldersCreated:     &foldersCreated,
	}

	// Sort directories by depth (create parents first)
	sort.Slice(directories, func(i, j int) bool {
		depthI := strings.Count(directories[i], string(os.PathSeparator))
		depthJ := strings.Count(directories[j], string(os.PathSeparator))
		return depthI < depthJ
	})

	// Group directories by depth level for concurrent processing
	depthGroups := make(map[int][]string)
	for _, dirPath := range directories {
		depth := strings.Count(dirPath, string(os.PathSeparator))
		depthGroups[depth] = append(depthGroups[depth], dirPath)
	}

	depths := make([]int, 0, len(depthGroups))
	for depth := range depthGroups {
		depths = append(depths, depth)
	}
	sort.Ints(depths)

	for _, depth := range depths {
		dirsAtDepth := depthGroups[depth]

		semaphore := make(chan struct{}, maxConcurrent)
		var wg sync.WaitGroup
		errChan := make(chan error, len(dirsAtDepth))

		for _, dirPath := range dirsAtDepth {
			wg.Add(1)
			go func(dp string) {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				_, _, err := processFolder(params, dp)
				if err != nil {
					errChan <- err
				}
			}(dirPath)
		}

		wg.Wait()
		close(errChan)

		// v4.8.7: Use unified error collection
		errs := transfer.CollectErrors(errChan)
		if len(errs) > 0 {
			return nil, 0, errs[0]
		}
	}

	return mapping, int(foldersCreated), nil
}

// CreateFolderStructureStreaming creates remote folders from a streaming directory channel.
// Unlike CreateFolderStructure which requires all directories upfront, this processes
// directories as they are discovered by WalkStream.
//
// v4.8.5: Uses parent-ready gating — a folder can be created as soon as its parent
// exists in the mapping. filepath.WalkDir guarantees parents are visited before
// children, so pending buffers stay small.
// v4.8.7: Added conflictPrompt parameter to decouple from CLI prompting.
//
// Returns the mapping (localPath → remoteID), folders created count, and any error.
func CreateFolderStructureStreaming(
	ctx context.Context,
	apiClient *api.Client,
	cache *FolderCache,
	rootPath string,
	dirChan <-chan localfs.FileEntry,
	rootRemoteID string,
	folderConflictMode *ConflictAction,
	maxConcurrent int,
	logger *logging.Logger,
	folderReadyChan chan<- FolderReadyEvent,
	progressWriter io.Writer,
	conflictPrompt ConflictPrompt,
) (map[string]string, int, error) {
	mapping := make(map[string]string)
	mapping[rootPath] = rootRemoteID
	var foldersCreated int32
	var mappingMu sync.RWMutex

	// Send ready event for root folder
	if folderReadyChan != nil {
		select {
		case folderReadyChan <- FolderReadyEvent{
			LocalPath: rootPath,
			RemoteID:  rootRemoteID,
			Depth:     strings.Count(rootPath, string(os.PathSeparator)),
		}:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}

	params := processFolderParams{
		ctx:                ctx,
		apiClient:          apiClient,
		cache:              cache,
		folderConflictMode: folderConflictMode,
		conflictPrompt:     conflictPrompt,
		logger:             logger,
		progressWriter:     progressWriter,
		folderReadyChan:    folderReadyChan,
		mapping:            mapping,
		mappingMu:          &mappingMu,
		foldersCreated:     &foldersCreated,
	}

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var pendingDirs []string
	var pendingMu sync.Mutex
	var firstErr error
	var errMu sync.Mutex

	setErr := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}

	// flushPending launches goroutines for any pending dirs whose parent is now ready.
	// Declared as var for recursive use from goroutines.
	// Must be called under pendingMu lock.
	var flushPending func()
	flushPending = func() {
		remaining := pendingDirs[:0]
		for _, dp := range pendingDirs {
			parentPath := filepath.Dir(dp)
			mappingMu.RLock()
			_, parentReady := mapping[parentPath]
			mappingMu.RUnlock()

			if parentReady {
				dpCopy := dp
				wg.Add(1)
				go func() {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()

					_, _, err := processFolder(params, dpCopy)
					if err != nil {
						setErr(err)
						return
					}
					// After adding to mapping, flush any pending children
					pendingMu.Lock()
					flushPending()
					pendingMu.Unlock()
				}()
			} else {
				remaining = append(remaining, dp)
			}
		}
		pendingDirs = remaining
	}

	// Process directories as they arrive from the walk
	for dir := range dirChan {
		errMu.Lock()
		hasErr := firstErr != nil
		errMu.Unlock()
		if hasErr {
			break
		}

		dirPath := dir.Path
		parentPath := filepath.Dir(dirPath)

		mappingMu.RLock()
		_, parentReady := mapping[parentPath]
		mappingMu.RUnlock()

		if parentReady {
			// Parent is ready — create immediately
			wg.Add(1)
			go func(dp string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				_, _, err := processFolder(params, dp)
				if err != nil {
					setErr(err)
					return
				}
				// Flush any pending children
				pendingMu.Lock()
				flushPending()
				pendingMu.Unlock()
			}(dirPath)
		} else {
			// Parent not yet created — buffer
			pendingMu.Lock()
			pendingDirs = append(pendingDirs, dirPath)
			pendingMu.Unlock()
		}
	}

	// Wait for all in-flight folder creations
	wg.Wait()

	// Final flush attempt for any remaining pending dirs
	pendingMu.Lock()
	flushPending()
	pendingMu.Unlock()
	wg.Wait()

	// Any remaining pending dirs are errors (parent was never created)
	pendingMu.Lock()
	if len(pendingDirs) > 0 && firstErr == nil {
		firstErr = fmt.Errorf("orphaned directories (parent never created): %d remaining, first: %s",
			len(pendingDirs), pendingDirs[0])
	}
	pendingMu.Unlock()

	return mapping, int(foldersCreated), firstErr
}
