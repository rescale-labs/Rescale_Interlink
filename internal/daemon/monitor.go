// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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

// EligibilityConfig defines criteria for auto-download eligibility.
// v4.0.0: Used by Windows Service auto-download feature.
type EligibilityConfig struct {
	// CorrectnessTag is the tag required for a job to be eligible (e.g., "isCorrect:true")
	// If empty, correctness tag checking is disabled.
	CorrectnessTag string

	// AutoDownloadField is the custom field name to check (default: "Auto Download")
	AutoDownloadField string

	// AutoDownloadValue is the required value for the field (default: "Enable")
	AutoDownloadValue string

	// DownloadedTag is the tag added after successful download (default: "autoDownloaded:true")
	// Jobs with this tag are skipped.
	DownloadedTag string

	// AutoDownloadPathField is the custom field for per-job download path (default: "Auto Download Path")
	// If set, overrides the default download directory.
	AutoDownloadPathField string

	// LookbackDays is the number of days to look back for completed jobs (default: 7).
	// Jobs older than this are ignored.
	LookbackDays int
}

// DefaultEligibilityConfig returns the default eligibility configuration.
func DefaultEligibilityConfig() *EligibilityConfig {
	return &EligibilityConfig{
		CorrectnessTag:        "isCorrect:true",
		AutoDownloadField:     "Auto Download",
		AutoDownloadValue:     "Enable",
		DownloadedTag:         "autoDownloaded:true",
		AutoDownloadPathField: "Auto Download Path",
		LookbackDays:          7,
	}
}

// Monitor watches for completed jobs and triggers downloads.
type Monitor struct {
	apiClient   *api.Client
	state       *State
	filter      *JobFilter
	eligibility *EligibilityConfig // v4.0.0: Auto-download eligibility config
	logger      *logging.Logger
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

// NewMonitorWithEligibility creates a new job monitor with eligibility checking.
// v4.0.0: Used by Windows Service auto-download feature.
func NewMonitorWithEligibility(client *api.Client, state *State, filter *JobFilter, eligibility *EligibilityConfig, logger *logging.Logger) *Monitor {
	if eligibility == nil {
		eligibility = DefaultEligibilityConfig()
	}
	return &Monitor{
		apiClient:   client,
		state:       state,
		filter:      filter,
		eligibility: eligibility,
		logger:      logger,
	}
}

// SetEligibility sets the eligibility configuration.
func (m *Monitor) SetEligibility(cfg *EligibilityConfig) {
	m.eligibility = cfg
}

// CheckEligibility checks if a job is eligible for auto-download.
// Returns (eligible, reason) where reason explains why the job is/isn't eligible.
// v4.0.0: Implements the auto-download eligibility engine.
func (m *Monitor) CheckEligibility(ctx context.Context, jobID string) (bool, string) {
	if m.eligibility == nil {
		return true, "eligibility checking disabled"
	}

	// Check for already-downloaded tag
	if m.eligibility.DownloadedTag != "" {
		hasDownloadedTag, err := m.apiClient.HasJobTag(ctx, jobID, m.eligibility.DownloadedTag)
		if err != nil {
			m.logger.Warn().Err(err).Str("job_id", jobID).Msg("Failed to check downloaded tag")
			return false, fmt.Sprintf("failed to check downloaded tag: %v", err)
		}
		if hasDownloadedTag {
			return false, fmt.Sprintf("job already has '%s' tag", m.eligibility.DownloadedTag)
		}
	}

	// Check correctness tag
	if m.eligibility.CorrectnessTag != "" {
		hasCorrectnessTag, err := m.apiClient.HasJobTag(ctx, jobID, m.eligibility.CorrectnessTag)
		if err != nil {
			m.logger.Warn().Err(err).Str("job_id", jobID).Msg("Failed to check correctness tag")
			return false, fmt.Sprintf("failed to check correctness tag: %v", err)
		}
		if !hasCorrectnessTag {
			return false, fmt.Sprintf("job missing required tag '%s'", m.eligibility.CorrectnessTag)
		}
	}

	// Check Auto Download custom field
	if m.eligibility.AutoDownloadField != "" {
		fieldValue, err := m.apiClient.GetJobCustomFieldValue(ctx, jobID, m.eligibility.AutoDownloadField)
		if err != nil {
			m.logger.Warn().Err(err).Str("job_id", jobID).Msg("Failed to check custom field")
			return false, fmt.Sprintf("failed to check custom field: %v", err)
		}
		if !strings.EqualFold(fieldValue, m.eligibility.AutoDownloadValue) {
			return false, fmt.Sprintf("custom field '%s' is '%s', expected '%s'",
				m.eligibility.AutoDownloadField, fieldValue, m.eligibility.AutoDownloadValue)
		}
	}

	return true, "all eligibility checks passed"
}

// GetJobDownloadPath returns the download path for a job.
// If the job has a custom "Auto Download Path" field, uses that; otherwise returns empty string.
func (m *Monitor) GetJobDownloadPath(ctx context.Context, jobID string) string {
	if m.eligibility == nil || m.eligibility.AutoDownloadPathField == "" {
		return ""
	}
	path, err := m.apiClient.GetJobCustomFieldValue(ctx, jobID, m.eligibility.AutoDownloadPathField)
	if err != nil {
		m.logger.Debug().Err(err).Str("job_id", jobID).Msg("Failed to get custom download path")
		return ""
	}
	return path
}

// CompletedJob represents a job ready for download.
type CompletedJob struct {
	ID      string
	Name    string
	Status  string
	Owner   string
	Created string
}

// FindCompletedJobs returns jobs that are completed but not yet downloaded.
func (m *Monitor) FindCompletedJobs(ctx context.Context) ([]*CompletedJob, error) {
	m.logger.Debug().Msg("Fetching job list")

	jobs, err := m.apiClient.ListJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}

	m.logger.Debug().Int("total_jobs", len(jobs)).Msg("Found jobs")

	// v4.0.0 (D2.6): Calculate lookback cutoff date if eligibility is configured
	var lookbackCutoff time.Time
	if m.eligibility != nil && m.eligibility.LookbackDays > 0 {
		lookbackCutoff = time.Now().AddDate(0, 0, -m.eligibility.LookbackDays)
		m.logger.Debug().
			Int("lookback_days", m.eligibility.LookbackDays).
			Time("cutoff", lookbackCutoff).
			Msg("Applying lookback filter")
	}

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

		// v4.0.0 (D2.6): Apply lookback window filter
		if !lookbackCutoff.IsZero() && job.CreatedAt != "" {
			if jobTime, err := time.Parse(time.RFC3339, job.CreatedAt); err == nil {
				if jobTime.Before(lookbackCutoff) {
					m.logger.Debug().
						Str("job_id", job.ID).
						Str("job_name", job.Name).
						Time("job_created", jobTime).
						Msg("Job older than lookback window, skipping")
					continue
				}
			}
		}

		// Apply name filters
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
