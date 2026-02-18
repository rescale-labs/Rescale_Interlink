// Package multipart provides shared directory scanning for multi-part PUR operations.
// v4.6.5: Added ScanDirectories() shared helper to unify CLI and GUI scan logic.
package multipart

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/rescale/rescale-int/internal/localfs"
)

// ScanResult holds a validated, named job entry from directory scanning.
type ScanResult struct {
	Directory   string // Absolute path to run directory
	JobName     string // Generated job name with optional project suffix
	ProjectName string // Source project name (multi-part only)
	DirNumber   int    // Extracted or sequential directory number
}

// ScanOpts controls how ScanDirectories scans for run directories.
type ScanOpts struct {
	PartDirs          []string // Multi-part: multiple project dirs. Single: nil.
	SingleDir         string   // Single-part: base directory. Multi-part: ignored.
	RunSubpath        string   // Subpath to navigate before finding runs (e.g., "Simcodes/Powerflow")
	Pattern           string   // Glob pattern for directories (e.g., "Run_*")
	ValidationPattern string   // File pattern to validate directories (e.g., "*.avg.fnc")
	BaseJobName       string   // Template job name (for name generation)
	StartIndex        int      // Starting index for sequential numbering
}

// ScanDirectories scans one or more project directories for run directories,
// applies validation, and generates unique job names.
// Used by both make-dirs-csv CLI and engine.Scan()/ScanToSpecs() GUI path.
//
// Multi-part mode (PartDirs non-empty): scans each project directory, collects
// all matching subdirectories, validates them, and generates job names with
// project suffix for uniqueness.
//
// Single-part mode (PartDirs empty): scans SingleDir for matching subdirectories,
// validates them, and generates simple job names.
func ScanDirectories(opts ScanOpts) ([]ScanResult, error) {
	if opts.Pattern == "" {
		return nil, fmt.Errorf("scan pattern is required")
	}

	type dirEntry struct {
		path        string
		projectName string
	}
	var dirEntries []dirEntry

	isMultiPart := len(opts.PartDirs) > 0

	if isMultiPart {
		// Multi-part mode: scan multiple project directories
		allRuns, err := CollectAllRunDirectories(opts.PartDirs, opts.RunSubpath, opts.Pattern)
		if err != nil {
			return nil, err
		}
		if len(allRuns) == 0 {
			return nil, fmt.Errorf("no run directories found in any project (pattern='%s')", opts.Pattern)
		}

		// Validate each run directory
		for _, run := range allRuns {
			if ValidateRunDirectory(run.RunPath, opts.ValidationPattern) {
				dirEntries = append(dirEntries, dirEntry{
					path:        run.RunPath,
					projectName: run.ProjectName,
				})
			}
		}

		if len(dirEntries) == 0 {
			return nil, fmt.Errorf("no valid run directories found (validation: %s)", opts.ValidationPattern)
		}
	} else {
		// Single-part mode: scan single directory
		scanRoot := opts.SingleDir
		if scanRoot == "" {
			var err error
			scanRoot, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("failed to get current directory: %w", err)
			}
		}

		// Navigate through run subpath if specified
		if opts.RunSubpath != "" {
			scanRoot = filepath.Join(scanRoot, opts.RunSubpath)
			if _, err := os.Stat(scanRoot); os.IsNotExist(err) {
				return nil, fmt.Errorf("subpath '%s' not found under %s", opts.RunSubpath, opts.SingleDir)
			}
		}

		// Glob for matching directories
		matches, err := filepath.Glob(filepath.Join(scanRoot, opts.Pattern))
		if err != nil {
			return nil, fmt.Errorf("failed to scan %s: %w", scanRoot, err)
		}

		// Filter to directories only, skip hidden
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil || !info.IsDir() {
				continue
			}
			if localfs.IsHidden(match) {
				continue
			}

			// Validate using recursive walk (matching old PUR's rglob behavior)
			if !ValidateRunDirectory(match, opts.ValidationPattern) {
				continue
			}

			dirEntries = append(dirEntries, dirEntry{
				path:        match,
				projectName: "",
			})
		}

		if len(dirEntries) == 0 {
			return nil, fmt.Errorf("no directories matched pattern: %s (with validation: %s)", opts.Pattern, opts.ValidationPattern)
		}
	}

	// Sort by path for deterministic output
	sort.Slice(dirEntries, func(i, j int) bool {
		return dirEntries[i].path < dirEntries[j].path
	})

	// Generate results with job names
	numRe := regexp.MustCompile(`\d+`)
	var results []ScanResult
	for i, entry := range dirEntries {
		dirNum := i + opts.StartIndex

		// Try to extract directory number from name
		baseName := filepath.Base(entry.path)
		if match := numRe.FindString(baseName); match != "" {
			if num, err := strconv.Atoi(match); err == nil {
				dirNum = num
			}
		}

		// Generate job name
		var jobName string
		if isMultiPart && entry.projectName != "" {
			jobName = fmt.Sprintf("%s_%d_%s", opts.BaseJobName, dirNum, entry.projectName)
		} else {
			jobName = fmt.Sprintf("%s_%d", opts.BaseJobName, dirNum)
		}

		results = append(results, ScanResult{
			Directory:   entry.path,
			JobName:     jobName,
			ProjectName: entry.projectName,
			DirNumber:   dirNum,
		})
	}

	return results, nil
}
