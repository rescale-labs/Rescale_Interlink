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
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/state"
	"github.com/rescale/rescale-int/internal/util/tar"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
)

// ProgressCallback is called when job progress updates
type ProgressCallback func(completed, total int, stage string, jobName string)

// LogCallback is called when a log message is generated
type LogCallback func(level string, message string, stage string, jobName string)

// StateChangeCallback is called when a job's state changes
// uploadProgress is 0.0-1.0 and only used for upload stage, 0.0 for other stages
type StateChangeCallback func(jobName, stage, newStatus, jobID, errorMessage string, uploadProgress float64)

// Pipeline orchestrates the parallel tar/upload/job workflow
type Pipeline struct {
	cfg           *config.Config
	apiClient     *api.Client
	stateMgr      *state.Manager
	jobs          []models.JobSpec
	tempDir       string
	multiPartMode bool

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

	// Progress tracking
	mu            sync.Mutex
	activeWorkers map[string]int
	completedJobs int
	totalJobs     int

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

// NewPipeline creates a new pipeline
func NewPipeline(cfg *config.Config, apiClient *api.Client, jobs []models.JobSpec, stateFile string, multiPartMode bool) (*Pipeline, error) {
	// Find common parent directory of all jobs - this is where tarballs will be created
	commonParent := findCommonParent(jobs)

	// Use the common parent directly as the temp directory (no subdirectory)
	tempDir := commonParent

	// Ensure the directory exists (it should already, but be safe)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to access tarball directory: %w", err)
	}

	// Initialize state manager
	stateMgr := state.NewManager(stateFile)
	if err := stateMgr.Load(); err != nil {
		return nil, fmt.Errorf("failed to load state: %w", err)
	}

	// Initialize resource and transfer managers for efficient upload management
	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads: 0, // Auto-detect
		AutoScale:  true,
	})
	transferMgr := transfer.NewManager(resourceMgr)

	return &Pipeline{
		cfg:           cfg,
		apiClient:     apiClient,
		stateMgr:      stateMgr,
		jobs:          jobs,
		tempDir:       tempDir,
		multiPartMode: multiPartMode,
		resourceMgr:   resourceMgr,
		transferMgr:   transferMgr,
		tarWorkers:    cfg.TarWorkers,
		uploadWorkers: cfg.UploadWorkers,
		jobWorkers:    cfg.JobWorkers,
		// Dynamic queue sizes based on worker count for better throughput
		tarQueue:      make(chan *workItem, cfg.TarWorkers*constants.DefaultQueueMultiplier),
		uploadQueue:   make(chan *workItem, cfg.UploadWorkers*constants.DefaultQueueMultiplier),
		jobQueue:      make(chan *workItem, cfg.JobWorkers*constants.DefaultQueueMultiplier),
		activeWorkers: make(map[string]int),
		totalJobs:     len(jobs),
	}, nil
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

// logf logs a message, using callback if available
func (p *Pipeline) logf(level, stage, jobName, format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if p.onLog != nil {
		p.onLog(level, message, stage, jobName)
	}
	// Also log to standard logger
	log.Printf("[%s] [%s] %s", level, stage, message)
}

// reportProgress reports progress, using callback if available
func (p *Pipeline) reportProgress(stage, jobName string) {
	if p.onProgress != nil {
		p.mu.Lock()
		completed := p.completedJobs
		total := p.totalJobs
		p.mu.Unlock()
		p.onProgress(completed, total, stage, jobName)
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

// Run executes the pipeline
func (p *Pipeline) Run(ctx context.Context) error {
	p.logf("INFO", "pipeline", "", "Starting pipeline with %d jobs", p.totalJobs)
	p.logf("INFO", "pipeline", "", "Workers: tar=%d upload=%d job=%d", p.tarWorkers, p.uploadWorkers, p.jobWorkers)

	var wg sync.WaitGroup

	// Start worker pools
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

	// Feed work items to tar queue
	go func() {
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

			// Determine which queue to start in based on current state
			if state.TarStatus == "success" && state.UploadStatus == "success" && state.JobID != "" {
				// Already uploaded and job created, check if we need to submit
				if state.SubmitStatus == "pending" && shouldSubmit(jobSpec.SubmitMode) {
					p.jobQueue <- item
				}
			} else if state.TarStatus == "success" && state.UploadStatus == "success" {
				// Already uploaded, need to create job
				p.jobQueue <- item
			} else if state.TarStatus == "success" {
				// Already tarred, need to upload
				p.uploadQueue <- item
			} else {
				// Need to tar
				p.tarQueue <- item
			}
		}

		// Close tar queue once all items are queued
		close(p.tarQueue)
	}()

	// Wait for all workers to complete
	wg.Wait()
	close(stopProgress)

	log.Printf("Pipeline completed: %d/%d jobs finished", p.completedJobs, p.totalJobs)

	return nil
}

// tarWorker processes tar operations
func (p *Pipeline) tarWorker(ctx context.Context, wg *sync.WaitGroup, workerID int) {
	defer wg.Done()

	for item := range p.tarQueue {
		p.setActiveWorker("tar", 1)

		// Check if already tarred
		if item.state.TarStatus == "success" {
			// Verify tar file exists
			exists, _ := tar.ValidateTarExists(item.state.TarPath)
			if exists {
				// Already done, move to upload
				p.setActiveWorker("tar", -1)
				p.uploadQueue <- item
				continue
			}
		}

		// Generate tar path
		tarPath := tar.GenerateTarPath(item.jobSpec.Directory, p.tempDir, p.cfg.TarCompression)
		item.state.TarPath = tarPath

		// Report start of tar operation
		p.reportStateChange(item.state.JobName, "tar", "in_progress", "", "", 0.0)

		log.Printf("[TAR #%d] Creating archive: %s -> %s", item.index, item.jobSpec.Directory, tarPath)

		// Create tar - use options if patterns or flatten mode are configured (v0.7.4)
		var err error
		if len(p.cfg.IncludePatterns) > 0 || len(p.cfg.ExcludePatterns) > 0 || p.cfg.FlattenTar {
			// Use CreateTarGzWithOptions for filtering/flattening
			if len(p.cfg.IncludePatterns) > 0 {
				log.Printf("[TAR #%d] Include patterns: %v", item.index, p.cfg.IncludePatterns)
			}
			if len(p.cfg.ExcludePatterns) > 0 {
				log.Printf("[TAR #%d] Exclude patterns: %v", item.index, p.cfg.ExcludePatterns)
			}
			if p.cfg.FlattenTar {
				log.Printf("[TAR #%d] Flatten mode enabled", item.index)
			}
			err = tar.CreateTarGzWithOptions(item.jobSpec.Directory, tarPath, p.multiPartMode,
				p.cfg.IncludePatterns, p.cfg.ExcludePatterns, p.cfg.FlattenTar, p.cfg.TarCompression)
		} else {
			// Use standard tar creation (faster for simple cases)
			err = tar.CreateTarGz(item.jobSpec.Directory, tarPath, p.multiPartMode, p.cfg.TarCompression)
		}

		if err != nil {
			log.Printf("[TAR #%d] FAILED: %v", item.index, err)
			item.state.TarStatus = "failed"
			item.state.ErrorMessage = err.Error()
			p.stateMgr.UpdateState(item.state)
			p.reportStateChange(item.state.JobName, "tar", "failed", "", err.Error(), 0.0)
			p.setActiveWorker("tar", -1)
			continue
		}

		// Update state
		item.state.TarStatus = "success"
		item.state.ErrorMessage = ""
		p.stateMgr.UpdateState(item.state)
		p.reportStateChange(item.state.JobName, "tar", "completed", "", "", 0.0)

		log.Printf("[TAR #%d] Success", item.index)

		// Move to upload queue
		p.setActiveWorker("tar", -1)
		p.uploadQueue <- item
	}

	// When tar queue is done, close upload queue if all tar workers are done
	p.mu.Lock()
	p.activeWorkers["tar_finished"]++
	if p.activeWorkers["tar_finished"] == p.tarWorkers {
		close(p.uploadQueue)
	}
	p.mu.Unlock()
}

// uploadWorker processes upload operations
func (p *Pipeline) uploadWorker(ctx context.Context, wg *sync.WaitGroup, workerID int) {
	defer wg.Done()

	for item := range p.uploadQueue {
		p.setActiveWorker("upload", 1)

		// Check if already uploaded
		if item.state.UploadStatus == "success" && item.state.FileID != "" {
			// Already done, move to job creation
			p.setActiveWorker("upload", -1)
			p.jobQueue <- item
			continue
		}

		log.Printf("[UPLOAD #%d] Uploading: %s", item.index, item.state.TarPath)

		// Report start of upload operation (0% progress)
		p.reportStateChange(item.state.JobName, "upload", "in_progress", "", "", 0.0)

		// Upload file with retry on proxy timeout (v0.7.3)
		var cloudFile *models.CloudFile
		var err error
		maxRetries := p.cfg.MaxRetries
		if maxRetries < 1 {
			maxRetries = 1
		}

		// Get file size for transfer allocation
		fileInfo, err := os.Stat(item.state.TarPath)
		if err != nil {
			log.Printf("[UPLOAD #%d] FAILED to stat file: %v", item.index, err)
			item.state.UploadStatus = "failed"
			item.state.ErrorMessage = err.Error()
			p.stateMgr.UpdateState(item.state)
			p.reportStateChange(item.state.JobName, "upload", "failed", "", err.Error(), 0.0)
			p.setActiveWorker("upload", -1)
			continue
		}

		// Allocate transfer handle for resource management
		transferHandle := p.transferMgr.AllocateTransfer(fileInfo.Size(), 1)
		defer transferHandle.Complete() // Release resources when done

		for attempt := 1; attempt <= maxRetries; attempt++ {
			// Create progress callback that emits StateChange events
			progressCallback := func(progress float64) {
				p.reportStateChange(item.state.JobName, "upload", "in_progress", "", "", progress)
			}

			// Use transfer-managed upload for better resource allocation
			cloudFile, err = upload.UploadFileToFolderWithTransfer(
				ctx,
				item.state.TarPath,
				"", // empty folder ID means root folder
				p.apiClient,
				progressCallback,
				transferHandle,
				io.Discard, // Pipeline is non-interactive, suppress output
			)

			if err == nil {
				// Success
				break
			}

			// Check if error is proxy timeout (v0.7.3)
			errStr := err.Error()
			isTimeout := strings.Contains(errStr, "timeout") ||
				strings.Contains(errStr, "SocketTimeoutException") ||
				strings.Contains(errStr, "connection reset") ||
				strings.Contains(errStr, "EOF")

			if isTimeout && attempt < maxRetries {
				log.Printf("[UPLOAD #%d] [RETRY] Detected proxy timeout, forcing fresh auth and retrying...", item.index)

				// Force fresh proxy authentication
				if p.cfg.ProxyMode == "basic" || p.cfg.ProxyMode == "ntlm" {
					// Get HTTP client from API client and force warmup
					// Note: This requires the API client to expose its HTTP client
					// For now, we'll rely on automatic warmup in basic mode (v0.7.2)
					log.Printf("[UPLOAD #%d] [RETRY] Waiting 2 seconds before retry...", item.index)
					time.Sleep(2 * time.Second)
				}

				log.Printf("[UPLOAD #%d] [RETRY] Attempt %d/%d", item.index, attempt+1, maxRetries)
				continue
			}

			// Non-timeout error or max retries reached
			break
		}

		if err != nil {
			if strings.Contains(err.Error(), "timeout") {
				log.Printf("[UPLOAD #%d] FAILED after %d retries: %v", item.index, maxRetries, err)
			} else {
				log.Printf("[UPLOAD #%d] FAILED: %v", item.index, err)
			}
			item.state.UploadStatus = "failed"
			item.state.ErrorMessage = err.Error()
			p.stateMgr.UpdateState(item.state)
			p.reportStateChange(item.state.JobName, "upload", "failed", "", err.Error(), 0.0)
			p.setActiveWorker("upload", -1)
			continue
		}

		// Update state
		item.state.FileID = cloudFile.ID
		item.state.UploadStatus = "success"
		item.state.ErrorMessage = ""
		p.stateMgr.UpdateState(item.state)
		p.reportStateChange(item.state.JobName, "upload", "completed", "", "", 1.0)

		log.Printf("[UPLOAD #%d] Success: File ID %s", item.index, cloudFile.ID)

		// Move to job queue
		p.setActiveWorker("upload", -1)
		p.jobQueue <- item
	}

	// When upload queue is done, close job queue if all upload workers are done
	p.mu.Lock()
	p.activeWorkers["upload_finished"]++
	if p.activeWorkers["upload_finished"] == p.uploadWorkers {
		close(p.jobQueue)
	}
	p.mu.Unlock()
}

// jobWorker processes job creation and submission
func (p *Pipeline) jobWorker(ctx context.Context, wg *sync.WaitGroup, workerID int) {
	defer wg.Done()

	for item := range p.jobQueue {
		p.setActiveWorker("job", 1)

		// Check if job already exists
		if item.state.JobID != "" && item.state.SubmitStatus == "success" {
			// Already done
			p.setActiveWorker("job", -1)
			p.incrementCompleted()
			continue
		}

		// Create job if not exists
		if item.state.JobID == "" {
			log.Printf("[JOB #%d] Creating job: %s", item.index, item.jobSpec.JobName)

			// Report start of create operation
			p.reportStateChange(item.state.JobName, "create", "in_progress", "", "", 0.0)

			jobReq, err := buildJobRequest(item.jobSpec, item.state.FileID)
			if err != nil {
				log.Printf("[JOB #%d] FAILED to build request: %v", item.index, err)
				item.state.ErrorMessage = err.Error()
				p.stateMgr.UpdateState(item.state)
				p.reportStateChange(item.state.JobName, "create", "failed", "", err.Error(), 0.0)
				p.setActiveWorker("job", -1)
				continue
			}

			jobResp, err := p.apiClient.CreateJob(ctx, *jobReq)
			if err != nil {
				log.Printf("[JOB #%d] FAILED to create: %v", item.index, err)
				item.state.ErrorMessage = err.Error()
				p.stateMgr.UpdateState(item.state)
				p.reportStateChange(item.state.JobName, "create", "failed", "", err.Error(), 0.0)
				p.setActiveWorker("job", -1)
				continue
			}

			item.state.JobID = jobResp.ID
			p.stateMgr.UpdateState(item.state)
			p.reportStateChange(item.state.JobName, "create", "completed", jobResp.ID, "", 0.0)

			log.Printf("[JOB #%d] Created: Job ID %s", item.index, jobResp.ID)
		}

		// Submit job if requested
		if shouldSubmit(item.jobSpec.SubmitMode) && item.state.SubmitStatus != "success" {
			log.Printf("[JOB #%d] Submitting job %s", item.index, item.state.JobID)

			// Report start of submit operation
			p.reportStateChange(item.state.JobName, "submit", "in_progress", item.state.JobID, "", 0.0)

			err := p.apiClient.SubmitJob(ctx, item.state.JobID)
			if err != nil {
				log.Printf("[JOB #%d] FAILED to submit: %v", item.index, err)
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

			log.Printf("[JOB #%d] Submitted successfully", item.index)
		} else {
			item.state.SubmitStatus = "skipped"
			p.stateMgr.UpdateState(item.state)
		}

		p.setActiveWorker("job", -1)
		p.incrementCompleted()
	}
}

// buildJobRequest builds a job request from job spec
func buildJobRequest(spec models.JobSpec, fileID string) (*models.JobRequest, error) {
	// Parse license settings
	licenseEnv, err := config.ParseLicenseJSON(spec.LicenseSettings)
	if err != nil {
		return nil, fmt.Errorf("invalid license settings: %w", err)
	}

	// Build input files
	inputFiles := []models.InputFileRequest{
		{
			ID:         fileID,
			Decompress: !spec.NoDecompress,
		},
	}

	// Add extra input files if specified
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

	return jobReq, nil
}

// shouldSubmit determines if a job should be submitted based on submit mode
func shouldSubmit(submitMode string) bool {
	mode := strings.ToLower(strings.TrimSpace(submitMode))
	return mode == "yes" || mode == "true" || mode == "submit" || mode == ""
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

			log.Printf("[PROGRESS] Active workers: tar=%d upload=%d job=%d | Completed: %d/%d",
				tarActive, uploadActive, jobActive, completed, p.totalJobs)

		case <-stop:
			return
		}
	}
}
