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
	if !cfg.Notifications.Enabled {
		t.Error("expected Notifications.Enabled to default to true")
	}
	if !cfg.Notifications.ShowDownloadComplete {
		t.Error("expected Notifications.ShowDownloadComplete to default to true")
	}
	if !cfg.Notifications.ShowDownloadFailed {
		t.Error("expected Notifications.ShowDownloadFailed to default to true")
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
		Notifications: NotificationConfig{
			Enabled:              true,
			ShowDownloadComplete: false,
			ShowDownloadFailed:   true,
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
	// API key is intentionally NOT written to file.
	// SaveAPIConfig strips legacy keys on save.
	if loadedCfg.APIKey != "" {
		t.Errorf("APIKey should not be saved to file, but loaded as %q", loadedCfg.APIKey)
	}
	if loadedCfg.Notifications != cfg.Notifications {
		t.Errorf("Notifications mismatch: expected %+v, got %+v", cfg.Notifications, loadedCfg.Notifications)
	}
}

func TestLoadAPIConfig(t *testing.T) {
	tests := []struct {
		name string
		// content, when set, is written to a temp apiconfig; path overrides it.
		content    string
		path       string
		wantErr    bool
		wantURL    string
		wantAPIKey string
		wantNonNil bool
	}{
		{
			// A missing file is not an error: the defaults stand in.
			name:    "nonexistent path returns defaults",
			path:    "/path/that/does/not/exist/apiconfig",
			wantURL: "https://platform.rescale.com",
		},
		{
			// An empty path means "the default location", which may or may not
			// exist on the machine running the test; either way it must not error.
			name:       "empty path uses the default location",
			path:       "",
			wantNonNil: true,
		},
		{
			name:    "invalid INI is an error",
			content: "this is not valid INI [[[",
			wantErr: true,
		},
		{
			name: "partial config loads the rescale section",
			content: `[rescale]
platform_url = https://partial.rescale.com
api_key = partial-key
`,
			wantURL: "https://partial.rescale.com", wantAPIKey: "partial-key",
		},
		{
			// Legacy api_key values are still read for backwards compatibility.
			name:    "legacy api_key is read",
			content: "[rescale]\nplatform_url = https://test.rescale.com\napi_key = legacy-key-value\n",
			wantURL: "https://test.rescale.com", wantAPIKey: "legacy-key-value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := tt.path
			if tt.content != "" {
				configPath = filepath.Join(t.TempDir(), "apiconfig")
				if err := os.WriteFile(configPath, []byte(tt.content), 0600); err != nil {
					t.Fatalf("failed to write test file: %v", err)
				}
			}

			cfg, err := LoadAPIConfig(configPath)
			if tt.wantErr {
				if err == nil {
					t.Fatal("LoadAPIConfig should fail")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadAPIConfig failed: %v", err)
			}
			if cfg == nil {
				t.Fatal("LoadAPIConfig should return a config, not nil")
			}
			if tt.wantURL != "" && cfg.PlatformURL != tt.wantURL {
				t.Errorf("PlatformURL = %q, want %q", cfg.PlatformURL, tt.wantURL)
			}
			if tt.wantAPIKey != "" && cfg.APIKey != tt.wantAPIKey {
				t.Errorf("APIKey = %q, want %q", cfg.APIKey, tt.wantAPIKey)
			}
		})
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

// LoadCompatProfile tests

func TestLoadCompatProfile(t *testing.T) {
	tests := []struct {
		name    string
		content string // when empty, no file is written
		section string
		envPath bool // point RESCALE_CONFIG_FILE at the file and pass an empty path
		wantErr bool
		wantKey string
		wantURL string
	}{
		{
			name:    "default section",
			content: "[default]\napikey = default-key\napibaseurl = https://platform.rescale.com\n",
			section: "default",
			wantKey: "default-key",
			wantURL: "https://platform.rescale.com",
		},
		{
			name:    "named section",
			content: "[default]\napikey = default-key\n\n[eu]\napikey = eu-key\napibaseurl = https://eu.rescale.com\n",
			section: "eu",
			wantKey: "eu-key",
			wantURL: "https://eu.rescale.com",
		},
		{
			name:    "missing section is an error",
			content: "[default]\napikey = default-key\n",
			section: "nonexistent",
			wantErr: true,
		},
		{
			// rescale-cli spells the keys apikey/apibaseurl.
			name:    "cli key format",
			content: "[default]\napikey = cli-format-key\n",
			section: "default",
			wantKey: "cli-format-key",
		},
		{
			// Interlink spells them api_key/platform_url.
			name:    "int key format",
			content: "[default]\napi_key = int-format-key\nplatform_url = https://int.rescale.com\n",
			section: "default",
			wantKey: "int-format-key",
			wantURL: "https://int.rescale.com",
		},
		{
			name:    "path from RESCALE_CONFIG_FILE",
			content: "[default]\napikey = env-path-key\n",
			section: "default",
			envPath: true,
			wantKey: "env-path-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "apiconfig")
			if err := os.WriteFile(configPath, []byte(tt.content), 0600); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}
			if tt.envPath {
				t.Setenv("RESCALE_CONFIG_FILE", configPath)
				configPath = ""
			}

			key, url, err := LoadCompatProfile(configPath, tt.section)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got key=%q url=%q", key, url)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key != tt.wantKey {
				t.Errorf("apiKey = %q, want %q", key, tt.wantKey)
			}
			if url != tt.wantURL {
				t.Errorf("baseURL = %q, want %q", url, tt.wantURL)
			}
		})
	}
}

// A missing file is not fatal: the compat path falls back to other sources.
func TestLoadCompatProfile_MissingFileNonFatal(t *testing.T) {
	key, url, err := LoadCompatProfile("/nonexistent/path/apiconfig", "default")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if key != "" || url != "" {
		t.Errorf("expected empty results for missing file, got key=%q url=%q", key, url)
	}
}

// Verify SaveAPIConfig strips legacy api_key from disk.
func TestSaveAPIConfig_StripsLegacyKey(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apiconfig")

	// Write an INI with api_key
	content := "[rescale]\nplatform_url = https://test.rescale.com\napi_key = old-key\n"
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Save a new config (with APIKey field set — it should still not be written)
	cfg := NewAPIConfig()
	cfg.PlatformURL = "https://test.rescale.com"
	cfg.APIKey = "should-not-be-written"
	if err := SaveAPIConfig(cfg, configPath); err != nil {
		t.Fatalf("SaveAPIConfig failed: %v", err)
	}

	// Read back and verify api_key is gone
	loaded, err := LoadAPIConfig(configPath)
	if err != nil {
		t.Fatalf("LoadAPIConfig failed: %v", err)
	}
	if loaded.APIKey != "" {
		t.Errorf("expected APIKey to be stripped, got %q", loaded.APIKey)
	}
}
