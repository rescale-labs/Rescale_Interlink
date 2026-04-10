package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/util/filter"
	"github.com/rescale/rescale-int/internal/watch"
)

func newJobsWatchCmd() *cobra.Command {
	var jobID string
	var newerThan string
	var interval int
	var outdir string
	var filterPatterns string
	var excludePatterns string
	var searchTerms string
	var maxConcurrent int

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch a job and incrementally download output files",
		Long: `Monitor a running job's status and incrementally download output files
as they become available. Exits when the job reaches a terminal state.

Single-job mode:
  Watch one job, downloading files into the output directory.

Newer-than mode:
  Watch all jobs created after a reference job, downloading each job's
  files into per-job subdirectories (OUTDIR/job_ID/).

Press Ctrl+C to stop watching.

Examples:
  # Watch a single job
  rescale-int jobs watch -j XxYyZz -d ./results

  # Watch with faster polling
  rescale-int jobs watch -j XxYyZz -i 10

  # Watch only specific file types
  rescale-int jobs watch -j XxYyZz --filter "*.dat,*.log"

  # Watch all jobs newer than a reference job
  rescale-int jobs watch --newer-than OlDjOb -d ./results`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validation
			if jobID == "" && newerThan == "" {
				return fmt.Errorf("one of --job-id or --newer-than is required")
			}
			if jobID != "" && newerThan != "" {
				return fmt.Errorf("--job-id and --newer-than are mutually exclusive")
			}
			if interval < 5 {
				return fmt.Errorf("--interval must be at least 5 seconds")
			}
			if newerThan != "" && (filterPatterns != "" || excludePatterns != "" || searchTerms != "") {
				return fmt.Errorf("--filter, --exclude, and --search are only valid with --job-id (not --newer-than)")
			}
			if maxConcurrent < constants.MinMaxConcurrent || maxConcurrent > constants.MaxMaxConcurrent {
				return fmt.Errorf("--max-concurrent must be between %d and %d",
					constants.MinMaxConcurrent, constants.MaxMaxConcurrent)
			}

			logger := GetLogger()
			apiClient, err := getAPIClient()
			if err != nil {
				return err
			}
			ctx := GetContext()

			if outdir == "" {
				outdir = "."
			}

			cfg := watch.Config{
				Interval: time.Duration(interval) * time.Second,
			}

			cb := &watch.Callbacks{
				OnStatusChange: func(jID, oldStatus, newStatus string) {
					ts := time.Now().Format("15:04:05")
					if oldStatus == "" {
						fmt.Printf("[%s] Job %s: %s\n", ts, jID, newStatus)
					} else {
						fmt.Printf("[%s] Job %s: %s -> %s\n", ts, jID, oldStatus, newStatus)
					}
				},
				OnDownloadPass: func(jID string, dlErr error) {
					if dlErr != nil {
						logger.Warn().Str("job_id", jID).Err(dlErr).Msg("Download pass error")
					}
				},
				OnTerminal: func(jID, finalStatus string) {
					ts := time.Now().Format("15:04:05")
					fmt.Printf("[%s] Job %s reached terminal status: %s\n", ts, jID, finalStatus)
				},
				OnError: func(jID string, e error) {
					logger.Warn().Str("job_id", jID).Err(e).Msg("Watch error")
				},
			}

			if jobID != "" {
				return runSingleJobWatch(ctx, jobID, outdir, maxConcurrent, filterPatterns, excludePatterns, searchTerms, cfg, cb, apiClient, logger)
			}
			return runNewerThanWatch(ctx, newerThan, outdir, maxConcurrent, cfg, cb, apiClient, logger)
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID to watch")
	cmd.Flags().StringVarP(&newerThan, "newer-than", "n", "", "Reference job ID (watch all jobs created after this one)")
	cmd.Flags().IntVarP(&interval, "interval", "i", 30, "Polling interval in seconds (minimum 5)")
	cmd.Flags().StringVarP(&outdir, "outdir", "d", ".", "Output directory")
	cmd.Flags().StringVar(&filterPatterns, "filter", "", "Include globs (comma-separated)")
	cmd.Flags().StringVarP(&excludePatterns, "exclude", "x", "", "Exclude globs (comma-separated)")
	cmd.Flags().StringVarP(&searchTerms, "search", "s", "", "Search terms (comma-separated)")
	cmd.Flags().IntVarP(&maxConcurrent, "max-concurrent", "m", constants.DefaultMaxConcurrent,
		fmt.Sprintf("Maximum concurrent downloads (%d-%d)", constants.MinMaxConcurrent, constants.MaxMaxConcurrent))

	return cmd
}

func runSingleJobWatch(
	ctx context.Context,
	jobID, outdir string,
	maxConcurrent int,
	filterPatternsStr, excludePatternsStr, searchTermsStr string,
	cfg watch.Config,
	cb *watch.Callbacks,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	fmt.Printf("Watching job %s (polling every %s, Ctrl+C to stop)...\n\n",
		jobID, cfg.Interval)

	filterList := filter.ParsePatternList(filterPatternsStr)
	excludeList := filter.ParsePatternList(excludePatternsStr)
	searchList := filter.ParsePatternList(searchTermsStr)

	statusFn := func(ctx context.Context, jID string) (string, error) {
		statuses, err := apiClient.GetJobStatuses(ctx, jID)
		if err != nil {
			return "", err
		}
		if len(statuses) == 0 {
			return "", fmt.Errorf("no status entries for job %s", jID)
		}
		return statuses[0].Status, nil
	}

	downloadFn := func(ctx context.Context, jID string) error {
		return executeJobDownload(ctx, jID, outdir, maxConcurrent,
			false, true, false, false, // overwrite=false, skip=true, resume=false, skipChecksum=false
			filterList, excludeList, searchList, nil,
			apiClient, logger)
	}

	return watch.WatchJob(ctx, jobID, cfg, statusFn, downloadFn, cb)
}

func runNewerThanWatch(
	ctx context.Context,
	refJobID, outdir string,
	maxConcurrent int,
	cfg watch.Config,
	cb *watch.Callbacks,
	apiClient *api.Client,
	logger *logging.Logger,
) error {
	fmt.Printf("Watching jobs newer than %s (polling every %s, Ctrl+C to stop)...\n\n",
		refJobID, cfg.Interval)

	statusFn := func(ctx context.Context, jID string) (string, error) {
		statuses, err := apiClient.GetJobStatuses(ctx, jID)
		if err != nil {
			return "", err
		}
		if len(statuses) == 0 {
			return "", fmt.Errorf("no status entries for job %s", jID)
		}
		return statuses[0].Status, nil
	}

	lister := func(ctx context.Context, refID string) ([]watch.JobInfo, error) {
		refJob, err := apiClient.GetJob(ctx, refID)
		if err != nil {
			return nil, fmt.Errorf("failed to get reference job %s: %w", refID, err)
		}
		if refJob.CreatedAt == "" {
			return nil, fmt.Errorf("reference job %s has no creation date", refID)
		}
		cutoff, err := time.Parse(time.RFC3339, refJob.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to parse reference job creation date: %w", err)
		}

		allJobs, err := apiClient.ListJobsWithCutoff(ctx, cutoff)
		if err != nil {
			return nil, fmt.Errorf("failed to list jobs: %w", err)
		}

		var result []watch.JobInfo
		for _, j := range allJobs {
			if j.ID == refID {
				continue // exclude the reference job itself
			}
			if j.CreatedAt != "" {
				jTime, parseErr := time.Parse(time.RFC3339, j.CreatedAt)
				if parseErr == nil && jTime.Before(cutoff) {
					continue // older than reference
				}
			}
			result = append(result, watch.JobInfo{
				ID:     j.ID,
				Name:   j.Name,
				Status: j.JobStatus.Status,
			})
		}
		return result, nil
	}

	dlFactory := func(jID string) watch.DownloadFunc {
		jobOutdir := filepath.Join(outdir, fmt.Sprintf("job_%s", jID))
		return func(ctx context.Context, _ string) error {
			return executeJobDownload(ctx, jID, jobOutdir, maxConcurrent,
				false, true, false, false, // skip-existing
				nil, nil, nil, nil,
				apiClient, logger)
		}
	}

	return watch.WatchNewerThan(ctx, refJobID, cfg, lister, statusFn, dlFactory, cb)
}
