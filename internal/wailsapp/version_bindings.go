// v4.8.2: Automatic version update notification.
// On GUI startup, checks GitHub releases for a newer version and surfaces
// a yellow badge in the header. Based on community contribution by @roque-rescale (PR #14).
package wailsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	inthttp "github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/version"
)

// VersionCheckDTO is the JSON-safe result of a version check.
type VersionCheckDTO struct {
	HasUpdate      bool   `json:"hasUpdate"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	CurrentVersion string `json:"currentVersion"`
	ReleaseURL     string `json:"releaseUrl,omitempty"`
	CheckedAt      string `json:"checkedAt"`
	Error          string `json:"error,omitempty"`
}

// versionCache holds the last version check result for in-memory caching.
// Cache is scoped to the current app session (cleared on restart).
type versionCache struct {
	mu         sync.RWMutex
	result     VersionCheckDTO
	lastCheck  time.Time
	cacheValid bool
}

// Cache durations: successes cached longer than errors to allow retry after transient failures.
var (
	versionCheckCache    = &versionCache{}
	successCacheDuration = 24 * time.Hour
	errorCacheDuration   = 1 * time.Hour
)

// In-flight dedup — same pattern as TestConnection in config_bindings.go.
var (
	checkInProgressMu sync.Mutex
	checkInProgress   bool
)

// releaseURL is the trusted URL opened when the user clicks the update badge.
// We never use API-provided URLs (html_url) to prevent open-redirect attacks.
const releaseURL = "https://github.com/rescale-labs/Rescale_Interlink/releases/latest"

// githubAPIURL is the GitHub API endpoint for the latest release.
const githubAPIURL = "https://api.github.com/repos/rescale-labs/Rescale_Interlink/releases/latest"

// githubRelease is the minimal subset of the GitHub release API response we parse.
type githubRelease struct {
	TagName string `json:"tag_name"`
}

// CheckForUpdates checks GitHub for a newer release version.
// Results are cached in-memory (24h for success, 1h for errors).
// Disabled automatically on FedRAMP platforms or when RESCALE_DISABLE_UPDATE_CHECK is set.
func (a *App) CheckForUpdates() VersionCheckDTO {
	// Policy gate: environment variable kill switch
	if envDisabled() {
		a.logDebug("version", "Update check disabled via RESCALE_DISABLE_UPDATE_CHECK")
		return VersionCheckDTO{
			CurrentVersion: version.Version,
			CheckedAt:      time.Now().UTC().Format(time.RFC3339),
		}
	}

	// Policy gate: FedRAMP platform detection
	if a.config != nil && config.IsFRMPlatform(a.config.APIBaseURL) {
		a.logDebug("version", "Update check disabled on FedRAMP platform")
		return VersionCheckDTO{
			CurrentVersion: version.Version,
			CheckedAt:      time.Now().UTC().Format(time.RFC3339),
		}
	}

	// Check cache
	versionCheckCache.mu.RLock()
	if versionCheckCache.cacheValid {
		cacheTTL := successCacheDuration
		if versionCheckCache.result.Error != "" {
			cacheTTL = errorCacheDuration
		}
		if time.Since(versionCheckCache.lastCheck) < cacheTTL {
			result := versionCheckCache.result
			versionCheckCache.mu.RUnlock()
			a.logDebug("version", "Returning cached version check result")
			return result
		}
	}
	versionCheckCache.mu.RUnlock()

	// In-flight dedup: if a check is already in progress, return cached (or empty) result
	checkInProgressMu.Lock()
	if checkInProgress {
		checkInProgressMu.Unlock()
		a.logDebug("version", "Version check already in progress")
		versionCheckCache.mu.RLock()
		if versionCheckCache.cacheValid {
			result := versionCheckCache.result
			versionCheckCache.mu.RUnlock()
			return result
		}
		versionCheckCache.mu.RUnlock()
		return VersionCheckDTO{
			CurrentVersion: version.Version,
			CheckedAt:      time.Now().UTC().Format(time.RFC3339),
		}
	}
	checkInProgress = true
	checkInProgressMu.Unlock()

	defer func() {
		checkInProgressMu.Lock()
		checkInProgress = false
		checkInProgressMu.Unlock()
	}()

	a.logInfo("version", "Checking for updates...")

	result := a.doVersionCheck()

	// Cache result
	versionCheckCache.mu.Lock()
	versionCheckCache.result = result
	versionCheckCache.lastCheck = time.Now()
	versionCheckCache.cacheValid = true
	versionCheckCache.mu.Unlock()

	if result.HasUpdate {
		a.logInfo("version", fmt.Sprintf("Update available: %s → %s", result.CurrentVersion, result.LatestVersion))
	} else if result.Error != "" {
		a.logWarn("version", fmt.Sprintf("Version check failed: %s", result.Error))
	} else {
		a.logInfo("version", fmt.Sprintf("Running latest version (%s)", result.CurrentVersion))
	}

	return result
}

// doVersionCheck performs the actual HTTP request to GitHub.
func (a *App) doVersionCheck() VersionCheckDTO {
	now := time.Now().UTC().Format(time.RFC3339)

	// Copy config with ProxyWarmup disabled — same pattern as TestConnection (config_bindings.go:299-311)
	var httpClient *http.Client
	if a.config != nil {
		configCopy := &config.Config{
			ProxyMode:     a.config.ProxyMode,
			ProxyHost:     a.config.ProxyHost,
			ProxyPort:     a.config.ProxyPort,
			ProxyUser:     a.config.ProxyUser,
			ProxyPassword: a.config.ProxyPassword,
			NoProxy:       a.config.NoProxy,
			ProxyWarmup:   false, // CRITICAL: Disable proxy warmup for version check
		}
		var err error
		httpClient, err = inthttp.ConfigureHTTPClient(configCopy)
		if err != nil {
			return VersionCheckDTO{
				CurrentVersion: version.Version,
				CheckedAt:      now,
				Error:          "failed to configure HTTP client: " + err.Error(),
			}
		}
	} else {
		httpClient = &http.Client{}
	}

	// 5-second timeout — generous enough for slow corporate proxies
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL, nil)
	if err != nil {
		return VersionCheckDTO{
			CurrentVersion: version.Version,
			CheckedAt:      now,
			Error:          "failed to create request: " + err.Error(),
		}
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "rescale-interlink/"+version.Version)

	resp, err := httpClient.Do(req)
	if err != nil {
		return VersionCheckDTO{
			CurrentVersion: version.Version,
			CheckedAt:      now,
			Error:          "request failed: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return VersionCheckDTO{
			CurrentVersion: version.Version,
			CheckedAt:      now,
			Error:          fmt.Sprintf("GitHub API returned status %d", resp.StatusCode),
		}
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return VersionCheckDTO{
			CurrentVersion: version.Version,
			CheckedAt:      now,
			Error:          "failed to parse response: " + err.Error(),
		}
	}

	if release.TagName == "" {
		return VersionCheckDTO{
			CurrentVersion: version.Version,
			CheckedAt:      now,
			Error:          "no tag_name in release response",
		}
	}

	hasUpdate := compareVersions(release.TagName, version.Version) > 0

	result := VersionCheckDTO{
		HasUpdate:      hasUpdate,
		LatestVersion:  release.TagName,
		CurrentVersion: version.Version,
		CheckedAt:      now,
	}
	if hasUpdate {
		result.ReleaseURL = releaseURL
	}
	return result
}

// envDisabled returns true if the update check is disabled via environment variable.
func envDisabled() bool {
	v := os.Getenv("RESCALE_DISABLE_UPDATE_CHECK")
	return v == "1" || strings.EqualFold(v, "true")
}

// compareVersions compares two semver-like version strings.
// Returns >0 if a > b, <0 if a < b, 0 if equal.
// Strips leading "v" prefix and "-dev"/"-beta"/etc. suffixes before comparison.
func compareVersions(a, b string) int {
	parseVersion := func(v string) []int {
		v = strings.TrimPrefix(v, "v")
		// Strip pre-release suffix (e.g., "-dev", "-beta.1")
		if idx := strings.IndexByte(v, '-'); idx != -1 {
			v = v[:idx]
		}
		parts := strings.Split(v, ".")
		nums := make([]int, len(parts))
		for i, p := range parts {
			n, _ := strconv.Atoi(p)
			nums[i] = n
		}
		return nums
	}

	aParts := parseVersion(a)
	bParts := parseVersion(b)

	// Pad shorter slice with zeros
	maxLen := len(aParts)
	if len(bParts) > maxLen {
		maxLen = len(bParts)
	}
	for len(aParts) < maxLen {
		aParts = append(aParts, 0)
	}
	for len(bParts) < maxLen {
		bParts = append(bParts, 0)
	}

	for i := 0; i < maxLen; i++ {
		if aParts[i] > bParts[i] {
			return 1
		}
		if aParts[i] < bParts[i] {
			return -1
		}
	}
	return 0
}
