package coordinator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

func TestTwoClientsSharedBudget(t *testing.T) {
	clients, _, cleanup := startTestServers(t, 2)
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
	clients, _, cleanup := startTestServers(t, 2)
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
	clients, _, cleanup := startTestServers(t, 2)
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

func TestClientReconnectAfterCoordinatorRestart(t *testing.T) {
	sockPath := testEndpoint(t)

	// Start first server
	listener1, err := listenTest(sockPath)
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
	listener2, err := listenTest(sockPath)
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
	// The full rate a connected client runs at, and the fraction a lease grants.
	// The emergency cap is pinned by the store's own tests.

	reg := ratelimit.NewRegistry()
	cfg := reg.GetScopeConfig(ratelimit.ScopeUser)

	// 1. Full rate (connected)
	rate1 := cfg.TargetRate
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
}

func TestConcurrentMultiClientAcquire(t *testing.T) {
	clients, _, cleanup := startTestServers(t, 5)
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
