package wailsapp

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const jobStatusPageSize = 50

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
	Jobs        []JobStatusItemDTO `json:"jobs"`
	Error       string             `json:"error,omitempty"`
	FetchErrors int                `json:"fetchErrors,omitempty"`
	// HasMore is true when there are likely more jobs beyond this page.
	HasMore bool `json:"hasMore"`
}

// ListJobStatuses fetches the first page (50) of the current user's most recent
// jobs and returns their id, name, status, reason, and creation date.
func (a *App) ListJobStatuses() JobStatusListDTO {
	return a.listJobStatusesPage(0)
}

// ListJobStatusesPage fetches a page of jobs starting at the given offset.
// Each page contains up to 50 jobs ordered by newest first.
func (a *App) ListJobStatusesPage(offset int) JobStatusListDTO {
	return a.listJobStatusesPage(offset)
}

func (a *App) listJobStatusesPage(offset int) JobStatusListDTO {
	if a.engine == nil {
		return JobStatusListDTO{Error: ErrNoEngine.Error()}
	}
	apiClient := a.engine.API()
	if apiClient == nil {
		return JobStatusListDTO{Error: "API client not available — please configure your API key"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Fetch offset + pageSize + 1 so we can both skip prior pages and detect hasMore.
	jobs, err := apiClient.ListJobsPaged(ctx, offset+jobStatusPageSize+1)
	if err != nil {
		return JobStatusListDTO{Error: fmt.Sprintf("Failed to fetch jobs: %v", err)}
	}

	hasMore := len(jobs) > offset+jobStatusPageSize

	// Slice to the requested page window.
	start := offset
	if start > len(jobs) {
		start = len(jobs)
	}
	end := offset + jobStatusPageSize
	if end > len(jobs) {
		end = len(jobs)
	}
	jobs = jobs[start:end]

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

	var fetchErrCount int64

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
						atomic.AddInt64(&fetchErrCount, 1)
						continue
					}
					// Sort by StatusDate descending to get the most recent reason.
					sort.Slice(statuses, func(i, j int) bool {
						return statuses[i].StatusDate > statuses[j].StatusDate
					})
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

	return JobStatusListDTO{Jobs: items, FetchErrors: int(fetchErrCount), HasMore: hasMore}
}
