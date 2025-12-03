package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

func TestSaveAndLoadJobsJSON(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "jobs_json_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test job
	jobs := []models.JobSpec{
		{
			Directory:       "./Run_1",
			JobName:         "TestJob",
			AnalysisCode:    "user_included",
			AnalysisVersion: "1.0",
			Command:         "./run.sh",
			CoreType:        "emerald",
			CoresPerSlot:    4,
			WalltimeHours:   1.5,
			Slots:           1,
			LicenseSettings: `{"LM_LICENSE_FILE":"27000@server"}`,
			Tags:            []string{"test", "json"},
			ProjectID:       "proj123",
		},
	}

	// Test SaveJobsJSON
	jsonPath := filepath.Join(tmpDir, "test_jobs.json")
	err = SaveJobsJSON(jsonPath, jobs)
	if err != nil {
		t.Fatalf("SaveJobsJSON failed: %v", err)
	}

	// Test LoadJobsJSON
	loaded, err := LoadJobsJSON(jsonPath)
	if err != nil {
		t.Fatalf("LoadJobsJSON failed: %v", err)
	}

	if len(loaded) != 1 {
		t.Fatalf("Expected 1 job, got %d", len(loaded))
	}

	job := loaded[0]
	if job.JobName != "TestJob" {
		t.Errorf("Expected JobName 'TestJob', got '%s'", job.JobName)
	}
	if job.AnalysisCode != "user_included" {
		t.Errorf("Expected AnalysisCode 'user_included', got '%s'", job.AnalysisCode)
	}
	if job.CoreType != "emerald" {
		t.Errorf("Expected CoreType 'emerald', got '%s'", job.CoreType)
	}
	if job.CoresPerSlot != 4 {
		t.Errorf("Expected CoresPerSlot 4, got %d", job.CoresPerSlot)
	}
	if job.WalltimeHours != 1.5 {
		t.Errorf("Expected WalltimeHours 1.5, got %f", job.WalltimeHours)
	}
	if len(job.Tags) != 2 {
		t.Errorf("Expected 2 tags, got %d", len(job.Tags))
	}
}

func TestSaveAndLoadSingleJobJSON(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "jobs_json_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test job
	job := models.JobSpec{
		Directory:    "./Run_1",
		JobName:      "SingleJob",
		AnalysisCode: "openfoam",
		CoreType:     "emerald",
		CoresPerSlot: 8,
	}

	// Test SaveJobJSON (single job)
	jsonPath := filepath.Join(tmpDir, "single_job.json")
	err = SaveJobJSON(jsonPath, job)
	if err != nil {
		t.Fatalf("SaveJobJSON failed: %v", err)
	}

	// Test LoadJobsJSON can load single object
	loaded, err := LoadJobsJSON(jsonPath)
	if err != nil {
		t.Fatalf("LoadJobsJSON failed: %v", err)
	}

	if len(loaded) != 1 {
		t.Fatalf("Expected 1 job, got %d", len(loaded))
	}

	if loaded[0].JobName != "SingleJob" {
		t.Errorf("Expected JobName 'SingleJob', got '%s'", loaded[0].JobName)
	}
}

func TestDetectJobFileFormat(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"jobs.csv", "csv"},
		{"jobs.CSV", "csv"},
		{"jobs.json", "json"},
		{"jobs.JSON", "json"},
		{"jobs.txt", "unknown"},
		{"/path/to/jobs.csv", "csv"},
		{"/path/to/template.json", "json"},
	}

	for _, tt := range tests {
		result := DetectJobFileFormat(tt.path)
		if result != tt.expected {
			t.Errorf("DetectJobFileFormat(%q) = %q, want %q", tt.path, result, tt.expected)
		}
	}
}

func TestLoadJobsJSON_EmptyFile(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "jobs_json_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create empty array JSON file
	jsonPath := filepath.Join(tmpDir, "empty.json")
	if err := os.WriteFile(jsonPath, []byte("[]"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Should return error for empty array
	_, err = LoadJobsJSON(jsonPath)
	if err == nil {
		t.Error("Expected error for empty array, got nil")
	}
}
