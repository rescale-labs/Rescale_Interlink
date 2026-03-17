package coordinator

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

// startIntegrationServer creates a server and multiple clients for integration testing.
func startIntegrationServer(t *testing.T, numClients int) ([]*Client, *Server, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "coordinator-integration")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	sockPath := filepath.Join(tmpDir, "test.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to listen: %v", err)
	}

	srv := NewServer()
	srv.Start(listener)

	clients := make([]*Client, numClients)
	for i := 0; i < numClients; i++ {
		clients[i] = NewClientWithPath(sockPath)
	}

	cleanup := func() {
		for _, c := range clients {
			c.Close()
		}
		srv.Stop()
		os.RemoveAll(tmpDir)
	}

	return clients, srv, cleanup
}

func TestTwoClientsSharedBudget(t *testing.T) {
	clients, _, cleanup := startIntegrationServer(t, 2)
	defer cleanup()

	// Both clients acquire tokens from the same bucket
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var totalGranted atomic.Int64

	for _, client := range clients {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				err := c.Acquire(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
				if err != nil {
					return
				}
				totalGranted.Add(1)
			}
		}(client)
	}

	wg.Wait()

	// Both clients should have been granted some tokens
	granted := totalGranted.Load()
	if granted < 2 {
		t.Errorf("expected at least 2 grants total, got %d", granted)
	}
	// Total grants should not exceed burst capacity (150 for user scope) + any refills
	if granted > 170 {
		t.Errorf("granted %d tokens — exceeds expected budget", granted)
	}
}

func TestDrainPropagatesAcrossClients(t *testing.T) {
	clients, _, cleanup := startIntegrationServer(t, 2)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Client A acquires a token
	err := clients[0].Acquire(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if err != nil {
		t.Fatalf("Client A acquire error: %v", err)
	}

	// Client A drains the bucket (simulating 429)
	err = clients[0].Drain(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if err != nil {
		t.Fatalf("Client A drain error: %v", err)
	}

	// Exhaust remaining tokens (bucket was partially filled)
	// After drain, tokens are 0, so next acquire should wait
	for i := 0; i < 160; i++ {
		resp, sendErr := clients[1].sendRequest(ctx, &Request{
			Type:    MsgAcquire,
			Scope:   ratelimit.ScopeUser,
			BaseURL: "https://platform.rescale.com",
			KeyHash: "abcdef01",
		})
		if sendErr != nil {
			t.Fatalf("sendRequest error: %v", sendErr)
		}
		if resp.Type == MsgWait {
			// Good — drain propagated, Client B is waiting
			return
		}
	}
	t.Error("Client B never received Wait after Client A's drain")
}

func TestCooldownPropagatesAcrossClients(t *testing.T) {
	clients, _, cleanup := startIntegrationServer(t, 2)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Client A sets a cooldown
	err := clients[0].SetCooldown(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser, 3*time.Second)
	if err != nil {
		t.Fatalf("Client A SetCooldown error: %v", err)
	}

	// Client B tries to acquire — should get Wait with cooldown
	resp, err := clients[1].sendRequest(ctx, &Request{
		Type:    MsgAcquire,
		Scope:   ratelimit.ScopeUser,
		BaseURL: "https://platform.rescale.com",
		KeyHash: "abcdef01",
	})
	if err != nil {
		t.Fatalf("Client B sendRequest error: %v", err)
	}

	if resp.Type != MsgWait {
		t.Errorf("expected Client B to Wait during cooldown, got %s", resp.Type)
	}
	if resp.WaitDuration < 2*time.Second {
		t.Errorf("WaitDuration should be ~3s, got %v", resp.WaitDuration)
	}
}

func TestEmergencyCapWhenNoCoordinator(t *testing.T) {
	// Test that EmergencyCap computes correct values for each scope
	reg := ratelimit.NewRegistry()

	tests := []struct {
		scope        ratelimit.Scope
		expectedRate float64
	}{
		{ratelimit.ScopeUser, (2.0 / 4.0) * 0.5},             // 0.25
		{ratelimit.ScopeJobSubmission, (0.278 / 4.0) * 0.5},   // ~0.035
		{ratelimit.ScopeJobsUsage, (25.0 / 4.0) * 0.5},        // 3.125
	}

	for _, tt := range tests {
		cfg := reg.GetScopeConfig(tt.scope)
		rate, burst := EmergencyCap(cfg)

		if rate < tt.expectedRate*0.9 || rate > tt.expectedRate*1.1 {
			t.Errorf("EmergencyCap(%s) rate = %v, want ~%v", tt.scope, rate, tt.expectedRate)
		}
		if burst != 1 {
			t.Errorf("EmergencyCap(%s) burst = %v, want 1", tt.scope, burst)
		}
	}
}

func TestClientReconnectAfterCoordinatorRestart(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "coordinator-restart")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start first server
	listener1, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	srv1 := NewServer()
	srv1.Start(listener1)

	client := NewClientWithPath(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Acquire from first server
	err = client.Acquire(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if err != nil {
		t.Fatalf("first acquire error: %v", err)
	}

	// Stop first server
	srv1.Stop()

	// Client should get ErrCoordinatorUnreachable
	err = client.Acquire(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if err != ErrCoordinatorUnreachable {
		t.Errorf("expected ErrCoordinatorUnreachable after server stop, got: %v", err)
	}

	// Start second server on same socket
	listener2, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen on restart: %v", err)
	}
	srv2 := NewServer()
	srv2.Start(listener2)
	defer srv2.Stop()

	// Client should reconnect automatically
	err = client.Acquire(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if err != nil {
		t.Fatalf("acquire after restart error: %v", err)
	}
}

func TestGracefulFallbackTransition(t *testing.T) {
	// Test the transition: connected → lease → emergency → reconnected
	// This tests the lease fraction calculation and emergency cap independently

	reg := ratelimit.NewRegistry()
	cfg := reg.GetScopeConfig(ratelimit.ScopeUser)

	// 1. Full rate (connected)
	rate1, burst1 := cfg.TargetRate, cfg.BurstCapacity
	if rate1 != ratelimit.UserScopeRatePerSec {
		t.Errorf("full rate = %v, want %v", rate1, ratelimit.UserScopeRatePerSec)
	}

	// 2. Leased rate (1 of 2 clients)
	rate2, burst2 := CalculateLeaseFraction(cfg, 2)
	if rate2 != ratelimit.UserScopeRatePerSec/2 {
		t.Errorf("lease rate (2 clients) = %v, want %v", rate2, ratelimit.UserScopeRatePerSec/2)
	}
	if burst2 != ratelimit.UserScopeBurstCapacity/2 {
		t.Errorf("lease burst (2 clients) = %v, want %v", burst2, ratelimit.UserScopeBurstCapacity/2)
	}

	// 3. Emergency cap
	rate3, burst3 := EmergencyCap(cfg)
	if rate3 >= rate2 {
		t.Errorf("emergency rate %v should be less than lease rate %v", rate3, rate2)
	}
	if burst3 != 1 {
		t.Errorf("emergency burst = %v, want 1", burst3)
	}

	// 4. Recovery: back to full rate
	_ = rate1
	_ = burst1
}

func TestConcurrentMultiClientAcquire(t *testing.T) {
	clients, _, cleanup := startIntegrationServer(t, 5)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, client := range clients {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				err := c.Acquire(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
				if err != nil {
					return
				}
			}
		}(client)
	}
	wg.Wait()
	// If we get here without deadlock or panic, the test passes
}
