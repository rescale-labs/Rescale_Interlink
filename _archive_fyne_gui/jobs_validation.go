package gui

import (
	"fmt"
	"strings"

	"github.com/rescale/rescale-int/internal/models"
)

// validateAllJobs validates a list of job specs
func (jt *JobsTab) validateAllJobs(jobs []models.JobSpec) error {
	if len(jobs) == 0 {
		return fmt.Errorf("No jobs found in CSV file")
	}

	var allErrors []string
	hasPlaceholders := false

	for i, job := range jobs {
		// Check for template placeholders
		if strings.Contains(job.Directory, "${") || strings.Contains(job.JobName, "${") {
			hasPlaceholders = true
			allErrors = append(allErrors, fmt.Sprintf("Job %d contains template placeholders (${...})", i+1))
		}

		errors := ValidateJobSpec(job)
		if len(errors) > 0 {
			for _, err := range errors {
				allErrors = append(allErrors, fmt.Sprintf("Job %d (%s): %v", i+1, job.JobName, err))
			}
		}
	}

	if len(allErrors) > 0 {
		errorMsg := "Validation failed:\n\n" + strings.Join(allErrors, "\n")

		if hasPlaceholders {
			errorMsg += "\n\n⚠️ This appears to be a TEMPLATE CSV, not a complete jobs CSV.\n\n" +
				"To use a template:\n" +
				"1. Go back and choose 'Create New' (not 'Load Existing')\n" +
				"2. Load or create your template\n" +
				"3. Use 'Scan Directories' to generate actual jobs\n\n" +
				"Or manually edit the CSV to replace placeholders like ${index} with actual values."
		} else {
			errorMsg += "\n\nPlease fix these issues in the CSV file."
		}

		return fmt.Errorf("%s", errorMsg)
	}

	return nil
}
