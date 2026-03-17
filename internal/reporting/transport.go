package reporting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rescale/rescale-int/internal/config"
)

// FileTransport writes a report to a specified file path.
type FileTransport struct{}

// Save writes the report as JSON to the given path.
func (t *FileTransport) Save(report *ErrorReport, path string) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

// AutoFileTransport writes a report to an auto-generated path under
// config.ReportDirectory().
type AutoFileTransport struct{}

// Save writes the report and returns the saved path.
func (t *AutoFileTransport) Save(report *ErrorReport) (string, error) {
	if err := config.EnsureReportDirectory(); err != nil {
		return "", fmt.Errorf("ensure report directory: %w", err)
	}

	filename := fmt.Sprintf("report-%s.json", time.Now().Format("2006-01-02T150405"))
	path := filepath.Join(config.ReportDirectory(), filename)

	ft := &FileTransport{}
	if err := ft.Save(report, path); err != nil {
		return "", err
	}
	return path, nil
}

// FormatTextSummary produces a compact text summary for CLI stderr output.
func FormatTextSummary(report *ErrorReport) string {
	return fmt.Sprintf(
		"Error Report (%s)\n"+
			"  Category:  %s\n"+
			"  Operation: %s\n"+
			"  Class:     %s\n"+
			"  Message:   %s\n"+
			"  Error ID:  %s",
		report.Severity,
		report.Category,
		report.Operation,
		report.ErrorClass,
		report.ErrorMessage,
		report.ErrorID,
	)
}
