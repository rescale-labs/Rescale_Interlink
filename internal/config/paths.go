// Package config provides configuration management for Rescale Interlink.
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// LogDirectory returns the unified log directory for all Interlink logs.
// v4.4.2: Centralized log path used by GUI, daemon, and tray.
//
// Locations:
//   - Windows: %LOCALAPPDATA%\Rescale\Interlink\logs
//   - Unix: ~/.config/rescale/logs
func LogDirectory() string {
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return filepath.Join(os.TempDir(), "rescale-interlink-logs")
			}
			localAppData = filepath.Join(homeDir, "AppData", "Local")
		}
		return filepath.Join(localAppData, "Rescale", "Interlink", "logs")
	}

	// Unix: Use XDG config directory
	configDir, err := os.UserConfigDir()
	if err != nil {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "rescale-interlink-logs")
		}
		return filepath.Join(homeDir, ".config", "rescale", "logs")
	}
	return filepath.Join(configDir, "rescale", "logs")
}

// EnsureLogDirectory creates the log directory if it doesn't exist.
// v4.4.2: Convenience function for initializing log directory.
// v4.5.1: Uses 0700 permissions to restrict log access to owner only.
func EnsureLogDirectory() error {
	return os.MkdirAll(LogDirectory(), 0700)
}

// LogDirectoryForUser returns the log directory for a specific user profile.
// v4.5.0: Used by multi-user service to store per-user daemon logs.
//
// On Windows, uses the user's profile path to construct the log directory:
//   - profilePath\AppData\Local\Rescale\Interlink\logs
func LogDirectoryForUser(profilePath string) string {
	if runtime.GOOS == "windows" {
		// Windows: profilePath\AppData\Local\Rescale\Interlink\logs
		return filepath.Join(profilePath, "AppData", "Local", "Rescale", "Interlink", "logs")
	}
	// Unix: Use profile-specific config directory
	return filepath.Join(profilePath, ".config", "rescale", "logs")
}
