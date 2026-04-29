//go:build internal

package config

import "testing"

// TestInternalBuildAcceptsInternalURLs asserts the `internal` build-tag
// appends the dev/staging URLs to the allowlist so validation accepts
// them and the gated GUI dropdown can offer them.
func TestInternalBuildAcceptsInternalURLs(t *testing.T) {
	internalOnly := []string{
		"https://platform-stage.rescale.com",
		"https://platform-dev.rescale.com",
	}
	for _, u := range internalOnly {
		if err := ValidatePlatformURL(u); err != nil {
			t.Errorf("internal build rejected %q: %v", u, err)
		}
	}
}
