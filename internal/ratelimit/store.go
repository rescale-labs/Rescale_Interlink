package ratelimit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"sync"
	"time"
)

// CoordinatorClient is the interface that the store uses to communicate with
// the cross-process rate limit coordinator. This avoids a circular import
// between ratelimit and ratelimit/coordinator packages.
//
// The coordinator package implements this interface via its Client type.
type CoordinatorClient interface {
	// Acquire blocks until the coordinator grants a token or ctx is cancelled.
	// Returns ErrCoordinatorUnreachable on connection failure (the ONLY path to local fallback).
	Acquire(ctx context.Context, baseURL, keyHash string, scope Scope) error

	// Drain notifies the coordinator to drain the authoritative bucket (fire-and-forget).
	Drain(ctx context.Context, baseURL, keyHash string, scope Scope) error

	// SetCooldown notifies the coordinator to set a cooldown (fire-and-forget).
	SetCooldown(ctx context.Context, baseURL, keyHash string, scope Scope, d time.Duration) error

	// GetLease returns the current lease for the given bucket, or nil if none.
	GetLease(baseURL, keyHash string, scope Scope) *LeaseInfo

	// Ping checks if the coordinator is reachable.
	Ping(ctx context.Context) error
}

// LeaseInfo contains the minimal lease info needed by the store for fallback.
// This avoids importing the coordinator package's LeaseGrant type directly.
type LeaseInfo struct {
	Rate      float64
	Burst     float64
	ExpiresAt time.Time
}

// CoordinatorEnsurer is a function that attempts to connect to (or start) the coordinator.
// Returns a CoordinatorClient on success, or an error if the coordinator is unavailable.
// The store calls this lazily on first GetLimiter() and retries periodically.
type CoordinatorEnsurer func() (CoordinatorClient, error)

// LimiterStore is a process-level singleton that manages shared rate limiters.
// All api.Client instances pointing at the same Rescale account share the same
// set of rate limiters, preventing independent buckets from exceeding the
// server-side rate limit.
//
// Key structure: {apiBaseURL, hash(apiKey), scope}
//   - Same account on same platform → shared limiters (correct: same server-side quota)
//   - Different accounts → independent limiters (correct: separate quotas)
//   - Config update with same account → preserves limiter state (no fresh bucket)
//   - Config update with different account → gets new limiters
//
// Phase 2: Coordinator awareness
// When a CoordinatorEnsurer is registered, the store attempts to connect on first
// use and injects coordinator hooks into each limiter. On coordinator failure,
// limiters are created at emergency cap rates. Periodic retry (30s backoff) allows
// recovery when the coordinator becomes available.
type LimiterStore struct {
	mu       sync.Mutex
	limiters map[string]*RateLimiter
	registry *Registry

	// Coordinator state
	coordMu          sync.Mutex
	coordClient      CoordinatorClient
	coordErr         error
	coordAttempted   bool
	coordLastAttempt time.Time
	coordEnsurer     CoordinatorEnsurer
}

const coordRetryBackoff = 30 * time.Second

var (
	globalStore     *LimiterStore
	globalStoreOnce sync.Once

	// Package-level notification hook for rate limit visibility.
	globalNotifyFn func(level, message string)
	globalNotifyMu sync.Mutex
)

// SetGlobalNotifyFunc registers a package-level callback for rate limit visibility
// notifications. All new limiters created after this call will use this callback.
// This keeps the ratelimit package free of EventBus/GUI dependencies.
func SetGlobalNotifyFunc(fn func(level, message string)) {
	globalNotifyMu.Lock()
	defer globalNotifyMu.Unlock()
	globalNotifyFn = fn
}

// getGlobalNotifyFn returns the current global notification function.
func getGlobalNotifyFn() func(level, message string) {
	globalNotifyMu.Lock()
	defer globalNotifyMu.Unlock()
	return globalNotifyFn
}

// GlobalStore returns the process-level singleton LimiterStore.
// Thread-safe; initialized exactly once.
func GlobalStore() *LimiterStore {
	globalStoreOnce.Do(func() {
		globalStore = &LimiterStore{
			limiters: make(map[string]*RateLimiter),
			registry: NewRegistry(),
		}
	})
	return globalStore
}

// ResetGlobalStore replaces the global store with a fresh instance.
// Only for use in tests — never call this in production code.
func ResetGlobalStore() {
	globalStoreOnce = sync.Once{}
	globalStore = nil
}

// SetCoordinatorEnsurer registers a function that the store calls to connect
// to the cross-process rate limit coordinator. This should be called once at
// startup (e.g., from main or init code) before any API calls are made.
func (s *LimiterStore) SetCoordinatorEnsurer(fn CoordinatorEnsurer) {
	s.coordMu.Lock()
	defer s.coordMu.Unlock()
	s.coordEnsurer = fn
}

// Registry returns the endpoint-scope registry used by this store.
func (s *LimiterStore) Registry() *Registry {
	return s.registry
}

// GetLimiter returns the shared rate limiter for the given account and scope.
// If no limiter exists for this combination, one is created.
//
// With coordinator awareness (Phase 2):
//   - If coordinator is available: limiter at full target rate with coordinator hooks
//   - If coordinator is unavailable: limiter at emergency cap rate (fail-safe invariant)
//   - On subsequent calls: retries coordinator connection every 30s
//
// Parameters:
//   - baseURL: the Rescale platform URL (e.g., "https://platform.rescale.com")
//   - apiKey: the API key (hashed internally — never stored in plain text)
//   - scope: the throttle scope (e.g., ScopeUser)
func (s *LimiterStore) GetLimiter(baseURL, apiKey string, scope Scope) *RateLimiter {
	key := s.makeKey(baseURL, apiKey, scope)
	keyHash := s.hashKey(apiKey)

	s.mu.Lock()
	if limiter, ok := s.limiters[key]; ok {
		s.mu.Unlock()
		return limiter
	}
	s.mu.Unlock()

	// Attempt coordinator connection (if ensurer is configured)
	coordClient, coordEnabled := s.tryCoordinator()

	cfg := s.registry.GetScopeConfig(scope)

	var limiter *RateLimiter
	if coordClient != nil {
		// Coordinator available: create at full target rate, inject hooks
		limiter = NewRateLimiter(cfg.TargetRate, cfg.BurstCapacity)
		s.injectCoordinatorHooks(limiter, coordClient, baseURL, keyHash, scope, cfg)
	} else if coordEnabled {
		// Coordinator configured but unreachable: emergency cap rate (fail-safe invariant)
		emergencyRate, emergencyBurst := emergencyCap(cfg)
		limiter = NewRateLimiter(emergencyRate, emergencyBurst)
	} else {
		// No coordinator configured: full target rate (backwards-compatible)
		limiter = NewRateLimiter(cfg.TargetRate, cfg.BurstCapacity)
	}

	// Wire visibility: hard limit and notification callback
	limiter.SetHardLimit(cfg.HardLimitPerS)
	if fn := getGlobalNotifyFn(); fn != nil {
		limiter.SetNotifyFunc(fn)
	}

	s.mu.Lock()
	// Double-check: another goroutine might have created it
	if existing, ok := s.limiters[key]; ok {
		s.mu.Unlock()
		return existing
	}
	s.limiters[key] = limiter
	s.mu.Unlock()

	return limiter
}

// GetLimiters returns all rate limiters for a given account as a map of scope → limiter.
// Creates any missing limiters for configured scopes.
func (s *LimiterStore) GetLimiters(baseURL, apiKey string) map[Scope]*RateLimiter {
	result := make(map[Scope]*RateLimiter, len(s.registry.scopeConfigs))
	for scope := range s.registry.scopeConfigs {
		result[scope] = s.GetLimiter(baseURL, apiKey, scope)
	}
	return result
}

// tryCoordinator attempts to connect to the coordinator, with backoff.
// Returns (client, true) if coordinator is configured and connected,
// (nil, true) if configured but unreachable, or (nil, false) if not configured.
func (s *LimiterStore) tryCoordinator() (CoordinatorClient, bool) {
	s.coordMu.Lock()
	defer s.coordMu.Unlock()

	// No ensurer configured — coordinator disabled
	if s.coordEnsurer == nil {
		return nil, false
	}

	// Already connected
	if s.coordClient != nil {
		return s.coordClient, true
	}

	// Backoff: don't retry too frequently
	if s.coordAttempted && time.Since(s.coordLastAttempt) < coordRetryBackoff {
		return nil, true
	}

	// Attempt connection
	s.coordAttempted = true
	s.coordLastAttempt = time.Now()

	client, err := s.coordEnsurer()
	if err != nil {
		s.coordErr = err
		return nil, true
	}

	s.coordClient = client
	s.coordErr = nil
	return client, true
}

// injectCoordinatorHooks installs coordinator delegation hooks on a limiter.
func (s *LimiterStore) injectCoordinatorHooks(
	limiter *RateLimiter,
	client CoordinatorClient,
	baseURL, keyHash string,
	scope Scope,
	cfg ScopeConfig,
) {
	waitFn := func(ctx context.Context) error {
		start := time.Now()
		err := client.Acquire(ctx, baseURL, keyHash, scope)
		if err != nil && isUnreachable(err) {
			// Coordinator lost — reconfigure to fallback rate BEFORE returning error
			s.handleCoordinatorDisconnect(limiter, client, baseURL, keyHash, scope, cfg)
			return err
		}
		if err == nil {
			// Coordinator granted — emit visibility notice if wait was non-trivial
			elapsed := time.Since(start)
			if elapsed > 100*time.Millisecond {
				limiter.emitUtilizationNotice(elapsed)
			}
		}
		return err
	}

	drainFn := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := client.Drain(ctx, baseURL, keyHash, scope); err != nil {
			log.Printf("coordinator drain failed (local drain still applied): %v", err)
		}
	}

	cooldownFn := func(d time.Duration) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := client.SetCooldown(ctx, baseURL, keyHash, scope, d); err != nil {
			log.Printf("coordinator cooldown failed (local cooldown still applied): %v", err)
		}
	}

	limiter.SetCoordinatorHook(waitFn, drainFn, cooldownFn)
}

// handleCoordinatorDisconnect reconfigures a limiter when the coordinator becomes unreachable.
// Checks for valid lease first (leased rate), then falls back to emergency cap.
func (s *LimiterStore) handleCoordinatorDisconnect(
	limiter *RateLimiter,
	client CoordinatorClient,
	baseURL, keyHash string,
	scope Scope,
	cfg ScopeConfig,
) {
	// Check for valid lease
	lease := client.GetLease(baseURL, keyHash, scope)
	if lease != nil && time.Now().Before(lease.ExpiresAt) {
		limiter.Reconfigure(lease.Rate, lease.Burst)
		return
	}

	// No lease — emergency cap
	rate, burst := emergencyCap(cfg)
	limiter.Reconfigure(rate, burst)

	// Clear coordinator hooks so subsequent Wait() calls go straight to local
	limiter.ClearCoordinatorHook()

	// Mark coordinator as disconnected for retry
	s.coordMu.Lock()
	s.coordClient = nil
	s.coordMu.Unlock()
}

// emergencyCap computes conservative rate and burst for when the coordinator is unreachable.
// Formula: rate = (hardLimit/4) * 0.5, burst = 1
//
// Burst is set to 1 (not 0) because the token bucket requires tokens >= 1.0 to grant
// a request. With maxTokens=0, tokens would always be capped at 0 and no request could
// ever be granted. Burst=1 allows exactly one request to accumulate, enforcing strict
// rate-only limiting without allowing bursts.
func emergencyCap(cfg ScopeConfig) (rate float64, burst float64) {
	rate = (cfg.HardLimitPerS / 4.0) * 0.5
	burst = 1 // Minimum to allow token acquisition
	return
}

// isUnreachable checks if an error indicates the coordinator is unreachable.
// Uses string matching to avoid importing the coordinator package.
func isUnreachable(err error) bool {
	return err != nil && err.Error() == "coordinator unreachable"
}

// makeKey builds a map key from {baseURL, hash(apiKey), scope}.
// The API key is hashed to avoid storing credentials in memory as map keys.
func (s *LimiterStore) makeKey(baseURL, apiKey string, scope Scope) string {
	h := sha256.Sum256([]byte(apiKey))
	return fmt.Sprintf("%s|%x|%s", baseURL, h[:8], scope)
}

// hashKey returns the SHA256[:8] hex of the API key (for coordinator communication).
func (s *LimiterStore) hashKey(apiKey string) string {
	h := sha256.Sum256([]byte(apiKey))
	return fmt.Sprintf("%x", h[:8])
}
