package compat

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
)

// terminalStatuses are job statuses that indicate the job has finished.
var terminalStatuses = map[string]bool{
	"Completed":     true,
	"Failed":        true,
	"Stopped":       true,
	"Force Stopped": true,
}

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
				return fmt.Errorf("'-n' (newer-than-job-id) is not yet implemented in compat mode")
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

			// Polling mode: download, sleep, check status, repeat
			return syncPollLoop(ctx, jobID, opts, syncInterval, client, cc)
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")
	cmd.Flags().IntVarP(&syncInterval, "sync-interval", "d", 0, "Polling interval in seconds (0 = single download)")
	cmd.Flags().StringVarP(&outputDir, "output", "o", "", "Output directory")
	cmd.Flags().StringSliceVarP(&fileMatchers, "file-matcher", "f", nil, "Glob patterns to include")
	cmd.Flags().StringVar(&excludeTerm, "exclude", "", "Exclude files matching pattern")
	cmd.Flags().StringVarP(&searchTerm, "search", "s", "", "Search term for file filtering")

	// Deferred flags
	cmd.Flags().StringVarP(&newerThanJobID, "newer-than-job-id", "n", "", "Only sync files newer than this job")
	cmd.Flags().MarkHidden("newer-than-job-id")

	return cmd
}

// syncPollLoop repeatedly downloads files and checks job status until the job reaches a terminal state.
func syncPollLoop(ctx context.Context, jobID string, opts compatDownloadOpts, intervalSec int, client *api.Client, cc *CompatContext) error {
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	for {
		// Download pass (skip-existing is already built into compatDownloadByJobID)
		if err := compatDownloadByJobID(ctx, jobID, opts, client, cc); err != nil {
			// Log download errors but continue polling
			log.Printf("sync download error: %v", err)
		}

		// Check job status
		statuses, err := client.GetJobStatuses(ctx, jobID)
		if err != nil {
			log.Printf("sync status check error: %v", err)
		} else if len(statuses) > 0 {
			currentStatus := statuses[0].Status
			if terminalStatuses[currentStatus] {
				// One final download sweep to catch any last files
				if dlErr := compatDownloadByJobID(ctx, jobID, opts, client, cc); dlErr != nil {
					log.Printf("sync final download error: %v", dlErr)
				}
				cc.Printf("Job %s reached terminal status: %s\n", jobID, currentStatus)
				return nil
			}
		}

		// Wait for next interval
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
