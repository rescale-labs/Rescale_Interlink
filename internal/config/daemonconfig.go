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

// DaemonConfig represents the unified daemon configuration.
// This is the v4.2.1+ configuration format, replacing the older apiconfig format.
//
// Config file location:
//   - Windows: %APPDATA%\Rescale\Interlink\daemon.conf
//   - Unix: ~/.config/rescale/daemon.conf
//
// INI format:
//
//	[daemon]
//	enabled = true
//	download_folder = /Users/me/Downloads/rescale-jobs
//	poll_interval_minutes = 5
//	use_job_name_dir = true
//	max_concurrent = 5
//	lookback_days = 7
//
//	[filters]
//	name_prefix =
//	name_contains =
//	exclude = test,debug,scratch
//
//	[eligibility]
//	correctness_tag = isCorrect:true
//	auto_download_value = Enable
//	downloaded_tag = autoDownloaded:true
//
//	[notifications]
//	enabled = true
//	show_download_complete = true
//	show_download_failed = true
type DaemonConfig struct {
	// Daemon core settings
	Daemon DaemonCoreConfig

	// Job filters
	Filters FilterConfig

	// Eligibility requirements
	Eligibility EligibilityConfig

	// Notification settings
	Notifications NotificationConfig
}

// DaemonCoreConfig contains core daemon settings.
type DaemonCoreConfig struct {
	// Enabled indicates whether auto-download is active.
	// Default: false
	Enabled bool `ini:"enabled"`

	// DownloadFolder is the base directory for downloaded job outputs.
	// Default: ~/Downloads/rescale-jobs (Unix) or %USERPROFILE%\Downloads\rescale-jobs (Windows)
	DownloadFolder string `ini:"download_folder"`

	// PollIntervalMinutes is the polling interval in minutes.
	// Minimum: 1, Maximum: 1440 (24 hours), Default: 5
	PollIntervalMinutes int `ini:"poll_interval_minutes"`

	// UseJobNameDir creates subdirectories named after job names instead of job IDs.
	// Default: true
	UseJobNameDir bool `ini:"use_job_name_dir"`

	// MaxConcurrent is the maximum number of concurrent file downloads per job.
	// Minimum: 1, Maximum: 10, Default: 5
	MaxConcurrent int `ini:"max_concurrent"`

	// LookbackDays is the number of days to look back for completed jobs.
	// Minimum: 1, Maximum: 365, Default: 7
	LookbackDays int `ini:"lookback_days"`
}

// FilterConfig contains job name filtering settings.
type FilterConfig struct {
	// NamePrefix only downloads jobs with names starting with this prefix.
	// Empty means no prefix filter.
	NamePrefix string `ini:"name_prefix"`

	// NameContains only downloads jobs with names containing this string.
	// Empty means no contains filter.
	NameContains string `ini:"name_contains"`

	// Exclude is a comma-separated list of patterns to exclude.
	// Jobs matching any of these patterns will be skipped.
	Exclude string `ini:"exclude"`
}

// EligibilityConfig contains job eligibility requirements.
type EligibilityConfig struct {
	// CorrectnessTag is the job tag required for download eligibility.
	// Jobs must have this tag to be auto-downloaded.
	// Default: "isCorrect:true"
	CorrectnessTag string `ini:"correctness_tag"`

	// AutoDownloadValue is the required value for the "Auto Download" custom field.
	// Jobs must have this exact value (case-insensitive) in the custom field.
	// The field NAME is hardcoded as "Auto Download" and must be created in the Rescale workspace.
	// Default: "Enable"
	AutoDownloadValue string `ini:"auto_download_value"`

	// DownloadedTag is the tag added to jobs after successful download.
	// This prevents jobs from being re-downloaded.
	// Default: "autoDownloaded:true"
	DownloadedTag string `ini:"downloaded_tag"`
}

// DaemonConfig validation errors
var (
	ErrDaemonMissingDownloadFolder = errors.New("download_folder is required when daemon is enabled")
	ErrDaemonInvalidPollInterval   = errors.New("poll_interval_minutes must be between 1 and 1440")
	ErrDaemonInvalidMaxConcurrent  = errors.New("max_concurrent must be between 1 and 10")
	ErrDaemonInvalidLookbackDays   = errors.New("lookback_days must be between 1 and 365")
)

// DefaultDaemonConfigPath returns the default path for the daemon.conf file.
//   - Windows: %APPDATA%\Rescale\Interlink\daemon.conf
//   - Unix: ~/.config/rescale/daemon.conf
func DefaultDaemonConfigPath() (string, error) {
	var configDir string

	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			userProfile := os.Getenv("USERPROFILE")
			if userProfile == "" {
				return "", errors.New("neither APPDATA nor USERPROFILE environment variable set")
			}
			appData = filepath.Join(userProfile, "AppData", "Roaming")
		}
		configDir = filepath.Join(appData, "Rescale", "Interlink")
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		configDir = filepath.Join(home, ".config", "rescale")
	}

	return filepath.Join(configDir, "daemon.conf"), nil
}

// DaemonConfigPathForUser returns the daemon.conf path for a specific user profile directory.
// This is used by the Windows service to enumerate per-user configs.
//   - Windows: <userProfileDir>\AppData\Roaming\Rescale\Interlink\daemon.conf
//   - Unix: <userProfileDir>/.config/rescale/daemon.conf
func DaemonConfigPathForUser(userProfileDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(userProfileDir, "AppData", "Roaming", "Rescale", "Interlink", "daemon.conf")
	}
	return filepath.Join(userProfileDir, ".config", "rescale", "daemon.conf")
}

// DefaultDownloadFolder returns the platform-specific default download folder.
func DefaultDownloadFolder() string {
	home, err := os.UserHomeDir()
	if err != nil {
		if runtime.GOOS == "windows" {
			return "C:\\Downloads\\rescale-jobs"
		}
		return "/tmp/rescale-jobs"
	}
	return filepath.Join(home, "Downloads", "rescale-jobs")
}

// NewDaemonConfig creates a new DaemonConfig with default values.
func NewDaemonConfig() *DaemonConfig {
	return &DaemonConfig{
		Daemon: DaemonCoreConfig{
			Enabled:             false,
			DownloadFolder:      DefaultDownloadFolder(),
			PollIntervalMinutes: 5,
			UseJobNameDir:       true,
			MaxConcurrent:       5,
			LookbackDays:        7,
		},
		Filters: FilterConfig{
			NamePrefix:   "",
			NameContains: "",
			Exclude:      "",
		},
		Eligibility: EligibilityConfig{
			CorrectnessTag:    "isCorrect:true",
			AutoDownloadValue: "Enable",
			DownloadedTag:     "autoDownloaded:true",
		},
		Notifications: NotificationConfig{
			Enabled:              true,
			ShowDownloadComplete: true,
			ShowDownloadFailed:   true,
		},
	}
}

// LoadDaemonConfig loads configuration from the daemon.conf file.
// If path is empty, uses the default path.
// If the file doesn't exist, returns a config with default values and no error.
// If the file exists but is invalid, returns an error.
func LoadDaemonConfig(path string) (*DaemonConfig, error) {
	cfg := NewDaemonConfig()

	// If no path provided, use default
	if path == "" {
		var err error
		path, err = DefaultDaemonConfigPath()
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
		return nil, fmt.Errorf("failed to load daemon.conf: %w", err)
	}

	// Parse [daemon] section
	daemonSection := iniFile.Section("daemon")
	cfg.Daemon.Enabled = daemonSection.Key("enabled").MustBool(false)
	cfg.Daemon.DownloadFolder = daemonSection.Key("download_folder").MustString(DefaultDownloadFolder())
	cfg.Daemon.PollIntervalMinutes = daemonSection.Key("poll_interval_minutes").MustInt(5)
	cfg.Daemon.UseJobNameDir = daemonSection.Key("use_job_name_dir").MustBool(true)
	cfg.Daemon.MaxConcurrent = daemonSection.Key("max_concurrent").MustInt(5)
	cfg.Daemon.LookbackDays = daemonSection.Key("lookback_days").MustInt(7)

	// Parse [filters] section
	filtersSection := iniFile.Section("filters")
	cfg.Filters.NamePrefix = filtersSection.Key("name_prefix").String()
	cfg.Filters.NameContains = filtersSection.Key("name_contains").String()
	cfg.Filters.Exclude = filtersSection.Key("exclude").String()

	// Parse [eligibility] section
	eligSection := iniFile.Section("eligibility")
	cfg.Eligibility.CorrectnessTag = eligSection.Key("correctness_tag").MustString("isCorrect:true")
	cfg.Eligibility.AutoDownloadValue = eligSection.Key("auto_download_value").MustString("Enable")
	cfg.Eligibility.DownloadedTag = eligSection.Key("downloaded_tag").MustString("autoDownloaded:true")

	// Parse [notifications] section
	notifySection := iniFile.Section("notifications")
	cfg.Notifications.Enabled = notifySection.Key("enabled").MustBool(true)
	cfg.Notifications.ShowDownloadComplete = notifySection.Key("show_download_complete").MustBool(true)
	cfg.Notifications.ShowDownloadFailed = notifySection.Key("show_download_failed").MustBool(true)

	return cfg, nil
}

// SaveDaemonConfig saves configuration to the daemon.conf file.
// If path is empty, uses the default path.
// Creates parent directories if they don't exist.
func SaveDaemonConfig(cfg *DaemonConfig, path string) error {
	// If no path provided, use default
	if path == "" {
		var err error
		path, err = DefaultDaemonConfigPath()
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

	// Write [daemon] section
	daemonSection, err := iniFile.NewSection("daemon")
	if err != nil {
		return fmt.Errorf("failed to create daemon section: %w", err)
	}
	daemonSection.Key("enabled").SetValue(fmt.Sprintf("%t", cfg.Daemon.Enabled))
	daemonSection.Key("download_folder").SetValue(cfg.Daemon.DownloadFolder)
	daemonSection.Key("poll_interval_minutes").SetValue(fmt.Sprintf("%d", cfg.Daemon.PollIntervalMinutes))
	daemonSection.Key("use_job_name_dir").SetValue(fmt.Sprintf("%t", cfg.Daemon.UseJobNameDir))
	daemonSection.Key("max_concurrent").SetValue(fmt.Sprintf("%d", cfg.Daemon.MaxConcurrent))
	daemonSection.Key("lookback_days").SetValue(fmt.Sprintf("%d", cfg.Daemon.LookbackDays))

	// Write [filters] section
	filtersSection, err := iniFile.NewSection("filters")
	if err != nil {
		return fmt.Errorf("failed to create filters section: %w", err)
	}
	filtersSection.Key("name_prefix").SetValue(cfg.Filters.NamePrefix)
	filtersSection.Key("name_contains").SetValue(cfg.Filters.NameContains)
	filtersSection.Key("exclude").SetValue(cfg.Filters.Exclude)

	// Write [eligibility] section
	eligSection, err := iniFile.NewSection("eligibility")
	if err != nil {
		return fmt.Errorf("failed to create eligibility section: %w", err)
	}
	eligSection.Key("correctness_tag").SetValue(cfg.Eligibility.CorrectnessTag)
	eligSection.Key("auto_download_value").SetValue(cfg.Eligibility.AutoDownloadValue)
	eligSection.Key("downloaded_tag").SetValue(cfg.Eligibility.DownloadedTag)

	// Write [notifications] section
	notifySection, err := iniFile.NewSection("notifications")
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

	// Set restrictive permissions on Unix
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

// Validate checks if the daemon configuration is valid.
// Returns nil if valid, or an error describing what's wrong.
func (cfg *DaemonConfig) Validate() error {
	// Only validate settings if daemon is enabled
	if cfg.Daemon.Enabled {
		if strings.TrimSpace(cfg.Daemon.DownloadFolder) == "" {
			return ErrDaemonMissingDownloadFolder
		}
		if cfg.Daemon.PollIntervalMinutes < 1 || cfg.Daemon.PollIntervalMinutes > 1440 {
			return ErrDaemonInvalidPollInterval
		}
		if cfg.Daemon.MaxConcurrent < 1 || cfg.Daemon.MaxConcurrent > 10 {
			return ErrDaemonInvalidMaxConcurrent
		}
		if cfg.Daemon.LookbackDays < 1 || cfg.Daemon.LookbackDays > 365 {
			return ErrDaemonInvalidLookbackDays
		}
	}

	return nil
}

// IsEnabled returns true if the daemon is enabled and properly configured.
func (cfg *DaemonConfig) IsEnabled() bool {
	return cfg.Daemon.Enabled && cfg.Validate() == nil
}

// GetExcludePatterns returns the exclude patterns as a slice.
func (cfg *DaemonConfig) GetExcludePatterns() []string {
	if cfg.Filters.Exclude == "" {
		return nil
	}
	patterns := strings.Split(cfg.Filters.Exclude, ",")
	result := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// SetExcludePatterns sets the exclude patterns from a slice.
func (cfg *DaemonConfig) SetExcludePatterns(patterns []string) {
	cfg.Filters.Exclude = strings.Join(patterns, ",")
}
