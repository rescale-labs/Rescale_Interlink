package multipart

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rescale/rescale-int/internal/localfs"
)

// RunDirectoryEntry represents a run directory from a specific project
type RunDirectoryEntry struct {
	ProjectName string // Name of the project/Part directory
	RunPath     string // Full path to the run directory
	RunName     string // Base name of the run directory (e.g., "Run_1")
}

// CollectAllRunDirectories collects ALL run directories from all project directories without deduplication.
//
// Instead of deduplicating
// runs based on priority, this function collects every run directory from every project,
// allowing duplicate run names across projects to be processed as separate jobs.
//
// Args:
//   - partDirs: List of project directory paths (e.g., ["Proj1", "Proj2"])
//   - runSubpath: Subpath to traverse before finding runs (e.g., "Simcodes/Powerflow")
//   - subdirPattern: Glob pattern to filter run directories (e.g., "Run_*")
//
// Returns:
//   - List of RunDirectoryEntry containing (project_name, run_path, run_name) tuples
//
// Example: [
//
//	{ProjectName: "Proj1", RunPath: "Proj1/Simcodes/Powerflow/Run_1", RunName: "Run_1"},
//	{ProjectName: "Proj2", RunPath: "Proj2/Simcodes/Powerflow/Run_1", RunName: "Run_1"},
//	{ProjectName: "Proj2", RunPath: "Proj2/Simcodes/Powerflow/Run_5", RunName: "Run_5"}
//
// ]
//
// Notes:
//   - Multiple projects can have same run name (e.g., Run_1 in Proj1 and Proj2)
//   - All are collected and will be processed separately with unique job names
//   - Project name extracted from directory name for job naming later
func CollectAllRunDirectories(partDirs []string, runSubpath, subdirPattern string) ([]RunDirectoryEntry, error) {
	var allRuns []RunDirectoryEntry

	for _, partPath := range partDirs {
		projectName := filepath.Base(partPath)

		// Navigate through runSubpath if specified
		scanPath := partPath
		if runSubpath != "" {
			scanPath = filepath.Join(partPath, runSubpath)
			if _, err := os.Stat(scanPath); os.IsNotExist(err) {
				// Warning logged by caller
				continue
			}
		}

		// Find run directories
		matches, err := filepath.Glob(filepath.Join(scanPath, subdirPattern))
		if err != nil {
			return nil, fmt.Errorf("failed to scan %s: %w", scanPath, err)
		}

		// Filter to directories only and skip hidden directories
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				continue
			}
			if !info.IsDir() {
				continue
			}
			if localfs.IsHidden(match) {
				continue
			}
			baseName := filepath.Base(match)

			allRuns = append(allRuns, RunDirectoryEntry{
				ProjectName: projectName,
				RunPath:     match,
				RunName:     baseName,
			})
		}
	}

	return allRuns, nil
}

// ValidateRunDirectory checks if run directory contains at least one file matching validation pattern.
//
// This function recursively searches the run directory to determine if it's a "good" run
// that should be processed. Used to filter out incomplete or invalid run directories.
//
// Args:
//   - runPath: Path to run directory to validate
//   - validationPattern: Glob pattern to check (e.g., "*.avg.fnc")
//     Empty string = always valid (no validation)
//
// Returns:
//   - true if directory contains at least one matching file, false otherwise
//
// Examples:
//
//	ValidateRunDirectory("Run_1", "*.avg.fnc")  → true if contains foo.avg.fnc
//	ValidateRunDirectory("Run_2", "*.avg.fnc")  → false if no .avg.fnc files
//	ValidateRunDirectory("Run_3", "")           → true (no validation)
func ValidateRunDirectory(runPath, validationPattern string) bool {
	// Empty pattern means no validation - always valid
	if validationPattern == "" {
		return true
	}

	// Walk the directory tree looking for matching files.
	// Uses WalkDir instead of Walk to avoid per-entry os.Stat syscalls.
	found := false
	filepath.WalkDir(runPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}
		if d.IsDir() {
			return nil // Continue into subdirectories
		}

		// Check if filename matches pattern
		matched, err := filepath.Match(validationPattern, filepath.Base(path))
		if err != nil {
			return nil // Invalid pattern, skip
		}
		if matched {
			found = true
			return filepath.SkipAll // Found a match, stop walking
		}
		return nil
	})

	return found
}
