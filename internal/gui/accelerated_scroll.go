package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	// DefaultScrollMultiplier increases scroll speed by this factor
	// Fyne default is ~12px per scroll, this makes it feel more like native GTK (~36px)
	DefaultScrollMultiplier = 3.0

	// Scroll bar constants
	scrollBarMinLength = 20
	scrollBarAreaWidth = 12
)

// AcceleratedScroll is a custom scroll container with accelerated scroll speed.
// Unlike container.Scroll, this implementation receives scroll events directly
// because it doesn't wrap another Scrollable widget.
//
// This addresses Fyne issue #775 where default scroll speed is too slow.
type AcceleratedScroll struct {
	widget.BaseWidget
	Content    fyne.CanvasObject
	Offset     fyne.Position
	Direction  fyne.ScrollDirection
	Multiplier float32
	OnScrolled func(fyne.Position)
	minSize    fyne.Size
}

// NewAcceleratedScroll creates a scroll container with accelerated scroll speed
// that scrolls in both directions.
func NewAcceleratedScroll(content fyne.CanvasObject) *AcceleratedScroll {
	as := &AcceleratedScroll{
		Content:    content,
		Direction:  fyne.ScrollBoth,
		Multiplier: DefaultScrollMultiplier,
	}
	as.ExtendBaseWidget(as)
	return as
}

// NewAcceleratedVScroll creates a vertical-only scroll container with accelerated speed.
func NewAcceleratedVScroll(content fyne.CanvasObject) *AcceleratedScroll {
	as := &AcceleratedScroll{
		Content:    content,
		Direction:  fyne.ScrollVerticalOnly,
		Multiplier: DefaultScrollMultiplier,
	}
	as.ExtendBaseWidget(as)
	return as
}

// NewAcceleratedHScroll creates a horizontal-only scroll container with accelerated speed.
func NewAcceleratedHScroll(content fyne.CanvasObject) *AcceleratedScroll {
	as := &AcceleratedScroll{
		Content:    content,
		Direction:  fyne.ScrollHorizontalOnly,
		Multiplier: DefaultScrollMultiplier,
	}
	as.ExtendBaseWidget(as)
	return as
}

// SetMultiplier changes the scroll speed multiplier.
func (as *AcceleratedScroll) SetMultiplier(m float32) {
	as.Multiplier = m
}

// SetMinSize sets the minimum size of the scroll container.
func (as *AcceleratedScroll) SetMinSize(size fyne.Size) {
	as.minSize = size
}

// MinSize returns the minimum size required for the widget.
func (as *AcceleratedScroll) MinSize() fyne.Size {
	if as.minSize.Width > 0 || as.minSize.Height > 0 {
		return as.minSize
	}
	return fyne.NewSize(32, 32) // Minimum scrollable area
}

// Scrolled handles scroll events with acceleration.
// This method receives events directly because AcceleratedScroll implements
// fyne.Scrollable and doesn't wrap another Scrollable widget.
func (as *AcceleratedScroll) Scrolled(e *fyne.ScrollEvent) {
	if as.Content == nil {
		return
	}

	// Apply multiplier to scroll delta
	dx := e.Scrolled.DX * as.Multiplier
	dy := e.Scrolled.DY * as.Multiplier

	// Respect scroll direction
	switch as.Direction {
	case fyne.ScrollVerticalOnly:
		dx = 0
	case fyne.ScrollHorizontalOnly:
		dy = 0
	case fyne.ScrollNone:
		return
	}

	as.scrollBy(-dx, -dy)
}

// scrollBy updates the offset by the given delta and refreshes.
func (as *AcceleratedScroll) scrollBy(dx, dy float32) {
	as.Offset = as.clampOffset(fyne.NewPos(as.Offset.X+dx, as.Offset.Y+dy))
	as.Refresh()

	if as.OnScrolled != nil {
		as.OnScrolled(as.Offset)
	}
}

// clampOffset ensures the offset stays within valid bounds.
func (as *AcceleratedScroll) clampOffset(pos fyne.Position) fyne.Position {
	contentSize := as.Content.MinSize()
	scrollSize := as.Size()

	maxX := contentSize.Width - scrollSize.Width
	maxY := contentSize.Height - scrollSize.Height

	x := pos.X
	y := pos.Y

	// Clamp X
	if maxX <= 0 {
		x = 0
	} else if x < 0 {
		x = 0
	} else if x > maxX {
		x = maxX
	}

	// Clamp Y
	if maxY <= 0 {
		y = 0
	} else if y < 0 {
		y = 0
	} else if y > maxY {
		y = maxY
	}

	return fyne.NewPos(x, y)
}

// ScrollToTop scrolls to the top of the content.
func (as *AcceleratedScroll) ScrollToTop() {
	as.Offset = fyne.NewPos(as.Offset.X, 0)
	as.Refresh()
	if as.OnScrolled != nil {
		as.OnScrolled(as.Offset)
	}
}

// ScrollToBottom scrolls to the bottom of the content.
func (as *AcceleratedScroll) ScrollToBottom() {
	contentSize := as.Content.MinSize()
	scrollSize := as.Size()
	maxY := contentSize.Height - scrollSize.Height
	if maxY < 0 {
		maxY = 0
	}
	as.Offset = fyne.NewPos(as.Offset.X, maxY)
	as.Refresh()
	if as.OnScrolled != nil {
		as.OnScrolled(as.Offset)
	}
}

// ScrollToOffset scrolls to a specific offset position.
func (as *AcceleratedScroll) ScrollToOffset(pos fyne.Position) {
	as.Offset = as.clampOffset(pos)
	as.Refresh()
	if as.OnScrolled != nil {
		as.OnScrolled(as.Offset)
	}
}

// CreateRenderer returns the renderer for this widget.
func (as *AcceleratedScroll) CreateRenderer() fyne.WidgetRenderer {
	as.ExtendBaseWidget(as)

	background := canvas.NewRectangle(color.Transparent)
	vBar := newScrollBarArea(as, scrollBarOrientationVertical)
	hBar := newScrollBarArea(as, scrollBarOrientationHorizontal)

	return &acceleratedScrollRenderer{
		scroll:     as,
		background: background,
		vBar:       vBar,
		hBar:       hBar,
	}
}

// Resize resizes the widget.
func (as *AcceleratedScroll) Resize(size fyne.Size) {
	as.BaseWidget.Resize(size)
	// Re-clamp offset in case viewport changed
	as.Offset = as.clampOffset(as.Offset)
}

// acceleratedScrollRenderer renders the scroll container.
type acceleratedScrollRenderer struct {
	scroll     *AcceleratedScroll
	background *canvas.Rectangle
	vBar       *scrollBarArea
	hBar       *scrollBarArea
}

func (r *acceleratedScrollRenderer) Layout(size fyne.Size) {
	if r.scroll.Content == nil {
		return
	}

	// Size content to at least its minimum size or the container size
	contentMin := r.scroll.Content.MinSize()
	contentWidth := contentMin.Width
	contentHeight := contentMin.Height

	// Content should be at least as big as the viewport in each dimension
	// unless we're in a restricted scroll direction
	switch r.scroll.Direction {
	case fyne.ScrollVerticalOnly:
		contentWidth = size.Width - scrollBarAreaWidth
	case fyne.ScrollHorizontalOnly:
		contentHeight = size.Height - scrollBarAreaWidth
	case fyne.ScrollBoth:
		// Content keeps its minimum size
	}

	if contentWidth < contentMin.Width {
		contentWidth = contentMin.Width
	}
	if contentHeight < contentMin.Height {
		contentHeight = contentMin.Height
	}

	r.scroll.Content.Resize(fyne.NewSize(contentWidth, contentHeight))

	// Position content at negative offset (creates scroll effect)
	r.scroll.Content.Move(fyne.NewPos(-r.scroll.Offset.X, -r.scroll.Offset.Y))

	// Background fills the area
	r.background.Resize(size)

	// Layout scroll bars
	r.layoutScrollBars(size, fyne.NewSize(contentWidth, contentHeight))
}

func (r *acceleratedScrollRenderer) layoutScrollBars(viewportSize, contentSize fyne.Size) {
	showVBar := r.scroll.Direction != fyne.ScrollHorizontalOnly &&
		contentSize.Height > viewportSize.Height
	showHBar := r.scroll.Direction != fyne.ScrollVerticalOnly &&
		contentSize.Width > viewportSize.Width

	barWidth := float32(scrollBarAreaWidth)

	if showVBar {
		r.vBar.Show()
		vBarHeight := viewportSize.Height
		if showHBar {
			vBarHeight -= barWidth
		}
		r.vBar.Resize(fyne.NewSize(barWidth, vBarHeight))
		r.vBar.Move(fyne.NewPos(viewportSize.Width-barWidth, 0))
		r.vBar.updateBar(r.scroll.Offset.Y, contentSize.Height, vBarHeight)
	} else {
		r.vBar.Hide()
	}

	if showHBar {
		r.hBar.Show()
		hBarWidth := viewportSize.Width
		if showVBar {
			hBarWidth -= barWidth
		}
		r.hBar.Resize(fyne.NewSize(hBarWidth, barWidth))
		r.hBar.Move(fyne.NewPos(0, viewportSize.Height-barWidth))
		r.hBar.updateBar(r.scroll.Offset.X, contentSize.Width, hBarWidth)
	} else {
		r.hBar.Hide()
	}
}

func (r *acceleratedScrollRenderer) MinSize() fyne.Size {
	return r.scroll.MinSize()
}

func (r *acceleratedScrollRenderer) Refresh() {
	if r.scroll.Content != nil {
		r.scroll.Content.Refresh()
	}
	r.vBar.Refresh()
	r.hBar.Refresh()
	canvas.Refresh(r.scroll)
}

func (r *acceleratedScrollRenderer) Objects() []fyne.CanvasObject {
	if r.scroll.Content == nil {
		return []fyne.CanvasObject{r.background, r.vBar, r.hBar}
	}
	return []fyne.CanvasObject{r.background, r.scroll.Content, r.vBar, r.hBar}
}

func (r *acceleratedScrollRenderer) Destroy() {}

// Scroll bar orientation constants
const (
	scrollBarOrientationVertical   = 0
	scrollBarOrientationHorizontal = 1
)

// scrollBarArea is the area containing the scroll bar track and thumb.
type scrollBarArea struct {
	widget.BaseWidget
	parent      *AcceleratedScroll
	orientation int
	thumbPos    float32
	thumbSize   float32
	dragging    bool
	dragStart   float32
}

func newScrollBarArea(parent *AcceleratedScroll, orientation int) *scrollBarArea {
	sba := &scrollBarArea{
		parent:      parent,
		orientation: orientation,
	}
	sba.ExtendBaseWidget(sba)
	return sba
}

func (sba *scrollBarArea) updateBar(offset, contentLength, viewportLength float32) {
	if contentLength <= viewportLength {
		sba.thumbSize = viewportLength
		sba.thumbPos = 0
		return
	}

	// Thumb size is proportional to visible portion
	sba.thumbSize = (viewportLength / contentLength) * viewportLength
	if sba.thumbSize < scrollBarMinLength {
		sba.thumbSize = scrollBarMinLength
	}

	// Thumb position maps offset to track space
	maxOffset := contentLength - viewportLength
	trackSpace := viewportLength - sba.thumbSize
	if maxOffset > 0 {
		sba.thumbPos = (offset / maxOffset) * trackSpace
	} else {
		sba.thumbPos = 0
	}
}

func (sba *scrollBarArea) CreateRenderer() fyne.WidgetRenderer {
	track := canvas.NewRectangle(theme.Color(theme.ColorNameScrollBar))
	track.SetMinSize(fyne.NewSize(scrollBarAreaWidth, scrollBarAreaWidth))

	thumb := canvas.NewRectangle(theme.Color(theme.ColorNameForeground))

	return &scrollBarAreaRenderer{
		area:  sba,
		track: track,
		thumb: thumb,
	}
}

func (sba *scrollBarArea) Dragged(e *fyne.DragEvent) {
	if !sba.dragging {
		sba.dragging = true
		if sba.orientation == scrollBarOrientationVertical {
			sba.dragStart = sba.parent.Offset.Y
		} else {
			sba.dragStart = sba.parent.Offset.X
		}
	}

	contentSize := sba.parent.Content.MinSize()
	viewportSize := sba.parent.Size()

	var contentLength, viewportLength, dragDelta float32
	if sba.orientation == scrollBarOrientationVertical {
		contentLength = contentSize.Height
		viewportLength = viewportSize.Height
		dragDelta = e.Dragged.DY
	} else {
		contentLength = contentSize.Width
		viewportLength = viewportSize.Width
		dragDelta = e.Dragged.DX
	}

	if contentLength <= viewportLength {
		return
	}

	// Convert drag delta in track space to content offset
	trackSpace := viewportLength - sba.thumbSize
	maxOffset := contentLength - viewportLength

	if trackSpace > 0 {
		offsetDelta := (dragDelta / trackSpace) * maxOffset

		if sba.orientation == scrollBarOrientationVertical {
			newOffset := sba.parent.Offset.Y + offsetDelta
			sba.parent.ScrollToOffset(fyne.NewPos(sba.parent.Offset.X, newOffset))
		} else {
			newOffset := sba.parent.Offset.X + offsetDelta
			sba.parent.ScrollToOffset(fyne.NewPos(newOffset, sba.parent.Offset.Y))
		}
	}
}

func (sba *scrollBarArea) DragEnd() {
	sba.dragging = false
}

// Implement desktop.Hoverable to show cursor changes
var _ desktop.Hoverable = (*scrollBarArea)(nil)

func (sba *scrollBarArea) MouseIn(*desktop.MouseEvent)  {}
func (sba *scrollBarArea) MouseOut()                    {}
func (sba *scrollBarArea) MouseMoved(*desktop.MouseEvent) {}

// scrollBarAreaRenderer renders the scroll bar area.
type scrollBarAreaRenderer struct {
	area  *scrollBarArea
	track *canvas.Rectangle
	thumb *canvas.Rectangle
}

func (r *scrollBarAreaRenderer) Layout(size fyne.Size) {
	r.track.Resize(size)

	if r.area.orientation == scrollBarOrientationVertical {
		thumbWidth := size.Width * 0.6
		thumbX := (size.Width - thumbWidth) / 2
		r.thumb.Resize(fyne.NewSize(thumbWidth, r.area.thumbSize))
		r.thumb.Move(fyne.NewPos(thumbX, r.area.thumbPos))
	} else {
		thumbHeight := size.Height * 0.6
		thumbY := (size.Height - thumbHeight) / 2
		r.thumb.Resize(fyne.NewSize(r.area.thumbSize, thumbHeight))
		r.thumb.Move(fyne.NewPos(r.area.thumbPos, thumbY))
	}
}

func (r *scrollBarAreaRenderer) MinSize() fyne.Size {
	return fyne.NewSize(scrollBarAreaWidth, scrollBarAreaWidth)
}

func (r *scrollBarAreaRenderer) Refresh() {
	r.track.FillColor = theme.Color(theme.ColorNameScrollBar)
	r.thumb.FillColor = theme.Color(theme.ColorNameForeground)
	r.track.Refresh()
	r.thumb.Refresh()
}

func (r *scrollBarAreaRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.track, r.thumb}
}

func (r *scrollBarAreaRenderer) Destroy() {}
