// Package tags provides tag normalization utilities for Rescale file tagging.
// v4.7.4: Created for centralized tag handling across CLI and GUI paths.
package tags

import "strings"

// NormalizeTags normalizes a list of tags by trimming whitespace,
// removing empty strings, and deduplicating.
func NormalizeTags(raw []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, tag := range raw {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if !seen[tag] {
			seen[tag] = true
			result = append(result, tag)
		}
	}
	return result
}

// ParseCommaSeparated splits a comma-separated string into normalized tags.
func ParseCommaSeparated(input string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	return NormalizeTags(parts)
}
