// Package wailsapp provides version checking Wails bindings.
package wailsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rescale/rescale-int/internal/http"
	"github.com/rescale/rescale-int/internal/version"
)

// VersionCheckDTO contains version update information
type VersionCheckDTO struct {
	HasUpdate      bool   `json:"hasUpdate"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	CurrentVersion string `json:"currentVersion"`
	ReleaseURL     string `json:"releaseUrl,omitempty"`
	CheckedAt      string `json:"checkedAt"`       // ISO timestamp
	Error          string `json:"error,omitempty"` // If check failed
}

// versionCache holds the cached version check result
type versionCache struct {
	mu         sync.RWMutex
	result     VersionCheckDTO
	lastCheck  time.Time
	cacheValid bool
}

var (
	versionCheckCache = &versionCache{}
	cacheDuration     = 24 * time.Hour
)

// GitHubRelease represents the GitHub API response structure
type GitHubRelease struct {
	TagName string `json:"tag_name"` // e.g., "v4.7.0"
	HTMLURL string `json:"html_url"` // e.g., "https://github.com/rescale-labs/Rescale_Interlink/releases/tag/v4.7.0"
}

// CheckForUpdates checks GitHub for newer releases.
// Returns cached result if checked within last 24 hours.
// This function respects proxy configuration and times out after 5 seconds.
func (a *App) CheckForUpdates() VersionCheckDTO {
	a.logInfo("version", "Checking for updates...")

	// Check cache first
	versionCheckCache.mu.RLock()
	if versionCheckCache.cacheValid && time.Since(versionCheckCache.lastCheck) < cacheDuration {
		a.logInfo("version", "Using cached version check result")
		result := versionCheckCache.result
		versionCheckCache.mu.RUnlock()
		return result
	}
	versionCheckCache.mu.RUnlock()

	// Create result DTO with current version
	result := VersionCheckDTO{
		HasUpdate:      false,
		CurrentVersion: version.Version,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	// Channel to receive result from worker goroutine
	resultChan := make(chan VersionCheckDTO, 1)

	// Run the API call in a goroutine with timeout
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Create HTTP client respecting proxy configuration
		httpClient, err := http.ConfigureHTTPClient(a.config)
		if err != nil {
			a.logError("version", fmt.Sprintf("Failed to create HTTP client: %v", err))
			result.Error = fmt.Sprintf("HTTP client error: %v", err)
			resultChan <- result
			return
		}

		// GitHub API endpoint for latest release
		url := "https://api.github.com/repos/rescale-labs/Rescale_Interlink/releases/latest"

		// Create request with context
		req, err := nethttp.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			a.logError("version", fmt.Sprintf("Failed to create request: %v", err))
			result.Error = fmt.Sprintf("Request creation error: %v", err)
			resultChan <- result
			return
		}

		// Set User-Agent header (GitHub API best practice)
		req.Header.Set("User-Agent", fmt.Sprintf("Rescale-Interlink/%s", version.Version))
		req.Header.Set("Accept", "application/vnd.github.v3+json")

		// Execute request
		resp, err := httpClient.Do(req)
		if err != nil {
			a.logError("version", fmt.Sprintf("GitHub API request failed: %v", err))
			result.Error = fmt.Sprintf("Network error: %v", err)
			resultChan <- result
			return
		}
		defer resp.Body.Close()

		// Check response status
		if resp.StatusCode != 200 {
			a.logError("version", fmt.Sprintf("GitHub API returned status %d", resp.StatusCode))
			result.Error = fmt.Sprintf("GitHub API error: status %d", resp.StatusCode)
			resultChan <- result
			return
		}

		// Parse JSON response
		var release GitHubRelease
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			a.logError("version", fmt.Sprintf("Failed to read response: %v", err))
			result.Error = fmt.Sprintf("Response read error: %v", err)
			resultChan <- result
			return
		}

		if err := json.Unmarshal(body, &release); err != nil {
			a.logError("version", fmt.Sprintf("Failed to parse JSON: %v", err))
			result.Error = fmt.Sprintf("JSON parse error: %v", err)
			resultChan <- result
			return
		}

		// Compare versions
		result.LatestVersion = release.TagName
		result.ReleaseURL = release.HTMLURL

		if compareVersions(version.Version, release.TagName) < 0 {
			result.HasUpdate = true
			a.logInfo("version", fmt.Sprintf("Update available: %s -> %s", version.Version, release.TagName))
		} else {
			a.logInfo("version", fmt.Sprintf("Current version %s is up to date", version.Version))
		}

		resultChan <- result
	}()

	// Wait for result or timeout
	select {
	case result = <-resultChan:
		// Success or handled error
	case <-time.After(6 * time.Second):
		a.logError("version", "Version check timed out after 6 seconds")
		result.Error = "Request timed out"
	}

	// Cache the result
	versionCheckCache.mu.Lock()
	versionCheckCache.result = result
	versionCheckCache.lastCheck = time.Now()
	versionCheckCache.cacheValid = true
	versionCheckCache.mu.Unlock()

	return result
}

// compareVersions compares two semantic version strings.
// Returns: -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
// Strips 'v' prefix and compares major.minor.patch numerically.
// Handles development versions (e.g., v4.6.8-dev) by stripping suffix.
func compareVersions(v1, v2 string) int {
	// Strip 'v' prefix
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")

	// Strip any suffix after hyphen (e.g., "-dev", "-beta")
	if idx := strings.Index(v1, "-"); idx != -1 {
		v1 = v1[:idx]
	}
	if idx := strings.Index(v2, "-"); idx != -1 {
		v2 = v2[:idx]
	}

	// Split into parts
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	// Compare each part numerically
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var p1, p2 int

		if i < len(parts1) {
			p1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) {
			p2, _ = strconv.Atoi(parts2[i])
		}

		if p1 < p2 {
			return -1
		}
		if p1 > p2 {
			return 1
		}
	}

	return 0
}
