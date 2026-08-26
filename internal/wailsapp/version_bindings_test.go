package wailsapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/version"
)

// =============================================================================
// compareVersions tests
// =============================================================================

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int // >0 if a>b, <0 if a<b, 0 if equal
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v2.0.0", "v1.0.0", 1},
		{"v1.0.0", "v2.0.0", -1},
		{"v1.1.0", "v1.0.0", 1},
		{"v1.0.1", "v1.0.0", 1},
		{"v1.0.0", "v1.0.1", -1},
		{"v4.8.2", "v4.8.1", 1},
		{"v4.8.1", "v4.8.2", -1},
		{"v4.8.2", "v4.8.2", 0},
		// Without v prefix
		{"4.8.2", "4.8.1", 1},
		{"4.8.2", "v4.8.2", 0},
		// Dev/beta suffixes stripped
		{"v4.8.2-dev", "v4.8.2", 0},
		{"v4.8.3-beta", "v4.8.2", 1},
		{"v4.8.1-rc1", "v4.8.2", -1},
		// Different segment lengths
		{"v1.0", "v1.0.0", 0},
		{"v1.0.0.1", "v1.0.0", 1},
		// Major version jump
		{"v5.0.0", "v4.99.99", 1},
		// Zero versions
		{"v0.0.1", "v0.0.0", 1},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.a, tt.b), func(t *testing.T) {
			got := compareVersions(tt.a, tt.b)
			if (tt.want > 0 && got <= 0) || (tt.want < 0 && got >= 0) || (tt.want == 0 && got != 0) {
				t.Errorf("compareVersions(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// =============================================================================
// URL safety tests
// =============================================================================

func TestReleaseURLIsTrusted(t *testing.T) {
	// The release URL must always be the hardcoded constant, never from API
	expected := "https://github.com/rescale-labs/Rescale_Interlink/releases/latest"
	if releaseURL != expected {
		t.Errorf("releaseURL = %q, want %q", releaseURL, expected)
	}
}

// =============================================================================
// Policy gate tests
// =============================================================================

func TestEnvDisabled(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"0", false},
		{"false", false},
		{"", false},
		{"yes", false},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			os.Setenv("RESCALE_DISABLE_UPDATE_CHECK", tt.value)
			defer os.Unsetenv("RESCALE_DISABLE_UPDATE_CHECK")

			got := envDisabled()
			if got != tt.want {
				t.Errorf("envDisabled() with %q = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestCheckForUpdatesDisabledByEnv(t *testing.T) {
	os.Setenv("RESCALE_DISABLE_UPDATE_CHECK", "1")
	defer os.Unsetenv("RESCALE_DISABLE_UPDATE_CHECK")

	app := &App{config: &config.Config{APIBaseURL: "https://platform.rescale.com"}}
	result := app.CheckForUpdates()

	if result.HasUpdate {
		t.Error("expected no update when check is disabled")
	}
	if result.CurrentVersion == "" {
		t.Error("expected CurrentVersion to be set")
	}
	if result.Error != "" {
		t.Errorf("expected no error, got: %s", result.Error)
	}
}

func TestCheckForUpdatesDisabledOnFedRAMP(t *testing.T) {
	// Reset cache to ensure fresh check
	resetVersionCache()

	app := &App{config: &config.Config{APIBaseURL: "https://rescale-gov.com"}}
	result := app.CheckForUpdates()

	if result.HasUpdate {
		t.Error("expected no update on FedRAMP platform")
	}
	if result.Error != "" {
		t.Errorf("expected no error, got: %s", result.Error)
	}
}

func TestCheckForUpdatesDisabledOnFedRAMPSubdomain(t *testing.T) {
	resetVersionCache()

	app := &App{config: &config.Config{APIBaseURL: "https://platform.rescale-gov.com"}}
	result := app.CheckForUpdates()

	if result.HasUpdate {
		t.Error("expected no update on FedRAMP subdomain")
	}
}

// =============================================================================
// HTTP behavior tests
// =============================================================================

// TestVersionCheckHTTPBehavior drives the real CheckForUpdates -> doVersionCheck
// path against a local server, covering the success, no-update, and every error
// response, plus the three result-reporting branches that follow the fetch.
func TestVersionCheckHTTPBehavior(t *testing.T) {
	t.Setenv("RESCALE_DISABLE_UPDATE_CHECK", "") // the env kill switch must be off

	tests := []struct {
		name           string
		status         int    // non-zero: reply with this status and no body
		body           string // non-empty: reply with this raw body instead of encoding tag
		tag            string
		wantErr        bool
		wantUpdate     bool
		wantReleaseURL string
	}{
		{name: "newer release", tag: "v99.0.0", wantUpdate: true, wantReleaseURL: releaseURL},
		{name: "older release", tag: "v0.0.1"},
		{name: "malformed json", body: "not json", wantErr: true},
		{name: "empty tag name", tag: "", wantErr: true},
		{name: "status 403", status: http.StatusForbidden, wantErr: true},
		{name: "status 404", status: http.StatusNotFound, wantErr: true},
		{name: "status 429", status: http.StatusTooManyRequests, wantErr: true},
		{name: "status 500", status: http.StatusInternalServerError, wantErr: true},
		{name: "status 502", status: http.StatusBadGateway, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Accept"); got != "application/vnd.github.v3+json" {
					t.Errorf("unexpected Accept header: %s", got)
				}
				switch {
				case tt.status != 0:
					w.WriteHeader(tt.status)
				case tt.body != "":
					_, _ = w.Write([]byte(tt.body))
				default:
					_ = json.NewEncoder(w).Encode(githubRelease{TagName: tt.tag})
				}
			}))
			defer server.Close()

			withGithubAPIURL(t, server.URL)
			resetVersionCache()
			t.Cleanup(resetVersionCache)

			app := &App{config: &config.Config{APIBaseURL: "https://platform.rescale.com"}}
			result := app.CheckForUpdates()

			if tt.wantErr {
				if result.Error == "" {
					t.Fatalf("expected error, got %+v", result)
				}
				return
			}
			if result.Error != "" {
				t.Fatalf("unexpected error: %s", result.Error)
			}
			if result.HasUpdate != tt.wantUpdate {
				t.Errorf("hasUpdate = %v, want %v", result.HasUpdate, tt.wantUpdate)
			}
			if result.LatestVersion != tt.tag {
				t.Errorf("latestVersion = %q, want %q", result.LatestVersion, tt.tag)
			}
			if result.ReleaseURL != tt.wantReleaseURL {
				t.Errorf("releaseURL = %q, want %q", result.ReleaseURL, tt.wantReleaseURL)
			}
			if result.CurrentVersion != version.Version {
				t.Errorf("currentVersion = %q, want %q", result.CurrentVersion, version.Version)
			}
		})
	}
}

// =============================================================================
// Cache behavior tests
// =============================================================================

func TestCacheDurations(t *testing.T) {
	if successCacheDuration != 24*time.Hour {
		t.Errorf("successCacheDuration = %v, want 24h", successCacheDuration)
	}
	if errorCacheDuration != 1*time.Hour {
		t.Errorf("errorCacheDuration = %v, want 1h", errorCacheDuration)
	}
}

func TestCacheHitPreventsHTTPCall(t *testing.T) {
	resetVersionCache()

	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		json.NewEncoder(w).Encode(githubRelease{TagName: "v99.0.0"})
	}))
	defer server.Close()
	withGithubAPIURL(t, server.URL)

	app := &App{config: &config.Config{APIBaseURL: "https://platform.rescale.com"}}

	// Pre-populate cache with a fresh successful result
	versionCheckCache.mu.Lock()
	versionCheckCache.result = VersionCheckDTO{
		HasUpdate:      true,
		LatestVersion:  "v99.0.0",
		CurrentVersion: "v4.8.2",
		ReleaseURL:     releaseURL,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	versionCheckCache.lastCheck = time.Now()
	versionCheckCache.cacheValid = true
	versionCheckCache.mu.Unlock()

	result := app.CheckForUpdates()

	if callCount.Load() != 0 {
		t.Errorf("expected 0 HTTP calls (cache hit), got %d", callCount.Load())
	}
	if !result.HasUpdate {
		t.Error("expected cached result to have HasUpdate=true")
	}
}

// =============================================================================
// In-flight dedup tests
// =============================================================================

func TestInFlightDedup(t *testing.T) {
	resetVersionCache()

	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		// Simulate slow response
		time.Sleep(100 * time.Millisecond)
		json.NewEncoder(w).Encode(githubRelease{TagName: "v99.0.0"})
	}))
	defer server.Close()
	withGithubAPIURL(t, server.URL)

	// Simulate in-flight check
	checkInProgressMu.Lock()
	checkInProgress = true
	checkInProgressMu.Unlock()

	app := &App{config: &config.Config{APIBaseURL: "https://platform.rescale.com"}}

	// This should return immediately without making an HTTP call
	result := app.CheckForUpdates()

	// Reset the flag
	checkInProgressMu.Lock()
	checkInProgress = false
	checkInProgressMu.Unlock()

	if callCount.Load() != 0 {
		t.Errorf("expected 0 HTTP calls during in-flight dedup, got %d", callCount.Load())
	}
	if result.CurrentVersion == "" {
		t.Error("expected CurrentVersion to be set even during dedup")
	}
}

// TestConcurrentChecksSingleHTTPCall verifies the in-flight dedup collapses
// concurrent CheckForUpdates calls into exactly one HTTP request: the winner
// fetches and caches, latecomers get the early-return or the cached result.
func TestConcurrentChecksSingleHTTPCall(t *testing.T) {
	resetVersionCache()
	t.Cleanup(resetVersionCache)

	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v99.0.0"})
	}))
	defer server.Close()
	withGithubAPIURL(t, server.URL)

	app := &App{config: &config.Config{APIBaseURL: "https://platform.rescale.com"}}

	var wg sync.WaitGroup
	results := make([]VersionCheckDTO, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = app.CheckForUpdates()
		}(i)
	}
	wg.Wait()

	if got := callCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 HTTP call for 5 concurrent checks, got %d", got)
	}
	for i, r := range results {
		if r.CurrentVersion == "" {
			t.Errorf("result[%d].CurrentVersion is empty", i)
		}
	}

	// The winning check must have populated the cache with the fetched release.
	versionCheckCache.mu.RLock()
	cached := versionCheckCache.result
	valid := versionCheckCache.cacheValid
	versionCheckCache.mu.RUnlock()
	if !valid {
		t.Fatal("cache should be valid after a completed check")
	}
	if cached.LatestVersion != "v99.0.0" || !cached.HasUpdate {
		t.Errorf("cached result = %+v, want HasUpdate with LatestVersion v99.0.0", cached)
	}
}

// =============================================================================
// Helpers
// =============================================================================

// resetVersionCache clears the version cache and in-flight flag for test isolation.
func resetVersionCache() {
	versionCheckCache.mu.Lock()
	versionCheckCache.result = VersionCheckDTO{}
	versionCheckCache.lastCheck = time.Time{}
	versionCheckCache.cacheValid = false
	versionCheckCache.mu.Unlock()

	checkInProgressMu.Lock()
	checkInProgress = false
	checkInProgressMu.Unlock()
}

// withGithubAPIURL points doVersionCheck at a test server for the duration of t.
func withGithubAPIURL(t *testing.T, url string) {
	t.Helper()
	orig := githubAPIURL
	githubAPIURL = url
	t.Cleanup(func() { githubAPIURL = orig })
}
