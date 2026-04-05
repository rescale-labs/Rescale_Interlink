package glob

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ExpandPatterns expands file glob patterns, deduplicating results by absolute path.
// Non-glob patterns are included as-is (after resolving to absolute paths).
// Returns an error if a glob pattern matches no files.
func ExpandPatterns(patterns []string) ([]string, error) {
	var expandedFiles []string
	seenFiles := make(map[string]bool)

	for _, pattern := range patterns {
		hasGlob := strings.ContainsAny(pattern, "*?[]")

		if hasGlob {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid pattern '%s': %w", pattern, err)
			}

			if len(matches) == 0 {
				return nil, fmt.Errorf("no files match pattern: %s", pattern)
			}

			for _, match := range matches {
				absPath, err := filepath.Abs(match)
				if err != nil {
					return nil, fmt.Errorf("failed to get absolute path for %s: %w", match, err)
				}

				if !seenFiles[absPath] {
					expandedFiles = append(expandedFiles, absPath)
					seenFiles[absPath] = true
				}
			}
		} else {
			absPath, err := filepath.Abs(pattern)
			if err != nil {
				return nil, fmt.Errorf("failed to get absolute path for %s: %w", pattern, err)
			}

			if !seenFiles[absPath] {
				expandedFiles = append(expandedFiles, absPath)
				seenFiles[absPath] = true
			}
		}
	}

	return expandedFiles, nil
}
