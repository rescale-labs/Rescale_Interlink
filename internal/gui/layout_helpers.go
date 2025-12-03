package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

// =============================================================================
// Layout Helper Functions
// =============================================================================
// These helpers provide consistent spacing throughout the UI.

// VerticalSpacer creates a fixed-height vertical spacer for adding breathing room
// between sections. Use standardized heights for consistency:
//   - Small: 8
//   - Medium: 16
//   - Large: 24
func VerticalSpacer(height float32) fyne.CanvasObject {
	spacer := canvas.NewRectangle(nil) // Transparent
	spacer.SetMinSize(fyne.NewSize(0, height))
	return spacer
}

// HorizontalSpacer creates a fixed-width horizontal spacer
func HorizontalSpacer(width float32) fyne.CanvasObject {
	spacer := canvas.NewRectangle(nil) // Transparent
	spacer.SetMinSize(fyne.NewSize(width, 0))
	return spacer
}

// =============================================================================
// Button Helper Functions
// =============================================================================
// These helpers create buttons with consistent styling (white text on blue).
// Fyne only uses ColorNameForegroundOnPrimary for HighImportance buttons,
// so we must set HighImportance to get white text on the primary (blue) background.

// NewPrimaryButton creates a button with white text on blue background.
// Use this for all standard action buttons.
func NewPrimaryButton(label string, tapped func()) *widget.Button {
	btn := widget.NewButton(label, tapped)
	btn.Importance = widget.HighImportance
	return btn
}

// NewPrimaryButtonWithIcon creates a button with an icon and white text on blue background.
func NewPrimaryButtonWithIcon(label string, icon fyne.Resource, tapped func()) *widget.Button {
	btn := widget.NewButtonWithIcon(label, icon, tapped)
	btn.Importance = widget.HighImportance
	return btn
}
