// Package filter provides reusable file filtering logic.
// This package is shared across jobs, files, and folders to ensure consistency.
package filter

import (
	"path/filepath"
	"strings"

	"github.com/rescale/rescale-int/internal/models"
)

// Config holds filter configuration.
type Config struct {
	// Include patterns (glob-style). Empty means include all.
	// Example: []string{"*.dat", "*.txt"}
	Include []string

	// Exclude patterns (glob-style). Takes precedence over Include.
	// Example: []string{"debug*", "temp*"}
	Exclude []string

	// Search terms (case-insensitive substring match).
	// File must match ALL search terms to be included.
	// Example: []string{"results", "final"}
	Search []string
}

// ApplyToJobFiles filters a slice of job files based on the filter configuration.
func ApplyToJobFiles(files []models.JobFile, config Config) []models.JobFile {
	if len(config.Include) == 0 && len(config.Exclude) == 0 && len(config.Search) == 0 {
		// No filters, return all files
		return files
	}

	filtered := make([]models.JobFile, 0, len(files))
	for _, file := range files {
		if matchesFilter(file.Name, config) {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

// matchesFilter checks if a filename matches the filter configuration.
func matchesFilter(filename string, config Config) bool {
	// 1. Check exclude patterns first (highest priority)
	for _, pattern := range config.Exclude {
		if matched, _ := filepath.Match(pattern, filename); matched {
			return false // Excluded
		}
		// Also check against base name
		if matched, _ := filepath.Match(pattern, filepath.Base(filename)); matched {
			return false // Excluded
		}
	}

	// 2. Check include patterns
	if len(config.Include) > 0 {
		included := false
		for _, pattern := range config.Include {
			if matched, _ := filepath.Match(pattern, filename); matched {
				included = true
				break
			}
			// Also check against base name
			if matched, _ := filepath.Match(pattern, filepath.Base(filename)); matched {
				included = true
				break
			}
		}
		if !included {
			return false // Not included by any pattern
		}
	}

	// 3. Check search terms (case-insensitive substring match)
	if len(config.Search) > 0 {
		lowerFilename := strings.ToLower(filename)
		for _, term := range config.Search {
			lowerTerm := strings.ToLower(term)
			if !strings.Contains(lowerFilename, lowerTerm) {
				return false // Must match ALL search terms
			}
		}
	}

	return true // Passed all filters
}

// ParsePatternList parses a comma-separated list of patterns into a slice.
// Example: "*.dat,*.txt" -> []string{"*.dat", "*.txt"}
func ParsePatternList(patternStr string) []string {
	if patternStr == "" {
		return nil
	}
	parts := strings.Split(patternStr, ",")
	patterns := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			patterns = append(patterns, trimmed)
		}
	}
	return patterns
}
