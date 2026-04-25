package wailsapp

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/reporting"
)

// BuildErrorReportRequest is the JSON payload the frontend sends when the user
// clicks "Copy to Clipboard" or "Save Report".
type BuildErrorReportRequest struct {
	ErrorID      string                          `json:"errorID"`
	Category     string                          `json:"category"`
	Severity     string                          `json:"severity"`
	Operation    string                          `json:"operation"`
	Backend      string                          `json:"backend"`
	ErrorMessage string                          `json:"errorMessage"`
	ErrorClass   string                          `json:"errorClass"`
	Timeline     []events.SanitizedTimelineEntry `json:"timeline"`
	UserNote     string                          `json:"userNote"`

	// Workspace context (sent by frontend from config store)
	WorkspaceName string `json:"workspaceName"`
	WorkspaceID   string `json:"workspaceID"`
	PlatformURL   string `json:"platformURL"`
}

// BuildErrorReport assembles a full report JSON string from the classified error
// data passed from the frontend. The frontend stores the full DTO locally when
// the event arrives and passes it back here — no backend lookup needed.
func (a *App) BuildErrorReport(requestJSON string) (string, error) {
	var req BuildErrorReportRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("invalid report request: %w", err)
	}

	classified := &reporting.ClassifiedError{
		ErrorID:      req.ErrorID,
		Category:     reporting.ErrorCategory(req.Category),
		Severity:     reporting.Severity(req.Severity),
		Operation:    req.Operation,
		Backend:      req.Backend,
		ErrorMessage: req.ErrorMessage,
		ErrorClass:   reporting.ErrorClass(req.ErrorClass),
	}

	builder := reporting.NewBuilder("gui")
	report := builder.Build(classified, req.Timeline, req.UserNote)
	if report == nil {
		return "", fmt.Errorf("failed to build report")
	}

	// Attach workspace context from the frontend config store.
	report.WorkspaceName = req.WorkspaceName
	report.WorkspaceID = req.WorkspaceID
	report.PlatformURL = req.PlatformURL

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal report: %w", err)
	}
	return string(data), nil
}

// SaveErrorReport opens a native save dialog and writes the report JSON to the
// selected path. Returns the saved path, or an error if the user cancelled or
// the write failed. Routed through portalAwareSaveFile under the shared
// dialogMu + recoverDialogPanic guards so the binding survives the Linux
// #41 GTK SIGTRAP and portal-unavailability conditions.
func (a *App) SaveErrorReport(reportJSON string) (path string, err error) {
	if !dialogMu.TryLock() {
		return "", fmt.Errorf(dialogBusyMessage)
	}
	defer dialogMu.Unlock()
	defer recoverDialogPanic("SaveErrorReport", &err)
	if a.ctx == nil {
		wailsLogger.Error().Str("binding", "SaveErrorReport").Msg("dialog binding invoked before context ready")
		return "", fmt.Errorf(appNotReadyError)
	}

	suggestedName := fmt.Sprintf("rescale-error-report-%s.json", time.Now().Format("2006-01-02T150405"))
	path, err = portalAwareSaveFile(a.ctx, "SaveErrorReport", runtime.SaveDialogOptions{
		DefaultFilename: suggestedName,
		Title:           "Save Error Report",
		Filters: []runtime.FileFilter{
			{DisplayName: "JSON Files (*.json)", Pattern: "*.json"},
			{DisplayName: "All Files (*.*)", Pattern: "*.*"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if path == "" {
		return "", nil // User cancelled
	}

	ft := &reporting.FileTransport{}
	var report reporting.ErrorReport
	if err := json.Unmarshal([]byte(reportJSON), &report); err != nil {
		return "", fmt.Errorf("invalid report JSON: %w", err)
	}
	if err := ft.Save(&report, path); err != nil {
		return "", err
	}

	return path, nil
}

// SaveLogExport opens a native save dialog and writes log text to the selected path.
// Uses native file dialog because blob URL downloads don't work in Wails WebView.
// Routed through portalAwareSaveFile under the shared dialogMu +
// recoverDialogPanic guards.
func (a *App) SaveLogExport(content string) (path string, err error) {
	if !dialogMu.TryLock() {
		return "", fmt.Errorf(dialogBusyMessage)
	}
	defer dialogMu.Unlock()
	defer recoverDialogPanic("SaveLogExport", &err)
	if a.ctx == nil {
		wailsLogger.Error().Str("binding", "SaveLogExport").Msg("dialog binding invoked before context ready")
		return "", fmt.Errorf(appNotReadyError)
	}

	suggestedName := fmt.Sprintf("interlink-activity-%s.log", time.Now().Format("2006-01-02"))
	path, err = portalAwareSaveFile(a.ctx, "SaveLogExport", runtime.SaveDialogOptions{
		DefaultFilename: suggestedName,
		Title:           "Export Activity Logs",
		Filters: []runtime.FileFilter{
			{DisplayName: "Log Files (*.log)", Pattern: "*.log"},
			{DisplayName: "Text Files (*.txt)", Pattern: "*.txt"},
			{DisplayName: "All Files (*.*)", Pattern: "*.*"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if path == "" {
		return "", nil // User cancelled
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write log export: %w", err)
	}

	return path, nil
}
