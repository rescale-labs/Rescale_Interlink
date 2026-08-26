package compat

import (
	"context"
	"fmt"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/watch"
)

// compatMonitorInterval is the poll interval for compatMonitorJob. A variable
// so tests can drive the loop without waiting on the real ticker.
var compatMonitorInterval = constants.JobTailTickerInterval

// compatMonitorJob polls job status until a terminal state is reached.
// Prints status transitions via cc.Printf (suppressed in quiet mode).
// Returns nil on Completed, error on any other terminal status or after 5
// consecutive errors.
func compatMonitorJob(ctx context.Context, jobID string, client *api.Client, cc *CompatContext) error {
	lastStatus := ""
	ticker := time.NewTicker(compatMonitorInterval)
	defer ticker.Stop()

	consecutiveErrors := 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			reqCtx, cancel := context.WithTimeout(ctx, constants.APIContextTimeout)
			statuses, err := client.GetJobStatuses(reqCtx, jobID)
			cancel()

			if err != nil {
				consecutiveErrors++
				if consecutiveErrors >= constants.MaxConsecutiveWatchErrors {
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

			// Only Completed counts as success — Stopped and Force Stopped
			// end the job without producing results.
			if watch.TerminalStatuses[currentStatus] {
				if currentStatus == watch.StatusCompleted {
					return nil
				}
				return fmt.Errorf("job ended with status: %s", currentStatus)
			}
		}
	}
}
