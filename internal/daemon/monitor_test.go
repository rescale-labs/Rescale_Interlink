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
