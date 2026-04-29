//go:build internal

// Platform URL allowlist additions for internal (dev/staging) builds.
// Compiled in only when building with `-tags internal` (see Makefile:
// build-internal-*). Production builds exclude this file entirely, so
// dev/staging URLs are not reachable through validation or the GUI.
package config

func init() {
	AllowedPlatformURLs = append(AllowedPlatformURLs,
		PlatformURL{URL: "https://platform-stage.rescale.com", Label: "Staging"},
		PlatformURL{URL: "https://platform-dev.rescale.com", Label: "Dev"},
	)
}
