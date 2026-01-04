// Package filescan provides shared file scanning logic for PUR jobs.
// v4.0.8: Unified backend for both GUI and CLI to scan primary/secondary files.
package filescan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecondaryPattern represents a secondary file pattern for file-based scanning.
type SecondaryPattern struct {
	Pattern  string // Glob pattern, may include subpath (e.g., "*.mesh", "../meshes/*.cfg")
	Required bool   // If true, skip job when file missing; if false, warn and continue
}

// ScanOptions configures a file scan operation.
type ScanOptions struct {
	RootDir           string             // Base directory to search in
	PrimaryPattern    string             // Primary file pattern (e.g., "*.inp", "inputs/*.inp")
	SecondaryPatterns []SecondaryPattern // Secondary files to attach to each primary
}

// JobFiles represents files found for a single job.
type JobFiles struct {
	PrimaryFile  string   // Path to the primary file
	PrimaryDir   string   // Directory containing the primary file
	PrimaryBase  string   // Base name of primary file (without extension)
	InputFiles   []string // All input files (primary + resolved secondary files)
	SkipReason   string   // Non-empty if job should be skipped
	Warnings     []string // Non-fatal warnings (e.g., optional file missing)
}

// ScanResult contains the results of a file scan operation.
type ScanResult struct {
	Jobs         []JobFiles // Successfully resolved job file sets
	TotalCount   int        // Total primary files found
	MatchCount   int        // Jobs that passed all requirements
	SkippedFiles []string   // Primary files skipped (with reasons)
	Warnings     []string   // Global warnings
	Error        string     // Fatal error if scan failed
}

// ScanFiles finds primary files matching the pattern and resolves secondary files for each.
// This is the unified backend used by both GUI (job_bindings.go) and CLI (pur scan-files).
func ScanFiles(opts ScanOptions) ScanResult {
	if opts.PrimaryPattern == "" {
		return ScanResult{Error: "primary file pattern is required"}
	}

	// Build the glob pattern
	pattern := filepath.Join(opts.RootDir, opts.PrimaryPattern)

	// Find all primary files matching the pattern
	primaryFiles, err := filepath.Glob(pattern)
	if err != nil {
		return ScanResult{Error: fmt.Sprintf("invalid primary pattern: %v", err)}
	}

	if len(primaryFiles) == 0 {
		return ScanResult{
			Error: fmt.Sprintf("no files found matching pattern: %s", opts.PrimaryPattern),
		}
	}

	var jobs []JobFiles
	var skippedFiles []string
	var warnings []string

	for _, primaryFile := range primaryFiles {
		primaryDir := filepath.Dir(primaryFile)
		primaryBase := strings.TrimSuffix(filepath.Base(primaryFile), filepath.Ext(primaryFile))

		// Collect all input files for this job
		inputFiles := []string{primaryFile}
		var jobWarnings []string
		skipJob := false
		skipReason := ""

		// Process secondary patterns
		for _, secPattern := range opts.SecondaryPatterns {
			secondaryFiles, warning, skip := ResolveSecondaryPattern(
				primaryDir, primaryBase, primaryFile, secPattern,
			)

			if skip != "" {
				skipReason = fmt.Sprintf("%s: %s", filepath.Base(primaryFile), skip)
				skipJob = true
				break
			}

			if warning != "" {
				jobWarnings = append(jobWarnings, warning)
			}

			inputFiles = append(inputFiles, secondaryFiles...)
		}

		if skipJob {
			skippedFiles = append(skippedFiles, skipReason)
			continue
		}

		warnings = append(warnings, jobWarnings...)

		jobs = append(jobs, JobFiles{
			PrimaryFile: primaryFile,
			PrimaryDir:  primaryDir,
			PrimaryBase: primaryBase,
			InputFiles:  inputFiles,
			Warnings:    jobWarnings,
		})
	}

	return ScanResult{
		Jobs:         jobs,
		TotalCount:   len(primaryFiles),
		MatchCount:   len(jobs),
		SkippedFiles: skippedFiles,
		Warnings:     warnings,
	}
}

// ResolveSecondaryPattern resolves a secondary file pattern relative to the primary file.
// Returns: (matched files, warning message, skip reason)
// If skip reason is non-empty, the job should be skipped.
func ResolveSecondaryPattern(
	primaryDir, primaryBase, primaryFile string,
	pattern SecondaryPattern,
) ([]string, string, string) {
	// Determine if pattern is a wildcard or literal
	hasWildcard := strings.Contains(pattern.Pattern, "*")

	var resolvedPattern string
	if hasWildcard {
		// Replace * with primary file's base name
		resolvedPattern = strings.ReplaceAll(pattern.Pattern, "*", primaryBase)
	} else {
		// Literal pattern - use as-is
		resolvedPattern = pattern.Pattern
	}

	// Resolve path relative to primary file's directory
	fullPath := filepath.Join(primaryDir, resolvedPattern)

	// Clean the path (handles ../ etc.)
	fullPath = filepath.Clean(fullPath)

	// Check if file exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		if pattern.Required {
			return nil, "", fmt.Sprintf("required secondary file not found: %s", resolvedPattern)
		}
		// Optional file missing - warn and continue
		return nil, fmt.Sprintf("%s: optional file not found: %s", filepath.Base(primaryFile), resolvedPattern), ""
	}

	return []string{fullPath}, "", ""
}
