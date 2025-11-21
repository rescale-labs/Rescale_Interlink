// Package ratelimit provides rate limiting for API calls using a token bucket algorithm.
package ratelimit

import (
	"context"
	"log"
	"sync"
	"time"
)

// RateLimiter implements a token bucket rate limiter.
// It allows bursts up to maxTokens, then refills at refillRate tokens/second.
type RateLimiter struct {
	tokens       float64   // Current number of tokens available
	maxTokens    float64   // Maximum bucket capacity
	refillRate   float64   // Tokens added per second
	lastRefill   time.Time // Last time tokens were refilled
	lastWarnTime time.Time // Last time we warned user about rate limiting
	mu           sync.Mutex
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
// Target Rate: 1.6 req/sec (80% of limit)
//   - Provides 20% safety margin to prevent hard throttle lockouts
//   - Accounts for concurrent operations and burst traffic
//   - See constants.UserScopeRatePerSec
//
// Burst Capacity: 150 tokens
//   - Allows ~93 seconds of rapid operations at startup
//   - Depletes faster with concurrent uploads/downloads, then refills at 1.6/sec
//   - See constants.UserScopeBurstCapacity
//
// Implementation: Token bucket algorithm
//   - Bucket starts with 150 tokens
//   - Each API call consumes 1 token
//   - Bucket refills at 1.6 tokens/second
//   - Maximum capacity capped at 150 tokens
//
// 2025-11-19: Changed from separate file/folder/credential limiters to single user scope limiter
// 2025-11-20: Reduced from 1.8 req/sec (90%) to 1.6 req/sec (80%) for better safety margin
func NewUserScopeRateLimiter() *RateLimiter {
	return NewRateLimiter(UserScopeRatePerSec, UserScopeBurstCapacity)
}

// NewJobSubmissionRateLimiter creates a rate limiter for job submission endpoints.
//
// Job submission endpoint (POST /api/v2/jobs/{id}/submit/) has a limit of 1000/hour = 0.278 req/sec.
//
// Target Rate: 0.139 req/sec (50% of limit)
//   - Very conservative due to low frequency of job submissions
//   - See constants.JobSubmissionRatePerSec
//
// Burst Capacity: 50 tokens
//   - Allows ~360 seconds (6 minutes) of rapid job submissions
//   - See constants.JobSubmissionBurstCapacity
//
// 2025-11-20: Updated to use constants from constants.go
func NewJobSubmissionRateLimiter() *RateLimiter {
	return NewRateLimiter(JobSubmissionRatePerSec, JobSubmissionBurstCapacity)
}

// NewJobsUsageRateLimiter creates a rate limiter for v2 job query endpoints.
//
// Scope: jobs-usage (Rescale's throttle scope for v2 job read operations)
//   - Hard Limit: 90000/hour = 25 req/sec
//   - This is 12.5x faster than the user scope (2 req/sec)
//
// Target Rate: 20 req/sec (80% of limit)
//   - 20% safety margin (same as user scope)
//   - See constants.JobsUsageRatePerSec
//
// Burst Capacity: 300 tokens
//   - Allows ~15 seconds of rapid operations
//   - See constants.JobsUsageBurstCapacity
//
// 2025-11-20: Added to support faster job file downloads via v2 endpoint
func NewJobsUsageRateLimiter() *RateLimiter {
	return NewRateLimiter(JobsUsageRatePerSec, JobsUsageBurstCapacity)
}

// Wait blocks until a token is available or context is cancelled.
// Returns an error if the context is cancelled before a token becomes available.
func (rl *RateLimiter) Wait(ctx context.Context) error {
	startTime := time.Now()

	// Try immediate acquire first
	if rl.tryAcquire() {
		return nil
	}

	// Need to wait - warn user if wait might be long
	waitTime := rl.timeUntilNextToken()
	if waitTime > 2*time.Second {
		rl.mu.Lock()
		// Only warn every 10 seconds to avoid spam
		if time.Since(rl.lastWarnTime) > 10*time.Second {
			log.Printf("⏳ Rate limited: waiting ~%.1fs for API capacity...", waitTime.Seconds())
			rl.lastWarnTime = time.Now()
		}
		rl.mu.Unlock()
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
			// Log if wait was significant
			actualWait := time.Since(startTime)
			if actualWait > 5*time.Second {
				log.Printf("⏳ Rate limit wait completed after %.1fs", actualWait.Seconds())
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
