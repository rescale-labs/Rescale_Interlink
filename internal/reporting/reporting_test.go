package reporting

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/events"
)

// --- Classifier tests ---

func TestClassify_BasicError(t *testing.T) {
	err := errors.New("upload failed: connection refused")
	c := Classify(err, CategoryTransfer, "folder_upload", "s3")
	if c == nil {
		t.Fatal("expected non-nil ClassifiedError")
	}
	if c.ErrorID == "" {
		t.Error("expected non-empty ErrorID")
	}
	if c.Category != CategoryTransfer {
		t.Errorf("expected category %q, got %q", CategoryTransfer, c.Category)
	}
	if c.ErrorClass != ClassNetwork {
		t.Errorf("expected class %q, got %q", ClassNetwork, c.ErrorClass)
	}
	if c.Operation != "folder_upload" {
		t.Errorf("expected operation %q, got %q", "folder_upload", c.Operation)
	}
}

func TestClassify_AuthError_IsCritical(t *testing.T) {
	err := errors.New("401 unauthorized")
	c := Classify(err, CategoryAuth, "test_connection", "")
	if c.Severity != SeverityCritical {
		t.Errorf("expected severity %q, got %q", SeverityCritical, c.Severity)
	}
	if c.ErrorClass != ClassAuth {
		t.Errorf("expected class %q, got %q", ClassAuth, c.ErrorClass)
	}
}

func TestClassify_Nil(t *testing.T) {
	c := Classify(nil, CategoryTransfer, "op", "")
	if c != nil {
		t.Error("expected nil for nil error")
	}
}

func TestClassifyErrorClass(t *testing.T) {
	tests := []struct {
		msg  string
		want ErrorClass
	}{
		{"HTTP 401 Unauthorized", ClassAuth},
		{"403 Forbidden access", ClassAuth},
		{"context deadline exceeded", ClassTimeout},
		{"dial tcp: connection refused", ClassNetwork},
		{"no space left on device", ClassDiskSpace},
		{"HTTP 400 Bad Request", ClassClientError},
		{"status 404: not found", ClassClientError},
		{"HTTP 500 Internal Server Error", ClassServerError},
		{"502 Bad Gateway", ClassServerError},
		{"503 Service Unavailable", ClassServerError},
		{"some unknown error", ClassInternal},
	}
	for _, tt := range tests {
		got := ClassifyErrorClass(tt.msg)
		if got != tt.want {
			t.Errorf("ClassifyErrorClass(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

func TestIsReportable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		category ErrorCategory
		want     bool
	}{
		// Not reportable: nil, cancellation, transient
		{"nil error", nil, CategoryTransfer, false},
		{"context.Canceled", context.Canceled, CategoryTransfer, false},
		{"contains context canceled", errors.New("context canceled during upload"), CategoryTransfer, false},
		{"rate limit 429", errors.New("429 Too Many Requests"), CategoryTransfer, false},
		{"daemon stopped", errors.New("daemon stopped during download"), CategoryTransfer, false},

		// Not reportable: user-fixable errors
		{"auth 401", errors.New("API request failed with status 401: invalid token"), CategoryAuth, false},
		{"auth 403", errors.New("403 Forbidden"), CategoryTransfer, false},
		{"network error", errors.New("connection refused"), CategoryTransfer, false},
		{"dns error", errors.New("no such host api.rescale.com"), CategoryTransfer, false},
		{"timeout", errors.New("context deadline exceeded"), CategoryTransfer, false},
		{"disk space", errors.New("no space left on device"), CategoryTransfer, false},
		{"client 400", errors.New("API returned 400 bad request"), CategoryTransfer, false},
		{"client 404", errors.New("status 404: file not found"), CategoryTransfer, false},

		// Reportable: server errors and unclassified internal errors
		{"server 500", errors.New("API returned 500 internal server error"), CategoryTransfer, true},
		{"server 502", errors.New("502 bad gateway"), CategoryTransfer, true},
		{"server 503", errors.New("503 service unavailable"), CategoryTransfer, true},
		{"unclassified error", errors.New("some unexpected error"), CategoryTransfer, true},
		{"batch wipeout", errors.New("batch download failed: 5/5 transfers failed"), CategoryTransfer, true},
		{"pipeline failure", errors.New("pipeline completed with 3/3 jobs failed"), CategoryPURPipeline, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsReportable(tt.err, tt.category)
			if got != tt.want {
				t.Errorf("IsReportable(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// --- Redactor tests ---

func TestRedactError(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(string) bool
	}{
		{
			"hex token stripped",
			"failed with token abc123def456789012345678 during upload",
			func(s string) bool { return strings.Contains(s, "[REDACTED]") && !strings.Contains(s, "abc123def") },
		},
		{
			"URL query params stripped",
			"GET https://storage.blob.core.windows.net/container/file?sig=abc123&se=2024-01-01",
			func(s string) bool { return strings.Contains(s, "?[REDACTED]") && !strings.Contains(s, "sig=") },
		},
		{
			"email stripped",
			"authenticated as user@example.com",
			func(s string) bool { return strings.Contains(s, "[EMAIL]") && !strings.Contains(s, "user@example.com") },
		},
		{
			"bearer token stripped",
			"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9",
			func(s string) bool { return strings.Contains(s, "[REDACTED]") },
		},
		{
			"home path stripped",
			"file not found at /Users/john/Documents/secret.txt",
			func(s string) bool { return strings.Contains(s, "[HOME]") && !strings.Contains(s, "/Users/john") },
		},
		{
			"clean error unchanged",
			"connection refused",
			func(s string) bool { return s == "connection refused" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactError(tt.input)
			if !tt.check(got) {
				t.Errorf("RedactError(%q) = %q, did not pass check", tt.input, got)
			}
		})
	}
}

func TestRedactTimelineEntry_LogEvent(t *testing.T) {
	e := &events.LogEvent{
		BaseEvent: events.BaseEvent{EventType: events.EventLog, Time: time.Now()},
		Level:     events.ErrorLevel,
		Message:   "failed to connect to host",
		Stage:     "upload",
	}
	entry := RedactTimelineEntry(e, 0)
	if entry.Type != "log" {
		t.Errorf("expected type 'log', got %q", entry.Type)
	}
	if !strings.Contains(entry.Summary, "ERROR") {
		t.Errorf("expected summary to contain 'ERROR', got %q", entry.Summary)
	}
}

func TestRedactTimeline_LimitsEntries(t *testing.T) {
	rawEvents := make([]events.Event, 30)
	for i := range rawEvents {
		rawEvents[i] = &events.LogEvent{
			BaseEvent: events.BaseEvent{EventType: events.EventLog, Time: time.Now()},
			Level:     events.InfoLevel,
			Message:   "test message",
		}
	}

	entries := RedactTimeline(rawEvents, 20)
	if len(entries) != 20 {
		t.Errorf("expected 20 entries, got %d", len(entries))
	}
}

func TestRedactTimeline_FewerThanLimit(t *testing.T) {
	rawEvents := make([]events.Event, 5)
	for i := range rawEvents {
		rawEvents[i] = &events.LogEvent{
			BaseEvent: events.BaseEvent{EventType: events.EventLog, Time: time.Now()},
			Level:     events.InfoLevel,
			Message:   "test",
		}
	}

	entries := RedactTimeline(rawEvents, 20)
	if len(entries) != 5 {
		t.Errorf("expected 5 entries, got %d", len(entries))
	}
}

// --- Builder tests ---

func TestBuilder_Build(t *testing.T) {
	b := NewBuilder("gui")
	classified := &ClassifiedError{
		ErrorID:      "test-id",
		Category:     CategoryTransfer,
		Severity:     SeverityError,
		Operation:    "folder_upload",
		Backend:      "s3",
		ErrorMessage: "connection refused",
		ErrorClass:   ClassNetwork,
	}
	timeline := []events.SanitizedTimelineEntry{
		{Timestamp: "2024-01-01T00:00:00Z", Type: "log", Summary: "test"},
	}

	report := b.Build(classified, timeline, "I was uploading files")
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if report.ReportVersion != "1.0" {
		t.Errorf("expected version 1.0, got %q", report.ReportVersion)
	}
	if report.Mode != "gui" {
		t.Errorf("expected mode 'gui', got %q", report.Mode)
	}
	if report.ErrorID != "test-id" {
		t.Errorf("expected errorID 'test-id', got %q", report.ErrorID)
	}
	if report.Category != "transfer" {
		t.Errorf("expected category 'transfer', got %q", report.Category)
	}
	if len(report.Timeline) != 1 {
		t.Errorf("expected 1 timeline entry, got %d", len(report.Timeline))
	}
	if report.UserNote != "I was uploading files" {
		t.Errorf("expected user note, got %q", report.UserNote)
	}
}

func TestBuilder_NilClassified(t *testing.T) {
	b := NewBuilder("cli")
	report := b.Build(nil, nil, "")
	if report != nil {
		t.Error("expected nil report for nil classified error")
	}
}

func TestBuilder_EmptyTimeline(t *testing.T) {
	b := NewBuilder("cli")
	classified := &ClassifiedError{
		ErrorID:      "test-id",
		Category:     CategoryTransfer,
		Severity:     SeverityError,
		Operation:    "file_download",
		ErrorMessage: "some error",
		ErrorClass:   ClassInternal,
	}
	report := b.Build(classified, nil, "")
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if report.Timeline != nil {
		t.Errorf("expected nil timeline, got %v", report.Timeline)
	}
}

// --- Reporter tests ---

func TestReporter_Report_Reportable(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()

	// Subscribe to capture the published event
	ch := eb.Subscribe(events.EventReportableError)

	r := NewReporter(eb)
	// Use a server error (5xx) — user-fixable errors like "connection refused" are no longer reportable
	errorID := r.Report(errors.New("API returned 500 internal server error"), CategoryTransfer, "folder_upload", "s3")
	if errorID == "" {
		t.Fatal("expected non-empty errorID for reportable error")
	}

	// Verify event was published
	select {
	case event := <-ch:
		re, ok := event.(*events.ReportableErrorEvent)
		if !ok {
			t.Fatalf("expected ReportableErrorEvent, got %T", event)
		}
		if re.ErrorID != errorID {
			t.Errorf("expected errorID %q, got %q", errorID, re.ErrorID)
		}
		if re.Category != "transfer" {
			t.Errorf("expected category 'transfer', got %q", re.Category)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ReportableErrorEvent")
	}
}

func TestReporter_Report_UserFixableNotReportable(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()

	r := NewReporter(eb)

	// Auth, network, timeout, disk space, client errors — user can fix these
	tests := []struct {
		name string
		err  error
	}{
		{"auth", errors.New("401 unauthorized")},
		{"network", errors.New("connection refused")},
		{"timeout", errors.New("context deadline exceeded")},
		{"disk", errors.New("no space left on device")},
		{"client 404", errors.New("status 404 not found")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errorID := r.Report(tt.err, CategoryTransfer, "folder_upload", "s3")
			if errorID != "" {
				t.Errorf("expected empty errorID for user-fixable %s error, got %q", tt.name, errorID)
			}
		})
	}
}

func TestReporter_Report_NotReportable(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()

	r := NewReporter(eb)
	errorID := r.Report(context.Canceled, CategoryTransfer, "folder_upload", "s3")
	if errorID != "" {
		t.Errorf("expected empty errorID for non-reportable error, got %q", errorID)
	}
}

func TestClassifyAndPublish(t *testing.T) {
	eb := events.NewEventBus(100)
	defer eb.Close()

	ch := eb.Subscribe(events.EventReportableError)

	errorID := ClassifyAndPublish(eb, errors.New("500 internal server error"), CategoryPURPipeline, "run", "")
	if errorID == "" {
		t.Fatal("expected non-empty errorID")
	}

	select {
	case event := <-ch:
		re := event.(*events.ReportableErrorEvent)
		if re.ErrorClass != "server_error" {
			t.Errorf("expected error class 'server_error', got %q", re.ErrorClass)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestClassifyAndPublish_NilBus(t *testing.T) {
	errorID := ClassifyAndPublish(nil, errors.New("error"), CategoryTransfer, "op", "")
	if errorID != "" {
		t.Errorf("expected empty errorID with nil bus, got %q", errorID)
	}
}

// --- Transport tests ---

func TestFileTransport_Save(t *testing.T) {
	report := &ErrorReport{
		ReportVersion: "1.0",
		ErrorID:       "test-id",
		Category:      "transfer",
		ErrorMessage:  "test error",
	}

	dir := t.TempDir()
	path := dir + "/report.json"

	ft := &FileTransport{}
	if err := ft.Save(report, path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(string(data), `"errorID": "test-id"`) {
		t.Errorf("report does not contain expected errorID, got: %s", string(data))
	}
	if !strings.Contains(string(data), `"reportVersion": "1.0"`) {
		t.Error("report does not contain version")
	}
}

func TestFormatTextSummary(t *testing.T) {
	report := &ErrorReport{
		Severity:     "error",
		Category:     "transfer",
		Operation:    "folder_upload",
		ErrorClass:   "network",
		ErrorMessage: "connection refused",
		ErrorID:      "abc-123",
	}
	summary := FormatTextSummary(report)
	if !strings.Contains(summary, "transfer") {
		t.Errorf("expected 'transfer' in summary, got: %s", summary)
	}
	if !strings.Contains(summary, "connection refused") {
		t.Errorf("expected error message in summary, got: %s", summary)
	}
	if !strings.Contains(summary, "abc-123") {
		t.Errorf("expected error ID in summary, got: %s", summary)
	}
}

// --- CLI helper tests ---

func TestHandleCLIError_Nil(t *testing.T) {
	path := HandleCLIError(nil, "cli", "upload", "")
	if path != "" {
		t.Errorf("expected empty path for nil error, got %q", path)
	}
}

func TestHandleCLIError_NotReportable(t *testing.T) {
	path := HandleCLIError(context.Canceled, "cli", "upload", "")
	if path != "" {
		t.Errorf("expected empty path for non-reportable error, got %q", path)
	}
}

// --- CLI usage error filter tests ---

func TestIsCLIUsageError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		// Cobra parse errors — user typos
		{"unknown flag", "unknown flag: --token", true},
		{"unknown command", `unknown command "folder" for "rescale-int"`, true},
		{"unknown shorthand", "unknown shorthand flag: 'x' in -x", true},
		{"required flag", `required flag "api-key" not set`, true},
		{"invalid argument", `invalid argument "abc" for "--limit"`, true},
		{"bad flag syntax", "bad flag syntax: --foo=", true},
		{"flag needs argument", "flag needs an argument: --output", true},
		{"arg count", "accepts 1 arg(s), received 0", true},

		// Local path validation errors — user gave bad path
		{"file not found", "file not found: /nonexistent/path/file.txt", true},
		{"dir stat error", "failed to access directory: stat /bad/path: no such file or directory", true},

		// User-initiated cancellation at app level
		{"upload cancelled", "cannot skip root folder with --skip-folder-conflicts - upload cancelled", true},
		{"download cancelled", "operation cancelled by user - download cancelled", true},

		// Validation errors — bad IDs, no matching files
		{"no valid files download", "no valid files to download", true},
		{"no valid files upload", "no valid files to upload", true},
		{"no files found", "no files found matching criteria", true},

		// Real errors — should NOT be filtered
		{"server error", "API returned 500 internal server error", false},
		{"auth error", "401 unauthorized", false},
		{"network error", "connection refused", false},
		{"batch failure", "batch download failed: 5/5 transfers failed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCLIUsageError(tt.msg)
			if got != tt.want {
				t.Errorf("isCLIUsageError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestCategoryFromOperation(t *testing.T) {
	tests := []struct {
		operation string
		want      ErrorCategory
	}{
		{"rescale-int pur run", CategoryPURPipeline},
		{"rescale-int pur submit-existing", CategoryPURPipeline},
		{"rescale-int jobs submit", CategoryJobCreate},
		{"rescale-int jobs download", CategoryJobCreate},
		{"rescale-int folders upload-dir", CategoryTransfer},
		{"rescale-int files download", CategoryTransfer},
		{"job_download", CategoryJobCreate},
		{"", CategoryTransfer},
	}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			got := categoryFromOperation(tt.operation)
			if got != tt.want {
				t.Errorf("categoryFromOperation(%q) = %q, want %q", tt.operation, got, tt.want)
			}
		})
	}
}
