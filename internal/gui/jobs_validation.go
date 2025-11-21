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

// validateCSVHeaders validates that a CSV has required headers
func validateCSVHeaders(headers []string) error {
	required := []string{
		"directory", "jobname", "analysiscode", "command",
		"coretype", "coresperslot", "walltimehours", "slots", "licensesettings",
	}

	headerMap := make(map[string]bool)
	for _, h := range headers {
		headerMap[strings.ToLower(strings.TrimSpace(h))] = true
	}

	var missing []string
	for _, req := range required {
		if !headerMap[req] {
			missing = append(missing, req)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("CSV is missing required columns: %s", strings.Join(missing, ", "))
	}

	return nil
}

// validateJobsWithAPI validates jobs against Rescale API (core types, etc.)
func (jt *JobsTab) validateJobsWithAPI(jobs []models.JobSpec) error {
	// Get cached core types
	coreTypes, isLoading, err := jt.apiCache.GetCoreTypes()

	if isLoading {
		// Can't validate right now, but allow to proceed
		return nil
	}

	if err != nil || len(coreTypes) == 0 {
		// Use defaults, don't block
		return nil
	}

	// Build map of valid core types
	validCoreTypes := make(map[string]bool)
	for _, ct := range coreTypes {
		validCoreTypes[ct.Code] = true
	}

	// Check each job
	var invalidJobs []string
	for _, job := range jobs {
		if job.CoreType != "" && !validCoreTypes[job.CoreType] {
			invalidJobs = append(invalidJobs,
				fmt.Sprintf("%s uses unknown core type '%s'", job.JobName, job.CoreType))
		}
	}

	if len(invalidJobs) > 0 {
		return fmt.Errorf("Jobs use invalid core types:\n%s\n\nThese core types may not be available.",
			strings.Join(invalidJobs, "\n"))
	}

	return nil
}
