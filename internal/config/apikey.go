// Package config provides configuration management for Rescale Interlink.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var legacyKeyWarningOnce sync.Once

func warnLegacyAPIKeyOnce() {
	legacyKeyWarningOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "WARNING: API key loaded from legacy apiconfig file. "+
			"For improved security, run 'rescale-int config init' to migrate your key "+
			"to the token file, then delete the api_key entry from your apiconfig.\n")
	})
}

// ResolveAPIKey returns an API key by checking multiple sources in priority order.
// This provides consistent API key resolution across CLI, GUI, and auto-download.
//
// Priority (highest to lowest):
//  1. Provided apiKey parameter (if non-empty) - e.g., from --api-key flag
//  2. Per-user token file (when userProfilePath is provided)
//  3. apiconfig INI file (older versions stored the key there)
//  4. Default token file (~/.config/rescale/token) - created by 'config init' or GUI
//  5. RESCALE_API_KEY environment variable
//
// Parameters:
//   - apiKey: Explicitly provided API key (e.g., from command line flag)
//   - userProfilePath: User's profile directory for loading per-user token and apiconfig
//     (Windows: C:\Users\username, Unix: /home/username)
//     If empty, per-user token and apiconfig checks are skipped.
//
// Returns empty string if no API key found in any source.
func ResolveAPIKey(apiKey string, userProfilePath string) string {
	// 1. If explicitly provided, use it (highest priority)
	if apiKey != "" {
		return apiKey
	}

	// 2. Per-user token file
	if userProfilePath != "" {
		userTokenPath := GetUserTokenPath(userProfilePath)
		if key, err := ReadTokenFile(userTokenPath); err == nil && key != "" {
			return key
		}
	}

	// 3. Try apiconfig INI file
	if userProfilePath != "" {
		apiconfigPath := APIConfigPathForUser(userProfilePath)
		if cfg, err := LoadAPIConfig(apiconfigPath); err == nil && cfg.APIKey != "" {
			warnLegacyAPIKeyOnce()
			return cfg.APIKey
		}
	}

	// 4. Try default token file (~/.config/rescale/token)
	if tokenPath := GetDefaultTokenPath(); tokenPath != "" {
		if key, err := ReadTokenFile(tokenPath); err == nil && key != "" {
			return key
		}
	}

	// 5. Environment variable (lowest priority)
	return os.Getenv("RESCALE_API_KEY")
}

// ResolveAPIKeyForCurrentUser is a convenience wrapper around ResolveAPIKey
// that automatically determines the current user's profile path.
// This is useful for CLI commands and GUI where we want to check all sources
// for the current user.
//
// Priority (highest to lowest):
//  1. Provided apiKey parameter (if non-empty)
//  2. Per-user token file in current user's profile
//  3. apiconfig INI file in current user's profile
//  4. Default token file (~/.config/rescale/token)
//  5. RESCALE_API_KEY environment variable
func ResolveAPIKeyForCurrentUser(apiKey string) string {
	// Get current user's home directory to construct profile path
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fall back to checking without apiconfig
		return ResolveAPIKey(apiKey, "")
	}
	return ResolveAPIKey(apiKey, homeDir)
}

// GetUserTokenPath returns the token file path for a specific user profile.
//
//   - Windows: <userProfilePath>\AppData\Local\Rescale\Interlink\token. Falls
//     back to the Roaming location during the Plan 2 transition window.
//   - Unix: <userProfilePath>/.config/rescale/token.
func GetUserTokenPath(userProfilePath string) string {
	if userProfilePath == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		newPath := filepath.Join(userProfilePath, "AppData", "Local", "Rescale", "Interlink", "token")
		if _, err := os.Stat(newPath); err == nil {
			return newPath
		}
		oldPath := filepath.Join(userProfilePath, "AppData", "Roaming", "Rescale", "Interlink", "token")
		if _, err := os.Stat(oldPath); err == nil {
			return oldPath
		}
		return newPath
	}
	return filepath.Join(userProfilePath, ".config", ConfigDir, "token")
}
