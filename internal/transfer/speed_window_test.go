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

	// No bytes transferred (stall) — no prior non-zero speed, so no grace period
	sw.Record(base, 100)
	sw.Record(base.Add(2*time.Second), 100)

	if speed := sw.Speed(); speed != 0 {
		t.Errorf("expected 0 with zero delta and no prior speed, got %.1f", speed)
	}
}

func TestSpeedWindow_GracePeriod(t *testing.T) {
	sw := newSpeedWindow(10 * time.Second)
	base := time.Now()

	// Build up a non-zero speed: 1000 bytes over 2 seconds = 500 B/s
	sw.Record(base, 0)
	sw.Record(base.Add(1*time.Second), 500)
	sw.Record(base.Add(2*time.Second), 1000)

	speed := sw.Speed()
	if speed < 490 || speed > 510 {
		t.Fatalf("expected ~500 B/s baseline, got %.1f", speed)
	}

	// Stall: no new bytes for 1 second (within 3s grace period)
	sw.Record(base.Add(3*time.Second), 1000)
	speed = sw.Speed()
	if speed < 100 {
		t.Errorf("expected grace period to hold non-zero speed, got %.1f", speed)
	}

	// Still within grace period at 2s after last non-zero
	sw.Record(base.Add(4*time.Second), 1000)
	speed = sw.Speed()
	if speed == 0 {
		t.Error("expected non-zero speed within grace period, got 0")
	}

	// Beyond grace period (>3s after last non-zero sample at t=2s)
	// Need samples spanning enough time that the window only contains stalled data
	sw2 := newSpeedWindow(3 * time.Second) // small window
	base2 := time.Now()

	// Build non-zero speed
	sw2.Record(base2, 0)
	sw2.Record(base2.Add(1*time.Second), 500)

	speed = sw2.Speed()
	if speed < 400 || speed > 600 {
		t.Fatalf("expected ~500 B/s baseline, got %.1f", speed)
	}

	// Stall for 4+ seconds (beyond 3s grace period)
	sw2.Record(base2.Add(2*time.Second), 500)
	sw2.Record(base2.Add(3*time.Second), 500)
	sw2.Record(base2.Add(4*time.Second), 500)
	sw2.Record(base2.Add(5*time.Second), 500) // 4s after lastNonZeroTime (t=1s)

	speed = sw2.Speed()
	if speed != 0 {
		t.Errorf("expected 0 after grace period expired, got %.1f", speed)
	}
}

func TestSpeedWindow_GracePeriodResumesAfterStall(t *testing.T) {
	sw := newSpeedWindow(10 * time.Second)
	base := time.Now()

	// Build speed, stall, then resume — speed should recover
	sw.Record(base, 0)
	sw.Record(base.Add(1*time.Second), 500)
	sw.Record(base.Add(2*time.Second), 1000)

	// Stall
	sw.Record(base.Add(3*time.Second), 1000)
	speed := sw.Speed()
	if speed == 0 {
		t.Error("expected grace period speed during stall, got 0")
	}

	// Resume: new bytes arrive
	sw.Record(base.Add(4*time.Second), 1500)
	sw.Record(base.Add(5*time.Second), 2000)

	speed = sw.Speed()
	if speed < 100 {
		t.Errorf("expected recovered speed after stall, got %.1f", speed)
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
