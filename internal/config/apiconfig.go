// Package config provides configuration management for Rescale Interlink.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/ini.v1"
)

// APIConfig represents the configuration contract between the GUI/CLI and the
// Windows Service auto-downloader. This is the sole configuration source for
// the auto-download service (clean break from legacy config.csv/token files).
//
// Config file location:
//   - Windows: %USERPROFILE%\.config\rescale\apiconfig
//   - Unix: ~/.config/rescale/apiconfig
//
// INI format:
//
//	[rescale]
//	platform_url = https://platform.rescale.com
//	api_key = <token-or-api-key>
//
//	[interlink.autoDownload]
//	enabled = true
//	correctness_tag = isCorrect:true
//	default_download_folder = A:\Rescale\Downloads
//	scan_interval_minutes = 10
//	lookback_days = 7
//
//	[interlink.notifications]
//	enabled = true
//	show_download_complete = true
//	show_download_failed = true
type APIConfig struct {
	// Rescale connection settings
	PlatformURL string `ini:"platform_url"`
	APIKey      string `ini:"api_key"`

	// Auto-download settings
	AutoDownload AutoDownloadConfig

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

// AutoDownloadConfig contains settings specific to the auto-download service.
type AutoDownloadConfig struct {
	// Enabled indicates whether auto-download is active for this user
	Enabled bool `ini:"enabled"`

	// CorrectnessTag is the job tag required for download eligibility.
	// Jobs must have this tag to be auto-downloaded.
	// Default: "isCorrect:true"
	CorrectnessTag string `ini:"correctness_tag"`

	// DefaultDownloadFolder is the base directory for downloaded job outputs.
	// Can be overridden per-job via the "Auto Download Path" custom field.
	DefaultDownloadFolder string `ini:"default_download_folder"`

	// ScanIntervalMinutes is the polling interval in minutes.
	// Minimum: 1, Maximum: 1440 (24 hours), Default: 10
	ScanIntervalMinutes int `ini:"scan_interval_minutes"`

	// LookbackDays is the number of days to look back for completed jobs.
	// Default: 7
	LookbackDays int `ini:"lookback_days"`
}

// Validation errors
var (
	ErrMissingPlatformURL      = errors.New("platform_url is required")
	ErrMissingAPIKey           = errors.New("api_key is required")
	ErrMissingDownloadFolder   = errors.New("default_download_folder is required when auto-download is enabled")
	ErrMissingCorrectnessTag   = errors.New("correctness_tag is required when auto-download is enabled")
	ErrInvalidScanInterval     = errors.New("scan_interval_minutes must be between 1 and 1440")
	ErrInvalidLookbackDays     = errors.New("lookback_days must be between 1 and 365")
)

// DefaultAPIConfigPath returns the default path for the apiconfig file.
// - Windows: %USERPROFILE%\.config\rescale\apiconfig
// - Unix: ~/.config/rescale/apiconfig
func DefaultAPIConfigPath() (string, error) {
	var configDir string

	if runtime.GOOS == "windows" {
		// On Windows, use %USERPROFILE%\.config\rescale
		userProfile := os.Getenv("USERPROFILE")
		if userProfile == "" {
			return "", errors.New("USERPROFILE environment variable not set")
		}
		configDir = filepath.Join(userProfile, ".config", "rescale")
	} else {
		// On Unix, use ~/.config/rescale
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
func APIConfigPathForUser(userProfileDir string) string {
	return filepath.Join(userProfileDir, ".config", "rescale", "apiconfig")
}

// NewAPIConfig creates a new APIConfig with default values.
func NewAPIConfig() *APIConfig {
	return &APIConfig{
		PlatformURL: "https://platform.rescale.com",
		AutoDownload: AutoDownloadConfig{
			Enabled:             false,
			CorrectnessTag:      "isCorrect:true",
			ScanIntervalMinutes: 10,
			LookbackDays:        7,
		},
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

	// Parse [interlink.autoDownload] section
	autoSection := iniFile.Section("interlink.autoDownload")
	cfg.AutoDownload.Enabled = autoSection.Key("enabled").MustBool(false)
	cfg.AutoDownload.CorrectnessTag = autoSection.Key("correctness_tag").MustString("isCorrect:true")
	cfg.AutoDownload.DefaultDownloadFolder = autoSection.Key("default_download_folder").String()
	cfg.AutoDownload.ScanIntervalMinutes = autoSection.Key("scan_interval_minutes").MustInt(10)
	cfg.AutoDownload.LookbackDays = autoSection.Key("lookback_days").MustInt(7)

	// Parse [interlink.notifications] section
	notifySection := iniFile.Section("interlink.notifications")
	cfg.Notifications.Enabled = notifySection.Key("enabled").MustBool(true)
	cfg.Notifications.ShowDownloadComplete = notifySection.Key("show_download_complete").MustBool(true)
	cfg.Notifications.ShowDownloadFailed = notifySection.Key("show_download_failed").MustBool(true)

	return cfg, nil
}

// SaveAPIConfig saves configuration to an INI file.
// Creates parent directories if they don't exist.
// The API key is stored in the file - ensure appropriate file permissions.
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
	rescaleSection.Key("api_key").SetValue(cfg.APIKey)

	// Write [interlink.autoDownload] section
	autoSection, err := iniFile.NewSection("interlink.autoDownload")
	if err != nil {
		return fmt.Errorf("failed to create autoDownload section: %w", err)
	}
	autoSection.Key("enabled").SetValue(fmt.Sprintf("%t", cfg.AutoDownload.Enabled))
	autoSection.Key("correctness_tag").SetValue(cfg.AutoDownload.CorrectnessTag)
	autoSection.Key("default_download_folder").SetValue(cfg.AutoDownload.DefaultDownloadFolder)
	autoSection.Key("scan_interval_minutes").SetValue(fmt.Sprintf("%d", cfg.AutoDownload.ScanIntervalMinutes))
	autoSection.Key("lookback_days").SetValue(fmt.Sprintf("%d", cfg.AutoDownload.LookbackDays))

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

	// Set restrictive permissions (API key is sensitive)
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

// Validate checks if the configuration is valid for the auto-download service.
// Returns nil if valid, or an error describing what's wrong.
func (cfg *APIConfig) Validate() error {
	// Always validate connection settings
	if strings.TrimSpace(cfg.PlatformURL) == "" {
		return ErrMissingPlatformURL
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return ErrMissingAPIKey
	}

	// Only validate auto-download settings if enabled
	if cfg.AutoDownload.Enabled {
		if strings.TrimSpace(cfg.AutoDownload.CorrectnessTag) == "" {
			return ErrMissingCorrectnessTag
		}
		if strings.TrimSpace(cfg.AutoDownload.DefaultDownloadFolder) == "" {
			return ErrMissingDownloadFolder
		}
		if cfg.AutoDownload.ScanIntervalMinutes < 1 || cfg.AutoDownload.ScanIntervalMinutes > 1440 {
			return ErrInvalidScanInterval
		}
		if cfg.AutoDownload.LookbackDays < 1 || cfg.AutoDownload.LookbackDays > 365 {
			return ErrInvalidLookbackDays
		}
	}

	return nil
}

// ValidateForConnection checks only the connection settings (platform_url and api_key).
// This is useful for validating before API calls, regardless of auto-download settings.
func (cfg *APIConfig) ValidateForConnection() error {
	if strings.TrimSpace(cfg.PlatformURL) == "" {
		return ErrMissingPlatformURL
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return ErrMissingAPIKey
	}
	return nil
}

// IsAutoDownloadEnabled returns true if auto-download is enabled and properly configured.
func (cfg *APIConfig) IsAutoDownloadEnabled() bool {
	return cfg.AutoDownload.Enabled && cfg.Validate() == nil
}

// GetScanInterval returns the scan interval as a duration string (e.g., "10m").
func (cfg *APIConfig) GetScanIntervalDuration() string {
	return fmt.Sprintf("%dm", cfg.AutoDownload.ScanIntervalMinutes)
}
