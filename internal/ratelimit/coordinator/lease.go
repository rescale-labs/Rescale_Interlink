package coordinator

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

// Lease timing constants.
const (
	// LeaseDefaultTTL is the default time-to-live for a lease.
	// After this period without renewal, the lease expires and its
	// budget is reclaimed by the coordinator.
	LeaseDefaultTTL = 60 * time.Second

	// LeaseRefreshInterval is how often clients should send Heartbeat
	// to refresh their lease. Must be less than LeaseDefaultTTL.
	LeaseRefreshInterval = 30 * time.Second

	// StaleClientTimeout is how long without a heartbeat before a client
	// is considered stale and its resources are reclaimed.
	StaleClientTimeout = 2 * LeaseDefaultTTL
)

// GenerateLeaseID creates a random lease identifier.
func GenerateLeaseID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails
		return fmt.Sprintf("lease-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("lease-%x", b)
}

// CalculateLeaseFraction computes the rate and burst for a single client's lease.
// The coordinator divides the scope's target rate evenly among active clients.
//
// Parameters:
//   - cfg: the scope's rate limit configuration
//   - activeClients: number of clients currently holding leases for this bucket
//     (including the requesting client)
func CalculateLeaseFraction(cfg ratelimit.ScopeConfig, activeClients int) (rate float64, burst float64) {
	if activeClients <= 0 {
		activeClients = 1
	}
	rate = cfg.TargetRate / float64(activeClients)
	burst = cfg.BurstCapacity / float64(activeClients)
	return
}
