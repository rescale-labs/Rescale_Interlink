// Package config provides configuration management for Rescale Interlink.
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// LogDirectory returns the unified log directory for all Interlink logs.
//
// Locations:
//   - Windows: %LOCALAPPDATA%\Rescale\Interlink\logs
//   - macOS and Linux: ~/.config/rescale/logs (spec §9.1 target; pinned
//     explicitly so macOS does not resolve to ~/Library/Application Support/
//     via os.UserConfigDir()).
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

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "rescale-interlink-logs")
	}
	return filepath.Join(homeDir, ".config", "rescale", "logs")
}

// MacOSLegacyLogDirectory returns the pre-Plan-2 macOS log directory
// (~/Library/Application Support/rescale/logs), used only by the one-time
// log-file migration in RunStartupMigrations.
func MacOSLegacyLogDirectory() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, "rescale", "logs")
}

// LogDirectoryForUser returns the log directory for a specific user profile.
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

// ReportDirectory returns the directory for error report files.
//
// Locations:
//   - Windows: %LOCALAPPDATA%\Rescale\Interlink\reports
//   - Unix: ~/.config/rescale/reports
func ReportDirectory() string {
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return filepath.Join(os.TempDir(), "rescale-interlink-reports")
			}
			localAppData = filepath.Join(homeDir, "AppData", "Local")
		}
		return filepath.Join(localAppData, "Rescale", "Interlink", "reports")
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "rescale-interlink-reports")
		}
		return filepath.Join(homeDir, ".config", "rescale", "reports")
	}
	return filepath.Join(configDir, "rescale", "reports")
}

// EnsureReportDirectory creates the report directory if it doesn't exist.
func EnsureReportDirectory() error {
	return os.MkdirAll(ReportDirectory(), 0700)
}
