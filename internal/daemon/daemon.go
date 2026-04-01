// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/config"
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/reporting"
	"github.com/rescale/rescale-int/internal/resources"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/validation"
)

// Config holds daemon configuration.
type Config struct {
	// PollInterval is how often to check for completed jobs
	PollInterval time.Duration

	// DownloadDir is where to download job output files
	DownloadDir string

	// UseJobNameDir uses job name instead of job ID for output directory
	UseJobNameDir bool

	// Filter specifies job name filtering criteria
	Filter *JobFilter

	// StateFile is the path to the daemon state file
	StateFile string

	// MaxConcurrent is the maximum number of concurrent file downloads per job
	MaxConcurrent int

	// LogFile is the path to write daemon logs (empty = stdout)
	LogFile string

	// Verbose enables debug logging
	Verbose bool

	// When set, jobs must pass eligibility checks to be downloaded
	Eligibility *EligibilityConfig
}

// DefaultConfig returns a daemon configuration with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		PollInterval:  5 * time.Minute,
		DownloadDir:   ".",
		UseJobNameDir: true,
		MaxConcurrent: 5,
		StateFile:     DefaultStateFilePath(),
	}
}

// Daemon is the background service for auto-downloading completed jobs.
type Daemon struct {
	cfg       *Config
	appCfg    *config.Config
	apiClient *api.Client
	state     *State
	monitor   *Monitor
	logger    *logging.Logger

	// Shutdown coordination
	stopChan chan struct{}
	wg       sync.WaitGroup
	running  bool
	mu       sync.RWMutex

	// Prevents concurrent poll() execution (Start, pollLoop, TriggerPoll can all invoke)
	polling atomic.Bool

	// Lifecycle context — created in Start(), cancelled in Stop()
	cancelFunc   context.CancelFunc
	lifecycleCtx context.Context

	// Centralized pause state, checked by pollLoop and TriggerPoll
	paused atomic.Bool

	activeDownloads int32
	tracker         *DaemonTransferTracker
}

// New creates a new daemon instance.
func New(appCfg *config.Config, daemonCfg *Config, logger *logging.Logger) (*Daemon, error) {
	if daemonCfg == nil {
		daemonCfg = DefaultConfig()
	}

	// Create API client
	apiClient, err := api.NewClient(appCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	// Create state manager
	state := NewState(daemonCfg.StateFile)
	if err := state.Load(); err != nil {
		return nil, fmt.Errorf("failed to load state: %w", err)
	}

	// Create monitor with eligibility checking if configured
	var monitor *Monitor
	if daemonCfg.Eligibility != nil {
		monitor = NewMonitorWithEligibility(apiClient, state, daemonCfg.Filter, daemonCfg.Eligibility, logger)
	} else {
		monitor = NewMonitor(apiClient, state, daemonCfg.Filter, logger)
	}

	return &Daemon{
		cfg:       daemonCfg,
		appCfg:    appCfg,
		apiClient: apiClient,
		state:     state,
		monitor:   monitor,
		logger:    logger,
		stopChan:  make(chan struct{}),
		tracker:   NewDaemonTransferTracker(),
	}, nil
}

// Start begins the daemon's polling loop.
func (d *Daemon) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return fmt.Errorf("daemon is already running")
	}
	d.running = true
	d.lifecycleCtx, d.cancelFunc = context.WithCancel(ctx)
	d.mu.Unlock()

	d.logger.Info().
		Str("download_dir", d.cfg.DownloadDir).
		Str("poll_interval", d.cfg.PollInterval.String()).
		Msg("Daemon starting")

	// Run initial poll immediately
	d.poll(d.lifecycleCtx)

	// Start polling loop
	d.wg.Add(1)
	go d.pollLoop(d.lifecycleCtx)

	return nil
}

// Stop signals the daemon to stop and waits for cleanup.
func (d *Daemon) Stop() {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return
	}
	d.running = false
	d.mu.Unlock()

	d.logger.Info().Msg("Daemon stopping")
	d.cancelFunc() // Cancel lifecycle context before closing stopChan
	close(d.stopChan)
	d.wg.Wait()

	// Save final state
	if err := d.state.Save(); err != nil {
		d.logger.Error().Err(err).Msg("Failed to save state on shutdown")
	}

	d.logger.Info().Msg("Daemon stopped")
}

// pollLoop runs the periodic polling.
func (d *Daemon) pollLoop(ctx context.Context) {
	defer d.wg.Done()

	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info().Msg("Poll loop cancelled by context")
			return
		case <-d.stopChan:
			d.logger.Info().Msg("Poll loop stopped")
			return
		case <-ticker.C:
			if d.paused.Load() {
				d.logger.Debug().Msg("Daemon paused, skipping scheduled poll")
				continue
			}
			d.poll(ctx)
		}
	}
}

// poll checks for completed jobs and downloads them.
func (d *Daemon) poll(ctx context.Context) {
	// Prevent concurrent polls (Start, pollLoop, TriggerPoll can all invoke)
	if !d.polling.CompareAndSwap(false, true) {
		d.logger.Debug().Msg("Poll already in progress, skipping")
		return
	}
	defer d.polling.Store(false)

	// Scan timeout must be longer than HTTP client timeout (300s) to allow retries
	scanCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	scanStart := time.Now()
	d.logger.Info().Msg("=== SCAN STARTED ===")

	inthttp.WarmupProxyIfNeeded(scanCtx, d.appCfg)
	credentials.GetManager(d.apiClient).WarmAll(scanCtx)

	// Find completed jobs that need downloading
	result, err := d.monitor.FindCompletedJobs(scanCtx)
	if err != nil {
		if scanCtx.Err() == context.DeadlineExceeded {
			d.logger.Error().Dur("duration", time.Since(scanStart)).Msg("Scan timed out after 10 minutes")
		} else {
			d.logger.Error().Msgf("Failed to find completed jobs: %v", err)
		}
		d.logger.Info().Msgf("=== SCAN COMPLETE === Error (took %.1fs)", time.Since(scanStart).Seconds())
		return
	}

	completed := result.Candidates
	totalScanned := result.TotalScanned

	if len(completed) == 0 {
		d.logger.Info().Msgf("=== SCAN COMPLETE === Scanned %d jobs, no candidates (took %.1fs)",
			totalScanned, time.Since(scanStart).Seconds())
		d.state.UpdateLastPoll()
		return
	}

	d.logger.Info().Msgf("Checking %d potential jobs...", len(completed))

	var downloadedCount, skippedCount, filteredCount int

	// Check eligibility and download each job
	for _, job := range completed {
		select {
		case <-ctx.Done():
			d.logger.Info().Msg("Scan interrupted by context cancellation")
			return
		case <-d.stopChan:
			d.logger.Info().Msg("Scan interrupted by stop signal")
			return
		default:
		}

		if d.cfg.Eligibility != nil {
			// Per-call timeout prevents a single slow eligibility check from blocking the scan
			eligCtx, eligCancel := context.WithTimeout(ctx, 2*time.Minute)
			eligResult := d.monitor.CheckEligibility(eligCtx, job.ID)
			eligCancel()

			if !eligResult.ShouldLog {
				// Not a real candidate (Auto Download not set/disabled) - skip silently
				filteredCount++
				continue
			}

			if !eligResult.Eligible {
				// Real candidate but not eligible (already downloaded, missing tag, etc.) - log it
				skippedCount++
				d.logger.Info().Msgf("SKIP: %s [%s] - %s", job.Name, job.ID, eligResult.Reason)
				continue
			}

			// Job is eligible - will download
			d.logger.Info().Msgf("DOWNLOAD: %s [%s] - %s", job.Name, job.ID, eligResult.Reason)
		}

		d.downloadJob(ctx, job)
		downloadedCount++
	}

	d.logger.Info().Msgf("=== SCAN COMPLETE === Scanned %d, filtered %d, downloaded %d, skipped %d (took %.1fs)",
		totalScanned, filteredCount, downloadedCount, skippedCount, time.Since(scanStart).Seconds())

	d.state.UpdateLastPoll()
	if err := d.state.Save(); err != nil {
		d.logger.Error().Err(err).Msg("Failed to save state after poll")
	}
}

// daemonDownloadItem implements transfer.WorkItem for downloadJob's RunBatch usage.
type daemonDownloadItem struct {
	file models.JobFile
}

func (d daemonDownloadItem) FileSize() int64 { return d.file.DecryptedSize }

// downloadJob downloads all files from a completed job.
func (d *Daemon) downloadJob(ctx context.Context, job *CompletedJob) {
	atomic.AddInt32(&d.activeDownloads, 1)
	defer atomic.AddInt32(&d.activeDownloads, -1)

	d.logger.Info().
		Str("job_id", job.ID).
		Str("job_name", job.Name).
		Msg("Downloading job")

	// Check for custom download path from eligibility config
	baseDir := d.cfg.DownloadDir
	if d.cfg.Eligibility != nil {
		if customPath := d.monitor.GetJobDownloadPath(ctx, job.ID); customPath != "" {
			// Custom path must resolve to within DownloadDir to prevent
			// arbitrary filesystem writes even when daemon runs as SYSTEM.
			candidate := customPath
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(d.cfg.DownloadDir, candidate)
			}
			candidate = filepath.Clean(candidate)

			// Resolve symlinks on both paths to prevent symlink-based escapes.
			realDownloadDir, err := filepath.EvalSymlinks(d.cfg.DownloadDir)
			if err != nil {
				realDownloadDir = filepath.Clean(d.cfg.DownloadDir)
			}
			realCandidate := resolvePathWithSymlinks(candidate)

			if err := validation.ValidatePathInDirectory(realCandidate, realDownloadDir); err != nil {
				d.logger.Warn().
					Str("job_id", job.ID).
					Str("custom_path", customPath).
					Str("download_dir", d.cfg.DownloadDir).
					Err(err).
					Msg("Rejecting custom download path: escapes download directory")
			} else {
				d.logger.Debug().
					Str("job_id", job.ID).
					Str("custom_path", customPath).
					Str("resolved", realCandidate).
					Msg("Using custom download path (validated under download directory)")
				baseDir = realCandidate
			}
		}
	}

	// Compute output directory - always include job ID suffix to avoid collisions
	// from jobs with the same name
	outputDir := ComputeOutputDir(baseDir, job.ID, job.Name, d.cfg.UseJobNameDir)

	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		d.logger.Error().Err(err).Str("dir", outputDir).Msg("Failed to create output directory")
		d.state.MarkFailed(job.ID, job.Name, err)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		reporting.HandleCLIError(err, "daemon", "job_download", "")
		return
	}

	// List job files
	files, err := d.apiClient.ListJobFiles(ctx, job.ID)
	if err != nil {
		d.logger.Error().Err(err).Str("job_id", job.ID).Msg("Failed to list job files")
		d.state.MarkFailed(job.ID, job.Name, err)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		reporting.HandleCLIError(err, "daemon", "job_download", "")
		return
	}

	if len(files) == 0 {
		d.logger.Info().Str("job_id", job.ID).Msg("No files to download for job")
		d.state.MarkDownloaded(job.ID, job.Name, outputDir, 0, 0)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		return
	}

	var trackerTotalBytes int64
	for _, f := range files {
		trackerTotalBytes += f.DecryptedSize
	}
	d.tracker.StartBatch(job.ID, job.Name, len(files), trackerTotalBytes)
	defer d.tracker.FinalizeBatch(job.ID)

	d.logger.Info().
		Str("job_id", job.ID).
		Int("file_count", len(files)).
		Msg("Downloading job files")

	// Create resource manager for transfer
	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads: d.cfg.MaxConcurrent,
		AutoScale:  true,
	})
	transferMgr := transfer.NewManager(resourceMgr)

	// Wire stopChan to context cancellation for RunBatch.
	downloadCtx, downloadCancel := context.WithCancel(ctx)
	defer downloadCancel()
	go func() {
		select {
		case <-d.stopChan:
			downloadCancel()
		case <-downloadCtx.Done():
		}
	}()

	// Build work items for RunBatch.
	items := make([]daemonDownloadItem, len(files))
	for i := range files {
		items[i] = daemonDownloadItem{file: files[i]}
	}

	// Note: We do NOT rely on BatchResult — all real outcomes are tracked
	// by manual counters (downloadedCount, totalSize, tracker).
	var totalSize int64
	var failedCount int32
	var skippedCount int32
	downloadedCount := 0
	batchCfg := transfer.BatchConfig{
		MaxWorkers:      d.cfg.MaxConcurrent,
		ResourceMgr:     resourceMgr,
		Label:           "DAEMON-DOWNLOAD",
		ForceSequential: true,
	}

	transfer.RunBatch(downloadCtx, items, batchCfg, func(ctx context.Context, item daemonDownloadItem) error {
		file := item.file

		// Validate filename
		if err := validation.ValidateFilename(file.Name); err != nil {
			d.logger.Warn().
				Str("file_id", file.ID).
				Str("file_name", file.Name).
				Err(err).
				Msg("Skipping file with invalid name")
			d.tracker.SkipFile(job.ID, file.DecryptedSize)
			atomic.AddInt32(&skippedCount, 1)
			return nil
		}

		// Compute output path
		// RelativePath from API already includes the full path with filename
		var localPath string
		if file.RelativePath != "" {
			// Validate relative path to prevent escaping output directory
			if err := validation.ValidatePathInDirectory(file.RelativePath, outputDir); err == nil {
				localPath = filepath.Join(outputDir, file.RelativePath)
			} else {
				// Invalid path - use name only
				localPath = filepath.Join(outputDir, file.Name)
			}
		} else {
			localPath = filepath.Join(outputDir, file.Name)
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
			d.logger.Error().Err(err).Str("path", localPath).Msg("Failed to create file directory")
			d.tracker.FailFile(job.ID, file.DecryptedSize)
			atomic.AddInt32(&failedCount, 1)
			return nil
		}

		if info, statErr := os.Stat(localPath); statErr == nil {
			if info.Size() == file.DecryptedSize {
				d.logger.Debug().Str("path", localPath).Msg("File already exists with correct size, skipping")
				downloadedCount++
				totalSize += file.DecryptedSize
				d.tracker.CompleteFile(job.ID, file.DecryptedSize)
				return nil
			}
			// File exists but wrong size — re-download
			d.logger.Warn().
				Str("path", localPath).
				Int64("expected_size", file.DecryptedSize).
				Int64("actual_size", info.Size()).
				Msg("File exists with wrong size, re-downloading")
		}

		// Allocate transfer handle (reuses single TransferManager per job)
		transferHandle := transferMgr.AllocateTransfer(file.DecryptedSize, 1)
		defer transferHandle.Complete()

		// Download file
		d.logger.Debug().
			Str("file_id", file.ID).
			Str("file_name", file.Name).
			Int64("size", file.DecryptedSize).
			Msg("Downloading file")

		err := download.DownloadFile(ctx, download.DownloadParams{
			FileID:    file.ID,
			LocalPath: localPath,
			APIClient: d.apiClient,
			ProgressCallback: func(fraction float64) {
				d.tracker.UpdateFileProgress(job.ID, file.DecryptedSize, fraction)
			},
			TransferHandle: transferHandle,
			SkipChecksum:   false,
		})

		if err != nil {
			d.logger.Error().
				Err(err).
				Str("file_id", file.ID).
				Str("file_name", file.Name).
				Msg("Failed to download file")
			d.tracker.FailFile(job.ID, file.DecryptedSize)
			atomic.AddInt32(&failedCount, 1)
			return nil
		}

		downloadedCount++
		totalSize += file.DecryptedSize
		d.tracker.CompleteFile(job.ID, file.DecryptedSize)
		return nil
	})

	// Check if download was interrupted (context cancelled by stopChan or parent)
	if downloadCtx.Err() != nil {
		downloadErr := downloadCtx.Err()
		if ctx.Err() == nil {
			// stopChan was closed — not parent context
			downloadErr = fmt.Errorf("daemon stopped during download")
		}
		d.logger.Error().Err(downloadErr).Str("job_id", job.ID).Msg("Job download interrupted")
		d.state.MarkFailed(job.ID, job.Name, downloadErr)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		reporting.HandleCLIError(downloadErr, "daemon", "job_download", "")
		return
	}

	// Only mark job as downloaded when ALL files succeeded; partial success is treated
	// as failure so the job is retried on the next poll cycle.
	fc := atomic.LoadInt32(&failedCount)
	sc := atomic.LoadInt32(&skippedCount)
	if fc+sc > 0 {
		failErr := fmt.Errorf("%d failed + %d skipped of %d files", fc, sc, len(files))
		d.logger.Warn().
			Str("job_id", job.ID).
			Int32("failed_files", fc).
			Int32("skipped_files", sc).
			Int("total_files", len(files)).
			Msg("Job incomplete, marking as failed for retry")
		d.state.MarkFailed(job.ID, job.Name, failErr)
	} else {
		d.state.MarkDownloaded(job.ID, job.Name, outputDir, downloadedCount, totalSize)

		// Tag the job as downloaded to prevent re-download via eligibility checking
		if d.cfg.Eligibility != nil {
			if err := d.apiClient.AddJobTag(ctx, job.ID, config.DownloadedTag); err != nil {
				d.logger.Warn().
					Err(err).
					Str("job_id", job.ID).
					Str("tag", config.DownloadedTag).
					Msg("Failed to tag job as downloaded (will retry on next poll)")
			} else {
				d.logger.Debug().
					Str("job_id", job.ID).
					Str("tag", config.DownloadedTag).
					Msg("Tagged job as downloaded")
			}
		}

		d.logger.Info().Msgf("COMPLETED: %s [%s] - %d files, %s",
			job.Name, job.ID, downloadedCount, formatBytes(totalSize))
	}

	// Persist state after each job for crash safety
	if saveErr := d.state.Save(); saveErr != nil {
		d.logger.Error().Err(saveErr).Msg("Failed to persist state")
	}
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// resolvePathWithSymlinks resolves symlinks for a path that may not fully exist.
// It walks upward from the given path to find the longest existing ancestor,
// resolves symlinks on that ancestor, then appends the non-existent suffix.
// This is needed because filepath.EvalSymlinks requires the path to exist.
func resolvePathWithSymlinks(path string) string {
	// Try the full path first
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	// Walk upward to find the longest existing ancestor
	current := path
	var suffix []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root without finding an existing path
			break
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent

		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			// Found an existing ancestor — resolve and append suffix
			result := resolved
			for _, s := range suffix {
				result = filepath.Join(result, s)
			}
			return result
		}
	}

	// Nothing resolvable — return cleaned original path
	return filepath.Clean(path)
}

// RunOnce performs a single poll cycle and exits.
// Useful for testing or one-shot downloads.
func (d *Daemon) RunOnce(ctx context.Context) error {
	d.logger.Info().Msg("Running single poll cycle")
	d.poll(ctx)
	if err := d.state.Save(); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}
	return nil
}

// GetLastPollTime returns the time of the last successful poll cycle.
func (d *Daemon) GetLastPollTime() time.Time {
	return d.state.GetLastPoll()
}

// GetDownloadedCount returns the total number of successfully downloaded jobs.
func (d *Daemon) GetDownloadedCount() int {
	return d.state.GetDownloadedCount()
}

// GetActiveDownloads returns the number of downloads currently in progress.
func (d *Daemon) GetActiveDownloads() int {
	return int(atomic.LoadInt32(&d.activeDownloads))
}

func (d *Daemon) SetPaused(paused bool) {
	d.paused.Store(paused)
	if paused {
		d.logger.Info().Msg("Daemon paused")
	} else {
		d.logger.Info().Msg("Daemon resumed")
	}
}

func (d *Daemon) IsPaused() bool {
	return d.paused.Load()
}

// TriggerPoll manually triggers a poll cycle outside the normal schedule.
// This is used by the tray app's "Trigger Scan Now" feature.
// Holds RLock through wg.Add to prevent a race with Stop() (which holds
// the write lock before calling wg.Wait).
func (d *Daemon) TriggerPoll() {
	d.mu.RLock()
	if !d.running {
		d.mu.RUnlock()
		return
	}
	if d.paused.Load() {
		d.mu.RUnlock()
		d.logger.Debug().Msg("Daemon paused, ignoring manual trigger")
		return
	}
	// wg.Add under lock — Stop() holds write lock before wg.Wait(),
	// so this Add is guaranteed to happen before or after Wait, never during.
	d.wg.Add(1)
	ctx := d.lifecycleCtx
	d.mu.RUnlock()

	go func() {
		defer d.wg.Done()
		d.poll(ctx)
	}()
}

