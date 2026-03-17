package coordinator

import (
	"context"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

// clientAdapter wraps a coordinator *Client to implement the ratelimit.CoordinatorClient
// interface. This adapter bridges the type gap between the coordinator package's concrete
// types (e.g., *LeaseGrant) and the ratelimit package's interface types (e.g., *LeaseInfo).
type clientAdapter struct {
	client *Client
}

// Acquire delegates to the underlying Client.Acquire.
func (a *clientAdapter) Acquire(ctx context.Context, baseURL, keyHash string, scope ratelimit.Scope) error {
	return a.client.Acquire(ctx, baseURL, keyHash, scope)
}

// Drain delegates to the underlying Client.Drain.
func (a *clientAdapter) Drain(ctx context.Context, baseURL, keyHash string, scope ratelimit.Scope) error {
	return a.client.Drain(ctx, baseURL, keyHash, scope)
}

// SetCooldown delegates to the underlying Client.SetCooldown.
func (a *clientAdapter) SetCooldown(ctx context.Context, baseURL, keyHash string, scope ratelimit.Scope, d time.Duration) error {
	return a.client.SetCooldown(ctx, baseURL, keyHash, scope, d)
}

// GetLease converts the coordinator's *LeaseGrant to the ratelimit package's *LeaseInfo.
func (a *clientAdapter) GetLease(baseURL, keyHash string, scope ratelimit.Scope) *ratelimit.LeaseInfo {
	grant := a.client.GetLease(baseURL, keyHash, scope)
	if grant == nil {
		return nil
	}
	return &ratelimit.LeaseInfo{
		Rate:      grant.Rate,
		Burst:     grant.Burst,
		ExpiresAt: grant.ExpiresAt,
	}
}

// Ping delegates to the underlying Client.Ping.
func (a *clientAdapter) Ping(ctx context.Context) error {
	return a.client.Ping(ctx)
}

// EnsureCoordinatorClient connects to (or starts) the coordinator and returns
// a ratelimit.CoordinatorClient. This is the function passed to
// LimiterStore.SetCoordinatorEnsurer() at startup.
func EnsureCoordinatorClient() (ratelimit.CoordinatorClient, error) {
	client, err := EnsureCoordinator()
	if err != nil {
		return nil, err
	}
	return &clientAdapter{client: client}, nil
}
