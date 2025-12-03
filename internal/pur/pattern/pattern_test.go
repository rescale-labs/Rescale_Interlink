package pattern

import (
	"testing"
)

func TestDetectNumericPatterns(t *testing.T) {
	tests := []struct {
		name          string
		command       string
		expectedCount int
		checkPatterns func([]PatternInfo) bool
	}{
		{
			name:          "Underscore separator",
			command:       "./run_1.sh && process_001.py",
			expectedCount: 4, // Detects both with and without extension
			checkPatterns: func(patterns []PatternInfo) bool {
				// Check that we have run_ and process_ patterns
				hasRun := false
				hasProcess := false
				for _, p := range patterns {
					if p.Prefix == "run_" && p.Number == "1" {
						hasRun = true
					}
					if p.Prefix == "process_" && p.Number == "001" && p.Padding == 3 {
						hasProcess = true
					}
				}
				return hasRun && hasProcess
			},
		},
		{
			name:          "Dash separator",
			command:       "file-01.txt data-002.csv",
			expectedCount: 4, // Detects both with and without extension
			checkPatterns: func(patterns []PatternInfo) bool {
				hasFile := false
				hasData := false
				for _, p := range patterns {
					if p.Prefix == "file-" && p.Padding == 2 {
						hasFile = true
					}
					if p.Prefix == "data-" && p.Padding == 3 {
						hasData = true
					}
				}
				return hasFile && hasData
			},
		},
		{
			name:          "No separator",
			command:       "test001 data2.txt",
			expectedCount: 2,
			checkPatterns: func(patterns []PatternInfo) bool {
				return patterns[0].Prefix == "test" && patterns[0].Padding == 3 &&
					patterns[1].Prefix == "data" && patterns[1].Padding == 1
			},
		},
		{
			name:          "Filter python3",
			command:       "python3 script.py",
			expectedCount: 0,
			checkPatterns: func(patterns []PatternInfo) bool {
				return true
			},
		},
		{
			name:          "Filter port numbers",
			command:       "server:8080 db:5432",
			expectedCount: 0,
			checkPatterns: func(patterns []PatternInfo) bool {
				return true
			},
		},
		{
			name:          "Filter year",
			command:       "data-2024.csv",
			expectedCount: 0,
			checkPatterns: func(patterns []PatternInfo) bool {
				return true
			},
		},
		{
			name:          "Filter version",
			command:       "gcc4 node18",
			expectedCount: 0,
			checkPatterns: func(patterns []PatternInfo) bool {
				return true
			},
		},
		{
			name:          "With extension",
			command:       "./run_1.sh process_001.py",
			expectedCount: 4, // Detects both with and without extension
			checkPatterns: func(patterns []PatternInfo) bool {
				hasShExtension := false
				hasPyExtension := false
				for _, p := range patterns {
					if p.Suffix == ".sh" {
						hasShExtension = true
					}
					if p.Suffix == ".py" {
						hasPyExtension = true
					}
				}
				return hasShExtension && hasPyExtension
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patterns := DetectNumericPatterns(tt.command)
			if len(patterns) != tt.expectedCount {
				t.Errorf("DetectNumericPatterns() returned %d patterns, want %d\nPatterns: %+v",
					len(patterns), tt.expectedCount, patterns)
			}
			if len(patterns) > 0 && !tt.checkPatterns(patterns) {
				t.Errorf("Pattern validation failed for: %+v", patterns)
			}
		})
	}
}

func TestIterateCommandPatterns(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		templateIdx int
		currentIdx  int
		expected    string
	}{
		{
			name:        "Preserve padding 001",
			command:     "process_001.py",
			templateIdx: 1,
			currentIdx:  10,
			expected:    "process_010.py",
		},
		{
			name:        "Preserve padding 01",
			command:     "data_01.txt",
			templateIdx: 1,
			currentIdx:  5,
			expected:    "data_05.txt",
		},
		{
			name:        "Single digit",
			command:     "run_1.sh",
			templateIdx: 1,
			currentIdx:  9,
			expected:    "run_9.sh",
		},
		{
			name:        "Multiple patterns",
			command:     "run_1.sh && data_01.txt",
			templateIdx: 1,
			currentIdx:  5,
			expected:    "run_5.sh && data_05.txt",
		},
		{
			name:        "No patterns (python3)",
			command:     "python3 script.py",
			templateIdx: 1,
			currentIdx:  5,
			expected:    "python3 script.py",
		},
		{
			name:        "Template index 5 to current 10",
			command:     "process_005.py",
			templateIdx: 5,
			currentIdx:  10,
			expected:    "process_010.py",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IterateCommandPatterns(tt.command, tt.templateIdx, tt.currentIdx)
			if result != tt.expected {
				t.Errorf("IterateCommandPatterns() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestExtractIndexFromJobName(t *testing.T) {
	tests := []struct {
		name     string
		jobName  string
		expected int
	}{
		{
			name:     "Job with underscore number",
			jobName:  "Job_1",
			expected: 1,
		},
		{
			name:     "Job with multiple digits",
			jobName:  "TestJob_123",
			expected: 123,
		},
		{
			name:     "Job with zero padding",
			jobName:  "Job_001",
			expected: 1,
		},
		{
			name:     "No number",
			jobName:  "JobName",
			expected: 1, // Default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractIndexFromJobName(tt.jobName)
			if result != tt.expected {
				t.Errorf("ExtractIndexFromJobName(%q) = %d, want %d", tt.jobName, result, tt.expected)
			}
		})
	}
}

func TestValidatePatternForIteration(t *testing.T) {
	tests := []struct {
		name    string
		pattern PatternInfo
		isValid bool
	}{
		{
			name:    "Valid pattern",
			pattern: PatternInfo{Prefix: "file_", Number: "1", Padding: 1},
			isValid: true,
		},
		{
			name:    "Year 2024 (should be filtered)",
			pattern: PatternInfo{Prefix: "data-", Number: "2024", Padding: 4},
			isValid: false,
		},
		{
			name:    "Port 8080 (should be filtered)",
			pattern: PatternInfo{Prefix: "server:", Number: "8080", Padding: 4},
			isValid: false,
		},
		{
			name:    "Version python (should be filtered)",
			pattern: PatternInfo{Prefix: "python", Number: "3", Padding: 1},
			isValid: false,
		},
		{
			name:    "Version v (should be filtered)",
			pattern: PatternInfo{Prefix: "v", Number: "2", Padding: 1},
			isValid: false,
		},
		{
			name:    "Large number with padding",
			pattern: PatternInfo{Prefix: "file_", Number: "001", Padding: 3},
			isValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validatePatternForIteration(&tt.pattern)
			if result != tt.isValid {
				t.Errorf("validatePatternForIteration() = %v, want %v for pattern: %+v",
					result, tt.isValid, tt.pattern)
			}
		})
	}
}
