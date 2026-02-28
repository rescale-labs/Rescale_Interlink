package coordinator

import (
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

// newTestRequest creates a Request with common fields filled in.
func newTestRequest(msgType MessageType, scope ratelimit.Scope) *Request {
	return &Request{
		Type:     msgType,
		ClientID: "pid-1",
		Scope:    scope,
		BaseURL:  "https://platform.rescale.com",
		KeyHash:  "abcdef01",
	}
}

func TestAcquireGrants(t *testing.T) {
	srv := NewServer()

	req := newTestRequest(MsgAcquire, ratelimit.ScopeUser)
	resp := srv.HandleRequest(req)

	if resp.Type != MsgGranted {
		t.Errorf("expected Granted, got %s", resp.Type)
	}
	if !resp.Success {
		t.Error("expected Success=true")
	}
}

func TestAcquireWaitsWhenEmpty(t *testing.T) {
	srv := NewServer()

	req := newTestRequest(MsgAcquire, ratelimit.ScopeUser)

	// Exhaust the bucket (user scope starts with 150 tokens)
	for i := 0; i < 160; i++ {
		srv.HandleRequest(req)
	}

	// Next acquire should return Wait
	resp := srv.HandleRequest(req)
	if resp.Type != MsgWait {
		t.Errorf("expected Wait after exhaustion, got %s", resp.Type)
	}
	if resp.WaitDuration <= 0 {
		t.Errorf("WaitDuration should be positive, got %v", resp.WaitDuration)
	}
}

func TestAcquireRespectsCooldown(t *testing.T) {
	srv := NewServer()

	// Set cooldown
	cooldownReq := newTestRequest(MsgSetCooldown, ratelimit.ScopeUser)
	cooldownReq.CooldownDuration = 5 * time.Second
	srv.HandleRequest(cooldownReq)

	// Acquire should return Wait with cooldown duration
	req := newTestRequest(MsgAcquire, ratelimit.ScopeUser)
	resp := srv.HandleRequest(req)

	if resp.Type != MsgWait {
		t.Errorf("expected Wait during cooldown, got %s", resp.Type)
	}
	if resp.WaitDuration < 4*time.Second {
		t.Errorf("WaitDuration should be ~5s, got %v", resp.WaitDuration)
	}
}

func TestDrainEmptiesBucket(t *testing.T) {
	srv := NewServer()

	// First acquire to create the bucket
	acquireReq := newTestRequest(MsgAcquire, ratelimit.ScopeUser)
	resp := srv.HandleRequest(acquireReq)
	if resp.Type != MsgGranted {
		t.Fatalf("first acquire should be Granted, got %s", resp.Type)
	}

	// Drain
	drainReq := newTestRequest(MsgDrain, ratelimit.ScopeUser)
	resp = srv.HandleRequest(drainReq)
	if resp.Type != MsgOK {
		t.Errorf("Drain should return OK, got %s", resp.Type)
	}

	// Next acquire should wait (bucket is empty)
	resp = srv.HandleRequest(acquireReq)
	if resp.Type != MsgWait {
		t.Errorf("expected Wait after drain, got %s", resp.Type)
	}
}

func TestSetCooldownPropagates(t *testing.T) {
	srv := NewServer()

	req := newTestRequest(MsgSetCooldown, ratelimit.ScopeUser)
	req.CooldownDuration = 2 * time.Second

	resp := srv.HandleRequest(req)
	if resp.Type != MsgOK {
		t.Errorf("SetCooldown should return OK, got %s", resp.Type)
	}

	// Verify: acquire should now wait
	acquireReq := newTestRequest(MsgAcquire, ratelimit.ScopeUser)
	acquireResp := srv.HandleRequest(acquireReq)
	if acquireResp.Type != MsgWait {
		t.Errorf("expected Wait after cooldown, got %s", acquireResp.Type)
	}
}

func TestCooldownMergeOnServer(t *testing.T) {
	srv := NewServer()

	// Set a 5s cooldown
	req1 := newTestRequest(MsgSetCooldown, ratelimit.ScopeUser)
	req1.CooldownDuration = 5 * time.Second
	srv.HandleRequest(req1)

	time.Sleep(50 * time.Millisecond)

	// Try to shorten with 1s cooldown — should be ignored (merge semantics)
	req2 := newTestRequest(MsgSetCooldown, ratelimit.ScopeUser)
	req2.CooldownDuration = 1 * time.Second
	srv.HandleRequest(req2)

	// Cooldown should still be ~5s
	acquireReq := newTestRequest(MsgAcquire, ratelimit.ScopeUser)
	resp := srv.HandleRequest(acquireReq)
	if resp.Type != MsgWait {
		t.Errorf("expected Wait, got %s", resp.Type)
	}
	if resp.WaitDuration < 4*time.Second {
		t.Errorf("cooldown should still be ~5s, but WaitDuration = %v", resp.WaitDuration)
	}
}

func TestLeaseGrantFraction(t *testing.T) {
	srv := NewServer()

	req := newTestRequest(MsgAcquireLease, ratelimit.ScopeUser)
	resp := srv.HandleRequest(req)

	if resp.Type != MsgLeaseGranted {
		t.Fatalf("expected LeaseGranted, got %s", resp.Type)
	}
	if resp.Lease == nil {
		t.Fatal("lease is nil")
	}

	// One client: should get full target rate
	if resp.Lease.Rate != ratelimit.UserScopeRatePerSec {
		t.Errorf("Rate = %v, want %v", resp.Lease.Rate, ratelimit.UserScopeRatePerSec)
	}
	if resp.Lease.LeaseID == "" {
		t.Error("LeaseID should not be empty")
	}
	if resp.Lease.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should not be zero")
	}
}

func TestLeaseFractionChangesWithClientCount(t *testing.T) {
	srv := NewServer()

	// Client 1 gets a lease
	req1 := newTestRequest(MsgAcquireLease, ratelimit.ScopeUser)
	req1.ClientID = "pid-1"
	resp1 := srv.HandleRequest(req1)
	if resp1.Lease == nil {
		t.Fatal("first lease is nil")
	}

	// Client 2 gets a lease — rate should be split
	req2 := newTestRequest(MsgAcquireLease, ratelimit.ScopeUser)
	req2.ClientID = "pid-2"
	resp2 := srv.HandleRequest(req2)
	if resp2.Lease == nil {
		t.Fatal("second lease is nil")
	}

	// With 2 active leases, each should get approximately half
	expectedRate := ratelimit.UserScopeRatePerSec / 2.0
	if resp2.Lease.Rate != expectedRate {
		t.Errorf("second lease Rate = %v, want %v", resp2.Lease.Rate, expectedRate)
	}
}

func TestHeartbeatRefreshesLease(t *testing.T) {
	srv := NewServer()

	// Get a lease
	leaseReq := newTestRequest(MsgAcquireLease, ratelimit.ScopeUser)
	leaseResp := srv.HandleRequest(leaseReq)
	if leaseResp.Lease == nil {
		t.Fatal("lease is nil")
	}
	originalExpiry := leaseResp.Lease.ExpiresAt

	time.Sleep(50 * time.Millisecond)

	// Heartbeat
	hbReq := newTestRequest(MsgHeartbeat, ratelimit.ScopeUser)
	resp := srv.HandleRequest(hbReq)
	if resp.Type != MsgOK {
		t.Errorf("Heartbeat should return OK, got %s", resp.Type)
	}

	// Check that lease expiry was extended
	srv.mu.Lock()
	for _, ls := range srv.leases {
		if ls.clientID == "pid-1" {
			if !ls.grant.ExpiresAt.After(originalExpiry) {
				t.Error("Heartbeat should have extended lease expiry")
			}
		}
	}
	srv.mu.Unlock()
}

func TestGetState(t *testing.T) {
	srv := NewServer()

	// Create some state: acquire a token, register a client
	acquireReq := newTestRequest(MsgAcquire, ratelimit.ScopeUser)
	srv.HandleRequest(acquireReq)

	// Get state
	stateReq := newTestRequest(MsgGetState, ratelimit.ScopeUser)
	resp := srv.HandleRequest(stateReq)

	if resp.Type != MsgStateData {
		t.Fatalf("expected StateData, got %s", resp.Type)
	}
	if resp.State == nil {
		t.Fatal("state is nil")
	}
	if resp.State.ActiveClients != 1 {
		t.Errorf("ActiveClients = %d, want 1", resp.State.ActiveClients)
	}
	if len(resp.State.Buckets) != 1 {
		t.Errorf("Buckets count = %d, want 1", len(resp.State.Buckets))
	}
}

func TestIdleTimeoutShutdown(t *testing.T) {
	srv := NewServer()
	srv.SetIdleTimeout(100 * time.Millisecond)
	// Use a fast watchdog interval for testing
	srv.setWatchdogInterval(200 * time.Millisecond)

	// Don't start the listener — just test the idle logic
	// Manually set lastActivity in the past
	srv.mu.Lock()
	srv.lastActivity = time.Now().Add(-200 * time.Millisecond)
	srv.mu.Unlock()

	// Start only the idle watchdog
	srv.wg.Add(1)
	go srv.idleWatchdog()

	select {
	case <-srv.Done():
		// Good — server shut down due to idle
	case <-time.After(2 * time.Second):
		t.Fatal("idle watchdog did not shut down within expected time")
	}
}

func TestConcurrentAcquire(t *testing.T) {
	srv := NewServer()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			req := &Request{
				Type:     MsgAcquire,
				ClientID: "pid-" + string(rune('a'+id)),
				Scope:    ratelimit.ScopeUser,
				BaseURL:  "https://platform.rescale.com",
				KeyHash:  "abcdef01",
			}
			for j := 0; j < 50; j++ {
				resp := srv.HandleRequest(req)
				if resp.Type != MsgGranted && resp.Type != MsgWait {
					t.Errorf("unexpected response type: %s", resp.Type)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestDifferentScopesDifferentBuckets(t *testing.T) {
	srv := NewServer()

	// Exhaust user scope
	userReq := newTestRequest(MsgAcquire, ratelimit.ScopeUser)
	for i := 0; i < 160; i++ {
		srv.HandleRequest(userReq)
	}
	userResp := srv.HandleRequest(userReq)
	if userResp.Type != MsgWait {
		t.Errorf("user scope should be exhausted, got %s", userResp.Type)
	}

	// Jobs-usage scope should still have tokens
	jobsReq := newTestRequest(MsgAcquire, ratelimit.ScopeJobsUsage)
	jobsResp := srv.HandleRequest(jobsReq)
	if jobsResp.Type != MsgGranted {
		t.Errorf("jobs-usage scope should still be available, got %s", jobsResp.Type)
	}
}

func TestShutdownRequest(t *testing.T) {
	srv := NewServer()

	req := newTestRequest(MsgShutdown, "")
	resp := srv.HandleRequest(req)

	if resp.Type != MsgOK {
		t.Errorf("Shutdown should return OK, got %s", resp.Type)
	}

	// Server should shut down shortly
	select {
	case <-srv.Done():
		// Good
	case <-time.After(1 * time.Second):
		t.Error("server did not shut down after Shutdown request")
	}
}

func TestUnknownMessageType(t *testing.T) {
	srv := NewServer()

	req := &Request{
		Type:     "Unknown",
		ClientID: "pid-1",
	}
	resp := srv.HandleRequest(req)

	if resp.Type != MsgError {
		t.Errorf("expected Error for unknown type, got %s", resp.Type)
	}
}
