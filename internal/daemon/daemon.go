// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
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

// scanBudget bounds one poll's scan phase: listing jobs and checking their
// eligibility. It must exceed the HTTP client timeout (300s) so a slow call can
// still be retried. Downloads are not covered by it — see poll().
const scanBudget = 10 * time.Minute

// stateRetentionBufferDays extends state retention past the lookback window by
// the same margin FindCompletedJobs uses for its creation-date pre-filter, so an
// entry is only dropped once no scan can select the job again.
const stateRetentionBufferDays = 30

// daemonBatchHistoryLimit caps how many finished download batches keep their
// tasks in the shared transfer queue. The queue never removes terminal tasks, so
// a daemon polling for weeks would accumulate one task per downloaded file
// forever. Older batches are dropped once this many newer ones exist, which
// keeps recent auto-downloads visible in the Transfers tab.
const daemonBatchHistoryLimit = 20

// DefaultConfig returns a daemon configuration with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		PollInterval:  5 * time.Minute,
		DownloadDir:   ".",
		UseJobNameDir: true,
		MaxConcurrent: constants.DefaultMaxConcurrent,
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

	// batchSeq makes every download attempt's batch ID unique. The queue never
	// removes terminal tasks, and BatchStats aggregates every task that ever
	// carried a batch ID, so a stable per-job ID would make attempt N inherit
	// attempts 1..N-1's failures and the job could never be recorded as
	// downloaded again.
	batchSeq atomic.Uint64

	// Most recent scan failure, surfaced over IPC. Guarded separately from mu
	// so status reads never contend with the lifecycle lock.
	scanErrMu     sync.RWMutex
	lastScanErr   string
	lastScanErrAt time.Time

	// Finished download batches in completion order, capped at
	// daemonBatchHistoryLimit; see retireOldBatches.
	batchHistMu sync.Mutex
	batchHist   []string

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

	// Bound the state file. Once a job's completion falls outside the lookback
	// window plus the API pre-filter buffer, no scan can select it again, so its
	// entry only grows the file — and this file is loaded and rewritten on every
	// poll for the daemon's whole lifetime.
	if daemonCfg.Eligibility != nil && daemonCfg.Eligibility.LookbackDays > 0 {
		retentionDays := daemonCfg.Eligibility.LookbackDays + stateRetentionBufferDays
		state.SetRetention(time.Duration(retentionDays) * 24 * time.Hour)
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

	// Cancel in-flight transfers via the shared queue. This is the queue-wide
	// sweep: it cancels every registered batch context and every non-terminal
	// task. (The GUI's "Cancel All" button instead iterates the visible batches
	// and calls CancelBatch per batch.) Partial files are tolerated by the
	// shared download path on next run.
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

	// scanCtx bounds the *scan* — listing jobs and checking their eligibility.
	// It deliberately does not reach the downloads themselves: a single large
	// file can legitimately take longer than any scan budget, and killing it
	// mid-transfer restarts it from zero on the next poll, forever.
	scanCtx, cancel := context.WithTimeout(ctx, scanBudget)
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
			d.logger.Error().Dur("duration", time.Since(scanStart)).Dur("budget", scanBudget).Msg("Scan timed out")
		} else {
			d.logger.Error().Msgf("Failed to find completed jobs: %v", err)
		}
		d.recordScanError(err)
		// Same canonical line as every other poll, with an error tag: support
		// scripts grep one format, not two.
		d.emitScanSummary(&ScanSummary{
			SkipBuckets:      make(map[SkipReasonCode]int),
			DownloadOutcomes: make(map[string]int),
		}, time.Since(scanStart), false, err)
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
	//
	// totalDownloadTime accumulates the time spent inside downloadJob so the
	// budget check below can subtract it. Transferring a large file is allowed
	// to take longer than the entire budget; charging that to the scan turned a
	// poll that worked perfectly into a standing "scan failed" error, which is
	// exactly the false alarm that makes a real one easy to ignore.
	var totalDownloadTime time.Duration
	for _, job := range completed {
		select {
		case <-ctx.Done():
			// Daemon shutting down, not a failure.
			d.logger.Info().Msg("Scan interrupted by context cancellation")
			d.emitScanSummary(summary, time.Since(scanStart), true, nil)
			return
		case <-d.stopChan:
			// Shutdown, not a failure. Stop() saves state on the way out.
			d.logger.Info().Msg("Scan interrupted by stop signal")
			d.emitScanSummary(summary, time.Since(scanStart), true, nil)
			return
		default:
		}

		// The scan phase outrunning its budget is a health problem the user
		// needs to see: before it was recorded, this path froze LastScanTime
		// while the daemon still reported "running". Note this is measured on
		// scan work only — see totalDownloadTime above.
		if scanBudgetExceeded(time.Since(scanStart), totalDownloadTime, scanBudget) {
			budgetErr := fmt.Errorf("scan did not finish within %s", scanBudget)
			d.logger.Warn().
				Dur("budget", scanBudget).
				Dur("download_time", totalDownloadTime).
				Msg("Scan budget exhausted; remaining jobs wait for the next poll")
			d.recordScanError(budgetErr)
			d.emitScanSummary(summary, time.Since(scanStart), true, budgetErr)
			// This poll did real work — jobs were checked and possibly
			// downloaded — so its progress is persisted and its timestamp
			// advances. The recorded error is what says it was cut short.
			d.persistPollProgress()
			return
		}

		if d.cfg.Eligibility != nil {
			// Per-call timeout prevents a single slow eligibility check from
			// blocking the scan. Parented on the lifecycle context, not scanCtx:
			// a legitimately long download can push the poll past the scan
			// deadline, and an eligibility check must not inherit a context that
			// is already dead and fail every job after it.
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

		// ctx, not scanCtx: the download gets the daemon's lifecycle context, so
		// it ends when the daemon stops or the batch is cancelled — never
		// because the scan budget elapsed. A 20GB file at 10MB/s needs half an
		// hour, and a partial file never matches the expected size, so a
		// budget-killed download restarts from zero on every poll.
		downloadStart := time.Now()
		outcome := d.downloadJob(ctx, job)
		totalDownloadTime += time.Since(downloadStart)
		summary.AddOutcome(string(outcome))
	}

	d.emitScanSummary(summary, time.Since(scanStart), false, nil)
	d.checkAllUnsetWarning(summary)

	d.clearScanError()
	d.persistPollProgress()
}

// scanBudgetExceeded reports whether a poll's scan work alone has outrun its
// budget. Time spent transferring files is subtracted first: a single large file
// may legitimately run longer than the whole budget, and counting that as scan
// time reported a healthy poll as a failed one.
//
// downloadTime greater than elapsed cannot happen from these measurements, but
// is treated as "nothing to charge" rather than trusted into a negative.
func scanBudgetExceeded(elapsed, downloadTime, budget time.Duration) bool {
	scanTime := elapsed - downloadTime
	if scanTime < 0 {
		return false
	}
	return scanTime > budget
}

// persistPollProgress stamps the poll time and writes state to disk. Called by
// every poll that ran to completion or did partial work before being cut short.
func (d *Daemon) persistPollProgress() {
	d.state.UpdateLastPoll()
	if err := d.state.Save(); err != nil {
		d.logger.Error().Err(err).Msg("Failed to save state after poll")
	}
}

// emitScanSummary logs the single canonical per-poll INFO summary line. Three
// shapes, distinguished by the interrupted and error markers:
//
//   - Complete: no markers, and the buckets sum to TotalScanned.
//   - Interrupted: interrupted=true with partial counts, so the buckets may sum
//     to less than TotalScanned. Ends a poll cut short by shutdown.
//   - Interrupted and failed: interrupted=true plus a quoted error, again with
//     partial counts. Ends a poll whose scan phase outran its budget.
//   - Failed outright: a quoted error with every count zero. The scan never got
//     past listing jobs.
func (d *Daemon) emitScanSummary(s *ScanSummary, duration time.Duration, interrupted bool, scanErr error) {
	// Classify download outcomes. no_files is reported separately from
	// downloaded: a completed job with an empty output set is not a download,
	// and folding it in made the downloaded count unfalsifiable.
	downloaded := s.DownloadOutcomes[string(OutcomeDownloaded)]
	noFiles := s.DownloadOutcomes[string(OutcomeNoFiles)]
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

	// The error is free-form text that routinely contains commas (wrapped
	// errors, HTTP bodies). Every other field on this line is comma-delimited
	// for grep/awk pipelines, so the error goes last and quoted — nothing a
	// splitter needs to read follows it.
	errorTag := ""
	if scanErr != nil {
		errorTag = fmt.Sprintf(", error=%q", scanErr.Error())
	}

	d.logger.Info().Msgf(
		"Poll complete: scanned=%d, eligibility-checked=%d, downloaded=%d, no_files=%d, failed=%d (partial=%d, list-failed=%d, dir-failed=%d), interrupted-jobs=%d, silent-skipped=%d (%s), logged-skipped=%d (%s)%s, duration=%.1fs%s",
		s.TotalScanned,
		s.EligibilityChecked,
		downloaded,
		noFiles,
		failed,
		partial, listFailed, dirFailed,
		interruptedJobs,
		silentTotal, silentBreakdown,
		loggedTotal, loggedBreakdown,
		interruptedTag,
		duration.Seconds(),
		errorTag,
	)
}

// recordScanError stores the most recent scan failure so IPC status consumers
// (CLI `daemon status`, GUI Setup tab) can tell a healthy daemon apart from one
// that is alive but failing every scan. Without it the only symptom is a
// LastScan timestamp that silently stops advancing.
func (d *Daemon) recordScanError(err error) {
	if err == nil {
		return
	}
	d.scanErrMu.Lock()
	d.lastScanErr = err.Error()
	d.lastScanErrAt = time.Now()
	d.scanErrMu.Unlock()
}

// clearScanError clears the recorded scan failure after a scan completes.
func (d *Daemon) clearScanError() {
	d.scanErrMu.Lock()
	d.lastScanErr = ""
	d.lastScanErrAt = time.Time{}
	d.scanErrMu.Unlock()
}

// LastScanError returns the most recent scan failure and when it happened.
// Empty string means the last completed scan succeeded.
func (d *Daemon) LastScanError() (string, time.Time) {
	d.scanErrMu.RLock()
	defer d.scanErrMu.RUnlock()
	return d.lastScanErr, d.lastScanErrAt
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
		// Tag it like any other finished job. Without the tag the job passes
		// the tag-first eligibility check on every subsequent poll and is
		// re-processed forever.
		d.applyDownloadedTag(ctx, job)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		return OutcomeNoFiles
	}

	d.logger.Info().
		Str("job_id", job.ID).
		Int("file_count", len(files)).
		Msg("Downloading job files")

	// The batch ID is unique per attempt so this attempt's stats cannot inherit
	// an earlier one's failures. The label carries the attempt number instead of
	// the sequence, because the sequence counts every batch the daemon has ever
	// started — the user needs to tell repeated attempts at *this* job apart.
	batchID := fmt.Sprintf("daemon:%s:%d", job.ID, d.batchSeq.Add(1))
	batchLabel := "Auto: " + job.Name
	if attempt := d.state.AttemptCount(job.ID) + 1; attempt > 1 {
		batchLabel = fmt.Sprintf("%s (attempt %d)", batchLabel, attempt)
	}

	reqCh := make(chan services.TransferRequest, 16)
	// batchCtx is derived from the daemon's lifecycle context, so downloads end
	// on daemon shutdown or an explicit batch cancel, and never on a scan
	// deadline. batchCancel is registered as the batch's cancel function, which
	// is how CancelBatch stops a scan that is still dispatching.
	batchCtx, batchCancel := context.WithCancel(ctx)
	defer batchCancel()

	if err := d.ts.StartStreamingDownloadBatch(batchCtx, reqCh, batchID, batchLabel, services.SourceLabelDaemon, batchCancel); err != nil {
		d.logger.Error().Err(err).Str("job_id", job.ID).Msg("Failed to start download batch")
		d.state.MarkFailed(job.ID, job.Name, err)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		return OutcomePartialFailure
	}

	// Every path from here on leaves a batch in the shared queue, whether the
	// job succeeded, failed, or was interrupted. Register it for retirement on
	// all of them: a job that fails every poll produces a batch every poll.
	defer d.retireOldBatches(batchID)

	// Dispatch files onto the queue. This goroutine closes reqCh when done,
	// which flips TotalKnown=true so WaitForBatch knows registration is
	// complete. Files already present on disk with correct size are counted
	// toward downloadedCount/totalSize but not pushed to the queue (shared
	// download path does not short-circuit correct-size local files).
	var totalSize int64
	var alreadyPresent int
	var dispatched int
	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
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
				dispatched++
			case <-batchCtx.Done():
				return
			}
		}
	}()

	// reqCh is closed before dispatchDone (defers run last-in-first-out), so by
	// the time this returns every request has been handed over. That is the
	// guarantee WaitForRegisteredBatch needs, and it also makes the dispatch
	// counters safe to read.
	<-dispatchDone

	var stats transfer.BatchStats
	if dispatched > 0 {
		var waitErr error
		stats, waitErr = d.ts.WaitForRegisteredBatch(ctx, batchID)
		if waitErr != nil {
			d.logger.Error().Err(waitErr).Str("job_id", job.ID).Msg("Job download interrupted")
			d.state.MarkFailed(job.ID, job.Name, waitErr)
			if saveErr := d.state.Save(); saveErr != nil {
				d.logger.Error().Err(saveErr).Msg("Failed to persist state")
			}
			// A vanished batch means someone cancelled it before its first task
			// registered. That is a user action, not a fault to report.
			if !errors.Is(waitErr, services.ErrBatchVanished) {
				reporting.HandleCLIError(waitErr, "daemon", "job_download", "")
			}
			return OutcomeInterrupted
		}
	} else if alreadyPresent == 0 {
		// Nothing dispatched and nothing on disk: every file was rejected by
		// name validation or its directory could not be created. Record a
		// failure instead of claiming success.
		noneErr := fmt.Errorf("no downloadable files of %d (all skipped)", len(files))
		d.logger.Warn().Str("job_id", job.ID).Int("total_files", len(files)).
			Msg("Job had no downloadable files, marking as failed for retry")
		d.state.MarkFailed(job.ID, job.Name, noneErr)
		if saveErr := d.state.Save(); saveErr != nil {
			d.logger.Error().Err(saveErr).Msg("Failed to persist state")
		}
		return OutcomePartialFailure
	}
	// dispatched == 0 with files already on disk falls through with zero-valued
	// stats. There is nothing to wait for: the batch registered no tasks, so
	// waiting could only report a batch the queue has already forgotten.

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
		d.applyDownloadedTag(ctx, job)

		d.logger.Info().Msgf("COMPLETED: %s [%s] - %d files, %s",
			job.Name, job.ID, fileCount, cloud.FormatBytes(totalSize))
		outcome = OutcomeDownloaded
	}

	if saveErr := d.state.Save(); saveErr != nil {
		d.logger.Error().Err(saveErr).Msg("Failed to persist state")
	}

	return outcome
}

// retireOldBatches records a finished download batch and drops the terminal
// tasks of any batch beyond the most recent daemonBatchHistoryLimit.
//
// The shared queue never removes terminal tasks, so a daemon polling for weeks
// accumulates one task per downloaded file for its whole lifetime. Retiring the
// oldest batches instead of clearing on completion keeps recent auto-downloads
// visible in the Transfers tab.
func (d *Daemon) retireOldBatches(batchID string) {
	if batchID == "" || d.ts == nil {
		return
	}

	d.batchHistMu.Lock()
	d.batchHist = append(d.batchHist, batchID)
	var retire []string
	if excess := len(d.batchHist) - daemonBatchHistoryLimit; excess > 0 {
		retire = append(retire, d.batchHist[:excess]...)
		d.batchHist = append([]string(nil), d.batchHist[excess:]...)
	}
	d.batchHistMu.Unlock()

	for _, old := range retire {
		if removed := d.ts.ClearBatchTerminalTasks(old); removed > 0 {
			d.logger.Debug().
				Str("batch_id", old).
				Int("tasks_removed", removed).
				Msg("Retired old daemon transfer batch from the queue")
		}
	}
}

// applyDownloadedTag tags a finished job so the tag-first eligibility check
// stops re-selecting it. On failure the job is flagged PendingTagApply and the
// poll loop retries just the tag call, without re-downloading files. No-op when
// eligibility checking is disabled (no tags are consulted in that mode).
func (d *Daemon) applyDownloadedTag(ctx context.Context, job *CompletedJob) {
	if d.cfg.Eligibility == nil {
		return
	}
	if err := d.apiClient.AddJobTag(ctx, job.ID, config.DownloadedTag); err != nil {
		d.logger.Warn().
			Err(err).
			Str("job_id", job.ID).
			Str("tag", config.DownloadedTag).
			Msg("Failed to tag job as downloaded (will retry on next poll)")
		d.state.MarkPendingTagApply(job.ID)
		return
	}
	d.logger.Debug().
		Str("job_id", job.ID).
		Str("tag", config.DownloadedTag).
		Msg("Tagged job as downloaded")
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
//
// Returns an error when no scan was started, so callers can report the truth
// rather than a blanket success. A poll already in progress is the ordinary
// case, but it is also what a wedged poll looks like — either way the caller's
// scan did not happen.
func (d *Daemon) TriggerPoll() error {
	d.mu.RLock()
	if !d.running {
		d.mu.RUnlock()
		return fmt.Errorf("daemon is not running")
	}
	if d.paused.Load() {
		d.mu.RUnlock()
		d.logger.Debug().Msg("Daemon paused, ignoring manual trigger")
		return fmt.Errorf("daemon is paused")
	}
	if d.polling.Load() {
		d.mu.RUnlock()
		d.logger.Debug().Msg("Poll already in progress, ignoring manual trigger")
		return fmt.Errorf("a scan is already in progress")
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
	return nil
}
