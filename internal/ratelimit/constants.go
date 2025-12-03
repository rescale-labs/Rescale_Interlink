// Package ratelimit provides rate limiting constants for Rescale API throttle scopes.
package ratelimit

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

// Conservative target percentages
//
// We target a percentage BELOW the hard limit to:
// 1. Prevent hitting the hard limit (which blocks access for extended time)
// 2. Account for concurrent operations and burst traffic
// 3. Provide safety margin for credential refreshes and other overhead
const (
	// UserScopeTargetPercent: Use 80% of the user scope limit
	// Rationale: 20% safety margin prevents throttle lockouts during concurrent uploads/downloads
	UserScopeTargetPercent = 80

	// JobSubmissionTargetPercent: Use 50% of the job submission limit
	// Rationale: Job submission is infrequent, so conservative limit is fine
	JobSubmissionTargetPercent = 50

	// JobsUsageTargetPercent: Use 80% of the jobs-usage scope limit
	// Rationale: 20% safety margin, same as user scope
	JobsUsageTargetPercent = 80
)

// Calculated target rates (requests per second)
//
// These are the actual rates our token bucket rate limiters use.
const (
	// UserScopeRatePerSec is 80% of 2 req/sec = 1.6 req/sec
	// Used for all v3 API endpoints (files, folders, jobs, credentials, etc.)
	UserScopeRatePerSec = 1.6

	// JobSubmissionRatePerSec is 50% of 0.278 req/sec = 0.139 req/sec
	// Used only for POST /api/v2/jobs/{id}/submit/
	JobSubmissionRatePerSec = 0.139

	// JobsUsageRatePerSec is 80% of 25 req/sec = 20 req/sec
	// Used for v2 job query endpoints (GET /api/v2/jobs/{id}/files/, etc.)
	JobsUsageRatePerSec = 20.0
)

// Burst capacities (tokens)
//
// Burst capacity allows rapid initial operations before settling into sustained rate.
// Calculated as: tokens = duration_in_seconds × rate_per_second
const (
	// UserScopeBurstCapacity allows ~93 seconds of burst operations
	// Calculation: 150 tokens ÷ 1.6 req/sec = 93.75 seconds
	// This allows rapid file registration or downloads at startup without throttling
	UserScopeBurstCapacity = 150

	// JobSubmissionBurstCapacity allows ~360 seconds of burst operations
	// Calculation: 50 tokens ÷ 0.139 req/sec = 359.7 seconds
	// Sufficient for batch job submissions
	JobSubmissionBurstCapacity = 50

	// JobsUsageBurstCapacity allows ~15 seconds of burst operations
	// Calculation: 300 tokens ÷ 20 req/sec = 15 seconds
	// Allows rapid file listing at startup
	JobsUsageBurstCapacity = 300
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
