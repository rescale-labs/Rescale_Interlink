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
		job.OrgCode = sanitize.SanitizeField(getCol("orgcode"))
		job.TarSubpath = getCol("tarsubpath")

		// Semicolon-separated, since a path may legitimately contain a comma.
		// Optional, so CSVs written before file-scan mode still load.
		if filesStr := getCol("localinputfiles"); filesStr != "" {
			for _, file := range strings.Split(filesStr, ";") {
				if file = sanitize.SanitizeField(file); file != "" {
					job.LocalInputFiles = append(job.LocalInputFiles, file)
				}
			}
		}

		// Automations and InputFiles are ID lists, ";"-separated for the same
		// reason LocalInputFiles is. Optional, so CSVs written before these
		// columns existed still load.
		if autoStr := getCol("automations"); autoStr != "" {
			for _, id := range strings.Split(autoStr, ";") {
				if id = sanitize.SanitizeField(id); id != "" {
					job.Automations = append(job.Automations, id)
				}
			}
		}
		if idsStr := getCol("inputfiles"); idsStr != "" {
			for _, id := range strings.Split(idsStr, ";") {
				if id = sanitize.SanitizeField(id); id != "" {
					job.InputFiles = append(job.InputFiles, id)
				}
			}
		}

		// Inbound SSH access, kept verbatim: these go straight to the API, and a
		// public key is not ours to normalize.
		job.CIDRRule = getCol("cidrrule")
		job.PublicKey = getCol("publickey")
		if portStr := getCol("sshport"); portStr != "" {
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return nil, fmt.Errorf("row %d: invalid SSHPort: %s", i+1, portStr)
			}
			job.SSHPort = port
		}

		// User-defined license feature. Both optional, so CSVs written before
		// feature sets still load, and a blank count reads as "none".
		job.LicenseFeatureName = sanitize.SanitizeField(getCol("licensefeaturename"))
		if countStr := getCol("licensesperjob"); countStr != "" {
			count, err := strconv.Atoi(countStr)
			if err != nil {
				return nil, fmt.Errorf("row %d: invalid LicensesPerJob: %s", i+1, countStr)
			}
			job.LicensesPerJob = count
		}

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
	// Checked before the file is created, so a refusal leaves an existing CSV
	// intact rather than truncated. Each list column is one ";"-separated field
	// whose entries are sanitized on the way back in, so an entry holding a ";"
	// reloads as two entries and one holding an invisible character reloads as a
	// different value — for a path, possibly the same name as another job's,
	// whose state record it would then share. The loss is only detectable here:
	// by load time the original is gone.
	for _, job := range jobs {
		for _, column := range []struct {
			label   string
			entries []string
		}{
			{"local input file", job.LocalInputFiles},
			{"automation", job.Automations},
			{"input file id", job.InputFiles},
		} {
			for _, entry := range column.entries {
				if strings.Contains(entry, ";") || sanitize.SanitizeField(entry) != entry {
					// No advice to write JSON: nothing in the CLI or the GUI
					// writes a jobs JSON, so the only way out is the value.
					return fmt.Errorf("job %q has the %s %q, which a jobs CSV cannot carry: "+
						"the column is \";\"-separated and sanitized on load, so the value would come "+
						"back changed; rename or shorten it, or leave it out of the job",
						job.JobName, column.label, entry)
				}
			}
		}
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create jobs CSV: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	header := []string{
		"Directory", "JobName", "AnalysisCode", "AnalysisVersion", "Command",
		"CoreType", "CoresPerSlot", "WalltimeHours", "Slots", "LicenseSettings",
		"ExtraInputFileIDs", "OnDemandLicenseSeller", "ProjectID", "OrgCode", "Tags",
		"NoDecompress", "IsLowPriority", "Submit", "TarSubpath", "LocalInputFiles",
		"LicenseFeatureName", "LicensesPerJob", "Automations", "InputFiles",
		"CIDRRule", "PublicKey", "SSHPort",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

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
			job.OrgCode,
			strings.Join(job.Tags, ","),
			strconv.FormatBool(job.NoDecompress),
			strconv.FormatBool(job.IsLowPriority),
			job.SubmitMode,
			job.TarSubpath,
			strings.Join(job.LocalInputFiles, ";"),
			job.LicenseFeatureName,
			strconv.Itoa(job.LicensesPerJob),
			strings.Join(job.Automations, ";"),
			strings.Join(job.InputFiles, ";"),
			job.CIDRRule,
			job.PublicKey,
			strconv.Itoa(job.SSHPort),
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("failed to write job row: %w", err)
		}
	}

	return nil
}
