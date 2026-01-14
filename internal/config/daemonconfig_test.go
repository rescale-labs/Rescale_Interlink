package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewDaemonConfig(t *testing.T) {
	cfg := NewDaemonConfig()

	// Check defaults
	if cfg.Daemon.Enabled != false {
		t.Errorf("Expected Enabled=false, got %v", cfg.Daemon.Enabled)
	}
	if cfg.Daemon.PollIntervalMinutes != 5 {
		t.Errorf("Expected PollIntervalMinutes=5, got %d", cfg.Daemon.PollIntervalMinutes)
	}
	if cfg.Daemon.MaxConcurrent != 5 {
		t.Errorf("Expected MaxConcurrent=5, got %d", cfg.Daemon.MaxConcurrent)
	}
	if cfg.Daemon.LookbackDays != 7 {
		t.Errorf("Expected LookbackDays=7, got %d", cfg.Daemon.LookbackDays)
	}
	if cfg.Daemon.UseJobNameDir != true {
		t.Errorf("Expected UseJobNameDir=true, got %v", cfg.Daemon.UseJobNameDir)
	}
	if cfg.Eligibility.AutoDownloadTag != "autoDownload" {
		t.Errorf("Expected AutoDownloadTag=autoDownload, got %s", cfg.Eligibility.AutoDownloadTag)
	}
	if cfg.Notifications.Enabled != true {
		t.Errorf("Expected Notifications.Enabled=true, got %v", cfg.Notifications.Enabled)
	}
}

func TestDaemonConfigLoadSave(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "daemon-config-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "daemon.conf")

	// Create a config
	cfg := NewDaemonConfig()
	cfg.Daemon.Enabled = true
	cfg.Daemon.DownloadFolder = "/test/downloads"
	cfg.Daemon.PollIntervalMinutes = 10
	cfg.Daemon.MaxConcurrent = 3
	cfg.Daemon.LookbackDays = 14
	cfg.Daemon.UseJobNameDir = false
	cfg.Filters.NamePrefix = "TestPrefix"
	cfg.Filters.NameContains = "Contains"
	cfg.Filters.Exclude = "test,debug,scratch"
	cfg.Eligibility.AutoDownloadTag = "custom:tag"
	cfg.Notifications.Enabled = false
	cfg.Notifications.ShowDownloadComplete = false
	cfg.Notifications.ShowDownloadFailed = true

	// Save it
	if err := SaveDaemonConfig(cfg, configPath); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("Config file was not created")
	}

	// Load it back
	loaded, err := LoadDaemonConfig(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify all fields
	if loaded.Daemon.Enabled != cfg.Daemon.Enabled {
		t.Errorf("Enabled mismatch: expected %v, got %v", cfg.Daemon.Enabled, loaded.Daemon.Enabled)
	}
	if loaded.Daemon.DownloadFolder != cfg.Daemon.DownloadFolder {
		t.Errorf("DownloadFolder mismatch: expected %s, got %s", cfg.Daemon.DownloadFolder, loaded.Daemon.DownloadFolder)
	}
	if loaded.Daemon.PollIntervalMinutes != cfg.Daemon.PollIntervalMinutes {
		t.Errorf("PollIntervalMinutes mismatch: expected %d, got %d", cfg.Daemon.PollIntervalMinutes, loaded.Daemon.PollIntervalMinutes)
	}
	if loaded.Daemon.MaxConcurrent != cfg.Daemon.MaxConcurrent {
		t.Errorf("MaxConcurrent mismatch: expected %d, got %d", cfg.Daemon.MaxConcurrent, loaded.Daemon.MaxConcurrent)
	}
	if loaded.Daemon.LookbackDays != cfg.Daemon.LookbackDays {
		t.Errorf("LookbackDays mismatch: expected %d, got %d", cfg.Daemon.LookbackDays, loaded.Daemon.LookbackDays)
	}
	if loaded.Daemon.UseJobNameDir != cfg.Daemon.UseJobNameDir {
		t.Errorf("UseJobNameDir mismatch: expected %v, got %v", cfg.Daemon.UseJobNameDir, loaded.Daemon.UseJobNameDir)
	}
	if loaded.Filters.NamePrefix != cfg.Filters.NamePrefix {
		t.Errorf("NamePrefix mismatch: expected %s, got %s", cfg.Filters.NamePrefix, loaded.Filters.NamePrefix)
	}
	if loaded.Filters.NameContains != cfg.Filters.NameContains {
		t.Errorf("NameContains mismatch: expected %s, got %s", cfg.Filters.NameContains, loaded.Filters.NameContains)
	}
	if loaded.Filters.Exclude != cfg.Filters.Exclude {
		t.Errorf("Exclude mismatch: expected %s, got %s", cfg.Filters.Exclude, loaded.Filters.Exclude)
	}
	if loaded.Eligibility.AutoDownloadTag != cfg.Eligibility.AutoDownloadTag {
		t.Errorf("AutoDownloadTag mismatch: expected %s, got %s", cfg.Eligibility.AutoDownloadTag, loaded.Eligibility.AutoDownloadTag)
	}
	if loaded.Notifications.Enabled != cfg.Notifications.Enabled {
		t.Errorf("Notifications.Enabled mismatch: expected %v, got %v", cfg.Notifications.Enabled, loaded.Notifications.Enabled)
	}
	if loaded.Notifications.ShowDownloadComplete != cfg.Notifications.ShowDownloadComplete {
		t.Errorf("ShowDownloadComplete mismatch: expected %v, got %v", cfg.Notifications.ShowDownloadComplete, loaded.Notifications.ShowDownloadComplete)
	}
	if loaded.Notifications.ShowDownloadFailed != cfg.Notifications.ShowDownloadFailed {
		t.Errorf("ShowDownloadFailed mismatch: expected %v, got %v", cfg.Notifications.ShowDownloadFailed, loaded.Notifications.ShowDownloadFailed)
	}
}

func TestDaemonConfigLoadNonExistent(t *testing.T) {
	// Load from non-existent path should return defaults
	cfg, err := LoadDaemonConfig("/nonexistent/path/daemon.conf")
	if err != nil {
		t.Errorf("Expected no error for non-existent file, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("Expected default config, got nil")
	}
	if cfg.Daemon.Enabled != false {
		t.Errorf("Expected default Enabled=false, got %v", cfg.Daemon.Enabled)
	}
}

func TestDaemonConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*DaemonConfig)
		wantErr error
	}{
		{
			name:    "disabled config is valid",
			modify:  func(cfg *DaemonConfig) { cfg.Daemon.Enabled = false },
			wantErr: nil,
		},
		{
			name: "enabled with valid settings",
			modify: func(cfg *DaemonConfig) {
				cfg.Daemon.Enabled = true
				cfg.Daemon.DownloadFolder = "/test"
			},
			wantErr: nil,
		},
		{
			name: "missing download folder",
			modify: func(cfg *DaemonConfig) {
				cfg.Daemon.Enabled = true
				cfg.Daemon.DownloadFolder = ""
			},
			wantErr: ErrDaemonMissingDownloadFolder,
		},
		{
			name: "poll interval too low",
			modify: func(cfg *DaemonConfig) {
				cfg.Daemon.Enabled = true
				cfg.Daemon.DownloadFolder = "/test"
				cfg.Daemon.PollIntervalMinutes = 0
			},
			wantErr: ErrDaemonInvalidPollInterval,
		},
		{
			name: "poll interval too high",
			modify: func(cfg *DaemonConfig) {
				cfg.Daemon.Enabled = true
				cfg.Daemon.DownloadFolder = "/test"
				cfg.Daemon.PollIntervalMinutes = 2000
			},
			wantErr: ErrDaemonInvalidPollInterval,
		},
		{
			name: "max concurrent too low",
			modify: func(cfg *DaemonConfig) {
				cfg.Daemon.Enabled = true
				cfg.Daemon.DownloadFolder = "/test"
				cfg.Daemon.MaxConcurrent = 0
			},
			wantErr: ErrDaemonInvalidMaxConcurrent,
		},
		{
			name: "max concurrent too high",
			modify: func(cfg *DaemonConfig) {
				cfg.Daemon.Enabled = true
				cfg.Daemon.DownloadFolder = "/test"
				cfg.Daemon.MaxConcurrent = 20
			},
			wantErr: ErrDaemonInvalidMaxConcurrent,
		},
		{
			name: "lookback days too low",
			modify: func(cfg *DaemonConfig) {
				cfg.Daemon.Enabled = true
				cfg.Daemon.DownloadFolder = "/test"
				cfg.Daemon.LookbackDays = 0
			},
			wantErr: ErrDaemonInvalidLookbackDays,
		},
		{
			name: "lookback days too high",
			modify: func(cfg *DaemonConfig) {
				cfg.Daemon.Enabled = true
				cfg.Daemon.DownloadFolder = "/test"
				cfg.Daemon.LookbackDays = 500
			},
			wantErr: ErrDaemonInvalidLookbackDays,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewDaemonConfig()
			tt.modify(cfg)
			err := cfg.Validate()
			if err != tt.wantErr {
				t.Errorf("Expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestDaemonConfigGetExcludePatterns(t *testing.T) {
	cfg := NewDaemonConfig()

	// Empty exclude
	patterns := cfg.GetExcludePatterns()
	if len(patterns) != 0 {
		t.Errorf("Expected empty patterns, got %v", patterns)
	}

	// Single pattern
	cfg.Filters.Exclude = "test"
	patterns = cfg.GetExcludePatterns()
	if len(patterns) != 1 || patterns[0] != "test" {
		t.Errorf("Expected [test], got %v", patterns)
	}

	// Multiple patterns
	cfg.Filters.Exclude = "test,debug,scratch"
	patterns = cfg.GetExcludePatterns()
	if len(patterns) != 3 {
		t.Errorf("Expected 3 patterns, got %d", len(patterns))
	}
	if patterns[0] != "test" || patterns[1] != "debug" || patterns[2] != "scratch" {
		t.Errorf("Unexpected patterns: %v", patterns)
	}

	// Patterns with spaces
	cfg.Filters.Exclude = " test , debug , scratch "
	patterns = cfg.GetExcludePatterns()
	if len(patterns) != 3 {
		t.Errorf("Expected 3 patterns, got %d", len(patterns))
	}
	if patterns[0] != "test" || patterns[1] != "debug" || patterns[2] != "scratch" {
		t.Errorf("Unexpected patterns (should be trimmed): %v", patterns)
	}
}

func TestDaemonConfigSetExcludePatterns(t *testing.T) {
	cfg := NewDaemonConfig()

	// Set patterns
	cfg.SetExcludePatterns([]string{"foo", "bar", "baz"})
	if cfg.Filters.Exclude != "foo,bar,baz" {
		t.Errorf("Expected 'foo,bar,baz', got '%s'", cfg.Filters.Exclude)
	}

	// Empty patterns
	cfg.SetExcludePatterns(nil)
	if cfg.Filters.Exclude != "" {
		t.Errorf("Expected empty string, got '%s'", cfg.Filters.Exclude)
	}
}

func TestDaemonConfigIsEnabled(t *testing.T) {
	cfg := NewDaemonConfig()

	// Not enabled
	if cfg.IsEnabled() {
		t.Error("Expected IsEnabled=false when Enabled=false")
	}

	// Enabled but invalid
	cfg.Daemon.Enabled = true
	cfg.Daemon.DownloadFolder = ""
	if cfg.IsEnabled() {
		t.Error("Expected IsEnabled=false when config is invalid")
	}

	// Enabled and valid
	cfg.Daemon.DownloadFolder = "/test"
	if !cfg.IsEnabled() {
		t.Error("Expected IsEnabled=true when config is valid and enabled")
	}
}
