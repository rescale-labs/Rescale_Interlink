// Package ratelimit provides rate limiting for API calls using a token bucket algorithm.
package ratelimit

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// RateLimiter implements a token bucket rate limiter.
// It allows bursts up to maxTokens, then refills at refillRate tokens/second.
//
// Thread-safe: all mutable state is protected by a sync.Mutex.
// Supports cooldown periods (triggered by 429 responses) during which all
// token acquisition blocks until the cooldown expires.
//
// Coordinator hooks: When connected to a cross-process coordinator, Wait/Drain/SetCooldown
// delegate to the coordinator first. If the coordinator is unreachable, they fall through
// to the local token bucket. See SetCoordinatorHook().
type RateLimiter struct {
	tokens       float64   // Current number of tokens available
	maxTokens    float64   // Maximum bucket capacity
	refillRate   float64   // Tokens added per second
	lastRefill   time.Time // Last time tokens were refilled
	cooldownEnd  time.Time // If set, Wait() blocks until this time (zero value = no cooldown)
	mu           sync.Mutex

	// Coordinator hooks — set via SetCoordinatorHook().
	// When non-nil, Wait/Drain/SetCooldown try the coordinator first.
	coordinatorWait     func(ctx context.Context) error
	coordinatorDrain    func()
	coordinatorCooldown func(d time.Duration)

	// Visibility: utilization-based notifications with hysteresis.
	hardLimitPerS  float64                     // Server hard limit for utilization calculation
	notifyFn       func(level, message string) // Optional visibility callback
	warningActive  bool                        // Hysteresis state: true when utilization >= warn threshold
	lastNotifyTime time.Time                   // Throttle notifications to NotifyMinInterval
}

// NewRateLimiter creates a new rate limiter.
//
// Parameters:
//   - tokensPerSecond: Rate at which tokens are added (e.g., 3.0 for 3 tokens/second)
//   - burstSize: Maximum tokens that can accumulate (allows brief bursts)
func NewRateLimiter(tokensPerSecond float64, burstSize float64) *RateLimiter {
	return &RateLimiter{
		tokens:     burstSize, // Start with full bucket
		maxTokens:  burstSize,
		refillRate: tokensPerSecond,
		lastRefill: time.Now(),
	}
}

// NewUserScopeRateLimiter creates a rate limiter for Rescale's v3 API "user" scope.
//
// All v3 API endpoints (/api/v3/*) belong to the "user" scope with a limit of 7200/hour = 2 req/sec.
//
// Target Rate: 1.7 req/sec (85% of limit)
//   - Provides 15% safety margin while maximizing throughput
//   - 429 feedback system provides safety net if server rejects requests
//   - See constants.UserScopeRatePerSec
//
// Burst Capacity: 150 tokens
//   - Allows ~88 seconds of rapid operations at startup
//   - Depletes faster with concurrent uploads/downloads, then refills at 1.7/sec
//   - See constants.UserScopeBurstCapacity
//
// Implementation: Token bucket algorithm
//   - Bucket starts with 150 tokens
//   - Each API call consumes 1 token
//   - Bucket refills at 1.7 tokens/second
//   - Maximum capacity capped at 150 tokens
//
// 2025-11-19: Changed from separate file/folder/credential limiters to single user scope limiter
// 2025-11-20: Reduced from 1.8 req/sec (90%) to 1.6 req/sec (80%) for better safety margin
// 2026-02-26: Raised to 1.7 req/sec (85%) with cross-process coordinator + 429 feedback as safety net
func NewUserScopeRateLimiter() *RateLimiter {
	return NewRateLimiter(UserScopeRatePerSec, UserScopeBurstCapacity)
}

// NewJobSubmissionRateLimiter creates a rate limiter for job submission endpoints.
//
// Job submission endpoint (POST /api/v2/jobs/{id}/submit/) has a limit of 1000/hour = 0.278 req/sec.
//
// Target Rate: 0.236 req/sec (85% of limit)
//   - With cross-process coordinator sharing the budget, 85% is safe
//   - See constants.JobSubmissionRatePerSec
//
// Burst Capacity: 50 tokens
//   - Allows ~212 seconds (~3.5 minutes) of rapid job submissions
//   - See constants.JobSubmissionBurstCapacity
//
// 2025-11-20: Updated to use constants from constants.go
// 2026-02-26: Raised from 50% to 85% with coordinator + 429 feedback as safety net
func NewJobSubmissionRateLimiter() *RateLimiter {
	return NewRateLimiter(JobSubmissionRatePerSec, JobSubmissionBurstCapacity)
}

// NewJobsUsageRateLimiter creates a rate limiter for v2 job query endpoints.
//
// Scope: jobs-usage (Rescale's throttle scope for v2 job read operations)
//   - Hard Limit: 90000/hour = 25 req/sec
//   - This is 12.5x faster than the user scope (2 req/sec)
//
// Target Rate: 21.25 req/sec (85% of limit)
//   - 15% safety margin, consistent with other scopes
//   - See constants.JobsUsageRatePerSec
//
// Burst Capacity: 300 tokens
//   - Allows ~14 seconds of rapid operations
//   - See constants.JobsUsageBurstCapacity
//
// 2025-11-20: Added to support faster job file downloads via v2 endpoint
// 2026-02-26: Raised from 80% to 85% with coordinator + 429 feedback as safety net
func NewJobsUsageRateLimiter() *RateLimiter {
	return NewRateLimiter(JobsUsageRatePerSec, JobsUsageBurstCapacity)
}

// SetCoordinatorHook installs coordinator delegation functions.
// When set, Wait/Drain/SetCooldown try the coordinator first.
// Pass nil for any hook to disable delegation for that operation.
func (rl *RateLimiter) SetCoordinatorHook(
	waitFn func(ctx context.Context) error,
	drainFn func(),
	cooldownFn func(d time.Duration),
) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.coordinatorWait = waitFn
	rl.coordinatorDrain = drainFn
	rl.coordinatorCooldown = cooldownFn
}

// ClearCoordinatorHook removes all coordinator delegation functions.
func (rl *RateLimiter) ClearCoordinatorHook() {
	rl.SetCoordinatorHook(nil, nil, nil)
}

// SetHardLimit sets the server hard limit (requests/second) for utilization calculation.
func (rl *RateLimiter) SetHardLimit(hardLimitPerS float64) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.hardLimitPerS = hardLimitPerS
}

// SetNotifyFunc sets the callback for rate limit visibility notifications.
func (rl *RateLimiter) SetNotifyFunc(fn func(level, message string)) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.notifyFn = fn
}

// Utilization returns the current utilization as a fraction (0.0–1.0).
// Utilization = refillRate / hardLimitPerS. Returns 0 if hardLimitPerS is not set.
func (rl *RateLimiter) Utilization() float64 {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.hardLimitPerS <= 0 {
		return 0
	}
	return rl.refillRate / rl.hardLimitPerS
}

// emitUtilizationNotice checks utilization thresholds with hysteresis and emits
// a notification if appropriate. Called after a non-trivial wait completes.
//
// Hysteresis logic:
//   - If utilization >= UtilizationWarnThreshold: activate warnings
//   - If utilization < UtilizationSuppressThreshold: deactivate warnings
//   - Between thresholds: maintain current state (prevents flickering)
//
// Notifications are throttled to NotifyMinInterval to prevent log spam.
func (rl *RateLimiter) emitUtilizationNotice(actualWait time.Duration) {
	rl.mu.Lock()
	fn := rl.notifyFn
	if fn == nil {
		rl.mu.Unlock()
		return
	}

	util := float64(0)
	if rl.hardLimitPerS > 0 {
		util = rl.refillRate / rl.hardLimitPerS
	}

	// Hysteresis: update warningActive state
	if util >= UtilizationWarnThreshold {
		rl.warningActive = true
	} else if util < UtilizationSuppressThreshold {
		rl.warningActive = false
	}
	// Between thresholds: maintain current state

	if !rl.warningActive {
		rl.mu.Unlock()
		return
	}

	// Throttle: don't notify more than once per NotifyMinInterval
	if !rl.lastNotifyTime.IsZero() && time.Since(rl.lastNotifyTime) < NotifyMinInterval {
		rl.mu.Unlock()
		return
	}
	rl.lastNotifyTime = time.Now()
	rl.mu.Unlock()

	// Release mutex before calling callback (same pattern as coordinator hooks)
	msg := fmt.Sprintf("Rate limiting: %.0f%% of API capacity, waited %.1fs", util*100, actualWait.Seconds())
	fn("warn", msg)
}

// TryAcquire attempts to acquire one token without blocking.
// Returns true if a token was acquired, false otherwise.
// Exported for use by the coordinator server.
func (rl *RateLimiter) TryAcquire() bool {
	return rl.tryAcquire()
}

// TimeUntilNextToken returns the estimated time until the next token is available.
// Exported for use by the coordinator server.
func (rl *RateLimiter) TimeUntilNextToken() time.Duration {
	return rl.timeUntilNextToken()
}

// Reconfigure changes the rate and burst parameters of a running limiter.
// Used to switch between full/lease/emergency rates when coordinator
// connectivity changes. If current tokens exceed the new burst, they are capped.
func (rl *RateLimiter) Reconfigure(rate, burst float64) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.refillRate = rate
	rl.maxTokens = burst
	if rl.tokens > burst {
		rl.tokens = burst
	}
}

// Wait blocks until a token is available or context is cancelled.
// Returns an error if the context is cancelled before a token becomes available.
//
// If a coordinator hook is installed, Wait tries the coordinator first.
// On coordinator success, returns immediately. On coordinator connection error,
// falls through to the local token bucket (the hook is responsible for
// reconfiguring the limiter to the appropriate fallback rate before returning error).
//
// If a cooldown is active (set via SetCooldown after a 429 response), Wait
// blocks until the cooldown expires before attempting to acquire a token.
func (rl *RateLimiter) Wait(ctx context.Context) error {
	// Try coordinator first if hook is installed
	rl.mu.Lock()
	coordWait := rl.coordinatorWait
	rl.mu.Unlock()
	if coordWait != nil {
		if err := coordWait(ctx); err == nil {
			return nil // Coordinator granted the token
		}
		// Coordinator unreachable — fall through to local bucket.
		// The hook is responsible for reconfiguring the limiter to
		// the appropriate fallback rate before returning the error.
	}

	startTime := time.Now()

	// If cooldown is active, wait for it to expire first
	if cooldown := rl.CooldownRemaining(); cooldown > 0 {
		// Cooldown always notifies regardless of utilization thresholds
		rl.mu.Lock()
		fn := rl.notifyFn
		rl.mu.Unlock()
		if fn != nil {
			fn("warn", fmt.Sprintf("Rate limited (cooldown): waiting ~%.1fs for server-requested cooldown...", cooldown.Seconds()))
		} else {
			log.Printf("Rate limited (cooldown): waiting ~%.1fs for server-requested cooldown...", cooldown.Seconds())
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cooldown):
			// Cooldown expired, fall through to normal token acquisition
		}
	}

	// Try immediate acquire first
	if rl.tryAcquire() {
		return nil
	}

	// Standard wait loop
	for {
		// Check if context is already cancelled
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Try to acquire a token
		if rl.tryAcquire() {
			// Emit utilization-based notice if wait was non-trivial
			actualWait := time.Since(startTime)
			if actualWait > 100*time.Millisecond {
				rl.emitUtilizationNotice(actualWait)
			}
			return nil
		}

		// Calculate how long to wait for next token
		waitDuration := rl.timeUntilNextToken()

		// Wait for either a token to be available or context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDuration):
			// Loop again to try acquiring
		}
	}
}

// tryAcquire attempts to acquire one token without blocking.
// Returns true if a token was acquired, false otherwise.
func (rl *RateLimiter) tryAcquire() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Refill tokens based on elapsed time
	now := time.Now()
	elapsed := now.Sub(rl.lastRefill).Seconds()
	rl.tokens += elapsed * rl.refillRate

	// Cap at max tokens (don't accumulate infinitely)
	if rl.tokens > rl.maxTokens {
		rl.tokens = rl.maxTokens
	}
	rl.lastRefill = now

	// Try to consume a token
	if rl.tokens >= 1.0 {
		rl.tokens -= 1.0
		return true
	}

	return false
}

// timeUntilNextToken calculates how long to wait until at least one token is available.
func (rl *RateLimiter) timeUntilNextToken() time.Duration {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	tokensNeeded := 1.0 - rl.tokens
	if tokensNeeded <= 0 {
		return 0
	}

	secondsNeeded := tokensNeeded / rl.refillRate
	return time.Duration(secondsNeeded * float64(time.Second))
}

// GetCurrentTokens returns the current number of tokens (for testing/debugging).
func (rl *RateLimiter) GetCurrentTokens() float64 {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Refill based on elapsed time before returning
	now := time.Now()
	elapsed := now.Sub(rl.lastRefill).Seconds()
	tokens := rl.tokens + (elapsed * rl.refillRate)

	if tokens > rl.maxTokens {
		tokens = rl.maxTokens
	}

	return tokens
}

// Drain empties the token bucket to zero. Subsequent Wait() calls will block
// until tokens refill. Used when a 429 response is received to immediately
// halt further requests on this scope.
//
// If a coordinator hook is installed, Drain also notifies the coordinator
// to drain the authoritative bucket (fire-and-forget). The local bucket
// is always drained regardless.
func (rl *RateLimiter) Drain() {
	// Notify coordinator (fire-and-forget)
	rl.mu.Lock()
	coordDrain := rl.coordinatorDrain
	rl.mu.Unlock()
	if coordDrain != nil {
		coordDrain()
	}

	// Always drain local bucket too
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.tokens = 0
	rl.lastRefill = time.Now()
}

// SetCooldown sets a cooldown period during which all Wait() calls block.
// Uses merge semantics: if an existing cooldown extends further into the future,
// it is preserved (a shorter Retry-After cannot shorten an active cooldown).
//
// If a coordinator hook is installed, SetCooldown also notifies the coordinator
// to set the cooldown on the authoritative bucket (fire-and-forget). The local
// cooldown is always set regardless.
//
// This prevents the following scenario:
//   - Server returns 429 with Retry-After: 60
//   - Cooldown set to now+60s
//   - Retry hits another 429 with Retry-After: 5
//   - Without merge: cooldown shortened to now+5s (wrong — server still enforcing 60s)
//   - With merge: cooldown stays at original now+60s (correct)
func (rl *RateLimiter) SetCooldown(d time.Duration) {
	// Notify coordinator (fire-and-forget)
	rl.mu.Lock()
	coordCooldown := rl.coordinatorCooldown
	rl.mu.Unlock()
	if coordCooldown != nil {
		coordCooldown(d)
	}

	// Always set local cooldown too
	rl.mu.Lock()
	defer rl.mu.Unlock()

	newEnd := time.Now().Add(d)
	// Merge: only extend, never shorten
	if newEnd.After(rl.cooldownEnd) {
		rl.cooldownEnd = newEnd
	}
}

// CooldownRemaining returns the time remaining on the active cooldown.
// Returns 0 if no cooldown is active.
func (rl *RateLimiter) CooldownRemaining() time.Duration {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.cooldownEnd.IsZero() {
		return 0
	}
	remaining := time.Until(rl.cooldownEnd)
	if remaining <= 0 {
		return 0
	}
	return remaining
}
