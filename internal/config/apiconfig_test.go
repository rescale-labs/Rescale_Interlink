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
}

// Verify LoadAPIConfig still reads legacy api_key values for backwards compat.
func TestLoadAPIConfig_LegacyAPIKey(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apiconfig")

	content := "[rescale]\nplatform_url = https://test.rescale.com\napi_key = legacy-key-value\n"
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadAPIConfig(configPath)
	if err != nil {
		t.Fatalf("LoadAPIConfig failed: %v", err)
	}
	if cfg.APIKey != "legacy-key-value" {
		t.Errorf("expected legacy APIKey to be read, got %q", cfg.APIKey)
	}
}

// LoadCompatProfile tests

func TestLoadCompatProfile_DefaultSection(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apiconfig")
	content := "[default]\napikey = default-key\napibaseurl = https://platform.rescale.com\n"
	os.WriteFile(configPath, []byte(content), 0600)

	key, url, err := LoadCompatProfile(configPath, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "default-key" {
		t.Errorf("apiKey = %q, want %q", key, "default-key")
	}
	if url != "https://platform.rescale.com" {
		t.Errorf("baseURL = %q, want %q", url, "https://platform.rescale.com")
	}
}

func TestLoadCompatProfile_NamedSection(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apiconfig")
	content := "[default]\napikey = default-key\n\n[eu]\napikey = eu-key\napibaseurl = https://eu.rescale.com\n"
	os.WriteFile(configPath, []byte(content), 0600)

	key, url, err := LoadCompatProfile(configPath, "eu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "eu-key" {
		t.Errorf("apiKey = %q, want %q", key, "eu-key")
	}
	if url != "https://eu.rescale.com" {
		t.Errorf("baseURL = %q, want %q", url, "https://eu.rescale.com")
	}
}

func TestLoadCompatProfile_MissingSectionError(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apiconfig")
	content := "[default]\napikey = default-key\n"
	os.WriteFile(configPath, []byte(content), 0600)

	_, _, err := LoadCompatProfile(configPath, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent section")
	}
}

func TestLoadCompatProfile_MissingFileNonFatal(t *testing.T) {
	key, url, err := LoadCompatProfile("/nonexistent/path/apiconfig", "default")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if key != "" || url != "" {
		t.Errorf("expected empty results for missing file, got key=%q url=%q", key, url)
	}
}

func TestLoadCompatProfile_CLIKeyFormat(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apiconfig")
	content := "[default]\napikey = cli-format-key\n"
	os.WriteFile(configPath, []byte(content), 0600)

	key, _, err := LoadCompatProfile(configPath, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "cli-format-key" {
		t.Errorf("apiKey = %q, want %q", key, "cli-format-key")
	}
}

func TestLoadCompatProfile_INTKeyFormat(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apiconfig")
	content := "[default]\napi_key = int-format-key\nplatform_url = https://int.rescale.com\n"
	os.WriteFile(configPath, []byte(content), 0600)

	key, url, err := LoadCompatProfile(configPath, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "int-format-key" {
		t.Errorf("apiKey = %q, want %q", key, "int-format-key")
	}
	if url != "https://int.rescale.com" {
		t.Errorf("baseURL = %q, want %q", url, "https://int.rescale.com")
	}
}

func TestLoadCompatProfile_RescaleConfigFileEnv(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "custom_apiconfig")
	content := "[default]\napikey = env-path-key\n"
	os.WriteFile(configPath, []byte(content), 0600)

	t.Setenv("RESCALE_CONFIG_FILE", configPath)

	key, _, err := LoadCompatProfile("", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "env-path-key" {
		t.Errorf("apiKey = %q, want %q", key, "env-path-key")
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
