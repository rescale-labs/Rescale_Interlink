package coordinator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

// ErrCoordinatorUnreachable is returned when the coordinator cannot be reached.
// This is the ONLY error that triggers fallback to local rate limiting.
var ErrCoordinatorUnreachable = errors.New("coordinator unreachable")

// Client communicates with the coordinator server.
type Client struct {
	clientID   string
	timeout    time.Duration
	socketPath string

	mu     sync.Mutex
	leases map[string]*LeaseGrant // keyed by BucketKey.String()
	closed bool
}

// NewClient creates a coordinator client.
func NewClient() *Client {
	return &Client{
		clientID:   fmt.Sprintf("pid-%d", os.Getpid()),
		timeout:    500 * time.Millisecond,
		socketPath: SocketPath(),
		leases:     make(map[string]*LeaseGrant),
	}
}

// NewClientWithPath creates a coordinator client with a custom socket path.
// Used in tests.
func NewClientWithPath(socketPath string) *Client {
	return &Client{
		clientID:   fmt.Sprintf("pid-%d", os.Getpid()),
		timeout:    500 * time.Millisecond,
		socketPath: socketPath,
		leases:     make(map[string]*LeaseGrant),
	}
}

// Close marks the client as closed.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// sendRequest opens a connection, sends a request, and reads a response.
// Each request uses a new connection (same pattern as IPC client).
func (c *Client) sendRequest(ctx context.Context, req *Request) (*Response, error) {
	req.ClientID = c.clientID

	conn, err := c.dial(ctx)
	if err != nil {
		return nil, ErrCoordinatorUnreachable
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(c.timeout))

	// Encode and send
	data, err := req.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	data = append(data, '\n')

	if _, err := conn.Write(data); err != nil {
		return nil, ErrCoordinatorUnreachable
	}

	// Read response
	reader := bufio.NewReader(conn)
	respData, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, ErrCoordinatorUnreachable
	}

	resp, err := DecodeResponse(respData)
	if err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return resp, nil
}

// Acquire blocks until the coordinator grants a token or the context is cancelled.
// This is the critical path for cross-process rate limiting.
//
// Behavior:
//   - Sends Acquire to coordinator
//   - If Granted: returns nil
//   - If Wait: sleeps the indicated duration, then loops back
//   - On connection error: returns ErrCoordinatorUnreachable immediately
//
// The loop is essential for slow scopes like job-submission (0.139 req/sec = ~7s between tokens).
// A premature fallback would bypass the coordinator under normal operation.
func (c *Client) Acquire(ctx context.Context, baseURL, keyHash string, scope ratelimit.Scope) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := c.sendRequest(ctx, &Request{
			Type:    MsgAcquire,
			Scope:   scope,
			BaseURL: baseURL,
			KeyHash: keyHash,
		})
		if err != nil {
			return ErrCoordinatorUnreachable
		}

		switch resp.Type {
		case MsgGranted:
			return nil
		case MsgWait:
			// Sleep the indicated duration, respecting context cancellation
			if resp.WaitDuration <= 0 {
				resp.WaitDuration = time.Millisecond
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(resp.WaitDuration):
				// Loop back to re-acquire
			}
		case MsgError:
			return fmt.Errorf("coordinator error: %s", resp.Error)
		default:
			return fmt.Errorf("unexpected response type: %s", resp.Type)
		}
	}
}

// AcquireLease requests a leased budget from the coordinator.
func (c *Client) AcquireLease(ctx context.Context, baseURL, keyHash string, scope ratelimit.Scope) (*LeaseGrant, error) {
	resp, err := c.sendRequest(ctx, &Request{
		Type:    MsgAcquireLease,
		Scope:   scope,
		BaseURL: baseURL,
		KeyHash: keyHash,
	})
	if err != nil {
		return nil, err
	}

	if resp.Type == MsgLeaseGranted && resp.Lease != nil {
		key := BucketKey{BaseURL: baseURL, KeyHash: keyHash, Scope: scope}
		c.mu.Lock()
		c.leases[key.String()] = resp.Lease
		c.mu.Unlock()
		return resp.Lease, nil
	}

	if resp.Type == MsgError {
		return nil, fmt.Errorf("coordinator error: %s", resp.Error)
	}
	return nil, fmt.Errorf("unexpected response type: %s", resp.Type)
}

// Drain notifies the coordinator to drain the authoritative bucket.
// Fire-and-forget: errors are returned but callers typically ignore them.
func (c *Client) Drain(ctx context.Context, baseURL, keyHash string, scope ratelimit.Scope) error {
	_, err := c.sendRequest(ctx, &Request{
		Type:    MsgDrain,
		Scope:   scope,
		BaseURL: baseURL,
		KeyHash: keyHash,
	})
	return err
}

// SetCooldown notifies the coordinator to set a cooldown on the authoritative bucket.
// Fire-and-forget: errors are returned but callers typically ignore them.
func (c *Client) SetCooldown(ctx context.Context, baseURL, keyHash string, scope ratelimit.Scope, d time.Duration) error {
	_, err := c.sendRequest(ctx, &Request{
		Type:             MsgSetCooldown,
		Scope:            scope,
		BaseURL:          baseURL,
		KeyHash:          keyHash,
		CooldownDuration: d,
	})
	return err
}

// Heartbeat sends a liveness signal and refreshes leases.
func (c *Client) Heartbeat(ctx context.Context) error {
	_, err := c.sendRequest(ctx, &Request{
		Type: MsgHeartbeat,
	})
	return err
}

// GetState retrieves the coordinator's state (for status display).
func (c *Client) GetState(ctx context.Context) (*StateInfo, error) {
	resp, err := c.sendRequest(ctx, &Request{
		Type: MsgGetState,
	})
	if err != nil {
		return nil, err
	}
	if resp.State != nil {
		return resp.State, nil
	}
	if resp.Type == MsgError {
		return nil, fmt.Errorf("coordinator error: %s", resp.Error)
	}
	return nil, fmt.Errorf("unexpected response type: %s", resp.Type)
}

// Shutdown requests the coordinator to shut down gracefully.
func (c *Client) Shutdown(ctx context.Context) error {
	_, err := c.sendRequest(ctx, &Request{
		Type: MsgShutdown,
	})
	return err
}

// GetLease returns the current lease for a bucket key, if any.
func (c *Client) GetLease(baseURL, keyHash string, scope ratelimit.Scope) *LeaseGrant {
	key := BucketKey{BaseURL: baseURL, KeyHash: keyHash, Scope: scope}
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok := c.leases[key.String()]
	if !ok {
		return nil
	}
	// Check expiry
	if time.Now().After(lease.ExpiresAt) {
		delete(c.leases, key.String())
		return nil
	}
	return lease
}

// Ping checks if the coordinator is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.sendRequest(ctx, &Request{
		Type: MsgGetState,
	})
	return err
}

// dial is implemented in platform-specific files:
// - client_unix.go: Unix domain socket
// - client_windows.go: Windows named pipe
