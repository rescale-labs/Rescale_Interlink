// Package daemon provides background service functionality for auto-downloading completed jobs.
// Version: 3.4.0
// Date: December 2025
package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
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

	// Create monitor
	monitor := NewMonitor(apiClient, state, daemonCfg.Filter, logger)

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

// IsRunning returns whether the daemon is currently running.
func (d *Daemon) IsRunning() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.running
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
func (d *Daemon) poll(ctx context.Context) {
	d.logger.Debug().Msg("Starting poll cycle")

	// Find completed jobs that need downloading
	completed, err := d.monitor.FindCompletedJobs(ctx)
	if err != nil {
		d.logger.Error().Err(err).Msg("Failed to find completed jobs")
		return
	}

	if len(completed) == 0 {
		d.logger.Debug().Msg("No new completed jobs to download")
		d.state.UpdateLastPoll()
		return
	}

	d.logger.Info().Int("count", len(completed)).Msg("Found completed jobs to download")

	// Download each job
	for _, job := range completed {
		select {
		case <-ctx.Done():
			d.logger.Info().Msg("Download interrupted by context cancellation")
			return
		case <-d.stopChan:
			d.logger.Info().Msg("Download interrupted by stop signal")
			return
		default:
			d.downloadJob(ctx, job)
		}
	}

	d.state.UpdateLastPoll()
	if err := d.state.Save(); err != nil {
		d.logger.Error().Err(err).Msg("Failed to save state after poll")
	}
}

// downloadJob downloads all files from a completed job.
func (d *Daemon) downloadJob(ctx context.Context, job *CompletedJob) {
	d.logger.Info().
		Str("job_id", job.ID).
		Str("job_name", job.Name).
		Msg("Downloading job")

	// Compute output directory - always include job ID suffix to avoid collisions
	// from jobs with the same name
	outputDir := ComputeOutputDir(d.cfg.DownloadDir, job.ID, job.Name, d.cfg.UseJobNameDir)

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

	// Mark as downloaded
	d.state.MarkDownloaded(job.ID, job.Name, outputDir, downloadedCount, totalSize)

	d.logger.Info().
		Str("job_id", job.ID).
		Str("job_name", job.Name).
		Str("output_dir", outputDir).
		Int("file_count", downloadedCount).
		Int64("total_size", totalSize).
		Msg("Job download complete")
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

// GetStatus returns current daemon status information.
func (d *Daemon) GetStatus() *Status {
	return &Status{
		Running:         d.IsRunning(),
		LastPoll:        d.state.GetLastPoll(),
		DownloadedCount: d.state.GetDownloadedCount(),
		FailedCount:     d.state.GetFailedCount(),
		DownloadDir:     d.cfg.DownloadDir,
		PollInterval:    d.cfg.PollInterval,
	}
}

// Status contains daemon status information.
type Status struct {
	Running         bool
	LastPoll        time.Time
	DownloadedCount int
	FailedCount     int
	DownloadDir     string
	PollInterval    time.Duration
}

// WriteStatus writes status to a writer.
func (s *Status) WriteStatus(w io.Writer) {
	fmt.Fprintf(w, "Daemon Status:\n")
	if s.Running {
		fmt.Fprintf(w, "  Running: Yes\n")
	} else {
		fmt.Fprintf(w, "  Running: No\n")
	}
	if !s.LastPoll.IsZero() {
		fmt.Fprintf(w, "  Last Poll: %s\n", s.LastPoll.Format(time.RFC3339))
	} else {
		fmt.Fprintf(w, "  Last Poll: Never\n")
	}
	fmt.Fprintf(w, "  Downloaded Jobs: %d\n", s.DownloadedCount)
	fmt.Fprintf(w, "  Failed Jobs: %d\n", s.FailedCount)
	fmt.Fprintf(w, "  Download Directory: %s\n", s.DownloadDir)
	fmt.Fprintf(w, "  Poll Interval: %s\n", s.PollInterval)
}

// ListDownloaded returns the list of downloaded jobs.
func (d *Daemon) ListDownloaded(limit int) []*DownloadedJob {
	return d.state.GetRecentDownloads(limit)
}

// ListFailed returns the list of failed downloads.
func (d *Daemon) ListFailed() []*DownloadedJob {
	return d.state.GetFailedJobs()
}

// RetryFailed clears failed status for a job, allowing it to be retried on next poll.
func (d *Daemon) RetryFailed(jobID string) {
	d.state.ClearFailed(jobID)
	if err := d.state.Save(); err != nil {
		d.logger.Error().Err(err).Msg("Failed to save state after retry")
	}
}

// RetryAllFailed clears failed status for all failed jobs.
func (d *Daemon) RetryAllFailed() int {
	failed := d.state.GetFailedJobs()
	for _, job := range failed {
		d.state.ClearFailed(job.JobID)
	}
	if err := d.state.Save(); err != nil {
		d.logger.Error().Err(err).Msg("Failed to save state after retry-all")
	}
	return len(failed)
}
