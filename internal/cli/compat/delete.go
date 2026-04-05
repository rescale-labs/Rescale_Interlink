package compat

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDeleteCmd() *cobra.Command {
	var jobID string

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a job",
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobID == "" {
				return fmt.Errorf("--job-id is required")
			}

			cc := GetCompatContext(cmd)
			client, err := cc.GetAPIClient(cmd.Context())
			if err != nil {
				return err
			}

			if err := client.DeleteJob(cmd.Context(), jobID); err != nil {
				return fmt.Errorf("failed to delete job: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&jobID, "job-id", "j", "", "Job ID (required)")

	return cmd
}
