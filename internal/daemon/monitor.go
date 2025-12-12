// Package daemon provides background service functionality for auto-downloading completed jobs.
// Version: 3.4.0
// Date: December 2025
package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
)

// JobFilter defines criteria for filtering jobs.
type JobFilter struct {
	// NamePrefix filters jobs by name prefix (case-insensitive)
	NamePrefix string

	// NameContains filters jobs that contain this substring (case-insensitive)
	NameContains string

	// ExcludeNames filters out jobs matching these prefixes (case-insensitive)
	ExcludeNames []string
}

// Monitor watches for completed jobs and triggers downloads.
type Monitor struct {
	apiClient *api.Client
	state     *State
	filter    *JobFilter
	logger    *logging.Logger
}

// NewMonitor creates a new job monitor.
func NewMonitor(client *api.Client, state *State, filter *JobFilter, logger *logging.Logger) *Monitor {
	return &Monitor{
		apiClient: client,
		state:     state,
		filter:    filter,
		logger:    logger,
	}
}

// CompletedJob represents a job ready for download.
type CompletedJob struct {
	ID       string
	Name     string
	Status   string
	Owner    string
	Created  string
	FileInfo *JobFileInfo
}

// JobFileInfo contains aggregated file information for a job.
type JobFileInfo struct {
	FileCount int
	TotalSize int64
}

// FindCompletedJobs returns jobs that are completed but not yet downloaded.
func (m *Monitor) FindCompletedJobs(ctx context.Context) ([]*CompletedJob, error) {
	m.logger.Debug().Msg("Fetching job list")

	jobs, err := m.apiClient.ListJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}

	m.logger.Debug().Int("total_jobs", len(jobs)).Msg("Found jobs")

	var completed []*CompletedJob

	for _, job := range jobs {
		// Check if job status is "Completed"
		if job.JobStatus.Status != "Completed" {
			continue
		}

		// Check if already downloaded
		if m.state.IsDownloaded(job.ID) {
			m.logger.Debug().
				Str("job_id", job.ID).
				Str("job_name", job.Name).
				Msg("Skipping already downloaded job")
			continue
		}

		// Apply filters
		if !m.matchesFilter(job) {
			m.logger.Debug().
				Str("job_id", job.ID).
				Str("job_name", job.Name).
				Msg("Job filtered out by name filter")
			continue
		}

		completed = append(completed, &CompletedJob{
			ID:      job.ID,
			Name:    job.Name,
			Status:  job.JobStatus.Status,
			Owner:   job.Owner,
			Created: job.CreatedAt,
		})
	}

	m.logger.Info().
		Int("completed_count", len(completed)).
		Int("total_jobs", len(jobs)).
		Msg("Found completed jobs for download")

	return completed, nil
}

// matchesFilter checks if a job matches the configured filters.
func (m *Monitor) matchesFilter(job models.JobResponse) bool {
	if m.filter == nil {
		return true
	}

	jobNameLower := strings.ToLower(job.Name)

	// Check name prefix
	if m.filter.NamePrefix != "" {
		prefixLower := strings.ToLower(m.filter.NamePrefix)
		if !strings.HasPrefix(jobNameLower, prefixLower) {
			return false
		}
	}

	// Check name contains
	if m.filter.NameContains != "" {
		containsLower := strings.ToLower(m.filter.NameContains)
		if !strings.Contains(jobNameLower, containsLower) {
			return false
		}
	}

	// Check exclusions
	for _, exclude := range m.filter.ExcludeNames {
		excludeLower := strings.ToLower(exclude)
		if strings.HasPrefix(jobNameLower, excludeLower) {
			return false
		}
	}

	return true
}

// GetJobFileInfo retrieves file count and total size for a job.
func (m *Monitor) GetJobFileInfo(ctx context.Context, jobID string) (*JobFileInfo, error) {
	files, err := m.apiClient.ListJobFiles(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("failed to list job files: %w", err)
	}

	info := &JobFileInfo{
		FileCount: len(files),
	}

	for _, f := range files {
		info.TotalSize += f.DecryptedSize
	}

	return info, nil
}

// ComputeOutputDir determines the output directory for a job.
// When useJobName is true, the directory name includes both the sanitized job name
// and the job ID (truncated) to avoid collisions from jobs with the same name.
func ComputeOutputDir(baseDir, jobID, jobName string, useJobName bool) string {
	if useJobName && jobName != "" {
		// Sanitize job name for use as directory name
		safeName := sanitizeDirectoryName(jobName)
		// Always include job ID suffix to avoid collisions from jobs with same name
		// Use short ID (first 6 chars) for readability
		shortID := jobID
		if len(shortID) > 6 {
			shortID = shortID[:6]
		}
		return filepath.Join(baseDir, fmt.Sprintf("%s_%s", safeName, shortID))
	}
	// Default: use job ID
	return filepath.Join(baseDir, fmt.Sprintf("job_%s", jobID))
}

// sanitizeDirectoryName makes a job name safe for use as a directory name.
func sanitizeDirectoryName(name string) string {
	// Replace problematic characters
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		"\n", "_",
		"\r", "_",
	)
	sanitized := replacer.Replace(name)

	// Trim leading/trailing whitespace and dots
	sanitized = strings.TrimSpace(sanitized)
	sanitized = strings.Trim(sanitized, ".")

	// Limit length
	if len(sanitized) > 100 {
		sanitized = sanitized[:100]
	}

	// Trim trailing whitespace after truncation
	sanitized = strings.TrimSpace(sanitized)

	// Fallback if empty
	if sanitized == "" {
		sanitized = "unnamed_job"
	}

	return sanitized
}
