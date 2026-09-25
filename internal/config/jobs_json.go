package config

import (
	"encoding/json"
	"fmt"
	"os"

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
