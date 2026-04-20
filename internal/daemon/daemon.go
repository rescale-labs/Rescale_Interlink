// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/events"
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/reporting"
	"github.com/rescale/rescale-int/internal/services"
	"github.com/rescale/rescale-int/internal/transfer"
	"github.com/rescale/rescale-int/internal/validation"
)

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

	// Shared transfer infrastructure (Plan 3).
	// The daemon is a consumer of TransferService, not a parallel
	// implementation. Per-daemon instance; no cross-process sharing.
	ts     *services.TransferService
	events *events.EventBus
}

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

	// Daemon-scoped EventBus + TransferService. EventBus drives the shared
	// transfer.Queue; IPC serializes from that queue on demand. No external
	// subscribers — the bus exists so the shared transfer path works.
	eventBus := events.NewEventBus(0) // default buffer
	ts := services.NewTransferService(apiClient, eventBus, services.TransferServiceConfig{
		MaxConcurrent: daemonCfg.MaxConcurrent,
	})

	return &Daemon{
		cfg:       daemonCfg,
		appCfg:    appCfg,
		apiClient: apiClient,
		state:     state,
		monitor:   monitor,
		logger:    logger,
		stopChan:  make(chan struct{}),
		ts:        ts,
		events:    eventBus,
	}, nil
}

// TransferService returns the daemon-scoped TransferService. Used by IPC
// handlers to serialize queue state and route cancel/retry actions.
func (d *Daemon) TransferService() *services.TransferService {
	return d.ts
}

// Queue returns the daemon's transfer queue. Convenience accessor for IPC
// handlers that want BatchStats snapshots.
func (d *Daemon) Queue() *transfer.Queue {
	if d.ts == nil {
		return nil
	}
	return d.ts.GetQueue()
}

// DaemonTransferSnapshot projects the daemon's transfer queue state into
// the IPC shape. Filters to SourceLabel=Daemon as a defensive guard even
// though the daemon only ever starts daemon-labeled batches.
func (d *Daemon) DaemonTransferSnapshot() *ipc.DaemonTransferSnapshot {
	if d.ts == nil {
		return &ipc.DaemonTransferSnapshot{}
	}
	queue := d.ts.GetQueue()
	qTasks := queue.GetTasks()
	tasks := make([]ipc.TransferTaskInfo, 0, len(qTasks))
	for i := range qTasks {
		qt := &qTasks[i]
		if qt.SourceLabel != services.SourceLabelDaemon {
			continue
		}
		info := ipc.TransferTaskInfo{
			ID:          qt.ID,
			Type:        string(qt.Type),
			State:       string(qt.State),
			Name:        qt.Name,
			Source:      qt.Source,
			Dest:        qt.Dest,
			Size:        qt.Size,
			Progress:    qt.Progress,
			Speed:       qt.Speed,
			SourceLabel: qt.SourceLabel,
			BatchID:     qt.BatchID,
			BatchLabel:  qt.BatchLabel,
			CreatedAt:   qt.CreatedAt.UnixMilli(),
		}
		if qt.Error != nil {
			info.Error = qt.Error.Error()
		}
		if !qt.StartedAt.IsZero() {
			info.StartedAt = qt.StartedAt.UnixMilli()
		}
		if !qt.CompletedAt.IsZero() {
			info.CompletedAt = qt.CompletedAt.UnixMilli()
		}
		tasks = append(tasks, info)
	}
	qBatches := queue.GetAllBatchStats()
	batches := make([]ipc.BatchStatsInfo, 0, len(qBatches))
	for _, bs := range qBatches {
		if bs.SourceLabel != services.SourceLabelDaemon {
			continue
		}
		out := ipc.BatchStatsInfo{
			BatchID:     bs.BatchID,
			BatchLabel:  bs.BatchLabel,
			Direction:   bs.Direction,
			SourceLabel: bs.SourceLabel,
			Total:       bs.Total,
			Queued:      bs.Queued,
			Active:      bs.Active,
			Completed:   bs.Completed,
			Failed:      bs.Failed,
			Cancelled:   bs.Cancelled,
			TotalBytes:  bs.TotalBytes,
			Progress:    bs.Progress,
			Speed:       bs.Speed,
			TotalKnown:  bs.TotalKnown,
		}
		if !bs.StartedAt.IsZero() {
			out.StartedAt = bs.StartedAt.UnixMilli()
		}
		batches = append(batches, out)
	}
	return &ipc.DaemonTransferSnapshot{Tasks: tasks, Batches: batches}
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

	// Cancel in-flight transfers via the shared queue. Mirrors the GUI's
	// Cancel All path — partial files are tolerated by the shared download
	// path on next run.
	if d.ts != nil {
		d.ts.CancelAll()
	}

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
	d.logger.Info().Msg("Poll started")

	inthttp.WarmupProxyIfNeeded(scanCtx, d.appCfg)
	credentials.GetManager(d.apiClient).WarmAll(scanCtx)

	// Plan 3: tag-retry pass before scan. Jobs downloaded successfully but
	// whose AddJobTag call failed get one tag-retry attempt per poll. On
	// success the pending flag is cleared; on failure it stays and the job
	// is suppressed pre-eligibility (below) so we do not re-download files
	// that are already on disk solely because the tag hasn't been applied.
	if d.cfg.Eligibility != nil {
		for _, jobID := range d.state.PendingTagApplyJobs() {
			if err := d.apiClient.AddJobTag(scanCtx, jobID, config.DownloadedTag); err != nil {
				d.logger.Debug().
					Str("job_id", jobID).
					Err(err).
					Msg("Tag retry failed; will try next poll")
				continue
			}
			d.state.ClearPendingTagApply(jobID)
			d.logger.Info().
				Str("job_id", jobID).
				Str("tag", config.DownloadedTag).
				Msg("Applied downloaded tag on retry")
		}
	}

	// Build the still-pending set AFTER the retry pass. Jobs in this set
	// will be skipped by FindCompletedJobs with ReasonPendingTagApply.
	var pendingSet map[string]struct{}
	if pendingIDs := d.state.PendingTagApplyJobs(); len(pendingIDs) > 0 {
		pendingSet = make(map[string]struct{}, len(pendingIDs))
		for _, id := range pendingIDs {
			pendingSet[id] = struct{}{}
		}
	}

	// Find completed jobs that need downloading
	result, err := d.monitor.FindCompletedJobs(scanCtx, pendingSet)
	if err != nil {
		if scanCtx.Err() == context.DeadlineExceeded {
			d.logger.Error().Dur("duration", time.Since(scanStart)).Msg("Scan timed out after 10 minutes")
		} else {
			d.logger.Error().Msgf("Failed to find completed jobs: %v", err)
		}
		d.logger.Info().Msgf("Poll complete: scanned=unknown, error=%v, duration=%.1fs", err, time.Since(scanStart).Seconds())
		return
	}

	summary := result.Summary
	if summary == nil {
		// Defensive: FindCompletedJobs should always return a summary.
		summary = &ScanSummary{
			TotalScanned:     result.TotalScanned,
			SkipBuckets:      make(map[SkipReasonCode]int),
			DownloadOutcomes: make(map[string]int),
		}
	}

	completed := result.Candidates

	if len(completed) > 0 {
		d.logger.Info().Msgf("Checking %d potential jobs...", len(completed))
	}

	// Check eligibility and download each job. Extend the summary with per-job
	// eligibility skips and download outcomes as we go.
	for _, job := range completed {
		select {
		case <-ctx.Done():
			d.logger.Info().Msg("Scan interrupted by context cancellation")
			d.emitScanSummary(summary, time.Since(scanStart), true)
			return
		case <-d.stopChan:
			d.logger.Info().Msg("Scan interrupted by stop signal")
			d.emitScanSummary(summary, time.Since(scanStart), true)
			return
		default:
		}

		if d.cfg.Eligibility != nil {
			// Per-call timeout prevents a single slow eligibility check from blocking the scan
			eligCtx, eligCancel := context.WithTimeout(ctx, 2*time.Minute)
			eligResult := d.monitor.CheckEligibility(eligCtx, job.ID)
			eligCancel()

			summary.EligibilityChecked++

			if !eligResult.EligibleForDownload {
				summary.AddSkip(eligResult.Reason.Code)
				if !eligResult.Reason.Code.IsSilent() {
					d.logger.Info().Msgf("SKIP: %s [%s] - %s", job.Name, job.ID, eligResult.Detail)
				}
				continue
			}

			// Job is eligible - will download
			d.logger.Info().Msgf("DOWNLOAD: %s [%s] - %s", job.Name, job.ID, eligResult.Detail)
		} else {
			// No eligibility config — every candidate is dispatched straight
			// to download. Count it as "checked" for parity with the configured
			// path.
			summary.EligibilityChecked++
		}

		outcome := d.downloadJob(ctx, job)
		summary.AddOutcome(string(outcome))
	}

	d.emitScanSummary(summary, time.Since(scanStart), false)
	d.checkAllUnsetWarning(summary)

	d.state.UpdateLastPoll()
	if err := d.state.Save(); err != nil {
		d.logger.Error().Err(err).Msg("Failed to save state after poll")
	}
}

// emitScanSummary logs the single canonical per-poll INFO summary line.
// Buckets sum to TotalScanned when interrupted=false. Interrupted polls emit
// partial counts with an interrupted marker so the sum may be less than
// TotalScanned.
func (d *Daemon) emitScanSummary(s *ScanSummary, duration time.Duration, interrupted bool) {
	// Classify download outcomes.
	downloaded := s.DownloadOutcomes[string(OutcomeDownloaded)] + s.DownloadOutcomes[string(OutcomeNoFiles)]
	partial := s.DownloadOutcomes[string(OutcomePartialFailure)]
	interruptedJobs := s.DownloadOutcomes[string(OutcomeInterrupted)]
	listFailed := s.DownloadOutcomes[string(OutcomeListFilesFailed)]
	dirFailed := s.DownloadOutcomes[string(OutcomeOutputDirCreateFailed)]
	failed := partial + listFailed + dirFailed

	// Classify skip buckets as silent vs. logged.
	silentTotal := 0
	loggedTotal := 0
	silentParts := make([]string, 0, len(s.SkipBuckets))
	loggedParts := make([]string, 0, len(s.SkipBuckets))
	for _, code := range scanSummaryReasonOrder {
		n := s.SkipBuckets[code]
		if n == 0 {
			continue
		}
		part := fmt.Sprintf("%s=%d", code, n)
		if code.IsSilent() {
			silentTotal += n
			silentParts = append(silentParts, part)
		} else {
			loggedTotal += n
			loggedParts = append(loggedParts, part)
		}
	}

	silentBreakdown := "none"
	if len(silentParts) > 0 {
		silentBreakdown = strings.Join(silentParts, ",")
	}
	loggedBreakdown := "none"
	if len(loggedParts) > 0 {
		loggedBreakdown = strings.Join(loggedParts, ",")
	}

	interruptedTag := ""
	if interrupted {
		interruptedTag = ", interrupted=true"
	}

	d.logger.Info().Msgf(
		"Poll complete: scanned=%d, eligibility-checked=%d, downloaded=%d, failed=%d (partial=%d, list-failed=%d, dir-failed=%d), interrupted-jobs=%d, silent-skipped=%d (%s), logged-skipped=%d (%s)%s, duration=%.1fs",
		s.TotalScanned,
		s.EligibilityChecked,
		downloaded,
		failed,
		partial, listFailed, dirFailed,
		interruptedJobs,
		silentTotal, silentBreakdown,
		loggedTotal, loggedBreakdown,
		interruptedTag,
		duration.Seconds(),
	)
}

// scanSummaryReasonOrder is the canonical order reasons appear in the scan
// summary log line. Keeping the order stable makes grep/awk pipelines in
// support scripts predictable.
var scanSummaryReasonOrder = []SkipReasonCode{
	ReasonNotCompleted,
	ReasonAlreadyDownloadedLocal,
	ReasonPendingTagApply,
	ReasonTooOldCreationPrefilter,
	ReasonNameFilter,
	ReasonInRetryBackoff,
	ReasonAutoDownloadUnset,
	ReasonAutoDownloadDisabled,
	ReasonAutoDownloadUnrecognized,
	ReasonFieldCheckAPIError,
	ReasonHasDownloadedTag,
	ReasonConditionalMissingTag,
	ReasonDownloadedTagCheckAPIError,
	ReasonConditionalTagCheckAPIError,
	ReasonOutsideLookbackWindow,
	ReasonCompletionTimeAPIError,
}

// checkAllUnsetWarning emits a WARN when every job that actually reached
// CheckEligibility had the "Auto Download" field unset. This is the D2
// signal: it almost always means the workspace is missing the custom field,
// and the user cannot figure that out from the per-poll noise alone.
func (d *Daemon) checkAllUnsetWarning(s *ScanSummary) {
	if s.EligibilityChecked == 0 {
		return
	}
	unset := s.SkipBuckets[ReasonAutoDownloadUnset]
	if unset != s.EligibilityChecked {
		return
	}
	d.logger.Warn().Msgf(
		"All %d eligibility-checked jobs had 'Auto Download' custom field unset — %s. %s",
		s.EligibilityChecked,
		ipc.CanonicalText[ipc.CodeWorkspaceMissingField],
		ipc.HintFor(ipc.CodeWorkspaceMissingField),
	)
}

// DownloadOutcome classifies the job-level result of downloadJob. It drives
// per-poll summary counts (ScanSummary) and lets the caller distinguish
// success from partial failure from interruption without re-deriving it
// from logs or state.
type DownloadOutcome string

const (
	// OutcomeDownloaded — all files for this job downloaded successfully.
	OutcomeDownloaded DownloadOutcome = "downloaded"

	// OutcomeNoFiles — job had no files to download (empty output set).
	OutcomeNoFiles DownloadOutcome = "no_files"

	// OutcomeOutputDirCreateFailed — could not create the output directory
	// (usually a mapped-drive or permissions issue).
	OutcomeOutputDirCreateFailed DownloadOutcome = "output_dir_create_failed"

	// OutcomeListFilesFailed — the ListJobFiles API call failed.
	OutcomeListFilesFailed DownloadOutcome = "list_files_failed"

	// OutcomePartialFailure — at least one file failed or was skipped as
	// invalid; the job is not marked Downloaded and will retry per backoff.
	OutcomePartialFailure DownloadOutcome = "partial_failure"

	// OutcomeInterrupted — the context was cancelled (stop signal or parent
	// context) partway through the job.
	OutcomeInterrupted DownloadOutcome = "interrupted"
)

// downloadJob downloads all files from a completed job through the shared
// TransferService, the same infrastructure the GUI File Browser uses.
// Returns a DownloadOutcome so the per-poll summary can distinguish
// succeeded / no-files / failed / interrupted / partial outcomes without
// re-reading state or logs.
//
// Plan 3: the daemon is a consumer of TransferService, not a parallel
// implementation. Worker pools, resource management, progress tracking,
// and cancellation all live in the shared queue.
func (d *Daemon) downloadJob(ctx context.Context, job *CompletedJob) DownloadOutcome {
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

	outputDir := ComputeOutputDir(baseDir, job.ID, job.Name, d.cfg.UseJobNameDir)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		d.logger.Error().Err(err).Str("dir", outputDir).Msg("Failed to create output directory")
		d.state.MarkFailed(job.ID, job.Name, err)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		reporting.HandleCLIError(err, "daemon", "job_download", "")
		return OutcomeOutputDirCreateFailed
	}

	files, err := d.apiClient.ListJobFiles(ctx, job.ID)
	if err != nil {
		d.logger.Error().Err(err).Str("job_id", job.ID).Msg("Failed to list job files")
		d.state.MarkFailed(job.ID, job.Name, err)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		reporting.HandleCLIError(err, "daemon", "job_download", "")
		return OutcomeListFilesFailed
	}

	if len(files) == 0 {
		d.logger.Info().Str("job_id", job.ID).Msg("No files to download for job")
		d.state.MarkDownloaded(job.ID, job.Name, outputDir, 0, 0)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		return OutcomeNoFiles
	}

	d.logger.Info().
		Str("job_id", job.ID).
		Int("file_count", len(files)).
		Msg("Downloading job files")

	batchID := "daemon:" + job.ID
	batchLabel := "Auto: " + job.Name
	reqCh := make(chan services.TransferRequest, 16)
	scanCtx, scanCancel := context.WithCancel(ctx)
	defer scanCancel()

	if err := d.ts.StartStreamingDownloadBatch(scanCtx, reqCh, batchID, batchLabel, services.SourceLabelDaemon, scanCancel); err != nil {
		d.logger.Error().Err(err).Str("job_id", job.ID).Msg("Failed to start download batch")
		d.state.MarkFailed(job.ID, job.Name, err)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		return OutcomePartialFailure
	}

	// Dispatch files onto the queue. This goroutine closes reqCh when done,
	// which flips TotalKnown=true so WaitForBatch knows registration is
	// complete. Files already present on disk with correct size are counted
	// toward downloadedCount/totalSize but not pushed to the queue (shared
	// download path does not short-circuit correct-size local files).
	var totalSize int64
	var alreadyPresent int
	go func() {
		defer close(reqCh)
		for i := range files {
			f := files[i]

			if err := validation.ValidateFilename(f.Name); err != nil {
				d.logger.Warn().
					Str("file_id", f.ID).
					Str("file_name", f.Name).
					Err(err).
					Msg("Skipping file with invalid name")
				continue
			}

			var localPath string
			if f.RelativePath != "" {
				if err := validation.ValidatePathInDirectory(f.RelativePath, outputDir); err == nil {
					localPath = filepath.Join(outputDir, f.RelativePath)
				} else {
					localPath = filepath.Join(outputDir, f.Name)
				}
			} else {
				localPath = filepath.Join(outputDir, f.Name)
			}

			if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
				d.logger.Error().Err(err).Str("path", localPath).Msg("Failed to create file directory")
				continue
			}

			if info, statErr := os.Stat(localPath); statErr == nil && info.Size() == f.DecryptedSize {
				d.logger.Debug().Str("path", localPath).Msg("File already exists with correct size, skipping")
				alreadyPresent++
				totalSize += f.DecryptedSize
				continue
			}

			req := services.TransferRequest{
				Type:        services.TransferTypeDownload,
				Source:      f.ID,
				Dest:        localPath,
				Name:        f.Name,
				Size:        f.DecryptedSize,
				SourceLabel: services.SourceLabelDaemon,
				BatchID:     batchID,
				BatchLabel:  batchLabel,
			}
			select {
			case reqCh <- req:
			case <-scanCtx.Done():
				return
			}
		}
	}()

	stats, waitErr := d.ts.WaitForBatch(ctx, batchID)
	if waitErr != nil {
		d.logger.Error().Err(waitErr).Str("job_id", job.ID).Msg("Job download interrupted")
		d.state.MarkFailed(job.ID, job.Name, waitErr)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		reporting.HandleCLIError(waitErr, "daemon", "job_download", "")
		return OutcomeInterrupted
	}

	// Partial-failure path: any failed or cancelled tasks mean the job is
	// incomplete and will retry on the next poll cycle.
	var outcome DownloadOutcome
	if stats.Failed > 0 || stats.Cancelled > 0 {
		failErr := fmt.Errorf("%d failed + %d cancelled of %d files",
			stats.Failed, stats.Cancelled, stats.Total+alreadyPresent)
		d.logger.Warn().
			Str("job_id", job.ID).
			Int("failed_files", stats.Failed).
			Int("cancelled_files", stats.Cancelled).
			Int("total_files", stats.Total+alreadyPresent).
			Msg("Job incomplete, marking as failed for retry")
		d.state.MarkFailed(job.ID, job.Name, failErr)
		outcome = OutcomePartialFailure
	} else {
		// Add queue-completed bytes to totalSize (already-present files were
		// added above as we skipped dispatch).
		for _, f := range files {
			if info, statErr := os.Stat(filepath.Join(outputDir, f.Name)); statErr == nil && info.Size() == f.DecryptedSize {
				// Already counted if dispatched path matched; avoid double-count
				// by using a clean recompute below.
				_ = info
			}
		}
		// Recompute totalSize from source-of-truth file list (all files succeeded).
		totalSize = 0
		for _, f := range files {
			totalSize += f.DecryptedSize
		}
		fileCount := stats.Completed + alreadyPresent
		d.state.MarkDownloaded(job.ID, job.Name, outputDir, fileCount, totalSize)

		// Tag the job as downloaded. On failure, MarkPendingTagApply so the
		// poll loop retries just the tag call (without re-downloading files).
		if d.cfg.Eligibility != nil {
			if err := d.apiClient.AddJobTag(ctx, job.ID, config.DownloadedTag); err != nil {
				d.logger.Warn().
					Err(err).
					Str("job_id", job.ID).
					Str("tag", config.DownloadedTag).
					Msg("Failed to tag job as downloaded (will retry on next poll)")
				d.state.MarkPendingTagApply(job.ID)
			} else {
				d.logger.Debug().
					Str("job_id", job.ID).
					Str("tag", config.DownloadedTag).
					Msg("Tagged job as downloaded")
			}
		}

		d.logger.Info().Msgf("COMPLETED: %s [%s] - %d files, %s",
			job.Name, job.ID, fileCount, formatBytes(totalSize))
		outcome = OutcomeDownloaded
	}

	if saveErr := d.state.Save(); saveErr != nil {
		d.logger.Error().Err(saveErr).Msg("Failed to persist state")
	}

	return outcome
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

// GetActiveDownloads returns the number of downloads currently in progress,
// derived from the shared transfer queue (Plan 3: no daemon-local counter).
func (d *Daemon) GetActiveDownloads() int {
	if d.ts == nil {
		return 0
	}
	stats := d.ts.GetStats()
	return stats.Queued + stats.Initializing + stats.Active
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

