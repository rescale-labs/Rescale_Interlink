package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
)

// TestConfigPath tests the config path command
func TestConfigPath(t *testing.T) {
	cmd := newConfigPathCmd()
	if cmd == nil {
		t.Fatal("newConfigPathCmd() returned nil")
	}

	if cmd.Use != "path" {
		t.Errorf("Expected Use='path', got '%s'", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Short description is empty")
	}
}

// TestConfigShow tests the config show command
func TestConfigShow(t *testing.T) {
	cmd := newConfigShowCmd()
	if cmd == nil {
		t.Fatal("newConfigShowCmd() returned nil")
	}

	if cmd.Use != "show" {
		t.Errorf("Expected Use='show', got '%s'", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Short description is empty")
	}

	if cmd.RunE == nil {
		t.Error("RunE function is nil")
	}
}

// TestConfigTest tests the config test command
func TestConfigTest(t *testing.T) {
	cmd := newConfigTestCmd()
	if cmd == nil {
		t.Fatal("newConfigTestCmd() returned nil")
	}

	if cmd.Use != "test" {
		t.Errorf("Expected Use='test', got '%s'", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Short description is empty")
	}

	if cmd.RunE == nil {
		t.Error("RunE function is nil")
	}
}

// TestConfigInit tests the config init command structure
func TestConfigInit(t *testing.T) {
	cmd := newConfigInitCmd()
	if cmd == nil {
		t.Fatal("newConfigInitCmd() returned nil")
	}

	if cmd.Use != "init" {
		t.Errorf("Expected Use='init', got '%s'", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Short description is empty")
	}

	if cmd.RunE == nil {
		t.Error("RunE function is nil")
	}

	// Check for --force flag
	forceFlag := cmd.Flags().Lookup("force")
	if forceFlag == nil {
		t.Error("--force flag not found")
	}
}

// TestConfigCmd tests the config command group
func TestConfigCmd(t *testing.T) {
	cmd := newConfigCmd()
	if cmd == nil {
		t.Fatal("newConfigCmd() returned nil")
	}

	if cmd.Use != "config" {
		t.Errorf("Expected Use='config', got '%s'", cmd.Use)
	}

	// Check that subcommands exist
	subcommands := cmd.Commands()
	expectedSubs := []string{"init", "show", "test", "path"}

	if len(subcommands) != len(expectedSubs) {
		t.Errorf("Expected %d subcommands, got %d", len(expectedSubs), len(subcommands))
	}

	foundSubs := make(map[string]bool)
	for _, sub := range subcommands {
		foundSubs[sub.Name()] = true
	}

	for _, expected := range expectedSubs {
		if !foundSubs[expected] {
			t.Errorf("Subcommand '%s' not found", expected)
		}
	}
}

// TestConfigSaveAndLoad tests config save and load functionality
func TestConfigSaveAndLoad(t *testing.T) {
	// Create temp directory
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "test_config.csv")

	// Create test config
	cfg := &config.Config{
		APIKey:         "test-key-123",
		APIBaseURL:     "https://test.rescale.com",
		TenantURL:      "https://test.rescale.com",
		TarWorkers:     2,
		UploadWorkers:  3,
		JobWorkers:     4,
		ProxyMode:      "no-proxy",
		TarCompression: "none",
		MaxRetries:     1,
	}

	// Save config
	err := config.SaveConfigCSV(cfg, configPath)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("Config file was not created")
	}

	// Load config
	loadedCfg, err := config.LoadConfigCSV(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify values
	if loadedCfg.APIKey != cfg.APIKey {
		t.Errorf("APIKey mismatch: expected '%s', got '%s'", cfg.APIKey, loadedCfg.APIKey)
	}

	if loadedCfg.APIBaseURL != cfg.APIBaseURL {
		t.Errorf("APIBaseURL mismatch: expected '%s', got '%s'", cfg.APIBaseURL, loadedCfg.APIBaseURL)
	}

	if loadedCfg.TarWorkers != cfg.TarWorkers {
		t.Errorf("TarWorkers mismatch: expected %d, got %d", cfg.TarWorkers, loadedCfg.TarWorkers)
	}

	if loadedCfg.UploadWorkers != cfg.UploadWorkers {
		t.Errorf("UploadWorkers mismatch: expected %d, got %d", cfg.UploadWorkers, loadedCfg.UploadWorkers)
	}

	if loadedCfg.JobWorkers != cfg.JobWorkers {
		t.Errorf("JobWorkers mismatch: expected %d, got %d", cfg.JobWorkers, loadedCfg.JobWorkers)
	}
}

// TestConfigValidation tests config validation
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: &config.Config{
				APIKey:        "valid-key",
				APIBaseURL:    "https://platform.rescale.com",
				TarWorkers:    4,
				UploadWorkers: 4,
				JobWorkers:    4,
			},
			wantErr: false,
		},
		{
			name: "missing API key",
			cfg: &config.Config{
				APIKey:        "",
				APIBaseURL:    "https://platform.rescale.com",
				TarWorkers:    4,
				UploadWorkers: 4,
				JobWorkers:    4,
			},
			wantErr: true,
		},
		{
			name: "missing API base URL",
			cfg: &config.Config{
				APIKey:        "valid-key",
				APIBaseURL:    "",
				TarWorkers:    4,
				UploadWorkers: 4,
				JobWorkers:    4,
			},
			wantErr: true,
		},
		{
			name: "invalid tar workers",
			cfg: &config.Config{
				APIKey:        "valid-key",
				APIBaseURL:    "https://platform.rescale.com",
				TarWorkers:    0,
				UploadWorkers: 4,
				JobWorkers:    4,
			},
			wantErr: true,
		},
		{
			name: "invalid upload workers",
			cfg: &config.Config{
				APIKey:        "valid-key",
				APIBaseURL:    "https://platform.rescale.com",
				TarWorkers:    4,
				UploadWorkers: 0,
				JobWorkers:    4,
			},
			wantErr: true,
		},
		{
			name: "invalid job workers",
			cfg: &config.Config{
				APIKey:        "valid-key",
				APIBaseURL:    "https://platform.rescale.com",
				TarWorkers:    4,
				UploadWorkers: 4,
				JobWorkers:    0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestConfigDefaultPath tests the default config path function
func TestConfigDefaultPath(t *testing.T) {
	path := config.GetDefaultConfigPath()
	if path == "" {
		t.Error("GetDefaultConfigPath() returned empty string")
	}

	// Should contain .config/rescale
	if !filepath.IsAbs(path) {
		t.Error("Default config path is not absolute")
	}
}
