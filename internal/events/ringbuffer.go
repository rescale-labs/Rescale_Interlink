package events

import "sync"

// RingBuffer is a fixed-capacity circular buffer of Events.
// It is safe for concurrent use. Add appends; Snapshot returns
// the contents in chronological order.
type RingBuffer struct {
	mu    sync.Mutex
	buf   []Event
	cap   int
	pos   int  // next write position
	full  bool // whether the buffer has wrapped
}

// NewRingBuffer creates a ring buffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 50
	}
	return &RingBuffer{
		buf: make([]Event, capacity),
		cap: capacity,
	}
}

// Add appends an event to the ring buffer, overwriting the oldest
// event if the buffer is full.
func (rb *RingBuffer) Add(event Event) {
	rb.mu.Lock()
	rb.buf[rb.pos] = event
	rb.pos = (rb.pos + 1) % rb.cap
	if rb.pos == 0 {
		rb.full = true
	}
	rb.mu.Unlock()
}

// Snapshot returns a chronological copy of all events in the buffer.
func (rb *RingBuffer) Snapshot() []Event {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if !rb.full {
		out := make([]Event, rb.pos)
		copy(out, rb.buf[:rb.pos])
		return out
	}

	out := make([]Event, rb.cap)
	// Oldest events start at rb.pos (the next write position wraps over the oldest)
	copy(out, rb.buf[rb.pos:])
	copy(out[rb.cap-rb.pos:], rb.buf[:rb.pos])
	return out
}
