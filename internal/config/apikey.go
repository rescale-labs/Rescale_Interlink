// Package config provides configuration management for Rescale Interlink.
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// ResolveAPIKey returns an API key by checking multiple sources in priority order.
// This provides consistent API key resolution across CLI, GUI, and auto-download service.
//
// Priority (highest to lowest):
//  1. Provided apiKey parameter (if non-empty) - e.g., from --api-key flag
//  2. apiconfig INI file (for auto-download service compatibility)
//  3. Default token file (~/.config/rescale/token) - created by 'config init' or GUI
//  4. RESCALE_API_KEY environment variable
//
// Parameters:
//   - apiKey: Explicitly provided API key (e.g., from command line flag)
//   - userProfilePath: User's profile directory for loading apiconfig
//     (Windows: C:\Users\username, Unix: /home/username)
//     If empty, apiconfig is not checked.
//
// Returns empty string if no API key found in any source.
func ResolveAPIKey(apiKey string, userProfilePath string) string {
	// 1. If explicitly provided, use it (highest priority)
	if apiKey != "" {
		return apiKey
	}

	// 2. Try apiconfig INI file (for auto-download service compatibility)
	if userProfilePath != "" {
		apiconfigPath := APIConfigPathForUser(userProfilePath)
		if cfg, err := LoadAPIConfig(apiconfigPath); err == nil && cfg.APIKey != "" {
			return cfg.APIKey
		}
	}

	// 3. Try default token file (~/.config/rescale/token)
	if tokenPath := GetDefaultTokenPath(); tokenPath != "" {
		if key, err := ReadTokenFile(tokenPath); err == nil && key != "" {
			return key
		}
	}

	// 4. Environment variable (lowest priority)
	return os.Getenv("RESCALE_API_KEY")
}

// ResolveAPIKeyForCurrentUser is a convenience wrapper around ResolveAPIKey
// that automatically determines the current user's profile path.
// This is useful for CLI commands and GUI where we want to check all sources
// for the current user.
//
// Priority (highest to lowest):
//  1. Provided apiKey parameter (if non-empty)
//  2. apiconfig INI file in current user's profile
//  3. Default token file (~/.config/rescale/token)
//  4. RESCALE_API_KEY environment variable
func ResolveAPIKeyForCurrentUser(apiKey string) string {
	// Get current user's home directory to construct profile path
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fall back to checking without apiconfig
		return ResolveAPIKey(apiKey, "")
	}
	return ResolveAPIKey(apiKey, homeDir)
}

// ResolveAPIKeySource returns the API key and its source for debugging/logging.
// This is useful for CLI --verbose mode to show where the API key came from.
//
// Returns:
//   - apiKey: The resolved API key (empty if not found)
//   - source: Description of where the key was found
//     "flag", "apiconfig", "token-file", "environment", or "" if not found
func ResolveAPIKeySource(apiKey string, userProfilePath string) (string, string) {
	// 1. If explicitly provided, use it
	if apiKey != "" {
		return apiKey, "flag"
	}

	// 2. Try apiconfig INI file
	if userProfilePath != "" {
		apiconfigPath := APIConfigPathForUser(userProfilePath)
		if cfg, err := LoadAPIConfig(apiconfigPath); err == nil && cfg.APIKey != "" {
			return cfg.APIKey, "apiconfig"
		}
	}

	// 3. Try default token file
	if tokenPath := GetDefaultTokenPath(); tokenPath != "" {
		if key, err := ReadTokenFile(tokenPath); err == nil && key != "" {
			return key, "token-file"
		}
	}

	// 4. Environment variable
	if envKey := os.Getenv("RESCALE_API_KEY"); envKey != "" {
		return envKey, "environment"
	}

	return "", ""
}

// GetUserTokenPath returns the token file path for a specific user profile.
// This is used by the Windows service to find per-user token files.
// - Windows: <userProfilePath>\AppData\Roaming\Rescale\Interlink\token
// - Unix: <userProfilePath>/.config/rescale/token
func GetUserTokenPath(userProfilePath string) string {
	if userProfilePath == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		// v4.0.8: Use standard Windows AppData location
		return filepath.Join(userProfilePath, "AppData", "Roaming", "Rescale", "Interlink", "token")
	}
	return filepath.Join(userProfilePath, ".config", ConfigDir, "token")
}
