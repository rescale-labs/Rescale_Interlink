package reporting

import (
	"fmt"
	"os"
	"strings"
)

// isCLIUsageError returns true for Cobra parse errors and local validation
// errors that represent user mistakes, not system failures.
func isCLIUsageError(msg string) bool {
	lower := strings.ToLower(msg)

	// Cobra command/flag parse errors
	if strings.HasPrefix(lower, "unknown flag") ||
		strings.HasPrefix(lower, "unknown command") ||
		strings.HasPrefix(lower, "unknown shorthand") ||
		strings.Contains(lower, "required flag") ||
		strings.HasPrefix(lower, "invalid argument") ||
		strings.HasPrefix(lower, "bad flag syntax") ||
		strings.HasPrefix(lower, "flag needs an argument") {
		return true
	}

	// Cobra arg count validation: "accepts N arg(s), received M"
	if strings.Contains(lower, "arg(s)") {
		return true
	}

	// Local path validation (stat errors from the CLI, not from remote APIs)
	if strings.HasPrefix(lower, "file not found:") ||
		strings.Contains(lower, "failed to access directory: stat") {
		return true
	}

	// User chose conflicting options or cancelled the operation themselves
	if strings.HasSuffix(lower, "upload cancelled") ||
		strings.HasSuffix(lower, "download cancelled") {
		return true
	}

	// Validation errors from commands that pre-check inputs
	if strings.Contains(lower, "no valid files to download") ||
		strings.Contains(lower, "no valid files to upload") ||
		strings.Contains(lower, "no files found") {
		return true
	}

	return false
}

// categoryFromOperation infers the error category from the CLI command path.
// operation may be a Cobra CommandPath() like "rescale-int folders upload-dir"
// or a short name like "job_download" (from daemon).
func categoryFromOperation(operation string) ErrorCategory {
	lower := strings.ToLower(operation)
	switch {
	case strings.Contains(lower, "pur"):
		return CategoryPURPipeline
	case strings.Contains(lower, "job"):
		return CategoryJobCreate
	default:
		return CategoryTransfer
	}
}

// HandleCLIError classifies an error and, if reportable, saves a report
// and prints a summary to stderr. Safe to call with any error.
// mode is "cli" or "daemon". operation is the Cobra CommandPath() or a short name.
// Returns the saved path, or "" if not reportable.
func HandleCLIError(err error, mode, operation, backend string) string {
	if err == nil {
		return ""
	}

	// CLI usage errors (typos, wrong flags, bad local paths) are user mistakes
	if isCLIUsageError(err.Error()) {
		return ""
	}

	category := categoryFromOperation(operation)

	if !IsReportable(err, category) {
		return ""
	}

	classified := Classify(err, category, operation, backend)
	if classified == nil {
		return ""
	}

	builder := NewBuilder(mode)
	report := builder.Build(classified, nil, "") // No timeline in CLI/daemon
	if report == nil {
		return ""
	}

	transport := &AutoFileTransport{}
	savedPath, saveErr := transport.Save(report)
	if saveErr != nil {
		// Don't fail the original error flow — just print a warning
		fmt.Fprintf(os.Stderr, "\nWarning: could not save error report: %v\n", saveErr)
		return ""
	}

	// Print summary + path to stderr
	fmt.Fprintf(os.Stderr, "\n%s\n", FormatTextSummary(report))
	fmt.Fprintf(os.Stderr, "\nFull report saved to: %s\n", savedPath)
	fmt.Fprintf(os.Stderr, "You can share this report with Rescale support for faster diagnosis.\n")

	return savedPath
}
