// Package daemon tests
package daemon

import (
	"testing"

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
			name:   "nil filter matches all",
			filter: nil,
			job:    models.JobResponse{Name: "Any Job Name"},
			expected: true,
		},
		{
			name:   "empty filter matches all",
			filter: &JobFilter{},
			job:    models.JobResponse{Name: "Any Job Name"},
			expected: true,
		},
		{
			name:   "name prefix match",
			filter: &JobFilter{NamePrefix: "Test"},
			job:    models.JobResponse{Name: "Test Job 1"},
			expected: true,
		},
		{
			name:   "name prefix no match",
			filter: &JobFilter{NamePrefix: "Test"},
			job:    models.JobResponse{Name: "Production Job 1"},
			expected: false,
		},
		{
			name:   "name prefix case insensitive",
			filter: &JobFilter{NamePrefix: "test"},
			job:    models.JobResponse{Name: "TEST Job 1"},
			expected: true,
		},
		{
			name:   "name contains match",
			filter: &JobFilter{NameContains: "simulation"},
			job:    models.JobResponse{Name: "CFD Simulation Run 1"},
			expected: true,
		},
		{
			name:   "name contains no match",
			filter: &JobFilter{NameContains: "simulation"},
			job:    models.JobResponse{Name: "CFD Analysis Run 1"},
			expected: false,
		},
		{
			name:   "exclude match",
			filter: &JobFilter{ExcludeNames: []string{"Debug"}},
			job:    models.JobResponse{Name: "Debug Test Run"},
			expected: false,
		},
		{
			name:   "exclude no match",
			filter: &JobFilter{ExcludeNames: []string{"Debug"}},
			job:    models.JobResponse{Name: "Production Run 1"},
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
			if result != tt.expected {
				t.Errorf("ComputeOutputDir() = %q, want %q", result, tt.expected)
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
// Eligibility Engine Tests (v4.0.0 - Auto-Download Feature)
// =============================================================================

func TestDefaultEligibilityConfig(t *testing.T) {
	cfg := DefaultEligibilityConfig()

	if cfg == nil {
		t.Fatal("DefaultEligibilityConfig() returned nil")
	}

	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"CorrectnessTag", cfg.CorrectnessTag, "isCorrect:true"},
		{"AutoDownloadField", cfg.AutoDownloadField, "Auto Download"},
		{"AutoDownloadValue", cfg.AutoDownloadValue, "Enable"},
		{"DownloadedTag", cfg.DownloadedTag, "autoDownloaded:true"},
		{"AutoDownloadPathField", cfg.AutoDownloadPathField, "Auto Download Path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.expected)
			}
		})
	}

	// Test LookbackDays default
	if cfg.LookbackDays != 7 {
		t.Errorf("LookbackDays = %d, want 7", cfg.LookbackDays)
	}
}

func TestNewMonitorWithEligibility_NilConfig(t *testing.T) {
	// When nil eligibility config is passed, should use defaults
	m := NewMonitorWithEligibility(nil, nil, nil, nil, nil)

	if m.eligibility == nil {
		t.Fatal("expected non-nil eligibility config when nil passed")
	}

	// Should have default values
	if m.eligibility.CorrectnessTag != "isCorrect:true" {
		t.Errorf("expected default CorrectnessTag, got %q", m.eligibility.CorrectnessTag)
	}
	if m.eligibility.AutoDownloadField != "Auto Download" {
		t.Errorf("expected default AutoDownloadField, got %q", m.eligibility.AutoDownloadField)
	}
}

func TestNewMonitorWithEligibility_CustomConfig(t *testing.T) {
	customCfg := &EligibilityConfig{
		CorrectnessTag:        "custom:tag",
		AutoDownloadField:     "CustomField",
		AutoDownloadValue:     "Yes",
		DownloadedTag:         "done:true",
		AutoDownloadPathField: "Custom Path",
	}

	m := NewMonitorWithEligibility(nil, nil, nil, customCfg, nil)

	if m.eligibility != customCfg {
		t.Error("expected custom config to be used")
	}
	if m.eligibility.CorrectnessTag != "custom:tag" {
		t.Errorf("expected custom CorrectnessTag, got %q", m.eligibility.CorrectnessTag)
	}
	if m.eligibility.AutoDownloadValue != "Yes" {
		t.Errorf("expected custom AutoDownloadValue, got %q", m.eligibility.AutoDownloadValue)
	}
}

func TestSetEligibility(t *testing.T) {
	m := &Monitor{}

	// Initially nil
	if m.eligibility != nil {
		t.Error("expected nil eligibility initially")
	}

	cfg := &EligibilityConfig{CorrectnessTag: "test:tag"}
	m.SetEligibility(cfg)

	if m.eligibility != cfg {
		t.Error("SetEligibility did not set the config")
	}
	if m.eligibility.CorrectnessTag != "test:tag" {
		t.Errorf("expected test:tag, got %q", m.eligibility.CorrectnessTag)
	}

	// Can set to nil
	m.SetEligibility(nil)
	if m.eligibility != nil {
		t.Error("SetEligibility(nil) did not clear config")
	}
}

func TestCheckEligibility_NilConfig(t *testing.T) {
	m := &Monitor{eligibility: nil}

	eligible, reason := m.CheckEligibility(nil, "test-job-id")

	if !eligible {
		t.Errorf("expected eligible=true for nil eligibility config, got false")
	}
	if reason != "eligibility checking disabled" {
		t.Errorf("expected 'eligibility checking disabled', got %q", reason)
	}
}

func TestGetJobDownloadPath_NilConfig(t *testing.T) {
	m := &Monitor{eligibility: nil}

	path := m.GetJobDownloadPath(nil, "test-job-id")

	if path != "" {
		t.Errorf("expected empty path for nil eligibility, got %q", path)
	}
}

func TestGetJobDownloadPath_EmptyField(t *testing.T) {
	m := &Monitor{eligibility: &EligibilityConfig{AutoDownloadPathField: ""}}

	path := m.GetJobDownloadPath(nil, "test-job-id")

	if path != "" {
		t.Errorf("expected empty path for empty AutoDownloadPathField, got %q", path)
	}
}

func TestEligibilityConfig_ZeroValue(t *testing.T) {
	// Zero-value EligibilityConfig should have empty strings
	cfg := &EligibilityConfig{}

	if cfg.CorrectnessTag != "" {
		t.Errorf("expected empty CorrectnessTag, got %q", cfg.CorrectnessTag)
	}
	if cfg.AutoDownloadField != "" {
		t.Errorf("expected empty AutoDownloadField, got %q", cfg.AutoDownloadField)
	}
	if cfg.DownloadedTag != "" {
		t.Errorf("expected empty DownloadedTag, got %q", cfg.DownloadedTag)
	}
}

func TestCompletedJob_Struct(t *testing.T) {
	cj := &CompletedJob{
		ID:      "job123",
		Name:    "Test Job",
		Status:  "Completed",
		Owner:   "user@example.com",
		Created: "2025-12-29T10:00:00Z",
	}

	if cj.ID != "job123" {
		t.Errorf("ID mismatch: got %q", cj.ID)
	}
	if cj.Name != "Test Job" {
		t.Errorf("Name mismatch: got %q", cj.Name)
	}
	if cj.Status != "Completed" {
		t.Errorf("Status mismatch: got %q", cj.Status)
	}
	if cj.Owner != "user@example.com" {
		t.Errorf("Owner mismatch: got %q", cj.Owner)
	}
	if cj.Created != "2025-12-29T10:00:00Z" {
		t.Errorf("Created mismatch: got %q", cj.Created)
	}
}

func TestNewMonitor_NoEligibility(t *testing.T) {
	// Original NewMonitor should not set eligibility
	m := NewMonitor(nil, nil, nil, nil)

	if m.eligibility != nil {
		t.Error("NewMonitor should not set eligibility config")
	}
}
