// Platform URL allowlist for credential exfiltration prevention.
package config

import (
	"fmt"
	"net/url"
	"strings"
)

// PlatformURL represents a known Rescale platform endpoint.
type PlatformURL struct {
	URL   string
	Label string
}

// AllowedPlatformURLs is the authoritative list of valid Rescale platform URLs.
// This list must stay in sync with the GUI dropdown in
// frontend/src/components/tabs/SetupTab.tsx (PLATFORM_URLS constant).
var AllowedPlatformURLs = []PlatformURL{
	{URL: "https://platform.rescale.com", Label: "North America"},
	{URL: "https://kr.rescale.com", Label: "Korea"},
	{URL: "https://platform.rescale.jp", Label: "Japan"},
	{URL: "https://eu.rescale.com", Label: "Europe"},
	{URL: "https://itar.rescale.com", Label: "US ITAR"},
	{URL: "https://itar.rescale-gov.com", Label: "US ITAR FRM"},
}

// DefaultPlatformURL is the default platform for new configurations.
var DefaultPlatformURL = AllowedPlatformURLs[0].URL

// ValidatePlatformURL checks if a URL is an exact approved HTTPS origin.
// Rejects: http:// scheme, custom ports, userinfo, path/query/fragment.
// Only the exact scheme+host of a known platform is accepted.
func ValidatePlatformURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("platform URL is empty")
	}

	// Normalize: add https:// if no scheme, trim trailing slash
	normalized := strings.TrimRight(rawURL, "/")
	if !strings.Contains(normalized, "://") {
		normalized = "https://" + normalized
	}

	u, err := url.Parse(normalized)
	if err != nil {
		return fmt.Errorf("invalid URL format: %w", err)
	}

	// Strict origin validation: must be HTTPS, no userinfo, no port, no path/query/fragment
	if u.Scheme != "https" {
		return fmt.Errorf("platform URL must use HTTPS (got %q)", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("platform URL must not contain userinfo")
	}
	if u.Port() != "" {
		return fmt.Errorf("platform URL must not specify a port")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("platform URL must not contain a path")
	}
	if u.RawQuery != "" {
		return fmt.Errorf("platform URL must not contain query parameters")
	}
	if u.Fragment != "" {
		return fmt.Errorf("platform URL must not contain a fragment")
	}

	// Check hostname against allowlist (case-insensitive)
	for _, allowed := range AllowedPlatformURLs {
		au, _ := url.Parse(allowed.URL)
		if strings.EqualFold(u.Hostname(), au.Hostname()) {
			return nil
		}
	}

	var list strings.Builder
	list.WriteString("unrecognized platform URL. Valid platforms:\n")
	for _, p := range AllowedPlatformURLs {
		fmt.Fprintf(&list, "  - %s (%s)\n", p.URL, p.Label)
	}
	return fmt.Errorf("%s", list.String())
}
