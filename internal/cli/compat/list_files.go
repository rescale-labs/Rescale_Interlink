package compat

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
)

func newListFilesCmd() *cobra.Command {
	var jobID string
	var runID string

	cmd := &cobra.Command{
		Use:   "list-files",
		Short: "List files from a running job's cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runID != "" {
				return fmt.Errorf("'-r' (run-id) is not yet implemented in compat mode")
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

			// Check for active run — the presence of a run is the source of truth,
			// not the job status. A job in Queued/Pending may not have a run yet.
			runs, err := client.GetJobRuns(ctx, jobID)
			if err != nil {
				// Fallback: if the runs endpoint fails (404/405), use ListJobFiles.
				// This is a documented deviation from rescale-cli.
				cc.Printf("Warning: runs endpoint unavailable, falling back to job files\n")
				return listJobFilesFallback(ctx, jobID, client, cc)
			}

			// Find an active run: dateStarted set, dateCompleted empty
			var activeRunID string
			for _, run := range runs {
				if run.DateStarted != "" && run.DateCompleted == "" {
					activeRunID = run.ID
					break
				}
			}

			if activeRunID == "" {
				return fmt.Errorf("The job (%s) has no active run. (The 'list-files' command lists files from a running cluster. To get files from a completed job, use the 'sync' command.)", jobID)
			}

			// List files from the active run
			runFiles, err := client.GetRunFiles(ctx, activeRunID)
			if err != nil {
				return fmt.Errorf("failed to list run files: %w", err)
			}

			for _, f := range runFiles {
				fmt.Fprintln(os.Stdout, f.Name)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")
	cmd.Flags().StringVarP(&runID, "run-id", "r", "", "Run ID")
	cmd.Flags().MarkHidden("run-id")

	return cmd
}

// listJobFilesFallback uses ListJobFiles when the runs endpoint is unavailable.
func listJobFilesFallback(ctx context.Context, jobID string, client *api.Client, cc *CompatContext) error {
	files, err := client.ListJobFiles(ctx, jobID)
	if err != nil {
		return fmt.Errorf("failed to list job files: %w", err)
	}

	if len(files) == 0 {
		cc.Printf("No files found for job %s\n", jobID)
		return nil
	}

	for _, f := range files {
		fmt.Fprintln(os.Stdout, f.Name)
	}
	return nil
}
