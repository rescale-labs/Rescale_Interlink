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
// Mode is per-job via the "Auto Download" custom field;
// only AutoDownloadTag and LookbackDays are configurable in Interlink.
type EligibilityConfig struct {
	// AutoDownloadTag is the tag to check when a job's "Auto Download" field is "Conditional".
	// Default: "autoDownload"
	AutoDownloadTag string

	// LookbackDays is the number of days to look back for completed jobs (default: 7).
	// Jobs older than this are ignored.
	LookbackDays int
}

// DefaultEligibilityConfig returns the default eligibility configuration.
func DefaultEligibilityConfig() *EligibilityConfig {
	return &EligibilityConfig{
		AutoDownloadTag: "autoDownload",
		LookbackDays:    7,
	}
}

// SkipReasonCode is a stable machine-readable identifier for why a job was
// skipped (or never considered) by the daemon. Codes drive the per-poll scan
// summary buckets (ScanSummary) and the silent-vs-logged decision for the
// per-job log line.
type SkipReasonCode string

const (
	// ReasonNone is the zero value; used when a job was downloaded or is still
	// under consideration.
	ReasonNone SkipReasonCode = ""

	// ReasonNotCompleted — job status is not "Completed".
	ReasonNotCompleted SkipReasonCode = "not_completed"

	// ReasonAlreadyDownloadedLocal — local state.json says the job is already
	// downloaded. Cross-session bookkeeping (the "downloaded" tag on the
	// Rescale side) is ReasonHasDownloadedTag.
	ReasonAlreadyDownloadedLocal SkipReasonCode = "already_downloaded_local"

	// ReasonTooOldCreationPrefilter — creation date older than the API
	// pre-filter cutoff (lookback_days + 30); short-circuits the lookback
	// check without an extra API call.
	ReasonTooOldCreationPrefilter SkipReasonCode = "too_old_creation_prefilter"

	// ReasonNameFilter — job excluded by configured name filters.
	ReasonNameFilter SkipReasonCode = "name_filter"

	// ReasonOutsideLookbackWindow — completion time is before the configured
	// lookback window.
	ReasonOutsideLookbackWindow SkipReasonCode = "outside_lookback_window"

	// ReasonAutoDownloadUnset — "Auto Download" custom field is empty. Silent.
	ReasonAutoDownloadUnset SkipReasonCode = "auto_download_unset"

	// ReasonAutoDownloadDisabled — "Auto Download" custom field is Disabled.
	// Silent.
	ReasonAutoDownloadDisabled SkipReasonCode = "auto_download_disabled"

	// ReasonAutoDownloadUnrecognized — "Auto Download" field has a value
	// that is not Enabled / Disabled / Conditional. Silent (treated as
	// opt-out).
	ReasonAutoDownloadUnrecognized SkipReasonCode = "auto_download_unrecognized"

	// ReasonHasDownloadedTag — job already carries the "downloaded" tag on
	// the Rescale side. Logged (useful diagnostic when users expect
	// re-download after tag removal).
	ReasonHasDownloadedTag SkipReasonCode = "has_downloaded_tag"

	// ReasonConditionalMissingTag — "Auto Download" is Conditional but the
	// job lacks the configured auto-download tag.
	ReasonConditionalMissingTag SkipReasonCode = "conditional_missing_tag"

	// ReasonFieldCheckAPIError — fetching the "Auto Download" custom field
	// failed. Silent, matching current behavior at monitor.go when field
	// lookup errors; a workspace without the field returns an error here
	// rather than a value, which would otherwise spam WARN for every job.
	ReasonFieldCheckAPIError SkipReasonCode = "field_check_api_error"

	// ReasonDownloadedTagCheckAPIError — checking whether the "downloaded"
	// tag is present failed. Logged.
	ReasonDownloadedTagCheckAPIError SkipReasonCode = "downloaded_tag_check_api_error"

	// ReasonConditionalTagCheckAPIError — checking the conditional
	// auto-download tag failed. Logged.
	ReasonConditionalTagCheckAPIError SkipReasonCode = "conditional_tag_check_api_error"

	// ReasonCompletionTimeAPIError — fetching the job's completion time
	// failed. Logged.
	ReasonCompletionTimeAPIError SkipReasonCode = "completion_time_api_error"

	// ReasonInRetryBackoff — job previously failed and is in exponential
	// backoff; not retried yet. Silent.
	ReasonInRetryBackoff SkipReasonCode = "in_retry_backoff"

	// ReasonPendingTagApply — job's files are on disk but the downloaded
	// tag API call failed; daemon retries the tag call separately. Silent
	// (transient, recovers on its own). Added by Plan 3 so pending-tag jobs
	// are not re-downloaded while the tag retry is still pending.
	ReasonPendingTagApply SkipReasonCode = "pending_tag_apply"
)

// IsSilent reports whether this skip reason should be omitted from the
// per-job INFO log line. Silent reasons still participate in the per-poll
// scan summary.
func (c SkipReasonCode) IsSilent() bool {
	switch c {
	case ReasonNone,
		ReasonNotCompleted,
		ReasonAlreadyDownloadedLocal,
		ReasonTooOldCreationPrefilter,
		ReasonNameFilter,
		ReasonAutoDownloadUnset,
		ReasonAutoDownloadDisabled,
		ReasonAutoDownloadUnrecognized,
		ReasonFieldCheckAPIError,
		ReasonInRetryBackoff,
		ReasonPendingTagApply,
		ReasonHasDownloadedTag:
		return true
	default:
		return false
	}
}

// SkipReason is a machine-readable code plus human-readable detail for why a
// job was skipped. The code drives log level and per-bucket counts; the
// detail is shown in the per-job log line when logged.
type SkipReason struct {
	Code   SkipReasonCode
	Detail string
}

// CheckEligibilityResult contains the eligibility check result.
type CheckEligibilityResult struct {
	// EligibleForDownload is true when the job should be downloaded.
	EligibleForDownload bool

	// Reason carries the skip reason when EligibleForDownload is false.
	// When eligible, Reason.Code == ReasonNone.
	Reason SkipReason

	// Detail is a human-readable explanation: a positive reason when the
	// job is eligible ("Auto Download is Enabled"), otherwise the skip
	// detail (same string as Reason.Detail).
	Detail string
}

// Monitor watches for completed jobs and triggers downloads.
type Monitor struct {
	apiClient   *api.Client
	state       *State
	filter      *JobFilter
	eligibility *EligibilityConfig
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
//
// Plan 3 tag-first order:
//   1. `downloaded` tag present  → skip silently (common case every poll)
//   2. `Auto Download` custom field check (Disabled/empty → silent skip)
//   3. Conditional tag check (when field is Conditional)
//
// The tag check is step 1 so a user who revokes the `downloaded` tag in
// the Rescale web UI triggers a re-download on the next poll — spec §7.6.
// Field lookup failures are silent (workspaces without the Auto Download
// field error here for every job, so logging each would be noise).
func (m *Monitor) CheckEligibility(ctx context.Context, jobID string) CheckEligibilityResult {
	if m.eligibility == nil {
		return CheckEligibilityResult{EligibleForDownload: true, Detail: "eligibility checking disabled"}
	}

	// Step 1: downloaded tag is authoritative over local state (Plan 3 F9).
	hasDownloadedTag, err := m.apiClient.HasJobTag(ctx, jobID, config.DownloadedTag)
	if err != nil {
		m.logger.Warn().Err(err).Str("job_id", jobID).Msg("Failed to check downloaded tag")
		detail := fmt.Sprintf("failed to check 'downloaded' tag: %v", err)
		return CheckEligibilityResult{
			Reason: SkipReason{Code: ReasonDownloadedTagCheckAPIError, Detail: detail},
			Detail: detail,
		}
	}
	if hasDownloadedTag {
		detail := fmt.Sprintf("already has '%s' tag", config.DownloadedTag)
		return CheckEligibilityResult{
			Reason: SkipReason{Code: ReasonHasDownloadedTag, Detail: detail},
			Detail: detail,
		}
	}

	// Step 2: check custom field.
	fieldValue, err := m.apiClient.GetJobCustomFieldValue(ctx, jobID, config.AutoDownloadFieldName)
	if err != nil {
		m.logger.Debug().Err(err).Str("job_id", jobID).Msg("Failed to get Auto Download field")
		detail := fmt.Sprintf("failed to check field: %v", err)
		return CheckEligibilityResult{
			Reason: SkipReason{Code: ReasonFieldCheckAPIError, Detail: detail},
			Detail: detail,
		}
	}

	fieldLower := strings.ToLower(strings.TrimSpace(fieldValue))

	// If not set or disabled → NOT a real candidate, skip silently
	if fieldLower == "" || fieldLower == "disabled" {
		code := ReasonAutoDownloadDisabled
		label := "disabled"
		if fieldValue == "" {
			code = ReasonAutoDownloadUnset
			label = "not set"
		}
		detail := fmt.Sprintf("Auto Download is %s", label)
		m.logger.Debug().
			Str("job_id", jobID).
			Str("field_value", fieldValue).
			Msgf("Auto Download is %s - silent skip", label)
		return CheckEligibilityResult{
			Reason: SkipReason{Code: code, Detail: detail},
			Detail: detail,
		}
	}

	// Step 3: Handle Enabled
	if fieldLower == "enabled" {
		m.logger.Debug().Str("job_id", jobID).Msg("Auto Download is Enabled - eligible")
		return CheckEligibilityResult{EligibleForDownload: true, Detail: "Auto Download is Enabled"}
	}

	// Step 5: Handle Conditional
	if fieldLower == "conditional" {
		if m.eligibility.AutoDownloadTag == "" {
			m.logger.Debug().Str("job_id", jobID).Msg("Auto Download is Conditional but no tag configured - eligible")
			return CheckEligibilityResult{EligibleForDownload: true, Detail: "Auto Download is Conditional (no tag configured)"}
		}
		hasTag, err := m.apiClient.HasJobTag(ctx, jobID, m.eligibility.AutoDownloadTag)
		if err != nil {
			m.logger.Warn().Err(err).Str("job_id", jobID).Str("tag", m.eligibility.AutoDownloadTag).Msg("Failed to check conditional tag")
			detail := fmt.Sprintf("failed to check conditional tag %q: %v", m.eligibility.AutoDownloadTag, err)
			return CheckEligibilityResult{
				Reason: SkipReason{Code: ReasonConditionalTagCheckAPIError, Detail: detail},
				Detail: detail,
			}
		}
		if !hasTag {
			m.logger.Debug().Str("job_id", jobID).Str("required_tag", m.eligibility.AutoDownloadTag).Msg("Conditional but missing tag")
			detail := fmt.Sprintf("Auto Download is Conditional but missing tag %q", m.eligibility.AutoDownloadTag)
			return CheckEligibilityResult{
				Reason: SkipReason{Code: ReasonConditionalMissingTag, Detail: detail},
				Detail: detail,
			}
		}
		m.logger.Debug().Str("job_id", jobID).Str("tag", m.eligibility.AutoDownloadTag).Msg("Conditional with required tag - eligible")
		return CheckEligibilityResult{
			EligibleForDownload: true,
			Detail:              fmt.Sprintf("Auto Download is Conditional with tag %q", m.eligibility.AutoDownloadTag),
		}
	}

	// Unknown value - treat as not a candidate (silent skip)
	m.logger.Warn().Str("job_id", jobID).Str("field_value", fieldValue).Msg("Unrecognized Auto Download value")
	detail := fmt.Sprintf("unrecognized value: %q", fieldValue)
	return CheckEligibilityResult{
		Reason: SkipReason{Code: ReasonAutoDownloadUnrecognized, Detail: detail},
		Detail: detail,
	}
}

// GetJobDownloadPath returns the download path for a job.
// If the job has a custom "Auto Download Path" field, uses that; otherwise returns empty string.
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
	CompletedAt time.Time
}

// getJobCompletionTime retrieves the actual completion time from job status history.
// Uses completion time (not creation time) for accurate lookback filtering.
// Retries once on failure (500ms delay) before returning error.
func (m *Monitor) getJobCompletionTime(ctx context.Context, jobID string) (time.Time, error) {
	completionTime, err := m.getJobCompletionTimeOnce(ctx, jobID)
	if err != nil {
		// Retry once after 500ms
		time.Sleep(500 * time.Millisecond)
		completionTime, err = m.getJobCompletionTimeOnce(ctx, jobID)
	}
	return completionTime, err
}

// getJobCompletionTimeOnce performs a single attempt to get the completion time.
func (m *Monitor) getJobCompletionTimeOnce(ctx context.Context, jobID string) (time.Time, error) {
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

// ScanSummary aggregates per-reason and per-outcome counts for a single poll
// cycle. FindCompletedJobs populates the pre-eligibility skip buckets; the
// caller (daemon.poll) extends it with per-job eligibility skips and
// download outcomes before emitting the single canonical scan-summary INFO
// line.
type ScanSummary struct {
	// TotalScanned is the raw number of jobs returned by the API for this
	// scan (before any filtering).
	TotalScanned int

	// EligibilityChecked is the number of jobs that actually reached the
	// CheckEligibility call — i.e., they passed Completed + not-already-
	// downloaded + creation-prefilter + name-filter + lookback. This is the
	// correct denominator for "all unset" predicates.
	EligibilityChecked int

	// SkipBuckets counts skips keyed by SkipReasonCode. Includes both
	// pre-eligibility skips (from FindCompletedJobs) and per-job eligibility
	// skips (added by the poll loop).
	SkipBuckets map[SkipReasonCode]int

	// DownloadOutcomes counts jobs keyed by DownloadOutcome. Only populated
	// by the poll loop.
	DownloadOutcomes map[string]int
}

// AddSkip increments the count for a skip reason.
func (s *ScanSummary) AddSkip(code SkipReasonCode) {
	if s.SkipBuckets == nil {
		s.SkipBuckets = make(map[SkipReasonCode]int)
	}
	s.SkipBuckets[code]++
}

// AddOutcome increments the count for a download outcome. The outcome arg
// is typed as any string-like so daemon.poll can pass DownloadOutcome
// without importing a circular dependency.
func (s *ScanSummary) AddOutcome(outcome string) {
	if s.DownloadOutcomes == nil {
		s.DownloadOutcomes = make(map[string]int)
	}
	s.DownloadOutcomes[outcome]++
}

// FindCompletedJobsResult contains the results of scanning for completed jobs.
type FindCompletedJobsResult struct {
	Candidates   []*CompletedJob
	TotalScanned int

	// Summary carries the pre-eligibility skip buckets. The poll loop
	// extends this in place with per-job eligibility skips and download
	// outcomes before emitting the single canonical INFO line.
	Summary *ScanSummary
}

// FindCompletedJobs returns jobs that are completed and warrant an
// eligibility check. The pendingSet (job IDs whose files are on disk but
// whose downloaded tag call has not yet succeeded) are skipped
// pre-eligibility so the tag-first check (Plan 3) cannot re-enqueue them
// for re-download while their tag is still being retried by the poll
// loop's separate tag-retry pass.
func (m *Monitor) FindCompletedJobs(ctx context.Context, pendingSet map[string]struct{}) (*FindCompletedJobsResult, error) {
	m.logger.Debug().Msg("Fetching job list")

	// Calculate lookback cutoff date if eligibility is configured.
	// Based on completion time, not creation time.
	var lookbackCutoff time.Time
	if m.eligibility != nil && m.eligibility.LookbackDays > 0 {
		lookbackCutoff = time.Now().AddDate(0, 0, -m.eligibility.LookbackDays)
		m.logger.Debug().
			Int("lookback_days", m.eligibility.LookbackDays).
			Time("completion_cutoff", lookbackCutoff).
			Msg("Applying lookback filter (jobs completed before this date are skipped)")
	}

	// Use optimized API call with early termination when lookback is configured.
	// Fetches jobs ordered by date (newest first) and stops when hitting old jobs.
	var jobs []models.JobResponse
	var err error
	if !lookbackCutoff.IsZero() {
		// Use creation cutoff with buffer for API early termination (optimization only)
		// Jobs created more than (lookback_days + 30) days ago cannot have completed within window
		creationCutoff := time.Now().AddDate(0, 0, -(m.eligibility.LookbackDays + 30))
		m.logger.Debug().
			Time("creation_cutoff", creationCutoff).
			Msg("API pre-filter: skipping jobs created before this date (optimization, not lookback)")
		jobs, err = m.apiClient.ListJobsWithCutoff(ctx, creationCutoff)
	} else {
		jobs, err = m.apiClient.ListJobs(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}

	// Debug-level only — verbose stats not useful in GUI
	m.logger.Debug().Int("jobs_to_scan", len(jobs)).Msg("Scanning jobs from API")

	// Pre-filter buffer: Use creation date with extra buffer to reduce API calls
	// Jobs created more than (lookback_days + 30) days ago cannot have completed within lookback window
	var creationCutoff time.Time
	if !lookbackCutoff.IsZero() {
		creationCutoff = time.Now().AddDate(0, 0, -(m.eligibility.LookbackDays + 30))
	}

	var completed []*CompletedJob
	summary := &ScanSummary{
		TotalScanned:     len(jobs),
		SkipBuckets:      make(map[SkipReasonCode]int),
		DownloadOutcomes: make(map[string]int),
	}

	for _, job := range jobs {
		// Check if job status is "Completed"
		if job.JobStatus.Status != "Completed" {
			summary.AddSkip(ReasonNotCompleted)
			continue
		}

		// Plan 3: jobs whose files are on disk but whose downloaded tag
		// call has not yet succeeded are suppressed pre-eligibility so
		// they are not re-downloaded during the poll loop's tag-retry
		// pass (see Daemon.poll). Silent — this is a transient state.
		if pendingSet != nil {
			if _, pending := pendingSet[job.ID]; pending {
				summary.AddSkip(ReasonPendingTagApply)
				continue
			}
		}

		// Pre-filter: Skip jobs created too long ago (can't have completed within window)
		// This avoids API calls for obviously out-of-window jobs
		if !creationCutoff.IsZero() && job.CreatedAt != "" {
			if createdAt, err := time.Parse(time.RFC3339, job.CreatedAt); err == nil {
				if createdAt.Before(creationCutoff) {
					summary.AddSkip(ReasonTooOldCreationPrefilter)
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
			summary.AddSkip(ReasonNameFilter)
			m.logger.Debug().
				Str("job_id", job.ID).
				Str("job_name", job.Name).
				Msg("Job filtered out by name filter")
			continue
		}

		// Get actual completion time for accurate lookback filtering.
		// On unknown completion time, include the job rather than falling back
		// to creation time (which would incorrectly skip long-running jobs).
		var completedAt time.Time
		if !lookbackCutoff.IsZero() {
			var err error
			completedAt, err = m.getJobCompletionTime(ctx, job.ID)
			if err != nil {
				summary.AddSkip(ReasonCompletionTimeAPIError)
				m.logger.Debug().
					Str("job_id", job.ID).
					Str("job_name", job.Name).
					Err(err).
					Msg("Could not get completion time after retry — including job (passed creation pre-filter)")
				// Don't fall back to creation time — job already passed the
				// creation pre-filter so it's likely recent enough. Include it.
				// completedAt stays zero, which skips the lookback filter below.
			}

			// Apply lookback filter based on completion time.
			// Zero completedAt (unknown) is NOT filtered — include the job.
			if !completedAt.IsZero() && completedAt.Before(lookbackCutoff) {
				summary.AddSkip(ReasonOutsideLookbackWindow)
				m.logger.Debug().
					Str("job_id", job.ID).
					Str("job_name", job.Name).
					Time("completed_at", completedAt).
					Time("completion_cutoff", lookbackCutoff).
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

	// Detailed pre-eligibility stats are at DEBUG level; the canonical scan
	// summary INFO line is emitted by daemon.poll after extending the buckets
	// with per-job eligibility skips and download outcomes.
	m.logger.Debug().
		Int("total_scanned", len(jobs)).
		Int("candidates", len(completed)).
		Msg("Pre-eligibility scan complete")

	return &FindCompletedJobsResult{
		Candidates:   completed,
		TotalScanned: len(jobs),
		Summary:      summary,
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
