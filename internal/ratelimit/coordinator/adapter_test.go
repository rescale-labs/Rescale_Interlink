package coordinator

import (
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

// Compile-time interface checks: verify clientAdapter satisfies CoordinatorClient
// and EnsureCoordinatorClient satisfies CoordinatorEnsurer.
var _ ratelimit.CoordinatorClient = (*clientAdapter)(nil)
var _ ratelimit.CoordinatorEnsurer = EnsureCoordinatorClient

// TestGetLeaseConversion verifies LeaseGrant → LeaseInfo field mapping.
func TestGetLeaseConversion(t *testing.T) {
	expiry := time.Now().Add(60 * time.Second)
	client := NewClient()

	// Inject a lease directly into the client's lease map
	key := BucketKey{BaseURL: "https://example.com", KeyHash: "abc123", Scope: ratelimit.ScopeUser}
	client.mu.Lock()
	client.leases[key.String()] = &LeaseGrant{
		LeaseID:   "test-lease",
		Scope:     ratelimit.ScopeUser,
		Rate:      1.5,
		Burst:     10.0,
		ExpiresAt: expiry,
		RefreshBy: expiry.Add(-30 * time.Second),
	}
	client.mu.Unlock()

	adapter := &clientAdapter{client: client}
	info := adapter.GetLease("https://example.com", "abc123", ratelimit.ScopeUser)
	if info == nil {
		t.Fatal("GetLease returned nil, expected LeaseInfo")
	}

	if info.Rate != 1.5 {
		t.Errorf("Rate = %v, want 1.5", info.Rate)
	}
	if info.Burst != 10.0 {
		t.Errorf("Burst = %v, want 10.0", info.Burst)
	}
	if !info.ExpiresAt.Equal(expiry) {
		t.Errorf("ExpiresAt = %v, want %v", info.ExpiresAt, expiry)
	}
}

// TestGetLeaseNilPassthrough verifies nil is returned when no lease exists.
func TestGetLeaseNilPassthrough(t *testing.T) {
	client := NewClient()
	adapter := &clientAdapter{client: client}

	info := adapter.GetLease("https://example.com", "abc123", ratelimit.ScopeUser)
	if info != nil {
		t.Errorf("GetLease returned %v, expected nil for missing lease", info)
	}
}
