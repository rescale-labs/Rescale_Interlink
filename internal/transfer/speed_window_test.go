package transfer

import (
	"sync"
	"testing"
	"time"
)

func TestSpeedWindow_BasicSpeed(t *testing.T) {
	sw := newSpeedWindow(10 * time.Second)
	base := time.Now()

	// 1000 bytes over 2 seconds = 500 B/s
	sw.Record(base, 0)
	sw.Record(base.Add(1*time.Second), 500)
	sw.Record(base.Add(2*time.Second), 1000)

	speed := sw.Speed()
	if speed < 490 || speed > 510 {
		t.Errorf("expected ~500 B/s, got %.1f", speed)
	}
}

func TestSpeedWindow_TrimOldSamples(t *testing.T) {
	sw := newSpeedWindow(5 * time.Second)
	base := time.Now()

	// Add samples spanning 10 seconds
	for i := 0; i <= 10; i++ {
		sw.Record(base.Add(time.Duration(i)*time.Second), int64(i*100))
	}

	// After trimming, speed should be based on the ~5s window
	speed := sw.Speed()
	// ~100 bytes/sec (100 bytes per second consistently)
	if speed < 90 || speed > 110 {
		t.Errorf("expected ~100 B/s after trim, got %.1f", speed)
	}

	// Verify samples were trimmed (should be ~6-7 samples, not 11)
	sw.mu.Lock()
	n := len(sw.samples)
	sw.mu.Unlock()
	if n > 8 {
		t.Errorf("expected trimmed samples, got %d", n)
	}
}

func TestSpeedWindow_InsufficientData(t *testing.T) {
	sw := newSpeedWindow(10 * time.Second)

	// Zero samples
	if speed := sw.Speed(); speed != 0 {
		t.Errorf("expected 0 with no samples, got %.1f", speed)
	}

	// One sample
	sw.Record(time.Now(), 100)
	if speed := sw.Speed(); speed != 0 {
		t.Errorf("expected 0 with one sample, got %.1f", speed)
	}

	// Two samples too close together (<500ms)
	base := time.Now()
	sw2 := newSpeedWindow(10 * time.Second)
	sw2.Record(base, 0)
	sw2.Record(base.Add(100*time.Millisecond), 1000)
	if speed := sw2.Speed(); speed != 0 {
		t.Errorf("expected 0 with <500ms span, got %.1f", speed)
	}
}

func TestSpeedWindow_ZeroDelta(t *testing.T) {
	sw := newSpeedWindow(10 * time.Second)
	base := time.Now()

	// No bytes transferred (stall)
	sw.Record(base, 100)
	sw.Record(base.Add(2*time.Second), 100)

	if speed := sw.Speed(); speed != 0 {
		t.Errorf("expected 0 with zero delta, got %.1f", speed)
	}
}

func TestSpeedWindow_ConcurrentAccess(t *testing.T) {
	sw := newSpeedWindow(10 * time.Second)
	base := time.Now()

	var wg sync.WaitGroup
	// Concurrent writes
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sw.Record(base.Add(time.Duration(idx)*100*time.Millisecond), int64(idx*10))
		}(i)
	}
	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sw.Speed()
		}()
	}
	wg.Wait()

	// Should not panic or deadlock — speed value not checked since order is non-deterministic
}

func TestSpeedWindow_RateForFileCount(t *testing.T) {
	// Verify Speed() works for file count tracking (same math, different semantic)
	sw := newSpeedWindow(10 * time.Second)
	base := time.Now()

	// 10 files over 5 seconds = 2 files/sec
	sw.Record(base, 0)
	sw.Record(base.Add(1*time.Second), 2)
	sw.Record(base.Add(2*time.Second), 4)
	sw.Record(base.Add(3*time.Second), 6)
	sw.Record(base.Add(4*time.Second), 8)
	sw.Record(base.Add(5*time.Second), 10)

	rate := sw.Speed()
	if rate < 1.9 || rate > 2.1 {
		t.Errorf("expected ~2 files/sec, got %.2f", rate)
	}
}
