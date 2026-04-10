package compat

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/watch"
)

func newSyncCmd() *cobra.Command {
	var jobID string
	var syncInterval int
	var outputDir string
	var fileMatchers []string
	var excludeTerm string
	var searchTerm string
	var newerThanJobID string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Download and optionally poll for job output files",
		RunE: func(cmd *cobra.Command, args []string) error {
			if newerThanJobID != "" {
				return runCompatNewerThan(cmd, newerThanJobID, outputDir, syncInterval)
			}
			if jobID == "" {
				return fmt.Errorf("--job-id is required")
			}

			cc := GetCompatContext(cmd)
			client, err := cc.GetAPIClient(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			opts := compatDownloadOpts{
				OutputDir:    outputDir,
				FileMatchers: fileMatchers,
				ExcludeTerm:  excludeTerm,
				SearchTerm:   searchTerm,
			}

			// Single-run mode: no polling
			if syncInterval <= 0 {
				return compatDownloadByJobID(ctx, jobID, opts, client, cc)
			}

			// Polling mode: delegate to watch engine
			return runCompatWatchPoll(ctx, jobID, opts, syncInterval, client, cc)
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")
	cmd.Flags().IntVarP(&syncInterval, "sync-interval", "d", 0, "Polling interval in seconds (0 = single download)")
	cmd.Flags().StringVarP(&outputDir, "output", "o", "", "Output directory")
	cmd.Flags().StringSliceVarP(&fileMatchers, "file-matcher", "f", nil, "Glob patterns to include")
	cmd.Flags().StringVar(&excludeTerm, "exclude", "", "Exclude files matching pattern")
	cmd.Flags().StringVarP(&searchTerm, "search", "s", "", "Search term for file filtering")
	cmd.Flags().StringVarP(&newerThanJobID, "newer-than-job-id", "n", "", "Sync files for all jobs newer than this job")

	// Accepted-but-ignored flags (rescale-cli has these, scripts may pass them)
	var verify string
	var maxConcurrent int
	cmd.Flags().StringVar(&verify, "verify", "true", "Verify file integrity (accepted, ignored)")
	cmd.Flags().MarkHidden("verify")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", 5, "Max concurrent downloads (accepted, ignored)")
	cmd.Flags().MarkHidden("max-concurrent")

	return cmd
}

// runCompatWatchPoll delegates polling-mode sync to the shared watch engine.
func runCompatWatchPoll(ctx context.Context, jobID string, opts compatDownloadOpts, intervalSec int, client *api.Client, cc *CompatContext) error {
	cfg := watch.Config{
		Interval: time.Duration(intervalSec) * time.Second,
	}

	statusFn := func(ctx context.Context, jID string) (string, error) {
		statuses, err := client.GetJobStatuses(ctx, jID)
		if err != nil {
			return "", err
		}
		if len(statuses) == 0 {
			return "", fmt.Errorf("no status entries for job %s", jID)
		}
		return statuses[0].Status, nil
	}

	downloadFn := func(ctx context.Context, jID string) error {
		return compatDownloadByJobID(ctx, jID, opts, client, cc)
	}

	cb := &watch.Callbacks{
		OnStatusChange: func(jID, oldStatus, newStatus string) {
			cc.Printf("%s - Job %s: %s -> %s\n",
				FormatSLF4JTimestamp(time.Now()), jID, oldStatus, newStatus)
		},
		OnDownloadPass: func(jID string, err error) {
			if err != nil {
				cc.Printf("%s - sync download error for %s: %v\n",
					FormatSLF4JTimestamp(time.Now()), jID, err)
			}
		},
		OnTerminal: func(jID, finalStatus string) {
			cc.Printf("Job %s reached terminal status: %s\n", jID, finalStatus)
		},
		OnError: func(jID string, err error) {
			cc.Printf("%s - sync status check error for %s: %v\n",
				FormatSLF4JTimestamp(time.Now()), jID, err)
		},
	}

	return watch.WatchJob(ctx, jobID, cfg, statusFn, downloadFn, cb)
}

// runCompatNewerThan delegates newer-than-job-id sync to the shared watch engine.
func runCompatNewerThan(cmd *cobra.Command, refJobID, outputDir string, syncInterval int) error {
	cc := GetCompatContext(cmd)
	client, err := cc.GetAPIClient(cmd.Context())
	if err != nil {
		return err
	}

	ctx := cmd.Context()

	if outputDir == "" {
		outputDir = "."
	}

	cfg := watch.Config{
		Interval: time.Duration(syncInterval) * time.Second,
	}
	// For single-run newer-than (syncInterval=0), use a minimal interval.
	// WatchNewerThan will process all jobs once and exit if all are terminal.
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}

	statusFn := func(ctx context.Context, jID string) (string, error) {
		statuses, err := client.GetJobStatuses(ctx, jID)
		if err != nil {
			return "", err
		}
		if len(statuses) == 0 {
			return "", fmt.Errorf("no status entries for job %s", jID)
		}
		return statuses[0].Status, nil
	}

	lister := func(ctx context.Context, refID string) ([]watch.JobInfo, error) {
		refJob, err := client.GetJob(ctx, refID)
		if err != nil {
			return nil, fmt.Errorf("failed to get reference job %s: %w", refID, err)
		}
		if refJob.CreatedAt == "" {
			return nil, fmt.Errorf("reference job %s has no creation date", refID)
		}
		cutoff, parseErr := time.Parse(time.RFC3339, refJob.CreatedAt)
		if parseErr != nil {
			return nil, fmt.Errorf("failed to parse reference job creation date: %w", parseErr)
		}

		allJobs, err := client.ListJobsWithCutoff(ctx, cutoff)
		if err != nil {
			return nil, fmt.Errorf("failed to list jobs: %w", err)
		}

		var result []watch.JobInfo
		for _, j := range allJobs {
			if j.ID == refID {
				continue
			}
			if j.CreatedAt != "" {
				jTime, pe := time.Parse(time.RFC3339, j.CreatedAt)
				if pe == nil && jTime.Before(cutoff) {
					continue
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
		jobOutdir := filepath.Join(outputDir, fmt.Sprintf("rescale_job_%s", jID))
		return func(ctx context.Context, _ string) error {
			opts := compatDownloadOpts{OutputDir: jobOutdir}
			return compatDownloadByJobID(ctx, jID, opts, client, cc)
		}
	}

	cb := &watch.Callbacks{
		OnStatusChange: func(jID, oldStatus, newStatus string) {
			cc.Printf("%s - Job %s: %s -> %s\n",
				FormatSLF4JTimestamp(time.Now()), jID, oldStatus, newStatus)
		},
		OnDownloadPass: func(jID string, err error) {
			if err != nil {
				cc.Printf("%s - sync download error for %s: %v\n",
					FormatSLF4JTimestamp(time.Now()), jID, err)
			}
		},
		OnTerminal: func(jID, finalStatus string) {
			cc.Printf("Job %s reached terminal status: %s\n", jID, finalStatus)
		},
		OnError: func(jID string, err error) {
			cc.Printf("%s - sync error for %s: %v\n",
				FormatSLF4JTimestamp(time.Now()), jID, err)
		},
	}

	return watch.WatchNewerThan(ctx, refJobID, cfg, lister, statusFn, dlFactory, cb)
}
