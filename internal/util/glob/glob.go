package glob

import (
	"fmt"
	"io/fs"
	"os"
	"path"
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

// UnderRoot matches pattern against the contents of root and returns the
// matches as ordinary paths under it.
//
// The pattern is matched inside the root rather than joined onto it, because
// joining makes the root's own name part of the pattern: a root of "proj [v2]"
// becomes a character class, and the scan then silently reports files from a
// sibling "proj v" — files that exist, so nothing looks wrong.
//
// Matching inside the root also means the pattern can only name things under
// it, so an absolute pattern or one climbing out with ".." is rejected instead
// of quietly matching nothing.
func UnderRoot(root, pattern string) ([]string, error) {
	if root == "" {
		root = "."
	}

	// filepath.IsAbs first: a Windows "C:/x" survives fs.ValidPath, which only
	// knows about the leading slash.
	if filepath.IsAbs(pattern) {
		return nil, fmt.Errorf("%q is an absolute path, but patterns are matched inside the scan root %s",
			pattern, root)
	}

	cleaned := path.Clean(filepath.ToSlash(pattern))
	if !fs.ValidPath(cleaned) {
		return nil, fmt.Errorf("%q reaches outside the scan root %s; patterns must name files under the root",
			pattern, root)
	}

	matches, err := fs.Glob(os.DirFS(root), cleaned)
	if err != nil {
		return nil, err
	}

	joined := make([]string, 0, len(matches))
	for _, m := range matches {
		joined = append(joined, filepath.Join(root, filepath.FromSlash(m)))
	}
	return joined, nil
}
