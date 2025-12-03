package config

import (
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

func TestLoadJobsCSV(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantErr bool
		wantLen int
		check   func(*testing.T, []models.JobSpec)
	}{
		{
			name:    "simple jobs",
			file:    "../../testdata/jobs/simple_jobs.csv",
			wantErr: false,
			wantLen: 2,
			check: func(t *testing.T, jobs []models.JobSpec) {
				if jobs[0].JobName != "TestJob1" {
					t.Errorf("Job 0 name = %q, want TestJob1", jobs[0].JobName)
				}
				if jobs[0].CoreType != "emerald" {
					t.Errorf("Job 0 coretype = %q, want emerald", jobs[0].CoreType)
				}
				if jobs[0].CoresPerSlot != 1 {
					t.Errorf("Job 0 coresperslot = %d, want 1", jobs[0].CoresPerSlot)
				}
				if jobs[1].SubmitMode != "yes" {
					t.Errorf("Job 1 submit = %q, want yes", jobs[1].SubmitMode)
				}
			},
		},
		{
			name:    "license jobs",
			file:    "../../testdata/jobs/license_jobs.csv",
			wantErr: false,
			wantLen: 1,
			check: func(t *testing.T, jobs []models.JobSpec) {
				if jobs[0].LicenseSettings == "" {
					t.Error("LicenseSettings should not be empty")
				}
				// Should be valid JSON
				_, err := ParseLicenseJSON(jobs[0].LicenseSettings)
				if err != nil {
					t.Errorf("ParseLicenseJSON() error = %v", err)
				}
			},
		},
		{
			name:    "non-existent file",
			file:    "nonexistent.csv",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs, err := LoadJobsCSV(tt.file)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadJobsCSV() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(jobs) != tt.wantLen {
					t.Errorf("len(jobs) = %d, want %d", len(jobs), tt.wantLen)
				}
				if tt.check != nil {
					tt.check(t, jobs)
				}
			}
		})
	}
}

func TestParseLicenseJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(*testing.T, map[string]string)
	}{
		{
			name:    "valid JSON",
			input:   `{"LM_LICENSE_FILE":"27000@server","KEY":"value"}`,
			wantErr: false,
			check: func(t *testing.T, licMap map[string]string) {
				if len(licMap) != 2 {
					t.Errorf("len(licMap) = %d, want 2", len(licMap))
				}
				if val, ok := licMap["LM_LICENSE_FILE"]; !ok || val != "27000@server" {
					t.Errorf("LM_LICENSE_FILE = %q, want 27000@server", val)
				}
			},
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true, // ParseLicenseJSON returns error for empty string
		},
		{
			name:    "invalid JSON",
			input:   `{"bad": json}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			licMap, err := ParseLicenseJSON(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseLicenseJSON() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.check != nil {
				tt.check(t, licMap)
			}
		})
	}
}

// parseJobRow is not exported, so we can't test it directly.
// We test it indirectly through LoadJobsCSV
