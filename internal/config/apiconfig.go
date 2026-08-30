// Package config provides configuration management for Rescale Interlink.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/ini.v1"
)

// APIConfig holds the platform URL, notification settings, and the legacy
// on-disk API key. Prefer ResolveAPIKey() for the key: this file is only one
// of its sources, and SaveAPIConfig never writes the key back.
//
// Config file location:
//   - Windows: %APPDATA%\Rescale\Interlink\apiconfig
//   - Unix: ~/.config/rescale/apiconfig
//
// INI format:
//
//	[interlink.notifications]
//	enabled = true
//	show_download_complete = true
//	show_download_failed = true
//
// NOTE: API key is read from this file for backwards compatibility with older versions
// that stored keys here. New code should use config.ResolveAPIKey() which prefers
// the token file. SaveAPIConfig intentionally does NOT write the API key —
// saves act as a migration step, stripping any legacy plaintext key from disk.
// Platform URL comes from the main config.csv or defaults to https://platform.rescale.com
type APIConfig struct {
	// PlatformURL is kept for backwards compatibility with existing apiconfig files.
	// New code should use the main config's APIBaseURL instead.
	// Deprecated: will be removed in future version.
	PlatformURL string `ini:"platform_url"`

	// APIKey is kept for backwards compatibility with existing apiconfig files.
	// New code should use config.ResolveAPIKey() instead.
	// Deprecated: will be removed in future version.
	APIKey string `ini:"api_key"`

	// Notification settings
	Notifications NotificationConfig
}

// NotificationConfig contains settings for desktop notifications.
type NotificationConfig struct {
	// Enabled indicates whether notifications are shown.
	// Default: true
	Enabled bool `ini:"enabled"`

	// ShowDownloadComplete shows a notification when a download completes.
	// Default: true
	ShowDownloadComplete bool `ini:"show_download_complete"`

	// ShowDownloadFailed shows a notification when a download fails.
	// Default: true
	ShowDownloadFailed bool `ini:"show_download_failed"`
}

// DefaultAPIConfigPath returns the default path for the apiconfig file.
// - Windows: %APPDATA%\Rescale\Interlink\apiconfig (standard Windows location)
// - Unix: ~/.config/rescale/apiconfig (XDG standard)
func DefaultAPIConfigPath() (string, error) {
	var configDir string

	if runtime.GOOS == "windows" {
		// This is C:\Users\<user>\AppData\Roaming\Rescale\Interlink
		appData := os.Getenv("APPDATA")
		if appData == "" {
			// Fallback to USERPROFILE if APPDATA not set (unusual)
			userProfile := os.Getenv("USERPROFILE")
			if userProfile == "" {
				return "", errors.New("neither APPDATA nor USERPROFILE environment variable set")
			}
			appData = filepath.Join(userProfile, "AppData", "Roaming")
		}
		configDir = filepath.Join(appData, "Rescale", "Interlink")
	} else {
		// On Unix, use ~/.config/rescale (XDG standard)
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		configDir = filepath.Join(home, ".config", "rescale")
	}

	return filepath.Join(configDir, "apiconfig"), nil
}

// APIConfigPathForUser returns the apiconfig path for a specific user profile directory.
// This is used by the Windows service to enumerate per-user configs.
// - Windows: <userProfileDir>\AppData\Roaming\Rescale\Interlink\apiconfig
// - Unix: <userProfileDir>/.config/rescale/apiconfig
func APIConfigPathForUser(userProfileDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(userProfileDir, "AppData", "Roaming", "Rescale", "Interlink", "apiconfig")
	}
	return filepath.Join(userProfileDir, ".config", "rescale", "apiconfig")
}

// NewAPIConfig creates a new APIConfig with default values.
func NewAPIConfig() *APIConfig {
	return &APIConfig{
		PlatformURL: "https://platform.rescale.com",
		Notifications: NotificationConfig{
			Enabled:              true,
			ShowDownloadComplete: true,
			ShowDownloadFailed:   true,
		},
	}
}

// LoadAPIConfig loads configuration from an INI file.
// If the file doesn't exist, returns a config with default values and no error.
// If the file exists but is invalid, returns an error.
func LoadAPIConfig(path string) (*APIConfig, error) {
	cfg := NewAPIConfig()

	// If no path provided, use default
	if path == "" {
		var err error
		path, err = DefaultAPIConfigPath()
		if err != nil {
			return cfg, nil // Return defaults if we can't determine path
		}
	}

	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil // Return defaults if config doesn't exist
	}

	// Load INI file
	iniFile, err := ini.Load(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load apiconfig: %w", err)
	}

	// Parse [rescale] section
	rescaleSection := iniFile.Section("rescale")
	cfg.PlatformURL = rescaleSection.Key("platform_url").MustString(cfg.PlatformURL)
	cfg.APIKey = rescaleSection.Key("api_key").String()

	// Parse [interlink.notifications] section
	notifySection := iniFile.Section("interlink.notifications")
	cfg.Notifications.Enabled = notifySection.Key("enabled").MustBool(true)
	cfg.Notifications.ShowDownloadComplete = notifySection.Key("show_download_complete").MustBool(true)
	cfg.Notifications.ShowDownloadFailed = notifySection.Key("show_download_failed").MustBool(true)

	return cfg, nil
}

// SaveAPIConfig saves configuration to an INI file.
// Creates parent directories if they don't exist.
// NOTE: The API key is intentionally NOT written to the file (security policy).
// This means saves will strip any legacy api_key that was previously in the file.
// LoadAPIConfig still reads legacy api_key values for backwards compatibility.
func SaveAPIConfig(cfg *APIConfig, path string) error {
	// If no path provided, use default
	if path == "" {
		var err error
		path, err = DefaultAPIConfigPath()
		if err != nil {
			return fmt.Errorf("failed to determine config path: %w", err)
		}
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Create INI file
	iniFile := ini.Empty()

	// Write [rescale] section
	rescaleSection, err := iniFile.NewSection("rescale")
	if err != nil {
		return fmt.Errorf("failed to create rescale section: %w", err)
	}
	rescaleSection.Key("platform_url").SetValue(cfg.PlatformURL)
	// API key intentionally NOT written — saves strip legacy plaintext keys from disk

	// Write [interlink.notifications] section
	notifySection, err := iniFile.NewSection("interlink.notifications")
	if err != nil {
		return fmt.Errorf("failed to create notifications section: %w", err)
	}
	notifySection.Key("enabled").SetValue(fmt.Sprintf("%t", cfg.Notifications.Enabled))
	notifySection.Key("show_download_complete").SetValue(fmt.Sprintf("%t", cfg.Notifications.ShowDownloadComplete))
	notifySection.Key("show_download_failed").SetValue(fmt.Sprintf("%t", cfg.Notifications.ShowDownloadFailed))

	// Save to file with restricted permissions (user read/write only)
	// Use temporary file + rename for atomicity
	tmpPath := path + ".tmp"
	if err := iniFile.SaveTo(tmpPath); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Set restrictive permissions (config may contain platform URL)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, 0600); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("failed to set config permissions: %w", err)
		}
	}

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}

// LoadCompatProfile reads API credentials from an INI config file's named profile section.
// This supports rescale-cli's --profile flag and multi-profile apiconfig files.
//
// Config file resolution: configPath > RESCALE_CONFIG_FILE env > DefaultAPIConfigPath()
// Returns ("", "", nil) if the file doesn't exist (not an error if simply absent).
// Returns an error if the file exists but the named section is missing.
func LoadCompatProfile(configPath, profileName string) (apiKey, baseURL string, err error) {
	if configPath == "" {
		configPath = os.Getenv("RESCALE_CONFIG_FILE")
	}
	if configPath == "" {
		var pathErr error
		configPath, pathErr = DefaultAPIConfigPath()
		if pathErr != nil {
			return "", "", nil
		}
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return "", "", nil
	}

	iniFile, err := ini.Load(configPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to load config file %s: %w", configPath, err)
	}

	section, err := iniFile.GetSection(profileName)
	if err != nil {
		return "", "", fmt.Errorf("profile section [%s] not found in %s", profileName, configPath)
	}

	// Try CLI key name first, fall back to INT key name
	apiKey = section.Key("apikey").String()
	if apiKey == "" {
		apiKey = section.Key("api_key").String()
	}

	// Try CLI URL key first, fall back to INT URL key
	baseURL = section.Key("apibaseurl").String()
	if baseURL == "" {
		baseURL = section.Key("platform_url").String()
	}

	return apiKey, baseURL, nil
}
