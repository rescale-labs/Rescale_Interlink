// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/credentials"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/events"
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
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

	// When set, jobs must pass eligibility checks to be downloaded
	Eligibility *EligibilityConfig

	// FlattenFolderStructure, when true, downloads workspace-folder jobs
	// directly into DownloadDir instead of mirroring the folder tree. Only
	// affects jobs that carry a workspace-folder path (CompletedJob.FolderPath).
	FlattenFolderStructure bool
}

// scanBudget bounds one poll's scan phase: listing jobs and checking their
// eligibility. It must exceed the HTTP client timeout (300s) so a slow call can
// still be retried. Downloads are not covered by it — see poll(). A variable
// so a test can shorten it.
var scanBudget = 10 * time.Minute

// claimLease is how long a started tag holds a job. Past it any client may
// take the job over, so a client that crashed or was removed holds none for
// ever. A day is longer than one job's download is expected to take, and
// leaves the job well inside the default lookback.
const claimLease = 24 * time.Hour

// claimAhead is how far ahead of this client's clock a started tag's time may
// be and still hold its job.
const claimAhead = 10 * time.Minute

// claimSettle bounds how long putting a started tag on may take, and how far
// apart two clients' clocks may be, for their claims to be ordered right; see
// claim. A variable so a test can shorten it.
var claimSettle = 2 * time.Second

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

// startDownloadBatch starts the shared queue's registration and transfer of the
// requests downloadJob hands over. A variable so a test can cancel a batch
// between handing a request over and its registration.
var startDownloadBatch = (*services.TransferService).StartStreamingDownloadBatch

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

// CheckMaxConcurrent refuses a max_concurrent 'daemon run' does not accept,
// naming where it came from. It is a usage error: the user set it.
func CheckMaxConcurrent(n int, source string) error {
	if n < constants.MinMaxConcurrent || n > constants.MaxMaxConcurrent {
		return reporting.UsageError(fmt.Errorf("%s must be between %d and %d, got %d",
			source, constants.MinMaxConcurrent, constants.MaxMaxConcurrent, n))
	}
	return nil
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

	// lastChecked is when each job the last poll found was last checked, so
	// a poll checks first the jobs checked longest ago. Only poll uses it.
	lastChecked map[string]time.Time

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

	// Files this daemon has already checksummed against their remote file,
	// keyed by local path. A poll happens every few minutes over output
	// directories that run to many gigabytes, so hashing every adopted file on
	// every cycle is not a cost the daemon can carry; the recorded size and
	// modification time expire the entry as soon as the file changes.
	verifiedMu sync.Mutex
	verified   map[string]verifiedFile

	// hashLocalFile computes a file's SHA-512. Nil, as every caller leaves it,
	// means encryption.CalculateSHA512. A seam for the daemon's own tests, which
	// need to count how often a file is read.
	hashLocalFile func(path string) (string, error)
}

// verifiedFile records that a local file hashed to the remote file's checksum,
// together with what the file looked like at the time.
type verifiedFile struct {
	size     int64
	modTime  time.Time
	checksum string // the remote checksum it was verified against
}

// alreadyDownloaded reports whether the file already at localPath is the remote
// file f, so this poll can skip it.
//
// The length used to be the whole test, and a file of the right length is
// exactly what an interrupted download leaves behind: a pre-allocated
// destination, or one written up to the part that failed, is full-size and
// holed. Adopting that file is permanent — every later poll adopts it too, so
// the job stays recorded as downloaded and the real bytes are never fetched.
//
// So when the remote file carries a SHA-512 checksum, the file has to hash to
// it before it is adopted. Without one, the length remains the only check
// available, and it is the same check every other presence test in the product
// makes.
func (d *Daemon) alreadyDownloaded(localPath string, f models.JobFile) bool {
	info, statErr := os.Stat(localPath)
	if statErr != nil || info.Size() != f.DecryptedSize {
		return false
	}

	// Other algorithms are not a substitute: the download path verifies SHA-512
	// only, so anything else leaves the length as the only check.
	expected := f.FileChecksums.SHA512()
	if expected == "" {
		if algorithms := f.FileChecksums.Algorithms(); len(algorithms) > 0 {
			d.logger.Warn().Str("path", localPath).Strs("checksums", algorithms).
				Msg("File already exists with correct size, skipping it unverified: its checksums include no SHA-512")
		} else {
			d.logger.Debug().Str("path", localPath).Msg("File already exists with correct size, skipping")
		}
		return true
	}

	if d.isVerified(localPath, info, expected) {
		d.logger.Debug().Str("path", localPath).Msg("File already verified against its checksum, skipping")
		return true
	}

	actual, hashErr := d.hashFile(localPath)
	if hashErr != nil {
		// Unreadable is not verified: fetch it again rather than adopt a file
		// nothing could check.
		d.logger.Warn().Err(hashErr).Str("path", localPath).
			Msg("Could not checksum the existing file, downloading it again")
		return false
	}
	if !strings.EqualFold(actual, expected) {
		d.logger.Warn().Str("path", localPath).
			Msg("Existing file is the right size but fails its checksum, downloading it again")
		return false
	}

	d.rememberVerified(localPath, info, expected)
	d.logger.Debug().Str("path", localPath).Msg("File already exists and matches its checksum, skipping")
	return true
}

// isVerified reports whether this daemon already checksummed exactly this file
// against exactly this checksum. Size and modification time are what make the
// answer expire: a file rewritten since is a different file, and a remote file
// given new contents carries a new checksum.
func (d *Daemon) isVerified(localPath string, info os.FileInfo, expected string) bool {
	d.verifiedMu.Lock()
	defer d.verifiedMu.Unlock()

	entry, ok := d.verified[localPath]
	return ok && entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) && entry.checksum == expected
}

func (d *Daemon) rememberVerified(localPath string, info os.FileInfo, expected string) {
	d.verifiedMu.Lock()
	defer d.verifiedMu.Unlock()

	if d.verified == nil {
		d.verified = make(map[string]verifiedFile)
	}
	d.verified[localPath] = verifiedFile{size: info.Size(), modTime: info.ModTime(), checksum: expected}
}

// hashFile computes a file's SHA-512 through whatever the daemon was given.
func (d *Daemon) hashFile(path string) (string, error) {
	if d.hashLocalFile != nil {
		return d.hashLocalFile(path)
	}
	return encryption.CalculateSHA512(path)
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
	if err := state.open(); err != nil {
		return nil, fmt.Errorf("failed to load state: %w", err)
	}
	if state.upgraded > 0 {
		logger.Info().Msgf("%d failed download(s) recorded by an earlier version will each get one more attempt", state.upgraded)
	}

	// Bound the state file. The daemon reads it once, here, and then holds it in
	// memory and rewrites it for the rest of its life — every poll, every
	// download outcome, and shutdown. Entries are what grow it, and an entry is
	// only useful while a scan could still pick the job up: the retention window
	// is the lookback window plus the same margin FindCompletedJobs allows its
	// creation-date pre-filter, so pruning is anchored to when the daemon
	// downloaded a job and selection to the platform's own creation and
	// completion timestamps for it.
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
	monitor.flatten = daemonCfg.FlattenFolderStructure

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
			info.Error = reporting.RedactSecrets(qt.Error.Error())
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
	d.wg.Add(1) // before Stop can see running: it waits for the poll loop below
	d.mu.Unlock()

	d.logger.Info().
		Str("download_dir", d.cfg.DownloadDir).
		Str("poll_interval", d.cfg.PollInterval.String()).
		Msg("Daemon starting")

	// Run initial poll immediately
	d.poll(d.lifecycleCtx)

	// Start polling loop
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

// pollDelay is the wait before the next poll: the interval, plus up to a tenth
// of it, 30 s at most, at random. Clients started together so drift apart,
// rather than claiming the same jobs at the same moment poll after poll.
func pollDelay(interval time.Duration) time.Duration {
	if spread := min(interval/10, 30*time.Second); spread > 0 {
		return interval + rand.N(spread)
	}
	return interval
}

// pollLoop runs the periodic polling.
func (d *Daemon) pollLoop(ctx context.Context) {
	defer d.wg.Done()

	timer := time.NewTimer(pollDelay(d.cfg.PollInterval))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info().Msg("Poll loop cancelled by context")
			return
		case <-d.stopChan:
			d.logger.Info().Msg("Poll loop stopped")
			return
		case <-timer.C:
			timer.Reset(pollDelay(d.cfg.PollInterval))
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
			d.releaseStarted(jobID)
			d.logger.Info().
				Str("job_id", jobID).
				Str("tag", config.DownloadedTag).
				Msg("Applied downloaded tag on retry")
		}
		// A started tag an ended attempt left on, by crashing or failing to
		// take it off, comes off now: no job downloads between polls.
		for _, jobID := range d.state.StartedToRemove() {
			d.releaseStarted(jobID)
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

	// Take in any 'daemon retry' run since the last poll: it edits the state
	// file, and this daemon holds its state in memory.
	if err := d.state.Save(); err != nil {
		d.logger.Error().Err(err).Msg("Failed to persist state")
	}

	// Find completed jobs that need downloading
	result, err := d.monitor.FindCompletedJobs(scanCtx, pendingSet)
	if ctx.Err() != nil {
		// Daemon shutting down, not a failure.
		d.logger.Info().Msg("Scan interrupted by context cancellation")
		d.emitScanSummary(&ScanSummary{}, time.Since(scanStart), true, nil)
		return
	}
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

	// New jobs first, then those checked longest ago: a poll that runs out
	// of budget leaves the rest to the next, and this way every job gets its
	// turn however many there are.
	checked := make(map[string]time.Time, len(completed))
	if result.WorkspaceErr != nil {
		// Jobs in workspace folders that could not be listed went unlisted,
		// not away: they keep their turns.
		maps.Copy(checked, d.lastChecked)
	}
	for _, job := range completed {
		checked[job.ID] = d.lastChecked[job.ID]
	}
	d.lastChecked = checked
	slices.SortStableFunc(completed, func(a, b *CompletedJob) int { return checked[a.ID].Compare(checked[b.ID]) })

	if len(completed) > 0 {
		d.logger.Info().Msgf("Checking %d potential jobs...", len(completed))
	}

	// Check eligibility and download each job. Extend the summary with per-job
	// eligibility skips and download outcomes as we go.
	//
	// totalDownloadTime accumulates the time spent claiming jobs and inside
	// downloadJob so the budget check below can subtract it: a claim waits
	// seconds for other clients' claims to show, which a backlog of jobs would
	// otherwise add up past the budget. Transferring a large file is allowed
	// to take longer than the entire budget; charging that to the scan turned a
	// poll that worked perfectly into a standing "scan failed" error, which is
	// exactly the false alarm that makes a real one easy to ignore.
	var totalDownloadTime time.Duration
	for i, job := range completed {
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

		// Out of budget, here or while the scan looked jobs up: the jobs left
		// wait for the next poll, which checks them first. The poll is partial,
		// not failed, and says how many it left. The budget is measured on scan
		// work only — see totalDownloadTime above.
		if summary.Unchecked > 0 || scanBudgetExceeded(time.Since(scanStart), totalDownloadTime, scanBudget) {
			summary.Unchecked += len(completed) - i
			break
		}
		checked[job.ID] = time.Now()

		if d.cfg.Eligibility != nil {
			// Per-call timeout prevents a single slow eligibility check from
			// blocking the scan. Parented on the lifecycle context, not scanCtx:
			// a legitimately long download can push the poll past the scan
			// deadline, and an eligibility check must not inherit a context that
			// is already dead and fail every job after it.
			eligCtx, eligCancel := context.WithTimeout(ctx, 2*time.Minute)
			eligResult := d.monitor.CheckEligibility(eligCtx, job)
			eligCancel()

			summary.EligibilityChecked++

			// An eligible job is claimed from other clients before anything
			// is written for it.
			reason, eligible := eligResult.Reason, eligResult.EligibleForDownload
			if eligible {
				// Where the job lands is settled before it is claimed, so one
				// that cannot land anywhere is refused with no tag put on it.
				if _, err := d.jobBaseDir(ctx, job); err != nil {
					d.countOutcome(summary, job, d.refuse(ctx, job, err))
					continue
				}
				claimStart := time.Now()
				reason, eligible = d.claim(ctx, job)
				totalDownloadTime += time.Since(claimStart)
			}
			if !eligible {
				if reason.Code != ReasonNone {
					summary.AddSkip(reason.Code)
				}
				if !reason.Code.IsSilent() {
					d.logger.Info().Msgf("SKIP: %s [%s] - %s", job.Name, job.ID, reason.Detail)
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
		d.countOutcome(summary, job, outcome)
	}

	if summary.Unchecked > 0 {
		d.logger.Warn().
			Dur("budget", scanBudget).
			Dur("download_time", totalDownloadTime).
			Int("unchecked", summary.Unchecked).
			Msg("Scan budget used up; the jobs left unchecked wait for the next poll")
	}
	d.emitScanSummary(summary, time.Since(scanStart), summary.Unchecked > 0, nil)
	d.checkAllUnsetWarning(summary)

	// Workspace folders that could not be listed went unscanned, which the
	// user has to be told.
	if result.WorkspaceErr != nil {
		d.recordScanError(result.WorkspaceErr)
	} else {
		d.clearScanError()
	}
	d.persistPollProgress(summary)
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

// countOutcome counts a download attempt's outcome, and says so when the job
// has used up its attempts.
func (d *Daemon) countOutcome(s *ScanSummary, job *CompletedJob, outcome DownloadOutcome) {
	s.AddOutcome(string(outcome))
	if d.state.AttemptCount(job.ID) >= MaxDownloadAttempts {
		d.logger.Warn().Msgf("GAVE UP: %s [%s] - %d download attempts failed; run 'rescale-int daemon retry --job-id %s' to try again",
			job.Name, job.ID, MaxDownloadAttempts, job.ID)
	}
}

// persistPollProgress stamps the poll time, with how many jobs the poll left to
// other clients, and writes state to disk. Called by every poll that ran to
// completion or did partial work before being cut short.
func (d *Daemon) persistPollProgress(s *ScanSummary) {
	d.state.UpdateLastPoll(s)
	if err := d.state.Save(); err != nil {
		d.logger.Error().Err(err).Msg("Failed to save state after poll")
	}
}

// emitScanSummary logs the single canonical per-poll INFO summary line. Three
// shapes, distinguished by the interrupted and error markers:
//
//   - Complete: no markers, and the buckets sum to TotalScanned.
//   - Interrupted: interrupted=true with partial counts, so the buckets may sum
//     to less than TotalScanned. Ends a poll cut short by shutdown, or by its
//     scan budget, when unchecked counts the jobs it left for the next poll.
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
		"Poll complete: scanned=%d, eligibility-checked=%d, downloaded=%d, no_files=%d, failed=%d (partial=%d, list-failed=%d, dir-failed=%d), interrupted-jobs=%d, silent-skipped=%d (%s), logged-skipped=%d (%s), skipped-folders=%d, unchecked=%d%s, duration=%.1fs%s",
		s.TotalScanned,
		s.EligibilityChecked,
		downloaded,
		noFiles,
		failed,
		partial, listFailed, dirFailed,
		interruptedJobs,
		silentTotal, silentBreakdown,
		loggedTotal, loggedBreakdown,
		s.SkippedFolders,
		s.Unchecked,
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
	d.lastScanErr = reporting.RedactSecrets(err.Error())
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
	ReasonHasStartedTag,
	ReasonClaimFailed,
	ReasonConditionalMissingTag,
	ReasonDownloadedTagCheckAPIError,
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

	// Settled again here, just before anything is written: poll settled it
	// before claiming the job, and other callers do not.
	baseDir, err := d.jobBaseDir(ctx, job)
	if err != nil {
		return d.refuse(ctx, job, err)
	}

	outputDir := ComputeOutputDir(baseDir, job.ID, job.Name, d.cfg.UseJobNameDir)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		d.logger.Error().Err(err).Str("dir", outputDir).Msg("Failed to create output directory")
		d.markFailed(ctx, job, "", err)
		reporting.HandleCLIError(err, "daemon", "job_download", "")
		return OutcomeOutputDirCreateFailed
	}

	// Record the job ID in a .jobid marker file. A directory named after the
	// job name alone (no ID suffix) is mapped back to its Rescale job by this
	// file. Best-effort: a write failure is logged but does not fail the
	// download.
	if err := WriteJobIDFile(outputDir, job.ID); err != nil {
		d.logger.Warn().Err(err).Str("dir", outputDir).Msg("Failed to write .jobid marker file")
	}
	// In such a directory no job file may take the marker's place: the job
	// would lose its folder, or hand it to the job the file names. Case is
	// ignored, as Windows and macOS ignore it.
	var marker string
	if d.cfg.UseJobNameDir && job.Name != "" {
		marker, _ = validation.DownloadPath(outputDir, JobIDFileName, "")
	}

	files, err := d.apiClient.ListJobFiles(ctx, job.ID)
	if err != nil {
		d.logger.Error().Err(err).Str("job_id", job.ID).Msg("Failed to list job files")
		d.markFailed(ctx, job, "", err)
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

	if err := startDownloadBatch(d.ts, batchCtx, reqCh, batchID, batchLabel, services.SourceLabelDaemon, batchCancel); err != nil {
		d.logger.Error().Err(err).Str("job_id", job.ID).Msg("Failed to start download batch")
		d.markFailed(ctx, job, "", err)
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
	var skipped int    // files the dispatch left out: a refused name or a folder not made
	var skipErr error  // why it left the last one out
	var cutShort error // the batch's cancellation, when it ended the dispatch early
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
				skipped, skipErr = skipped+1, err
				continue
			}

			localPath, err := validation.DownloadPath(outputDir, f.Name, f.RelativePath)
			if err != nil {
				err = fmt.Errorf("invalid path from API for file %s: %w", validation.QuoteUnsafe(f.ID), err)
			} else if marker != "" && strings.EqualFold(localPath, marker) {
				err = fmt.Errorf("refusing to download to %s: a folder named after its job keeps the job's ID there", validation.Quote(localPath))
			} else if err = os.MkdirAll(filepath.Dir(localPath), 0755); err == nil {
				// Before the presence check: a link of the right size is not
				// the file, and is refused and left alone.
				err = validation.ValidateDownloadTarget(localPath)
			}
			if err != nil {
				d.logger.Warn().Err(err).Str("file_id", f.ID).Msg("Skipping file")
				skipped, skipErr = skipped+1, err
				continue
			}

			if d.alreadyDownloaded(localPath, f) {
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
			// Check the cancel first: select picks at random among ready cases,
			// and a request handed over after the cancel may never be registered.
			if batchCtx.Err() != nil {
				cutShort = batchCtx.Err()
				return
			}
			select {
			case reqCh <- req:
				dispatched++
			case <-batchCtx.Done():
				cutShort = batchCtx.Err()
				return
			}
		}
	}()

	// reqCh is closed before dispatchDone (defers run last-in-first-out), so by
	// the time this returns every request has been handed over. That is the
	// guarantee WaitForRegisteredBatch needs, and it also makes the dispatch
	// counters safe to read.
	<-dispatchDone

	// fail records the attempt as failed. Files the dispatch left out lead the
	// record, counted against all the job's files, and err, if any, says what
	// else went wrong. They failed for a reason of their own, which no stop
	// explains, so they count the attempt however late a stop comes.
	ofFiles := fmt.Sprintf("of %d files", len(files))
	if len(files) == 1 {
		ofFiles = "of 1 file"
	}
	fail := func(batchID string, err error) {
		if skipErr != nil {
			left := fmt.Errorf("%d %s could not be downloaded: %w", skipped, ofFiles, skipErr)
			if err != nil {
				left = fmt.Errorf("%w; %v", left, err)
			}
			batchID, err = "", left
		}
		d.markFailed(ctx, job, batchID, err)
	}

	var stats transfer.BatchStats
	if dispatched > 0 {
		var waitErr error
		stats, waitErr = d.ts.WaitForRegisteredBatch(ctx, batchID)
		// A vanished batch means someone cancelled it before its first task
		// registered. That is a user action, not a fault to report, and the
		// accounting below explains it: no task, so no file counted.
		if waitErr != nil && !errors.Is(waitErr, services.ErrBatchVanished) {
			d.logger.Error().Err(waitErr).Str("job_id", job.ID).Msg("Job download interrupted")
			fail(batchID, waitErr)
			reporting.HandleCLIError(waitErr, "daemon", "job_download", "")
			return OutcomeInterrupted
		}
	} else if alreadyPresent == 0 || cutShort != nil {
		// Nothing dispatched, and either nothing on disk (every file was
		// rejected by name validation or its directory could not be created)
		// or the batch was cancelled first. Record a failure instead of
		// claiming success. No file reached the queue, so the error alone says
		// whether a stop caused this. Without one every file was left out, and
		// fail says why.
		var noneErr error
		if cutShort != nil {
			noneErr = fmt.Errorf("cancelled before all files were queued: %w", cutShort)
		}
		d.logger.Warn().Str("job_id", job.ID).Int("total_files", len(files)).
			Msg("Job had no downloadable files, marking as failed for retry")
		fail("", noneErr)
		return OutcomePartialFailure
	}
	// dispatched == 0 with files already on disk falls through with zero-valued
	// stats. There is nothing to wait for: the batch registered no tasks, so
	// waiting could only report a batch the queue has already forgotten.

	// The job is downloaded only when every file it lists is counted as there:
	// verified on disk, or completed through the queue. A request handed over
	// as the batch was cancelled may never have been registered, and a file
	// whose name is refused or whose folder could not be made never reached the
	// queue, so no failure shows for either. A job short of that is retried on
	// a later poll.
	var outcome DownloadOutcome
	if alreadyPresent+stats.Completed < len(files) {
		var failErr error // none when the files left out are all the job lacks
		if cutShort != nil || stats.Total < dispatched {
			failErr = fmt.Errorf("cancelled before all files were queued: %w", batchCtx.Err())
		} else if alreadyPresent+stats.Completed+skipped < len(files) {
			failErr = fmt.Errorf("%d failed + %d cancelled %s", stats.Failed, stats.Cancelled, ofFiles)
		}
		d.logger.Warn().
			Str("job_id", job.ID).
			Int("failed_files", stats.Failed).
			Int("cancelled_files", stats.Cancelled).
			Int("total_files", stats.Total+alreadyPresent).
			Msg("Job incomplete, marking as failed for retry")
		fail(batchID, failErr)
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

// markFailed records a failed download attempt, which schedules the job's next
// one, and saves it together with any 'daemon retry' made during the attempt,
// which it takes in first, so the count restarts. It first releases the job's
// 'started' tag, on a stop as on a failure, so other clients may take the job.
//
// What the daemon's own stopping causes is not a failure: counting it would
// hold the job in backoff after the restart, and a few restarts during one long
// download would use up all its attempts. So while the daemon stops, an error
// that is the cancellation is left out, and so is a batch the stop cut short,
// unless one of its files had failed for a reason of its own.
func (d *Daemon) markFailed(ctx context.Context, job *CompletedJob, batchID string, err error) {
	d.releaseStarted(job.ID)
	if ctx.Err() != nil {
		if batchID != "" {
			err = d.fileFailure(batchID)
		} else if errors.Is(err, context.Canceled) {
			err = nil
		}
		if err == nil {
			return
		}
	}
	if saveErr := d.state.update(func() { d.state.MarkFailed(job.ID, job.Name, err) }); saveErr != nil {
		d.logger.Error().Err(saveErr).Msg("Failed to persist state")
	}
}

// fileFailure returns the error of a file in the batch that failed other than
// by being cancelled, or nil. A wait the stop cuts short reports no statistics,
// so the batch's tasks are read instead.
func (d *Daemon) fileFailure(batchID string) error {
	tasks := d.ts.GetQueue().GetBatchTasks(batchID, 0, math.MaxInt, string(transfer.TaskFailed))
	for i := range tasks {
		if err := tasks[i].Error; err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return nil
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
	d.releaseStarted(job.ID)
}

// claim puts this client's started tag, naming it and the time, on the job,
// and reports whether this client is to download the job. If not, it takes the
// tag off again and says why, unless the daemon is stopping.
//
// Two clients can find a job free at once, so each reads the job's tags back
// once every claim made before its own has had time to show, and goes on only
// if the job is not done and its own claim is the earliest there. A claim that
// took longer than claimSettle to put on may have shown only after another
// client's read, so it goes on only as the job's one claim. With clocks within
// claimSettle of each other, at most one client goes on.
func (d *Daemon) claim(ctx context.Context, job *CompletedJob) (SkipReason, bool) {
	// One attempt never holds two tags: one left on the job comes off first.
	if !d.releaseStarted(job.ID) {
		return SkipReason{Code: ReasonClaimFailed, Detail: "could not take this client's earlier started tag off"}, false
	}
	// Saved before the tag goes on, so this client knows every tag it put on.
	at := time.Now()
	var tag string
	if err := d.state.update(func() { tag = d.state.MarkStarted(job.ID, at) }); err != nil {
		d.state.ClearStarted(job.ID)
		return SkipReason{Code: ReasonClaimFailed, Detail: fmt.Sprintf("could not record the claim: %v", err)}, false
	}
	leave := func(reason SkipReason) (SkipReason, bool) {
		d.releaseStarted(job.ID)
		if ctx.Err() != nil {
			return SkipReason{}, false // a stop is not a skip
		}
		return reason, false
	}
	failed := func(err error) (SkipReason, bool) {
		return leave(SkipReason{Code: ReasonClaimFailed, Detail: fmt.Sprintf("could not claim the job: %v", err)})
	}

	if err := d.apiClient.AddJobTag(ctx, job.ID, tag); err != nil {
		return failed(err)
	}
	slow := time.Since(at) > claimSettle
	select {
	case <-time.After(time.Until(at.Add(2 * claimSettle))):
	case <-ctx.Done():
		return leave(SkipReason{})
	}
	tags, err := d.apiClient.GetJobTags(ctx, job.ID)
	if err != nil {
		return failed(err)
	}
	if done := doneTag(tags); done != "" {
		return leave(SkipReason{Code: ReasonHasDownloadedTag, Detail: fmt.Sprintf("already has '%s' tag", done)})
	}
	// This client's other tags hold nothing for it, nor do expired ones: both
	// come off, as nothing else would take them off.
	self := d.state.ClientID()
	var live, stale []startedClaim
	for _, c := range startedClaims(tags, job.CompletedAt) {
		switch {
		case c.tag == tag || c.client != self && c.holds():
			live = append(live, c)
		case !c.at.IsZero():
			stale = append(stale, c)
		}
	}
	d.removeStale(ctx, job.ID, stale)
	switch i := slices.IndexFunc(live, func(c startedClaim) bool { return c.tag == tag }); {
	case i < 0:
		return failed(errors.New("its started tag did not show on the job"))
	case i > 0:
		return leave(heldReason(live[0]))
	case slow && len(live) > 1:
		return leave(heldReason(live[1]))
	}
	d.logger.Debug().Str("job_id", job.ID).Str("tag", tag).Msg("Claimed job")
	return SkipReason{}, true
}

// removeStale takes started tags that hold nothing off the job, best-effort,
// with one line naming those it took off.
func (d *Daemon) removeStale(ctx context.Context, jobID string, stale []startedClaim) {
	var removed []string
	for _, c := range stale {
		if err := d.apiClient.DeleteJobTag(ctx, jobID, c.tag); err != nil {
			d.logger.Debug().Err(err).Str("job_id", jobID).Str("tag", c.tag).Msg("Failed to take stale started tag off")
			continue
		}
		removed = append(removed, c.tag)
	}
	if len(removed) > 0 {
		d.logger.Info().Str("job_id", jobID).Strs("tags", removed).Msg("Took started tags that hold nothing off the job")
	}
}

// releaseStarted takes this client's started tag off the job and forgets it,
// saving that at once, and reports whether the job is left without one. A
// removal that fails keeps the tag this client's, and the next poll tries
// again, until the tag's lease is over: it holds nothing then, and a removal
// that still fails, as every one does once the job is deleted, would be tried
// for weeks. The call gets a context of its own, so the tag comes off even
// while the daemon stops, and 4 s, inside the 5 s 'daemon run' gives a
// stopping daemon, so a stop does not cut it off between the removal and the
// save.
func (d *Daemon) releaseStarted(jobID string) bool {
	tag, ok := d.state.StartedTag(jobID)
	if !ok {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := d.apiClient.DeleteJobTag(ctx, jobID, tag); err != nil {
		log := d.logger.Warn().Err(err).Str("job_id", jobID).Str("tag", tag)
		if c := startedClaims([]string{tag}, time.Time{}); len(c) == 1 && c[0].holds() {
			log.Msg("Failed to take started tag off (will retry at the next poll)")
			return false
		}
		log.Msg("Gave up taking started tag off: its lease is over, so it holds nothing")
	}
	if err := d.state.update(func() { d.state.ClearStarted(jobID) }); err != nil {
		d.logger.Error().Err(err).Msg("Failed to persist state")
	}
	return true
}

// jobBaseDir returns the folder a job's own folder goes in. That is the job's
// own "Auto Download Path" when that lies within DownloadDir; otherwise, for a
// job in a workspace folder, the mirror of that folder under DownloadDir,
// unless flattening is enabled; otherwise DownloadDir. The folder names come
// from the server, so each is held to the rules for every other server name,
// and the path must stay within DownloadDir: a path that cannot be mirrored is
// an error, unless the job's own download path applies. So is a job ID that is
// not one, as the job's folder is named after it.
func (d *Daemon) jobBaseDir(ctx context.Context, job *CompletedJob) (string, error) {
	if err := validation.ValidateID(job.ID); err != nil {
		return "", fmt.Errorf("invalid job ID: %w", err)
	}
	baseDir := d.cfg.DownloadDir
	var mirrorErr error
	if len(job.FolderPath) > 0 && !d.cfg.FlattenFolderStructure {
		var dir string
		if dir, mirrorErr = mirrorDir(d.cfg.DownloadDir, job.FolderPath); mirrorErr == nil {
			baseDir = dir
		}
	}

	// Check for custom download path from eligibility config. A per-job
	// "Auto Download Path" override takes precedence over folder mirroring and
	// must resolve to within DownloadDir.
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
				baseDir, mirrorErr = realCandidate, nil
			}
		}
	}
	if mirrorErr != nil {
		return "", mirrorErr
	}
	return baseDir, nil
}

// refuse fails a job that cannot be downloaded, with the reason.
func (d *Daemon) refuse(ctx context.Context, job *CompletedJob, err error) DownloadOutcome {
	d.logger.Error().Err(err).Str("job_id", job.ID).Msg("Refusing to download job")
	d.markFailed(ctx, job, "", err)
	return OutcomeOutputDirCreateFailed
}

// mirrorDir returns the folder under downloadDir that mirrors a job's
// workspace folder path, symlinks resolved.
func mirrorDir(downloadDir string, folderPath []string) (string, error) {
	refuse := func(err error) (string, error) {
		return "", fmt.Errorf("cannot mirror workspace folder %s: %w", validation.Quote(strings.Join(folderPath, "/")), err)
	}
	for _, name := range folderPath {
		if err := validation.ValidateFilename(name); err != nil {
			return refuse(err)
		}
	}
	realDownloadDir, err := filepath.EvalSymlinks(downloadDir)
	if err != nil {
		realDownloadDir = filepath.Clean(downloadDir)
	}
	realCandidate := resolvePathWithSymlinks(filepath.Join(append([]string{downloadDir}, folderPath...)...))
	if err := validation.ValidatePathInDirectory(realCandidate, realDownloadDir); err != nil {
		return refuse(err)
	}
	return realCandidate, nil
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
