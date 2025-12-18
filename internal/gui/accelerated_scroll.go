package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
)

// AcceleratedScroll wraps container.Scroll.
// Note: Scroll acceleration was attempted but is not possible due to Fyne's
// internal event routing. See SCROLL_ISSUE_NOTES.md for details.
// This wrapper is kept for API compatibility and potential future improvements.
type AcceleratedScroll struct {
	*container.Scroll
}

// NewAcceleratedScroll creates a scroll container that scrolls in both directions.
func NewAcceleratedScroll(content fyne.CanvasObject) *AcceleratedScroll {
	return &AcceleratedScroll{
		Scroll: container.NewScroll(content),
	}
}

// NewAcceleratedVScroll creates a vertical-only scroll container.
func NewAcceleratedVScroll(content fyne.CanvasObject) *AcceleratedScroll {
	return &AcceleratedScroll{
		Scroll: container.NewVScroll(content),
	}
}

// NewAcceleratedHScroll creates a horizontal-only scroll container.
func NewAcceleratedHScroll(content fyne.CanvasObject) *AcceleratedScroll {
	return &AcceleratedScroll{
		Scroll: container.NewHScroll(content),
	}
}

// SetMinSize sets the minimum size of the scroll container.
func (as *AcceleratedScroll) SetMinSize(size fyne.Size) {
	as.Scroll.SetMinSize(size)
}

// ScrollToTop scrolls to the top of the content.
func (as *AcceleratedScroll) ScrollToTop() {
	as.Scroll.Offset = fyne.NewPos(as.Scroll.Offset.X, 0)
	as.Scroll.Refresh()
}

// ScrollToBottom scrolls to the bottom of the content.
func (as *AcceleratedScroll) ScrollToBottom() {
	as.Scroll.ScrollToBottom()
}

// ScrollToOffset scrolls to a specific offset position.
func (as *AcceleratedScroll) ScrollToOffset(pos fyne.Position) {
	as.Scroll.Offset = pos
	as.Scroll.Refresh()
}
