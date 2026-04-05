package compat

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var jobID string
	var extendedOutput bool
	var loadHours int

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check job status",
		RunE: func(cmd *cobra.Command, args []string) error {
			if extendedOutput {
				return fmt.Errorf("'-e' (extended output) is not yet implemented in compat mode (planned for Plan 3)")
			}
			if cmd.Flags().Changed("load-hours") {
				return fmt.Errorf("'--load-hours' is not yet implemented in compat mode (planned for Plan 3)")
			}

			if jobID == "" {
				return fmt.Errorf("--job-id is required")
			}

			cc := GetCompatContext(cmd)
			client, err := cc.GetAPIClient(cmd.Context())
			if err != nil {
				return err
			}

			statuses, err := client.GetJobStatuses(cmd.Context(), jobID)
			if err != nil {
				return fmt.Errorf("failed to get job status: %w", err)
			}

			if len(statuses) == 0 {
				return fmt.Errorf("no status found for job %s", jobID)
			}

			// Data output — always printed (not suppressed by -q)
			fmt.Fprintf(os.Stdout, "The status of job %s is %s\n", jobID, statuses[0].Status)
			return nil
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")

	// Deferred flags (accepted but not implemented)
	cmd.Flags().BoolVarP(&extendedOutput, "extended-output", "e", false, "Extended JSON output")
	cmd.Flags().MarkHidden("extended-output")
	cmd.Flags().IntVar(&loadHours, "load-hours", 0, "Load hours")
	cmd.Flags().MarkHidden("load-hours")

	return cmd
}
