// Package ratelimit provides rate limiting for API calls using a token bucket algorithm.
package ratelimit

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Scope identifies a Rescale API throttle scope.
type Scope string

const (
	// ScopeUser is the default scope for all v3 API endpoints (7200/hour = 2 req/sec).
	ScopeUser Scope = "user"

	// ScopeJobSubmission is the scope for POST /api/v2/jobs/{id}/submit/ (1000/hour = 0.278 req/sec).
	ScopeJobSubmission Scope = "job-submission"

	// ScopeJobsUsage is the scope for v2 job query endpoints (90000/hour = 25 req/sec).
	ScopeJobsUsage Scope = "jobs-usage"
)

// ScopeConfig holds the rate limit configuration for a single scope.
type ScopeConfig struct {
	Scope         Scope
	HardLimitPerH float64 // Rescale's hard limit (requests per hour)
	HardLimitPerS float64 // Rescale's hard limit (requests per second), derived
	TargetRate    float64 // Our target rate (requests per second)
	TargetPercent float64 // Target as percentage of hard limit
	BurstCapacity float64 // Token bucket burst capacity
}

// EndpointRule maps an API endpoint pattern to its throttle scope.
// Rules are matched in order of specificity: longer patterns and method-specific
// rules take precedence over shorter/wildcard ones.
type EndpointRule struct {
	// Pattern is a path prefix to match against (e.g., "/api/v2/jobs/").
	// Matched using strings.Contains for flexibility with path parameters.
	Pattern string

	// Method is the HTTP method to match, or "" for any method.
	Method string

	// Scope is the throttle scope this endpoint belongs to.
	Scope Scope
}

// specificity returns a score for rule precedence. Higher = more specific.
// Method-specific rules get a bonus. Longer patterns are more specific.
func (r EndpointRule) specificity() int {
	score := len(r.Pattern)
	if r.Method != "" {
		score += 1000 // Method-specific rules always win over method-agnostic
	}
	return score
}

// Registry is the single source of truth for endpoint-to-scope mapping and
// per-scope rate limit configuration. All rate limiting decisions (request
// routing, retry-callback scope resolution, metrics) use this registry.
type Registry struct {
	// rules sorted by specificity descending (most specific first)
	rules []EndpointRule

	// scopeConfigs maps scope name to its rate limit configuration
	scopeConfigs map[Scope]ScopeConfig

	// defaultScope is used when no rule matches
	defaultScope Scope
}

// NewRegistry creates the global endpoint-scope registry with all known
// Rescale API endpoint rules and scope configurations.
//
// Scope configurations reference the constants defined in constants.go.
// Endpoint rules are ordered by specificity — longest/most-specific pattern wins.
func NewRegistry() *Registry {
	r := &Registry{
		defaultScope: ScopeUser,
		scopeConfigs: map[Scope]ScopeConfig{
			ScopeUser: {
				Scope:         ScopeUser,
				HardLimitPerH: UserScopeLimitPerHour,
				HardLimitPerS: float64(UserScopeLimitPerHour) / 3600.0,
				TargetRate:    UserScopeRatePerSec,
				TargetPercent: UserScopeTargetPercent,
				BurstCapacity: UserScopeBurstCapacity,
			},
			ScopeJobSubmission: {
				Scope:         ScopeJobSubmission,
				HardLimitPerH: JobSubmissionLimitPerHour,
				HardLimitPerS: float64(JobSubmissionLimitPerHour) / 3600.0,
				TargetRate:    JobSubmissionRatePerSec,
				TargetPercent: JobSubmissionTargetPercent,
				BurstCapacity: JobSubmissionBurstCapacity,
			},
			ScopeJobsUsage: {
				Scope:         ScopeJobsUsage,
				HardLimitPerH: JobsUsageLimitPerHour,
				HardLimitPerS: float64(JobsUsageLimitPerHour) / 3600.0,
				TargetRate:    JobsUsageRatePerSec,
				TargetPercent: JobsUsageTargetPercent,
				BurstCapacity: JobsUsageBurstCapacity,
			},
		},
	}

	// Endpoint rules. Added in any order — will be sorted by specificity.
	//
	// IMPORTANT: When adding new Rescale API endpoints, add a rule here.
	// The most specific matching rule wins (longest pattern + method match).
	r.rules = []EndpointRule{
		// Job submission — most restrictive scope (1000/hour)
		// Must match before the general /api/v2/jobs/ rule
		{Pattern: "/submit/", Method: http.MethodPost, Scope: ScopeJobSubmission},

		// v2 job query endpoints — jobs-usage scope (90000/hour)
		{Pattern: "/api/v2/jobs/", Method: "", Scope: ScopeJobsUsage},

		// All v3 endpoints — user scope (7200/hour) — this is the default,
		// listed explicitly for documentation and metrics clarity
		{Pattern: "/api/v3/", Method: "", Scope: ScopeUser},
	}

	// Sort rules by specificity descending (most specific first)
	sort.Slice(r.rules, func(i, j int) bool {
		return r.rules[i].specificity() > r.rules[j].specificity()
	})

	return r
}

// ResolveScope determines the throttle scope for a given HTTP method and path.
// Returns the most specific matching scope, or the default scope (ScopeUser)
// if no rule matches.
//
// This method is used by both doRequest() for pre-request rate limiting and
// by the CheckRetry callback for 429 feedback, ensuring consistent scope
// resolution across the request lifecycle.
func (r *Registry) ResolveScope(method, path string) Scope {
	for _, rule := range r.rules {
		// Check pattern match
		if !strings.Contains(path, rule.Pattern) {
			continue
		}
		// Check method match (empty = any method)
		if rule.Method != "" && !strings.EqualFold(rule.Method, method) {
			continue
		}
		return rule.Scope
	}
	return r.defaultScope
}

// GetScopeConfig returns the rate limit configuration for a scope.
// Returns the default scope config if the scope is not found.
func (r *Registry) GetScopeConfig(scope Scope) ScopeConfig {
	if cfg, ok := r.scopeConfigs[scope]; ok {
		return cfg
	}
	return r.scopeConfigs[r.defaultScope]
}

// AllScopes returns all configured scope names.
func (r *Registry) AllScopes() []Scope {
	scopes := make([]Scope, 0, len(r.scopeConfigs))
	for s := range r.scopeConfigs {
		scopes = append(scopes, s)
	}
	return scopes
}

// ScopeDisplayString returns a human-readable description of the scope for logging.
// Example: "user (v3 default, 7200/hour = 2.00/sec)"
func (r *Registry) ScopeDisplayString(scope Scope) string {
	cfg, ok := r.scopeConfigs[scope]
	if !ok {
		return string(scope) + " (unknown scope)"
	}
	return fmt.Sprintf("%s (%.0f/hour = %.2f/sec)", scope, cfg.HardLimitPerH, cfg.HardLimitPerS)
}
