// Package gui provides the graphical user interface for rescale-int.
// StatusBar widget - unified status display component with level-based icons.
// v2.6.0 (November 25, 2025)
package gui

import (
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// StatusLevel represents the type of status being displayed
type StatusLevel int

const (
	// StatusInfo is the default info level
	StatusInfo StatusLevel = iota
	// StatusSuccess indicates a successful operation
	StatusSuccess
	// StatusWarning indicates a warning condition
	StatusWarning
	// StatusError indicates an error condition
	StatusError
	// StatusProgress indicates an operation in progress
	StatusProgress
)

// StatusBar is a reusable status display widget with level-based icons
type StatusBar struct {
	widget.BaseWidget

	mu      sync.RWMutex
	level   StatusLevel
	message string

	// UI components
	icon    *widget.Icon
	label   *widget.Label
	spinner *widget.Activity
}

// NewStatusBar creates a new status bar with default "Ready" message
func NewStatusBar() *StatusBar {
	sb := &StatusBar{
		level:   StatusInfo,
		message: "Ready",
	}
	sb.label = widget.NewLabel("Ready")
	sb.label.TextStyle = fyne.TextStyle{Italic: true}
	sb.icon = widget.NewIcon(theme.InfoIcon())
	sb.spinner = widget.NewActivity()
	sb.spinner.Hide()
	sb.ExtendBaseWidget(sb)
	return sb
}

// SetStatus updates the status message and level
func (sb *StatusBar) SetStatus(message string, level StatusLevel) {
	sb.mu.Lock()
	sb.level = level
	sb.message = message
	sb.mu.Unlock()

	fyne.Do(func() {
		sb.label.SetText(message)
		sb.spinner.Stop()
		sb.spinner.Hide()
		sb.icon.Show()

		switch level {
		case StatusInfo:
			sb.icon.SetResource(theme.InfoIcon())
		case StatusSuccess:
			sb.icon.SetResource(theme.ConfirmIcon())
		case StatusWarning:
			sb.icon.SetResource(theme.WarningIcon())
		case StatusError:
			sb.icon.SetResource(theme.ErrorIcon())
		case StatusProgress:
			sb.icon.Hide()
			sb.spinner.Show()
			sb.spinner.Start()
		}
	})
}

// SetInfo is a convenience method for info-level status
func (sb *StatusBar) SetInfo(message string) {
	sb.SetStatus(message, StatusInfo)
}

// SetSuccess is a convenience method for success-level status
func (sb *StatusBar) SetSuccess(message string) {
	sb.SetStatus(message, StatusSuccess)
}

// SetWarning is a convenience method for warning-level status
func (sb *StatusBar) SetWarning(message string) {
	sb.SetStatus(message, StatusWarning)
}

// SetError is a convenience method for error-level status
func (sb *StatusBar) SetError(message string) {
	sb.SetStatus(message, StatusError)
}

// SetProgress is a convenience method for progress-level status (shows spinner)
func (sb *StatusBar) SetProgress(message string) {
	sb.SetStatus(message, StatusProgress)
}

// GetMessage returns the current status message
func (sb *StatusBar) GetMessage() string {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.message
}

// GetLevel returns the current status level
func (sb *StatusBar) GetLevel() StatusLevel {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.level
}

// CreateRenderer implements fyne.Widget
func (sb *StatusBar) CreateRenderer() fyne.WidgetRenderer {
	content := container.NewHBox(sb.icon, sb.spinner, sb.label)
	return widget.NewSimpleRenderer(content)
}
