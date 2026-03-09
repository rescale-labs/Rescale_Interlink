package ratelimit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/platform"
)

// limiterEntry wraps a RateLimiter with recovery metadata needed to restore it
// after a coordinator disconnect. The store needs the original baseURL, keyHash,
// and scope to re-inject coordinator hooks during recovery.
// v4.8.4: Added for coordinator self-healing.
type limiterEntry struct {
	limiter *RateLimiter
	baseURL string
	keyHash string
	scope   Scope
}

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
	limiters map[string]*limiterEntry // v4.8.4: changed from *RateLimiter for recovery metadata
	registry *Registry

	// Coordinator state
	coordMu          sync.Mutex
	coordClient      CoordinatorClient
	coordErr         error
	coordAttempted   bool
	coordLastAttempt time.Time
	coordEnsurer     CoordinatorEnsurer

	// v4.8.4: Self-healing state
	lastActivityTime   time.Time          // tracks last GetLimiter() call for gap detection
	recoveryOnce       sync.Once          // ensures recovery loop starts at most once
	staleConnCleanupFn func()             // called on wall-clock gap to close idle API HTTP connections

	// v4.8.4: Coordinator keepalive during active transfers
	keepaliveMu     sync.Mutex
	keepaliveCount  int32              // number of active transfer batches
	keepaliveCancel context.CancelFunc
	sleepRelease    func()             // v4.8.7: release function for OS sleep inhibition
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
			limiters: make(map[string]*limiterEntry),
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

	// v4.8.4: Start background recovery loop when coordinator is configured
	if fn != nil {
		s.startRecoveryLoop()
	}
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
	// v4.8.4: Wall-clock gap detection — triggers recovery after sleep/wake or long idle
	s.checkWallClockGap()

	key := s.makeKey(baseURL, apiKey, scope)
	keyHash := s.hashKey(apiKey)

	s.mu.Lock()
	if entry, ok := s.limiters[key]; ok {
		s.mu.Unlock()
		return entry.limiter
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
		limiter.mu.Lock()
		limiter.degraded = true // v4.8.4: mark as degraded for recovery
		limiter.mu.Unlock()
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
		return existing.limiter
	}
	s.limiters[key] = &limiterEntry{
		limiter: limiter,
		baseURL: baseURL,
		keyHash: keyHash,
		scope:   scope,
	}
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

	// v4.8.4: Mark limiter as degraded for self-healing recovery
	limiter.mu.Lock()
	limiter.degraded = true
	limiter.mu.Unlock()
	log.Printf("[RATELIMIT] Limiter degraded to emergency cap (%.2f req/s) — coordinator disconnected", rate)

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

// RecoverEmergencyLimiters attempts to restore degraded limiters to full rate.
// Called periodically by the recovery loop and on wall-clock gap detection.
// v4.8.4: Core self-healing mechanism.
func (s *LimiterStore) RecoverEmergencyLimiters() int {
	// 1. Collect degraded entries (under s.mu)
	s.mu.Lock()
	var degraded []*limiterEntry
	for _, entry := range s.limiters {
		if entry.limiter.IsDegraded() {
			degraded = append(degraded, entry)
		}
	}
	s.mu.Unlock()

	if len(degraded) == 0 {
		return 0
	}

	// 2. Single coordinator reconnection attempt for all degraded limiters
	coordClient, coordEnabled := s.tryCoordinator()
	if !coordEnabled || coordClient == nil {
		return 0
	}

	// 3. Reconfigure each degraded limiter back to full rate + re-inject hooks
	recovered := 0
	for _, entry := range degraded {
		cfg := s.registry.GetScopeConfig(entry.scope)
		entry.limiter.Reconfigure(cfg.TargetRate, cfg.BurstCapacity)
		s.injectCoordinatorHooks(entry.limiter, coordClient, entry.baseURL, entry.keyHash, entry.scope, cfg)
		entry.limiter.mu.Lock()
		entry.limiter.degraded = false
		entry.limiter.mu.Unlock()
		recovered++
	}

	if recovered > 0 {
		log.Printf("[RATELIMIT] Recovered %d degraded limiters — coordinator reconnected", recovered)
	}
	return recovered
}

// RefreshCoordinatorHooks re-validates and rebinds coordinator hooks on all cached limiters.
// This handles the "stale hook" gap: a cached limiter may still hold dead coordinator hooks
// after a long quiet period without being marked degraded (degradation only occurs on
// coordinator failure during Wait()). Called after a wall-clock gap or coordinator reconnect.
// v4.8.4: Addresses feedback #1 — prevents first request after gap from falling to emergency cap.
func (s *LimiterStore) RefreshCoordinatorHooks() int {
	coordClient, coordEnabled := s.tryCoordinator()
	if !coordEnabled || coordClient == nil {
		return 0
	}

	s.mu.Lock()
	var stale []*limiterEntry
	for _, entry := range s.limiters {
		// Skip degraded limiters (handled by RecoverEmergencyLimiters)
		// Target: non-degraded limiters that had coordinator hooks
		// After a gap, the coordinator process may have exited and restarted,
		// so the hooks point at a dead client. Re-inject fresh hooks.
		if !entry.limiter.IsDegraded() && entry.limiter.HasCoordinatorHooks() {
			stale = append(stale, entry)
		}
	}
	s.mu.Unlock()

	if len(stale) == 0 {
		return 0
	}

	refreshed := 0
	for _, entry := range stale {
		cfg := s.registry.GetScopeConfig(entry.scope)
		s.injectCoordinatorHooks(entry.limiter, coordClient, entry.baseURL, entry.keyHash, entry.scope, cfg)
		refreshed++
	}

	if refreshed > 0 {
		log.Printf("[RATELIMIT] Refreshed coordinator hooks on %d cached limiters", refreshed)
	}
	return refreshed
}

// checkWallClockGap detects large gaps between GetLimiter() calls (e.g., laptop sleep,
// App Nap, long S3 streaming phase) and triggers recovery.
// v4.8.4: Called at the top of GetLimiter().
func (s *LimiterStore) checkWallClockGap() {
	s.mu.Lock()
	now := time.Now()
	gap := now.Sub(s.lastActivityTime)
	wasSet := !s.lastActivityTime.IsZero()
	s.lastActivityTime = now
	s.mu.Unlock()

	if wasSet && gap > WallClockGapThreshold {
		log.Printf("[RATELIMIT] Wall-clock gap detected: %v (threshold=%v) — triggering recovery", gap, WallClockGapThreshold)
		s.resetCoordinatorBackoff()
		s.RecoverEmergencyLimiters()
		s.RefreshCoordinatorHooks()
		if s.staleConnCleanupFn != nil {
			s.staleConnCleanupFn()
		}
	}
}

// resetCoordinatorBackoff clears the coordinator retry backoff so the next tryCoordinator()
// attempt happens immediately. Called after a wall-clock gap where the coordinator
// may have restarted.
// v4.8.4
func (s *LimiterStore) resetCoordinatorBackoff() {
	s.coordMu.Lock()
	s.coordLastAttempt = time.Time{}
	s.coordClient = nil // Force fresh connection attempt
	s.coordMu.Unlock()
}

// startRecoveryLoop starts a background goroutine that periodically checks for
// degraded limiters and attempts recovery. Only runs when a coordinator is configured.
// v4.8.4
func (s *LimiterStore) startRecoveryLoop() {
	s.recoveryOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(RecoveryCheckInterval)
			defer ticker.Stop()
			for range ticker.C {
				s.RecoverEmergencyLimiters()
			}
		}()
	})
}

// SetStaleConnectionCleanup registers a function called on wall-clock gap to close
// idle API HTTP connections. This only covers the API client pool — S3/Azure transport
// pools are separate and not covered by this hook.
// v4.8.4
func (s *LimiterStore) SetStaleConnectionCleanup(fn func()) {
	s.staleConnCleanupFn = fn
}

// BeginTransferActivity signals that a transfer batch is active.
// Starts a coordinator keepalive if not already running.
// Called from transfer.RunBatch/RunBatchFromChannel to cover both GUI and CLI paths.
// v4.8.4
func (s *LimiterStore) BeginTransferActivity() {
	s.keepaliveMu.Lock()
	defer s.keepaliveMu.Unlock()
	s.keepaliveCount++
	if s.keepaliveCount == 1 {
		// v4.8.7: Inhibit OS sleep during active transfers.
		release, err := platform.InhibitSleep("Rescale Interlink file transfer in progress")
		if err != nil {
			log.Printf("[RATELIMIT] Sleep inhibition failed (non-fatal): %v", err)
		}
		s.sleepRelease = release

		if s.coordEnsurer != nil {
			ctx, cancel := context.WithCancel(context.Background())
			s.keepaliveCancel = cancel
			go s.keepaliveLoop(ctx)
		}
	}
}

// EndTransferActivity signals that a transfer batch completed.
// Stops the keepalive when no batches remain active.
// v4.8.4
func (s *LimiterStore) EndTransferActivity() {
	s.keepaliveMu.Lock()
	defer s.keepaliveMu.Unlock()
	s.keepaliveCount--
	if s.keepaliveCount <= 0 {
		s.keepaliveCount = 0
		if s.keepaliveCancel != nil {
			s.keepaliveCancel()
			s.keepaliveCancel = nil
		}
		// v4.8.7: Release OS sleep inhibition when all transfers complete.
		if s.sleepRelease != nil {
			s.sleepRelease()
			s.sleepRelease = nil
		}
	}
}

// keepaliveLoop pings the coordinator periodically during active transfers to prevent
// the coordinator's 5-minute idle timeout. If ping fails, triggers recovery.
// v4.8.4
func (s *LimiterStore) keepaliveLoop(ctx context.Context) {
	ticker := time.NewTicker(CoordinatorKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.coordMu.Lock()
			client := s.coordClient
			s.coordMu.Unlock()
			if client != nil {
				pingCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				err := client.Ping(pingCtx)
				cancel()
				if err != nil {
					// v4.8.4 feedback #2: Don't just ignore ping failures.
					// Actively trigger recovery so limiters don't stay on dead hooks.
					log.Printf("[RATELIMIT] Keepalive ping failed: %v — triggering recovery", err)
					s.resetCoordinatorBackoff()
					s.RecoverEmergencyLimiters()
					s.RefreshCoordinatorHooks()
				}
			} else if s.coordEnsurer != nil {
				// v4.8.4 feedback #2: No client but ensurer configured — try to reconnect
				s.resetCoordinatorBackoff()
				s.RecoverEmergencyLimiters()
				s.RefreshCoordinatorHooks()
			}
		}
	}
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
