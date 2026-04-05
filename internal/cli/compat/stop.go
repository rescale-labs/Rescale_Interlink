package compat

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newStopCmd() *cobra.Command {
	var jobID string

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a running job",
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobID == "" {
				return fmt.Errorf("--job-id is required")
			}

			cc := GetCompatContext(cmd)
			client, err := cc.GetAPIClient(cmd.Context())
			if err != nil {
				return err
			}

			if err := client.StopJob(cmd.Context(), jobID); err != nil {
				return fmt.Errorf("failed to stop job: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")

	return cmd
}
