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

	// PathInclude patterns match against the full relative path.
	// Supports standard glob patterns plus ** for multi-directory matching.
	// Example: []string{"run_1/*.dat", "run_*/output/*"}
	// For ** support: "**/results.dat" matches "a/b/c/results.dat"
	PathInclude []string
}

// ApplyToJobFiles filters a slice of job files based on the filter configuration.
func ApplyToJobFiles(files []models.JobFile, config Config) []models.JobFile {
	if len(config.Include) == 0 && len(config.Exclude) == 0 && len(config.Search) == 0 && len(config.PathInclude) == 0 {
		// No filters, return all files
		return files
	}

	filtered := make([]models.JobFile, 0, len(files))
	for _, file := range files {
		// First check path filter if specified
		if len(config.PathInclude) > 0 {
			// Get the file's relative path (or just name if no path)
			filePath := file.RelativePath
			if filePath == "" {
				filePath = file.Name
			}
			if !matchesPathFilter(filePath, config.PathInclude) {
				continue // Doesn't match path filter, skip
			}
		}

		// Then check other filters
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

// matchesPathFilter checks if a file path matches any of the path patterns.
// Supports glob patterns including ** for multi-directory matching.
func matchesPathFilter(filePath string, patterns []string) bool {
	// Normalize path separators to forward slash
	filePath = filepath.ToSlash(filePath)

	for _, pattern := range patterns {
		pattern = filepath.ToSlash(pattern)
		if matchPathPattern(filePath, pattern) {
			return true
		}
	}
	return false
}

// matchPathPattern matches a single path against a pattern.
// Supports standard glob patterns plus ** for recursive directory matching.
func matchPathPattern(path, pattern string) bool {
	// Handle ** patterns specially
	if strings.Contains(pattern, "**") {
		return matchDoubleStarPattern(path, pattern)
	}

	// Standard glob match
	matched, err := filepath.Match(pattern, path)
	if err != nil {
		return false
	}
	return matched
}

// matchDoubleStarPattern handles ** glob patterns for multi-directory matching.
// Examples:
//   - "**/foo.txt" matches "foo.txt", "a/foo.txt", "a/b/c/foo.txt"
//   - "run_1/**" matches "run_1/anything", "run_1/a/b/c/file.txt"
//   - "run_*/*.dat" matches "run_1/file.dat", "run_5/other.dat"
func matchDoubleStarPattern(path, pattern string) bool {
	// Case 1: Pattern starts with **/ (match any prefix)
	if strings.HasPrefix(pattern, "**/") {
		suffix := pattern[3:] // Remove "**/""
		// Try matching the suffix at any position
		// Check if path ends with this suffix (ignoring leading directories)
		if matchPathPattern(path, suffix) {
			return true
		}
		// Also check each subdirectory level
		parts := strings.Split(path, "/")
		for i := range parts {
			subPath := strings.Join(parts[i:], "/")
			if matchPathPattern(subPath, suffix) {
				return true
			}
		}
		return false
	}

	// Case 2: Pattern ends with /** (match any suffix)
	if strings.HasSuffix(pattern, "/**") {
		prefix := pattern[:len(pattern)-3] // Remove "/**"
		// Check if path starts with this prefix
		if strings.HasPrefix(path, prefix+"/") || path == prefix {
			return true
		}
		// Also try glob match on prefix
		parts := strings.Split(path, "/")
		for i := 1; i <= len(parts); i++ {
			subPath := strings.Join(parts[:i], "/")
			matched, _ := filepath.Match(prefix, subPath)
			if matched {
				return true
			}
		}
		return false
	}

	// Case 3: ** in the middle (e.g., "foo/**/bar.txt")
	// Split pattern at ** and match prefix and suffix
	doubleStar := strings.Index(pattern, "/**/")
	if doubleStar != -1 {
		prefix := pattern[:doubleStar]
		suffix := pattern[doubleStar+4:] // Skip "/**/"

		// Path must start matching prefix and end matching suffix
		// with any number of directories in between
		parts := strings.Split(path, "/")
		for i := 1; i < len(parts); i++ {
			prefixPath := strings.Join(parts[:i], "/")
			if matched, _ := filepath.Match(prefix, prefixPath); matched {
				// Prefix matches, now check suffix for remaining path
				for j := i; j <= len(parts); j++ {
					suffixPath := strings.Join(parts[j:], "/")
					if matchPathPattern(suffixPath, suffix) {
						return true
					}
				}
			}
		}
		return false
	}

	// Case 4: ** is the whole pattern (match everything)
	if pattern == "**" {
		return true
	}

	// Fallback: treat ** as * (match any single segment)
	replaced := strings.ReplaceAll(pattern, "**", "*")
	matched, _ := filepath.Match(replaced, path)
	return matched
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
