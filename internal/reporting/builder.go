package reporting

import (
	"runtime"
	"time"

	"github.com/rescale/rescale-int/internal/events"
	intfips "github.com/rescale/rescale-int/internal/fips"
	"github.com/rescale/rescale-int/internal/version"
)

// ErrorReport is the top-level structure serialized to JSON for the user.
type ErrorReport struct {
	ReportVersion string    `json:"reportVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`

	// Environment
	AppVersion  string `json:"appVersion"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Mode        string `json:"mode"` // "gui", "cli", "daemon"
	FIPSEnabled bool   `json:"fipsEnabled"`

	// Workspace context (helps support identify the customer/environment)
	WorkspaceName string `json:"workspaceName,omitempty"`
	WorkspaceID   string `json:"workspaceID,omitempty"`
	PlatformURL   string `json:"platformURL,omitempty"`

	// Error context
	ErrorID      string `json:"errorID"`
	Category     string `json:"category"`
	Severity     string `json:"severity"`
	Operation    string `json:"operation"`
	Backend      string `json:"backend"`
	ErrorMessage string `json:"errorMessage"`
	ErrorClass   string `json:"errorClass"`

	// Runtime facts (allowlisted only)
	MaxConcurrent  int `json:"maxConcurrent,omitempty"`
	FailedCount    int `json:"failedCount,omitempty"`
	SucceededCount int `json:"succeededCount,omitempty"`

	// Timeline (last 20 events, redacted — GUI only, empty for CLI/daemon)
	Timeline []events.SanitizedTimelineEntry `json:"timeline"`

	// User note (GUI only — CLI has no modal)
	UserNote string `json:"userNote,omitempty"`
}

// Builder assembles ErrorReports from classified errors and pre-snapshotted timelines.
type Builder struct {
	mode string // "gui", "cli", "daemon"
}

// NewBuilder creates a Builder for the given mode.
func NewBuilder(mode string) *Builder {
	return &Builder{mode: mode}
}

// Build assembles a complete ErrorReport from a classified error, pre-redacted timeline,
// and optional user note. The timeline is passed in pre-snapshotted and pre-redacted —
// the builder does NOT access the EventBus.
func (b *Builder) Build(classified *ClassifiedError, timeline []events.SanitizedTimelineEntry, userNote string) *ErrorReport {
	if classified == nil {
		return nil
	}

	return &ErrorReport{
		ReportVersion: "1.0",
		GeneratedAt:   time.Now(),

		AppVersion:  version.Version,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Mode:        b.mode,
		FIPSEnabled: intfips.Enabled,

		ErrorID:      classified.ErrorID,
		Category:     string(classified.Category),
		Severity:     string(classified.Severity),
		Operation:    classified.Operation,
		Backend:      classified.Backend,
		ErrorMessage: classified.ErrorMessage,
		ErrorClass:   string(classified.ErrorClass),

		Timeline: timeline,
		UserNote: userNote,
	}
}
