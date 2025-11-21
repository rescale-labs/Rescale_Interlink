package config

import (
	"os"
	"testing"
)

func TestLoadConfigCSV(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantErr bool
		check   func(*testing.T, *Config)
	}{
		{
			name:    "valid config",
			file:    "../../testdata/configs/valid_config.csv",
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				if cfg.APIKey != "test_api_key_12345" {
					t.Errorf("APIKey = %q, want %q", cfg.APIKey, "test_api_key_12345")
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
			file:    "../../testdata/configs/minimal_config.csv",
			wantErr: false,
			check: func(t *testing.T, cfg *Config) {
				if cfg.APIKey != "minimal_key" {
					t.Errorf("APIKey = %q, want %q", cfg.APIKey, "minimal_key")
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

func TestMergeWithFlags(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		apiKey    string
		apiURL    string
		proxyMode string
		proxyHost string
		proxyPort int
		want      *Config
	}{
		{
			name: "flags override config",
			config: &Config{
				APIKey:     "config_key",
				APIBaseURL: "https://config.com",
			},
			apiKey: "flag_key",
			apiURL: "https://flag.com",
			want: &Config{
				APIKey:     "flag_key",
				APIBaseURL: "https://flag.com",
			},
		},
		{
			name: "empty flags use config",
			config: &Config{
				APIKey:     "config_key",
				APIBaseURL: "https://config.com",
			},
			apiKey: "",
			apiURL: "",
			want: &Config{
				APIKey:     "config_key",
				APIBaseURL: "https://config.com",
			},
		},
		{
			name: "proxy settings merge",
			config: &Config{
				ProxyMode: "no-proxy",
			},
			proxyMode: "ntlm",
			proxyHost: "proxy.example.com",
			proxyPort: 8080,
			want: &Config{
				ProxyMode: "ntlm",
				ProxyHost: "proxy.example.com",
				ProxyPort: 8080,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.config.MergeWithFlags(tt.apiKey, tt.apiURL, tt.proxyMode, tt.proxyHost, tt.proxyPort)

			if tt.apiKey != "" && tt.config.APIKey != tt.want.APIKey {
				t.Errorf("APIKey = %q, want %q", tt.config.APIKey, tt.want.APIKey)
			}
			if tt.apiURL != "" && tt.config.APIBaseURL != tt.want.APIBaseURL {
				t.Errorf("APIBaseURL = %q, want %q", tt.config.APIBaseURL, tt.want.APIBaseURL)
			}
			if tt.proxyMode != "" && tt.config.ProxyMode != tt.want.ProxyMode {
				t.Errorf("ProxyMode = %q, want %q", tt.config.ProxyMode, tt.want.ProxyMode)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: &Config{
				APIKey:        "valid_key",
				APIBaseURL:    "https://platform.rescale.com",
				TarWorkers:    2,
				UploadWorkers: 2,
				JobWorkers:    2,
			},
			wantErr: false,
		},
		{
			name: "missing API key",
			config: &Config{
				APIKey:     "",
				APIBaseURL: "https://platform.rescale.com",
			},
			wantErr: true,
		},
		{
			name: "invalid workers (negative)",
			config: &Config{
				APIKey:     "valid_key",
				APIBaseURL: "https://platform.rescale.com",
				TarWorkers: -1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEnvironmentVariables(t *testing.T) {
	// Save original env vars
	origKey := os.Getenv("RESCALE_API_KEY")
	origURL := os.Getenv("RESCALE_API_URL")
	defer func() {
		if origKey != "" {
			os.Setenv("RESCALE_API_KEY", origKey)
		} else {
			os.Unsetenv("RESCALE_API_KEY")
		}
		if origURL != "" {
			os.Setenv("RESCALE_API_URL", origURL)
		} else {
			os.Unsetenv("RESCALE_API_URL")
		}
	}()

	// Set test env vars
	os.Setenv("RESCALE_API_KEY", "env_key")
	os.Setenv("RESCALE_API_URL", "https://env.com")

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
