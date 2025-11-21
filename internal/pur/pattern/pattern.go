package pattern

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PatternInfo represents a detected numeric pattern in a command
type PatternInfo struct {
	FullMatch string // Complete matched string (e.g., "data_1.txt")
	Prefix    string // Part before number (e.g., "data_")
	Number    string // The numeric part (e.g., "1" or "001")
	Suffix    string // Part after number (e.g., ".txt")
	Padding   int    // Number of digits for padding (e.g., 3 for "001")
	Separator string // Separator character before number ('_', '-', or '')
	StartPos  int    // Start position in original string
	EndPos    int    // End position in original string
}

// DetectNumericPatterns detects all numeric patterns in a command string that look like iteration candidates
func DetectNumericPatterns(command string) []PatternInfo {
	var patterns []PatternInfo
	uniquePatterns := make(map[string]*PatternInfo)

	// Define regex patterns to try, in order of specificity
	// Pattern 1: word + separator + number + extension (e.g., "file_1.txt", "data-001.csv")
	re1 := regexp.MustCompile(`(\b[a-zA-Z_]+)([_-])(\d+)(\.[a-zA-Z0-9]+)`)
	for _, match := range re1.FindAllStringSubmatchIndex(command, -1) {
		fullMatch := command[match[0]:match[1]]
		prefix := command[match[2]:match[3]] + command[match[4]:match[5]]
		number := command[match[6]:match[7]]
		suffix := command[match[8]:match[9]]
		separator := command[match[4]:match[5]]

		pattern := PatternInfo{
			FullMatch: fullMatch,
			Prefix:    prefix,
			Number:    number,
			Suffix:    suffix,
			Padding:   len(number),
			Separator: separator,
			StartPos:  match[0],
			EndPos:    match[1],
		}

		if validatePatternForIteration(&pattern) {
			patternText := pattern.FullMatch
			if _, exists := uniquePatterns[patternText]; !exists {
				uniquePatterns[patternText] = &pattern
			}
			patterns = append(patterns, pattern)
		}
	}

	// Pattern 2: word + separator + number (e.g., "run_1", "test-002")
	re2 := regexp.MustCompile(`(\b[a-zA-Z_]+)([_-])(\d+)\b`)
	for _, match := range re2.FindAllStringSubmatchIndex(command, -1) {
		fullMatch := command[match[0]:match[1]]
		prefix := command[match[2]:match[3]] + command[match[4]:match[5]]
		number := command[match[6]:match[7]]
		separator := command[match[4]:match[5]]

		pattern := PatternInfo{
			FullMatch: fullMatch,
			Prefix:    prefix,
			Number:    number,
			Suffix:    "",
			Padding:   len(number),
			Separator: separator,
			StartPos:  match[0],
			EndPos:    match[1],
		}

		if validatePatternForIteration(&pattern) {
			patternText := pattern.FullMatch
			if _, exists := uniquePatterns[patternText]; !exists {
				uniquePatterns[patternText] = &pattern
			}
			patterns = append(patterns, pattern)
		}
	}

	// Pattern 3: word + number (no separator, e.g., "file1", "test001")
	re3 := regexp.MustCompile(`(\b[a-zA-Z]+)(\d+)(?:\s|$|\.)`)
	for _, match := range re3.FindAllStringSubmatchIndex(command, -1) {
		// The full match includes the lookahead character, so adjust
		endPos := match[5] // End of number group (group 2)
		if endPos > len(command) {
			endPos = len(command)
		}

		fullMatch := command[match[2]:endPos] // Prefix + number only
		prefix := command[match[2]:match[3]]
		number := command[match[4]:match[5]]

		pattern := PatternInfo{
			FullMatch: fullMatch,
			Prefix:    prefix,
			Number:    number,
			Suffix:    "",
			Padding:   len(number),
			Separator: "",
			StartPos:  match[2],
			EndPos:    endPos,
		}

		if validatePatternForIteration(&pattern) {
			patternText := pattern.FullMatch
			if _, exists := uniquePatterns[patternText]; !exists {
				uniquePatterns[patternText] = &pattern
			}
			patterns = append(patterns, pattern)
		}
	}

	return patterns
}

// validatePatternForIteration determines if a detected pattern should be iterated
// Filters out likely version numbers, ports, years, etc.
func validatePatternForIteration(pattern *PatternInfo) bool {
	numVal, err := strconv.Atoi(pattern.Number)
	if err != nil {
		return false
	}

	// Skip likely years (2000-2099)
	if numVal >= 2000 && numVal <= 2099 {
		return false
	}

	// Skip likely ports (common port ranges)
	commonPorts := map[int]bool{80: true, 443: true, 8080: true, 8081: true, 8082: true,
		3000: true, 3001: true, 5000: true, 8000: true, 8888: true, 9000: true}

	if commonPorts[numVal] || (numVal >= 1024 && numVal <= 65535 && strings.HasSuffix(pattern.Prefix, ":")) {
		return false
	}

	// Skip version-like patterns (v1, v2, etc.)
	if strings.HasSuffix(strings.ToLower(pattern.Prefix), "v") && numVal < 100 {
		return false
	}

	// Skip common commands with version numbers (python3, gcc4, etc.)
	commonVersionedCommands := []string{"python", "gcc", "node", "java", "ruby", "php"}
	for _, cmd := range commonVersionedCommands {
		if strings.ToLower(pattern.Prefix) == cmd && numVal < 20 {
			return false
		}
	}

	// Skip if the number is too large (likely not an iteration index)
	if numVal > 9999 {
		return false
	}

	// Accept patterns that look like iteration indices
	// Typically these are small numbers (0-999)
	if numVal <= 999 {
		return true
	}

	// For larger numbers, only accept if they have padding (e.g., 0001)
	return pattern.Padding > 1
}

// IterateCommandPatterns iterates all valid patterns in a command
// Args:
//   - templateCommand: The original command from template
//   - templateIndex: The index found in the template JobName (or 1 if none)
//   - targetIndex: The target index for this row
//
// Returns: Updated command with patterns replaced
func IterateCommandPatterns(templateCommand string, templateIndex, targetIndex int) string {
	if templateCommand == "" {
		return templateCommand
	}

	// Detect all patterns
	patterns := DetectNumericPatterns(templateCommand)
	if len(patterns) == 0 {
		return templateCommand
	}

	// Filter out overlapping patterns - keep only the longest match at each position
	// This prevents "data_1" from being detected when "data_1.csv" already covers it
	filteredPatterns := filterOverlappingPatterns(patterns)

	// Sort patterns by position (process from right to left to avoid offset issues)
	sort.Slice(filteredPatterns, func(i, j int) bool {
		return filteredPatterns[i].StartPos > filteredPatterns[j].StartPos
	})

	// Build result by replacing patterns from right to left
	result := []rune(templateCommand)

	for _, pattern := range filteredPatterns {
		// Create replacement with same padding
		var newNumber string
		if pattern.Padding > 1 {
			newNumber = fmt.Sprintf("%0*d", pattern.Padding, targetIndex)
		} else {
			newNumber = strconv.Itoa(targetIndex)
		}

		replacement := pattern.Prefix + newNumber + pattern.Suffix

		// Replace this specific occurrence at its position
		before := result[:pattern.StartPos]
		after := result[pattern.EndPos:]
		result = append(before, append([]rune(replacement), after...)...)
	}

	return string(result)
}

// filterOverlappingPatterns removes overlapping patterns, keeping only the longest one
func filterOverlappingPatterns(patterns []PatternInfo) []PatternInfo {
	if len(patterns) == 0 {
		return patterns
	}

	// Sort by start position, then by length (longest first)
	sorted := make([]PatternInfo, len(patterns))
	copy(sorted, patterns)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].StartPos != sorted[j].StartPos {
			return sorted[i].StartPos < sorted[j].StartPos
		}
		// Same position - prefer longer match
		return len(sorted[i].FullMatch) > len(sorted[j].FullMatch)
	})

	var filtered []PatternInfo
	lastEndPos := -1

	for _, p := range sorted {
		// Skip if this pattern overlaps with a previous one
		if p.StartPos < lastEndPos {
			continue
		}
		filtered = append(filtered, p)
		lastEndPos = p.EndPos
	}

	return filtered
}

// ExtractIndexFromJobName extracts a numeric index from a job name
// For example, "Job_10" returns 10
func ExtractIndexFromJobName(jobName string) int {
	re := regexp.MustCompile(`\d+`)
	match := re.FindString(jobName)
	if match == "" {
		return 1
	}

	index, err := strconv.Atoi(match)
	if err != nil {
		return 1
	}

	return index
}
