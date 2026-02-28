package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestNewRateLimiterStartsFull verifies the bucket starts at full capacity.
func TestNewRateLimiterStartsFull(t *testing.T) {
	rl := NewRateLimiter(1.0, 10.0)
	tokens := rl.GetCurrentTokens()
	if tokens < 9.9 { // Allow small float imprecision
		t.Errorf("expected ~10 tokens, got %.2f", tokens)
	}
}

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

// TestTokenRefill verifies tokens refill over time.
func TestTokenRefill(t *testing.T) {
	rl := NewRateLimiter(10.0, 10.0) // 10 tokens/sec

	// Drain all tokens
	for i := 0; i < 10; i++ {
		rl.tryAcquire()
	}

	// Wait for partial refill
	time.Sleep(200 * time.Millisecond) // Should refill ~2 tokens

	tokens := rl.GetCurrentTokens()
	if tokens < 1.5 || tokens > 3.0 {
		t.Errorf("expected ~2 tokens after 200ms at 10/sec, got %.2f", tokens)
	}
}

// TestTokenRefillCapsAtMax verifies tokens don't exceed max capacity.
func TestTokenRefillCapsAtMax(t *testing.T) {
	rl := NewRateLimiter(100.0, 5.0) // Very fast refill, low max

	// Wait a bit to accumulate
	time.Sleep(100 * time.Millisecond)

	tokens := rl.GetCurrentTokens()
	if tokens > 5.1 { // Allow tiny float imprecision
		t.Errorf("tokens should cap at 5, got %.2f", tokens)
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

// TestBurstBehavior verifies rapid consumption depletes the bucket.
func TestBurstBehavior(t *testing.T) {
	rl := NewRateLimiter(1.0, 20.0) // 1/sec refill, 20 burst capacity

	// Rapid burst — should get all 20
	for i := 0; i < 20; i++ {
		if !rl.tryAcquire() {
			t.Fatalf("burst failed at token %d", i+1)
		}
	}

	// Next should fail
	if rl.tryAcquire() {
		t.Error("should fail after burst exhaustion")
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

// TestDrainCausesWaitToBlock verifies that after Drain, Wait blocks until refill.
func TestDrainCausesWaitToBlock(t *testing.T) {
	rl := NewRateLimiter(10.0, 10.0) // 10/sec refill

	rl.Drain()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := rl.Wait(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Wait() after Drain returned error: %v", err)
	}

	// Should have waited ~100ms for 1 token at 10/sec
	if elapsed < 50*time.Millisecond {
		t.Errorf("Wait() after Drain completed too quickly: %v", elapsed)
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
