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
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
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

	// v4.0.0: Eligibility configuration for auto-download feature
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
	apiClient *api.Client
	state     *State
	monitor   *Monitor
	logger    *logging.Logger

	// Shutdown coordination
	stopChan chan struct{}
	wg       sync.WaitGroup
	running  bool
	mu       sync.RWMutex

	// v4.0.8: Active download tracking for IPC status reporting
	activeDownloads int32
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

	// Create monitor with eligibility checking if configured (v4.0.0)
	var monitor *Monitor
	if daemonCfg.Eligibility != nil {
		monitor = NewMonitorWithEligibility(apiClient, state, daemonCfg.Filter, daemonCfg.Eligibility, logger)
	} else {
		monitor = NewMonitor(apiClient, state, daemonCfg.Filter, logger)
	}

	return &Daemon{
		cfg:       daemonCfg,
		apiClient: apiClient,
		state:     state,
		monitor:   monitor,
		logger:    logger,
		stopChan:  make(chan struct{}),
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
	d.mu.Unlock()

	d.logger.Info().
		Str("download_dir", d.cfg.DownloadDir).
		Str("poll_interval", d.cfg.PollInterval.String()).
		Msg("Daemon starting")

	// Run initial poll immediately
	d.poll(ctx)

	// Start polling loop
	d.wg.Add(1)
	go d.pollLoop(ctx)

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
			d.poll(ctx)
		}
	}
}

// poll checks for completed jobs and downloads them.
// v4.3.4: Improved step-by-step logging for visibility
// v4.3.5: Embed key info in message text (structured fields don't show in GUI)
// v4.3.6: Silent filtering - only log jobs with Auto Download = Enabled/Conditional
// v4.5.5: Added 10-minute scan timeout to prevent indefinite hangs
func (d *Daemon) poll(ctx context.Context) {
	// v4.5.5: Add timeout to entire scan operation (10 minutes max)
	// Must be longer than HTTP client timeout (300s) to allow retries
	scanCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	scanStart := time.Now()
	d.logger.Info().Msg("=== SCAN STARTED ===")

	// Find completed jobs that need downloading
	result, err := d.monitor.FindCompletedJobs(scanCtx)
	if err != nil {
		// v4.5.5: Add timeout-specific error handling
		if scanCtx.Err() == context.DeadlineExceeded {
			d.logger.Error().Dur("duration", time.Since(scanStart)).Msg("Scan timed out after 10 minutes")
		} else {
			d.logger.Error().Err(err).Msg("Failed to find completed jobs")
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

	// v4.3.6: Track statistics for summary - added filteredCount for silent skips
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

		// v4.3.6: Check eligibility - result now includes ShouldLog flag
		if d.cfg.Eligibility != nil {
			eligResult := d.monitor.CheckEligibility(ctx, job.ID)

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

	// v4.3.6: Log cycle summary with all counts
	d.logger.Info().Msgf("=== SCAN COMPLETE === Scanned %d, filtered %d, downloaded %d, skipped %d (took %.1fs)",
		totalScanned, filteredCount, downloadedCount, skippedCount, time.Since(scanStart).Seconds())

	d.state.UpdateLastPoll()
	if err := d.state.Save(); err != nil {
		d.logger.Error().Err(err).Msg("Failed to save state after poll")
	}
}

// downloadJob downloads all files from a completed job.
func (d *Daemon) downloadJob(ctx context.Context, job *CompletedJob) {
	// v4.0.8: Track active downloads for IPC status
	atomic.AddInt32(&d.activeDownloads, 1)
	defer atomic.AddInt32(&d.activeDownloads, -1)

	d.logger.Info().
		Str("job_id", job.ID).
		Str("job_name", job.Name).
		Msg("Downloading job")

	// v4.0.0 (D2.4): Check for custom download path from eligibility config
	baseDir := d.cfg.DownloadDir
	if d.cfg.Eligibility != nil {
		if customPath := d.monitor.GetJobDownloadPath(ctx, job.ID); customPath != "" {
			d.logger.Debug().
				Str("job_id", job.ID).
				Str("custom_path", customPath).
				Msg("Using custom download path from job")
			baseDir = customPath
		}
	}

	// Compute output directory - always include job ID suffix to avoid collisions
	// from jobs with the same name
	outputDir := ComputeOutputDir(baseDir, job.ID, job.Name, d.cfg.UseJobNameDir)

	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		d.logger.Error().Err(err).Str("dir", outputDir).Msg("Failed to create output directory")
		d.state.MarkFailed(job.ID, job.Name, err)
		return
	}

	// List job files
	files, err := d.apiClient.ListJobFiles(ctx, job.ID)
	if err != nil {
		d.logger.Error().Err(err).Str("job_id", job.ID).Msg("Failed to list job files")
		d.state.MarkFailed(job.ID, job.Name, err)
		return
	}

	if len(files) == 0 {
		d.logger.Info().Str("job_id", job.ID).Msg("No files to download for job")
		d.state.MarkDownloaded(job.ID, job.Name, outputDir, 0, 0)
		return
	}

	d.logger.Info().
		Str("job_id", job.ID).
		Int("file_count", len(files)).
		Msg("Downloading job files")

	// Create resource manager for transfer
	resourceMgr := resources.NewManager(resources.Config{
		MaxThreads: d.cfg.MaxConcurrent,
		AutoScale:  true,
	})

	// Download files
	var totalSize int64
	downloadedCount := 0
	var downloadErr error

	for _, file := range files {
		select {
		case <-ctx.Done():
			downloadErr = ctx.Err()
			break
		case <-d.stopChan:
			downloadErr = fmt.Errorf("daemon stopped during download")
			break
		default:
		}

		if downloadErr != nil {
			break
		}

		// Validate filename
		if err := validation.ValidateFilename(file.Name); err != nil {
			d.logger.Warn().
				Str("file_id", file.ID).
				Str("file_name", file.Name).
				Err(err).
				Msg("Skipping file with invalid name")
			continue
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
			continue
		}

		// Skip if file exists
		if _, err := os.Stat(localPath); err == nil {
			d.logger.Debug().Str("path", localPath).Msg("File already exists, skipping")
			downloadedCount++
			totalSize += file.DecryptedSize
			continue
		}

		// Allocate transfer handle
		transferMgr := transfer.NewManager(resourceMgr)
		transferHandle := transferMgr.AllocateTransfer(file.DecryptedSize, 1)

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
				// Silent progress for daemon mode
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
			// Continue with other files rather than failing entire job
			continue
		}

		downloadedCount++
		totalSize += file.DecryptedSize
	}

	if downloadErr != nil {
		d.logger.Error().Err(downloadErr).Str("job_id", job.ID).Msg("Job download interrupted")
		d.state.MarkFailed(job.ID, job.Name, downloadErr)
		return
	}

	// Mark as downloaded in local state
	d.state.MarkDownloaded(job.ID, job.Name, outputDir, downloadedCount, totalSize)

	// v4.0.0: Tag the job as downloaded (D2.3)
	// v4.3.0: Use hardcoded tag from config package
	// This prevents the job from being downloaded again via eligibility checking
	if d.cfg.Eligibility != nil {
		if err := d.apiClient.AddJobTag(ctx, job.ID, config.DownloadedTag); err != nil {
			// Log but don't fail - job is already downloaded locally
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

	// v4.3.5: Embed info in message text for GUI visibility
	d.logger.Info().Msgf("COMPLETED: %s [%s] - %d files, %s",
		job.Name, job.ID, downloadedCount, formatBytes(totalSize))
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

// v4.0.8: Stats methods for IPC status reporting

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

// TriggerPoll manually triggers a poll cycle outside the normal schedule.
// This is used by the tray app's "Trigger Scan Now" feature.
func (d *Daemon) TriggerPoll() {
	d.mu.RLock()
	running := d.running
	d.mu.RUnlock()

	if !running {
		return
	}

	// Run poll in background to not block caller
	go d.poll(context.Background())
}

