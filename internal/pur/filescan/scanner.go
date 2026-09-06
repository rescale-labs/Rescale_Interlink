// Package filescan provides shared file scanning logic for PUR jobs.
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
	PrimaryFile string   // Path to the primary file
	PrimaryDir  string   // Directory containing the primary file
	PrimaryBase string   // Base name of primary file (without extension)
	InputFiles  []string // All input files (primary + resolved secondary files)
	Warnings    []string // Non-fatal warnings (e.g., optional file missing)
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

	primaryFiles, err := filepath.Glob(filepath.Join(opts.RootDir, opts.PrimaryPattern))
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

		// The archive is flat (tar.CreateTarGzFromFiles), so the list has to be a
		// set of distinct names. Resolved here rather than at tar time, where the
		// same set fails the job after the run has started.
		inputFiles := []string{primaryFile}
		byName := map[string]string{filepath.Base(primaryFile): primaryFile}
		var jobWarnings []string
		skipJob := false
		skipReason := ""

		for _, secPattern := range opts.SecondaryPatterns {
			secondaryFiles, warning, skip := ResolveSecondaryPattern(
				primaryDir, primaryBase, primaryFile, secPattern,
			)

			if skip != "" {
				skipReason = fmt.Sprintf("%s: %s", displayPath(primaryDir, primaryFile), skip)
				skipJob = true
				break
			}

			if warning != "" {
				jobWarnings = append(jobWarnings, warning)
			}

			for _, secondaryFile := range secondaryFiles {
				name := filepath.Base(secondaryFile)
				existing, taken := byName[name]
				if taken && existing == secondaryFile {
					// The same file reached the set twice: a wildcard secondary
					// substitutes the primary's stem, so "*.inp" against a primary
					// of "*.inp" resolves to the primary itself. Nothing is lost.
					continue
				}
				if taken {
					skipReason = fmt.Sprintf("%s: %s and %s would both be archived as %q",
						displayPath(primaryDir, primaryFile), existing, secondaryFile, name)
					skipJob = true
					break
				}
				byName[name] = secondaryFile
				inputFiles = append(inputFiles, secondaryFile)
			}
			if skipJob {
				break
			}
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
	// A "*" stands for the primary file's stem, which is what attaches
	// "case1.mesh" to "case1.inp"; a pattern without one names a fixed file
	// every job in the scan shares.
	resolvedPattern := pattern.Pattern
	if strings.Contains(pattern.Pattern, "*") {
		resolvedPattern = strings.ReplaceAll(pattern.Pattern, "*", primaryBase)
	}

	// Relative to the primary file's folder, and cleaned, so a subpath pattern
	// such as "../meshes/*.cfg" resolves the way it reads.
	fullPath := filepath.Clean(filepath.Join(primaryDir, resolvedPattern))

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		if pattern.Required {
			return nil, "", fmt.Sprintf("required secondary file not found: %s", resolvedPattern)
		}
		return nil, fmt.Sprintf("%s: optional file not found: %s", displayPath(primaryDir, primaryFile), resolvedPattern), ""
	}

	return []string{fullPath}, "", ""
}

// displayPath names a primary file as "<parent folder>/<basename>".
//
// A bare base name is ambiguous exactly where these messages matter: the layout
// a scan pattern like "*/model.inp" exists for gives every match the same base
// name, so lines about two different files would otherwise be byte-identical —
// indistinguishable to the reader, and one React key to the GUI list rendering
// them.
func displayPath(primaryDir, primaryFile string) string {
	return filepath.Join(filepath.Base(primaryDir), filepath.Base(primaryFile))
}
