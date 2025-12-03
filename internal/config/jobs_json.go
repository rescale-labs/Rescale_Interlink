// Package config provides job specification loading and saving in various formats.
// The JSON functions provide a JSON alternative to CSV for job templates.
// Note: Some functions (LoadJobsJSON, SaveJobsJSON, DetectJobFileFormat, LoadJobs)
// are infrastructure for future CLI integration and may appear as "unreachable"
// in deadcode analysis. They are intentionally available.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rescale/rescale-int/internal/models"
)

// LoadJobsJSON loads job specifications from a JSON file.
// Supports both single JobSpec and array of JobSpec.
func LoadJobsJSON(path string) ([]models.JobSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read jobs JSON file: %w", err)
	}

	// Try parsing as array first
	var jobs []models.JobSpec
	if err := json.Unmarshal(data, &jobs); err == nil {
		if len(jobs) == 0 {
			return nil, fmt.Errorf("jobs JSON file contains empty array")
		}
		return jobs, nil
	}

	// Try parsing as single object
	var singleJob models.JobSpec
	if err := json.Unmarshal(data, &singleJob); err != nil {
		return nil, fmt.Errorf("failed to parse jobs JSON (expected array or single object): %w", err)
	}

	// Validate the single job has required fields
	if singleJob.JobName == "" && singleJob.AnalysisCode == "" {
		return nil, fmt.Errorf("jobs JSON appears to be empty or invalid")
	}

	return []models.JobSpec{singleJob}, nil
}

// SaveJobsJSON writes job specifications to a JSON file.
// Saves as array (even for single job) for consistency.
func SaveJobsJSON(path string, jobs []models.JobSpec) error {
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal jobs to JSON: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write jobs JSON file: %w", err)
	}

	return nil
}

// SaveJobJSON writes a single job specification to a JSON file.
// Saves as single object (not array) for single-job templates.
func SaveJobJSON(path string, job models.JobSpec) error {
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal job to JSON: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write job JSON file: %w", err)
	}

	return nil
}

// DetectJobFileFormat attempts to detect if a file is CSV or JSON based on extension.
// Returns "csv", "json", or "unknown".
func DetectJobFileFormat(path string) string {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".csv") {
		return "csv"
	}
	if strings.HasSuffix(lower, ".json") {
		return "json"
	}
	return "unknown"
}

// LoadJobs loads jobs from a file, auto-detecting format based on extension.
func LoadJobs(path string) ([]models.JobSpec, error) {
	format := DetectJobFileFormat(path)
	switch format {
	case "csv":
		return LoadJobsCSV(path)
	case "json":
		return LoadJobsJSON(path)
	default:
		return nil, fmt.Errorf("unknown file format (use .csv or .json extension): %s", path)
	}
}
