// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
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
// v4.3.0: Simplified - mode is now per-job via the "Auto Download" custom field.
// Only AutoDownloadTag and LookbackDays are configurable in Interlink.
type EligibilityConfig struct {
	// AutoDownloadTag is the tag to check when a job's "Auto Download" field is "Conditional".
	// Default: "autoDownload"
	AutoDownloadTag string

	// LookbackDays is the number of days to look back for completed jobs (default: 7).
	// Jobs older than this are ignored.
	LookbackDays int
}

// DefaultEligibilityConfig returns the default eligibility configuration.
// v4.3.0: Simplified - only AutoDownloadTag and LookbackDays are configurable.
func DefaultEligibilityConfig() *EligibilityConfig {
	return &EligibilityConfig{
		AutoDownloadTag: "autoDownload",
		LookbackDays:    7,
	}
}

// CheckEligibilityResult contains the eligibility check result.
// v4.3.6: Added ShouldLog flag to distinguish between "not a candidate" (silent skip)
// and "is a candidate but not eligible" (logged skip).
type CheckEligibilityResult struct {
	Eligible  bool   // Whether the job should be downloaded
	Reason    string // Human-readable explanation
	ShouldLog bool   // False for "not set"/"disabled" - caller should skip silently
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
// Returns CheckEligibilityResult with Eligible, Reason, and ShouldLog fields.
// v4.3.6: CRITICAL FIX - Check custom field FIRST to minimize API calls and enable silent filtering.
// - ShouldLog=false for "not set"/"disabled" → caller skips silently (not a real candidate)
// - ShouldLog=true for everything else → caller logs the result
// v4.3.0: Mode is now per-job via the "Auto Download" custom field.
// - "Disabled" or empty → not eligible, ShouldLog=false (silent)
// - "Enabled" → eligible (no tag check)
// - "Conditional" → eligible only if job has the configured tag
func (m *Monitor) CheckEligibility(ctx context.Context, jobID string) CheckEligibilityResult {
	if m.eligibility == nil {
		return CheckEligibilityResult{Eligible: true, Reason: "eligibility checking disabled", ShouldLog: true}
	}

	// v4.3.6: Step 1 - Check custom field FIRST (determines if this is a real candidate)
	// This saves API calls by not checking tags for jobs that will be silently skipped anyway
	fieldValue, err := m.apiClient.GetJobCustomFieldValue(ctx, jobID, config.AutoDownloadFieldName)
	if err != nil {
		m.logger.Debug().Err(err).Str("job_id", jobID).Msg("Failed to get Auto Download field")
		return CheckEligibilityResult{Eligible: false, Reason: fmt.Sprintf("failed to check field: %v", err), ShouldLog: false}
	}

	fieldLower := strings.ToLower(strings.TrimSpace(fieldValue))

	// Step 2: If not set or disabled → NOT a real candidate, skip silently
	if fieldLower == "" || fieldLower == "disabled" {
		reason := "disabled"
		if fieldValue == "" {
			reason = "not set"
		}
		m.logger.Debug().
			Str("job_id", jobID).
			Str("field_value", fieldValue).
			Msgf("Auto Download is %s - silent skip", reason)
		return CheckEligibilityResult{Eligible: false, Reason: fmt.Sprintf("Auto Download is %s", reason), ShouldLog: false}
	}

	// Step 3: This IS a real candidate (Enabled or Conditional) - now check downloaded tag
	hasDownloadedTag, err := m.apiClient.HasJobTag(ctx, jobID, config.DownloadedTag)
	if err != nil {
		m.logger.Warn().Err(err).Str("job_id", jobID).Msg("Failed to check downloaded tag")
		return CheckEligibilityResult{Eligible: false, Reason: fmt.Sprintf("failed to check tag: %v", err), ShouldLog: true}
	}
	if hasDownloadedTag {
		return CheckEligibilityResult{Eligible: false, Reason: fmt.Sprintf("already has '%s' tag", config.DownloadedTag), ShouldLog: true}
	}

	// Step 4: Handle Enabled
	if fieldLower == "enabled" {
		m.logger.Debug().Str("job_id", jobID).Msg("Auto Download is Enabled - eligible")
		return CheckEligibilityResult{Eligible: true, Reason: "Auto Download is Enabled", ShouldLog: true}
	}

	// Step 5: Handle Conditional - check conditional tag
	if fieldLower == "conditional" {
		if m.eligibility.AutoDownloadTag == "" {
			m.logger.Debug().Str("job_id", jobID).Msg("Auto Download is Conditional but no tag configured - eligible")
			return CheckEligibilityResult{Eligible: true, Reason: "Auto Download is Conditional (no tag configured)", ShouldLog: true}
		}
		hasTag, err := m.apiClient.HasJobTag(ctx, jobID, m.eligibility.AutoDownloadTag)
		if err != nil {
			m.logger.Warn().Err(err).Str("job_id", jobID).Str("tag", m.eligibility.AutoDownloadTag).Msg("Failed to check conditional tag")
			return CheckEligibilityResult{Eligible: false, Reason: fmt.Sprintf("failed to check tag '%s': %v", m.eligibility.AutoDownloadTag, err), ShouldLog: true}
		}
		if !hasTag {
			m.logger.Debug().Str("job_id", jobID).Str("required_tag", m.eligibility.AutoDownloadTag).Msg("Conditional but missing tag")
			return CheckEligibilityResult{Eligible: false, Reason: fmt.Sprintf("Auto Download is Conditional but missing tag '%s'", m.eligibility.AutoDownloadTag), ShouldLog: true}
		}
		m.logger.Debug().Str("job_id", jobID).Str("tag", m.eligibility.AutoDownloadTag).Msg("Conditional with required tag - eligible")
		return CheckEligibilityResult{Eligible: true, Reason: fmt.Sprintf("Auto Download is Conditional with tag '%s'", m.eligibility.AutoDownloadTag), ShouldLog: true}
	}

	// Unknown value - treat as not a candidate (silent skip)
	m.logger.Warn().Str("job_id", jobID).Str("field_value", fieldValue).Msg("Unrecognized Auto Download value")
	return CheckEligibilityResult{Eligible: false, Reason: fmt.Sprintf("unrecognized value: '%s'", fieldValue), ShouldLog: false}
}

// GetJobDownloadPath returns the download path for a job.
// If the job has a custom "Auto Download Path" field, uses that; otherwise returns empty string.
// v4.3.0: Uses hardcoded field name from config package.
func (m *Monitor) GetJobDownloadPath(ctx context.Context, jobID string) string {
	path, err := m.apiClient.GetJobCustomFieldValue(ctx, jobID, config.AutoDownloadPathFieldName)
	if err != nil {
		m.logger.Debug().Err(err).Str("job_id", jobID).Msg("Failed to get custom download path")
		return ""
	}
	return path
}

// CompletedJob represents a job ready for download.
type CompletedJob struct {
	ID          string
	Name        string
	Status      string
	Owner       string
	Created     string
	CompletedAt time.Time // v4.3.0: Actual completion time from status history
}

// getJobCompletionTime retrieves the actual completion time from job status history.
// v4.3.0: Used for accurate lookback filtering based on when the job finished,
// not when it was created.
func (m *Monitor) getJobCompletionTime(ctx context.Context, jobID string) (time.Time, error) {
	statuses, err := m.apiClient.GetJobStatuses(ctx, jobID)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get job statuses: %w", err)
	}

	// Find the "Completed" status entry
	for _, status := range statuses {
		if status.Status == "Completed" && status.StatusDate != "" {
			completedAt, err := time.Parse(time.RFC3339, status.StatusDate)
			if err != nil {
				// Try alternative format
				completedAt, err = time.Parse("2006-01-02T15:04:05.000000Z", status.StatusDate)
				if err != nil {
					m.logger.Debug().
						Str("job_id", jobID).
						Str("status_date", status.StatusDate).
						Err(err).
						Msg("Failed to parse completion date")
					continue
				}
			}
			return completedAt, nil
		}
	}

	return time.Time{}, fmt.Errorf("no completion time found in status history")
}

// FindCompletedJobsResult contains the results of scanning for completed jobs.
type FindCompletedJobsResult struct {
	Candidates   []*CompletedJob
	TotalScanned int
}

// FindCompletedJobs returns jobs that are completed but not yet downloaded.
// v4.3.5: Returns result struct with total scanned count for logging.
func (m *Monitor) FindCompletedJobs(ctx context.Context) (*FindCompletedJobsResult, error) {
	m.logger.Debug().Msg("Fetching job list")

	// v4.3.0: Calculate lookback cutoff date if eligibility is configured
	// This is now based on completion time, not creation time
	var lookbackCutoff time.Time
	if m.eligibility != nil && m.eligibility.LookbackDays > 0 {
		lookbackCutoff = time.Now().AddDate(0, 0, -m.eligibility.LookbackDays)
		m.logger.Debug().
			Int("lookback_days", m.eligibility.LookbackDays).
			Time("cutoff", lookbackCutoff).
			Msg("Applying lookback filter (based on completion time)")
	}

	// v4.3.4: Use optimized API call with early termination when lookback is configured
	// This fetches jobs ordered by date (newest first) and stops when hitting old jobs
	var jobs []models.JobResponse
	var err error
	if !lookbackCutoff.IsZero() {
		// Use creation cutoff with buffer for API early termination
		// Jobs created more than (lookback_days + 30) days ago cannot have completed within window
		creationCutoff := time.Now().AddDate(0, 0, -(m.eligibility.LookbackDays + 30))
		jobs, err = m.apiClient.ListJobsWithCutoff(ctx, creationCutoff)
	} else {
		jobs, err = m.apiClient.ListJobs(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}

	// v4.3.5: Debug-level log for API job count (verbose stats not useful in GUI)
	m.logger.Debug().Int("jobs_to_scan", len(jobs)).Msg("Scanning jobs from API")

	// Pre-filter buffer: Use creation date with extra buffer to reduce API calls
	// Jobs created more than (lookback_days + 30) days ago cannot have completed within lookback window
	var creationCutoff time.Time
	if !lookbackCutoff.IsZero() {
		creationCutoff = time.Now().AddDate(0, 0, -(m.eligibility.LookbackDays + 30))
	}

	var completed []*CompletedJob

	// v4.3.4: Track filtering statistics
	var skippedNotCompleted, skippedAlreadyDownloaded, skippedTooOld, skippedNameFilter, skippedOutsideWindow int

	for _, job := range jobs {
		// Check if job status is "Completed"
		if job.JobStatus.Status != "Completed" {
			skippedNotCompleted++
			continue
		}

		// Check if already downloaded
		if m.state.IsDownloaded(job.ID) {
			skippedAlreadyDownloaded++
			m.logger.Debug().
				Str("job_id", job.ID).
				Str("job_name", job.Name).
				Msg("Skipping already downloaded job")
			continue
		}

		// Pre-filter: Skip jobs created too long ago (can't have completed within window)
		// This avoids API calls for obviously out-of-window jobs
		if !creationCutoff.IsZero() && job.CreatedAt != "" {
			if createdAt, err := time.Parse(time.RFC3339, job.CreatedAt); err == nil {
				if createdAt.Before(creationCutoff) {
					skippedTooOld++
					m.logger.Debug().
						Str("job_id", job.ID).
						Str("job_name", job.Name).
						Time("job_created", createdAt).
						Msg("Job created too long ago, skipping (pre-filter)")
					continue
				}
			}
		}

		// Apply name filters
		if !m.matchesFilter(job) {
			skippedNameFilter++
			m.logger.Debug().
				Str("job_id", job.ID).
				Str("job_name", job.Name).
				Msg("Job filtered out by name filter")
			continue
		}

		// v4.3.0: Get actual completion time for accurate lookback filtering
		var completedAt time.Time
		if !lookbackCutoff.IsZero() {
			var err error
			completedAt, err = m.getJobCompletionTime(ctx, job.ID)
			if err != nil {
				m.logger.Debug().
					Str("job_id", job.ID).
					Str("job_name", job.Name).
					Err(err).
					Msg("Could not get completion time, using creation time as fallback")
				// Fallback to creation time if status history unavailable
				if job.CreatedAt != "" {
					completedAt, _ = time.Parse(time.RFC3339, job.CreatedAt)
				}
			}

			// Apply lookback filter based on completion time
			if !completedAt.IsZero() && completedAt.Before(lookbackCutoff) {
				skippedOutsideWindow++
				m.logger.Debug().
					Str("job_id", job.ID).
					Str("job_name", job.Name).
					Time("completed_at", completedAt).
					Time("cutoff", lookbackCutoff).
					Msg("Job completed before lookback window, skipping")
				continue
			}
		}

		completed = append(completed, &CompletedJob{
			ID:          job.ID,
			Name:        job.Name,
			Status:      job.JobStatus.Status,
			Owner:       job.Owner,
			Created:     job.CreatedAt,
			CompletedAt: completedAt,
		})
	}

	// v4.3.5: Simplified log - detailed stats are at DEBUG level
	m.logger.Debug().
		Int("total_scanned", len(jobs)).
		Int("skipped_not_completed", skippedNotCompleted).
		Int("skipped_already_downloaded", skippedAlreadyDownloaded).
		Int("skipped_too_old", skippedTooOld).
		Int("skipped_outside_window", skippedOutsideWindow).
		Msg("Scan filter statistics")

	return &FindCompletedJobsResult{
		Candidates:   completed,
		TotalScanned: len(jobs),
	}, nil
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
