package coordinator

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

// startTestServers creates a server on a temp socket, starts it, and returns
// numClients clients connected to it plus a cleanup function.
func startTestServers(t *testing.T, numClients int) ([]*Client, *Server, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "coordinator-test")
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
	for i := range clients {
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

// startTestServer is the single-client form of startTestServers.
func startTestServer(t *testing.T) (*Client, *Server, func()) {
	t.Helper()
	clients, srv, cleanup := startTestServers(t, 1)
	return clients[0], srv, cleanup
}

func TestClientConnect(t *testing.T) {
	client, _, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := client.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping() error: %v", err)
	}
}

func TestClientAcquireSuccess(t *testing.T) {
	client, _, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := client.Acquire(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
}

func TestClientAcquireWaitAndRetry(t *testing.T) {
	client, srv, cleanup := startTestServer(t)
	defer cleanup()

	// Drain the server bucket first
	drainReq := newTestRequest(MsgDrain, ratelimit.ScopeUser)
	srv.HandleRequest(drainReq)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	err := client.Acquire(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}

	// Should have waited for token refill (user scope: 1.6/sec = ~625ms per token)
	if elapsed < 200*time.Millisecond {
		t.Errorf("Acquire() returned too quickly after drain: %v", elapsed)
	}
}

func TestClientFallbackOnDisconnect(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "coordinator-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "nonexistent.sock")
	client := NewClientWithPath(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err = client.Acquire(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if err != ErrCoordinatorUnreachable {
		t.Errorf("expected ErrCoordinatorUnreachable, got: %v", err)
	}
}

func TestClientAcquireLease(t *testing.T) {
	client, _, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	lease, err := client.AcquireLease(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if err != nil {
		t.Fatalf("AcquireLease() error: %v", err)
	}
	if lease == nil {
		t.Fatal("lease is nil")
	}
	if lease.LeaseID == "" {
		t.Error("LeaseID should not be empty")
	}
	if lease.Rate <= 0 {
		t.Errorf("Rate should be positive, got %v", lease.Rate)
	}
}

func TestClientDrain(t *testing.T) {
	client, _, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := client.Drain(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if err != nil {
		t.Fatalf("Drain() error: %v", err)
	}
}

func TestClientSetCooldown(t *testing.T) {
	client, _, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := client.SetCooldown(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser, 5*time.Second)
	if err != nil {
		t.Fatalf("SetCooldown() error: %v", err)
	}
}

func TestClientGetState(t *testing.T) {
	client, _, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	state, err := client.GetState(ctx)
	if err != nil {
		t.Fatalf("GetState() error: %v", err)
	}
	if state == nil {
		t.Fatal("state is nil")
	}
}

func TestClientGetLease(t *testing.T) {
	client, _, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// No lease initially
	lease := client.GetLease("https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if lease != nil {
		t.Error("should have no lease initially")
	}

	// Acquire a lease
	_, err := client.AcquireLease(ctx, "https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if err != nil {
		t.Fatalf("AcquireLease() error: %v", err)
	}

	// Now should have a lease
	lease = client.GetLease("https://platform.rescale.com", "abcdef01", ratelimit.ScopeUser)
	if lease == nil {
		t.Error("should have a lease after AcquireLease")
	}
}
