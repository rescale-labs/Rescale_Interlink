// Package filescan provides shared file scanning logic for PUR jobs.
package filescan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rescale/rescale-int/internal/util/glob"
)

// SecondaryPattern represents a secondary file pattern for file-based scanning.
type SecondaryPattern struct {
	// Pattern is resolved per primary file rather than globbed: a "*" is
	// replaced by that file's stem and the result is stat'ed. It may include a
	// subpath (e.g., "*.mesh", "../meshes/*.cfg").
	Pattern string

	// Required, when true, skips the job if the file is missing; when false it
	// warns and continues.
	Required bool
}

// ScanOptions configures a file scan operation.
type ScanOptions struct {
	RootDir           string             // Base directory to search in
	PrimaryPattern    string             // Primary file pattern (e.g., "*.inp", "inputs/*.inp", "**/*.inp")
	SecondaryPatterns []SecondaryPattern // Secondary files to attach to each primary
	Recursive         bool               // Search subfolders too: a pattern without "**" is matched as "**/<pattern>"
}

// JobFiles represents files found for a single job.
type JobFiles struct {
	PrimaryFile string   // Path to the primary file
	PrimaryRel  string   // PrimaryFile relative to the scan root, which is how messages name it
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

	// Absolute, so the jobs built from this scan, and a CSV saved from them,
	// name the same files whichever folder they are later run from.
	root, err := filepath.Abs(opts.RootDir)
	if err != nil {
		return ScanResult{Error: fmt.Sprintf("cannot resolve scan root: %v", err)}
	}

	// Recursive puts "**/" in front, so one walk serves both ways of asking. A
	// pattern the root cannot hold is left for FilesUnderRoot to refuse: cleaning
	// "**/../x" would leave "x", a different pattern.
	pattern := opts.PrimaryPattern
	if cleaned := path.Clean(filepath.ToSlash(pattern)); opts.Recursive && !strings.Contains(pattern, "**") &&
		!filepath.IsAbs(pattern) && fs.ValidPath(cleaned) && cleaned != "." {
		pattern = "**/" + cleaned
	}

	primaryFiles, err := glob.FilesUnderRoot(root, pattern)
	if err != nil {
		return ScanResult{Error: fmt.Sprintf("invalid primary pattern: %v", err)}
	}

	if len(primaryFiles) == 0 {
		msg := fmt.Sprintf("no files found matching pattern: %s", pattern)
		// A bare "*.xml" reads as "every .xml file", but it searches the root
		// folder alone.
		if !strings.Contains(pattern, "**") && !strings.Contains(filepath.ToSlash(pattern), "/") {
			msg += "; to search subfolders too, use **/" + pattern
		} else if opts.Recursive {
			msg += "; a recursive scan does not go into hidden or linked folders"
		}
		return ScanResult{Error: msg}
	}

	var jobs []JobFiles
	var skippedFiles []string
	var warnings []string

	for _, primaryFile := range primaryFiles {
		display := displayPath(root, primaryFile)
		primaryDir := filepath.Dir(primaryFile)
		primaryBase := strings.TrimSuffix(filepath.Base(primaryFile), filepath.Ext(primaryFile))

		// A glob matches directories as readily as files, and "model.inp/" is
		// not something the archive can carry: attaching it failed the job at
		// tar time, after the run had started. So did a link to a missing file.
		if info, err := os.Stat(primaryFile); err != nil || !info.Mode().IsRegular() {
			skippedFiles = append(skippedFiles, fmt.Sprintf("%s: %s", display, notRegularReason(info, err)))
			continue
		}

		// The archive is flat (tar.CreateTarGzFromFiles), so the list has to be a
		// set of distinct names. Resolved here rather than at tar time, where the
		// same set fails the job after the run has started.
		inputFiles := []string{primaryFile}
		byName := map[string]string{filepath.Base(primaryFile): primaryFile}
		var jobWarnings []string
		skipJob := false
		skipReason := ""

		for _, secPattern := range opts.SecondaryPatterns {
			secondaryFiles, warning, skip := ResolveSecondaryPattern(primaryDir, primaryBase, secPattern)

			if skip != "" {
				skipReason = fmt.Sprintf("%s: %s", display, skip)
				skipJob = true
				break
			}

			if warning != "" {
				jobWarnings = append(jobWarnings, fmt.Sprintf("%s: %s", display, warning))
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
						display, existing, secondaryFile, name)
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
			PrimaryRel:  display,
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
// Returns: (matched files, warning message, skip reason), for the caller to
// put the primary file's name in front of.
// If skip reason is non-empty, the job should be skipped.
func ResolveSecondaryPattern(primaryDir, primaryBase string, pattern SecondaryPattern) ([]string, string, string) {
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

	info, err := os.Stat(fullPath)
	if os.IsNotExist(err) {
		if pattern.Required {
			return nil, "", fmt.Sprintf("required secondary file not found: %s", resolvedPattern)
		}
		return nil, fmt.Sprintf("optional file not found: %s", resolvedPattern), ""
	}

	// Existing but not a file: the same tar-time failure a directory primary
	// causes, so it is caught here for the same reason.
	if err == nil && !info.Mode().IsRegular() {
		if pattern.Required {
			return nil, "", fmt.Sprintf("required secondary file %s %s", resolvedPattern, notRegularReason(info, nil))
		}
		return nil, fmt.Sprintf("optional file %s %s", resolvedPattern, notRegularReason(info, nil)), ""
	}

	return []string{fullPath}, "", ""
}

// notRegularReason says why a matched path cannot be archived, naming the case
// that actually happens rather than leaving the user to work out what a
// non-regular file is. err is from os.Stat, which is where a link to a missing
// file fails.
func notRegularReason(info os.FileInfo, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("cannot be read: %v", errors.Unwrap(err)) // the cause, without the path again
	case info.IsDir():
		return "is a directory, not a file"
	}
	return "is not a regular file"
}

// displayPath names a primary file by its path from the scan root.
//
// Anything shorter is ambiguous exactly where these messages matter: the
// layouts a pattern like "*/model.inp" or "**/vasprun.xml" exists for give
// many matches the same base name, and often the same folder name too, so
// lines about two different files would otherwise be byte-identical:
// indistinguishable to the reader, and one React key to the GUI list
// rendering them.
func displayPath(root, primaryFile string) string {
	if rel, err := filepath.Rel(root, primaryFile); err == nil {
		return rel
	}
	return primaryFile
}
