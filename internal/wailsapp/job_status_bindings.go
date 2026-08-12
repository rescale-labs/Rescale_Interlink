package wailsapp

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// latestStatusReason returns the StatusReason of the chronologically newest
// entry that has one. StatusDate values parse as RFC 3339; comparing them as
// raw strings is only correct when every entry shares the same UTC offset.
// Entries are decorated with their parsed time once up front — a comparator
// that switches between time and string comparison per pair is not transitive
// when parseable and unparseable dates mix, and can return order-dependent
// results. Parseable entries always rank ahead of unparseable ones.
func latestStatusReason(statuses []models.JobStatusEntry) string {
	type decorated struct {
		entry  models.JobStatusEntry
		t      time.Time
		parsed bool
	}
	dec := make([]decorated, len(statuses))
	for i, s := range statuses {
		t, err := time.Parse(time.RFC3339Nano, s.StatusDate)
		dec[i] = decorated{entry: s, t: t, parsed: err == nil}
	}
	sort.SliceStable(dec, func(i, j int) bool {
		if dec[i].parsed != dec[j].parsed {
			return dec[i].parsed
		}
		if dec[i].parsed {
			return dec[i].t.After(dec[j].t)
		}
		return dec[i].entry.StatusDate > dec[j].entry.StatusDate
	})
	for _, d := range dec {
		if d.entry.StatusReason != "" {
			return d.entry.StatusReason
		}
	}
	return ""
}

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

	// Fetch one row beyond the page so hasMore is knowable without a count query
	// or refetching earlier pages.
	jobs, err := apiClient.ListJobsWindow(ctx, offset, jobStatusPageSize+1)
	if err != nil {
		return JobStatusListDTO{Error: fmt.Sprintf("Failed to fetch jobs: %v", err)}
	}

	hasMore := len(jobs) > jobStatusPageSize
	if hasMore {
		jobs = jobs[:jobStatusPageSize]
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
					reason := latestStatusReason(statuses)
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
