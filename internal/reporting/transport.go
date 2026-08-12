package reporting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// maxRetainedReports bounds how many auto-saved reports are kept on disk.
// Nothing else prunes this directory, and a repeating failure (a daemon retrying
// the same broken job every poll, for instance) writes one file per occurrence
// for as long as it keeps failing.
const maxRetainedReports = 500

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

	pruneOldReports(config.ReportDirectory(), maxRetainedReports)
	return path, nil
}

// pruneOldReports keeps the newest keep report files and deletes the rest.
// Best-effort: a report that cannot be removed is left in place, since failing
// to prune must never fail the report that was just written.
func pruneOldReports(dir string, keep int) {
	if keep <= 0 {
		return
	}
	matches, err := filepath.Glob(filepath.Join(dir, "report-*.json"))
	if err != nil || len(matches) <= keep {
		return
	}

	// Filenames carry a sortable timestamp, but a lexical sort would misorder
	// files written by an older naming scheme. Sort on mtime instead.
	type entry struct {
		path string
		mod  time.Time
	}
	entries := make([]entry, 0, len(matches))
	for _, m := range matches {
		info, statErr := os.Stat(m)
		if statErr != nil {
			continue
		}
		entries = append(entries, entry{path: m, mod: info.ModTime()})
	}
	if len(entries) <= keep {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod.After(entries[j].mod) })
	for _, e := range entries[keep:] {
		_ = os.Remove(e.path)
	}
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
