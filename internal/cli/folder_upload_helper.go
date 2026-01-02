package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/diskspace"
	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/progress"
)

// DirectoryMapping tracks local path to remote folder ID
type DirectoryMapping struct {
	LocalPath      string
	RemoteFolderID string
	Created        bool
}

// UploadTask represents a file to upload
type UploadTask struct {
	LocalPath      string
	RelativePath   string // Relative to root
	RemoteFolderID string
	Size           int64
}

// UploadResult tracks what happened during upload
type UploadResult struct {
	FoldersCreated  int
	FilesUploaded   int
	FilesSkipped    int
	FilesIgnored    int
	TotalBytes      int64
	Errors          []UploadError
	SymlinksSkipped []string
}

// UploadError tracks failed uploads
type UploadError struct {
	FilePath string
	Error    error
}

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

	// Fetch from API
	contents, err := apiClient.ListFolderContents(ctx, folderID)
	if err != nil {
		return nil, err
	}

	fc.cache[folderID] = contents
	return contents, nil
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
	for _, folder := range contents.Folders {
		if folder.Name == name {
			return folder.ID, true, nil
		}
	}

	return "", false, nil
}

// checkFileExists checks if a file with the given name exists in the folder
func checkFileExists(ctx context.Context, apiClient *api.Client, cache *FolderCache, folderID, fileName string) (string, bool, error) {
	// Get contents from cache (will fetch from API if not cached)
	contents, err := cache.Get(ctx, apiClient, folderID)
	if err != nil {
		return "", false, err
	}

	for _, file := range contents.Files {
		if file.Name == fileName {
			return file.ID, true, nil
		}
	}

	return "", false, nil
}

// CreateFolderStructure creates all folders recursively, handling conflicts
// If folderReadyChan is provided, sends events as folders become ready for file uploads
// Exported for GUI reuse
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
) (map[string]string, int, error) {
	// Returns mapping: local path -> remote folder ID, and count of folders created
	mapping := make(map[string]string)
	mapping[rootPath] = rootRemoteID
	foldersCreated := 0
	var mappingMutex sync.RWMutex

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

	// Process each depth level sequentially, but folders within a level concurrently
	depths := make([]int, 0, len(depthGroups))
	for depth := range depthGroups {
		depths = append(depths, depth)
	}
	sort.Ints(depths)

	for _, depth := range depths {
		dirsAtDepth := depthGroups[depth]

		// Use semaphore to limit concurrent folder creates
		semaphore := make(chan struct{}, maxConcurrent)
		var wg sync.WaitGroup
		errChan := make(chan error, len(dirsAtDepth))

		for _, dirPath := range dirsAtDepth {
			wg.Add(1)
			go func(dp string) {
				defer wg.Done()

				// Acquire semaphore
				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				// Get parent directory
				parentPath := filepath.Dir(dp)
				mappingMutex.RLock()
				parentRemoteID, ok := mapping[parentPath]
				mappingMutex.RUnlock()

				if !ok {
					errChan <- fmt.Errorf("parent folder not created: %s", parentPath)
					return
				}

				// Get folder name
				folderName := filepath.Base(dp)

				// Check if exists (uses cache)
				existingID, exists, err := CheckFolderExists(ctx, apiClient, cache, parentRemoteID, folderName)
				if err != nil {
					errChan <- fmt.Errorf("failed to check if folder exists: %w", err)
					return
				}

				if exists {
					// Handle conflict (note: prompts will serialize due to stdin)
					action := *folderConflictMode
					if action == ConflictSkipOnce || action == ConflictMergeOnce {
						// Prompt user
						action, err = promptFolderConflict(folderName)
						if err != nil {
							errChan <- err
							return
						}
						// Update mode if "all" selected
						if action == ConflictSkipAll || action == ConflictMergeAll {
							*folderConflictMode = action
						}
					}

					switch action {
					case ConflictSkipOnce, ConflictSkipAll:
						if progressWriter != nil {
							fmt.Fprintf(progressWriter, "  ⏭  Skipping existing folder: %s\n", folderName)
						}
						// Don't add to mapping, so files in this folder will also be skipped
						return
					case ConflictMergeOnce, ConflictMergeAll:
						if progressWriter != nil {
							fmt.Fprintf(progressWriter, "  ♻️  Using existing folder: %s\n", folderName)
						}
						mappingMutex.Lock()
						mapping[dp] = existingID
						mappingMutex.Unlock()

						// Send folder ready event if channel provided
						if folderReadyChan != nil {
							select {
							case folderReadyChan <- FolderReadyEvent{
								LocalPath: dp,
								RemoteID:  existingID,
								Depth:     depth,
							}:
							case <-ctx.Done():
								errChan <- ctx.Err()
								return
							}
						}
					case ConflictAbort:
						errChan <- fmt.Errorf("upload aborted by user")
						return
					}
				} else {
					// Create new folder
					folderID, err := apiClient.CreateFolder(ctx, folderName, parentRemoteID)
					if err != nil {
						errChan <- fmt.Errorf("failed to create folder %s: %w", folderName, err)
						return
					}

					// Populate cache for newly created folder (empty initially)
					// This ensures subsequent file checks can use cache
					_, err = cache.Get(ctx, apiClient, folderID)
					if err != nil {
						logger.Warn().Str("folder_id", folderID).Err(err).Msg("Failed to populate cache for new folder")
						// Non-fatal, continue
					}

					mappingMutex.Lock()
					mapping[dp] = folderID
					foldersCreated++
					mappingMutex.Unlock()

					if progressWriter != nil {
						fmt.Fprintf(progressWriter, "  ✓ Created folder: %s (ID: %s)\n", folderName, folderID)
					}

					// Send folder ready event if channel provided
					if folderReadyChan != nil {
						select {
						case folderReadyChan <- FolderReadyEvent{
							LocalPath: dp,
							RemoteID:  folderID,
							Depth:     depth,
						}:
						case <-ctx.Done():
							errChan <- ctx.Err()
							return
						}
					}
				}
			}(dirPath)
		}

		// Wait for all folders at this depth level
		wg.Wait()
		close(errChan)

		// Check for errors
		for err := range errChan {
			return nil, 0, err
		}
	}

	return mapping, foldersCreated, nil
}

// uploadDirectoryPipelined coordinates pipelined folder creation and file uploads
// Folders are created depth-by-depth, and files are uploaded as soon as their parent folder is ready
func uploadDirectoryPipelined(
	ctx context.Context,
	apiClient *api.Client,
	cache *FolderCache,
	rootPath string,
	directories []string,
	rootRemoteID string,
	files []string,
	folderConcurrency int,
	fileConcurrency int,
	continueOnError bool,
	skipExisting bool,
	cfg *config.Config,
	logger *logging.Logger,
) (*UploadResult, int, error) {
	result := &UploadResult{}
	var resultMutex sync.Mutex
	foldersCreated := 0
	var foldersCreatedMutex sync.Mutex

	// Build map of files per directory
	filesPerDir := make(map[string][]string)
	for _, filePath := range files {
		dirPath := filepath.Dir(filePath)
		filesPerDir[dirPath] = append(filesPerDir[dirPath], filePath)
	}

	// Create folder ready channel (buffered to prevent blocking)
	folderReadyChan := make(chan FolderReadyEvent, 100)

	// Create progress UI
	uploadUI := progress.NewUploadUI(len(files))

	// NOTE: Do NOT redirect zerolog through uploadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.
	// The mpb library handles rendering progress bars above stderr output automatically.

	defer uploadUI.Wait()

	// Pre-warm credentials and prepare for uploads
	logger.Debug().Msg("Pre-fetching storage credentials")
	credManager := credentials.GetManager(apiClient)
	_, err := credManager.GetS3Credentials(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to pre-fetch credentials, will fetch on demand")
	}

	// Shared state for conflict modes
	folderConflictMode := ConflictMergeOnce
	if skipExisting {
		folderConflictMode = ConflictMergeAll
	}
	fileConflictMode := FileOverwriteOnce
	if skipExisting {
		fileConflictMode = FileSkipAll
	}
	errorMode := ErrorContinueOnce

	// Semaphore for file uploads
	uploadSemaphore := make(chan struct{}, fileConcurrency)

	// WaitGroup to track all operations
	var wg sync.WaitGroup

	// 1. Start folder creation goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(folderReadyChan) // Signal completion to file uploader

		_, created, err := CreateFolderStructure(
			ctx, apiClient, cache, rootPath, directories, rootRemoteID,
			&folderConflictMode, folderConcurrency, logger, folderReadyChan,
			uploadUI.Writer())

		if err != nil {
			logger.Error().Err(err).Msg("Folder creation failed")
			return
		}

		foldersCreatedMutex.Lock()
		foldersCreated = created
		foldersCreatedMutex.Unlock()

		logger.Info().Int("folders", created).Msg("Folder creation complete")
	}()

	// 2. Start file upload dispatcher goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Track which folders we've processed
		processedFolders := make(map[string]bool)
		var processedMutex sync.Mutex

		// Listen for folder ready events
		for event := range folderReadyChan {
			// Get files in this folder
			filesInFolder, hasFiles := filesPerDir[event.LocalPath]
			if !hasFiles || len(filesInFolder) == 0 {
				continue // No files in this folder
			}

			// Mark as processed
			processedMutex.Lock()
			if processedFolders[event.LocalPath] {
				processedMutex.Unlock()
				continue // Already processed
			}
			processedFolders[event.LocalPath] = true
			processedMutex.Unlock()

			// Cache folder path for display
			relativePath, err := filepath.Rel(rootPath, event.LocalPath)
			if err != nil {
				relativePath = filepath.Base(event.LocalPath)
			} else if relativePath == "." {
				relativePath = filepath.Base(rootPath)
			}
			uploadUI.SetFolderPath(event.RemoteID, relativePath)

			// Dispatch uploads for all files in this folder
			for i, filePath := range filesInFolder {
				wg.Add(1)
				go func(idx int, fpath string, remoteFolderID string) {
					defer wg.Done()

					// Acquire upload semaphore
					uploadSemaphore <- struct{}{}
					defer func() { <-uploadSemaphore }()

					// Upload the file (similar logic to uploadFiles function)
					fileName := filepath.Base(fpath)

					// Check if file exists
					existingFileID, exists, checkErr := checkFileExists(ctx, apiClient, cache, remoteFolderID, fileName)
					if checkErr != nil {
						if continueOnError || errorMode == ErrorContinueAll {
							resultMutex.Lock()
							result.Errors = append(result.Errors, UploadError{fpath, checkErr})
							resultMutex.Unlock()
							logger.Error().Str("file", fpath).Err(checkErr).Msg("Error checking file existence")
							return
						}
						// Handle error prompts...
						logger.Error().Err(checkErr).Str("file", fpath).Msg("Failed to check if file exists")
						return
					}

					if exists {
						// Handle file conflict
						action := fileConflictMode
						if action == FileSkipOnce || action == FileOverwriteOnce {
							var promptErr error
							action, promptErr = promptFileConflict(fileName, relativePath)
							if promptErr != nil {
								logger.Error().Err(promptErr).Str("file", fileName).Msg("Error prompting for file conflict")
								resultMutex.Lock()
								result.Errors = append(result.Errors, UploadError{fpath, promptErr})
								resultMutex.Unlock()
								return
							}
							if action == FileSkipAll || action == FileOverwriteAll {
								fileConflictMode = action
							}
						}

						switch action {
						case FileSkipOnce, FileSkipAll:
							logger.Debug().Str("file", fileName).Msg("Ignoring existing file")
							resultMutex.Lock()
							result.FilesIgnored++
							resultMutex.Unlock()
							return
						case FileOverwriteOnce, FileOverwriteAll:
							// Delete existing file
							if delErr := apiClient.DeleteFile(ctx, existingFileID); delErr != nil {
								logger.Error().Str("file", fileName).Err(delErr).Msg("Failed to delete existing file")
							}
						case FileAbort:
							logger.Info().Msg("Upload aborted by user")
							return
						}
					}

					// Get file info for size
					fileInfo, statErr := os.Stat(fpath)
					if statErr != nil {
						resultMutex.Lock()
						result.Errors = append(result.Errors, UploadError{fpath, statErr})
						resultMutex.Unlock()
						return
					}

					// Create progress bar
					fileBar := uploadUI.AddFileBar(fpath, remoteFolderID, fileInfo.Size())

					// Upload file
					cloudFile, uploadErr := upload.UploadFile(ctx, upload.UploadParams{
						LocalPath: fpath,
						FolderID:  remoteFolderID,
						APIClient: apiClient,
						ProgressCallback: func(fraction float64) {
							fileBar.UpdateProgress(fraction)
						},
						OutputWriter: uploadUI.Writer(),
					})

					if uploadErr != nil {
						fileBar.Complete("", uploadErr)

						// Check if resume state exists to provide helpful guidance
						if state.UploadResumeStateExists(fpath) {
							fmt.Fprintf(os.Stderr, "\n💡 Resume state saved for %s. To resume, re-run the upload command.\n", filepath.Base(fpath))
						}

						resultMutex.Lock()
						result.Errors = append(result.Errors, UploadError{fpath, uploadErr})
						resultMutex.Unlock()
						logger.Error().Str("file", fpath).Err(uploadErr).Msg("Failed to upload file")
						return
					}

					// Success
					fileBar.Complete(cloudFile.ID, nil)
					resultMutex.Lock()
					result.FilesUploaded++
					result.TotalBytes += fileInfo.Size()
					resultMutex.Unlock()
				}(i, filePath, event.RemoteID)
			}
		}
	}()

	// Wait for all operations to complete
	wg.Wait()

	return result, foldersCreated, nil
}

// uploadFiles uploads all files with progress tracking and conflict/error handling
func uploadFiles(
	ctx context.Context,
	rootPath string,
	files []string,
	mapping map[string]string,
	apiClient *api.Client,
	cache *FolderCache,
	uploadUI *progress.UploadUI,
	fileConflictMode *FileConflictAction,
	errorMode *ErrorAction,
	continueOnError bool,
	maxConcurrent int,
	cfg *config.Config,
	logger *logging.Logger,
) (*UploadResult, error) {
	result := &UploadResult{}
	var resultMutex sync.Mutex

	// NOTE: Do NOT redirect zerolog through uploadUI.Writer()
	// Zerolog outputs JSON which causes "invalid character '\x1b'" errors
	// when mixed with ANSI escape codes from mpb progress bars.

	// Pre-warm credential cache before starting uploads
	// This eliminates the first-upload credential fetch delay
	logger.Debug().Msg("Pre-fetching storage credentials")
	credManager := credentials.GetManager(apiClient)
	_, err := credManager.GetS3Credentials(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to pre-fetch credentials, will fetch on demand")
		// Continue anyway - uploads will fetch credentials as needed
	}

	// Use semaphore to limit concurrent uploads
	semaphore := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for _, filePath := range files {
		wg.Add(1)
		go func(fpath string) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Get parent directory
			dirPath := filepath.Dir(fpath)
			remoteFolderID, ok := mapping[dirPath]
			if !ok {
				// Folder was skipped, skip this file
				resultMutex.Lock()
				result.FilesSkipped++
				resultMutex.Unlock()
				logger.Info().Str("file", fpath).Msg("Skipping file (parent folder was skipped)")
				return
			}

			fileName := filepath.Base(fpath)
			relativePath, relErr := filepath.Rel(rootPath, fpath)
			if relErr != nil {
				relativePath = fileName // Fall back to just the filename
			}

			// SAFE MODE: Check if file exists before uploading (uses cache)
			// FAST MODE: Skip check, handle conflicts on upload error
			var existingFileID string
			var exists bool
			var err error

			if cfg.CheckConflictsBeforeUpload {
				existingFileID, exists, err = checkFileExists(ctx, apiClient, cache, remoteFolderID, fileName)
			}

			if cfg.CheckConflictsBeforeUpload && err != nil {
				// Handle check error
				if continueOnError || *errorMode == ErrorContinueAll {
					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, err})
					resultMutex.Unlock()
					logger.Error().Str("file", fpath).Err(err).Msg("Error checking file existence")
					return
				} else if *errorMode == ErrorContinueOnce {
					action, promptErr := promptUploadError(fileName, err)
					if promptErr != nil {
						logger.Error().Err(promptErr).Msg("Error prompting user")
						return
					}
					if action == ErrorAbort {
						logger.Info().Msg("Upload aborted by user")
						return
					}
					if action == ErrorContinueAll {
						*errorMode = ErrorContinueAll
					}
					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, err})
					resultMutex.Unlock()
					return
				} else {
					logger.Error().Err(err).Str("file", fpath).Msg("Failed to check if file exists")
					return
				}
			}

			// SAFE MODE: Handle conflicts BEFORE upload
			if cfg.CheckConflictsBeforeUpload && exists {
				// Handle file conflict
				action := *fileConflictMode
				if action == FileSkipOnce || action == FileOverwriteOnce {
					// Prompt user
					folderPath := filepath.Dir(relativePath)
					if folderPath == "." {
						folderPath = filepath.Base(rootPath)
					}
					action, err = promptFileConflict(fileName, folderPath)
					if err != nil {
						logger.Error().Err(err).Msg("Error prompting user")
						return
					}
					// Update mode if "all" selected
					if action == FileSkipAll || action == FileOverwriteAll {
						*fileConflictMode = action
					}
				}

				switch action {
				case FileSkipOnce, FileSkipAll:
					logger.Debug().Str("file", fileName).Msg("Ignoring existing file")
					fmt.Fprintf(uploadUI.Writer(), "  ⏭  Ignoring existing file: %s\n", fileName)
					resultMutex.Lock()
					result.FilesIgnored++
					resultMutex.Unlock()
					return
				case FileOverwriteOnce, FileOverwriteAll:
					// Delete existing file first
					logger.Info().Str("file", fileName).Str("file_id", existingFileID).Msg("Deleting existing file before overwrite")
					if err := apiClient.DeleteFile(ctx, existingFileID); err != nil {
						logger.Error().Str("file", fileName).Err(err).Msg("Failed to delete existing file")
						// Continue with upload anyway
					}
				case FileAbort:
					logger.Info().Msg("Upload aborted by user")
					return
				}
			}

			// Get file info
			fileInfo, err := os.Stat(fpath)
			if err != nil {
				// Handle error
				if continueOnError || *errorMode == ErrorContinueAll {
					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, err})
					resultMutex.Unlock()
					logger.Error().Str("file", fpath).Err(err).Msg("Error getting file info")
					return
				} else if *errorMode == ErrorContinueOnce {
					action, promptErr := promptUploadError(fileName, err)
					if promptErr != nil {
						logger.Error().Err(promptErr).Msg("Error prompting user")
						return
					}
					if action == ErrorAbort {
						logger.Info().Msg("Upload aborted by user")
						return
					}
					if action == ErrorContinueAll {
						*errorMode = ErrorContinueAll
					}
					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, err})
					resultMutex.Unlock()
					return
				} else {
					logger.Error().Err(err).Str("file", fpath).Msg("Failed to stat file")
					return
				}
			}

			// Create progress bar for this file
			fileBar := uploadUI.AddFileBar(fpath, remoteFolderID, fileInfo.Size())

			// Upload with progress callback
			cloudFile, err := upload.UploadFile(ctx, upload.UploadParams{
				LocalPath: fpath,
				FolderID:  remoteFolderID,
				APIClient: apiClient,
				ProgressCallback: func(prog float64) {
					fileBar.UpdateProgress(prog)
				},
				OutputWriter: uploadUI.Writer(),
			})

			// Get fileID for completion
			fileID := ""
			if cloudFile != nil {
				fileID = cloudFile.ID
			}

			if err != nil {
				// Mark upload as failed
				fileBar.Complete(fileID, err)

				// Special handling for disk space errors - always skip, don't prompt
				if diskspace.IsInsufficientSpaceError(err) {
					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, err})
					resultMutex.Unlock()
					logger.Error().Str("file", fpath).Err(err).Msg("Upload skipped - insufficient disk space")
					return
				}

				// FAST MODE: Handle conflict detected on upload
				if !cfg.CheckConflictsBeforeUpload && api.IsFileExistsError(err) {
					logger.Warn().Str("file", fileName).Msg("File already exists (detected on upload)")

					// Query folder to get existing file ID
					contents, queryErr := apiClient.ListFolderContents(ctx, remoteFolderID)
					if queryErr != nil {
						logger.Error().Err(queryErr).Msg("Failed to query folder after conflict")
						resultMutex.Lock()
						result.Errors = append(result.Errors, UploadError{fpath, queryErr})
						resultMutex.Unlock()
						return
					}

					// Find existing file
					foundExisting := false
					for _, file := range contents.Files {
						if file.Name == fileName {
							existingFileID = file.ID
							foundExisting = true
							break
						}
					}

					if !foundExisting {
						logger.Error().Msg("File exists error but couldn't find existing file")
						resultMutex.Lock()
						result.Errors = append(result.Errors, UploadError{fpath, err})
						resultMutex.Unlock()
						return
					}

					// Prompt user for conflict resolution
					action := *fileConflictMode
					if action == FileSkipOnce || action == FileOverwriteOnce {
						folderPath := filepath.Dir(relativePath)
						if folderPath == "." {
							folderPath = filepath.Base(rootPath)
						}
						action, promptErr := promptFileConflict(fileName, folderPath)
						if promptErr != nil {
							logger.Error().Err(promptErr).Msg("Error prompting user")
							return
						}
						if action == FileSkipAll || action == FileOverwriteAll {
							*fileConflictMode = action
						}
					}

					switch action {
					case FileSkipOnce, FileSkipAll:
						logger.Debug().Str("file", fileName).Msg("Ignoring existing file")
						fmt.Fprintf(uploadUI.Writer(), "  ⏭  Ignoring existing file: %s\n", fileName)
						resultMutex.Lock()
						result.FilesIgnored++
						resultMutex.Unlock()
						return

					case FileOverwriteOnce, FileOverwriteAll:
						// Delete and retry upload
						logger.Info().Str("file", fileName).Str("file_id", existingFileID).Msg("Deleting existing file for overwrite")
						if delErr := apiClient.DeleteFile(ctx, existingFileID); delErr != nil {
							logger.Error().Err(delErr).Msg("Failed to delete existing file")
							resultMutex.Lock()
							result.Errors = append(result.Errors, UploadError{fpath, delErr})
							resultMutex.Unlock()
							return
						}

						// Retry upload
						logger.Info().Str("file", fileName).Msg("Retrying upload after deletion")
						cloudFile, retryErr := upload.UploadFile(ctx, upload.UploadParams{
							LocalPath: fpath,
							FolderID:  remoteFolderID,
							APIClient: apiClient,
							ProgressCallback: func(prog float64) {
								fileBar.UpdateProgress(prog)
							},
							OutputWriter: uploadUI.Writer(),
						})

						if retryErr != nil {
							fileBar.Complete("", retryErr)
							logger.Error().Err(retryErr).Msg("Upload failed after overwrite")
							resultMutex.Lock()
							result.Errors = append(result.Errors, UploadError{fpath, retryErr})
							resultMutex.Unlock()
							return
						}

						// Success after retry
						fileBar.Complete(cloudFile.ID, nil)
						resultMutex.Lock()
						result.FilesUploaded++
						result.TotalBytes += cloudFile.DecryptedSize
						resultMutex.Unlock()
						logger.Info().Str("file", fileName).Str("file_id", cloudFile.ID).Msg("Upload successful (after overwrite)")
						return

					case FileAbort:
						logger.Info().Msg("Upload aborted by user")
						return
					}

					// Should not reach here
					return
				}

				// Handle other upload errors
				if continueOnError || *errorMode == ErrorContinueAll {
					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, err})
					resultMutex.Unlock()
					logger.Error().Str("file", fpath).Err(err).Msg("Upload failed")
					return
				} else if *errorMode == ErrorContinueOnce {
					action, promptErr := promptUploadError(fileName, err)
					if promptErr != nil {
						logger.Error().Err(promptErr).Msg("Error prompting user")
						return
					}
					if action == ErrorAbort {
						logger.Info().Msg("Upload aborted by user")
						return
					}
					if action == ErrorContinueAll {
						*errorMode = ErrorContinueAll
					}
					resultMutex.Lock()
					result.Errors = append(result.Errors, UploadError{fpath, err})
					resultMutex.Unlock()
					return
				} else {
					logger.Error().Err(err).Str("file", fpath).Msg("Failed to upload file")
					return
				}
			}

			// Mark upload as successful
			fileBar.Complete(fileID, nil)

			resultMutex.Lock()
			result.FilesUploaded++
			result.TotalBytes += fileInfo.Size()
			resultMutex.Unlock()
		}(filePath)
	}

	// Wait for all uploads
	wg.Wait()

	return result, nil
}
