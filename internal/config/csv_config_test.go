package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// CSV fixtures are inlined instead of read from a testdata/ directory so the
// tests run on any checkout, including a fresh clone.
const (
	validConfigCSV = `key,value
api_base_url,https://platform.rescale.com
tar_workers,2
upload_workers,2
job_workers,2
proxy_mode,no-proxy
`

	minimalConfigCSV = `key,value
api_base_url,https://platform.rescale.com
`
)

// writeFixtureCSV writes content to a fresh temp file and returns its path.
func writeFixtureCSV(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write fixture %s: %v", name, err)
	}
	return path
}

func TestLoadConfigCSV(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantErr bool
		check   func(*testing.T, *Config)
	}{
		{
			name:    "valid config",
			file:    writeFixtureCSV(t, "valid_config.csv", validConfigCSV),
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				// API key is intentionally NOT loaded from config files for security
				if cfg.APIKey != "" {
					t.Errorf("APIKey should be empty (not loaded from config), got %q", cfg.APIKey)
				}
				if cfg.APIBaseURL != "https://platform.rescale.com" {
					t.Errorf("APIBaseURL = %q, want %q", cfg.APIBaseURL, "https://platform.rescale.com")
				}
				if cfg.TarWorkers != 2 {
					t.Errorf("TarWorkers = %d, want 2", cfg.TarWorkers)
				}
				if cfg.UploadWorkers != 2 {
					t.Errorf("UploadWorkers = %d, want 2", cfg.UploadWorkers)
				}
				if cfg.JobWorkers != 2 {
					t.Errorf("JobWorkers = %d, want 2", cfg.JobWorkers)
				}
			},
		},
		{
			name:    "minimal config",
			file:    writeFixtureCSV(t, "minimal_config.csv", minimalConfigCSV),
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				// API key is intentionally NOT loaded from config files for security
				if cfg.APIKey != "" {
					t.Errorf("APIKey should be empty (not loaded from config), got %q", cfg.APIKey)
				}
				// Should have defaults
				if cfg.TarWorkers == 0 {
					t.Error("TarWorkers should have default value")
				}
			},
		},
		{
			name:    "non-existent file returns defaults",
			file:    "nonexistent.csv",
			wantErr: false, // LoadConfigCSV returns defaults for missing files
			check: func(t *testing.T, cfg *Config) {
				// Should have defaults
				if cfg.TarWorkers == 0 {
					t.Error("Should have default TarWorkers")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadConfigCSV(tt.file)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadConfigCSV() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

// MergeWithFlags delegates to MergeWithFlagsAndTokenFile with no token file.
// API-key precedence is TestMergeWithFlagsAndTokenFile's job; this covers the
// flag application for URL and proxy settings. APIKey is only asserted where
// the --api-key flag is set, because it is the one source that outranks a
// default token file sitting in the developer's real config dir.
func TestMergeWithFlags(t *testing.T) {
	tests := []struct {
		name                                 string
		config                               Config
		apiKey, apiURL, proxyMode, proxyHost string
		proxyPort                            int
		want                                 Config
	}{
		{
			name:   "flags override config",
			config: Config{APIKey: "config_key", APIBaseURL: "https://config.com"},
			apiKey: "flag_key", apiURL: "https://flag.com",
			want: Config{APIKey: "flag_key", APIBaseURL: "https://flag.com", TenantURL: "https://flag.com"},
		},
		{
			name:   "empty flags leave config in place",
			config: Config{APIKey: "config_key", APIBaseURL: "https://config.com"},
			want:   Config{APIBaseURL: "https://config.com"},
		},
		{
			name:      "proxy settings merge",
			config:    Config{ProxyMode: "no-proxy"},
			proxyMode: "ntlm", proxyHost: "proxy.example.com", proxyPort: 8080,
			want: Config{ProxyMode: "ntlm", ProxyHost: "proxy.example.com", ProxyPort: 8080},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// These env sources outrank config values and would mask the flags.
			t.Setenv("RESCALE_API_KEY", "")
			t.Setenv("RESCALE_API_URL", "")
			t.Setenv("HTTPS_PROXY", "")

			cfg := tt.config
			cfg.MergeWithFlags(tt.apiKey, tt.apiURL, tt.proxyMode, tt.proxyHost, tt.proxyPort)

			if tt.apiKey != "" && cfg.APIKey != tt.want.APIKey {
				t.Errorf("APIKey = %q, want %q", cfg.APIKey, tt.want.APIKey)
			}
			if cfg.APIBaseURL != tt.want.APIBaseURL {
				t.Errorf("APIBaseURL = %q, want %q", cfg.APIBaseURL, tt.want.APIBaseURL)
			}
			if tt.want.TenantURL != "" && cfg.TenantURL != tt.want.TenantURL {
				t.Errorf("TenantURL = %q, want %q", cfg.TenantURL, tt.want.TenantURL)
			}
			if cfg.ProxyMode != tt.want.ProxyMode {
				t.Errorf("ProxyMode = %q, want %q", cfg.ProxyMode, tt.want.ProxyMode)
			}
			if cfg.ProxyHost != tt.want.ProxyHost {
				t.Errorf("ProxyHost = %q, want %q", cfg.ProxyHost, tt.want.ProxyHost)
			}
			if cfg.ProxyPort != tt.want.ProxyPort {
				t.Errorf("ProxyPort = %d, want %d", cfg.ProxyPort, tt.want.ProxyPort)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config) // nil leaves the config valid
	}{
		{"valid config", nil},
		{"missing API key", func(c *Config) { c.APIKey = "" }},
		{"missing API base URL", func(c *Config) { c.APIBaseURL = "" }},
		{"zero tar workers", func(c *Config) { c.TarWorkers = 0 }},
		{"negative tar workers", func(c *Config) { c.TarWorkers = -1 }},
		{"invalid upload workers", func(c *Config) { c.UploadWorkers = 0 }},
		{"invalid job workers", func(c *Config) { c.JobWorkers = 0 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{APIKey: "valid_key", APIBaseURL: "https://platform.rescale.com", TarWorkers: 2, UploadWorkers: 2, JobWorkers: 2}
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			if err := cfg.Validate(); (err != nil) != (tt.mutate != nil) {
				t.Errorf("Validate() error = %v, want an error: %v", err, tt.mutate != nil)
			}
		})
	}
}

func TestEnvironmentVariables(t *testing.T) {
	t.Setenv("RESCALE_API_KEY", "env_key")
	t.Setenv("RESCALE_API_URL", "https://env.com")

	cfg := &Config{}
	cfg.MergeWithFlags("", "", "", "", 0)

	if cfg.APIKey != "env_key" {
		t.Errorf("APIKey from env = %q, want %q", cfg.APIKey, "env_key")
	}
	if cfg.APIBaseURL != "https://env.com" {
		t.Errorf("APIBaseURL from env = %q, want %q", cfg.APIBaseURL, "https://env.com")
	}

	// Flags should override env
	cfg.MergeWithFlags("flag_key", "", "", "", 0)
	if cfg.APIKey != "flag_key" {
		t.Errorf("APIKey with flag = %q, want %q", cfg.APIKey, "flag_key")
	}
}

func TestConfigDefaults(t *testing.T) {
	// LoadConfigCSV with empty path returns defaults
	cfg, err := LoadConfigCSV("")
	if err != nil {
		t.Fatalf("LoadConfigCSV(\"\") error = %v", err)
	}

	if cfg.TarWorkers <= 0 {
		t.Errorf("TarWorkers default = %d, want > 0", cfg.TarWorkers)
	}
	if cfg.UploadWorkers <= 0 {
		t.Errorf("UploadWorkers default = %d, want > 0", cfg.UploadWorkers)
	}
	if cfg.JobWorkers <= 0 {
		t.Errorf("JobWorkers default = %d, want > 0", cfg.JobWorkers)
	}
	if cfg.APIBaseURL == "" {
		t.Error("APIBaseURL should have default")
	}
}

// TestEnvVarOverridesDefault tests that RESCALE_API_URL env var overrides the default URL
// This is a regression test for the bug where env var was ignored if default was set
func TestEnvVarOverridesDefault(t *testing.T) {
	t.Setenv("RESCALE_API_KEY", "test_key")
	t.Setenv("RESCALE_API_URL", "https://kr.rescale.com") // a platform other than the default

	// Load config with defaults (this sets APIBaseURL to platform.rescale.com)
	cfg, err := LoadConfigCSV("")
	if err != nil {
		t.Fatalf("LoadConfigCSV error = %v", err)
	}

	// Verify default was set
	if cfg.APIBaseURL != "https://platform.rescale.com" {
		t.Fatalf("Expected default APIBaseURL to be platform.rescale.com, got %s", cfg.APIBaseURL)
	}

	// Merge with env vars - this should override the default with kr.rescale.com
	cfg.MergeWithFlagsAndTokenFile("", "", "", "", "", 0)

	// Verify env var overrode the default
	if cfg.APIBaseURL != "https://kr.rescale.com" {
		t.Errorf("RESCALE_API_URL env var should override default, got APIBaseURL = %q, want %q",
			cfg.APIBaseURL, "https://kr.rescale.com")
	}
	if cfg.TenantURL != "https://kr.rescale.com" {
		t.Errorf("RESCALE_API_URL env var should set TenantURL, got TenantURL = %q, want %q",
			cfg.TenantURL, "https://kr.rescale.com")
	}
	if cfg.APIKey != "test_key" {
		t.Errorf("RESCALE_API_KEY env var not applied, got APIKey = %q, want %q",
			cfg.APIKey, "test_key")
	}
}

func TestReadTokenFile(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	tests := []struct {
		name      string
		content   string
		wantToken string
		wantErr   bool
	}{
		{
			name:      "valid token",
			content:   "my-secret-token-12345",
			wantToken: "my-secret-token-12345",
			wantErr:   false,
		},
		{
			name:      "token with whitespace",
			content:   "  my-token-with-spaces  \n",
			wantToken: "my-token-with-spaces",
			wantErr:   false,
		},
		{
			name:      "empty file",
			content:   "",
			wantToken: "",
			wantErr:   true,
		},
		{
			name:      "whitespace only",
			content:   "   \n\t  ",
			wantToken: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file with content
			tokenFile := tmpDir + "/" + tt.name + ".txt"
			if err := os.WriteFile(tokenFile, []byte(tt.content), 0600); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			token, err := ReadTokenFile(tokenFile)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadTokenFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if token != tt.wantToken {
				t.Errorf("ReadTokenFile() = %q, want %q", token, tt.wantToken)
			}
		})
	}

	// Test non-existent file
	t.Run("non-existent file", func(t *testing.T) {
		_, err := ReadTokenFile("/non/existent/path/token.txt")
		if err == nil {
			t.Error("Expected error for non-existent file")
		}
	})
}

// TestEmptyTenantURLDoesNotOverwriteAPIBaseURL is a regression test for the bug where
// config.csv with api_base_url=https://platform.rescale.com followed by tenant_url=
// (empty) would overwrite APIBaseURL to "".
func TestEmptyTenantURLDoesNotOverwriteAPIBaseURL(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := tmpDir + "/config.csv"

	// Write a config.csv that mimics SaveConfigCSV output:
	// api_base_url has a value, tenant_url is empty
	content := `key,value
api_base_url,https://platform.rescale.com
tenant_url,
tar_workers,4
`
	if err := os.WriteFile(csvPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfigCSV(csvPath)
	if err != nil {
		t.Fatalf("LoadConfigCSV() error = %v", err)
	}

	if cfg.APIBaseURL != "https://platform.rescale.com" {
		t.Errorf("APIBaseURL = %q, want %q (empty tenant_url must not overwrite non-empty api_base_url)",
			cfg.APIBaseURL, "https://platform.rescale.com")
	}
	// Post-parse normalization should sync TenantURL from APIBaseURL
	if cfg.TenantURL != "https://platform.rescale.com" {
		t.Errorf("TenantURL = %q, want %q (should be synced from APIBaseURL)",
			cfg.TenantURL, "https://platform.rescale.com")
	}
}

// TestConfigRoundTripPreservesAPIURL tests that Save then Load preserves the API URL
// when TenantURL is blank (legacy alias sync).
func TestConfigRoundTripPreservesAPIURL(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := tmpDir + "/config.csv"

	original := &Config{
		APIKey:        "FAKEKEY-never-saved",
		ProxyPassword: "FAKEPASSWORD-never-saved",
		TarWorkers:    2,
		UploadWorkers: 3,
		JobWorkers:    4,
		ProxyMode:     "no-proxy",
		APIBaseURL:    "https://platform.rescale.com",
		TenantURL:     "", // Intentionally blank to test sync
		MaxRetries:    1,
		SortField:     "name",
		SortAscending: true,
	}

	if err := SaveConfigCSV(original, csvPath); err != nil {
		t.Fatalf("SaveConfigCSV() error = %v", err)
	}
	// The key and the proxy password live elsewhere; a config file never holds them.
	if saved, _ := os.ReadFile(csvPath); strings.Contains(string(saved), "never-saved") {
		t.Errorf("the saved config holds a credential:\n%s", saved)
	}

	loaded, err := LoadConfigCSV(csvPath)
	if err != nil {
		t.Fatalf("LoadConfigCSV() error = %v", err)
	}

	if loaded.APIBaseURL != "https://platform.rescale.com" {
		t.Errorf("After round-trip: APIBaseURL = %q, want %q",
			loaded.APIBaseURL, "https://platform.rescale.com")
	}
	if loaded.TenantURL != "https://platform.rescale.com" {
		t.Errorf("After round-trip: TenantURL = %q, want %q (should be synced)",
			loaded.TenantURL, "https://platform.rescale.com")
	}
	if loaded.TarWorkers != 2 || loaded.UploadWorkers != 3 || loaded.JobWorkers != 4 {
		t.Errorf("After round-trip: workers = %d/%d/%d, want 2/3/4", loaded.TarWorkers, loaded.UploadWorkers, loaded.JobWorkers)
	}
}

// TestConfigRoundTripFlattenJobDownload verifies the flatten_job_download flag
// persists through a save/load cycle and defaults to false when absent.
func TestConfigRoundTripFlattenJobDownload(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := tmpDir + "/config.csv"

	original := &Config{
		TarWorkers:         4,
		UploadWorkers:      4,
		JobWorkers:         4,
		ProxyMode:          "no-proxy",
		APIBaseURL:         "https://platform.rescale.com",
		MaxRetries:         1,
		SortField:          "name",
		SortAscending:      true,
		FlattenJobDownload: true,
	}

	if err := SaveConfigCSV(original, csvPath); err != nil {
		t.Fatalf("SaveConfigCSV() error = %v", err)
	}
	loaded, err := LoadConfigCSV(csvPath)
	if err != nil {
		t.Fatalf("LoadConfigCSV() error = %v", err)
	}
	if !loaded.FlattenJobDownload {
		t.Error("After round-trip: FlattenJobDownload = false, want true")
	}

	// Default: a config with no flatten_job_download key parses as false.
	defPath := tmpDir + "/default.csv"
	if err := os.WriteFile(defPath, []byte("key,value\napi_base_url,https://platform.rescale.com\n"), 0644); err != nil {
		t.Fatal(err)
	}
	def, err := LoadConfigCSV(defPath)
	if err != nil {
		t.Fatalf("LoadConfigCSV(default) error = %v", err)
	}
	if def.FlattenJobDownload {
		t.Error("Default FlattenJobDownload = true, want false")
	}
}

// TestEmptyAPIBaseURLWithTenantURL tests the reverse case: tenant_url set, api_base_url empty.
func TestEmptyAPIBaseURLWithTenantURL(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := tmpDir + "/config.csv"

	content := `key,value
api_base_url,
tenant_url,https://kr.rescale.com
`
	if err := os.WriteFile(csvPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfigCSV(csvPath)
	if err != nil {
		t.Fatalf("LoadConfigCSV() error = %v", err)
	}

	if cfg.TenantURL != "https://kr.rescale.com" {
		t.Errorf("TenantURL = %q, want %q", cfg.TenantURL, "https://kr.rescale.com")
	}
	if cfg.APIBaseURL != "https://kr.rescale.com" {
		t.Errorf("APIBaseURL = %q, want %q (should be synced from TenantURL)",
			cfg.APIBaseURL, "https://kr.rescale.com")
	}
}

// TestIsFRMPlatform validates proper hostname-based FedRAMP URL detection.
// Ensures substring spoofing attacks like "evil-rescale-gov.com" are rejected.
func TestIsFRMPlatform(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "exact match with scheme", url: "https://rescale-gov.com", want: true},
		{name: "subdomain match", url: "https://itar.rescale-gov.com", want: true},
		{name: "with port", url: "https://rescale-gov.com:8443", want: true},
		{name: "no scheme", url: "rescale-gov.com", want: true},
		{name: "uppercase", url: "HTTPS://ITAR.RESCALE-GOV.COM", want: true},
		{name: "prefix spoof rejected", url: "https://evil-rescale-gov.com", want: false},
		{name: "suffix spoof rejected", url: "https://rescale-gov.com.evil.com", want: false},
		{name: "IDN spoof rejected", url: "https://xn--rescale-gov.com", want: false},
		{name: "empty string", url: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsFRMPlatform(tt.url)
			if got != tt.want {
				t.Errorf("IsFRMPlatform(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestMergeWithFlagsAndTokenFile(t *testing.T) {
	// Create temp token file
	tmpDir := t.TempDir()
	tokenFile := tmpDir + "/token.txt"
	if err := os.WriteFile(tokenFile, []byte("token-from-file"), 0600); err != nil {
		t.Fatalf("Failed to write token file: %v", err)
	}

	t.Setenv("RESCALE_API_KEY", "") // the subtests below change it; restored at the end

	// Test priority: flag > env > token-file > default-token-file
	// This matches common CLI conventions and user expectations
	t.Run("flag overrides env", func(t *testing.T) {
		os.Setenv("RESCALE_API_KEY", "env-key")
		cfg := &Config{}
		cfg.MergeWithFlagsAndTokenFile("flag-key", tokenFile, "", "", "", 0)
		if cfg.APIKey != "flag-key" {
			t.Errorf("APIKey = %q, want 'flag-key'", cfg.APIKey)
		}
	})

	t.Run("env overrides token file", func(t *testing.T) {
		os.Setenv("RESCALE_API_KEY", "env-key")
		cfg := &Config{}
		cfg.MergeWithFlagsAndTokenFile("", tokenFile, "", "", "", 0)
		if cfg.APIKey != "env-key" {
			t.Errorf("APIKey = %q, want 'env-key' (env takes priority over token file)", cfg.APIKey)
		}
	})

	t.Run("token file used when no flag or env", func(t *testing.T) {
		os.Unsetenv("RESCALE_API_KEY")
		cfg := &Config{}
		cfg.MergeWithFlagsAndTokenFile("", tokenFile, "", "", "", 0)
		if cfg.APIKey != "token-from-file" {
			t.Errorf("APIKey = %q, want 'token-from-file'", cfg.APIKey)
		}
	})

	t.Run("invalid token file uses env", func(t *testing.T) {
		os.Setenv("RESCALE_API_KEY", "env-key")
		cfg := &Config{}
		cfg.MergeWithFlagsAndTokenFile("", "/non/existent/token.txt", "", "", "", 0)
		if cfg.APIKey != "env-key" {
			t.Errorf("APIKey = %q, want 'env-key'", cfg.APIKey)
		}
	})
}

// TestWriteTokenFile_RepairsLoosePermissions verifies that overwriting an
// existing token file that has loose (0644) permissions tightens it back to
// 0600. os.WriteFile alone only sets the mode on create, so a pre-existing
// loose file would otherwise keep its mode.
func TestWriteTokenFile_RepairsLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission model; Windows uses ACLs")
	}
	dir := t.TempDir()
	path := dir + "/token"

	// Pre-create the file with loose 0644 perms.
	if err := os.WriteFile(path, []byte("old\n"), 0644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatalf("setup chmod: %v", err)
	}

	if err := WriteTokenFile(path, "newtoken"); err != nil {
		t.Fatalf("WriteTokenFile() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("token file perms = %o, want 0600", perm)
	}
}

// TestReadTokenFile_ModeWarning covers the permission warning: it is given for a
// group- or world-readable token file where a mode means something, and never
// on Windows, where Go reports every writable file as 0666 whatever its ACL.
func TestReadTokenFile_ModeWarning(t *testing.T) {
	orig := tokenModeMeaningful
	t.Cleanup(func() { tokenModeMeaningful = orig })
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("FAKE-TOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, meaningful := range []bool{true, false} {
		tokenModeMeaningful = meaningful
		warned := captureStderr(t, func() {
			if _, err := ReadTokenFile(path); err != nil {
				t.Fatal(err)
			}
		})
		if got := strings.Contains(warned, "insecure permissions"); got != meaningful {
			t.Errorf("with the mode meaningful=%v, warned %q", meaningful, warned)
		}
	}
}

// captureStderr returns what f writes to os.Stderr.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	defer func(orig *os.File) { os.Stderr = orig }(os.Stderr)
	os.Stderr = file
	f()
	said, _ := os.ReadFile(file.Name())
	return string(said)
}
