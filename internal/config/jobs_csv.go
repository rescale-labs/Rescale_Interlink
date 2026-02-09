package config

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/util/sanitize"
)

// LoadJobsCSV loads job specifications from a CSV file
func LoadJobsCSV(path string) ([]models.JobSpec, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open jobs CSV: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to read jobs CSV: %w", err)
	}

	if len(records) < 2 {
		return nil, fmt.Errorf("jobs CSV must have at least a header row and one data row")
	}

	// Parse header
	header := records[0]
	headerMap := make(map[string]int)
	for i, col := range header {
		headerMap[strings.ToLower(strings.TrimSpace(col))] = i
	}

	// Required columns
	requiredCols := []string{"directory", "jobname", "analysiscode", "command", "coretype",
		"coresperslot", "walltimehours", "slots", "licensesettings"}
	for _, col := range requiredCols {
		if _, ok := headerMap[col]; !ok {
			return nil, fmt.Errorf("missing required column: %s", col)
		}
	}

	// Parse data rows
	var jobs []models.JobSpec
	for i := 1; i < len(records); i++ {
		record := records[i]
		if len(record) == 0 || (len(record) == 1 && strings.TrimSpace(record[0]) == "") {
			continue // Skip empty rows
		}

		job := models.JobSpec{}

		// Helper to get column value
		getCol := func(name string) string {
			if idx, ok := headerMap[name]; ok && idx < len(record) {
				return strings.TrimSpace(record[idx])
			}
			return ""
		}

		// Parse required fields (with sanitization)
		job.Directory = sanitize.SanitizeField(getCol("directory"))
		job.JobName = sanitize.SanitizeField(getCol("jobname"))
		job.AnalysisCode = sanitize.SanitizeField(getCol("analysiscode"))
		job.Command = sanitize.SanitizeCommand(getCol("command"))
		job.CoreType = sanitize.SanitizeField(getCol("coretype"))
		job.LicenseSettings = getCol("licensesettings")

		// Parse numeric fields
		if cps := getCol("coresperslot"); cps != "" {
			if v, err := strconv.Atoi(cps); err == nil {
				job.CoresPerSlot = v
			} else {
				return nil, fmt.Errorf("row %d: invalid CoresPerSlot: %s", i+1, cps)
			}
		}

		if wt := getCol("walltimehours"); wt != "" {
			if v, err := strconv.ParseFloat(wt, 64); err == nil {
				job.WalltimeHours = v
			} else {
				return nil, fmt.Errorf("row %d: invalid WalltimeHours: %s", i+1, wt)
			}
		}

		if slots := getCol("slots"); slots != "" {
			if v, err := strconv.Atoi(slots); err == nil {
				job.Slots = v
			} else {
				return nil, fmt.Errorf("row %d: invalid Slots: %s", i+1, slots)
			}
		}

		// Optional fields
		job.AnalysisVersion = getCol("analysisversion")
		job.ExtraInputFileIDs = getCol("extrainputfileids")
		job.OnDemandLicenseSeller = getCol("ondemandlicenseseller")
		job.ProjectID = sanitize.SanitizeField(getCol("projectid"))
		job.TarSubpath = getCol("tarsubpath")

		// Parse tags (comma-separated)
		if tagsStr := getCol("tags"); tagsStr != "" {
			tagParts := strings.Split(tagsStr, ",")
			for _, tag := range tagParts {
				tag = sanitize.SanitizeField(tag)
				if tag != "" {
					job.Tags = append(job.Tags, tag)
				}
			}
		}

		// Parse boolean fields
		if nd := strings.ToLower(getCol("nodecompress")); nd == "true" || nd == "yes" || nd == "1" {
			job.NoDecompress = true
		}

		if lp := strings.ToLower(getCol("islowpriority")); lp == "true" || lp == "yes" || lp == "1" {
			job.IsLowPriority = true
		}

		// Submit mode (default to "yes")
		submitMode := strings.ToLower(getCol("submit"))
		if submitMode == "" {
			submitMode = "yes"
		}
		job.SubmitMode = submitMode

		// Validate license settings JSON
		if job.LicenseSettings != "" {
			if err := validateLicenseJSON(job.LicenseSettings); err != nil {
				return nil, fmt.Errorf("row %d (%s): invalid LicenseSettings: %w", i+1, job.JobName, err)
			}
		}

		jobs = append(jobs, job)
	}

	return jobs, nil
}

// validateLicenseJSON validates that license settings is valid JSON and returns a map
func validateLicenseJSON(licenseJSON string) error {
	licenseJSON = strings.TrimSpace(licenseJSON)
	if licenseJSON == "" {
		return fmt.Errorf("LicenseSettings is required and must be valid JSON")
	}

	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(licenseJSON), &obj); err != nil {
		return fmt.Errorf("LicenseSettings must be valid JSON: %w", err)
	}

	if len(obj) == 0 {
		return fmt.Errorf("LicenseSettings must be a non-empty JSON object")
	}

	return nil
}

// ParseLicenseJSON parses license settings JSON into a map
func ParseLicenseJSON(licenseJSON string) (map[string]string, error) {
	licenseJSON = strings.TrimSpace(licenseJSON)
	if licenseJSON == "" {
		return nil, fmt.Errorf("LicenseSettings is empty")
	}

	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(licenseJSON), &obj); err != nil {
		return nil, fmt.Errorf("failed to parse LicenseSettings JSON: %w", err)
	}

	result := make(map[string]string)
	for k, v := range obj {
		if v == nil {
			result[k] = ""
		} else {
			result[k] = fmt.Sprintf("%v", v)
		}
	}

	return result, nil
}

// SaveJobsCSV writes job specifications to a CSV file
func SaveJobsCSV(path string, jobs []models.JobSpec) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create jobs CSV: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	header := []string{
		"Directory", "JobName", "AnalysisCode", "AnalysisVersion", "Command",
		"CoreType", "CoresPerSlot", "WalltimeHours", "Slots", "LicenseSettings",
		"ExtraInputFileIDs", "OnDemandLicenseSeller", "ProjectID", "Tags",
		"NoDecompress", "IsLowPriority", "Submit", "TarSubpath",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	// Write data rows
	for _, job := range jobs {
		row := []string{
			job.Directory,
			job.JobName,
			job.AnalysisCode,
			job.AnalysisVersion,
			job.Command,
			job.CoreType,
			strconv.Itoa(job.CoresPerSlot),
			strconv.FormatFloat(job.WalltimeHours, 'f', 1, 64),
			strconv.Itoa(job.Slots),
			job.LicenseSettings,
			job.ExtraInputFileIDs,
			job.OnDemandLicenseSeller,
			job.ProjectID,
			strings.Join(job.Tags, ","),
			strconv.FormatBool(job.NoDecompress),
			strconv.FormatBool(job.IsLowPriority),
			job.SubmitMode,
			job.TarSubpath,
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("failed to write job row: %w", err)
		}
	}

	return nil
}
