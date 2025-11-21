package config

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config represents the PUR configuration
type Config struct {
	// Worker settings
	TarWorkers    int
	UploadWorkers int
	JobWorkers    int

	// Proxy settings
	ProxyMode     string // "no-proxy", "ntlm", "basic", "system"
	ProxyHost     string
	ProxyPort     int
	ProxyUser     string
	ProxyPassword string
	NoProxy       string // Comma-separated list of hosts to bypass proxy
	ProxyWarmup   bool

	// API settings
	APIKey     string
	APIBaseURL string

	// Rescale tenant URL (for v2/v3 API calls)
	TenantURL string

	// v0.7.4 features
	ExcludePatterns []string // Patterns to exclude from tarballs (e.g., *.log, *.tmp)
	IncludePatterns []string // Include-only patterns (mutually exclusive with exclude)
	FlattenTar      bool     // Remove subdirectory structure in tarballs

	// v0.7.5 features
	RunSubpath string // Subpath to traverse before finding run directories (e.g., "Simcodes/Powerflow")

	// v0.7.6 features
	ValidationPattern string // Pattern to validate runs (e.g., "*.avg.fnc"), opt-in feature (default: disabled)

	// Tar compression
	TarCompression string // "none" or "gz"

	// Retry settings
	MaxRetries int // Maximum upload retry attempts (default: 1)

	// Upload conflict detection mode (v2.4.6)
	// CheckConflictsBeforeUpload controls how file upload conflicts are detected.
	//
	// When false (default - FAST MODE):
	//   - Upload files directly without checking if they exist first
	//   - Handle conflicts only if upload fails with "already exists" error
	//   - Makes 1 API call per file (RegisterFile only)
	//   - Faster: ~2.3 hours for 13,440 files
	//
	// When true (SAFE MODE - enabled with --check-conflicts flag):
	//   - Check if files exist before uploading
	//   - Present conflict options BEFORE starting upload
	//   - Makes 1-2 API calls per file (ListFolderContents cached + RegisterFile)
	//   - Slower: ~2.3-4.6 hours for 13,440 files depending on folder structure
	//   - More predictable: user sees all conflicts upfront
	CheckConflictsBeforeUpload bool
}

// LoadConfigCSV loads configuration from a CSV file
// CSV format: key,value pairs
func LoadConfigCSV(path string) (*Config, error) {
	cfg := &Config{
		TarWorkers:        4,
		UploadWorkers:     4,
		JobWorkers:        4,
		ProxyMode:         "no-proxy",
		APIBaseURL:        "https://platform.rescale.com",
		ValidationPattern: "", // v0.7.6: validation is opt-in, disabled by default
		TarCompression:    "none",
		MaxRetries:        1,
	}

	if path == "" {
		return cfg, nil
	}

	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil // Return defaults if config doesn't exist
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to read config CSV: %w", err)
	}

	// Parse key-value pairs
	for i, record := range records {
		if i == 0 {
			// Skip header row if it looks like a header
			if len(record) >= 2 && strings.ToLower(record[0]) == "key" {
				continue
			}
		}

		if len(record) < 2 {
			continue
		}

		key := strings.TrimSpace(strings.ToLower(record[0]))
		value := strings.TrimSpace(record[1])

		switch key {
		case "tar_workers":
			if v, err := strconv.Atoi(value); err == nil {
				cfg.TarWorkers = v
			}
		case "upload_workers":
			if v, err := strconv.Atoi(value); err == nil {
				cfg.UploadWorkers = v
			}
		case "job_workers":
			if v, err := strconv.Atoi(value); err == nil {
				cfg.JobWorkers = v
			}
		case "proxy_mode":
			cfg.ProxyMode = value
		case "proxy_host":
			cfg.ProxyHost = value
		case "proxy_port":
			if v, err := strconv.Atoi(value); err == nil {
				cfg.ProxyPort = v
			}
		case "proxy_user":
			cfg.ProxyUser = value
		case "proxy_password":
			cfg.ProxyPassword = value
		case "no_proxy":
			cfg.NoProxy = value
		case "proxy_warmup":
			cfg.ProxyWarmup = strings.ToLower(value) == "true" || value == "1"
		case "api_key":
			cfg.APIKey = value
		case "api_base_url", "tenant_url":
			cfg.APIBaseURL = value
			cfg.TenantURL = value
		case "exclude_pattern":
			// Parse semicolon-separated patterns
			if value != "" {
				cfg.ExcludePatterns = strings.Split(value, ";")
				for i := range cfg.ExcludePatterns {
					cfg.ExcludePatterns[i] = strings.TrimSpace(cfg.ExcludePatterns[i])
				}
			}
		case "include_pattern":
			// Parse semicolon-separated patterns
			if value != "" {
				cfg.IncludePatterns = strings.Split(value, ";")
				for i := range cfg.IncludePatterns {
					cfg.IncludePatterns[i] = strings.TrimSpace(cfg.IncludePatterns[i])
				}
			}
		case "flatten_tar":
			cfg.FlattenTar = strings.ToLower(value) == "true" || value == "1"
		case "run_subpath":
			cfg.RunSubpath = value
		case "validation_pattern":
			cfg.ValidationPattern = value
		case "tar_compression":
			cfg.TarCompression = value
		case "max_retries":
			if v, err := strconv.Atoi(value); err == nil {
				cfg.MaxRetries = v
			}
		}
	}

	// Validate mutual exclusivity of include/exclude patterns
	if len(cfg.IncludePatterns) > 0 && len(cfg.ExcludePatterns) > 0 {
		return nil, fmt.Errorf("include_pattern and exclude_pattern are mutually exclusive")
	}

	return cfg, nil
}

// SaveConfigCSV saves configuration to a CSV file
// CSV format: key,value pairs
func SaveConfigCSV(cfg *Config, path string) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	if err := writer.Write([]string{"key", "value"}); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	// Write all config fields
	records := [][]string{
		{"tar_workers", strconv.Itoa(cfg.TarWorkers)},
		{"upload_workers", strconv.Itoa(cfg.UploadWorkers)},
		{"job_workers", strconv.Itoa(cfg.JobWorkers)},
		{"proxy_mode", cfg.ProxyMode},
		{"proxy_host", cfg.ProxyHost},
		{"proxy_port", strconv.Itoa(cfg.ProxyPort)},
		{"proxy_user", cfg.ProxyUser},
		{"proxy_password", cfg.ProxyPassword},
		{"no_proxy", cfg.NoProxy},
		{"proxy_warmup", strconv.FormatBool(cfg.ProxyWarmup)},
		{"api_key", cfg.APIKey},
		{"api_base_url", cfg.APIBaseURL},
		{"tenant_url", cfg.TenantURL},
		{"exclude_pattern", strings.Join(cfg.ExcludePatterns, ",")},
		{"include_pattern", strings.Join(cfg.IncludePatterns, ",")},
		{"flatten_tar", strconv.FormatBool(cfg.FlattenTar)},
		{"run_subpath", cfg.RunSubpath},
		{"validation_pattern", cfg.ValidationPattern},
		{"tar_compression", cfg.TarCompression},
		{"max_retries", strconv.Itoa(cfg.MaxRetries)},
	}

	for _, record := range records {
		// Only write non-empty values to keep file clean
		if record[1] != "" && record[1] != "0" && record[1] != "false" {
			if err := writer.Write(record); err != nil {
				return fmt.Errorf("failed to write record: %w", err)
			}
		}
	}

	return nil
}

// MergeWithFlags merges config with command-line flags and environment variables
// Priority: flags > environment > config file > defaults
func (c *Config) MergeWithFlags(apiKey, apiBaseURL, proxyMode, proxyHost string, proxyPort int) {
	// Environment variables
	if envKey := os.Getenv("RESCALE_API_KEY"); envKey != "" && c.APIKey == "" {
		c.APIKey = envKey
	}
	if envURL := os.Getenv("RESCALE_API_URL"); envURL != "" && c.APIBaseURL == "" {
		c.APIBaseURL = envURL
		c.TenantURL = envURL
	}
	if envProxy := os.Getenv("HTTPS_PROXY"); envProxy != "" && c.ProxyHost == "" {
		c.parseProxyURL(envProxy)
	}

	// Command-line flags (highest priority)
	if apiKey != "" {
		c.APIKey = apiKey
	}
	if apiBaseURL != "" {
		c.APIBaseURL = apiBaseURL
		c.TenantURL = apiBaseURL
	}
	if proxyMode != "" {
		c.ProxyMode = proxyMode
	}
	if proxyHost != "" {
		c.ProxyHost = proxyHost
	}
	if proxyPort > 0 {
		c.ProxyPort = proxyPort
	}

	// Ensure HTTPS scheme
	if c.APIBaseURL != "" && !strings.HasPrefix(c.APIBaseURL, "http") {
		c.APIBaseURL = "https://" + c.APIBaseURL
	}
	if c.TenantURL != "" && !strings.HasPrefix(c.TenantURL, "http") {
		c.TenantURL = "https://" + c.TenantURL
	}
}

// parseProxyURL parses a proxy URL from environment variable
func (c *Config) parseProxyURL(proxyURL string) {
	// Simple parsing for http://host:port or https://host:port
	proxyURL = strings.TrimPrefix(proxyURL, "http://")
	proxyURL = strings.TrimPrefix(proxyURL, "https://")

	parts := strings.Split(proxyURL, ":")
	if len(parts) >= 1 {
		c.ProxyHost = parts[0]
	}
	if len(parts) >= 2 {
		if port, err := strconv.Atoi(parts[1]); err == nil {
			c.ProxyPort = port
		}
	}
	if c.ProxyHost != "" && c.ProxyMode == "no-proxy" {
		c.ProxyMode = "system"
	}
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("API key is required (set via config, -p flag, or RESCALE_API_KEY env)")
	}
	if c.APIBaseURL == "" {
		return fmt.Errorf("API base URL is required")
	}
	if c.TarWorkers < 1 {
		return fmt.Errorf("tar_workers must be at least 1")
	}
	if c.UploadWorkers < 1 {
		return fmt.Errorf("upload_workers must be at least 1")
	}
	if c.JobWorkers < 1 {
		return fmt.Errorf("job_workers must be at least 1")
	}
	return nil
}

// GetDefaultConfigPath returns the default config file path
func GetDefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.csv"
	}
	return filepath.Join(home, ".config", "rescale", "pur_config.csv")
}
