// Package daemon tests
package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
)

func TestJobFilter_MatchesFilter(t *testing.T) {
	tests := []struct {
		name     string
		filter   *JobFilter
		job      models.JobResponse
		expected bool
	}{
		{
			name:     "nil filter matches all",
			filter:   nil,
			job:      models.JobResponse{Name: "Any Job Name"},
			expected: true,
		},
		{
			name:     "empty filter matches all",
			filter:   &JobFilter{},
			job:      models.JobResponse{Name: "Any Job Name"},
			expected: true,
		},
		{
			name:     "name prefix match",
			filter:   &JobFilter{NamePrefix: "Test"},
			job:      models.JobResponse{Name: "Test Job 1"},
			expected: true,
		},
		{
			name:     "name prefix no match",
			filter:   &JobFilter{NamePrefix: "Test"},
			job:      models.JobResponse{Name: "Production Job 1"},
			expected: false,
		},
		{
			name:     "name prefix case insensitive",
			filter:   &JobFilter{NamePrefix: "test"},
			job:      models.JobResponse{Name: "TEST Job 1"},
			expected: true,
		},
		{
			name:     "name contains match",
			filter:   &JobFilter{NameContains: "simulation"},
			job:      models.JobResponse{Name: "CFD Simulation Run 1"},
			expected: true,
		},
		{
			name:     "name contains no match",
			filter:   &JobFilter{NameContains: "simulation"},
			job:      models.JobResponse{Name: "CFD Analysis Run 1"},
			expected: false,
		},
		{
			name:     "exclude match",
			filter:   &JobFilter{ExcludeNames: []string{"Debug"}},
			job:      models.JobResponse{Name: "Debug Test Run"},
			expected: false,
		},
		{
			name:     "exclude no match",
			filter:   &JobFilter{ExcludeNames: []string{"Debug"}},
			job:      models.JobResponse{Name: "Production Run 1"},
			expected: true,
		},
		{
			name: "combined filters - all match",
			filter: &JobFilter{
				NamePrefix:   "Sim",
				NameContains: "CFD",
			},
			job:      models.JobResponse{Name: "Simulation CFD Run 1"},
			expected: true,
		},
		{
			name: "combined filters - prefix fails",
			filter: &JobFilter{
				NamePrefix:   "Sim",
				NameContains: "CFD",
			},
			job:      models.JobResponse{Name: "Test CFD Run 1"},
			expected: false,
		},
		{
			name: "combined filters - contains fails",
			filter: &JobFilter{
				NamePrefix:   "Sim",
				NameContains: "CFD",
			},
			job:      models.JobResponse{Name: "Simulation FEA Run 1"},
			expected: false,
		},
		{
			name: "combined filters with exclude",
			filter: &JobFilter{
				NamePrefix:   "Sim",
				ExcludeNames: []string{"SimDebug"},
			},
			job:      models.JobResponse{Name: "SimDebug Test"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create monitor with filter
			m := &Monitor{filter: tt.filter}
			result := m.matchesFilter(tt.job)
			if result != tt.expected {
				t.Errorf("matchesFilter() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestComputeOutputDir(t *testing.T) {
	tests := []struct {
		name       string
		baseDir    string
		jobID      string
		jobName    string
		useJobName bool
		expected   string
	}{
		{
			name:       "use job ID",
			baseDir:    "/downloads",
			jobID:      "abc123",
			jobName:    "Test Job",
			useJobName: false,
			expected:   "/downloads/job_abc123",
		},
		{
			name:       "use job name includes short ID suffix",
			baseDir:    "/downloads",
			jobID:      "abc123xyz",
			jobName:    "Test Job",
			useJobName: true,
			expected:   "/downloads/Test Job_abc123",
		},
		{
			name:       "short job ID kept as-is",
			baseDir:    "/downloads",
			jobID:      "abc",
			jobName:    "Test Job",
			useJobName: true,
			expected:   "/downloads/Test Job_abc",
		},
		{
			name:       "empty job name falls back to job ID",
			baseDir:    "/downloads",
			jobID:      "abc123",
			jobName:    "",
			useJobName: true,
			expected:   "/downloads/job_abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ComputeOutputDir(tt.baseDir, tt.jobID, tt.jobName, tt.useJobName)
			if want := filepath.FromSlash(tt.expected); result != want {
				t.Errorf("ComputeOutputDir() = %q, want %q", result, want)
			}
		})
	}
}

func TestSanitizeDirectoryName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "Test Job",
			expected: "Test Job",
		},
		{
			name:     "name with slashes",
			input:    "Test/Job\\Path",
			expected: "Test_Job_Path",
		},
		{
			name:     "name with special chars",
			input:    "Test:Job*File?Name",
			expected: "Test_Job_File_Name",
		},
		{
			name:     "name with quotes and pipes",
			input:    `Test"Job|Name`,
			expected: "Test_Job_Name",
		},
		{
			name:     "name with angle brackets",
			input:    "Test<Job>Name",
			expected: "Test_Job_Name",
		},
		{
			name:     "leading/trailing spaces",
			input:    "  Test Job  ",
			expected: "Test Job",
		},
		{
			name:     "leading/trailing dots",
			input:    "..Test Job..",
			expected: "Test Job",
		},
		{
			name:     "long name gets truncated",
			input:    "This is a very long job name that exceeds the maximum allowed length for directory names and should be truncated to a reasonable size",
			expected: "This is a very long job name that exceeds the maximum allowed length for directory names and should",
		},
		{
			name:     "empty name",
			input:    "",
			expected: "unnamed_job",
		},
		{
			name:     "only special chars",
			input:    "...",
			expected: "unnamed_job",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeDirectoryName(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeDirectoryName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// Eligibility Engine Tests
// =============================================================================

// TestMonitorEligibilityConfig pins where a monitor's eligibility config comes
// from: the defaults (autoDownload, seven days) when none is given, the
// caller's own when one is, none at all from NewMonitor, and SetEligibility.
func TestMonitorEligibilityConfig(t *testing.T) {
	defaults := EligibilityConfig{AutoDownloadTag: "autoDownload", LookbackDays: 7}
	if got := DefaultEligibilityConfig(); got == nil || *got != defaults {
		t.Errorf("DefaultEligibilityConfig() = %+v, want %+v", got, defaults)
	}
	if m := NewMonitorWithEligibility(nil, nil, nil, nil, nil); m.eligibility == nil || *m.eligibility != defaults {
		t.Errorf("NewMonitorWithEligibility(nil config) uses %+v, want the defaults", m.eligibility)
	}
	custom := &EligibilityConfig{AutoDownloadTag: "custom:tag", LookbackDays: 14}
	if m := NewMonitorWithEligibility(nil, nil, nil, custom, nil); m.eligibility != custom {
		t.Errorf("NewMonitorWithEligibility(custom) uses %+v, want the custom config", m.eligibility)
	}

	m := NewMonitor(nil, nil, nil, nil)
	if m.eligibility != nil {
		t.Errorf("NewMonitor set eligibility %+v, want none", m.eligibility)
	}
	m.SetEligibility(custom)
	if m.eligibility != custom {
		t.Error("SetEligibility did not set the config")
	}
	m.SetEligibility(nil)
	if m.eligibility != nil {
		t.Error("SetEligibility(nil) did not clear the config")
	}
}

func TestCheckEligibility_NilConfig(t *testing.T) {
	m := &Monitor{eligibility: nil}

	result := m.CheckEligibility(nil, "test-job-id")

	if !result.EligibleForDownload {
		t.Errorf("expected EligibleForDownload=true for nil eligibility config, got false")
	}
	if result.Detail != "eligibility checking disabled" {
		t.Errorf("expected 'eligibility checking disabled', got %q", result.Detail)
	}
	if result.Reason.Code != ReasonNone {
		t.Errorf("expected Reason.Code=ReasonNone for eligible result, got %q", result.Reason.Code)
	}
}

// TestSkipReasonCodeIsSilent asserts that every SkipReasonCode used by the
// daemon has a deterministic silent-vs-logged classification. If a new code
// is added and not classified in IsSilent, add it here and in the switch.
func TestSkipReasonCodeIsSilent(t *testing.T) {
	silent := map[SkipReasonCode]bool{
		ReasonNotCompleted:             true,
		ReasonAlreadyDownloadedLocal:   true,
		ReasonTooOldCreationPrefilter:  true,
		ReasonNameFilter:               true,
		ReasonAutoDownloadUnset:        true,
		ReasonAutoDownloadDisabled:     true,
		ReasonAutoDownloadUnrecognized: true,
		ReasonFieldCheckAPIError:       true,
		ReasonInRetryBackoff:           true,
		ReasonOutsideLookbackWindow:    false,
		// ReasonHasDownloadedTag is the common case on every poll, so it is
		// silent to avoid log noise; ReasonPendingTagApply is a transient
		// retry state.
		ReasonHasDownloadedTag:            true,
		ReasonPendingTagApply:             true,
		ReasonConditionalMissingTag:       false,
		ReasonDownloadedTagCheckAPIError:  false,
		ReasonConditionalTagCheckAPIError: false,
		ReasonCompletionTimeAPIError:      false,
	}
	for code, want := range silent {
		if got := code.IsSilent(); got != want {
			t.Errorf("SkipReasonCode(%q).IsSilent() = %v, want %v", code, got, want)
		}
	}
	// ReasonNone is the zero value; classify it as silent (no reason = nothing
	// to log about).
	if !ReasonNone.IsSilent() {
		t.Errorf("ReasonNone.IsSilent() = false, want true")
	}
}

// A job whose files are down but whose downloaded tag is still being applied is
// skipped before any eligibility lookup, so the poll's tag retry is not raced
// by a second download of the same job.
func TestFindCompletedJobsSkipsJobsPendingTheirTag(t *testing.T) {
	var mu sync.Mutex
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requested = append(requested, r.URL.Path)
		mu.Unlock()
		var body any
		switch r.URL.Path {
		case "/api/v3/jobs/":
			body = map[string]any{"results": []models.JobResponse{
				{ID: "pending-job", Name: "pending", JobStatus: models.JobStatusContent{Status: "Completed"}},
				{ID: "ready-job", Name: "ready", JobStatus: models.JobStatusContent{Status: "Completed"}},
			}}
		case "/api/v3/jobs/ready-job/statuses/":
			body = map[string]any{"results": []models.JobStatusEntry{{Status: "Completed", StatusDate: time.Now().UTC().Format(time.RFC3339)}}}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	client := api.NewClientForTest(&config.Config{APIKey: "test-key", APIBaseURL: server.URL, ProxyMode: "no-proxy"})
	m := NewMonitorWithEligibility(client, nil, nil, DefaultEligibilityConfig(), logging.NewLoggerWithWriter(io.Discard))

	result, err := m.FindCompletedJobs(context.Background(), map[string]struct{}{"pending-job": {}})
	if err != nil {
		t.Fatalf("FindCompletedJobs: %v", err)
	}
	var candidates []string
	for _, job := range result.Candidates {
		candidates = append(candidates, job.ID)
	}
	if !slices.Equal(candidates, []string{"ready-job"}) {
		t.Errorf("candidates = %v, want only ready-job", candidates)
	}
	if n := result.Summary.SkipBuckets[ReasonPendingTagApply]; n != 1 {
		t.Errorf("skipped %d jobs as pending their tag, want 1 (buckets %v)", n, result.Summary.SkipBuckets)
	}
	mu.Lock()
	defer mu.Unlock()
	// The ready job's completion time was looked up, so the pending one would have been.
	if !slices.Contains(requested, "/api/v3/jobs/ready-job/statuses/") || slices.ContainsFunc(requested, func(p string) bool { return strings.Contains(p, "pending-job") }) {
		t.Errorf("requests %v: want ready-job's completion time looked up and nothing asked about pending-job", requested)
	}
}
