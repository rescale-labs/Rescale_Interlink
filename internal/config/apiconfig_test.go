package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewAPIConfig(t *testing.T) {
	cfg := NewAPIConfig()

	// Check defaults
	if cfg.PlatformURL != "https://platform.rescale.com" {
		t.Errorf("expected default PlatformURL to be https://platform.rescale.com, got %s", cfg.PlatformURL)
	}
	if cfg.AutoDownload.Enabled != false {
		t.Error("expected AutoDownload.Enabled to default to false")
	}
	if cfg.AutoDownload.CorrectnessTag != "isCorrect:true" {
		t.Errorf("expected default CorrectnessTag to be isCorrect:true, got %s", cfg.AutoDownload.CorrectnessTag)
	}
	if cfg.AutoDownload.ScanIntervalMinutes != 10 {
		t.Errorf("expected default ScanIntervalMinutes to be 10, got %d", cfg.AutoDownload.ScanIntervalMinutes)
	}
	if cfg.AutoDownload.LookbackDays != 7 {
		t.Errorf("expected default LookbackDays to be 7, got %d", cfg.AutoDownload.LookbackDays)
	}
}

func TestSaveAndLoadAPIConfig(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apiconfig")

	// Create test config
	cfg := &APIConfig{
		PlatformURL: "https://test.rescale.com",
		APIKey:      "test-api-key-12345",
		AutoDownload: AutoDownloadConfig{
			Enabled:               true,
			CorrectnessTag:        "myTag:verified",
			DefaultDownloadFolder: "/tmp/downloads",
			ScanIntervalMinutes:   15,
			LookbackDays:          14,
		},
	}

	// Save config
	if err := SaveAPIConfig(cfg, configPath); err != nil {
		t.Fatalf("SaveAPIConfig failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("config file was not created")
	}

	// Load config back
	loadedCfg, err := LoadAPIConfig(configPath)
	if err != nil {
		t.Fatalf("LoadAPIConfig failed: %v", err)
	}

	// Verify values
	if loadedCfg.PlatformURL != cfg.PlatformURL {
		t.Errorf("PlatformURL mismatch: expected %s, got %s", cfg.PlatformURL, loadedCfg.PlatformURL)
	}
	if loadedCfg.APIKey != cfg.APIKey {
		t.Errorf("APIKey mismatch: expected %s, got %s", cfg.APIKey, loadedCfg.APIKey)
	}
	if loadedCfg.AutoDownload.Enabled != cfg.AutoDownload.Enabled {
		t.Errorf("Enabled mismatch: expected %v, got %v", cfg.AutoDownload.Enabled, loadedCfg.AutoDownload.Enabled)
	}
	if loadedCfg.AutoDownload.CorrectnessTag != cfg.AutoDownload.CorrectnessTag {
		t.Errorf("CorrectnessTag mismatch: expected %s, got %s", cfg.AutoDownload.CorrectnessTag, loadedCfg.AutoDownload.CorrectnessTag)
	}
	if loadedCfg.AutoDownload.DefaultDownloadFolder != cfg.AutoDownload.DefaultDownloadFolder {
		t.Errorf("DefaultDownloadFolder mismatch: expected %s, got %s", cfg.AutoDownload.DefaultDownloadFolder, loadedCfg.AutoDownload.DefaultDownloadFolder)
	}
	if loadedCfg.AutoDownload.ScanIntervalMinutes != cfg.AutoDownload.ScanIntervalMinutes {
		t.Errorf("ScanIntervalMinutes mismatch: expected %d, got %d", cfg.AutoDownload.ScanIntervalMinutes, loadedCfg.AutoDownload.ScanIntervalMinutes)
	}
	if loadedCfg.AutoDownload.LookbackDays != cfg.AutoDownload.LookbackDays {
		t.Errorf("LookbackDays mismatch: expected %d, got %d", cfg.AutoDownload.LookbackDays, loadedCfg.AutoDownload.LookbackDays)
	}
}

func TestLoadAPIConfig_NonExistent(t *testing.T) {
	// Load from non-existent path should return defaults
	cfg, err := LoadAPIConfig("/path/that/does/not/exist/apiconfig")
	if err != nil {
		t.Fatalf("LoadAPIConfig should not fail for non-existent file: %v", err)
	}

	// Should return defaults
	if cfg.PlatformURL != "https://platform.rescale.com" {
		t.Errorf("expected default PlatformURL for non-existent file")
	}
	if cfg.AutoDownload.Enabled != false {
		t.Error("expected default Enabled=false for non-existent file")
	}
}

func TestLoadAPIConfig_EmptyPath(t *testing.T) {
	// Empty path should try default location (may or may not exist)
	// This should not panic or error
	cfg, err := LoadAPIConfig("")
	if err != nil {
		t.Fatalf("LoadAPIConfig with empty path should not error: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadAPIConfig should return a config, not nil")
	}
}

func TestAPIConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *APIConfig
		wantErr error
	}{
		{
			name: "valid disabled config",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled: false,
				},
			},
			wantErr: nil,
		},
		{
			name: "valid enabled config",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled:               true,
					CorrectnessTag:        "isCorrect:true",
					DefaultDownloadFolder: "/downloads",
					ScanIntervalMinutes:   10,
					LookbackDays:          7,
				},
			},
			wantErr: nil,
		},
		{
			name: "missing platform URL",
			cfg: &APIConfig{
				PlatformURL: "",
				APIKey:      "my-api-key",
			},
			wantErr: ErrMissingPlatformURL,
		},
		{
			name: "missing API key",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "",
			},
			wantErr: ErrMissingAPIKey,
		},
		{
			name: "enabled but missing download folder",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled:             true,
					CorrectnessTag:      "isCorrect:true",
					ScanIntervalMinutes: 10,
					LookbackDays:        7,
				},
			},
			wantErr: ErrMissingDownloadFolder,
		},
		{
			name: "enabled but missing correctness tag",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled:               true,
					CorrectnessTag:        "",
					DefaultDownloadFolder: "/downloads",
					ScanIntervalMinutes:   10,
					LookbackDays:          7,
				},
			},
			wantErr: ErrMissingCorrectnessTag,
		},
		{
			name: "invalid scan interval too low",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled:               true,
					CorrectnessTag:        "isCorrect:true",
					DefaultDownloadFolder: "/downloads",
					ScanIntervalMinutes:   0,
					LookbackDays:          7,
				},
			},
			wantErr: ErrInvalidScanInterval,
		},
		{
			name: "invalid scan interval too high",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled:               true,
					CorrectnessTag:        "isCorrect:true",
					DefaultDownloadFolder: "/downloads",
					ScanIntervalMinutes:   1500, // > 1440
					LookbackDays:          7,
				},
			},
			wantErr: ErrInvalidScanInterval,
		},
		{
			name: "invalid lookback days too low",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled:               true,
					CorrectnessTag:        "isCorrect:true",
					DefaultDownloadFolder: "/downloads",
					ScanIntervalMinutes:   10,
					LookbackDays:          0,
				},
			},
			wantErr: ErrInvalidLookbackDays,
		},
		{
			name: "invalid lookback days too high",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled:               true,
					CorrectnessTag:        "isCorrect:true",
					DefaultDownloadFolder: "/downloads",
					ScanIntervalMinutes:   10,
					LookbackDays:          400, // > 365
				},
			},
			wantErr: ErrInvalidLookbackDays,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if err != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAPIConfig_ValidateForConnection(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *APIConfig
		wantErr error
	}{
		{
			name: "valid connection",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
			},
			wantErr: nil,
		},
		{
			name: "missing platform URL",
			cfg: &APIConfig{
				PlatformURL: "",
				APIKey:      "my-api-key",
			},
			wantErr: ErrMissingPlatformURL,
		},
		{
			name: "missing API key",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "",
			},
			wantErr: ErrMissingAPIKey,
		},
		{
			name: "whitespace only platform URL",
			cfg: &APIConfig{
				PlatformURL: "   ",
				APIKey:      "my-api-key",
			},
			wantErr: ErrMissingPlatformURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateForConnection()
			if err != tt.wantErr {
				t.Errorf("ValidateForConnection() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAPIConfig_IsAutoDownloadEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  *APIConfig
		want bool
	}{
		{
			name: "enabled and valid",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled:               true,
					CorrectnessTag:        "isCorrect:true",
					DefaultDownloadFolder: "/downloads",
					ScanIntervalMinutes:   10,
					LookbackDays:          7,
				},
			},
			want: true,
		},
		{
			name: "disabled",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled: false,
				},
			},
			want: false,
		},
		{
			name: "enabled but invalid (missing folder)",
			cfg: &APIConfig{
				PlatformURL: "https://platform.rescale.com",
				APIKey:      "my-api-key",
				AutoDownload: AutoDownloadConfig{
					Enabled:             true,
					CorrectnessTag:      "isCorrect:true",
					ScanIntervalMinutes: 10,
					LookbackDays:        7,
				},
			},
			want: false, // Invalid config means not enabled
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsAutoDownloadEnabled(); got != tt.want {
				t.Errorf("IsAutoDownloadEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAPIConfig_GetScanIntervalDuration(t *testing.T) {
	cfg := &APIConfig{
		AutoDownload: AutoDownloadConfig{
			ScanIntervalMinutes: 15,
		},
	}

	want := "15m"
	if got := cfg.GetScanIntervalDuration(); got != want {
		t.Errorf("GetScanIntervalDuration() = %s, want %s", got, want)
	}
}

func TestAPIConfigPathForUser(t *testing.T) {
	path := APIConfigPathForUser("/Users/testuser")
	expected := filepath.Join("/Users/testuser", ".config", "rescale", "apiconfig")
	if path != expected {
		t.Errorf("APIConfigPathForUser() = %s, want %s", path, expected)
	}
}

func TestSaveAPIConfig_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	// Use a nested path that doesn't exist yet
	configPath := filepath.Join(tmpDir, "nested", "dir", "apiconfig")

	cfg := NewAPIConfig()
	cfg.PlatformURL = "https://test.rescale.com"
	cfg.APIKey = "test-key"

	if err := SaveAPIConfig(cfg, configPath); err != nil {
		t.Fatalf("SaveAPIConfig should create parent directories: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("config file was not created")
	}
}

func TestLoadAPIConfig_InvalidINI(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.ini")

	// Write invalid INI content
	if err := os.WriteFile(configPath, []byte("this is not valid INI [[["), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := LoadAPIConfig(configPath)
	if err == nil {
		t.Error("LoadAPIConfig should fail for invalid INI")
	}
}

func TestLoadAPIConfig_PartialConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "partial.ini")

	// Write partial config (only rescale section)
	content := `[rescale]
platform_url = https://partial.rescale.com
api_key = partial-key
`
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadAPIConfig(configPath)
	if err != nil {
		t.Fatalf("LoadAPIConfig failed: %v", err)
	}

	// Rescale section should be loaded
	if cfg.PlatformURL != "https://partial.rescale.com" {
		t.Errorf("PlatformURL not loaded correctly")
	}
	if cfg.APIKey != "partial-key" {
		t.Errorf("APIKey not loaded correctly")
	}

	// AutoDownload should use defaults
	if cfg.AutoDownload.Enabled != false {
		t.Error("AutoDownload.Enabled should default to false")
	}
	if cfg.AutoDownload.ScanIntervalMinutes != 10 {
		t.Errorf("ScanIntervalMinutes should default to 10, got %d", cfg.AutoDownload.ScanIntervalMinutes)
	}
}
