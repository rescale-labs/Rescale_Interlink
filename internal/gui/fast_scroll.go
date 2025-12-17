package gui

import (
	"log"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// Debug flag - set to true to verify FastScroll events are being intercepted
var fastScrollDebug = true

const (
	// DefaultScrollMultiplier increases scroll speed by this factor
	// Fyne default is ~12px per scroll, this makes it feel more like native GTK (~36px)
	DefaultScrollMultiplier = 3.0
)

// FastScroll wraps a container.Scroll with accelerated scroll speed
// This addresses the slow scroll issue on some platforms (Fyne issue #775)
//
// IMPORTANT: This implementation may not intercept scroll events as expected.
// Fyne routes scroll events to the deepest Scrollable widget, and since
// container.Scroll is returned by the renderer, events may bypass FastScroll.Scrolled().
// Set fastScrollDebug=true and test manually to verify events are intercepted.
// If not working, alternatives include:
// 1. Window-level event interception
// 2. Custom scroll implementation without container.Scroll
// 3. Timer-based scroll smoothing
type FastScroll struct {
	widget.BaseWidget
	Scroll     *container.Scroll
	Multiplier float32
	// Callback when scroll position changes
	OnScrolled func(offset fyne.Position)
}

// NewFastScroll creates a new scroll container with accelerated scroll speed
func NewFastScroll(content fyne.CanvasObject) *FastScroll {
	scroll := container.NewScroll(content)
	fs := &FastScroll{
		Scroll:     scroll,
		Multiplier: DefaultScrollMultiplier,
	}
	fs.ExtendBaseWidget(fs)

	// Set up callback to intercept scroll changes
	scroll.OnScrolled = func(offset fyne.Position) {
		if fs.OnScrolled != nil {
			fs.OnScrolled(offset)
		}
	}

	return fs
}

// NewFastVScroll creates a vertical-only scroll with accelerated speed
func NewFastVScroll(content fyne.CanvasObject) *FastScroll {
	scroll := container.NewVScroll(content)
	fs := &FastScroll{
		Scroll:     scroll,
		Multiplier: DefaultScrollMultiplier,
	}
	fs.ExtendBaseWidget(fs)

	scroll.OnScrolled = func(offset fyne.Position) {
		if fs.OnScrolled != nil {
			fs.OnScrolled(offset)
		}
	}

	return fs
}

// NewFastHScroll creates a horizontal-only scroll with accelerated speed
func NewFastHScroll(content fyne.CanvasObject) *FastScroll {
	scroll := container.NewHScroll(content)
	fs := &FastScroll{
		Scroll:     scroll,
		Multiplier: DefaultScrollMultiplier,
	}
	fs.ExtendBaseWidget(fs)

	scroll.OnScrolled = func(offset fyne.Position) {
		if fs.OnScrolled != nil {
			fs.OnScrolled(offset)
		}
	}

	return fs
}

// SetMultiplier changes the scroll speed multiplier
func (fs *FastScroll) SetMultiplier(m float32) {
	fs.Multiplier = m
}

// SetMinSize sets the minimum size of the scroll container
func (fs *FastScroll) SetMinSize(size fyne.Size) {
	fs.Scroll.SetMinSize(size)
}

// Scrolled handles scroll events with acceleration
func (fs *FastScroll) Scrolled(e *fyne.ScrollEvent) {
	if fastScrollDebug {
		log.Printf("FastScroll.Scrolled called: dx=%f, dy=%f, multiplier=%f", e.Scrolled.DX, e.Scrolled.DY, fs.Multiplier)
	}

	// Multiply the scroll delta by our multiplier for faster scrolling
	acceleratedDX := e.Scrolled.DX * fs.Multiplier
	acceleratedDY := e.Scrolled.DY * fs.Multiplier

	// Apply the accelerated scroll to the underlying container
	newOffset := fyne.NewPos(
		fs.Scroll.Offset.X-acceleratedDX,
		fs.Scroll.Offset.Y-acceleratedDY,
	)

	// Clamp to valid range
	contentSize := fs.Scroll.Content.Size()
	scrollSize := fs.Scroll.Size()

	maxX := contentSize.Width - scrollSize.Width
	maxY := contentSize.Height - scrollSize.Height

	if newOffset.X < 0 {
		newOffset.X = 0
	} else if maxX > 0 && newOffset.X > maxX {
		newOffset.X = maxX
	}

	if newOffset.Y < 0 {
		newOffset.Y = 0
	} else if maxY > 0 && newOffset.Y > maxY {
		newOffset.Y = maxY
	}

	fs.Scroll.Offset = newOffset
	fs.Scroll.Refresh()
}

// CreateRenderer returns the renderer for this widget
func (fs *FastScroll) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(fs.Scroll)
}

// MinSize returns the minimum size required for the widget
func (fs *FastScroll) MinSize() fyne.Size {
	return fs.Scroll.MinSize()
}

// Resize resizes the widget
func (fs *FastScroll) Resize(size fyne.Size) {
	fs.BaseWidget.Resize(size)
	fs.Scroll.Resize(size)
}

// Move moves the widget
func (fs *FastScroll) Move(pos fyne.Position) {
	fs.BaseWidget.Move(pos)
	fs.Scroll.Move(pos)
}

// ScrollToTop scrolls to the top of the content
func (fs *FastScroll) ScrollToTop() {
	fs.Scroll.ScrollToTop()
}

// ScrollToBottom scrolls to the bottom of the content
func (fs *FastScroll) ScrollToBottom() {
	fs.Scroll.ScrollToBottom()
}

// Refresh refreshes the widget
func (fs *FastScroll) Refresh() {
	fs.Scroll.Refresh()
	fs.BaseWidget.Refresh()
}
