package reporting

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rescale/rescale-int/internal/events"
)

// Regex patterns for redaction
var (
	// Hex tokens longer than 20 chars (API keys, SAS tokens, etc.)
	hexTokenRe = regexp.MustCompile(`[0-9a-fA-F]{20,}`)
	// URL query parameters (contains sensitive tokens)
	urlQueryRe = regexp.MustCompile(`\?[^\s"']+`)
	// Email addresses
	emailRe = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	// Bearer/authorization tokens
	bearerRe = regexp.MustCompile(`(?i)(bearer|token|key|authorization)[=:\s]+\S+`)
	// File paths with home directory
	homePathRe = regexp.MustCompile(`(/Users/[^/]+|/home/[^/]+|C:\\Users\\[^\\]+)`)
)

// RedactError strips sensitive data from an error message using an allowlist approach.
// It removes hex tokens, URL query params, email addresses, and auth tokens.
func RedactError(msg string) string {
	msg = hexTokenRe.ReplaceAllString(msg, "[REDACTED]")
	msg = urlQueryRe.ReplaceAllString(msg, "?[REDACTED]")
	msg = emailRe.ReplaceAllString(msg, "[EMAIL]")
	msg = bearerRe.ReplaceAllString(msg, "${1}=[REDACTED]")
	msg = homePathRe.ReplaceAllString(msg, "[HOME]")
	return msg
}

// RedactTimelineEntry converts an internal Event into a sanitized one-liner.
// Job names are replaced with "job-N" placeholders to avoid leaking sensitive names.
func RedactTimelineEntry(event events.Event, jobIndex int) events.SanitizedTimelineEntry {
	entry := events.SanitizedTimelineEntry{
		Timestamp: event.Timestamp().Format(time.RFC3339),
	}

	switch e := event.(type) {
	case *events.LogEvent:
		entry.Type = "log"
		entry.Summary = fmt.Sprintf("[%s] %s", e.Level.String(), RedactError(e.Message))
	case *events.ErrorEvent:
		entry.Type = "error"
		msg := ""
		if e.Error != nil {
			msg = RedactError(e.Error.Error())
		}
		entry.Summary = fmt.Sprintf("error in %s: %s", e.Stage, msg)
	case *events.StateChangeEvent:
		entry.Type = "state_change"
		entry.Summary = fmt.Sprintf("job-%d: %s → %s (%s)", jobIndex, e.OldStatus, e.NewStatus, e.Stage)
	case *events.CompleteEvent:
		entry.Type = "complete"
		entry.Summary = fmt.Sprintf("completed: %d/%d succeeded in %s", e.SuccessJobs, e.TotalJobs, e.Duration.Round(time.Second))
	case *events.TransferEvent:
		entry.Type = "transfer"
		entry.Summary = fmt.Sprintf("%s: %s %.0f%%", e.EventType, sanitizeFileName(e.Name), e.Progress*100)
		if e.Error != nil {
			entry.Summary += " error: " + RedactError(e.Error.Error())
		}
	case *events.ProgressEvent:
		entry.Type = "progress"
		entry.Summary = fmt.Sprintf("job-%d %s: %.0f%%", jobIndex, e.Stage, e.Progress*100)
	case *events.BatchProgressEvent:
		entry.Type = "batch_progress"
		entry.Summary = fmt.Sprintf("batch %s/%s: %d/%d (%.0f%%)", e.Direction, e.Label, e.Completed, e.Total, e.Progress*100)
	case *events.EnumerationEvent:
		entry.Type = "enumeration"
		entry.Summary = fmt.Sprintf("scan %s: %d files, %d folders", e.Direction, e.FilesFound, e.FoldersFound)
	default:
		entry.Type = string(event.Type())
		entry.Summary = "(event)"
	}

	// Truncate long summaries
	if len(entry.Summary) > 200 {
		entry.Summary = entry.Summary[:200] + "..."
	}

	return entry
}

// RedactTimeline batch-converts the most recent N events from a snapshot.
func RedactTimeline(rawEvents []events.Event, limit int) []events.SanitizedTimelineEntry {
	start := 0
	if len(rawEvents) > limit {
		start = len(rawEvents) - limit
	}

	jobNameIndex := make(map[string]int)
	nextIndex := 1

	entries := make([]events.SanitizedTimelineEntry, 0, len(rawEvents)-start)
	for _, event := range rawEvents[start:] {
		// Determine job index from event if applicable
		jobIdx := 0
		jobName := extractJobName(event)
		if jobName != "" {
			if idx, ok := jobNameIndex[jobName]; ok {
				jobIdx = idx
			} else {
				jobIdx = nextIndex
				jobNameIndex[jobName] = nextIndex
				nextIndex++
			}
		}
		entries = append(entries, RedactTimelineEntry(event, jobIdx))
	}

	return entries
}

// extractJobName returns the job name from an event, if applicable.
func extractJobName(event events.Event) string {
	switch e := event.(type) {
	case *events.LogEvent:
		return e.JobName
	case *events.ErrorEvent:
		return e.JobName
	case *events.StateChangeEvent:
		return e.JobName
	case *events.ProgressEvent:
		return e.JobName
	default:
		return ""
	}
}

// sanitizeFileName keeps only the basename of a file path to avoid leaking directory structure.
func sanitizeFileName(name string) string {
	// Find last separator
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' || name[i] == '\\' {
			return name[i+1:]
		}
	}
	return name
}

// SanitizeFilePaths replaces full paths with just basenames in a string.
func SanitizeFilePaths(s string) string {
	// Replace common absolute path patterns
	s = homePathRe.ReplaceAllString(s, "[HOME]")

	// Replace Windows-style paths
	winPath := regexp.MustCompile(`[A-Z]:\\[^\s"']+`)
	s = winPath.ReplaceAllStringFunc(s, func(match string) string {
		parts := strings.Split(match, "\\")
		return "[PATH]\\" + parts[len(parts)-1]
	})

	return s
}
