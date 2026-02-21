package pipeline

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/upload"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pathutil"
	"github.com/rescale/rescale-int/internal/pur/state"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/util/tar"
)

// AnalysisResolver abstracts the API call used by resolveAnalysisVersions.
// This allows tests to inject a fast mock instead of calling the real API.
type AnalysisResolver interface {
	GetAnalyses(ctx context.Context) ([]models.Analysis, error)
}

// ProgressCallback is called when job progress updates
type ProgressCallback func(completed, total int, stage string, jobName string)

// LogCallback is called when a log message is generated
type LogCallback func(level string, message string, stage string, jobName string)

// StateChangeCallback is called when a job's state changes
// uploadProgress is 0.0-1.0 and only used for upload stage, 0.0 for other stages
type StateChangeCallback func(jobName, stage, newStatus, jobID, errorMessage string, uploadProgress float64)

// Pipeline orchestrates the parallel tar/upload/job workflow
type Pipeline struct {
	cfg              *config.Config
	apiClient        *api.Client
	analysisResolver AnalysisResolver // For version resolution (defaults to apiClient)
	stateMgr         *state.Manager
	jobs             []models.JobSpec
	tempDir          string
	multiPartMode    bool
	skipTarUpload    bool // true for submit-existing: skip tar/upload, go directly to job creation

	// Shared files attached to all jobs (from --extra-input-files)
	extraInputFilesRaw string   // Raw comma-separated flag value; resolved in ResolveSharedFiles
	sharedFileIDs      []string // Resolved file IDs (after upload of local paths)
	decompressExtras   bool     // Whether to decompress shared files on cluster

	// Cleanup options
	rmTarOnSuccess bool // Delete local tar file after successful upload

	// Resource and transfer management
	resourceMgr *resources.Manager
	transferMgr *transfer.Manager

	// Worker pools
	tarWorkers    int
	uploadWorkers int
	jobWorkers    int

	// Channels
	tarQueue    chan *workItem
	uploadQueue chan *workItem
	jobQueue    chan *workItem

	// Synchronization for safe channel close
	feederDone      chan struct{}
	closeUploadOnce sync.Once
	closeJobOnce    sync.Once

	// Concurrent version resolution
	versionsResolved chan struct{}
	resolvedVersions map[string]string // "analysisCode:displayVersion" -> versionCode

	// Progress tracking
	mu            sync.Mutex
	activeWorkers map[string]int
	completedJobs int
	totalJobs     int

	// Phase timing
	pipelineStart time.Time
	firstTarOnce  sync.Once

	// Callbacks (optional)
	onProgress    ProgressCallback
	onLog         LogCallback
	onStateChange StateChangeCallback
}

type workItem struct {
	index   int
	jobSpec models.JobSpec
	state   *models.JobState
}

// findCommonParent finds the common parent directory of all job directories
func findCommonParent(jobs []models.JobSpec) string {
	if len(jobs) == 0 {
		return "."
	}

	// Get absolute paths and find common parent
	var absPaths []string
	for _, job := range jobs {
		absPath, err := filepath.Abs(job.Directory)
		if err != nil {
			// If we can't get absolute path, use the directory as-is
			absPath = job.Directory
		}
		// Get the parent directory (the directory containing Run_X)
		parent := filepath.Dir(absPath)
		absPaths = append(absPaths, parent)
	}

	// Find common prefix
	if len(absPaths) == 0 {
		return "."
	}

	common := absPaths[0]
	for _, path := range absPaths[1:] {
		// Find common prefix between common and path
		for !strings.HasPrefix(path, common) {
			common = filepath.Dir(common)
			if common == "." || common == "/" {
				return "."
			}
		}
	}

	return common
}

// NewPipeline creates a new pipeline.
// v4.6.0: Added existingState parameter. When non-nil, the pipeline shares
// the caller's state manager instead of creating a duplicate. This fixes the
// dual-state bug where GUI and pipeline each had their own state.Manager,
// causing the GUI to always read empty state. CLI callers pass nil.
func NewPipeline(cfg *config.Config, apiClient *api.Client, jobs []models.JobSpec, stateFile string, multiPartMode bool, existingState *state.Manager, skipTarUpload bool, extraInputFiles string, decompressExtras bool) (*Pipeline, error) {
	// Normalize all job directories to absolute paths at ingress.
	// This prevents CWD-dependent failures when paths were generated
	// with a different working directory (especially GUI mode).
	for i := range jobs {
		if jobs[i].Directory != "" && !filepath.IsAbs(jobs[i].Directory) {
			if abs, err := pathutil.ResolveAbsolutePath(jobs[i].Directory); err == nil {
				jobs[i].Directory = abs
			}
		}
	}

	// Find common parent directory of all jobs - this is where tarballs will be created
	commonParent := findCommonParent(jobs)

	// Use the common parent directly as the temp directory (no subdirectory)
	tempDir := commonParent

	// Ensure the directory exists (it should already, but be safe)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to access tarball directory: %w", err)
	}

	// Use existing state manager if provided (shared with Engine/GUI),
	// otherwise create a new one (CLI paths).
	var stateMgr *state.Manager
	if existingState != nil {
		stateMgr = existingState
	} else {
		stateMgr = state.NewManager(stateFile)
		if err := stateMgr.Load(); err != nil {
			return nil, fmt.Errorf("failed to load state: %w", err)
		}
	}

	// Initialize resource and transfer managers for efficient upload management
	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads: 0, // Auto-detect
		AutoScale:  true,
	})
	transferMgr := transfer.NewManager(resourceMgr)

	p := &Pipeline{
		cfg:              cfg,
		apiClient:        apiClient,
		analysisResolver: apiClient, // Default: real API client satisfies AnalysisResolver
		stateMgr:         stateMgr,
		jobs:             jobs,
		tempDir:          tempDir,
		multiPartMode:    multiPartMode,
		skipTarUpload:    skipTarUpload,
		decompressExtras: decompressExtras,
		resourceMgr:      resourceMgr,
		transferMgr:      transferMgr,
		feederDone:       make(chan struct{}),
		versionsResolved: make(chan struct{}),
		tarWorkers:       cfg.TarWorkers,
		uploadWorkers:    cfg.UploadWorkers,
		jobWorkers:       cfg.JobWorkers,
		// Dynamic queue sizes based on worker count for better throughput
		tarQueue:      make(chan *workItem, cfg.TarWorkers*constants.DefaultQueueMultiplier),
		uploadQueue:   make(chan *workItem, cfg.UploadWorkers*constants.DefaultQueueMultiplier),
		jobQueue:      make(chan *workItem, cfg.JobWorkers*constants.DefaultQueueMultiplier),
		activeWorkers: make(map[string]int),
		totalJobs:     len(jobs),
	}

	// Parse extraInputFiles into sharedFileIDs where possible (id: refs only at construction time;
	// local paths require ctx and are resolved in ResolveSharedFiles during Run).
	if extraInputFiles != "" {
		p.extraInputFilesRaw = extraInputFiles
	}

	return p, nil
}

// SetAnalysisResolver overrides the default AnalysisResolver (the API client).
// Used in tests to inject a mock.
func (p *Pipeline) SetAnalysisResolver(resolver AnalysisResolver) {
	p.analysisResolver = resolver
}

// SetProgressCallback sets the progress callback function
func (p *Pipeline) SetProgressCallback(callback ProgressCallback) {
	p.onProgress = callback
}

// SetLogCallback sets the log callback function
func (p *Pipeline) SetLogCallback(callback LogCallback) {
	p.onLog = callback
}

// SetStateChangeCallback sets the state change callback function
func (p *Pipeline) SetStateChangeCallback(callback StateChangeCallback) {
	p.onStateChange = callback
}

// SetRmTarOnSuccess configures whether to delete local tar files after successful upload.
func (p *Pipeline) SetRmTarOnSuccess(rm bool) {
	p.rmTarOnSuccess = rm
}

// logf logs a message, using callback if available.
// When a callback is set (GUI mode via Engine), the callback path handles
// both log.Printf and EventBus emission, so we skip direct logging to
// avoid duplicate stdout output.
func (p *Pipeline) logf(level, stage, jobName, format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if p.onLog != nil {
		p.onLog(level, message, stage, jobName)
	} else {
		// Only log directly when no callback is set (CLI mode without Engine)
		log.Printf("[%s] [%s] %s", level, stage, message)
	}
}

// reportStateChange reports a state change, using callback if available
func (p *Pipeline) reportStateChange(jobName, stage, newStatus, jobID, errorMessage string, uploadProgress float64) {
	log.Printf("[DEBUG] reportStateChange called: job=%s, stage=%s, status=%s, jobID=%s, err=%s, progress=%.2f",
		jobName, stage, newStatus, jobID, errorMessage, uploadProgress)
	if p.onStateChange != nil {
		p.onStateChange(jobName, stage, newStatus, jobID, errorMessage, uploadProgress)
	} else {
		log.Printf("[DEBUG] reportStateChange: no callback set!")
	}
}

// resolveAnalysisVersions resolves display names (e.g. "CPU") to versionCodes
// (e.g. "0") for all jobs. This catches every entry path: GUI, CLI PUR (CSV),
// legacy saved templates, JSON/SGE imports.
func (p *Pipeline) resolveAnalysisVersions(ctx context.Context) {
	p.logf("INFO", "pipeline", "", "Resolving analysis versions...")
	analyses, err := p.analysisResolver.GetAnalyses(ctx)
	if err != nil {
		p.logf("WARN", "pipeline", "", "Could not fetch analyses for version resolution: %v", err)
		return // best-effort; API will reject invalid versions later
	}

	// Build lookup: analysisCode -> (version display name -> versionCode)
	resolved := make(map[string]string)
	for _, a := range analyses {
		for _, v := range a.Versions {
			if v.Version != "" && v.VersionCode != "" {
				resolved[a.Code+":"+v.Version] = v.VersionCode
			}
		}
	}
	p.resolvedVersions = resolved // Single write before channel close

	// Preflight validation: check that each (analysisCode, analysisVersion) pair
	// is recognized before tar/upload work begins.
	// Build a lookup for validation
	lookup := make(map[string]map[string]string)
	for _, a := range analyses {
		vmap := make(map[string]string)
		for _, v := range a.Versions {
			if v.Version != "" && v.VersionCode != "" {
				vmap[v.Version] = v.VersionCode
			}
		}
		lookup[a.Code] = vmap
	}

	var validationErrors []string
	for _, job := range p.jobs {
		if job.AnalysisVersion == "" {
			continue
		}
		found := false
		for _, a := range analyses {
			if a.Code == job.AnalysisCode {
				for _, v := range a.Versions {
					if v.VersionCode == job.AnalysisVersion || v.Version == job.AnalysisVersion {
						found = true
						break
					}
				}
				break
			}
		}
		if !found {
			validationErrors = append(validationErrors, fmt.Sprintf("%s: analysis %q version %q not found",
				job.JobName, job.AnalysisCode, job.AnalysisVersion))
		}
	}
	if len(validationErrors) > 0 {
		for _, e := range validationErrors {
			p.logf("ERROR", "pipeline", "", "PREFLIGHT: %s", e)
		}
		// Log prominently but don't block — the API will reject invalid versions
		// and the error messages above give users clear diagnosis.
		p.logf("WARN", "pipeline", "", "%d job(s) have unrecognized analysis versions - these will likely fail at job creation", len(validationErrors))
	}
}

// ResolveSharedFiles uploads local paths and collects file IDs from the
// --extra-input-files flag. Called once at the start of Run() so the
// resolved IDs are available for every job.
func (p *Pipeline) ResolveSharedFiles(ctx context.Context) error {
	if p.extraInputFilesRaw == "" {
		return nil
	}
	items := strings.Split(p.extraInputFilesRaw, ",")
	seen := make(map[string]bool) // dedupe

	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		if strings.HasPrefix(item, "id:") {
			// Pre-uploaded file ID
			fileID := strings.TrimPrefix(item, "id:")
			if !seen[fileID] {
				p.sharedFileIDs = append(p.sharedFileIDs, fileID)
				seen[fileID] = true
			}
		} else {
			// Local path — upload it
			absPath, err := filepath.Abs(item)
			if err != nil {
				return fmt.Errorf("invalid path %s: %w", item, err)
			}
			if seen[absPath] {
				continue // Already uploaded
			}

			p.logf("INFO", "pipeline", "", "Uploading shared file: %s", absPath)
			cloudFile, err := upload.UploadFile(ctx, upload.UploadParams{
				LocalPath:    absPath,
				APIClient:    p.apiClient,
				OutputWriter: io.Discard,
			})
			if err != nil {
				return fmt.Errorf("failed to upload shared file %s: %w", item, err)
			}
			p.sharedFileIDs = append(p.sharedFileIDs, cloudFile.ID)
			seen[absPath] = true
			p.logf("INFO", "pipeline", "", "Shared file uploaded: %s -> %s", absPath, cloudFile.ID)
		}
	}
	return nil
}

// Run executes the pipeline
func (p *Pipeline) Run(ctx context.Context) error {
	p.pipelineStart = time.Now()
	p.logf("INFO", "pipeline", "", "Starting pipeline with %d jobs", p.totalJobs)
	p.logf("INFO", "pipeline", "", "Workers: tar=%d upload=%d job=%d", p.tarWorkers, p.uploadWorkers, p.jobWorkers)

	// Resolve shared files synchronously (fast: parses IDs or uploads 1-2 files)
	sharedStart := time.Now()
	if err := p.ResolveSharedFiles(ctx); err != nil {
		return fmt.Errorf("failed to resolve shared files: %w", err)
	}
	p.logf("INFO", "pipeline", "", "Shared files resolved in %v", time.Since(sharedStart))

	// Resolve analysis versions concurrently - only job workers need this
	go func() {
		defer close(p.versionsResolved)
		vStart := time.Now()
		p.resolveAnalysisVersions(ctx)
		p.logf("INFO", "pipeline", "", "Analysis versions resolved in %v", time.Since(vStart))
	}()

	var wg sync.WaitGroup

	// Start worker pools IMMEDIATELY (tar/upload don't need version info)
	p.logf("INFO", "pipeline", "", "Workers started at %v after pipeline entry", time.Since(p.pipelineStart))
	for i := 0; i < p.tarWorkers; i++ {
		wg.Add(1)
		go p.tarWorker(ctx, &wg, i)
	}

	for i := 0; i < p.uploadWorkers; i++ {
		wg.Add(1)
		go p.uploadWorker(ctx, &wg, i)
	}

	for i := 0; i < p.jobWorkers; i++ {
		wg.Add(1)
		go p.jobWorker(ctx, &wg, i)
	}

	// Start progress reporter
	stopProgress := make(chan struct{})
	go p.progressReporter(stopProgress)

	// Feed work items to tar queue (v4.6.0: context-aware to support cancellation)
	go func() {
		defer close(p.tarQueue)
		defer close(p.feederDone)
		for i, jobSpec := range p.jobs {
			index := i + 1
			state := p.stateMgr.GetState(index)

			// Initialize state if needed
			if state == nil {
				state = p.stateMgr.InitializeState(index, jobSpec.JobName, jobSpec.Directory)
				p.stateMgr.Save()
			}

			item := &workItem{
				index:   index,
				jobSpec: jobSpec,
				state:   state,
			}

			// v4.6.0: submit-existing mode — skip tar/upload, go directly to job creation
			if p.skipTarUpload && state.TarStatus != "success" {
				item.state.TarStatus = "skipped"
				item.state.UploadStatus = "skipped"
				p.stateMgr.UpdateState(item.state)
				p.reportStateChange(item.state.JobName, "tar", "skipped", "", "", 0.0)
				p.reportStateChange(item.state.JobName, "upload", "skipped", "", "", 0.0)
				select {
				case <-ctx.Done():
					return
				case p.jobQueue <- item:
				}
				continue
			}

			// v4.6.8: If job has pre-specified input file IDs (single job remoteFiles/localFiles mode),
			// skip tar/upload and go directly to job creation.
			// Gated on Directory=="" to avoid interfering with file-scan mode where
			// InputFiles contains local paths but Directory is also set.
			if jobSpec.Directory == "" && len(jobSpec.InputFiles) > 0 {
				item.state.TarStatus = "skipped"
				item.state.UploadStatus = "skipped"
				p.stateMgr.UpdateState(item.state)
				p.reportStateChange(item.state.JobName, "tar", "skipped", "", "", 0.0)
				p.reportStateChange(item.state.JobName, "upload", "skipped", "", "", 0.0)
				select {
				case <-ctx.Done():
					return
				case p.jobQueue <- item:
				}
				continue
			}

			// Determine which queue to start in based on current state
			if state.TarStatus == "success" && state.UploadStatus == "success" && state.JobID != "" {
				// Already uploaded and job created, check if we need to submit
				if state.SubmitStatus == "pending" && shouldSubmit(jobSpec.SubmitMode) {
					select {
					case <-ctx.Done():
						return
					case p.jobQueue <- item:
					}
				}
			} else if state.TarStatus == "success" && state.UploadStatus == "success" {
				// Already uploaded, need to create job
				select {
				case <-ctx.Done():
					return
				case p.jobQueue <- item:
				}
			} else if state.TarStatus == "success" {
				// Already tarred, need to upload
				select {
				case <-ctx.Done():
					return
				case p.uploadQueue <- item:
				}
			} else {
				// Need to tar
				select {
				case <-ctx.Done():
					return
				case p.tarQueue <- item:
				}
			}
		}
	}()

	// Wait for all workers to complete
	wg.Wait()
	close(stopProgress)

	p.logf("INFO", "pipeline", "", "Pipeline completed: %d/%d jobs finished in %v",
		p.completedJobs, p.totalJobs, time.Since(p.pipelineStart))

	return nil
}

// tarWorker processes tar operations.
// v4.6.0: Rewritten with select on ctx.Done() to support cancellation.
func (p *Pipeline) tarWorker(ctx context.Context, wg *sync.WaitGroup, workerID int) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			goto shutdown
		case item, ok := <-p.tarQueue:
			if !ok {
				goto shutdown
			}

			p.setActiveWorker("tar", 1)

			// Belt-and-suspenders normalization for paths from legacy CSV/state files
			if !filepath.IsAbs(item.jobSpec.Directory) {
				if abs, err := pathutil.ResolveAbsolutePath(item.jobSpec.Directory); err == nil {
					item.jobSpec.Directory = abs
					p.logf("WARN", "tar", item.state.JobName,
						"Normalized relative directory to absolute: %s", abs)
				}
			}

			// Check if already tarred
			if item.state.TarStatus == "success" {
				exists, err := tar.ValidateTarExists(item.state.TarPath)
				if err != nil {
					p.logf("WARN", "tar", item.state.JobName,
						"Error checking existing tar file %s: %v (will recreate)", item.state.TarPath, err)
				} else if exists {
					p.setActiveWorker("tar", -1)
					select {
					case <-ctx.Done():
						goto shutdown
					case p.uploadQueue <- item:
					}
					continue
				}
			}

			// v4.6.0: Resolve tar source directory, applying TarSubpath if set
			tarSourceDir := item.jobSpec.Directory
			if item.jobSpec.TarSubpath != "" {
				tarSourceDir = filepath.Join(item.jobSpec.Directory, item.jobSpec.TarSubpath)
				// Path traversal guard: prevent ../ escape outside run directory
				absSource, errAbs := filepath.Abs(tarSourceDir)
				absRunDir, errRun := filepath.Abs(item.jobSpec.Directory)
				rel, errRel := filepath.Rel(absRunDir, absSource)
				if errAbs != nil || errRun != nil || errRel != nil || strings.HasPrefix(rel, "..") {
					p.logf("ERROR", "tar", item.state.JobName,
						"REJECTED: tar subpath '%s' escapes run directory", item.jobSpec.TarSubpath)
					item.state.TarStatus = "failed"
					item.state.SubmitStatus = "failed"
					item.state.ErrorMessage = fmt.Sprintf("tar subpath '%s' escapes run directory", item.jobSpec.TarSubpath)
					p.stateMgr.UpdateState(item.state)
					p.reportStateChange(item.state.JobName, "tar", "failed", "", item.state.ErrorMessage, 0.0)
					p.setActiveWorker("tar", -1)
					continue
				}
				// Verify subpath exists
				if _, errStat := os.Stat(tarSourceDir); os.IsNotExist(errStat) {
					p.logf("ERROR", "tar", item.state.JobName,
						"Tar subpath '%s' does not exist in %s", item.jobSpec.TarSubpath, item.jobSpec.Directory)
					item.state.TarStatus = "failed"
					item.state.SubmitStatus = "failed"
					item.state.ErrorMessage = fmt.Sprintf("tar subpath '%s' does not exist in %s", item.jobSpec.TarSubpath, item.jobSpec.Directory)
					p.stateMgr.UpdateState(item.state)
					p.reportStateChange(item.state.JobName, "tar", "failed", "", item.state.ErrorMessage, 0.0)
					p.setActiveWorker("tar", -1)
					continue
				}
			}

			tarPath := tar.GenerateTarPath(tarSourceDir, p.tempDir, p.cfg.TarCompression)
			item.state.TarPath = tarPath

			p.reportStateChange(item.state.JobName, "tar", "in_progress", "", "", 0.0)

			p.firstTarOnce.Do(func() {
				p.logf("INFO", "tar", item.state.JobName,
					"First tarball creation started at %v after pipeline entry",
					time.Since(p.pipelineStart))
			})

			p.logf("INFO", "tar", item.state.JobName, "Creating archive: %s -> %s", tarSourceDir, tarPath)

			var err error
			if len(p.cfg.IncludePatterns) > 0 || len(p.cfg.ExcludePatterns) > 0 || p.cfg.FlattenTar {
				if len(p.cfg.IncludePatterns) > 0 {
					p.logf("INFO", "tar", item.state.JobName, "Include patterns: %v", p.cfg.IncludePatterns)
				}
				if len(p.cfg.ExcludePatterns) > 0 {
					p.logf("INFO", "tar", item.state.JobName, "Exclude patterns: %v", p.cfg.ExcludePatterns)
				}
				if p.cfg.FlattenTar {
					p.logf("INFO", "tar", item.state.JobName, "Flatten mode enabled")
				}
				err = tar.CreateTarGzWithOptions(tarSourceDir, tarPath, p.multiPartMode,
					p.cfg.IncludePatterns, p.cfg.ExcludePatterns, p.cfg.FlattenTar, p.cfg.TarCompression)
			} else {
				err = tar.CreateTarGz(tarSourceDir, tarPath, p.multiPartMode, p.cfg.TarCompression)
			}

			if err != nil {
				p.logf("ERROR", "tar", item.state.JobName, "Failed: %v", err)
				item.state.TarStatus = "failed"
				item.state.SubmitStatus = "failed"
				item.state.ErrorMessage = err.Error()
				p.stateMgr.UpdateState(item.state)
				p.reportStateChange(item.state.JobName, "tar", "failed", "", err.Error(), 0.0)
				p.setActiveWorker("tar", -1)
				continue
			}

			item.state.TarStatus = "success"
			item.state.ErrorMessage = ""
			p.stateMgr.UpdateState(item.state)
			p.reportStateChange(item.state.JobName, "tar", "completed", "", "", 0.0)
			p.logf("INFO", "tar", item.state.JobName, "Success")

			p.setActiveWorker("tar", -1)
			select {
			case <-ctx.Done():
				goto shutdown
			case p.uploadQueue <- item:
			}
		}
	}

shutdown:
	p.mu.Lock()
	p.activeWorkers["tar_finished"]++
	if p.activeWorkers["tar_finished"] == p.tarWorkers {
		go func() {
			<-p.feederDone
			p.closeUploadOnce.Do(func() { close(p.uploadQueue) })
		}()
	}
	p.mu.Unlock()
}

// uploadWorker processes upload operations.
// v4.6.0: Rewritten with select on ctx.Done() to support cancellation.
func (p *Pipeline) uploadWorker(ctx context.Context, wg *sync.WaitGroup, workerID int) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			goto shutdown
		case item, ok := <-p.uploadQueue:
			if !ok {
				goto shutdown
			}

			p.setActiveWorker("upload", 1)

			// Check if already uploaded
			if item.state.UploadStatus == "success" && item.state.FileID != "" {
				p.setActiveWorker("upload", -1)
				select {
				case <-ctx.Done():
					goto shutdown
				case p.jobQueue <- item:
				}
				continue
			}

			p.logf("INFO", "upload", item.state.JobName, "Uploading: %s", item.state.TarPath)
			p.reportStateChange(item.state.JobName, "upload", "in_progress", "", "", 0.0)

			// v4.6.5: Per-upload proxy warmup for Basic proxy mode.
			// Prevents proxy session expiry during long batch runs (matching old PUR behavior).
			if strings.ToLower(p.cfg.ProxyMode) == "basic" {
				if err := inthttp.WarmupProxyConnection(ctx, p.cfg); err != nil {
					p.logf("WARN", "upload", item.state.JobName, "Proxy warmup failed: %v (continuing anyway)", err)
				}
			}

			var cloudFile *models.CloudFile
			var err error
			maxRetries := p.cfg.MaxRetries
			if maxRetries < 1 {
				maxRetries = 1
			}

			fileInfo, err := os.Stat(item.state.TarPath)
			if err != nil {
				p.logf("ERROR", "upload", item.state.JobName, "Failed to stat file: %v", err)
				item.state.UploadStatus = "failed"
				item.state.SubmitStatus = "failed"
				item.state.ErrorMessage = err.Error()
				p.stateMgr.UpdateState(item.state)
				p.reportStateChange(item.state.JobName, "upload", "failed", "", err.Error(), 0.0)
				p.setActiveWorker("upload", -1)
				continue
			}

			transferHandle := p.transferMgr.AllocateTransfer(fileInfo.Size(), 1)

			for attempt := 1; attempt <= maxRetries; attempt++ {
				progressCallback := func(progress float64) {
					p.reportStateChange(item.state.JobName, "upload", "in_progress", "", "", progress)
				}

				cloudFile, err = upload.UploadFile(ctx, upload.UploadParams{
					LocalPath:        item.state.TarPath,
					FolderID:         "",
					APIClient:        p.apiClient,
					ProgressCallback: progressCallback,
					TransferHandle:   transferHandle,
					OutputWriter:     io.Discard,
				})

				if err == nil {
					break
				}

				errStr := err.Error()
				isTimeout := strings.Contains(errStr, "timeout") ||
					strings.Contains(errStr, "SocketTimeoutException") ||
					strings.Contains(errStr, "connection reset") ||
					strings.Contains(errStr, "EOF")

				if isTimeout && attempt < maxRetries {
					p.logf("WARN", "upload", item.state.JobName, "Detected proxy timeout, forcing fresh auth and retrying...")
					// v4.6.5: Warmup proxy on retry to re-establish session
					if strings.ToLower(p.cfg.ProxyMode) == "basic" {
						_ = inthttp.WarmupProxyConnection(ctx, p.cfg)
					}
					if strings.ToLower(p.cfg.ProxyMode) == "basic" || strings.ToLower(p.cfg.ProxyMode) == "ntlm" {
						p.logf("INFO", "upload", item.state.JobName, "Waiting 2 seconds before retry...")
						time.Sleep(2 * time.Second)
					}
					p.logf("INFO", "upload", item.state.JobName, "Retry attempt %d/%d", attempt+1, maxRetries)
					continue
				}

				break
			}

			if err != nil {
				if strings.Contains(err.Error(), "timeout") {
					p.logf("ERROR", "upload", item.state.JobName, "Failed after %d retries: %v", maxRetries, err)
				} else {
					p.logf("ERROR", "upload", item.state.JobName, "Failed: %v", err)
				}
				item.state.UploadStatus = "failed"
				item.state.SubmitStatus = "failed"
				item.state.ErrorMessage = err.Error()
				p.stateMgr.UpdateState(item.state)
				p.reportStateChange(item.state.JobName, "upload", "failed", "", err.Error(), 0.0)
				p.setActiveWorker("upload", -1)
				transferHandle.Complete()
				continue
			}

			item.state.FileID = cloudFile.ID
			item.state.UploadStatus = "success"
			item.state.ErrorMessage = ""
			p.stateMgr.UpdateState(item.state)
			p.reportStateChange(item.state.JobName, "upload", "completed", "", "", 1.0)
			p.logf("INFO", "upload", item.state.JobName, "Success: File ID %s", cloudFile.ID)

			// Clean up tar file if requested
			if p.rmTarOnSuccess && item.state.TarPath != "" {
				if err := os.Remove(item.state.TarPath); err != nil {
					p.logf("WARN", "upload", item.state.JobName, "Failed to remove tar file %s: %v", item.state.TarPath, err)
				} else {
					p.logf("INFO", "upload", item.state.JobName, "Removed tar file: %s", item.state.TarPath)
				}
			}

			transferHandle.Complete()

			p.setActiveWorker("upload", -1)
			select {
			case <-ctx.Done():
				goto shutdown
			case p.jobQueue <- item:
			}
		}
	}

shutdown:
	p.mu.Lock()
	p.activeWorkers["upload_finished"]++
	if p.activeWorkers["upload_finished"] == p.uploadWorkers {
		go func() {
			<-p.feederDone
			p.closeJobOnce.Do(func() { close(p.jobQueue) })
		}()
	}
	p.mu.Unlock()
}

// jobWorker processes job creation and submission
func (p *Pipeline) jobWorker(ctx context.Context, wg *sync.WaitGroup, workerID int) {
	defer wg.Done()

	// v4.6.0: Rewritten with select on ctx.Done() to support cancellation.
	for {
		select {
		case <-ctx.Done():
			return
		case item, ok := <-p.jobQueue:
			if !ok {
				return
			}

			p.setActiveWorker("job", 1)

			if item.state.JobID != "" && item.state.SubmitStatus == "success" {
				p.setActiveWorker("job", -1)
				p.incrementCompleted()
				continue
			}

			if item.state.JobID == "" {
				// Wait for version resolution (concurrent with tar/upload)
				select {
				case <-ctx.Done():
					p.setActiveWorker("job", -1)
					continue
				case <-p.versionsResolved:
				}

				// Apply resolved version
				if p.resolvedVersions != nil {
					key := item.jobSpec.AnalysisCode + ":" + item.jobSpec.AnalysisVersion
					if code, ok := p.resolvedVersions[key]; ok {
						p.logf("INFO", "job", item.state.JobName,
							"Resolved version %q -> %q", item.jobSpec.AnalysisVersion, code)
						item.jobSpec.AnalysisVersion = code
					}
				}

				p.logf("INFO", "job", item.state.JobName, "Creating job: %s", item.jobSpec.JobName)
				p.reportStateChange(item.state.JobName, "create", "in_progress", "", "", 0.0)

				// v4.6.8: When InputFiles are pre-specified (single job remoteFiles/localFiles mode),
				// use them directly instead of the FileID from upload stage.
				var fileIDs []string
				if item.jobSpec.Directory == "" && len(item.jobSpec.InputFiles) > 0 {
					fileIDs = item.jobSpec.InputFiles
				} else {
					fileIDs = []string{item.state.FileID}
				}
				jobReq, err := BuildJobRequest(item.jobSpec, fileIDs, p.sharedFileIDs, p.decompressExtras)
				if err != nil {
					p.logf("ERROR", "job", item.state.JobName, "Failed to build request: %v", err)
					item.state.SubmitStatus = "failed"
					item.state.ErrorMessage = err.Error()
					p.stateMgr.UpdateState(item.state)
					p.reportStateChange(item.state.JobName, "create", "failed", "", err.Error(), 0.0)
					p.setActiveWorker("job", -1)
					continue
				}

				jobResp, err := p.apiClient.CreateJob(ctx, *jobReq)
				if err != nil {
					p.logf("ERROR", "job", item.state.JobName, "Failed to create: %v", err)
					item.state.SubmitStatus = "failed"
					item.state.ErrorMessage = err.Error()
					p.stateMgr.UpdateState(item.state)
					p.reportStateChange(item.state.JobName, "create", "failed", "", err.Error(), 0.0)
					p.setActiveWorker("job", -1)
					continue
				}

				item.state.JobID = jobResp.ID
				p.stateMgr.UpdateState(item.state)
				p.reportStateChange(item.state.JobName, "create", "completed", jobResp.ID, "", 0.0)
				p.logf("INFO", "job", item.state.JobName, "Created: Job ID %s", jobResp.ID)

				// v4.6.5: OrgCode project assignment (org-scoped endpoint)
				orgCode := item.jobSpec.OrgCode
				if orgCode == "" {
					orgCode = p.cfg.OrgCode
				}
				if orgCode != "" && item.jobSpec.ProjectID != "" {
					maxAssignRetries := 3
					for assignAttempt := 1; assignAttempt <= maxAssignRetries; assignAttempt++ {
						err := p.apiClient.AssignProjectToJob(ctx, orgCode, item.state.JobID, item.jobSpec.ProjectID)
						if err == nil {
							p.logf("INFO", "job", item.state.JobName, "Project assignment successful (org=%s)", orgCode)
							break
						}
						p.logf("WARN", "job", item.state.JobName, "Project assignment attempt %d failed: %v", assignAttempt, err)
						if assignAttempt < maxAssignRetries {
							time.Sleep(time.Duration(min(60, 1<<uint(assignAttempt))) * time.Second)
						}
					}
					// Non-fatal: job continues even if assignment fails
				}
			}

			if shouldSubmit(item.jobSpec.SubmitMode) && item.state.SubmitStatus != "success" {
				p.logf("INFO", "job", item.state.JobName, "Submitting job %s", item.state.JobID)
				p.reportStateChange(item.state.JobName, "submit", "in_progress", item.state.JobID, "", 0.0)

				err := p.apiClient.SubmitJob(ctx, item.state.JobID)
				if err != nil {
					p.logf("ERROR", "job", item.state.JobName, "Failed to submit: %v", err)
					item.state.SubmitStatus = "failed"
					item.state.ErrorMessage = err.Error()
					p.stateMgr.UpdateState(item.state)
					p.reportStateChange(item.state.JobName, "submit", "failed", item.state.JobID, err.Error(), 0.0)
					p.setActiveWorker("job", -1)
					continue
				}

				item.state.SubmitStatus = "success"
				p.stateMgr.UpdateState(item.state)
				p.reportStateChange(item.state.JobName, "submit", "completed", item.state.JobID, "", 0.0)
				p.logf("INFO", "job", item.state.JobName, "Submitted successfully")
			} else if item.state.SubmitStatus != "success" && item.state.SubmitStatus != "failed" {
				item.state.SubmitStatus = "skipped"
				p.stateMgr.UpdateState(item.state)
			}

			p.setActiveWorker("job", -1)
			p.incrementCompleted()
		}
	}
}

// BuildJobRequest builds a job request from job spec.
// This is the single source of truth for JobSpec -> JobRequest conversion.
// Used by both GUI (single job tab) and PUR pipeline.
// fileIDs are the primary input files; ExtraInputFileIDs from spec are also included.
// sharedFileIDs are pipeline-level shared files (from --extra-input-files) attached to every job.
// decompressExtras controls whether those shared files are decompressed on the cluster.
func BuildJobRequest(spec models.JobSpec, fileIDs []string, sharedFileIDs []string, decompressExtras bool) (*models.JobRequest, error) {
	// Parse license settings (optional - empty string is valid)
	var licenseEnv map[string]string
	if spec.LicenseSettings != "" {
		var err error
		licenseEnv, err = config.ParseLicenseJSON(spec.LicenseSettings)
		if err != nil {
			return nil, fmt.Errorf("invalid license settings: %w", err)
		}
	}

	// Build input files from provided file IDs
	inputFiles := make([]models.InputFileRequest, 0, len(fileIDs))
	for _, id := range fileIDs {
		if id != "" {
			inputFiles = append(inputFiles, models.InputFileRequest{
				ID:         id,
				Decompress: !spec.NoDecompress,
			})
		}
	}

	// Add extra input files if specified in spec (per-job)
	if spec.ExtraInputFileIDs != "" {
		extraIDs := strings.Split(spec.ExtraInputFileIDs, ",")
		for _, id := range extraIDs {
			id = strings.TrimSpace(id)
			if id != "" {
				inputFiles = append(inputFiles, models.InputFileRequest{
					ID:         id,
					Decompress: true,
				})
			}
		}
	}

	// Add shared files (pipeline-level --extra-input-files), deduplicating
	// against files already in the list.
	if len(sharedFileIDs) > 0 {
		seen := make(map[string]bool, len(inputFiles))
		for _, f := range inputFiles {
			seen[f.ID] = true
		}
		for _, id := range sharedFileIDs {
			if !seen[id] {
				inputFiles = append(inputFiles, models.InputFileRequest{
					ID:         id,
					Decompress: decompressExtras,
				})
				seen[id] = true
			}
		}
	}

	// Build job request
	jobReq := &models.JobRequest{
		Name: spec.JobName,
		JobAnalyses: []models.JobAnalysisRequest{
			{
				Command: spec.Command,
				Analysis: models.AnalysisRequest{
					Code:    spec.AnalysisCode,
					Version: spec.AnalysisVersion,
				},
				Hardware: models.HardwareRequest{
					CoreType: models.CoreTypeRequest{
						Code: spec.CoreType,
					},
					CoresPerSlot: spec.CoresPerSlot,
					Slots:        spec.Slots,
					Walltime:     int(spec.WalltimeHours * 3600), // Convert hours to seconds
				},
				InputFiles:                 inputFiles,
				EnvVars:                    licenseEnv,
				UseRescaleLicense:          false,
				OnDemandLicenseSeller:      nil,
				UserDefinedLicenseSettings: nil,
			},
		},
		IsLowPriority: spec.IsLowPriority,
		Tags:          spec.Tags,
		ProjectID:     spec.ProjectID,
	}

	if spec.OnDemandLicenseSeller != "" {
		jobReq.JobAnalyses[0].OnDemandLicenseSeller = &spec.OnDemandLicenseSeller
	}

	// v3.6.1: Add automations from JobSpec
	if len(spec.Automations) > 0 {
		jobReq.JobAutomations = make([]models.JobAutomationRequest, len(spec.Automations))
		for i, autoID := range spec.Automations {
			jobReq.JobAutomations[i] = models.JobAutomationRequest{
				Automation:           models.AutomationRef{ID: autoID},
				EnvironmentVariables: map[string]string{},
			}
		}
	}

	return jobReq, nil
}

// NormalizeSubmitMode converts UI mode strings to canonical pipeline values.
// Returns "submit", "create_only", or error for unrecognized modes.
// v4.6.0: Single source of truth for mode normalization — used by both
// the pipeline (shouldSubmit) and GUI validation (ValidateJobSpec).
func NormalizeSubmitMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "yes", "true", "submit", "create_and_submit":
		return "submit", nil
	case "no", "false", "create_only", "draft":
		return "create_only", nil
	default:
		return "", fmt.Errorf("unrecognized submitMode: %q", mode)
	}
}

// shouldSubmit determines if a job should be submitted based on submit mode
func shouldSubmit(submitMode string) bool {
	normalized, err := NormalizeSubmitMode(submitMode)
	if err != nil {
		return false
	}
	return normalized == "submit"
}

// setActiveWorker updates the active worker count
func (p *Pipeline) setActiveWorker(workerType string, delta int) {
	p.mu.Lock()
	p.activeWorkers[workerType] += delta
	if p.activeWorkers[workerType] < 0 {
		p.activeWorkers[workerType] = 0
	}
	p.mu.Unlock()
}

// incrementCompleted increments completed job count
func (p *Pipeline) incrementCompleted() {
	p.mu.Lock()
	p.completedJobs++
	p.mu.Unlock()
}

// progressReporter reports progress every 10 seconds
func (p *Pipeline) progressReporter(stop chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.mu.Lock()
			tarActive := p.activeWorkers["tar"]
			uploadActive := p.activeWorkers["upload"]
			jobActive := p.activeWorkers["job"]
			completed := p.completedJobs
			p.mu.Unlock()

			p.logf("INFO", "pipeline", "", "Active workers: tar=%d upload=%d job=%d | Completed: %d/%d",
				tarActive, uploadActive, jobActive, completed, p.totalJobs)

		case <-stop:
			return
		}
	}
}
