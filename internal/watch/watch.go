// Package watch provides a polling engine for monitoring job status and
// incrementally downloading output files. It is imported by both the native
// CLI (internal/cli) and the compat layer (internal/cli/compat), so it has
// zero imports from those packages — all dependencies are injected via
// function types.
package watch

import (
	"context"
	"fmt"
	"time"

	"github.com/rescale/rescale-int/internal/constants"
)

// TerminalStatuses is the unified superset of statuses that indicate a job
// has finished (compat sync.go + jobs.go monitorJobUntilComplete).
var TerminalStatuses = map[string]bool{
	"Completed":     true,
	"Failed":        true,
	"Stopped":       true,
	"Force Stopped": true,
	"Terminated":    true,
}

// Config controls watch/poll behaviour.
type Config struct {
	Interval             time.Duration
	MaxConsecutiveErrors int
}

func (c *Config) applyDefaults() {
	if c.Interval <= 0 {
		c.Interval = constants.JobPollInterval
	}
	if c.MaxConsecutiveErrors <= 0 {
		c.MaxConsecutiveErrors = constants.MaxConsecutiveWatchErrors
	}
}

// Callbacks receives watch-specific events. All fields are optional (nil-safe).
// Download progress is NOT reported here — the injected download functions own
// their own stdout output (progress bars, file counts, etc.).
type Callbacks struct {
	OnStatusChange func(jobID, oldStatus, newStatus string)
	OnDownloadPass func(jobID string, err error)
	OnTerminal     func(jobID, finalStatus string)
	OnError        func(jobID string, err error)
}

func (cb *Callbacks) statusChange(jobID, old, new string) {
	if cb != nil && cb.OnStatusChange != nil {
		cb.OnStatusChange(jobID, old, new)
	}
}

func (cb *Callbacks) downloadPass(jobID string, err error) {
	if cb != nil && cb.OnDownloadPass != nil {
		cb.OnDownloadPass(jobID, err)
	}
}

func (cb *Callbacks) terminal(jobID, status string) {
	if cb != nil && cb.OnTerminal != nil {
		cb.OnTerminal(jobID, status)
	}
}

func (cb *Callbacks) onError(jobID string, err error) {
	if cb != nil && cb.OnError != nil {
		cb.OnError(jobID, err)
	}
}

// StatusFunc fetches the current status string for a job.
type StatusFunc func(ctx context.Context, jobID string) (string, error)

// DownloadFunc runs one download pass for a job (skip-existing semantics).
type DownloadFunc func(ctx context.Context, jobID string) error

// JobInfo is a lightweight job descriptor returned by a JobLister.
type JobInfo struct {
	ID     string
	Name   string
	Status string
}

// JobLister discovers jobs newer than a reference job ID. Called each tick to
// pick up newly-created jobs.
type JobLister func(ctx context.Context, referenceJobID string) ([]JobInfo, error)

// DownloadFuncFactory creates a DownloadFunc for a specific job ID. The watch
// engine caches the result per job so the factory is called at most once per job.
type DownloadFuncFactory func(jobID string) DownloadFunc

// WatchJob polls a single job until it reaches a terminal status, running a
// download pass after each status check. Returns nil for Completed, or an
// error for failure/cancellation.
func WatchJob(ctx context.Context, jobID string, cfg Config, statusFn StatusFunc, downloadFn DownloadFunc, cb *Callbacks) error {
	cfg.applyDefaults()

	consecutiveErrors := 0
	lastStatus := ""

	// Immediate first tick: status check + download
	status, err := checkStatus(ctx, jobID, statusFn)
	if err != nil {
		consecutiveErrors++
		cb.onError(jobID, err)
	} else {
		consecutiveErrors = 0
		if status != lastStatus {
			cb.statusChange(jobID, lastStatus, status)
			lastStatus = status
		}
	}

	if err == nil {
		dlErr := downloadFn(ctx, jobID)
		cb.downloadPass(jobID, dlErr)

		if TerminalStatuses[lastStatus] {
			cb.terminal(jobID, lastStatus)
			return terminalError(lastStatus)
		}
	}

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err := checkStatus(ctx, jobID, statusFn)
			if err != nil {
				consecutiveErrors++
				cb.onError(jobID, err)
				if consecutiveErrors >= cfg.MaxConsecutiveErrors {
					return fmt.Errorf("watch aborted after %d consecutive status check failures: %w", consecutiveErrors, err)
				}
				continue
			}
			consecutiveErrors = 0

			if status != lastStatus {
				cb.statusChange(jobID, lastStatus, status)
				lastStatus = status
			}

			if TerminalStatuses[lastStatus] {
				// Final download sweep
				dlErr := downloadFn(ctx, jobID)
				cb.downloadPass(jobID, dlErr)
				cb.terminal(jobID, lastStatus)
				return terminalError(lastStatus)
			}

			// Non-terminal: incremental download
			dlErr := downloadFn(ctx, jobID)
			cb.downloadPass(jobID, dlErr)
		}
	}
}

// WatchNewerThan discovers all jobs newer than referenceJobID and watches them
// until all reach terminal status (and no new jobs appear on re-discovery).
func WatchNewerThan(ctx context.Context, refJobID string, cfg Config, lister JobLister, statusFn StatusFunc, dlFactory DownloadFuncFactory, cb *Callbacks) error {
	cfg.applyDefaults()

	// Per-job state
	jobStatus := make(map[string]string)      // last known status
	jobTerminal := make(map[string]bool)       // reached terminal?
	jobDownload := make(map[string]DownloadFunc) // cached download closures

	// Initial discovery
	jobs, err := lister(ctx, refJobID)
	if err != nil {
		return fmt.Errorf("failed to list jobs for --newer-than: %w", err)
	}
	if len(jobs) == 0 {
		return nil
	}

	for _, j := range jobs {
		jobStatus[j.ID] = ""
		jobDownload[j.ID] = dlFactory(j.ID)
	}

	// Process all known jobs once immediately
	processJobs(ctx, jobStatus, jobTerminal, jobDownload, statusFn, cb)

	// If all terminal after first pass, exit
	if allTerminal(jobTerminal, jobStatus) {
		return nil
	}

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Re-discover to pick up newly-created jobs
			newJobs, err := lister(ctx, refJobID)
			if err != nil {
				cb.onError("", fmt.Errorf("job re-discovery failed: %w", err))
				continue
			}

			newJobDiscovered := false
			for _, j := range newJobs {
				if _, exists := jobStatus[j.ID]; !exists {
					jobStatus[j.ID] = ""
					jobDownload[j.ID] = dlFactory(j.ID)
					newJobDiscovered = true
				}
			}
			_ = newJobDiscovered

			// Process all non-terminal jobs
			processJobs(ctx, jobStatus, jobTerminal, jobDownload, statusFn, cb)

			// Exit when all jobs are terminal and no new jobs were discovered
			if allTerminal(jobTerminal, jobStatus) {
				return nil
			}
		}
	}
}

// processJobs iterates non-terminal jobs: status check, download pass.
func processJobs(ctx context.Context, statuses map[string]string, terminal map[string]bool, downloads map[string]DownloadFunc, statusFn StatusFunc, cb *Callbacks) {
	for jobID := range statuses {
		if terminal[jobID] {
			continue
		}

		status, err := checkStatus(ctx, jobID, statusFn)
		if err != nil {
			cb.onError(jobID, err)
			continue
		}

		old := statuses[jobID]
		if status != old {
			cb.statusChange(jobID, old, status)
			statuses[jobID] = status
		}

		dlErr := downloads[jobID](ctx, jobID)
		cb.downloadPass(jobID, dlErr)

		if TerminalStatuses[status] {
			terminal[jobID] = true
			cb.terminal(jobID, status)
		}
	}
}

// allTerminal returns true if every tracked job has reached a terminal status.
func allTerminal(terminal map[string]bool, statuses map[string]string) bool {
	if len(statuses) == 0 {
		return true
	}
	for id := range statuses {
		if !terminal[id] {
			return false
		}
	}
	return true
}

// checkStatus calls the status function with a per-request timeout.
func checkStatus(ctx context.Context, jobID string, fn StatusFunc) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, constants.APIContextTimeout)
	defer cancel()
	return fn(reqCtx, jobID)
}

// terminalError returns nil for Completed, error for anything else.
func terminalError(status string) error {
	if status == "Completed" {
		return nil
	}
	return fmt.Errorf("job reached terminal status: %s", status)
}
