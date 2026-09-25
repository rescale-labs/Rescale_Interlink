package config

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestLoadAPIConfig(t *testing.T) {
	defaults := NewAPIConfig().Notifications
	tests := []struct {
		name string
		// content, when set, is written to a temp apiconfig, or to the default
		// location of an isolated home when atDefault is set; path overrides it.
		content    string
		atDefault  bool
		path       string
		wantErr    bool
		wantURL    string
		wantAPIKey string
		wantNotify *NotificationConfig // nil: the defaults
	}{
		{
			// A missing file is not an error: the defaults stand in.
			name:    "nonexistent path returns defaults",
			path:    "/path/that/does/not/exist/apiconfig",
			wantURL: "https://platform.rescale.com",
		},
		{
			name:      "empty path reads the default location",
			content:   "[rescale]\nplatform_url = https://test.rescale.com\n",
			atDefault: true,
			wantURL:   "https://test.rescale.com",
		},
		{
			name:       "notification settings load",
			content:    "[interlink.notifications]\nenabled = true\nshow_download_complete = false\nshow_download_failed = true\n",
			wantNotify: &NotificationConfig{Enabled: true, ShowDownloadComplete: false, ShowDownloadFailed: true},
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
				if tt.atDefault {
					home := t.TempDir()
					for _, name := range []string{"HOME", "USERPROFILE", "APPDATA"} {
						t.Setenv(name, home)
					}
					var err error
					if configPath, err = DefaultAPIConfigPath(); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(configPath, []byte(tt.content), 0600); err != nil {
					t.Fatalf("failed to write test file: %v", err)
				}
				if tt.atDefault {
					configPath = ""
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
			wantNotify := defaults
			if tt.wantNotify != nil {
				wantNotify = *tt.wantNotify
			}
			if cfg.Notifications != wantNotify {
				t.Errorf("Notifications = %+v, want %+v", cfg.Notifications, wantNotify)
			}
		})
	}
}

func TestAPIConfigPathForUser(t *testing.T) {
	path := APIConfigPathForUser("/Users/testuser")
	expected := filepath.Join("/Users/testuser", ".config", "rescale", "apiconfig")
	if runtime.GOOS == "windows" {
		expected = filepath.Join("/Users/testuser", "AppData", "Roaming", "Rescale", "Interlink", "apiconfig")
	}
	if path != expected {
		t.Errorf("APIConfigPathForUser() = %s, want %s", path, expected)
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
