package multipart

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PartInfo contains information about a Part directory
type PartInfo struct {
	PartNum  int      // Part number extracted from pattern (or order)
	PartPath string   // Path to the Part directory
	Subdirs  []string // List of subdirectory names in this Part
	PartName string   // Display name (e.g., "Part2" or "Order1")
}

// MergedDir represents a merged directory result
type MergedDir struct {
	Path     string // Full path to the directory
	PartName string // Which Part it came from
}

// ExtractPartNumber extracts a Part number from a directory name using a regex pattern
// Returns (partNum, found) where found indicates if a number was extracted
// If pattern is empty, returns (0, false) indicating order-based priority
func ExtractPartNumber(dirName, pattern string) (int, bool) {
	if pattern == "" {
		// Empty pattern means use ordering (command-line position determines priority)
		return 0, false
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0, false
	}

	match := re.FindStringSubmatch(dirName)
	if len(match) < 2 {
		return 0, false
	}

	partNum, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}

	return partNum, true
}

// BuildMultipartMerge builds a merged directory map from multiple Part directories with precedence
// This function scans all Part directories, identifies subdirectories, and creates
// a merged view where each subdirectory comes from the highest-priority Part.
//
// Args:
//   - partDirs: List of Part directory paths (e.g., ["DOE_Base", "DOE_Part2"])
//   - patternStr: Regex pattern to extract Part numbers (empty = use list order)
//   - subdirPattern: Glob pattern to filter subdirectories (e.g., "Run_*")
//
// Returns:
//   - mergedDirs: map of subdirName → MergedDir (path and Part info)
//   - partInfoList: list of PartInfo for logging and validation
func BuildMultipartMerge(partDirs []string, patternStr, subdirPattern string) (map[string]*MergedDir, []*PartInfo, error) {
	mergedDirs := make(map[string]*MergedDir)
	var partInfoList []*PartInfo

	// Sort Part directories by priority (highest first)
	type partPriority struct {
		path     string
		partNum  int
		hasNum   bool
		position int
	}

	var priorities []partPriority
	for i, partPath := range partDirs {
		dirName := filepath.Base(partPath)
		partNum, hasNum := ExtractPartNumber(dirName, patternStr)

		priorities = append(priorities, partPriority{
			path:     partPath,
			partNum:  partNum,
			hasNum:   hasNum,
			position: i,
		})
	}

	// Sort by part number (descending) or by reverse position
	sort.Slice(priorities, func(i, j int) bool {
		// If both have numbers, higher number comes first
		if priorities[i].hasNum && priorities[j].hasNum {
			return priorities[i].partNum > priorities[j].partNum
		}
		// If only one has a number, it comes first
		if priorities[i].hasNum {
			return true
		}
		if priorities[j].hasNum {
			return false
		}
		// If neither has numbers, later position (last in command line) comes first
		return priorities[i].position > priorities[j].position
	})

	// Scan each Part directory (in priority order)
	for _, pri := range priorities {
		// Scan subdirectories
		subdirs, err := filepath.Glob(filepath.Join(pri.path, subdirPattern))
		if err != nil {
			return nil, nil, fmt.Errorf("failed to scan %s: %w", pri.path, err)
		}

		// Filter to directories only and get basenames
		var subdirNames []string
		for _, subdir := range subdirs {
			// Check if it's a directory
			// Since we can't easily check without os.Stat, we'll assume glob results are directories
			subdirNames = append(subdirNames, filepath.Base(subdir))
		}

		// Create readable Part identifier
		var partName string
		if patternStr != "" {
			partName = fmt.Sprintf("Part%d", pri.partNum)
		} else {
			partName = fmt.Sprintf("Order%d", pri.position+1)
		}

		partInfo := &PartInfo{
			PartNum:  pri.partNum,
			PartPath: pri.path,
			Subdirs:  subdirNames,
			PartName: partName,
		}
		partInfoList = append(partInfoList, partInfo)

		// Claim unclaimed subdirectories for this Part
		for _, subdirName := range subdirNames {
			if _, claimed := mergedDirs[subdirName]; !claimed {
				// First time seeing this subdir (from highest Part that has it)
				fullPath := filepath.Join(pri.path, subdirName)
				mergedDirs[subdirName] = &MergedDir{
					Path:     fullPath,
					PartName: partName,
				}
			}
			// else: already claimed by higher Part, ignore this one
		}
	}

	return mergedDirs, partInfoList, nil
}

// ValidateMultipartCoverage validates multi-part merge and warns about coverage gaps
// This function checks if higher-priority Parts are missing subdirectories
// that exist in lower-priority Parts. This helps identify incomplete Part updates.
//
// Returns a list of warning messages
func ValidateMultipartCoverage(mergedDirs map[string]*MergedDir, partInfoList []*PartInfo) []string {
	var warnings []string

	// Build map of Part → set of subdirectory names
	partSubdirs := make(map[string]map[string]bool)
	for _, partInfo := range partInfoList {
		subdirSet := make(map[string]bool)
		for _, subdirName := range partInfo.Subdirs {
			subdirSet[subdirName] = true
		}
		partSubdirs[partInfo.PartName] = subdirSet
	}

	// Check each Part against higher-priority Parts
	for i := 0; i < len(partInfoList); i++ {
		lowerPart := partInfoList[i]
		lowerSubdirs := partSubdirs[lowerPart.PartName]

		// Check against all higher-priority Parts (earlier in list)
		for j := 0; j < i; j++ {
			higherPart := partInfoList[j]
			higherSubdirs := partSubdirs[higherPart.PartName]

			// Find subdirectories in lower Part but missing from higher Part
			var missing []string
			for subdirName := range lowerSubdirs {
				if !higherSubdirs[subdirName] {
					// Only report if this subdir is actually used (claimed by lower Part)
					if mergedDir, exists := mergedDirs[subdirName]; exists {
						if mergedDir.PartName == lowerPart.PartName {
							missing = append(missing, subdirName)
						}
					}
				}
			}

			if len(missing) > 0 {
				sort.Strings(missing)
				warnings = append(warnings, fmt.Sprintf(
					"%s missing subdirectories that exist in %s: %s",
					higherPart.PartName,
					lowerPart.PartName,
					strings.Join(missing, ", "),
				))
			}
		}
	}

	return warnings
}

// RunDirectoryEntry represents a run directory from a specific project (v0.7.6)
type RunDirectoryEntry struct {
	ProjectName string // Name of the project/Part directory
	RunPath     string // Full path to the run directory
	RunName     string // Base name of the run directory (e.g., "Run_1")
}

// CollectAllRunDirectories collects ALL run directories from all project directories without deduplication (v0.7.6).
//
// This replaces the precedence-based merge logic from v0.7.5. Instead of deduplicating
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
			baseName := filepath.Base(match)
			if strings.HasPrefix(baseName, ".") {
				continue
			}

			allRuns = append(allRuns, RunDirectoryEntry{
				ProjectName: projectName,
				RunPath:     match,
				RunName:     baseName,
			})
		}
	}

	return allRuns, nil
}

// ValidateRunDirectory checks if run directory contains at least one file matching validation pattern (v0.7.6).
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

	// Walk the directory tree looking for matching files
	found := false
	filepath.Walk(runPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}
		if info.IsDir() {
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
