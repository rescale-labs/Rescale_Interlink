package glob

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/util/filter"
	"github.com/rescale/rescale-int/internal/validation"
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
	return underRoot(root, pattern, fs.Glob)
}

// FilesUnderRoot is UnderRoot for the primary pattern of a file scan, with two
// differences. Each part of the pattern also matches a name that is literally
// the same, as in filter.MatchPathPattern, even a part that is not a valid
// glob, so a malformed pattern is reported only when it matches nothing. And
// "**", which fs.Glob reads as a plain "*", matches any number of folders.
//
// Without "**" a pattern keeps UnderRoot's fixed depth and reach. With it the
// root is walked instead. The walk never returns a folder or a link to one,
// and searches neither hidden folders, where PUR stages its own archives, nor
// linked folders, which the Folders-mode walk does not follow either. Any
// other match that is not a regular file is left, as UnderRoot leaves it, for
// the caller to report.
//
// Either way the results come in UnderRoot's order, compared a part at a time
// (m/z.dat before m.dat, job1 before job10), which is the same on every
// platform: a scan numbers its jobs in this order.
func FilesUnderRoot(root, pattern string) ([]string, error) {
	find := globLevels
	if strings.Contains(pattern, "**") {
		find = walkFiles
	}
	return underRoot(root, pattern, func(fsys fs.FS, cleaned string) ([]string, error) {
		found := find(fsys, cleaned)
		if len(found) == 0 {
			for _, part := range strings.Split(cleaned, "/") {
				if _, err := path.Match(part, ""); err != nil {
					return nil, err // checked part by part, as the parts are matched
				}
			}
		}
		return found, nil
	})
}

func underRoot(root, pattern string, match func(fs.FS, string) ([]string, error)) ([]string, error) {
	if root == "" {
		root = "."
	}

	// filepath.IsAbs first: a Windows "C:/x" survives fs.ValidPath, which only
	// knows about the leading slash.
	if filepath.IsAbs(pattern) {
		return nil, fmt.Errorf("%s is an absolute path, but patterns are matched inside the scan root %s",
			validation.Quote(pattern), root)
	}

	cleaned := path.Clean(filepath.ToSlash(pattern))
	if !fs.ValidPath(cleaned) {
		return nil, fmt.Errorf("%s reaches outside the scan root %s; patterns must name files under the root",
			validation.Quote(pattern), root)
	}

	matches, err := match(os.DirFS(root), cleaned)
	if err != nil {
		return nil, err
	}

	joined := make([]string, 0, len(matches))
	for _, m := range matches {
		joined = append(joined, filepath.Join(root, filepath.FromSlash(m)))
	}
	return joined, nil
}

// globLevels matches a pattern without "**" one level at a time, as fs.Glob
// does, so it reaches what UnderRoot reaches: a linked folder is followed to
// the next level, the parts before the first wildcard are looked up however
// the file system treats their case, and an unreadable folder is passed over.
func globLevels(fsys fs.FS, pattern string) []string {
	if !strings.ContainsAny(pattern, `*?[\`) {
		if _, err := fs.Stat(fsys, pattern); err != nil {
			return nil
		}
		return []string{pattern}
	}
	dirs := []string{"."}
	dir, file := path.Split(pattern)
	if dir != "" {
		dirs = globLevels(fsys, strings.TrimSuffix(dir, "/"))
	}
	var matches []string
	for _, d := range dirs {
		entries, _ := fs.ReadDir(fsys, d)
		for _, e := range entries {
			if filter.MatchPathPattern(e.Name(), file) {
				matches = append(matches, path.Join(d, e.Name()))
			}
		}
	}
	return matches
}

// walkFiles returns what pattern matches in fsys, folders aside. fs.WalkDir
// reads each folder in lexical order, so the matches already come compared a
// part at a time.
func walkFiles(fsys fs.FS, pattern string) []string {
	var matches []string
	// The callback passes over every error, so the walk itself returns none.
	_ = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil // an unreadable folder is passed over, as fs.Glob passes it over
		case d.IsDir():
			// The root arrives named ".", which is not hidden, so a hidden
			// root is still searched.
			if localfs.IsHiddenName(d.Name()) {
				return fs.SkipDir
			}
		case filter.MatchPathPattern(name, pattern):
			// WalkDir does not follow links, so a linked folder arrives here.
			if d.Type()&fs.ModeSymlink != 0 {
				if info, err := fs.Stat(fsys, name); err == nil && info.IsDir() {
					return nil
				}
			}
			matches = append(matches, name)
		}
		return nil
	})
	return matches
}
