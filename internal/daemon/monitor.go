// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/validation"
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
	// Default: "autodownload"
	AutoDownloadTag string

	// LookbackDays is the number of days to look back for completed jobs (default: 7).
	// Jobs older than this are ignored.
	LookbackDays int

	// IncludeWorkspaceFolders, when true, also enumerates jobs in the
	// workspace's shared folders (recursively) in addition to the user's own
	// jobs. Default: false.
	IncludeWorkspaceFolders bool
}

// DefaultEligibilityConfig returns the default eligibility configuration.
func DefaultEligibilityConfig() *EligibilityConfig {
	return &EligibilityConfig{
		AutoDownloadTag: "autodownload",
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

	// ReasonHasStartedTag — another client's started tag holds the job: that
	// client is downloading it (see Daemon.claim). Logged, with the tag and
	// until when it holds the job, since a client that never finishes holds
	// it for all of claimLease.
	ReasonHasStartedTag SkipReasonCode = "has_started_tag"

	// ReasonClaimFailed — putting this client's started tag on the job, or
	// reading the job's tags back after, failed. Logged.
	ReasonClaimFailed SkipReasonCode = "claim_failed"

	// ReasonConditionalMissingTag — "Auto Download" is Conditional but the
	// job lacks the configured auto-download tag.
	ReasonConditionalMissingTag SkipReasonCode = "conditional_missing_tag"

	// ReasonFieldCheckAPIError — fetching the "Auto Download" custom field
	// failed. Silent, matching current behavior at monitor.go when field
	// lookup errors; a workspace without the field returns an error here
	// rather than a value, which would otherwise spam WARN for every job.
	ReasonFieldCheckAPIError SkipReasonCode = "field_check_api_error"

	// ReasonDownloadedTagCheckAPIError — fetching the job's tags, which the
	// downloaded, started and conditional checks all read, failed. Logged.
	ReasonDownloadedTagCheckAPIError SkipReasonCode = "downloaded_tag_check_api_error"

	// ReasonCompletionTimeAPIError — fetching the job's completion time
	// failed. Logged.
	ReasonCompletionTimeAPIError SkipReasonCode = "completion_time_api_error"

	// ReasonInRetryBackoff — job's download failed and it is waiting out its
	// backoff, or has used up its attempts until 'daemon retry'. Silent:
	// 'daemon list --failed' says which.
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
//  1. `downloaded` tag present  → skip silently (common case every poll)
//  2. `Auto Download` custom field check (Disabled/empty → silent skip)
//  3. Conditional tag check (when field is Conditional)
//
// The tag check is step 1 so a user who revokes the `downloaded` tag in
// the Rescale web UI triggers a re-download on the next poll — spec §7.6.
// Field lookup failures are silent (workspaces without the Auto Download
// field error here for every job, so logging each would be noise).
func (m *Monitor) CheckEligibility(ctx context.Context, job *CompletedJob) CheckEligibilityResult {
	if m.eligibility == nil {
		return CheckEligibilityResult{EligibleForDownload: true, Detail: "eligibility checking disabled"}
	}
	jobID := job.ID

	// Step 1: the job's tags, fetched once for every tag check below. The done
	// tag is authoritative over local state (Plan 3 F9).
	tags, err := m.apiClient.GetJobTags(ctx, jobID)
	if err != nil {
		m.logger.Warn().Err(err).Str("job_id", jobID).Msg("Failed to check job tags")
		detail := fmt.Sprintf("failed to check job tags: %v", err)
		return CheckEligibilityResult{
			Reason: SkipReason{Code: ReasonDownloadedTagCheckAPIError, Detail: detail},
			Detail: detail,
		}
	}
	if done := doneTag(tags); done != "" {
		detail := fmt.Sprintf("already has '%s' tag", done)
		return CheckEligibilityResult{
			Reason: SkipReason{Code: ReasonHasDownloadedTag, Detail: detail},
			Detail: detail,
		}
	}

	// Step 1b: another client's started tag holds the job while that client
	// downloads it. This client's own holds nothing: it is this client's to
	// take off.
	self := m.state.ClientID()
	for _, c := range startedClaims(tags, job.CompletedAt) {
		if c.client != self && c.holds() {
			reason := heldReason(c)
			return CheckEligibilityResult{Reason: reason, Detail: reason.Detail}
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
		if !slices.Contains(tags, m.eligibility.AutoDownloadTag) {
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

// doneTag returns the tag among tags that says a client has downloaded the
// job, or "". The tag earlier versions applied counts, so their jobs are not
// downloaded again.
func doneTag(tags []string) string {
	for _, done := range []string{config.DownloadedTag, config.LegacyDownloadedTag} {
		if slices.Contains(tags, done) {
			return done
		}
	}
	return ""
}

// startedTag is the started tag the client puts on a job at at:
// config.StartedTag, then the client's name and the time in milliseconds.
func startedTag(client string, at time.Time) string {
	return fmt.Sprintf("%s:%s:%d", config.StartedTag, client, at.UnixMilli())
}

// startedClaim is a started tag on a job: which client put it on, and when.
type startedClaim struct {
	tag, client string
	at          time.Time
}

// startedClaims returns the started tags among tags, earliest first, ties
// going to the lesser client name so every client orders them alike. The bare
// tag earlier versions put on names neither client nor time; it cannot
// predate the job's completion, so it takes that time, unknown when that is.
func startedClaims(tags []string, completedAt time.Time) []startedClaim {
	var claims []startedClaim
	for _, tag := range tags {
		c := startedClaim{tag: tag, at: completedAt}
		if rest, ok := strings.CutPrefix(tag, config.StartedTag+":"); ok {
			client, ms, _ := strings.Cut(rest, ":")
			n, err := strconv.ParseInt(ms, 10, 64)
			if client == "" || err != nil {
				continue
			}
			c.client, c.at = client, time.UnixMilli(n)
		} else if tag != config.StartedTag {
			continue
		}
		claims = append(claims, c)
	}
	slices.SortFunc(claims, func(a, b startedClaim) int {
		return cmp.Or(a.at.Compare(b.at), strings.Compare(a.client, b.client))
	})
	return claims
}

// holds reports whether the tag holds its job now: for claimLease from its
// time. One whose time is unknown holds nothing, nor does one stamped more
// than claimAhead ahead, which no clock in step makes and which would hold
// the job past its lease.
func (c startedClaim) holds() bool {
	age := time.Since(c.at)
	return !c.at.IsZero() && age < claimLease && age > -claimAhead
}

// heldReason says why a job another client's started tag holds is skipped.
func heldReason(c startedClaim) SkipReason {
	return SkipReason{Code: ReasonHasStartedTag, Detail: fmt.Sprintf("another client is downloading it: tag '%s', which holds it until %s",
		c.tag, c.at.Add(claimLease).Format(time.RFC3339))}
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

	// FolderPath names the job's workspace folders below the
	// sharedWithWorkspace root, outermost first (e.g. ["ExampleFolder"] or
	// ["TeamA", "Sub"]), with the "Shared" root excluded. Empty for the user's
	// personal jobs. Used to mirror the folder structure into the download
	// directory; ignored when flatten_folder_structure is enabled.
	FolderPath []string
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

	// WorkspaceErr is why the workspace folders could not be listed; the
	// user's own jobs were scanned all the same.
	WorkspaceErr error
}

// jobWithPath pairs a job with its workspace-folder path relative to the
// shared root (empty for the user's personal jobs).
type jobWithPath struct {
	job        models.JobResponse
	folderPath []string
}

// maxWorkspaceFolderDepth bounds the recursive folder walk as a safety net
// against cycles or pathological trees.
const maxWorkspaceFolderDepth = 20

// collectWorkspaceJobs returns every job in the sharedWithWorkspace folder
// tree, each tagged with its path relative to the shared root (the "Shared"
// root itself is excluded from the path).
//
// It relies on the jobs/?q=folder:<id>&f=0 listing of the shared root to
// include the jobs in every (sub)folder, whoever owns them, each with the
// folder reference (folder.id) that says where it lives. So we:
//  1. build a folder-id -> relative-path map from the meta/folders tree
//     (skipping archived folders), then
//  2. query the root once and map each job to its folder's path.
//
// A job whose folder is not in the map (e.g. an archived folder) is skipped.
func (m *Monitor) collectWorkspaceJobs(ctx context.Context, creationCutoff time.Time) ([]jobWithPath, error) {
	meta, err := m.apiClient.GetMetaFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace folders: %w", err)
	}

	root := meta.SharedWithWorkspace
	if root.ID == "" {
		m.logger.Debug().Msg("No sharedWithWorkspace root; skipping workspace folder scan")
		return nil, nil
	}

	// Build folder-id -> folder path. The root maps to an empty path (jobs
	// directly in the shared root download to the download-folder root).
	// Archived folders and their descendants are omitted so their jobs are
	// skipped.
	pathByFolder := map[string][]string{root.ID: nil}
	buildFolderPaths(root.Children, nil, pathByFolder, 1)

	// One query for all jobs under the shared root.
	jobs, err := m.apiClient.ListJobsInFolder(ctx, root.ID, creationCutoff)
	if err != nil {
		return nil, fmt.Errorf("failed to list workspace jobs: %w", err)
	}

	out := make([]jobWithPath, 0, len(jobs))
	for i := range jobs {
		fid := ""
		if jobs[i].Folder != nil {
			fid = jobs[i].Folder.ID
		}
		folderPath, ok := pathByFolder[fid]
		if !ok {
			// Job's folder is archived or otherwise not in the active tree.
			m.logger.Debug().
				Str("job_id", jobs[i].ID).
				Str("folder_id", fid).
				Msg("Skipping workspace job: folder not in active tree (archived?)")
			continue
		}
		out = append(out, jobWithPath{job: jobs[i], folderPath: folderPath})
	}
	return out, nil
}

// buildFolderPaths fills pathByFolder with folder-id -> path-relative-to-shared-root
// for each folder in the meta tree, appending the folder name to its parent's
// path. Archived folders (and their descendants) are skipped.
func buildFolderPaths(folders []models.MetaFolder, parentPath []string, pathByFolder map[string][]string, depth int) {
	if depth > maxWorkspaceFolderDepth {
		return
	}
	for _, f := range folders {
		if f.IsArchived {
			continue
		}
		folderPath := append(slices.Clip(parentPath), f.Name)
		pathByFolder[f.ID] = folderPath
		buildFolderPaths(f.Children, folderPath, pathByFolder, depth+1)
	}
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

	// Pre-filter buffer: Use creation date with extra buffer to reduce API calls
	// Jobs created more than (lookback_days + 30) days ago cannot have completed within lookback window
	var creationCutoff time.Time
	if !lookbackCutoff.IsZero() {
		creationCutoff = time.Now().AddDate(0, 0, -(m.eligibility.LookbackDays + 30))
	}

	// Build the worklist: jobs in the workspace's shared folders (each tagged
	// with its path relative to the Shared root) plus the user's own jobs.
	// Workspace entries are added FIRST and take precedence on a dedupe by job
	// ID, because a job can appear in BOTH the personal listing (which has no
	// folder path) and a workspace folder; keeping the workspace entry
	// preserves its folder path so the download mirrors the folder structure.
	worklist := make([]jobWithPath, 0, len(jobs))
	seen := make(map[string]struct{}, len(jobs))
	var wsErr error
	if m.eligibility != nil && m.eligibility.IncludeWorkspaceFolders {
		var wsJobs []jobWithPath
		wsJobs, wsErr = m.collectWorkspaceJobs(ctx, creationCutoff)
		if wsErr != nil {
			// Non-fatal: log and continue with personal jobs so a folder API
			// hiccup does not stall the whole scan.
			m.logger.Warn().Err(wsErr).Msg("Failed to enumerate workspace folders; scanning personal jobs only")
		} else {
			for _, wj := range wsJobs {
				if _, dup := seen[wj.job.ID]; dup {
					continue
				}
				seen[wj.job.ID] = struct{}{}
				worklist = append(worklist, wj)
			}
		}
	}
	// Add the user's own jobs, skipping any already contributed (with their
	// folder path) by the workspace scan above.
	for i := range jobs {
		if _, dup := seen[jobs[i].ID]; dup {
			continue
		}
		seen[jobs[i].ID] = struct{}{}
		worklist = append(worklist, jobWithPath{job: jobs[i]})
	}

	// Debug-level only — verbose stats not useful in GUI
	m.logger.Debug().Int("jobs_to_scan", len(worklist)).Msg("Scanning jobs from API")

	var completed []*CompletedJob
	summary := &ScanSummary{
		TotalScanned:     len(worklist),
		SkipBuckets:      make(map[SkipReasonCode]int),
		DownloadOutcomes: make(map[string]int),
	}

	now := time.Now()
	for _, item := range worklist {
		job := item.job
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

		// A job whose download failed waits out its backoff, and once its
		// attempts are used up, waits for 'daemon retry'. Checked before the
		// completion time lookup, so a waiting job costs no API call.
		if m.state != nil && m.state.InRetryBackoff(job.ID, now) {
			summary.AddSkip(ReasonInRetryBackoff)
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
			FolderPath:  item.folderPath,
		})
	}

	// Detailed pre-eligibility stats are at DEBUG level; the canonical scan
	// summary INFO line is emitted by daemon.poll after extending the buckets
	// with per-job eligibility skips and download outcomes.
	m.logger.Debug().
		Int("total_scanned", len(worklist)).
		Int("candidates", len(completed)).
		Msg("Pre-eligibility scan complete")

	return &FindCompletedJobsResult{
		Candidates:   completed,
		TotalScanned: len(worklist),
		Summary:      summary,
		WorkspaceErr: wsErr,
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

// JobIDFileName is the marker file written inside each job's output directory
// holding the Rescale job ID. It replaces the old "_<shortID>" directory-name
// suffix so folders can be named purely after the (sanitized) job name while
// the authoritative job ID is still recoverable from disk.
const JobIDFileName = ".jobid"

// ComputeOutputDir determines the output directory for a job.
//
// When useJobName is true, the directory is named after the sanitized job name
// alone (no job-ID suffix); the job ID is recorded in a .jobid file inside the
// directory (see WriteJobIDFile). When useJobName is false, or the job has no
// name, the directory is "job_<jobID>".
//
// Collision handling: if the job-name directory already exists and belongs to a
// DIFFERENT job (its .jobid does not match, or it has none), the job ID is
// appended ("<name>_<jobID>") to keep the two jobs separate. A directory that
// does not exist, or that already belongs to this job (matching .jobid), uses
// the plain name — so a re-download of the same job reuses its folder.
//
// A folder already made for this job under a suffixed name is reused too:
// "<name>_<jobID>", and "<name>_<first six characters of the ID>", the name
// earlier versions gave every job folder, which has no .jobid. Otherwise an
// upgrade would download each of those jobs a second time. Only a real folder
// is reused, never a link, and never one whose .jobid names another job.
// When every such name is taken, "<name>_<jobID>_2", "_3" and so on follow.
func ComputeOutputDir(baseDir, jobID, jobName string, useJobName bool) string {
	if !useJobName || jobName == "" {
		return filepath.Join(baseDir, fmt.Sprintf("job_%s", jobID))
	}

	dir := filepath.Join(baseDir, sanitizeDirectoryName(jobName))
	suffixed := dir + "_" + jobID
	if holdsJob(dir, jobID, false) {
		return dir
	}
	for _, prior := range []string{suffixed, dir + "_" + jobID[:min(len(jobID), 6)]} {
		if holdsJob(prior, jobID, true) {
			return prior
		}
	}
	// Anything else at a name, a folder or not, belongs to something else. The
	// loop ends: a folder holds only so many names.
	if _, err := os.Lstat(dir); err != nil {
		return dir
	}
	next := suffixed
	for n := 2; ; n++ {
		if _, err := os.Lstat(next); err != nil || holdsJob(next, jobID, false) {
			return next
		}
		next = fmt.Sprintf("%s_%d", suffixed, n)
	}
}

// holdsJob reports whether dir is a real folder, not a link, whose .jobid
// names jobID, or, when unmarked is set, one with no .jobid at all.
func holdsJob(dir, jobID string, unmarked bool) bool {
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return false
	}
	if id, ok := readJobIDFile(dir); ok {
		return id == jobID
	}
	_, err := os.Lstat(filepath.Join(dir, JobIDFileName))
	return unmarked && os.IsNotExist(err)
}

// WriteJobIDFile writes the job ID into a .jobid marker file inside outputDir.
// This records the authoritative job ID now that the directory name no longer
// carries an ID suffix. Best-effort: returns an error the caller may log, but
// a failure should not fail the download.
//
// The marker path gets the check every download target gets, so a link or
// anything but a file there is refused and left alone, and the marker is
// replaced by a rename, never written through.
func WriteJobIDFile(outputDir, jobID string) error {
	path := filepath.Join(outputDir, JobIDFileName)
	if err := validation.ValidateDownloadTarget(path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(outputDir, JobIDFileName+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // No-op once the rename below succeeds.
	if _, err := tmp.WriteString(jobID + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readJobIDFile reads the job ID from the .jobid marker inside dir. Returns the
// trimmed ID and true on success, or ("", false) if the file is absent,
// unreadable, not a file (a link is not followed, nor a FIFO opened), or holds
// anything but a job ID.
func readJobIDFile(dir string) (string, bool) {
	path := filepath.Join(dir, JobIDFileName)
	if validation.ValidateDownloadTarget(path) != nil {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	// A job ID is a few characters; a longer file is not a marker.
	data, err := io.ReadAll(io.LimitReader(f, maxJobIDFileSize+1))
	if err != nil || len(data) > maxJobIDFileSize {
		return "", false
	}
	id := strings.TrimSpace(string(data))
	if validation.ValidateID(id) != nil {
		return "", false
	}
	return id, true
}

const maxJobIDFileSize = 64

// sanitizeDirectoryName makes a job name safe for use as a single directory
// name on both Windows and Unix. It replaces path separators, the characters
// Windows reserves (\ / : * ? " < > |) and control characters, trims leading
// and trailing dots and spaces (Windows drops trailing ones, so the folder
// would not match the name), bounds the length without splitting a character,
// and prefixes a device name, so the result passes validation.ValidateFilename.
func sanitizeDirectoryName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			// Control characters (includes \n, \r, \t) -> underscore.
			b.WriteByte('_')
		case strings.ContainsRune(`/\:*?"<>|`, r):
			// Windows-reserved filename characters (also unsafe on Unix for /).
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}

	// Dots and spaces are trimmed together, so no mix of them ("abc. .") is
	// left at either end.
	trim := func(s string) string {
		return strings.TrimFunc(s, func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
	}
	sanitized := trim(b.String())

	// Limit length, cutting at the start of a character.
	if len(sanitized) > 100 {
		n := 100
		for n > 0 && !utf8.RuneStart(sanitized[n]) {
			n--
		}
		sanitized = trim(sanitized[:n])
	}

	// Fallback if empty after sanitization.
	if sanitized == "" {
		return "unnamed_job"
	}

	// All ValidateFilename can still refuse is a Windows device name ("CON",
	// "nul.txt", "CONIN$", COM with a superscript digit). Prefix with
	// underscore to keep it recognizable.
	if validation.ValidateFilename(sanitized) != nil {
		sanitized = "_" + sanitized
	}

	return sanitized
}
