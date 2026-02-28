// Package ratelimit provides rate limiting constants for Rescale API throttle scopes.
package ratelimit

import "time"

// Rescale API Throttle Limits
//
// Rescale's API uses scope-based throttling where endpoints are grouped into scopes,
// and each scope has a shared rate limit. All endpoints in a scope count against the
// same limit, regardless of which specific endpoint is called.
//
// Source: Rescale Platform Documentation
// Retrieved: November 2025

// Base rate limits (from Rescale documentation)
const (
	// UserScopeLimitPerHour is the rate limit for the "user" scope.
	// All /api/v3/* endpoints belong to this scope unless explicitly listed
	// in Rescale's throttle table with a different scope.
	// This is the DEFAULT scope for all v3 API calls.
	UserScopeLimitPerHour = 7200 // 2 requests per second

	// JobSubmissionLimitPerHour is the rate limit for job submission endpoints.
	// Only applies to: POST /api/v2/jobs/{id}/submit/
	JobSubmissionLimitPerHour = 1000 // 0.278 requests per second

	// JobsUsageLimitPerHour is the rate limit for v2 job endpoints (jobs-usage scope).
	// Applies to v2 job query/read operations (not submission).
	// Example: GET /api/v2/jobs/{id}/files/
	JobsUsageLimitPerHour = 90000 // 25 requests per second
)

// Target percentages
//
// We target 85% of the hard limit:
// 1. 15% safety margin prevents hitting the hard limit (which blocks access for extended time)
// 2. Maximizes throughput while accounting for concurrent operations and burst traffic
// 3. The 429 feedback system (CheckRetry + coordinator drain/cooldown) provides a safety net
//    if the server does reject requests
const (
	// UserScopeTargetPercent: Use 85% of the user scope limit
	// Rationale: 15% safety margin balances throughput and safety
	UserScopeTargetPercent = 85

	// JobSubmissionTargetPercent: Use 85% of the job submission limit
	// Rationale: With cross-process coordinator sharing the budget, 85% is safe
	JobSubmissionTargetPercent = 85

	// JobsUsageTargetPercent: Use 85% of the jobs-usage scope limit
	// Rationale: 15% safety margin, consistent with other scopes
	JobsUsageTargetPercent = 85
)

// Calculated target rates (requests per second)
//
// These are the actual rates our token bucket rate limiters use.
// 85% provides 15% safety margin while maximizing throughput.
const (
	// UserScopeRatePerSec is 85% of 2 req/sec = 1.7 req/sec
	// Used for all v3 API endpoints (files, folders, jobs, credentials, etc.)
	UserScopeRatePerSec = 1.7

	// JobSubmissionRatePerSec is 85% of 0.278 req/sec = 0.236 req/sec
	// Used only for POST /api/v2/jobs/{id}/submit/
	JobSubmissionRatePerSec = 0.236

	// JobsUsageRatePerSec is 85% of 25 req/sec = 21.25 req/sec
	// Used for v2 job query endpoints (GET /api/v2/jobs/{id}/files/, etc.)
	JobsUsageRatePerSec = 21.25
)

// Burst capacities (tokens)
//
// Burst capacity allows rapid initial operations before settling into sustained rate.
// Calculated as: tokens = duration_in_seconds × rate_per_second
const (
	// UserScopeBurstCapacity allows ~88 seconds of burst operations
	// Calculation: 150 tokens ÷ 1.7 req/sec = 88.2 seconds
	// This allows rapid file registration or downloads at startup without throttling
	UserScopeBurstCapacity = 150

	// JobSubmissionBurstCapacity allows ~212 seconds of burst operations
	// Calculation: 50 tokens ÷ 0.236 req/sec = 211.9 seconds
	// Sufficient for batch job submissions
	JobSubmissionBurstCapacity = 50

	// JobsUsageBurstCapacity allows ~14 seconds of burst operations
	// Calculation: 300 tokens ÷ 21.25 req/sec = 14.1 seconds
	// Allows rapid file listing at startup
	JobsUsageBurstCapacity = 300
)

// Visibility thresholds for utilization-based rate limit notifications.
//
// Hysteresis prevents flickering between warn and silent states:
//   - Warning activates when utilization >= UtilizationWarnThreshold (60%)
//   - Warning deactivates only when utilization drops below UtilizationSuppressThreshold (50%)
const (
	// UtilizationWarnThreshold is the utilization level above which warnings are emitted.
	UtilizationWarnThreshold = 0.60

	// UtilizationSuppressThreshold is the utilization level below which warnings are suppressed.
	// Must be less than UtilizationWarnThreshold to provide hysteresis.
	UtilizationSuppressThreshold = 0.50

	// NotifyMinInterval is the minimum time between consecutive notifications.
	// Prevents log spam during sustained high-utilization periods.
	NotifyMinInterval = 10 * time.Second
)

// Endpoint Scope Assignments
//
// Documentation of which Rescale API endpoints belong to which throttle scope.
// This is for reference only - the actual routing logic is in api/client.go
//
// USER SCOPE (7200/hour = 2 req/sec):
//   - POST /api/v3/credentials/
//   - GET  /api/v3/users/me/
//   - GET  /api/v3/users/me/folders/
//   - POST /api/v3/files/
//   - GET  /api/v3/files/{id}/
//   - GET  /api/v3/files/
//   - DELETE /api/v3/files/{id}/
//   - PATCH /api/v3/files/{id}/
//   - POST /api/v3/folders/{id}/
//   - GET  /api/v3/folders/{id}/contents/
//   - DELETE /api/v3/folders/{id}/
//   - POST /api/v3/jobs/
//   - GET  /api/v3/jobs/{id}/
//   - GET  /api/v3/jobs/
//   - POST /api/v3/jobs/{id}/stop/
//   - GET  /api/v3/jobs/{id}/statuses/
//   - GET  /api/v3/jobs/{id}/files/
//   - DELETE /api/v3/jobs/{id}/
//   - GET  /api/v3/coretypes/
//   - GET  /api/v3/analyses/
//
// JOB-SUBMISSION SCOPE (1000/hour = 0.278 req/sec):
//   - POST /api/v2/jobs/{id}/submit/
//
// JOBS-USAGE SCOPE (90000/hour = 25 req/sec):
//   - GET /api/v2/jobs/{id}/files/
//   - (other v2 job query endpoints)
//
// NOTE: We do NOT use other Rescale throttle scopes (file-access, credential-access,
// clusters-usage, runs-usage, billing-endpoint, login-method) because those endpoints
// are not currently needed by rescale-int.
