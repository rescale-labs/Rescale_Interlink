package ratelimit

import (
	"net/http"
	"testing"
)

func TestResolveScopeV3Default(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		method string
		path   string
		want   Scope
	}{
		{"GET", "/api/v3/users/me/", ScopeUser},
		{"POST", "/api/v3/credentials/", ScopeUser},
		{"GET", "/api/v3/files/abc123/", ScopeUser},
		{"POST", "/api/v3/files/", ScopeUser},
		{"DELETE", "/api/v3/files/abc123/", ScopeUser},
		{"PATCH", "/api/v3/files/abc123/", ScopeUser},
		{"GET", "/api/v3/folders/abc123/contents/", ScopeUser},
		{"POST", "/api/v3/jobs/", ScopeUser},
		{"GET", "/api/v3/jobs/abc123/", ScopeUser},
		{"GET", "/api/v3/jobs/abc123/statuses/", ScopeUser},
		{"GET", "/api/v3/jobs/abc123/files/", ScopeUser},
		{"POST", "/api/v3/jobs/abc123/stop/", ScopeUser},
		{"GET", "/api/v3/coretypes/", ScopeUser},
		{"GET", "/api/v3/analyses/", ScopeUser},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			got := r.ResolveScope(tt.method, tt.path)
			if got != tt.want {
				t.Errorf("ResolveScope(%q, %q) = %q, want %q", tt.method, tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveScopeJobSubmission(t *testing.T) {
	r := NewRegistry()

	// POST /api/v2/jobs/{id}/submit/ → job-submission scope
	got := r.ResolveScope(http.MethodPost, "/api/v2/jobs/abc123/submit/")
	if got != ScopeJobSubmission {
		t.Errorf("POST submit: got %q, want %q", got, ScopeJobSubmission)
	}

	// GET on the same path should NOT match job-submission (method-specific rule)
	// It should fall through to the /api/v2/jobs/ rule → jobs-usage
	got = r.ResolveScope(http.MethodGet, "/api/v2/jobs/abc123/submit/")
	if got != ScopeJobsUsage {
		t.Errorf("GET submit: got %q, want %q", got, ScopeJobsUsage)
	}
}

func TestResolveScopeJobsUsage(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v2/jobs/abc123/files/"},
		{"GET", "/api/v2/jobs/abc123/"},
		{"GET", "/api/v2/jobs/"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			got := r.ResolveScope(tt.method, tt.path)
			if got != ScopeJobsUsage {
				t.Errorf("ResolveScope(%q, %q) = %q, want %q", tt.method, tt.path, got, ScopeJobsUsage)
			}
		})
	}
}

func TestResolveScopeUnknownPath(t *testing.T) {
	r := NewRegistry()

	// Unknown paths should fall back to user scope (default)
	got := r.ResolveScope("GET", "/api/v4/something/new/")
	if got != ScopeUser {
		t.Errorf("unknown path: got %q, want %q", got, ScopeUser)
	}
}

func TestResolveScopePrecedence(t *testing.T) {
	r := NewRegistry()

	// POST /api/v2/jobs/{id}/submit/ matches BOTH:
	//   - "/submit/" (method=POST, specificity=1000+8=1008)
	//   - "/api/v2/jobs/" (method="", specificity=14)
	// The submit rule should win due to method specificity bonus.
	got := r.ResolveScope(http.MethodPost, "/api/v2/jobs/abc123/submit/")
	if got != ScopeJobSubmission {
		t.Errorf("precedence: got %q, want %q", got, ScopeJobSubmission)
	}
}

func TestGetScopeConfig(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		scope     Scope
		wantRate  float64
		wantBurst float64
	}{
		{ScopeUser, UserScopeRatePerSec, UserScopeBurstCapacity},
		{ScopeJobSubmission, JobSubmissionRatePerSec, JobSubmissionBurstCapacity},
		{ScopeJobsUsage, JobsUsageRatePerSec, JobsUsageBurstCapacity},
	}

	for _, tt := range tests {
		t.Run(string(tt.scope), func(t *testing.T) {
			cfg := r.GetScopeConfig(tt.scope)
			if cfg.TargetRate != tt.wantRate {
				t.Errorf("TargetRate = %v, want %v", cfg.TargetRate, tt.wantRate)
			}
			if cfg.BurstCapacity != tt.wantBurst {
				t.Errorf("BurstCapacity = %v, want %v", cfg.BurstCapacity, tt.wantBurst)
			}
		})
	}
}

func TestGetScopeConfigUnknown(t *testing.T) {
	r := NewRegistry()

	// Unknown scope should return the default (user) config
	cfg := r.GetScopeConfig(Scope("nonexistent"))
	if cfg.Scope != ScopeUser {
		t.Errorf("unknown scope: got %q, want %q", cfg.Scope, ScopeUser)
	}
}

func TestAllScopes(t *testing.T) {
	r := NewRegistry()
	scopes := r.AllScopes()

	if len(scopes) != 3 {
		t.Fatalf("AllScopes() returned %d scopes, want 3", len(scopes))
	}

	found := make(map[Scope]bool)
	for _, s := range scopes {
		found[s] = true
	}
	for _, want := range []Scope{ScopeUser, ScopeJobSubmission, ScopeJobsUsage} {
		if !found[want] {
			t.Errorf("AllScopes() missing %q", want)
		}
	}
}

func TestScopeDisplayString(t *testing.T) {
	r := NewRegistry()

	// Should not panic and should contain scope name
	for _, scope := range r.AllScopes() {
		s := r.ScopeDisplayString(scope)
		if s == "" {
			t.Errorf("ScopeDisplayString(%q) returned empty string", scope)
		}
	}

	// Unknown scope
	s := r.ScopeDisplayString(Scope("bogus"))
	if s == "" {
		t.Error("ScopeDisplayString for unknown scope returned empty string")
	}
}

func TestRulesAreSortedBySpecificity(t *testing.T) {
	r := NewRegistry()

	for i := 1; i < len(r.rules); i++ {
		if r.rules[i].specificity() > r.rules[i-1].specificity() {
			t.Errorf("rules not sorted: rule[%d] (specificity=%d) > rule[%d] (specificity=%d)",
				i, r.rules[i].specificity(), i-1, r.rules[i-1].specificity())
		}
	}
}
