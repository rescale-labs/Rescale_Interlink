package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
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
