package wailsapp

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// JobStatusItemDTO represents a single job entry for the Job Status tab.
type JobStatusItemDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"createdAt"`
}

// JobStatusListDTO is the response from ListJobStatuses.
type JobStatusListDTO struct {
	Jobs  []JobStatusItemDTO `json:"jobs"`
	Error string             `json:"error,omitempty"`
}

// ListJobStatuses fetches the current user's jobs from the Rescale API
// and returns their id, name, status, reason, and creation date.
//
// The list endpoint only provides statusReason for a few statuses. For jobs
// where the list endpoint gives no reason, we fetch the per-job statuses
// endpoint concurrently (up to 8 at a time) to retrieve the latest reason.
func (a *App) ListJobStatuses() JobStatusListDTO {
	if a.engine == nil {
		return JobStatusListDTO{Error: ErrNoEngine.Error()}
	}
	apiClient := a.engine.API()
	if apiClient == nil {
		return JobStatusListDTO{Error: "API client not available — please configure your API key"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	jobs, err := apiClient.ListJobs(ctx)
	if err != nil {
		return JobStatusListDTO{Error: fmt.Sprintf("Failed to fetch jobs: %v", err)}
	}

	items := make([]JobStatusItemDTO, len(jobs))
	for i, j := range jobs {
		status := j.JobStatus.Status
		if status == "" {
			status = "Not Submitted"
		}
		items[i] = JobStatusItemDTO{
			ID:        j.ID,
			Name:      j.Name,
			Status:    status,
			Reason:    j.JobStatus.Content,
			CreatedAt: j.CreatedAt,
		}
	}

	// Fetch per-job status history for any job missing a reason.
	// Use a worker pool of 8 to avoid overwhelming the API.
	type work struct {
		index int
		id    string
	}
	workCh := make(chan work, len(items))
	needFetch := 0
	for i, item := range items {
		if item.Reason == "" && item.Status != "Not Submitted" {
			workCh <- work{i, item.ID}
			needFetch++
		}
	}
	close(workCh)

	if needFetch > 0 {
		const workers = 8
		var wg sync.WaitGroup
		var mu sync.Mutex
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range workCh {
					statuses, err := apiClient.GetJobStatuses(ctx, job.id)
					if err != nil || len(statuses) == 0 {
						log.Printf("[JobStatus] id=%s err=%v entries=%d", job.id, err, len(statuses))
						continue
					}
					log.Printf("[JobStatus] id=%s entries=%d first={status=%q reason=%q} last={status=%q reason=%q}",
						job.id, len(statuses),
						statuses[0].Status, statuses[0].StatusReason,
						statuses[len(statuses)-1].Status, statuses[len(statuses)-1].StatusReason,
					)
					// Search all entries for a non-empty statusReason —
					// the ordering varies and most entries have no reason.
					reason := ""
					for _, s := range statuses {
						if s.StatusReason != "" {
							reason = s.StatusReason
							break
						}
					}
					if reason != "" {
						mu.Lock()
						items[job.index].Reason = reason
						mu.Unlock()
					}
				}
			}()
		}
		wg.Wait()
	}

	return JobStatusListDTO{Jobs: items}
}
