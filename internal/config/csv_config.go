package config

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
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

	// Tarball options
	ExcludePatterns []string // Patterns to exclude from tarballs (e.g., *.log, *.tmp)
	IncludePatterns []string // Include-only patterns (mutually exclusive with exclude)
	FlattenTar      bool     // Remove subdirectory structure in tarballs
	RunSubpath      string   // Subpath to traverse before finding run directories (e.g., "Simcodes/Powerflow")
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

	// v3.6.3: File browser sort preferences (persisted across sessions)
	SortField     string // "name", "size", "modified" (default: "name")
	SortAscending bool   // true = ascending, false = descending (default: true)

	// v4.0.0: Detailed logging toggle for timing/metrics in Activity tab
	DetailedLogging bool
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
		ValidationPattern: "", // validation is opt-in, disabled by default
		TarCompression:    "none",
		MaxRetries:        1,
		SortField:         "name",  // v3.6.3: default sort by name
		SortAscending:     true,    // v3.6.3: default ascending
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
			// SECURITY: Ignore proxy_password from config files
			// Proxy passwords should be entered at runtime via secure prompt
			// This maintains backwards compatibility with old config files
			if value != "" {
				log.Printf("[WARN] proxy_password in config file is ignored for security - use secure prompt at runtime")
			}
		case "no_proxy":
			cfg.NoProxy = value
		case "proxy_warmup":
			cfg.ProxyWarmup = strings.ToLower(value) == "true" || value == "1"
		case "api_key":
			// SECURITY: Ignore api_key from config files
			// API keys should be provided via RESCALE_API_KEY env var or --token-file flag
			// This maintains backwards compatibility with old config files
			if value != "" {
				log.Printf("[WARN] api_key in config file is ignored for security - use RESCALE_API_KEY env var or --token-file flag")
			}
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
		case "sort_field": // v3.6.3
			cfg.SortField = value
		case "sort_ascending": // v3.6.3
			cfg.SortAscending = strings.ToLower(value) == "true" || value == "1"
		case "detailed_logging": // v4.0.0
			cfg.DetailedLogging = strings.ToLower(value) == "true" || value == "1"
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
	// Ensure parent directory exists (fixes Windows issue where directory may not exist)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

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
	// SECURITY: api_key and proxy_password are intentionally NOT saved to config files
	// API keys should be provided via RESCALE_API_KEY env var or --token-file flag
	// Proxy passwords should be entered at runtime via secure prompt
	records := [][]string{
		{"tar_workers", strconv.Itoa(cfg.TarWorkers)},
		{"upload_workers", strconv.Itoa(cfg.UploadWorkers)},
		{"job_workers", strconv.Itoa(cfg.JobWorkers)},
		{"proxy_mode", cfg.ProxyMode},
		{"proxy_host", cfg.ProxyHost},
		{"proxy_port", strconv.Itoa(cfg.ProxyPort)},
		{"proxy_user", cfg.ProxyUser},
		// proxy_password intentionally omitted for security
		{"no_proxy", cfg.NoProxy},
		{"proxy_warmup", strconv.FormatBool(cfg.ProxyWarmup)},
		// api_key intentionally omitted for security
		{"api_base_url", cfg.APIBaseURL},
		{"tenant_url", cfg.TenantURL},
		{"exclude_pattern", strings.Join(cfg.ExcludePatterns, ",")},
		{"include_pattern", strings.Join(cfg.IncludePatterns, ",")},
		{"flatten_tar", strconv.FormatBool(cfg.FlattenTar)},
		{"run_subpath", cfg.RunSubpath},
		{"validation_pattern", cfg.ValidationPattern},
		{"tar_compression", cfg.TarCompression},
		{"max_retries", strconv.Itoa(cfg.MaxRetries)},
		{"sort_field", cfg.SortField},                              // v3.6.3
		{"sort_ascending", strconv.FormatBool(cfg.SortAscending)},  // v3.6.3
		{"detailed_logging", strconv.FormatBool(cfg.DetailedLogging)}, // v4.0.0
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
// Priority: flags > token-file > environment > defaults
func (c *Config) MergeWithFlags(apiKey, apiBaseURL, proxyMode, proxyHost string, proxyPort int) {
	c.MergeWithFlagsAndTokenFile(apiKey, "", apiBaseURL, proxyMode, proxyHost, proxyPort)
}

// MergeWithFlagsAndTokenFile merges config with flags, token file, and environment variables
// Priority (highest to lowest):
//  1. --api-key flag (command line)
//  2. RESCALE_API_KEY environment variable
//  3. --token-file flag (explicit token file path)
//  4. Default token file (~/.config/rescale-int/token)
//
// Warning: If multiple sources are set, only the highest priority source is used.
func (c *Config) MergeWithFlagsAndTokenFile(apiKey, tokenFilePath, apiBaseURL, proxyMode, proxyHost string, proxyPort int) {
	// Track API key sources for warning messages
	var apiKeySources []string

	// 0. Default token file (lowest priority - created by 'config init')
	var defaultTokenKey string
	if defaultTokenPath := GetDefaultTokenPath(); defaultTokenPath != "" {
		if tokenKey, err := ReadTokenFile(defaultTokenPath); err == nil && tokenKey != "" {
			defaultTokenKey = tokenKey
			apiKeySources = append(apiKeySources, fmt.Sprintf("default token file (%s)", defaultTokenPath))
		}
	}

	// 1. Explicit token file (--token-file flag)
	var explicitTokenKey string
	if tokenFilePath != "" {
		if tokenKey, err := ReadTokenFile(tokenFilePath); err == nil && tokenKey != "" {
			explicitTokenKey = tokenKey
			apiKeySources = append(apiKeySources, "--token-file flag")
		}
	}

	// 2. Environment variable
	envKey := os.Getenv("RESCALE_API_KEY")
	if envKey != "" {
		apiKeySources = append(apiKeySources, "RESCALE_API_KEY environment variable")
	}

	// 3. Command-line flag (highest priority)
	if apiKey != "" {
		apiKeySources = append(apiKeySources, "--api-key flag")
	}

	// Warn if multiple API key sources are set
	if len(apiKeySources) > 1 {
		log.Printf("[WARN] Multiple API key sources detected: %v", apiKeySources)
		log.Printf("[WARN] API key precedence (highest to lowest): --api-key > RESCALE_API_KEY env > --token-file > default token file")
		log.Printf("[WARN] Using: %s", apiKeySources[len(apiKeySources)-1])
	}

	// Apply API key in order of priority (lowest to highest, each overwriting the previous)
	if defaultTokenKey != "" {
		c.APIKey = defaultTokenKey
	}
	if explicitTokenKey != "" {
		c.APIKey = explicitTokenKey
	}
	if envKey != "" {
		c.APIKey = envKey
	}
	if apiKey != "" {
		c.APIKey = apiKey
	}

	// Environment URL overrides
	if envURL := os.Getenv("RESCALE_API_URL"); envURL != "" {
		c.APIBaseURL = envURL
		c.TenantURL = envURL
	}
	if envProxy := os.Getenv("HTTPS_PROXY"); envProxy != "" && c.ProxyHost == "" {
		c.parseProxyURL(envProxy)
	}

	// Command-line flags (highest priority)
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
		return fmt.Errorf("API key is required (set via RESCALE_API_KEY env var or --token-file flag)")
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

// IsFRMPlatform returns true if the URL is a FedRAMP-regulated platform.
// v4.5.1: FRM platforms require strict FIPS 140-3 compliance.
// NTLM proxy mode uses non-FIPS algorithms (MD4/MD5) and must be disabled for these platforms.
func IsFRMPlatform(url string) bool {
	return strings.Contains(url, "rescale-gov.com")
}

// ValidateNTLMForFIPS checks if NTLM proxy mode is appropriate for the current configuration.
// v4.5.1: Returns a warning message if NTLM is used with a FRM platform.
func (c *Config) ValidateNTLMForFIPS() string {
	if c.ProxyMode == "ntlm" && IsFRMPlatform(c.APIBaseURL) {
		return "NTLM proxy mode uses non-FIPS algorithms (MD4/MD5) and is not compliant with FedRAMP requirements. Consider using 'basic' proxy mode over TLS."
	}
	return ""
}

// ConfigDir is the standard configuration directory name
const ConfigDir = "rescale"

// OldConfigDir is the previous directory name (for migration)
const OldConfigDir = "rescale-int"

// getConfigDir returns the platform-appropriate config directory.
// - Windows: %APPDATA%\Rescale\Interlink (standard Windows location)
// - Unix: ~/.config/rescale (XDG standard)
func getConfigDir() string {
	if runtime.GOOS == "windows" {
		// v4.0.8: Use standard Windows %APPDATA% location
		appData := os.Getenv("APPDATA")
		if appData != "" {
			return filepath.Join(appData, "Rescale", "Interlink")
		}
		// Fallback to USERPROFILE if APPDATA not set
		if userProfile := os.Getenv("USERPROFILE"); userProfile != "" {
			return filepath.Join(userProfile, "AppData", "Roaming", "Rescale", "Interlink")
		}
	}
	// Unix: use XDG standard ~/.config/rescale
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", ConfigDir)
	}
	return ""
}

// getOldConfigDir returns legacy config directory for migration checking.
func getOldConfigDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", OldConfigDir)
	}
	return ""
}

// GetDefaultConfigPath returns the default config file path
// - Windows: %APPDATA%\Rescale\Interlink\config.csv (v4.0.8: standard Windows location)
// - Unix: ~/.config/rescale/config.csv (XDG standard)
// Falls back to old location ~/.config/rescale-int/config.csv if new location doesn't exist
func GetDefaultConfigPath() string {
	configDir := getConfigDir()
	if configDir == "" {
		return "config.csv"
	}
	newPath := filepath.Join(configDir, "config.csv")

	// Check if new location exists
	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}

	// Check if old location exists (migration case - Unix only)
	if oldDir := getOldConfigDir(); oldDir != "" {
		oldPath := filepath.Join(oldDir, "config.csv")
		if _, err := os.Stat(oldPath); err == nil {
			log.Printf("[INFO] Config found at old location: %s", oldPath)
			log.Printf("[INFO] Consider migrating to new location: %s", newPath)
			return oldPath
		}
	}

	// Neither exists - return new path (for new installations)
	return newPath
}

// GetDefaultTokenPath returns the default token file path
// - Windows: %APPDATA%\Rescale\Interlink\token (v4.0.8: standard Windows location)
// - Unix: ~/.config/rescale/token (XDG standard)
// Falls back to old location ~/.config/rescale-int/token if new location doesn't exist
// This is where 'config init' saves the API key
func GetDefaultTokenPath() string {
	configDir := getConfigDir()
	if configDir == "" {
		return ""
	}
	newPath := filepath.Join(configDir, "token")

	// Check if new location exists
	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}

	// Check if old location exists (migration case - Unix only)
	if oldDir := getOldConfigDir(); oldDir != "" {
		oldPath := filepath.Join(oldDir, "token")
		if _, err := os.Stat(oldPath); err == nil {
			return oldPath
		}
	}

	// Neither exists - return new path (for new installations)
	return newPath
}

// EnsureConfigDir creates the config directory if it doesn't exist
// - Windows: Creates %APPDATA%\Rescale\Interlink (v4.0.8: standard Windows location)
// - Unix: Creates ~/.config/rescale/ (XDG standard)
func EnsureConfigDir() error {
	configDir := getConfigDir()
	if configDir == "" {
		return fmt.Errorf("could not determine config directory")
	}
	return os.MkdirAll(configDir, 0700)
}

// ReadTokenFile reads an API token from a file
// The file should contain only the API token (whitespace is trimmed)
// Returns empty string if file cannot be read
// Warns if file permissions are too open (not 0600 on Unix systems)
func ReadTokenFile(path string) (string, error) {
	// Check file permissions before reading
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("failed to stat token file: %w", err)
	}

	// On Unix systems, warn if permissions are too open
	// Token files should be readable only by owner (0600 or stricter)
	mode := info.Mode().Perm()
	if mode&0077 != 0 {
		// File is readable by group or others - security warning
		fmt.Fprintf(os.Stderr, "Warning: Token file %s has insecure permissions %04o. Consider using 'chmod 600 %s'\n", path, mode, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file is empty")
	}
	return token, nil
}

// WriteTokenFile writes an API token to a file with secure permissions (0600)
// The token is written as-is (trimmed of leading/trailing whitespace)
func WriteTokenFile(path, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("cannot write empty token")
	}

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create token directory: %w", err)
	}

	// Write token with secure permissions (0600 = owner read/write only)
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	return nil
}
