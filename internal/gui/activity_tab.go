package gui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
)

// ActivityTab manages the activity and logs interface
type ActivityTab struct {
	engine *core.Engine
	window fyne.Window

	// UI components
	logText      *widget.Entry
	logScroll    *container.Scroll
	progressBar  *widget.ProgressBar
	statusLabel  *widget.Label
	levelFilter  *widget.Select
	searchEntry  *widget.Entry
	autoScroll   *widget.Check
	clearButton  *widget.Button
	exportButton *widget.Button

	// Stats labels
	totalLogsLabel *widget.Label
	errorsLabel    *widget.Label
	warningsLabel  *widget.Label
	uptimeLabel    *widget.Label

	// Data
	logs      []LogEntry
	logsLock  sync.RWMutex
	maxLogs   int
	startTime time.Time
}

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp time.Time
	Level     events.LogLevel
	Stage     string
	JobName   string
	Message   string
}

// NewActivityTab creates a new activity tab
func NewActivityTab(engine *core.Engine, window fyne.Window) *ActivityTab {
	return &ActivityTab{
		engine:      engine,
		window:      window,
		logs:        make([]LogEntry, 0, 1000),
		maxLogs:     10000, // Keep last 10k logs
		progressBar: widget.NewProgressBar(),
		statusLabel: widget.NewLabel("Ready"),
		startTime:   time.Now(),
	}
}

// Build creates the activity tab UI
func (at *ActivityTab) Build() fyne.CanvasObject {
	// Log text area
	at.logText = widget.NewMultiLineEntry()
	at.logText.SetPlaceHolder("Activity logs will appear here...")
	at.logText.Wrapping = fyne.TextWrapWord
	at.logText.Disable() // Read-only

	at.logScroll = container.NewScroll(at.logText)
	at.logScroll.SetMinSize(fyne.NewSize(800, 500))

	// Create stat labels FIRST (before any callbacks that might use them)
	at.totalLogsLabel = widget.NewLabel("0")
	at.errorsLabel = widget.NewLabel("0")
	at.warningsLabel = widget.NewLabel("0")
	at.uptimeLabel = widget.NewLabel("0s")

	// Create search entry (before levelFilter triggers callback)
	at.searchEntry = widget.NewEntry()
	at.searchEntry.SetPlaceHolder("Search logs...")
	at.searchEntry.OnChanged = at.onSearchChange

	// Now create filter controls (callback will have searchEntry available)
	at.levelFilter = widget.NewSelect([]string{
		"All Levels",
		"DEBUG",
		"INFO",
		"WARN",
		"ERROR",
	}, at.onFilterChange)
	at.levelFilter.SetSelected("All Levels")

	at.autoScroll = widget.NewCheck("Auto-scroll", nil)
	at.autoScroll.SetChecked(true)

	at.clearButton = widget.NewButton("Clear Logs", func() {
		// Confirm before clearing
		dialog.ShowConfirm("Clear Logs?",
			fmt.Sprintf("This will permanently delete all %d log entries.\n\nAre you sure?", len(at.logs)),
			func(confirmed bool) {
				if confirmed {
					at.clearLogs()
				}
			},
			at.window,
		)
	})

	at.exportButton = widget.NewButton("Export Logs", at.exportLogs)

	// Use GridWithColumns for better layout control
	filterSection := container.NewBorder(
		nil, nil,
		// Left side: filters
		container.NewHBox(
			widget.NewLabel("Level:"),
			at.levelFilter,
		),
		// Right side: buttons
		container.NewHBox(
			at.autoScroll,
			at.clearButton,
			at.exportButton,
		),
		// Center: search with label
		container.NewBorder(
			nil, nil,
			widget.NewLabel("Search:"),
			nil,
			at.searchEntry,
		),
	)

	// Progress section
	progressSection := container.NewVBox(
		widget.NewLabel("Overall Progress:"),
		at.progressBar,
		at.statusLabel,
	)

	// Statistics section - labels already created above
	statsGrid := container.NewGridWithColumns(4,
		at.createStatCardWithLabel("Total Logs", at.totalLogsLabel),
		at.createStatCardWithLabel("Errors", at.errorsLabel),
		at.createStatCardWithLabel("Warnings", at.warningsLabel),
		at.createStatCardWithLabel("Uptime", at.uptimeLabel),
	)

	// Layout
	return container.NewBorder(
		container.NewVBox(
			filterSection,
			widget.NewSeparator(),
			statsGrid,
			widget.NewSeparator(),
		),
		progressSection,
		nil,
		nil,
		at.logScroll,
	)
}

func (at *ActivityTab) createStatCard(title, value string) fyne.CanvasObject {
	titleLabel := widget.NewLabel(title)
	titleLabel.TextStyle = fyne.TextStyle{Bold: true}

	valueLabel := widget.NewLabel(value)

	return container.NewVBox(
		titleLabel,
		valueLabel,
	)
}

func (at *ActivityTab) createStatCardWithLabel(title string, valueLabel *widget.Label) fyne.CanvasObject {
	titleLabel := widget.NewLabel(title)
	titleLabel.TextStyle = fyne.TextStyle{Bold: true}

	return container.NewVBox(
		titleLabel,
		valueLabel,
	)
}

// AddLog adds a log entry and updates the display
func (at *ActivityTab) AddLog(event *events.LogEvent) {
	at.logsLock.Lock()

	// Add new log
	entry := LogEntry{
		Timestamp: event.Timestamp(),
		Level:     event.Level,
		Stage:     event.Stage,
		JobName:   event.JobName,
		Message:   event.Message,
	}

	at.logs = append(at.logs, entry)

	// Trim old logs if necessary
	if len(at.logs) > at.maxLogs {
		at.logs = at.logs[len(at.logs)-at.maxLogs:]
	}

	at.logsLock.Unlock()

	// Update display
	at.refreshDisplay()

	// Auto-scroll if enabled
	if at.autoScroll.Checked {
		// Scroll to bottom using scroll container API
		at.logScroll.ScrollToBottom()
	}
}

// UpdateOverallProgress updates the overall progress bar
func (at *ActivityTab) UpdateOverallProgress(event *events.ProgressEvent) {
	if event.Stage == "overall" {
		at.progressBar.SetValue(event.Progress)
		at.statusLabel.SetText(event.Message)
	}
}

func (at *ActivityTab) refreshDisplay() {
	at.logsLock.RLock()
	defer at.logsLock.RUnlock()

	// Apply filters
	filtered := at.filterLogs()

	// Build display text
	var sb strings.Builder
	for _, entry := range filtered {
		line := at.formatLogEntry(entry)
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	at.logText.SetText(sb.String())

	// Update statistics
	at.updateStats()
}

func (at *ActivityTab) updateStats() {
	// Count errors and warnings
	errorCount := 0
	warningCount := 0
	for _, entry := range at.logs {
		if entry.Level == events.ErrorLevel {
			errorCount++
		} else if entry.Level == events.WarnLevel {
			warningCount++
		}
	}

	// Update labels (already holding RLock from caller)
	// Safety check: labels may not be initialized yet if called during Build
	if at.totalLogsLabel != nil {
		at.totalLogsLabel.SetText(fmt.Sprintf("%d", len(at.logs)))
	}
	if at.errorsLabel != nil {
		at.errorsLabel.SetText(fmt.Sprintf("%d", errorCount))
	}
	if at.warningsLabel != nil {
		at.warningsLabel.SetText(fmt.Sprintf("%d", warningCount))
	}

	// Calculate uptime
	uptime := time.Since(at.startTime)
	uptimeStr := ""
	if uptime.Hours() >= 1 {
		uptimeStr = fmt.Sprintf("%.1fh", uptime.Hours())
	} else if uptime.Minutes() >= 1 {
		uptimeStr = fmt.Sprintf("%.1fm", uptime.Minutes())
	} else {
		uptimeStr = fmt.Sprintf("%.0fs", uptime.Seconds())
	}
	if at.uptimeLabel != nil {
		at.uptimeLabel.SetText(uptimeStr)
	}
}

func (at *ActivityTab) filterLogs() []LogEntry {
	filtered := make([]LogEntry, 0, len(at.logs))

	// Get filter criteria
	levelFilter := at.levelFilter.Selected
	searchText := strings.ToLower(at.searchEntry.Text)

	for _, entry := range at.logs {
		// Level filter
		if levelFilter != "All Levels" && entry.Level.String() != levelFilter {
			continue
		}

		// Search filter
		if searchText != "" {
			entryText := strings.ToLower(at.formatLogEntry(entry))
			if !strings.Contains(entryText, searchText) {
				continue
			}
		}

		filtered = append(filtered, entry)
	}

	return filtered
}

func (at *ActivityTab) formatLogEntry(entry LogEntry) string {
	timestamp := entry.Timestamp.Format("15:04:05")
	level := entry.Level.String()

	var parts []string
	parts = append(parts, timestamp)
	parts = append(parts, level)

	if entry.Stage != "" {
		parts = append(parts, fmt.Sprintf("[%s]", entry.Stage))
	}

	if entry.JobName != "" {
		parts = append(parts, fmt.Sprintf("[%s]", entry.JobName))
	}

	parts = append(parts, entry.Message)

	return strings.Join(parts, " ")
}

func (at *ActivityTab) onFilterChange(value string) {
	at.refreshDisplay()
}

func (at *ActivityTab) onSearchChange(value string) {
	at.refreshDisplay()
}

func (at *ActivityTab) clearLogs() {
	at.logsLock.Lock()
	at.logs = make([]LogEntry, 0, 1000)
	at.startTime = time.Now() // Reset uptime
	at.logsLock.Unlock()

	at.logText.SetText("")
	at.statusLabel.SetText("Logs cleared")
	at.refreshDisplay() // Update stats to show zeros
}

func (at *ActivityTab) exportLogs() {
	at.logsLock.RLock()
	defer at.logsLock.RUnlock()

	// Build export text
	var sb strings.Builder
	sb.WriteString("PUR Activity Log Export\n")
	sb.WriteString(fmt.Sprintf("Exported: %s\n", time.Now().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Total Entries: %d\n", len(at.logs)))
	sb.WriteString(strings.Repeat("=", 80))
	sb.WriteString("\n\n")

	for _, entry := range at.logs {
		line := at.formatLogEntry(entry)
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	// Show export text in a scrollable dialog
	content := widget.NewMultiLineEntry()
	content.SetText(sb.String())
	content.Wrapping = fyne.TextWrapWord
	content.Disable() // Read-only

	scrollContent := container.NewScroll(content)
	scrollContent.SetMinSize(fyne.NewSize(800, 500))

	// Use proper dialog instead of modal popup
	exportDialog := dialog.NewCustom(
		"Exported Logs",
		"Close",
		container.NewVBox(
			widget.NewLabel("Copy the text below or save to a file:"),
			scrollContent,
		),
		at.window,
	)
	exportDialog.Resize(fyne.NewSize(850, 650))
	exportDialog.Show()
}
