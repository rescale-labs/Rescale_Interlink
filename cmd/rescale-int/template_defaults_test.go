package main

import (
	"testing"

	"github.com/rescale/rescale-int/internal/models"
)

func TestGetDefaultTemplate(t *testing.T) {
	template := GetDefaultTemplate()

	if template.JobName != "Run_${index}" {
		t.Errorf("Expected JobName 'Run_${index}', got '%s'", template.JobName)
	}

	if template.AnalysisCode != "powerflow" {
		t.Errorf("Expected AnalysisCode 'powerflow', got '%s'", template.AnalysisCode)
	}

	if template.CoreType != "calcitev2" {
		t.Errorf("Expected CoreType 'calcitev2', got '%s'", template.CoreType)
	}

	if template.CoresPerSlot != 4 {
		t.Errorf("Expected CoresPerSlot 4, got %d", template.CoresPerSlot)
	}

	if template.WalltimeHours != 48.0 {
		t.Errorf("Expected WalltimeHours 48.0, got %f", template.WalltimeHours)
	}

	if template.Slots != 1 {
		t.Errorf("Expected Slots 1, got %d", template.Slots)
	}

	if template.SubmitMode != "create_and_submit" {
		t.Errorf("Expected SubmitMode 'create_and_submit', got '%s'", template.SubmitMode)
	}
}

func TestGetCommonLicenseTypes(t *testing.T) {
	types := GetCommonLicenseTypes()

	if len(types) == 0 {
		t.Fatal("Expected at least one license type")
	}

	// Check RLM_LICENSE exists
	found := false
	for _, lt := range types {
		if lt.Key == "RLM_LICENSE" {
			found = true
			if lt.DisplayName == "" {
				t.Error("RLM_LICENSE should have a display name")
			}
			if lt.Placeholder == "" {
				t.Error("RLM_LICENSE should have a placeholder")
			}
			break
		}
	}

	if !found {
		t.Error("Expected RLM_LICENSE in common license types")
	}
}

func TestGetSubmitModes(t *testing.T) {
	modes := GetSubmitModes()

	if len(modes) != 2 {
		t.Errorf("Expected 2 submit modes, got %d", len(modes))
	}

	expected := map[string]bool{
		"create_and_submit": true,
		"create_only":       true,
	}

	for _, mode := range modes {
		if !expected[mode] {
			t.Errorf("Unexpected submit mode: %s", mode)
		}
	}
}

func TestBuildLicenseJSON(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		value       string
		shouldError bool
	}{
		{"Valid", "RLM_LICENSE", "123@server", false},
		{"Empty key", "", "value", true},
		{"Empty value", "key", "", true},
		{"Both empty", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := BuildLicenseJSON(tt.key, tt.value)

			if tt.shouldError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			// Verify it's valid JSON with correct key-value
			key, value, err := ParseLicenseJSON(result)
			if err != nil {
				t.Fatalf("Generated invalid JSON: %v", err)
			}

			if key != tt.key {
				t.Errorf("Expected key '%s', got '%s'", tt.key, key)
			}

			if value != tt.value {
				t.Errorf("Expected value '%s', got '%s'", tt.value, value)
			}
		})
	}
}

func TestParseLicenseJSON(t *testing.T) {
	tests := []struct {
		name          string
		json          string
		expectedKey   string
		expectedValue string
		shouldError   bool
	}{
		{
			"Valid JSON",
			`{"RLM_LICENSE": "123@server"}`,
			"RLM_LICENSE",
			"123@server",
			false,
		},
		{
			"Empty JSON",
			"",
			"",
			"",
			true,
		},
		{
			"Invalid JSON",
			"{not valid}",
			"",
			"",
			true,
		},
		{
			"Empty object",
			"{}",
			"",
			"",
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, value, err := ParseLicenseJSON(tt.json)

			if tt.shouldError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if key != tt.expectedKey {
				t.Errorf("Expected key '%s', got '%s'", tt.expectedKey, key)
			}

			if value != tt.expectedValue {
				t.Errorf("Expected value '%s', got '%s'", tt.expectedValue, value)
			}
		})
	}
}

func TestValidateNumericField(t *testing.T) {
	tests := []struct {
		name        string
		value       int
		fieldName   string
		shouldError bool
	}{
		{"Valid minimum", 1, "TestField", false},
		{"Valid mid-range", 100, "TestField", false},
		{"Valid maximum", 5000, "TestField", false},
		{"Too small", 0, "TestField", true},
		{"Negative", -1, "TestField", true},
		{"Too large", 5001, "TestField", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNumericField(tt.value, tt.fieldName)

			if tt.shouldError && err == nil {
				t.Error("Expected error but got none")
			}

			if !tt.shouldError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestValidateWalltimeField(t *testing.T) {
	tests := []struct {
		name        string
		value       float64
		shouldError bool
	}{
		{"Valid minimum", 0.1, false},
		{"Valid mid-range", 24.5, false},
		{"Valid maximum", 5000.0, false},
		{"Too small", 0.09, true},
		{"Zero", 0.0, true},
		{"Negative", -1.0, true},
		{"Too large", 5000.1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWalltimeField(tt.value)

			if tt.shouldError && err == nil {
				t.Error("Expected error but got none")
			}

			if !tt.shouldError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestValidateJobSpec(t *testing.T) {
	// Valid spec
	validSpec := GetDefaultTemplate()
	errors := ValidateJobSpec(validSpec)
	if len(errors) != 0 {
		t.Errorf("Valid spec should have no errors, got %d: %v", len(errors), errors)
	}

	// Missing required fields
	invalidSpec := models.JobSpec{}
	errors = ValidateJobSpec(invalidSpec)
	if len(errors) == 0 {
		t.Error("Invalid spec should have errors")
	}

	// Invalid numeric fields
	invalidNumeric := GetDefaultTemplate()
	invalidNumeric.CoresPerSlot = 0
	invalidNumeric.Slots = 6000
	invalidNumeric.WalltimeHours = -1.0
	errors = ValidateJobSpec(invalidNumeric)
	if len(errors) < 3 {
		t.Errorf("Expected at least 3 errors for invalid numeric fields, got %d", len(errors))
	}

	// Invalid license JSON
	invalidLicense := GetDefaultTemplate()
	invalidLicense.LicenseSettings = "{invalid}"
	errors = ValidateJobSpec(invalidLicense)
	if len(errors) == 0 {
		t.Error("Invalid license JSON should produce error")
	}
}
