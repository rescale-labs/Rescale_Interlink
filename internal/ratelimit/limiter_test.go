package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestTryAcquireConsumesToken verifies token consumption.
func TestTryAcquireConsumesToken(t *testing.T) {
	rl := NewRateLimiter(1.0, 5.0)

	// Should succeed 5 times (burst capacity)
	for i := 0; i < 5; i++ {
		if !rl.tryAcquire() {
			t.Fatalf("tryAcquire() failed on attempt %d", i+1)
		}
	}

	// 6th should fail (bucket exhausted, no time for refill)
	if rl.tryAcquire() {
		t.Error("tryAcquire() should fail when bucket is empty")
	}
}

// TestTokenBucketRefill pins the refill arithmetic — tokens grow by
// elapsed*refillRate and never exceed the burst — at the three points that
// behave differently: a fresh bucket (starts full), a drained bucket part-way
// through a refill, and a bucket whose refill would overshoot the burst. All
// three buckets share one sleep, so the arithmetic is checked against a single
// elapsed interval.
func TestTokenBucketRefill(t *testing.T) {
	const elapsed = 200 * time.Millisecond

	cases := []struct {
		name    string
		rate    float64
		burst   float64
		drain   int
		wantMin float64
		wantMax float64
	}{
		{"starts full", 1.0, 10.0, 0, 9.9, 10.0},
		{"refills at rate", 10.0, 10.0, 10, 1.5, 3.0},
		{"caps at burst", 100.0, 5.0, 0, 4.9, 5.1},
	}

	limiters := make([]*RateLimiter, len(cases))
	for i, c := range cases {
		rl := NewRateLimiter(c.rate, c.burst)
		for j := 0; j < c.drain; j++ {
			rl.tryAcquire()
		}
		limiters[i] = rl
	}

	time.Sleep(elapsed)

	for i, c := range cases {
		got := limiters[i].GetCurrentTokens()
		if got < c.wantMin || got > c.wantMax {
			t.Errorf("%s: tokens after %v at %.1f/s (burst %.1f) = %.2f, want [%.2f, %.2f]",
				c.name, elapsed, c.rate, c.burst, got, c.wantMin, c.wantMax)
		}
	}
}

// TestWaitBlocksUntilTokenAvailable verifies Wait blocks and then succeeds.
func TestWaitBlocksUntilTokenAvailable(t *testing.T) {
	rl := NewRateLimiter(10.0, 1.0) // 10 tokens/sec, 1 max

	// Consume the only token
	rl.tryAcquire()

	// Wait should block briefly then succeed
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := rl.Wait(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Wait() returned error: %v", err)
	}

	// Should have waited ~100ms (1 token / 10 tokens/sec)
	if elapsed < 50*time.Millisecond || elapsed > 300*time.Millisecond {
		t.Errorf("Wait() took %v, expected ~100ms", elapsed)
	}
}

// TestWaitRespectsContextCancellation verifies Wait returns on context cancel.
func TestWaitRespectsContextCancellation(t *testing.T) {
	rl := NewRateLimiter(0.1, 1.0) // Very slow refill

	// Consume the only token
	rl.tryAcquire()

	// Cancel context quickly
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := rl.Wait(ctx)
	if err == nil {
		t.Error("Wait() should return error when context is cancelled")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("Wait() error = %v, want context.DeadlineExceeded", err)
	}
}

// TestDrainEmptiesBucket verifies Drain sets tokens to zero.
func TestDrainEmptiesBucket(t *testing.T) {
	rl := NewRateLimiter(1.0, 100.0)

	// Verify bucket starts full
	if tokens := rl.GetCurrentTokens(); tokens < 99.0 {
		t.Fatalf("expected ~100 tokens at start, got %.2f", tokens)
	}

	rl.Drain()

	// Should be zero (allow tiny refill from time between Drain and GetCurrentTokens)
	tokens := rl.GetCurrentTokens()
	if tokens > 0.1 {
		t.Errorf("after Drain: tokens = %.2f, want ~0", tokens)
	}
}

// TestSetCooldown verifies cooldown blocks Wait.
func TestSetCooldown(t *testing.T) {
	rl := NewRateLimiter(100.0, 100.0) // Very fast, plenty of tokens

	// Set a short cooldown
	rl.SetCooldown(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	start := time.Now()
	err := rl.Wait(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Wait() during cooldown returned error: %v", err)
	}

	// Should have waited ~200ms for cooldown
	if elapsed < 150*time.Millisecond || elapsed > 400*time.Millisecond {
		t.Errorf("Wait() during cooldown took %v, expected ~200ms", elapsed)
	}
}

// TestCooldownMergeDoesNotShorten verifies merge semantics.
func TestCooldownMergeDoesNotShorten(t *testing.T) {
	rl := NewRateLimiter(100.0, 100.0)

	// Set a 500ms cooldown
	rl.SetCooldown(500 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	// Try to shorten with a 100ms cooldown — should be ignored
	rl.SetCooldown(100 * time.Millisecond)

	remaining := rl.CooldownRemaining()
	// Should still have ~400-450ms remaining, not ~100ms
	if remaining < 350*time.Millisecond {
		t.Errorf("cooldown shortened to %v, should still be ~400ms+", remaining)
	}
}

// TestCooldownMergeExtends verifies longer cooldowns extend.
func TestCooldownMergeExtends(t *testing.T) {
	rl := NewRateLimiter(100.0, 100.0)

	// Set a 200ms cooldown
	rl.SetCooldown(200 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	// Extend with a 1s cooldown
	rl.SetCooldown(1 * time.Second)

	remaining := rl.CooldownRemaining()
	if remaining < 800*time.Millisecond {
		t.Errorf("cooldown should have extended to ~1s, but remaining = %v", remaining)
	}
}

// TestCooldownRemaining returns zero when no cooldown active.
func TestCooldownRemainingNoCooldown(t *testing.T) {
	rl := NewRateLimiter(1.0, 1.0)

	if d := rl.CooldownRemaining(); d != 0 {
		t.Errorf("CooldownRemaining() = %v, want 0", d)
	}
}

// TestCooldownExpires verifies cooldown eventually reaches zero.
func TestCooldownExpires(t *testing.T) {
	rl := NewRateLimiter(1.0, 1.0)

	rl.SetCooldown(100 * time.Millisecond)
	time.Sleep(150 * time.Millisecond)

	if d := rl.CooldownRemaining(); d != 0 {
		t.Errorf("CooldownRemaining() = %v, want 0 (cooldown should have expired)", d)
	}
}

// TestConcurrentAccess verifies thread safety under contention.
func TestConcurrentAccess(t *testing.T) {
	rl := NewRateLimiter(100.0, 50.0) // Fast refill

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Launch 20 goroutines all trying to acquire tokens
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if err := rl.Wait(ctx); err != nil {
					return // Context cancelled, that's fine
				}
			}
		}()
	}

	wg.Wait()
	// If we get here without deadlock or panic, the test passes
}

// TestConcurrentDrainAndWait verifies no race between Drain and Wait.
func TestConcurrentDrainAndWait(t *testing.T) {
	rl := NewRateLimiter(100.0, 100.0)

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Waiters
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := rl.Wait(ctx); err != nil {
					return
				}
			}
		}()
	}

	// Drainer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			rl.Drain()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	wg.Wait()
}

// --- Phase 2: Coordinator hooks and Reconfigure tests ---

// TestCoordinatorHookWaitDelegation verifies Wait delegates to coordinator hook.
func TestCoordinatorHookWaitDelegation(t *testing.T) {
	rl := NewRateLimiter(0.01, 0) // Very slow limiter, empty bucket

	called := false
	rl.SetCoordinatorHook(
		func(ctx context.Context) error {
			called = true
			return nil // Grant immediately
		},
		nil, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := rl.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() returned error: %v", err)
	}
	if !called {
		t.Error("coordinator wait hook was not called")
	}
}

// TestCoordinatorHookWaitFallbackOnError verifies Wait falls through to local on hook error.
func TestCoordinatorHookWaitFallbackOnError(t *testing.T) {
	rl := NewRateLimiter(100.0, 10.0) // Fast local limiter

	rl.SetCoordinatorHook(
		func(ctx context.Context) error {
			return fmt.Errorf("coordinator unreachable")
		},
		nil, nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Should fall through to local bucket (which has tokens)
	err := rl.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() should succeed via local fallback, got: %v", err)
	}
}

// TestCoordinatorHookDrainDelegation verifies Drain calls coordinator hook.
func TestCoordinatorHookDrainDelegation(t *testing.T) {
	rl := NewRateLimiter(1.0, 100.0)

	called := false
	rl.SetCoordinatorHook(
		nil,
		func() { called = true },
		nil,
	)

	rl.Drain()

	if !called {
		t.Error("coordinator drain hook was not called")
	}
	// Local bucket should also be drained
	if tokens := rl.GetCurrentTokens(); tokens > 0.1 {
		t.Errorf("local bucket should be drained, got %.2f tokens", tokens)
	}
}

// TestCoordinatorHookCooldownDelegation verifies SetCooldown calls coordinator hook.
func TestCoordinatorHookCooldownDelegation(t *testing.T) {
	rl := NewRateLimiter(1.0, 100.0)

	var hookDuration time.Duration
	rl.SetCoordinatorHook(
		nil, nil,
		func(d time.Duration) { hookDuration = d },
	)

	rl.SetCooldown(30 * time.Second)

	if hookDuration != 30*time.Second {
		t.Errorf("coordinator cooldown hook received %v, want 30s", hookDuration)
	}
	// Local cooldown should also be set
	if remaining := rl.CooldownRemaining(); remaining < 25*time.Second {
		t.Errorf("local cooldown should be ~30s, got %v", remaining)
	}
}

// TestClearCoordinatorHook verifies ClearCoordinatorHook removes hooks.
func TestClearCoordinatorHook(t *testing.T) {
	rl := NewRateLimiter(100.0, 10.0)

	called := false
	rl.SetCoordinatorHook(
		func(ctx context.Context) error {
			called = true
			return nil
		},
		nil, nil,
	)

	rl.ClearCoordinatorHook()

	ctx := context.Background()
	rl.Wait(ctx)

	if called {
		t.Error("coordinator hook should not be called after ClearCoordinatorHook")
	}
}

// TestReconfigure verifies rate and burst can be changed at runtime.
func TestReconfigure(t *testing.T) {
	rl := NewRateLimiter(1.0, 100.0)

	// Reconfigure to emergency rate
	rl.Reconfigure(0.25, 0)

	// Tokens should be capped at new burst (0)
	tokens := rl.GetCurrentTokens()
	if tokens > 0.1 {
		t.Errorf("after Reconfigure(0.25, 0): tokens = %.2f, want ~0", tokens)
	}

	// TryAcquire should fail (no burst, need to wait for refill)
	if rl.TryAcquire() {
		t.Error("TryAcquire should fail with burst=0 and no refill time")
	}
}

// TestReconfigurePreservesTokensWhenPossible verifies tokens are preserved if under new burst.
func TestReconfigurePreservesTokensWhenPossible(t *testing.T) {
	rl := NewRateLimiter(1.0, 100.0)

	// Consume 95 tokens (leaves ~5)
	for i := 0; i < 95; i++ {
		rl.tryAcquire()
	}

	// Reconfigure to a higher burst — tokens should be preserved
	rl.Reconfigure(2.0, 200.0)

	tokens := rl.GetCurrentTokens()
	if tokens < 3.0 || tokens > 7.0 {
		t.Errorf("after Reconfigure with higher burst: tokens = %.2f, want ~5", tokens)
	}
}

// TestTryAcquireExported verifies the exported TryAcquire works the same as tryAcquire.
func TestTryAcquireExported(t *testing.T) {
	rl := NewRateLimiter(1.0, 3.0)

	// Should succeed 3 times
	for i := 0; i < 3; i++ {
		if !rl.TryAcquire() {
			t.Fatalf("TryAcquire() failed on attempt %d", i+1)
		}
	}

	// 4th should fail
	if rl.TryAcquire() {
		t.Error("TryAcquire() should fail when bucket is empty")
	}
}

// TestTimeUntilNextTokenExported verifies the exported method.
func TestTimeUntilNextTokenExported(t *testing.T) {
	rl := NewRateLimiter(10.0, 1.0) // 10/sec, 1 burst

	// Drain the token
	rl.TryAcquire()

	d := rl.TimeUntilNextToken()
	// Should be ~100ms (1 token / 10 tokens per sec)
	if d < 50*time.Millisecond || d > 200*time.Millisecond {
		t.Errorf("TimeUntilNextToken() = %v, want ~100ms", d)
	}
}

// --- Visibility: Utilization-based notification tests ---

// TestUtilizationCalculation verifies the utilization metric.
func TestUtilizationCalculation(t *testing.T) {
	rl := NewRateLimiter(1.7, 150)
	rl.SetHardLimit(2.0)

	util := rl.Utilization()
	if util < 0.84 || util > 0.86 {
		t.Errorf("Utilization() = %.4f, want ~0.85", util)
	}
}

// TestUtilizationZeroWithoutHardLimit verifies Utilization returns 0 when hardLimit is not set.
func TestUtilizationZeroWithoutHardLimit(t *testing.T) {
	rl := NewRateLimiter(1.7, 150)

	util := rl.Utilization()
	if util != 0 {
		t.Errorf("Utilization() = %v, want 0 when hardLimitPerS is unset", util)
	}
}

// TestNotifyCallbackFires verifies callback fires when utilization is above warn threshold.
func TestNotifyCallbackFires(t *testing.T) {
	// 85% utilization (above 60% warn threshold)
	rl := NewRateLimiter(1.7, 1.0)
	rl.SetHardLimit(2.0)

	var called bool
	var gotLevel, gotMsg string
	rl.SetNotifyFunc(func(level, message string) {
		called = true
		gotLevel = level
		gotMsg = message
	})

	// Drain bucket and wait for token (forces a wait)
	rl.tryAcquire()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("Wait() error: %v", err)
	}

	if !called {
		t.Error("notify callback was not called at 85% utilization")
	}
	if gotLevel != "warn" {
		t.Errorf("level = %q, want %q", gotLevel, "warn")
	}
	if gotMsg == "" {
		t.Error("message was empty")
	}
}

// TestNotifyCallbackSilentBelowThreshold verifies no callback at low utilization.
func TestNotifyCallbackSilentBelowThreshold(t *testing.T) {
	// 12.5% utilization (below 50% suppress threshold)
	rl := NewRateLimiter(0.25, 1.0) // emergency-like rate
	rl.SetHardLimit(2.0)            // 0.25/2.0 = 12.5%

	var called bool
	rl.SetNotifyFunc(func(level, message string) {
		called = true
	})

	// Drain bucket and wait
	rl.tryAcquire()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("Wait() error: %v", err)
	}

	if called {
		t.Error("notify callback should NOT fire at 12.5% utilization")
	}
}

// TestHysteresisActivation verifies warn→maintain→suppress transitions.
func TestHysteresisActivation(t *testing.T) {
	rl := NewRateLimiter(1.4, 1.0) // 70% utilization (above 60% → warn)
	rl.SetHardLimit(2.0)

	callCount := 0
	rl.SetNotifyFunc(func(level, message string) {
		callCount++
	})

	// Force emission at 70%: should activate warning
	rl.emitUtilizationNotice(500 * time.Millisecond)
	if callCount != 1 {
		t.Errorf("at 70%%: callCount = %d, want 1", callCount)
	}

	// Reconfigure to 55% (between thresholds — should maintain warning state)
	rl.Reconfigure(1.1, 1.0) // 1.1/2.0 = 55%

	// Need to reset throttle timer for next emission
	rl.mu.Lock()
	rl.lastNotifyTime = time.Time{}
	rl.mu.Unlock()

	rl.emitUtilizationNotice(500 * time.Millisecond)
	if callCount != 2 {
		t.Errorf("at 55%% (hysteresis still active): callCount = %d, want 2", callCount)
	}

	// Reconfigure to 45% (below suppress threshold — should deactivate)
	rl.Reconfigure(0.9, 1.0) // 0.9/2.0 = 45%

	rl.mu.Lock()
	rl.lastNotifyTime = time.Time{}
	rl.mu.Unlock()

	rl.emitUtilizationNotice(500 * time.Millisecond)
	if callCount != 2 {
		t.Errorf("at 45%% (suppressed): callCount = %d, want 2 (no new call)", callCount)
	}
}

// TestNotifyThrottling verifies max 1 notification per NotifyMinInterval.
func TestNotifyThrottling(t *testing.T) {
	rl := NewRateLimiter(1.7, 1.0) // 85% utilization
	rl.SetHardLimit(2.0)

	callCount := 0
	rl.SetNotifyFunc(func(level, message string) {
		callCount++
	})

	// First call should fire
	rl.emitUtilizationNotice(500 * time.Millisecond)
	// Rapid second call should be throttled
	rl.emitUtilizationNotice(500 * time.Millisecond)
	rl.emitUtilizationNotice(500 * time.Millisecond)

	if callCount != 1 {
		t.Errorf("callCount = %d, want 1 (should be throttled)", callCount)
	}
}

// TestCooldownNotification verifies cooldown always notifies regardless of utilization.
func TestCooldownNotification(t *testing.T) {
	rl := NewRateLimiter(0.25, 100.0) // Low utilization (12.5%)
	rl.SetHardLimit(2.0)

	var called bool
	rl.SetNotifyFunc(func(level, message string) {
		called = true
	})

	// Set a short cooldown and trigger Wait
	rl.SetCooldown(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("Wait() error: %v", err)
	}

	if !called {
		t.Error("cooldown notification should fire regardless of utilization level")
	}
}

// TestNotifyFuncNilSafe verifies no panic when notifyFn is nil.
func TestNotifyFuncNilSafe(t *testing.T) {
	rl := NewRateLimiter(1.7, 1.0)
	rl.SetHardLimit(2.0)
	// notifyFn deliberately not set

	// Should not panic
	rl.emitUtilizationNotice(500 * time.Millisecond)
}

// TestConcurrentCoordinatorHooks verifies no race with hooks and Wait/Drain/SetCooldown.
func TestConcurrentCoordinatorHooks(t *testing.T) {
	rl := NewRateLimiter(100.0, 50.0)

	rl.SetCoordinatorHook(
		func(ctx context.Context) error { return nil },
		func() {},
		func(d time.Duration) {},
	)

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Concurrent Wait calls
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := rl.Wait(ctx); err != nil {
					return
				}
			}
		}()
	}

	// Concurrent Drain calls
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			rl.Drain()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Concurrent SetCooldown calls
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			rl.SetCooldown(10 * time.Millisecond)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	wg.Wait()
}

// TestWaitFIFOOrder verifies that Wait() grants tokens in FIFO order under contention.
// Drain the bucket, launch N goroutines with stagger, verify acquisition order.
func TestWaitFIFOOrder(t *testing.T) {
	// Slow refill (1 token/sec) with 1-token bucket — forces sequential acquisition
	rl := NewRateLimiter(1.0, 1.0)

	// Drain the bucket
	ctx := context.Background()
	_ = rl.Wait(ctx) // consumes the 1 starting token

	const N = 5
	results := make(chan int, N)
	var wg sync.WaitGroup

	// Launch N goroutines with stagger to establish a deterministic queue order
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := rl.Wait(ctx)
			if err != nil {
				t.Errorf("goroutine %d: Wait returned error: %v", i, err)
				return
			}
			results <- i
		}()
		// 50ms stagger between launches ensures queue ordering
		time.Sleep(50 * time.Millisecond)
	}

	wg.Wait()
	close(results)

	// Verify FIFO order
	expected := 0
	for got := range results {
		if got != expected {
			t.Errorf("expected goroutine %d to acquire next, got %d", expected, got)
		}
		expected++
	}
	if expected != N {
		t.Errorf("expected %d results, got %d", N, expected)
	}
}

// TestQueuedWaitersHonorLateCooldown verifies that a 429 cooldown arriving after
// callers are already queued still blocks them. Wait() checks the cooldown on
// entry only, so the guarantee has to come from token acquisition itself.
func TestQueuedWaitersHonorLateCooldown(t *testing.T) {
	// 10 tokens/sec with a 1-token bucket: the next token is 100ms out, so the
	// cooldown below lands well before any waiter could have been granted.
	rl := NewRateLimiter(10.0, 1.0)
	rl.Drain()

	const (
		waiters  = 3
		cooldown = 250 * time.Millisecond
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acquired := make(chan time.Duration, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			if err := rl.Wait(ctx); err != nil {
				t.Errorf("Wait() error: %v", err)
				return
			}
			acquired <- time.Since(start)
		}()
	}

	// Let the waiters queue, then impose the cooldown behind their backs.
	time.Sleep(25 * time.Millisecond)
	rl.SetCooldown(cooldown)

	wg.Wait()
	close(acquired)

	// Each waiter started before the cooldown was set, so its own elapsed time
	// must still cover the full cooldown. Without the fix they land at ~100ms,
	// when the first token refills.
	minAllowed := cooldown - 20*time.Millisecond // timer granularity tolerance
	count := 0
	for elapsed := range acquired {
		count++
		if elapsed < minAllowed {
			t.Errorf("waiter acquired after %v — inside the %v cooldown", elapsed, cooldown)
		}
	}
	if count != waiters {
		t.Fatalf("expected %d waiters to acquire, got %d", waiters, count)
	}
}

// TestDegradedNoticesAreOrdered verifies that a transition cannot deliver its
// notice while an earlier one is still being delivered. Without the ordering,
// the recovery notice below would jump ahead of the degrade notice that is
// blocked in its callback, leaving the user's last message contradicting state.
func TestDegradedNoticesAreOrdered(t *testing.T) {
	rl := NewRateLimiter(1.0, 1.0)

	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var delivered []string

	var once sync.Once
	rl.SetNotifyFunc(func(_, message string) {
		mu.Lock()
		delivered = append(delivered, message)
		mu.Unlock()

		// Only the first notice blocks; it holds the delivery slot while the
		// second transition happens behind it.
		once.Do(func() {
			close(entered)
			<-release
		})
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rl.setDegraded(true, "degraded")
	}()

	<-entered // The degrade notice is now mid-delivery.

	wg.Add(1)
	go func() {
		defer wg.Done()
		rl.setDegraded(false, "recovered")
	}()

	// Give the recovery every chance to deliver early. It flips the flag under
	// mu, then has to wait for the delivery slot.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	count := len(delivered)
	mu.Unlock()
	if count != 1 {
		t.Errorf("%d notices delivered while the first was still in flight, want 1: %v", count, delivered)
	}

	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	want := []string{"degraded", "recovered"}
	if len(delivered) != len(want) {
		t.Fatalf("delivered %v, want %v", delivered, want)
	}
	for i := range want {
		if delivered[i] != want[i] {
			t.Errorf("notice %d = %q, want %q (delivery order must match transition order)", i, delivered[i], want[i])
		}
	}
}

// A slow degraded-notice subscriber must never block the token bucket: the
// notice mutex is claimed only after the limiter's own lock is released.
func TestSlowNoticeDoesNotBlockBucket(t *testing.T) {
	rl := NewRateLimiter(100.0, 10.0)
	release := make(chan struct{})
	entered := make(chan struct{})
	rl.SetNotifyFunc(func(level, message string) {
		close(entered)
		<-release
	})

	go rl.setDegraded(true, "degraded")
	<-entered // the slow callback now holds the delivery slot

	done := make(chan struct{})
	go func() {
		rl.IsDegraded()
		rl.TryAcquire()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("token bucket blocked while a notice callback was in flight")
	}
	close(release)
}
