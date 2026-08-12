package reporting

import (
	"fmt"
	"os"
	"regexp"
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

	// Commands that check their own required flags, e.g. "--job-id (or --id) is
	// required" or "at least one --fileid is required". A missing flag is a
	// mistake at the keyboard, not something to file a diagnostic report about.
	//
	// Anchored on the flag marker: "is required" on its own also matches internal
	// invariants ("storageInfo is required", "apiClient is required") and API
	// error bodies echoing a field name, which are exactly the failures a report
	// is for.
	if strings.Contains(lower, "is required") && strings.Contains(msg, "--") {
		return true
	}

	// Prompts that cannot run without a terminal name the flag to use instead —
	// the user has everything they need.
	if strings.Contains(lower, "no interactive terminal") ||
		strings.Contains(lower, "requires an interactive terminal") ||
		strings.Contains(lower, "stdin is not a terminal") {
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
		strings.HasSuffix(lower, "download cancelled") ||
		strings.HasSuffix(lower, "aborted by user") {
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

// reJobRollup matches the pipeline's roll-up, "N of M job(s) failed", however it
// is wrapped ("pipeline failed: 2 of 10 job(s) failed").
var reJobRollup = regexp.MustCompile(`\d+ of \d+ job\(s\) failed`)

// isAggregateFailure matches the summary a batch command returns when it has
// already reported each failure item by item, so the roll-up itself carries no
// diagnostic detail worth a report.
//
// Matched narrowly, on the exact shapes those commands produce. A loose match on
// "file(s) failed" also catches files upload's primary error ("upload failed:
// 3 file(s) failed (first error: ...)"), which is the only thing that error says
// — suppressing it would mean two failures report nothing while one failure
// reports normally.
func isAggregateFailure(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.HasSuffix(lower, "file(s) failed to upload") ||
		strings.HasSuffix(lower, "deletion(s) failed") ||
		strings.HasSuffix(lower, "some files failed to download") ||
		reJobRollup.MatchString(lower)
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

	if isAggregateFailure(err.Error()) {
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

	// The diagnostic summary is the part the user asked for by hitting an error,
	// so print it first and unconditionally. Whether a report file could also be
	// written is a detail about a file they never requested: worth knowing when
	// troubleshooting, not worth a warning that competes with the real failure.
	fmt.Fprintf(os.Stderr, "\n%s\n", FormatTextSummary(report))

	transport := &AutoFileTransport{}
	savedPath, saveErr := transport.Save(report)
	if saveErr != nil {
		if os.Getenv("RESCALE_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "(debug) could not save the error report: %v\n", saveErr)
		}
		return ""
	}

	fmt.Fprintf(os.Stderr, "\nFull report saved to: %s\n", savedPath)
	fmt.Fprintf(os.Stderr, "You can share this report with Rescale support for faster diagnosis.\n")

	return savedPath
}
