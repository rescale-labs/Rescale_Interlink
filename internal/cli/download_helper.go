package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/cloud/state"
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/progress"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/util/filter"
	"github.com/rescale/rescale-int/internal/util/paths"
	"github.com/rescale/rescale-int/internal/validation"
)

// Test seams for CLI-level integration testing.
// These are package-level function variables that default to the real implementations
// but can be overridden in tests.
var (
	listJobFilesFn = func(ctx context.Context, apiClient *api.Client, jobID string) ([]models.JobFile, error) {
		return apiClient.ListJobFiles(ctx, jobID)
	}
	downloadFileFn = func(ctx context.Context, params download.DownloadParams) error {
		return download.DownloadFile(ctx, params)
	}
)

// Precompiled regexes for sanitizeErrorString — avoids recompilation per call.
var (
	reSASToken       = regexp.MustCompile(`(sig|se|sp|sv|sr|spr|sip|srt|ss)=[^&\s"')]+`)
	reAWSKey         = regexp.MustCompile(`(?i)(access.?key|secret.?key|session.?token)=\S+`)
	reAzureKey       = regexp.MustCompile(`(?i)AccountKey=[^;&\s"']+`)
	reBearerToken    = regexp.MustCompile(`(?i)(Authorization:\s*)?((Bearer|Token)\s+)[A-Za-z0-9._\-/+=]+`)
	reAWSAccessKeyID = regexp.MustCompile(`AKIA[A-Z0-9]{16}`)
)

// cliDownloadItem wraps a file for download with index info.
// Implements transfer.WorkItem for BatchExecutor.
type cliDownloadItem struct {
	idx       int    // 0-based index in the batch
	fileID    string // Rescale file ID
	name      string // display name
	size      int64  // decrypted size
	localPath string // resolved output path
	cloudFile *models.CloudFile
	jobFile   *models.JobFile // non-nil for job downloads
}

// FileSize implements transfer.WorkItem.
func (d cliDownloadItem) FileSize() int64 { return d.size }

// storageType names the storage backend an item came from, for the error a
// failed download reports. A job file carries its own metadata; a file-ID
// download carries the CloudFile its metadata fetch returned.
func (d cliDownloadItem) storageType() string {
	if d.jobFile != nil {
		if d.jobFile.Storage != nil {
			return d.jobFile.Storage.StorageType
		}
		return "unknown"
	}
	if d.cloudFile != nil && d.cloudFile.Storage != nil {
		return d.cloudFile.Storage.StorageType
	}
	return "unknown"
}

// executeFileDownload - Common download logic for both files download and download shortcut.
// Fetches all file metadata first, resolves collisions using shared
// paths.ResolveCollisions(), then downloads. This ensures multiple files with
// the same name don't corrupt each other.
func executeFileDownload(
	ctx context.Context,
	fileIDs []string,
	outputDir string,
	maxConcurrent int,
	overwriteAll bool,
	skipAll bool,
	resumeAll bool,
	skipChecksum bool,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	if len(fileIDs) == 0 {
		return fmt.Errorf("at least one file ID is required")
	}

	inthttp.WarmupProxyIfNeeded(ctx, apiClient.GetConfig())
	credentials.GetManager(apiClient).WarmAll(ctx)

	if outputDir == "" {
		outputDir = "."
	}

	logger.Info().
		Int("count", len(fileIDs)).
		Str("outdir", outputDir).
		Msg("Starting file download")

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	fmt.Printf("Fetching metadata for %d file(s)...\n", len(fileIDs))

	// PHASE 1: Fetch all file metadata first to detect filename collisions before downloading.
	// This allows us to detect filename collisions before downloading.
	type fileMetadata struct {
		ID            string
		Name          string
		DecryptedSize int64
		CloudFile     *models.CloudFile
	}
	fileMetadataList := make([]fileMetadata, len(fileIDs))
	metadataErrors := make([]error, len(fileIDs))

	// Use semaphore to limit concurrent metadata fetches
	metaSemaphore := make(chan struct{}, maxConcurrent)
	var metaWg sync.WaitGroup

	for i, fileID := range fileIDs {
		metaWg.Add(1)
		go func(idx int, fid string) {
			defer metaWg.Done()

			// Acquire semaphore
			metaSemaphore <- struct{}{}
			defer func() { <-metaSemaphore }()

			// Get file metadata
			fileInfo, err := apiClient.GetFileInfo(ctx, fid)
			if err != nil {
				metadataErrors[idx] = fmt.Errorf("failed to get file info for %s: %w", fid, err)
				return
			}

			// Validate filename from API to prevent path traversal
			if err := validation.ValidateFilename(fileInfo.Name); err != nil {
				metadataErrors[idx] = fmt.Errorf("invalid filename from API for file %s: %w", fid, err)
				return
			}

			fileMetadataList[idx] = fileMetadata{
				ID:            fid,
				Name:          fileInfo.Name,
				DecryptedSize: fileInfo.DecryptedSize,
				CloudFile:     fileInfo,
			}
		}(i, fileID)
	}
	metaWg.Wait()

	// Check for metadata fetch errors
	var validFiles []fileMetadata
	for i, meta := range fileMetadataList {
		if metadataErrors[i] != nil {
			fmt.Printf("⚠️  %v\n", metadataErrors[i])
			continue
		}
		if meta.ID != "" {
			validFiles = append(validFiles, meta)
		}
	}

	if len(validFiles) == 0 {
		return fmt.Errorf("no valid files to download")
	}

	// PHASE 2: Build file list and resolve collisions using shared utility
	downloadFiles := make([]paths.FileForDownload, len(validFiles))
	for i, meta := range validFiles {
		downloadFiles[i] = paths.FileForDownload{
			FileID:    meta.ID,
			Name:      meta.Name,
			LocalPath: filepath.Join(outputDir, meta.Name),
			Size:      meta.DecryptedSize,
		}
	}

	// Resolve filename collisions using shared utility (consistent with GUI)
	downloadFiles, collisionCount := paths.ResolveCollisions(downloadFiles)
	if collisionCount > 0 {
		fmt.Printf("⚠️  Found %d files with duplicate names. File IDs will be appended to ensure unique downloads.\n", collisionCount)
	}

	// Build map from file ID to resolved path
	fileIDToPath := make(map[string]string)
	fileIDToMeta := make(map[string]fileMetadata)
	for i, df := range downloadFiles {
		fileIDToPath[df.FileID] = df.LocalPath
		fileIDToMeta[df.FileID] = validFiles[i]
	}

	fmt.Printf("Downloading %d file(s) to: %s\n\n", len(validFiles), outputDir)

	// Build work items for the batch runner
	items := make([]cliDownloadItem, len(downloadFiles))
	for i, df := range downloadFiles {
		meta := fileIDToMeta[df.FileID]
		items[i] = cliDownloadItem{
			idx:       i,
			fileID:    df.FileID,
			name:      meta.Name,
			size:      meta.DecryptedSize,
			localPath: df.LocalPath,
			cloudFile: meta.CloudFile,
		}
	}

	return runDownloadBatch(ctx, items, downloadBatchOptions{
		label:            "FILE-DOWNLOAD",
		maxConcurrent:    maxConcurrent,
		skipChecksum:     skipChecksum,
		conflictMode:     initialDownloadConflictMode(overwriteAll, skipAll, resumeAll),
		promptOnConflict: true,
		announcePrepare:  true,
		apiClient:        apiClient,
		logger:           logger,
	})
}

// executeJobDownload - Common download logic for job output files.
// Uses v2 ListJobFiles endpoint (jobs-usage scope) for efficient metadata
// retrieval — no per-file GetFileInfo calls needed.
func executeJobDownload(
	ctx context.Context,
	jobID string,
	outputDir string,
	maxConcurrent int,
	overwriteAll bool,
	skipAll bool,
	resumeAll bool,
	skipChecksum bool,
	filterPatterns []string,
	excludePatterns []string,
	searchTerms []string,
	pathFilterPatterns []string,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	inthttp.WarmupProxyIfNeeded(ctx, apiClient.GetConfig())
	credentials.GetManager(apiClient).WarmAll(ctx)

	// List all job output files
	fmt.Printf("Fetching output files for job %s...\n", jobID)
	logger.Info().Str("job_id", jobID).Msg("Listing job output files")

	allFiles, err := listJobFilesFn(ctx, apiClient, jobID)
	if err != nil {
		return fmt.Errorf("failed to list job files: %w", err)
	}

	if len(allFiles) == 0 {
		fmt.Println("No output files found for this job")
		return nil
	}

	// Apply filters if any are specified
	files := allFiles
	if len(filterPatterns) > 0 || len(excludePatterns) > 0 || len(searchTerms) > 0 || len(pathFilterPatterns) > 0 {
		filterCfg := filter.Config{
			Include:     filterPatterns,
			Exclude:     excludePatterns,
			Search:      searchTerms,
			PathInclude: pathFilterPatterns,
		}
		files = filter.ApplyToJobFiles(allFiles, filterCfg)

		if len(files) == 0 {
			fmt.Println("No files match the specified filters")
			return nil
		}

		if len(files) < len(allFiles) {
			fmt.Printf("Filtered: %d of %d files match filters\n", len(files), len(allFiles))
		}
	}

	// Drop files whose server-supplied name is not a plain filename. Name is the
	// fallback used to build the local path whenever RelativePath is absent or
	// escapes the output directory, so an unchecked name would let the API place
	// a file anywhere on disk. Matches the file-ID download path and the
	// auto-download daemon, which reject the same names.
	files, nameErrs := filterValidJobFiles(files)
	for _, nameErr := range nameErrs {
		fmt.Printf("⚠️  %v\n", nameErr)
	}
	if len(files) == 0 {
		return fmt.Errorf("no valid files to download")
	}

	if outputDir == "" {
		outputDir = "."
	}

	// Pre-compute output paths and detect filename collisions
	// Using shared paths.ResolveCollisions() utility for consistency with GUI and CLI.
	// When multiple files have the same name (e.g., from different job runs), we must
	// give them unique output paths to prevent concurrent download corruption.
	downloadFiles := make([]paths.FileForDownload, len(files))
	for i, file := range files {
		var basePath string
		if file.RelativePath != "" {
			// Validate relative path to prevent escaping output directory
			if validation.ValidatePathInDirectory(file.RelativePath, outputDir) == nil {
				basePath = filepath.Join(outputDir, file.RelativePath)
			} else {
				// Invalid path - use name only
				basePath = filepath.Join(outputDir, file.Name)
			}
		} else {
			basePath = filepath.Join(outputDir, file.Name)
		}
		downloadFiles[i] = paths.FileForDownload{
			FileID:    file.ID,
			Name:      file.Name,
			LocalPath: basePath,
			Size:      file.DecryptedSize,
		}
	}

	// Resolve filename collisions using shared utility
	downloadFiles, collisionCount := paths.ResolveCollisions(downloadFiles)
	if collisionCount > 0 {
		fmt.Printf("⚠️  Found %d files with duplicate names. File IDs will be appended to ensure unique downloads.\n", collisionCount)
	}

	// Build map from file ID to resolved path
	fileOutputPaths := make(map[string]string, len(downloadFiles))
	for _, df := range downloadFiles {
		fileOutputPaths[df.FileID] = df.LocalPath
	}

	logger.Info().
		Int("count", len(files)).
		Str("job_id", jobID).
		Str("outdir", outputDir).
		Msg("Starting job file download")

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	fmt.Printf("Downloading %d file(s) from job %s to: %s\n\n", len(files), jobID, outputDir)

	// Build work items for the batch runner
	items := make([]cliDownloadItem, len(files))
	for i, file := range files {
		outputPath := fileOutputPaths[file.ID]
		if outputPath == "" {
			outputPath = filepath.Join(outputDir, file.Name)
		}
		jf := file // capture loop variable
		items[i] = cliDownloadItem{
			idx:       i,
			fileID:    file.ID,
			name:      file.Name,
			size:      file.DecryptedSize,
			localPath: outputPath,
			jobFile:   &jf,
		}
	}

	return runDownloadBatch(ctx, items, downloadBatchOptions{
		label:          "JOB-DOWNLOAD",
		jobID:          jobID,
		maxConcurrent:  maxConcurrent,
		skipChecksum:   skipChecksum,
		conflictMode:   initialDownloadConflictMode(overwriteAll, skipAll, resumeAll),
		makeParentDirs: true,
		announceSkip:   true,
		apiClient:      apiClient,
		logger:         logger,
	})
}

// downloadBatchOptions carries the parts of a CLI download that differ per
// command. Everything else — progress bars, conflict handling, the worker pool,
// the closing summary — is the same for every download, and lives in
// runDownloadBatch.
type downloadBatchOptions struct {
	label         string // BatchExecutor label for the batch log line
	jobID         string // job downloads only; empty leaves job context out of errors and logs
	maxConcurrent int
	skipChecksum  bool
	conflictMode  DownloadConflictAction

	// promptOnConflict offers the per-file choice when a local file is already
	// in the way. Job downloads leave it off: they run unattended from jobs
	// watch and from the end-to-end workflow, so they act on the mode the flags
	// set and never block on a prompt.
	promptOnConflict bool

	// makeParentDirs creates each item's parent directory before downloading.
	// Job files carry a relative path, so their local path can be nested.
	makeParentDirs bool

	// announceSkip prints a line for each existing file left in place.
	announceSkip bool

	// announcePrepare prints a numbered line per file before its transfer starts.
	announcePrepare bool

	apiClient *api.Client
	logger    *logging.Logger
}

// initialDownloadConflictMode maps the mutually exclusive conflict flags onto the
// resolver's starting mode. With none of them set the mode stays a "once"
// action, which is what makes a prompting command prompt.
func initialDownloadConflictMode(overwriteAll, skipAll, resumeAll bool) DownloadConflictAction {
	switch {
	case overwriteAll:
		return DownloadOverwriteAll
	case skipAll:
		return DownloadSkipAll
	case resumeAll:
		return DownloadResumeAll
	}
	return DownloadSkipOnce
}

// resolveDownloadConflict decides what to do about a file already sitting at
// outputPath and carries out whatever that decision needs before the transfer
// starts: dropping a file that is to be replaced, clearing a leftover of the
// wrong size, or lining up the resume state. It reports skip=true when the file
// on disk stands and the download must not run.
func resolveDownloadConflict(
	resolver *ConflictResolver[DownloadConflictAction],
	item cliDownloadItem,
	outputPath string,
	info os.FileInfo,
	opts downloadBatchOptions,
	w io.Writer,
) (bool, error) {
	var action DownloadConflictAction
	if opts.promptOnConflict {
		chosen, err := resolver.Resolve(func() (DownloadConflictAction, error) {
			return promptDownloadConflict(item.name, outputPath)
		})
		if err != nil {
			return false, fmt.Errorf("conflict prompt failed: %w", err)
		}
		action = chosen
	} else {
		action = resolver.Mode()
	}

	switch action {
	case DownloadSkipOnce, DownloadSkipAll:
		if !existingFileIsComplete(info, item.size) {
			fmt.Fprintf(w, "⚠️  Existing file %s is %d bytes, expected %d — re-downloading\n",
				item.name, info.Size(), item.size)
			if rmErr := os.Remove(outputPath); rmErr != nil {
				return false, fmt.Errorf("failed to remove incomplete existing file: %w", rmErr)
			}
			return false, nil
		}
		if opts.announceSkip {
			fmt.Fprintf(w, "⊘ Skipping existing file: %s\n", item.name)
		}
		return true, nil
	case DownloadAbort:
		return false, fmt.Errorf("download aborted by user")
	case DownloadOverwriteOnce, DownloadOverwriteAll:
		if err := os.Remove(outputPath); err != nil {
			return false, fmt.Errorf("failed to remove existing file: %w", err)
		}
	case DownloadResumeOnce, DownloadResumeAll:
		encryptedPath := outputPath + ".encrypted"
		encryptedInfo, encErr := os.Stat(encryptedPath)
		_, outErr := os.Stat(outputPath)

		minEncryptedSize := item.size + 1
		maxEncryptedSize := item.size + 16

		if encErr == nil && encryptedInfo.Size() >= minEncryptedSize && encryptedInfo.Size() <= maxEncryptedSize {
			fmt.Fprintf(w, "✓ Encrypted file complete (%d bytes), retrying decryption for %s...\n",
				encryptedInfo.Size(), item.name)
			if outErr == nil {
				os.Remove(outputPath)
			}
		} else {
			resumeState, _ := state.LoadDownloadState(outputPath)
			if resumeState != nil {
				if err := state.ValidateDownloadState(resumeState, outputPath); err == nil {
					resumeProgress := state.GetDownloadResumeProgress(resumeState)
					fmt.Fprintf(w, "↻ Resuming download for %s from %.1f%% (%d/%d bytes)...\n",
						item.name, resumeProgress*100, resumeState.DownloadedBytes, resumeState.TotalSize)
					if outErr == nil {
						os.Remove(outputPath)
					}
				} else {
					fmt.Fprintf(w, "Resume state invalid for %s (reason: %v). Starting fresh download...\n",
						item.name, err)
					state.CleanupExpiredDownloadResume(resumeState, outputPath, false)
					os.Remove(outputPath)
				}
			} else {
				if encErr == nil {
					fmt.Fprintf(w, "Encrypted file has unexpected size (%d bytes, expected %d-%d bytes). Starting fresh download for %s...\n",
						encryptedInfo.Size(), minEncryptedSize, maxEncryptedSize, item.name)
					os.Remove(encryptedPath)
				}
				os.Remove(outputPath)
			}
		}
	}

	return false, nil
}

// runDownloadBatch downloads a prepared batch of files: one progress display,
// one worker pool sized from the item sizes, conflict handling per file, and the
// summary the user reads at the end. Callers do the command-specific work first
// — metadata, filtering, collision-resolved local paths — and hand over the
// finished item list.
func runDownloadBatch(ctx context.Context, items []cliDownloadItem, opts downloadBatchOptions) error {
	logger := opts.logger

	// Create DownloadUI for professional progress bars
	downloadUI := progress.NewDownloadUI(len(items))

	// Route this command's logs through the bars — see executeFileUpload for why.
	if downloadUI.IsTerminal() {
		logger = logger.WithOutput(downloadUI.Writer())
	}

	defer downloadUI.Wait()

	downloadedFiles := make([]string, 0, len(items))
	skippedFiles := make([]string, 0)
	var downloadMutex sync.Mutex

	conflictResolver := NewDownloadConflictResolver(opts.conflictMode)

	// Create resource manager from global flags
	resourceMgr := CreateResourceManager()
	transferMgr := transfer.NewManager(resourceMgr)

	cfg := transfer.BatchConfig{
		MaxWorkers:  opts.maxConcurrent,
		ResourceMgr: resourceMgr,
		Label:       opts.label,
	}
	numWorkers := transfer.ComputedWorkers(items, cfg)

	// Download each file concurrently via BatchExecutor
	batchResult := transfer.RunBatch(ctx, items, cfg, func(ctx context.Context, item cliDownloadItem) error {
		outputPath := item.localPath

		if opts.makeParentDirs {
			// Ensure directory exists
			if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
				return fmt.Errorf("failed to create directory for %s: %w", item.name, err)
			}
		}

		// Check if path exists as a directory (name collision with folder)
		if info, statErr := os.Stat(outputPath); statErr == nil && info.IsDir() {
			originalPath := outputPath
			outputPath = outputPath + ".file"
			fmt.Fprintf(downloadUI.Writer(), "⚠️  File '%s' conflicts with directory, downloading as '%s'\n",
				filepath.Base(originalPath), filepath.Base(outputPath))
		}

		// Check if file exists and handle conflict
		if info, statErr := os.Stat(outputPath); statErr == nil && !info.IsDir() {
			skip, err := resolveDownloadConflict(conflictResolver, item, outputPath, info, opts, downloadUI.Writer())
			if err != nil {
				return err
			}
			if skip {
				downloadMutex.Lock()
				skippedFiles = append(skippedFiles, outputPath)
				downloadMutex.Unlock()
				return nil
			}
		}

		if opts.announcePrepare {
			fmt.Fprintf(downloadUI.Writer(), "[%d/%d] Preparing to download %s...\n", item.idx+1, len(items), item.name)
		}

		transferHandle := transferMgr.AllocateTransfer(item.size, numWorkers)

		if transferHandle.GetThreads() > 1 && item.size > 100*1024*1024 {
			fmt.Fprintf(downloadUI.Writer(), "Using %d concurrent threads for %s\n",
				transferHandle.GetThreads(), item.name)
		}

		// The progress and retry callbacks run on different transfer goroutines,
		// so the bar is created under a lock rather than with a bare sync.Once.
		var (
			barMu   sync.Mutex
			fileBar *progress.DownloadFileBar
		)
		ensureBar := func() *progress.DownloadFileBar {
			barMu.Lock()
			defer barMu.Unlock()
			if fileBar == nil {
				fileBar = downloadUI.AddFileBar(item.idx+1, item.fileID, item.name, outputPath, item.size)
			}
			return fileBar
		}

		params := download.DownloadParams{
			LocalPath: outputPath,
			APIClient: opts.apiClient,
			ProgressCallback: func(fraction float64) {
				ensureBar().UpdateProgress(fraction)
			},
			OnRetry: func(ev cloud.RetryEvent) {
				retryReporter(ensureBar(), downloadUI.Writer())(ev)
			},
			TransferHandle: transferHandle,
			SkipChecksum:   opts.skipChecksum,
		}
		// A job file arrives from the v2 listing with its metadata attached, so
		// the download reads it straight off the item. A file-ID download has
		// only the ID and the metadata is fetched downstream.
		if item.jobFile != nil {
			params.FileInfo = item.jobFile.ToCloudFile()
		} else {
			params.FileID = item.fileID
		}

		if err := downloadFileFn(ctx, params); err != nil {
			ensureBar().Complete(err)

			if state.DownloadResumeStateExists(outputPath) {
				fmt.Fprintf(downloadUI.Writer(), "\n💡 Resume state saved for %s. To resume this download, run the same command again.\n", item.name)
			}

			failure := logger.Debug().Str("error", sanitizeErrorString(err.Error())).Str("file_id", item.fileID).Str("file_name", item.name)
			if opts.jobID != "" {
				failure = failure.Str("job_id", opts.jobID)
			}
			failure.Msg("download failed - full error chain for debugging")
			return formatDownloadError(item.name, item.fileID, opts.jobID, item.storageType(), err)
		}

		logger.Info().
			Str("file_id", item.fileID).
			Str("path", outputPath).
			Msg("File downloaded successfully")

		ensureBar().Complete(nil)

		downloadMutex.Lock()
		downloadedFiles = append(downloadedFiles, outputPath)
		downloadMutex.Unlock()
		return nil
	})

	// Collect errors from batch result
	errs := batchResult.Errors

	// Print summary
	if len(errs) > 0 {
		fmt.Printf("\n✓ Successfully downloaded %d file(s)\n", len(downloadedFiles))
		if len(skippedFiles) > 0 {
			fmt.Printf("⊘ Skipped %d file(s)\n", len(skippedFiles))
		}
		fmt.Printf("✗ Failed to download %d file(s)\n", len(errs))
		// Return first error but continue with others (per project objectives)
		return errs[0]
	}

	fmt.Printf("\n✓ Successfully downloaded %d file(s)\n", len(downloadedFiles))
	if len(skippedFiles) > 0 {
		fmt.Printf("⊘ Skipped %d file(s)\n", len(skippedFiles))
	}
	return nil
}

// existingFileIsComplete reports whether a file already on disk can stand in for
// the download. Existence alone is not proof: an interrupted or checksum-failed
// earlier attempt leaves a file at the same path, and skipping it would hand the
// user bad data while reporting success. Size is the same evidence the
// auto-download daemon uses. When the expected size is unknown (metadata omitted
// it) existence is all there is to go on.
func existingFileIsComplete(info os.FileInfo, expectedSize int64) bool {
	if expectedSize <= 0 {
		return true
	}
	return info.Size() == expectedSize
}

// filterValidJobFiles splits job files into those whose server-supplied name is
// a plain filename and one error per rejected file. See executeJobDownload for
// why the name is checked even when the file also carries a relative path.
func filterValidJobFiles(files []models.JobFile) ([]models.JobFile, []error) {
	valid := make([]models.JobFile, 0, len(files))
	var errs []error
	for _, f := range files {
		if err := validation.ValidateFilename(f.Name); err != nil {
			errs = append(errs, fmt.Errorf("invalid filename from API for file %s: %w", f.ID, err))
			continue
		}
		valid = append(valid, f)
	}
	return valid, errs
}

// sanitizeErrorString removes secrets (SAS tokens, access keys, session tokens)
// from error messages to prevent leakage in logs and user-facing output.
func sanitizeErrorString(s string) string {
	s = reSASToken.ReplaceAllString(s, "$1=REDACTED")
	s = reAWSKey.ReplaceAllString(s, "$1=REDACTED")
	s = reAzureKey.ReplaceAllString(s, "AccountKey=REDACTED")
	s = reBearerToken.ReplaceAllString(s, "${1}${2}REDACTED")
	s = reAWSAccessKeyID.ReplaceAllString(s, "[REDACTED_AWS_KEY]")
	return s
}

// classifyDownloadStep inspects the error chain to identify which download step failed.
func classifyDownloadStep(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "failed to list job files"):
		return "listing job files"
	case strings.Contains(s, "credentials") || strings.Contains(s, "credential"):
		return "fetching storage credentials"
	case strings.Contains(s, "failed to get Azure client") || strings.Contains(s, "failed to create"):
		return "creating storage client"
	case strings.Contains(s, "download failed") || strings.Contains(s, "file size"):
		return "downloading from storage"
	case strings.Contains(s, "checksum"):
		return "verifying checksum"
	case strings.Contains(s, "decrypt"):
		return "decrypting file"
	default:
		return "downloading"
	}
}

// formatDownloadError creates a user-friendly error for download failures.
// Collapses the internal error chain to the root cause, includes context
// (file name, IDs, storage type), classifies the failed step, and provides
// actionable guidance. Avoids leaking Go internals or secrets.
func formatDownloadError(fileName, fileID, jobID, storageType string, err error) error {
	step := classifyDownloadStep(err)

	// Extract root cause
	rootCause := err
	for {
		unwrapped := errors.Unwrap(rootCause)
		if unwrapped == nil {
			break
		}
		rootCause = unwrapped
	}

	// Sanitize: remove Go struct/field references from root cause
	rootMsg := rootCause.Error()
	if strings.Contains(rootMsg, "Go struct field") || strings.Contains(rootMsg, "json:") {
		rootMsg = "unexpected credential response format"
	}
	rootMsg = sanitizeErrorString(rootMsg)

	// Build context string
	errCtx := fmt.Sprintf("file %s", fileID)
	if jobID != "" {
		errCtx = fmt.Sprintf("file %s, job %s", fileID, jobID)
	}

	return fmt.Errorf("download failed for %q (%s, storage: %s)\n  Step: %s\n  Cause: %s\n  Try: rerun with --debug for details, or verify you have access to this job",
		fileName, errCtx, storageType, step, rootMsg)
}
