//go:build !internal

package config

import "testing"

// TestProductionBuildRejectsInternalURLs guards the build-tag gate: in a
// production build (no `internal` tag) the dev/staging URLs must NOT be
// in the allowlist, so validation rejects them and the GUI dropdown
// (which mirrors AllowedPlatformURLs) cannot offer them.
func TestProductionBuildRejectsInternalURLs(t *testing.T) {
	internalOnly := []string{
		"https://platform-stage.rescale.com",
		"https://platform-dev.rescale.com",
	}
	for _, u := range internalOnly {
		if err := ValidatePlatformURL(u); err == nil {
			t.Errorf("production build accepted %q — dev/staging URL leaked out of the internal build tag", u)
		}
	}
	for _, p := range AllowedPlatformURLs {
		if p.URL == "https://platform-stage.rescale.com" || p.URL == "https://platform-dev.rescale.com" {
			t.Errorf("AllowedPlatformURLs in production build contains internal URL %q", p.URL)
		}
	}
}
