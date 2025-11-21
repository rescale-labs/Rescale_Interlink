package main

import (
	"encoding/json"
	"fmt"

	"github.com/rescale/rescale-int/internal/models"
)

// LicenseType represents a common license configuration type
type LicenseType struct {
	Key         string
	DisplayName string
	Placeholder string
}

// GetDefaultTemplate returns the default job template based on the reference template
func GetDefaultTemplate() models.JobSpec {
	return models.JobSpec{
		Directory:       "./Run_${index}",
		JobName:         "Run_${index}",
		AnalysisCode:    "powerflow",
		AnalysisVersion: "6-2024-hf1 Intel MPI 2021.13",
		Command:         "pf2ens -c _Moving_Belt_CSYS -v p:Cp,pt:Cp Run_${index}.avg.fnc",
		CoreType:        "calcitev2",
		CoresPerSlot:    4,
		WalltimeHours:   48.0,
		Slots:           1,
		LicenseSettings: `{"RLM_LICENSE": "123@license-server"}`,
		SubmitMode:      "create_and_submit",
		Tags:            []string{"pur_test"},
	}
}

// GetCommonLicenseTypes returns the list of common license types
func GetCommonLicenseTypes() []LicenseType {
	return []LicenseType{
		{
			Key:         "RLM_LICENSE",
			DisplayName: "RLM License",
			Placeholder: "port@license-server",
		},
		{
			Key:         "LM_LICENSE_FILE",
			DisplayName: "FlexLM License",
			Placeholder: "port@license-server.example.com",
		},
		{
			Key:         "FLEX_LICENSE",
			DisplayName: "Flex License",
			Placeholder: "port@license-server",
		},
		{
			Key:         "ANSYSLMD_LICENSE_FILE",
			DisplayName: "ANSYS License",
			Placeholder: "1055@license-server",
		},
	}
}

// GetSubmitModes returns the available submit modes
func GetSubmitModes() []string {
	return []string{
		"create_and_submit",
		"create_only",
	}
}

// BuildLicenseJSON creates a license JSON string from key-value pair
func BuildLicenseJSON(key, value string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("license key cannot be empty")
	}
	if value == "" {
		return "", fmt.Errorf("license value cannot be empty")
	}

	licenseMap := map[string]string{
		key: value,
	}

	data, err := json.Marshal(licenseMap)
	if err != nil {
		return "", fmt.Errorf("failed to create license JSON: %w", err)
	}

	return string(data), nil
}

// ParseLicenseJSON extracts the first key-value pair from license JSON
// Returns key, value, error
func ParseLicenseJSON(licenseJSON string) (string, string, error) {
	if licenseJSON == "" {
		return "", "", fmt.Errorf("license JSON is empty")
	}

	var licenseMap map[string]string
	if err := json.Unmarshal([]byte(licenseJSON), &licenseMap); err != nil {
		return "", "", fmt.Errorf("invalid license JSON: %w", err)
	}

	// Return first key-value pair
	for key, value := range licenseMap {
		return key, value, nil
	}

	return "", "", fmt.Errorf("license JSON contains no keys")
}

// ValidateNumericField validates numeric fields are within acceptable range
func ValidateNumericField(value int, fieldName string) error {
	if value < 1 {
		return fmt.Errorf("%s: must be at least 1\n\nYou entered: %d", fieldName, value)
	}
	if value > 5000 {
		return fmt.Errorf("%s: must not exceed 5000\n\nYou entered: %d", fieldName, value)
	}
	return nil
}

// ValidateWalltimeField validates walltime is within acceptable range
func ValidateWalltimeField(value float64) error {
	if value < 0.1 {
		return fmt.Errorf("Walltime Hours: must be at least 0.1 hours\n\nYou entered: %.2f", value)
	}
	if value > 5000.0 {
		return fmt.Errorf("Walltime Hours: must not exceed 5000 hours\n\nYou entered: %.2f", value)
	}
	return nil
}

// ValidateJobSpec performs basic validation on a job template
func ValidateJobSpec(spec models.JobSpec) []error {
	var errors []error

	if spec.JobName == "" {
		errors = append(errors, fmt.Errorf("Job Name: is required\n\nPlease enter a name for this job."))
	}

	if spec.AnalysisCode == "" {
		errors = append(errors, fmt.Errorf("Analysis Code: is required\n\nPlease select a software application from the dropdown."))
	}

	if spec.Command == "" {
		errors = append(errors, fmt.Errorf("Command: is required\n\nPlease enter the command to execute for this job."))
	}

	if spec.CoreType == "" {
		errors = append(errors, fmt.Errorf("Core Type: is required\n\nPlease select a hardware type from the dropdown."))
	}

	if err := ValidateNumericField(spec.CoresPerSlot, "Cores Per Slot"); err != nil {
		errors = append(errors, err)
	}

	if err := ValidateNumericField(spec.Slots, "Slots"); err != nil {
		errors = append(errors, err)
	}

	if err := ValidateWalltimeField(spec.WalltimeHours); err != nil {
		errors = append(errors, err)
	}

	if spec.LicenseSettings != "" {
		if _, _, err := ParseLicenseJSON(spec.LicenseSettings); err != nil {
			errors = append(errors, fmt.Errorf("License Settings: invalid format\n\n%w", err))
		}
	}

	return errors
}
