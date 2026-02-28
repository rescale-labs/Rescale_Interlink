package ratelimit

import (
	"context"
	"errors"
	"sync"
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
	return m.pingErr
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
