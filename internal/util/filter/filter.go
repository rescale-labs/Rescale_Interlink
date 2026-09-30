// Package filter provides reusable file filtering logic.
// This package is shared across jobs, files, and folders to ensure consistency.
package filter

import (
	"path"
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
		if MatchesFilter(file.Name, config) {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

// MatchesFilter reports whether a filename passes the filter configuration:
// exclude patterns win over include patterns, and every search term must match.
// Patterns are tried against both the full name and its base name.
func MatchesFilter(filename string, config Config) bool {
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
		if MatchPathPattern(filePath, pattern) {
			return true
		}
	}
	return false
}

// MatchPathPattern reports whether a slash-separated path matches pattern.
// A "**" segment matches any number of folders, none included: "**/foo.txt"
// matches "foo.txt" and "a/b/c/foo.txt", and "run_1/**" everything under
// run_1. Every other segment matches one path segment by path.Match, as
// fs.Glob matches it, so "*" never crosses a "/", on Windows included.
//
// A segment also matches a name that is literally the same, so "case [1]/**"
// reaches a folder named "case [1]": a pattern's backslashes become separators
// on Windows, so there a bracket cannot be escaped. A trailing "/" is ignored.
func MatchPathPattern(name, pattern string) bool {
	pat, segs := strings.Split(strings.TrimRight(pattern, "/"), "/"), strings.Split(name, "/")

	// Worked from the last pattern segment back, next[j] reports whether the
	// segments after pat[i] match segs[j:]. That settles each pair of pattern
	// and path segments once; retrying every split for every "**" instead
	// multiplies the work by the path's depth for each "**".
	next := make([]bool, len(segs)+1)
	next[len(segs)] = true
	for i := len(pat) - 1; i >= 0; i-- {
		cur := make([]bool, len(segs)+1)
		for j := len(segs); j >= 0; j-- {
			switch {
			case pat[i] == "**":
				cur[j] = next[j] || j < len(segs) && cur[j+1]
			case j < len(segs) && next[j+1]:
				ok, err := path.Match(pat[i], segs[j])
				cur[j] = pat[i] == segs[j] || ok && err == nil
			}
		}
		next = cur
	}
	return next[0]
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
