package compat

import (
	"context"
	"fmt"
	"time"

	"github.com/rescale/rescale-int/internal/api"
)

// compatMonitorJob polls job status until a terminal state is reached.
// Prints status transitions via cc.Printf (suppressed in quiet mode).
// Returns nil on Completed, error on Failed/Terminated or after 5 consecutive errors.
func compatMonitorJob(ctx context.Context, jobID string, client *api.Client, cc *CompatContext) error {
	lastStatus := ""
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	consecutiveErrors := 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			statuses, err := client.GetJobStatuses(reqCtx, jobID)
			cancel()

			if err != nil {
				consecutiveErrors++
				if consecutiveErrors >= 5 {
					return fmt.Errorf("failed to get job status after %d attempts: %w", consecutiveErrors, err)
				}
				continue
			}

			consecutiveErrors = 0

			if len(statuses) == 0 {
				continue
			}

			currentStatus := statuses[0].Status
			if currentStatus != lastStatus {
				cc.Printf("Job %s status: %s\n", jobID, currentStatus)
				lastStatus = currentStatus
			}

			switch currentStatus {
			case "Completed":
				return nil
			case "Failed", "Terminated":
				return fmt.Errorf("job ended with status: %s", currentStatus)
			}
		}
	}
}
