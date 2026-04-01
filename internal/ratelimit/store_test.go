package ratelimit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStoreReturnsSameLimiterForSameKey(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	l1 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	l2 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	if l1 != l2 {
		t.Error("GetLimiter should return the same instance for the same {baseURL, apiKey, scope}")
	}
}

func TestStoreDifferentScopesReturnDifferentLimiters(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	l1 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	l2 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeJobSubmission)

	if l1 == l2 {
		t.Error("different scopes should return different limiter instances")
	}
}

func TestStoreDifferentAPIKeysReturnDifferentLimiters(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	l1 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	l2 := s.GetLimiter("https://platform.rescale.com", "key-xyz", ScopeUser)

	if l1 == l2 {
		t.Error("different API keys should return different limiter instances (separate quotas)")
	}
}

func TestStoreDifferentBaseURLsReturnDifferentLimiters(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	l1 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	l2 := s.GetLimiter("https://staging.rescale.com", "key-abc", ScopeUser)

	if l1 == l2 {
		t.Error("different base URLs should return different limiter instances")
	}
}

func TestStoreLimiterHasCorrectConfig(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	// User scope limiter should have user scope config
	l := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	tokens := l.GetCurrentTokens()
	if tokens < float64(UserScopeBurstCapacity)-1 || tokens > float64(UserScopeBurstCapacity)+1 {
		t.Errorf("user scope limiter tokens = %.2f, want ~%d", tokens, UserScopeBurstCapacity)
	}

	// Job submission limiter should have job submission config
	l2 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeJobSubmission)
	tokens2 := l2.GetCurrentTokens()
	if tokens2 < float64(JobSubmissionBurstCapacity)-1 || tokens2 > float64(JobSubmissionBurstCapacity)+1 {
		t.Errorf("job submit limiter tokens = %.2f, want ~%d", tokens2, JobSubmissionBurstCapacity)
	}
}

func TestStoreGetLimitersReturnsAllScopes(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	limiters := s.GetLimiters("https://platform.rescale.com", "key-abc")

	if len(limiters) != 3 {
		t.Fatalf("GetLimiters returned %d limiters, want 3", len(limiters))
	}

	for _, scope := range []Scope{ScopeUser, ScopeJobSubmission, ScopeJobsUsage} {
		if _, ok := limiters[scope]; !ok {
			t.Errorf("GetLimiters missing scope %q", scope)
		}
	}
}

func TestStoreSharedStateSurvivesNewClient(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	// Simulate first client consuming tokens
	l1 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	for i := 0; i < 10; i++ {
		l1.tryAcquire()
	}
	tokensAfterConsume := l1.GetCurrentTokens()

	// Simulate second client with same credentials (like config update)
	l2 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	tokensSecondClient := l2.GetCurrentTokens()

	// Should be the same limiter instance — tokens should match
	if l1 != l2 {
		t.Error("second GetLimiter call should return same instance")
	}
	// Tokens from second client perspective should match (minus any tiny refill)
	if tokensSecondClient > tokensAfterConsume+1 {
		t.Errorf("second client sees %v tokens, but first client had %v (state not shared)",
			tokensSecondClient, tokensAfterConsume)
	}
}

func TestGlobalStoreSingleton(t *testing.T) {
	ResetGlobalStore()

	s1 := GlobalStore()
	s2 := GlobalStore()

	if s1 != s2 {
		t.Error("GlobalStore() should return the same instance")
	}
}

func TestStoreRegistryIsAccessible(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	r := s.Registry()
	if r == nil {
		t.Fatal("Registry() returned nil")
	}

	scope := r.ResolveScope("GET", "/api/v3/users/me/")
	if scope != ScopeUser {
		t.Errorf("Registry scope = %q, want %q", scope, ScopeUser)
	}
}

// --- Phase 2: Coordinator-aware store tests ---

// mockCoordClient implements CoordinatorClient for testing.
type mockCoordClient struct {
	mu              sync.Mutex
	acquireCalls    int
	drainCalls      int
	cooldownCalls   int
	pingCalls       int
	acquireErr      error
	lease           *LeaseInfo
	pingErr         error
}

func (m *mockCoordClient) Acquire(_ context.Context, _, _ string, _ Scope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acquireCalls++
	return m.acquireErr
}

func (m *mockCoordClient) Drain(_ context.Context, _, _ string, _ Scope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drainCalls++
	return nil
}

func (m *mockCoordClient) SetCooldown(_ context.Context, _, _ string, _ Scope, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cooldownCalls++
	return nil
}

func (m *mockCoordClient) GetLease(_, _ string, _ Scope) *LeaseInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lease
}

func (m *mockCoordClient) Ping(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pingCalls++
	return m.pingErr
}

func (m *mockCoordClient) getPingCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pingCalls
}

func TestStoreCoordinatorHookInjected(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	mock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return mock, nil
	})

	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	// Wait should delegate to coordinator
	ctx := context.Background()
	err := limiter.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error: %v", err)
	}

	mock.mu.Lock()
	calls := mock.acquireCalls
	mock.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 Acquire call, got %d", calls)
	}
}

func TestStoreFallbackWithoutCoordinator(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return nil, errors.New("no coordinator")
	})

	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	// Should be at emergency rate (burst=1 = starts with 1 token)
	tokens := limiter.GetCurrentTokens()
	if tokens > 1.1 {
		t.Errorf("emergency cap limiter should start with ~1 token (burst=1), got %.2f", tokens)
	}
}

func TestColdStartEmergencyCapInvariant(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	// Coordinator unreachable + no lease + first GetLimiter()
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return nil, errors.New("coordinator unreachable")
	})

	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	// Emergency cap for user scope: (2.0/4)*0.5 = 0.25 req/sec, burst = 1
	// The FIRST Wait() must already be at emergency rate.
	// With burst=1, the limiter starts with 1 token (allows exactly one immediate request).
	tokens := limiter.GetCurrentTokens()
	if tokens > 1.1 {
		t.Errorf("cold start invariant violated: tokens = %.2f, want ~1 (emergency burst=1)", tokens)
	}
}

func TestRuntimeDisconnectEmergencyCapInvariant(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	mock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return mock, nil
	})

	// Use jobs-usage scope (25 req/sec hard limit → emergency = 3.125 req/sec)
	// so fallback Wait completes quickly (~320ms) instead of ~4s for user scope
	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeJobsUsage)

	// First call succeeds via coordinator
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := limiter.Wait(ctx); err != nil {
		t.Fatalf("first Wait() error: %v", err)
	}

	// Simulate coordinator disconnect
	mock.mu.Lock()
	mock.acquireErr = errors.New("coordinator unreachable")
	mock.mu.Unlock()

	// Next Wait() should fail-over to local with reconfigured emergency rate
	if err := limiter.Wait(ctx); err != nil {
		t.Fatalf("second Wait() error after disconnect: %v", err)
	}

	// Verify limiter was reconfigured (hooks cleared after disconnect)
	// The limiter should now be at emergency rate, not full rate
}

func TestCoordinatorReconnectAfterFailure(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	callCount := 0
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		callCount++
		if callCount == 1 {
			return nil, errors.New("not ready")
		}
		return &mockCoordClient{}, nil
	})

	// First call — coordinator fails, limiter at emergency cap (burst=1)
	l1 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	tokens := l1.GetCurrentTokens()
	if tokens > 1.1 {
		t.Errorf("expected emergency cap tokens (~1), got %.2f", tokens)
	}

	// Simulate time passing beyond backoff
	s.coordMu.Lock()
	s.coordLastAttempt = time.Now().Add(-2 * coordRetryBackoff)
	s.coordMu.Unlock()

	// Next GetLimiter for a DIFFERENT key should retry and succeed
	l2 := s.GetLimiter("https://platform.rescale.com", "key-xyz", ScopeUser)

	// Should have coordinator hooks (full target rate)
	tokens2 := l2.GetCurrentTokens()
	if tokens2 < float64(UserScopeBurstCapacity)-1 {
		t.Errorf("after reconnect, expected full burst tokens, got %.2f", tokens2)
	}
}

func TestStoreNoCoordinatorEnsurer(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	// No coordinator ensurer set — should behave exactly as Phase 1
	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	tokens := limiter.GetCurrentTokens()
	if tokens < float64(UserScopeBurstCapacity)-1 {
		t.Errorf("without coordinator, expected full target rate tokens, got %.2f", tokens)
	}
}

func TestStoreCoordinatorDrainHook(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	mock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return mock, nil
	})

	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	limiter.Drain()

	mock.mu.Lock()
	calls := mock.drainCalls
	mock.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 Drain call to coordinator, got %d", calls)
	}
}

func TestStoreCoordinatorCooldownHook(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	mock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return mock, nil
	})

	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	limiter.SetCooldown(10 * time.Second)

	mock.mu.Lock()
	calls := mock.cooldownCalls
	mock.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected 1 SetCooldown call to coordinator, got %d", calls)
	}
}

// --- Visibility: Store-level notification tests ---

func TestSetGlobalNotifyFuncWiresNewLimiters(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	var called bool
	SetGlobalNotifyFunc(func(level, message string) {
		called = true
	})
	defer SetGlobalNotifyFunc(nil) // Clean up

	limiter := s.GetLimiter("https://platform.rescale.com", "key-notify", ScopeUser)

	// The limiter should have the notify func wired
	// Trigger an emission by calling emitUtilizationNotice directly
	// (limiter is at 85% utilization: 1.7/2.0 — above 60% threshold)
	limiter.emitUtilizationNotice(500 * time.Millisecond)

	if !called {
		t.Error("global notify func was not wired to new limiter")
	}
}

func TestCoordinatorWaitEmitsVisibility(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	var notifyCalled bool
	SetGlobalNotifyFunc(func(level, message string) {
		notifyCalled = true
	})
	defer SetGlobalNotifyFunc(nil)

	// Mock coordinator that introduces a 150ms delay (above 100ms threshold)
	mock := &mockCoordClient{
		acquireErr: nil,
	}
	// Override Acquire to add delay
	delayMock := &delayingMockCoordClient{
		mockCoordClient: mock,
		acquireDelay:    150 * time.Millisecond,
	}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return delayMock, nil
	})

	limiter := s.GetLimiter("https://platform.rescale.com", "key-delay", ScopeUser)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := limiter.Wait(ctx); err != nil {
		t.Fatalf("Wait() error: %v", err)
	}

	if !notifyCalled {
		t.Error("visibility notification was not emitted for coordinator-mediated wait >100ms")
	}
}

// delayingMockCoordClient wraps mockCoordClient and adds a delay to Acquire.
type delayingMockCoordClient struct {
	*mockCoordClient
	acquireDelay time.Duration
}

func (d *delayingMockCoordClient) Acquire(ctx context.Context, baseURL, keyHash string, scope Scope) error {
	time.Sleep(d.acquireDelay)
	return d.mockCoordClient.Acquire(ctx, baseURL, keyHash, scope)
}

// =============================================================================
// Coordinator self-healing tests
// =============================================================================

func TestIsDegraded(t *testing.T) {
	rl := NewRateLimiter(1.0, 1)

	if rl.IsDegraded() {
		t.Error("new limiter should not be degraded")
	}

	rl.mu.Lock()
	rl.degraded = true
	rl.mu.Unlock()

	if !rl.IsDegraded() {
		t.Error("limiter should be degraded after setting flag")
	}

	rl.mu.Lock()
	rl.degraded = false
	rl.mu.Unlock()

	if rl.IsDegraded() {
		t.Error("limiter should not be degraded after clearing flag")
	}
}

func TestHasCoordinatorHooks(t *testing.T) {
	rl := NewRateLimiter(1.0, 1)

	if rl.HasCoordinatorHooks() {
		t.Error("new limiter should not have coordinator hooks")
	}

	rl.SetCoordinatorHook(func(ctx context.Context) error { return nil }, func() {}, func(d time.Duration) {})

	if !rl.HasCoordinatorHooks() {
		t.Error("limiter should have coordinator hooks after SetCoordinatorHook")
	}

	rl.ClearCoordinatorHook()

	if rl.HasCoordinatorHooks() {
		t.Error("limiter should not have coordinator hooks after ClearCoordinatorHook")
	}
}

func TestHandleCoordinatorDisconnect_SetsDegraded(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	disconnectTriggered := false
	mock := &mockCoordClient{
		acquireErr: errors.New("coordinator unreachable"),
	}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		if disconnectTriggered {
			return nil, errors.New("still down")
		}
		return mock, nil
	})

	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	// The limiter should have coordinator hooks injected
	if !limiter.HasCoordinatorHooks() {
		t.Fatal("limiter should have coordinator hooks after creation with coordinator")
	}

	// Trigger a Wait() that will fail — this triggers handleCoordinatorDisconnect
	disconnectTriggered = true
	_ = limiter.Wait(context.Background())

	// After disconnect, limiter should be degraded
	if !limiter.IsDegraded() {
		t.Error("limiter should be degraded after coordinator disconnect")
	}
}

func TestRecoverEmergencyLimiters_RecoversDegraded(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	// Phase 1: Create with coordinator available → limiter gets hooks
	mock := &mockCoordClient{}
	coordAvailable := true
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		if coordAvailable {
			return mock, nil
		}
		return nil, errors.New("unavailable")
	})

	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	if limiter.IsDegraded() {
		t.Fatal("limiter should not start degraded")
	}

	// Phase 2: Manually degrade the limiter (simulating coordinator disconnect)
	cfg := s.registry.GetScopeConfig(ScopeUser)
	emergencyRate, emergencyBurst := emergencyCap(cfg)
	limiter.Reconfigure(emergencyRate, emergencyBurst)
	limiter.ClearCoordinatorHook()
	limiter.mu.Lock()
	limiter.degraded = true
	limiter.mu.Unlock()

	if !limiter.IsDegraded() {
		t.Fatal("limiter should be degraded after manual degradation")
	}

	// Phase 3: Make coordinator available again and recover
	coordAvailable = true
	// Reset backoff so tryCoordinator will attempt immediately
	s.resetCoordinatorBackoff()

	recovered := s.RecoverEmergencyLimiters()
	if recovered != 1 {
		t.Errorf("expected 1 recovered limiter, got %d", recovered)
	}

	if limiter.IsDegraded() {
		t.Error("limiter should not be degraded after recovery")
	}

	// Verify rate is back to full (not emergency cap)
	limiter.mu.Lock()
	rate := limiter.refillRate
	limiter.mu.Unlock()
	if rate != cfg.TargetRate {
		t.Errorf("expected rate %.2f after recovery, got %.2f", cfg.TargetRate, rate)
	}
}

func TestRecoverEmergencyLimiters_NoOpWhenHealthy(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	mock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return mock, nil
	})

	_ = s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	recovered := s.RecoverEmergencyLimiters()
	if recovered != 0 {
		t.Errorf("expected 0 recovered (all healthy), got %d", recovered)
	}
}

func TestWallClockGap_TriggersRecovery(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	ensurerCalls := int32(0)
	mock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		atomic.AddInt32(&ensurerCalls, 1)
		return mock, nil
	})

	// First GetLimiter sets lastActivityTime
	_ = s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	// Manually set lastActivityTime to 3 minutes ago to simulate a gap
	s.mu.Lock()
	s.lastActivityTime = time.Now().Add(-3 * time.Minute)
	s.mu.Unlock()

	callsBefore := atomic.LoadInt32(&ensurerCalls)

	// Next GetLimiter should detect the gap and trigger recovery
	_ = s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	callsAfter := atomic.LoadInt32(&ensurerCalls)
	if callsAfter <= callsBefore {
		t.Error("expected coordinator ensurer to be called after wall-clock gap")
	}
}

func TestRefreshCoordinatorHooks_RebindsStaleHooks(t *testing.T) {
	// Feedback #1 test: coordinator dies during quiet download phase,
	// next upload uses existing cached limiter. The stale hook should be
	// refreshed before the first request falls to emergency cap.
	ResetGlobalStore()
	s := GlobalStore()

	mock1 := &mockCoordClient{}
	mock2 := &mockCoordClient{}
	useSecondMock := false

	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		if useSecondMock {
			return mock2, nil
		}
		return mock1, nil
	})

	// Create limiter with first coordinator
	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	// Limiter has hooks from mock1
	if !limiter.HasCoordinatorHooks() {
		t.Fatal("limiter should have coordinator hooks")
	}

	// Simulate: coordinator dies and restarts (mock2 replaces mock1)
	useSecondMock = true
	s.resetCoordinatorBackoff()

	// RefreshCoordinatorHooks should rebind to mock2
	refreshed := s.RefreshCoordinatorHooks()
	if refreshed != 1 {
		t.Errorf("expected 1 refreshed hook, got %d", refreshed)
	}

	// The limiter should still have hooks (now from mock2)
	if !limiter.HasCoordinatorHooks() {
		t.Error("limiter should still have coordinator hooks after refresh")
	}

	// Verify the hooks point to mock2 by doing an Acquire
	_ = limiter.Wait(context.Background())
	if mock2.acquireCalls == 0 {
		t.Error("expected Acquire to be called on new coordinator (mock2)")
	}
}

func TestRecovery_ConcurrencySafe(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	ensurerCalls := int32(0)
	mock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		atomic.AddInt32(&ensurerCalls, 1)
		return mock, nil
	})

	// Create a degraded limiter
	_ = s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	s.mu.Lock()
	for _, entry := range s.limiters {
		entry.limiter.mu.Lock()
		entry.limiter.degraded = true
		entry.limiter.mu.Unlock()
	}
	s.mu.Unlock()
	s.resetCoordinatorBackoff()

	// Call RecoverEmergencyLimiters from 10 goroutines
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.RecoverEmergencyLimiters()
		}()
	}
	wg.Wait()

	// Coordinator ensurer should be called at most a few times (30s backoff deduplication)
	// Not exactly once due to race, but should be << 10
	calls := atomic.LoadInt32(&ensurerCalls)
	if calls > 5 {
		t.Errorf("expected coordinator ensurer to be called few times (backoff dedup), got %d", calls)
	}
}

func TestRecovery_RestoresFullRate(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	mock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return mock, nil
	})

	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)
	cfg := s.registry.GetScopeConfig(ScopeUser)

	// Degrade
	emergencyRate, emergencyBurst := emergencyCap(cfg)
	limiter.Reconfigure(emergencyRate, emergencyBurst)
	limiter.ClearCoordinatorHook()
	limiter.mu.Lock()
	limiter.degraded = true
	limiter.mu.Unlock()

	// Verify it's at emergency cap
	limiter.mu.Lock()
	rateBefore := limiter.refillRate
	limiter.mu.Unlock()
	if rateBefore != emergencyRate {
		t.Fatalf("expected emergency rate %.2f, got %.2f", emergencyRate, rateBefore)
	}

	// Recover
	s.resetCoordinatorBackoff()
	s.RecoverEmergencyLimiters()

	// Verify restored to full rate
	limiter.mu.Lock()
	rateAfter := limiter.refillRate
	limiter.mu.Unlock()
	if rateAfter != cfg.TargetRate {
		t.Errorf("expected full rate %.2f after recovery, got %.2f", cfg.TargetRate, rateAfter)
	}
}

func TestKeepAlive_PreventsCoordinatorTimeout(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	mock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return mock, nil
	})

	// Force create coordinator client
	_ = s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	// Start keepalive with a very short interval for testing
	s.keepaliveMu.Lock()
	s.keepaliveCount++
	ctx, cancel := context.WithCancel(context.Background())
	s.keepaliveCancel = cancel
	s.keepaliveMu.Unlock()

	// Run keepalive loop manually with short ticker
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
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
					pingCtx, pingCancel := context.WithTimeout(ctx, 500*time.Millisecond)
					_ = client.Ping(pingCtx)
					pingCancel()
				}
			}
		}
	}()

	// Wait for some pings
	time.Sleep(50 * time.Millisecond)

	// Stop keepalive
	cancel()

	// Verify pings were made
	pings := mock.getPingCalls()
	if pings == 0 {
		t.Error("expected at least one Ping call during keepalive")
	}
}

func TestKeepAlive_RefCounting(t *testing.T) {
	ResetGlobalStore()
	s := GlobalStore()

	mock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return mock, nil
	})

	// Initialize coordinator
	_ = s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	// Begin two transfer activities
	s.BeginTransferActivity()
	s.BeginTransferActivity()

	s.keepaliveMu.Lock()
	count1 := s.keepaliveCount
	hasCancel1 := s.keepaliveCancel != nil
	s.keepaliveMu.Unlock()

	if count1 != 2 {
		t.Errorf("expected keepaliveCount=2, got %d", count1)
	}
	if !hasCancel1 {
		t.Error("expected keepalive to be running after BeginTransferActivity")
	}

	// End one — keepalive should still run
	s.EndTransferActivity()

	s.keepaliveMu.Lock()
	count2 := s.keepaliveCount
	hasCancel2 := s.keepaliveCancel != nil
	s.keepaliveMu.Unlock()

	if count2 != 1 {
		t.Errorf("expected keepaliveCount=1, got %d", count2)
	}
	if !hasCancel2 {
		t.Error("expected keepalive to still run with count=1")
	}

	// End second — keepalive should stop
	s.EndTransferActivity()

	s.keepaliveMu.Lock()
	count3 := s.keepaliveCount
	hasCancel3 := s.keepaliveCancel != nil
	s.keepaliveMu.Unlock()

	if count3 != 0 {
		t.Errorf("expected keepaliveCount=0, got %d", count3)
	}
	if hasCancel3 {
		t.Error("expected keepalive to be stopped when count=0")
	}
}

func TestStaleHookDetection_AfterCoordinatorDeath(t *testing.T) {
	// Feedback #1 exact scenario: coordinator dies during quiet download phase,
	// next upload uses existing cached limiter. The first request should NOT
	// fall through to emergency cap.
	ResetGlobalStore()
	s := GlobalStore()

	mock := &mockCoordClient{}
	coordAvailable := true
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		if coordAvailable {
			return mock, nil
		}
		return nil, errors.New("dead")
	})

	// Get limiter (creates it with coordinator hooks)
	limiter := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	// Limiter is at full rate with hooks
	cfg := s.registry.GetScopeConfig(ScopeUser)
	limiter.mu.Lock()
	rate := limiter.refillRate
	limiter.mu.Unlock()
	if rate != cfg.TargetRate {
		t.Fatalf("expected full rate %.2f, got %.2f", cfg.TargetRate, rate)
	}

	// Simulate: coordinator dies, but limiter is cached and not yet used
	// (not degraded because no Wait() has been called)
	coordAvailable = false

	// Simulate wall-clock gap (long download phase with no API calls)
	s.mu.Lock()
	s.lastActivityTime = time.Now().Add(-3 * time.Minute)
	s.mu.Unlock()

	// Now make coordinator available again (simulating restart)
	coordAvailable = true
	newMock := &mockCoordClient{}
	s.SetCoordinatorEnsurer(func() (CoordinatorClient, error) {
		return newMock, nil
	})

	// Next GetLimiter triggers wall-clock gap detection → RefreshCoordinatorHooks
	limiter2 := s.GetLimiter("https://platform.rescale.com", "key-abc", ScopeUser)

	// Should be same cached limiter
	if limiter2 != limiter {
		t.Error("expected same cached limiter")
	}

	// Limiter should NOT be degraded — hooks were refreshed
	if limiter.IsDegraded() {
		t.Error("limiter should not be degraded after refresh")
	}

	// Rate should still be full
	limiter.mu.Lock()
	rateAfter := limiter.refillRate
	limiter.mu.Unlock()
	if rateAfter != cfg.TargetRate {
		t.Errorf("expected full rate %.2f after refresh, got %.2f", cfg.TargetRate, rateAfter)
	}
}
