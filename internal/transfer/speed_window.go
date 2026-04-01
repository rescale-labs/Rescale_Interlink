// Package transfer provides transfer queue management for uploads and downloads.
package transfer

import (
	"sync"
	"time"
)

// speedSample records a cumulative counter value at a specific time.
type speedSample struct {
	time  time.Time
	bytes int64
}

// speedWindow computes a sliding-window rate from cumulative counter samples.
// Used for both bytes/sec (speed) and files/sec (completion rate).
// Thread-safe via mutex.
//
// Grace period: when speed transiently drops to 0 between file completions
// (no byte delta in the current window), the last non-zero speed is held for
// up to 3 seconds. This prevents speed/ETA flashing in the UI.
type speedWindow struct {
	mu              sync.Mutex
	samples         []speedSample
	windowSize      time.Duration
	lastNonZero     float64
	lastNonZeroTime time.Time
}

// newSpeedWindow creates a speed window with the specified duration.
func newSpeedWindow(windowSize time.Duration) *speedWindow {
	return &speedWindow{
		samples:    make([]speedSample, 0, 32),
		windowSize: windowSize,
	}
}

// Record appends a cumulative counter sample and trims old samples.
// The bytes value must be monotonically non-decreasing.
func (sw *speedWindow) Record(now time.Time, cumulativeBytes int64) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	sw.samples = append(sw.samples, speedSample{time: now, bytes: cumulativeBytes})

	// Trim samples older than windowSize, but keep at least one baseline
	cutoff := now.Add(-sw.windowSize)
	firstKeep := 0
	for i := 0; i < len(sw.samples)-1; i++ {
		if sw.samples[i].time.Before(cutoff) {
			firstKeep = i + 1
		} else {
			break
		}
	}
	if firstKeep > 0 {
		// Keep one sample before the cutoff as baseline
		keepFrom := firstKeep - 1
		n := copy(sw.samples, sw.samples[keepFrom:])
		sw.samples = sw.samples[:n]
	}
}

// speedGracePeriod is how long a non-zero speed is held when the computed
// speed transiently drops to 0 (e.g., between file completions).
const speedGracePeriod = 3 * time.Second

// Speed returns the current rate (units per second) based on the sliding window.
// Returns 0 if fewer than 2 samples or time span < 500ms.
//
// If the computed speed is 0 but a previous non-zero speed exists and the last
// sample is within speedGracePeriod of lastNonZeroTime, the previous non-zero
// value is returned. This prevents speed/ETA flashing between file completions.
func (sw *speedWindow) Speed() float64 {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if len(sw.samples) < 2 {
		return 0
	}

	first := sw.samples[0]
	last := sw.samples[len(sw.samples)-1]
	span := last.time.Sub(first.time).Seconds()

	if span < 0.5 {
		return 0
	}

	delta := last.bytes - first.bytes
	if delta > 0 {
		computed := float64(delta) / span
		sw.lastNonZero = computed
		sw.lastNonZeroTime = last.time
		return computed
	}

	// delta <= 0: check grace period
	if sw.lastNonZero > 0 && !sw.lastNonZeroTime.IsZero() &&
		last.time.Sub(sw.lastNonZeroTime) <= speedGracePeriod {
		return sw.lastNonZero
	}

	return 0
}
