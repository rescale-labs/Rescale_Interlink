package compat

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
			if jobID == "" {
				return fmt.Errorf("--job-id is required")
			}

			cc := GetCompatContext(cmd)
			client, err := cc.GetAPIClient(cmd.Context())
			if err != nil {
				return err
			}

			ctx := cmd.Context()

			if extendedOutput {
				// Fetch raw statuses (preserves notify, preventDuplicates)
				rawStatuses, err := client.GetJobStatusesRaw(ctx, jobID)
				if err != nil {
					return fmt.Errorf("failed to get job status: %w", err)
				}
				if len(rawStatuses) == 0 {
					return fmt.Errorf("no status found for job %s", jobID)
				}

				// Transform lastStatus: remove id/jobId/substatus, add notify/preventDuplicates
				lastStatus, err := transformLastStatus(rawStatuses[0])
				if err != nil {
					return fmt.Errorf("failed to transform status: %w", err)
				}

				// Fetch raw connection details
				rawConnDetails, err := client.GetJobConnectionDetailsRaw(ctx, jobID)
				if err != nil {
					return fmt.Errorf("failed to get connection details: %w", err)
				}

				// Fetch load measurements if requested
				var loadMeasurements json.RawMessage = json.RawMessage("[]")
				if loadHours > 0 {
					raw, err := client.GetJobLoadMeasurementsRaw(ctx, jobID, loadHours)
					if err != nil {
						// Only swallow "endpoint not found" (404/405). Surface auth, network, and server errors.
						if !isNotFoundOrMethodNotAllowed(err) {
							return fmt.Errorf("failed to get load measurements: %w", err)
						}
					} else if raw != nil {
						loadMeasurements = raw
					}
				}

				composite := map[string]json.RawMessage{
					"lastStatus":        lastStatus,
					"connectionDetails": rawConnDetails,
					"loadMeasurements":  loadMeasurements,
				}

				return writeJSON(os.Stdout, composite)
			}

			statuses, err := client.GetJobStatuses(ctx, jobID)
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

	cmd.Flags().BoolVarP(&extendedOutput, "extended-output", "e", false, "Extended JSON output")
	cmd.Flags().IntVar(&loadHours, "load-hours", 0, "Hours of load measurement data to include")

	return cmd
}

// isNotFoundOrMethodNotAllowed checks if an error contains a 404 or 405 status code.
func isNotFoundOrMethodNotAllowed(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "status 404") || strings.Contains(msg, "status 405")
}

// transformLastStatus adjusts the raw API status JSON to match rescale-cli's
// output: removes id/jobId/substatus (not in CLI output), adds notify and
// preventDuplicates defaults (present in CLI output as client-side defaults).
func transformLastStatus(raw json.RawMessage) (json.RawMessage, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, "id")
	delete(m, "jobId")
	delete(m, "substatus")
	if _, ok := m["notify"]; !ok {
		m["notify"] = false
	}
	if _, ok := m["preventDuplicates"]; !ok {
		m["preventDuplicates"] = false
	}
	return json.Marshal(m)
}
