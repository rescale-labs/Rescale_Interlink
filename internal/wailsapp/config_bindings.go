// Package wailsapp provides configuration-related Wails bindings.
package wailsapp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/config"
)

// AppInfoDTO contains application version and status information.
type AppInfoDTO struct {
	Version      string           `json:"version"`
	BuildTime    string           `json:"buildTime"`
	FIPSEnabled  bool             `json:"fipsEnabled"`
	FIPSStatus   string           `json:"fipsStatus"`
	VersionCheck *VersionCheckDTO `json:"versionCheck,omitempty"`
}

// GetAppInfo returns version, build time, and FIPS status.
// Also includes cached version check result if available.
func (a *App) GetAppInfo() AppInfoDTO {
	info := AppInfoDTO{
		Version:     cli.Version,
		BuildTime:   cli.BuildTime,
		FIPSEnabled: cli.FIPSStatus() != "",
		FIPSStatus:  cli.FIPSStatus(),
	}

	// Include cached version check if available and valid
	versionCheckCache.mu.RLock()
	if versionCheckCache.cacheValid && time.Since(versionCheckCache.lastCheck) < cacheDuration {
		info.VersionCheck = &versionCheckCache.result
	}
	versionCheckCache.mu.RUnlock()

	return info
}

// ConfigDTO is the JSON-safe configuration structure.
type ConfigDTO struct {
	APIBaseURL        string `json:"apiBaseUrl"`
	TenantURL         string `json:"tenantUrl"`
	APIKey            string `json:"apiKey"`
	ProxyMode         string `json:"proxyMode"`
	ProxyHost         string `json:"proxyHost"`
	ProxyPort         int    `json:"proxyPort"`
	ProxyUser         string `json:"proxyUser"`
	ProxyPassword     string `json:"proxyPassword"`
	NoProxy           string `json:"noProxy"`
	ProxyWarmup       bool   `json:"proxyWarmup"`
	TarWorkers        int    `json:"tarWorkers"`
	UploadWorkers     int    `json:"uploadWorkers"`
	JobWorkers        int    `json:"jobWorkers"`
	ExcludePatterns   string `json:"excludePatterns"`
	IncludePatterns   string `json:"includePatterns"`
	FlattenTar        bool   `json:"flattenTar"`
	TarCompression    string `json:"tarCompression"`
	ValidationPattern string `json:"validationPattern"`
	RunSubpath        string `json:"runSubpath"`
	MaxRetries        int    `json:"maxRetries"`
	DetailedLogging   bool   `json:"detailedLogging"` // v4.0.0: Toggle for timing/metrics in Activity tab
}

// GetConfig returns the current configuration.
func (a *App) GetConfig() ConfigDTO {
	if a.config == nil {
		return ConfigDTO{}
	}
	return ConfigDTO{
		APIBaseURL:        a.config.APIBaseURL,
		TenantURL:         a.config.TenantURL,
		APIKey:            a.config.APIKey,
		ProxyMode:         a.config.ProxyMode,
		ProxyHost:         a.config.ProxyHost,
		ProxyPort:         a.config.ProxyPort,
		ProxyUser:         a.config.ProxyUser,
		ProxyPassword:     a.config.ProxyPassword,
		NoProxy:           a.config.NoProxy,
		ProxyWarmup:       a.config.ProxyWarmup,
		TarWorkers:        a.config.TarWorkers,
		UploadWorkers:     a.config.UploadWorkers,
		JobWorkers:        a.config.JobWorkers,
		ExcludePatterns:   strings.Join(a.config.ExcludePatterns, ","),
		IncludePatterns:   strings.Join(a.config.IncludePatterns, ","),
		FlattenTar:        a.config.FlattenTar,
		TarCompression:    a.config.TarCompression,
		ValidationPattern: a.config.ValidationPattern,
		RunSubpath:        a.config.RunSubpath,
		MaxRetries:        a.config.MaxRetries,
		DetailedLogging:   a.config.DetailedLogging,
	}
}

// UpdateConfig applies a complete configuration update.
// v4.0.1: Now properly updates the engine's API client when API-related settings change.
func (a *App) UpdateConfig(cfg ConfigDTO) error {
	wailsLogger.Info().Msg("UpdateConfig: ENTER")
	if a.config == nil {
		wailsLogger.Warn().Msg("UpdateConfig: config is nil, returning")
		return nil
	}

	// v4.0.1: Track if API-related settings changed
	// These affect the API client and require engine update
	// v4.5.9: Added NoProxy to trigger API client refresh when bypass list changes
	apiSettingsChanged := a.config.APIKey != cfg.APIKey ||
		a.config.APIBaseURL != cfg.APIBaseURL ||
		a.config.TenantURL != cfg.TenantURL ||
		a.config.ProxyMode != cfg.ProxyMode ||
		a.config.ProxyHost != cfg.ProxyHost ||
		a.config.ProxyPort != cfg.ProxyPort ||
		a.config.ProxyUser != cfg.ProxyUser ||
		a.config.ProxyPassword != cfg.ProxyPassword ||
		a.config.NoProxy != cfg.NoProxy

	a.config.APIBaseURL = cfg.APIBaseURL
	a.config.TenantURL = cfg.TenantURL
	a.config.APIKey = cfg.APIKey
	a.config.ProxyMode = cfg.ProxyMode
	a.config.ProxyHost = cfg.ProxyHost
	a.config.ProxyPort = cfg.ProxyPort
	a.config.ProxyUser = cfg.ProxyUser
	a.config.ProxyPassword = cfg.ProxyPassword
	a.config.NoProxy = cfg.NoProxy
	a.config.ProxyWarmup = cfg.ProxyWarmup
	a.config.TarWorkers = cfg.TarWorkers
	a.config.UploadWorkers = cfg.UploadWorkers
	a.config.JobWorkers = cfg.JobWorkers
	if cfg.ExcludePatterns != "" {
		a.config.ExcludePatterns = strings.Split(cfg.ExcludePatterns, ",")
	} else {
		a.config.ExcludePatterns = nil
	}
	if cfg.IncludePatterns != "" {
		a.config.IncludePatterns = strings.Split(cfg.IncludePatterns, ",")
	} else {
		a.config.IncludePatterns = nil
	}
	a.config.FlattenTar = cfg.FlattenTar
	a.config.TarCompression = cfg.TarCompression
	a.config.ValidationPattern = cfg.ValidationPattern
	a.config.RunSubpath = cfg.RunSubpath
	a.config.MaxRetries = cfg.MaxRetries
	a.config.DetailedLogging = cfg.DetailedLogging

	// tenant_url is a legacy alias — keep in sync (both directions)
	if a.config.TenantURL == "" && a.config.APIBaseURL != "" {
		a.config.TenantURL = a.config.APIBaseURL
	}
	if a.config.APIBaseURL == "" && a.config.TenantURL != "" {
		a.config.APIBaseURL = a.config.TenantURL
	}

	// v4.0.0: Update timing system when DetailedLogging changes
	cloud.SetDetailedLogging(cfg.DetailedLogging)

	// v4.0.1: Update engine's API client when API-related settings change
	// This fixes the bug where typing a new API key and clicking "Test Connection"
	// would fail because the engine still had the old API client from startup.
	if apiSettingsChanged && a.engine != nil {
		wailsLogger.Info().Msg("UpdateConfig: API settings changed, starting background engine update")
		// Run in background to avoid blocking UI during proxy warmup
		go func() {
			if err := a.engine.UpdateConfig(a.config); err != nil {
				wailsLogger.Error().Err(err).Msg("Failed to update engine config")
			}
			wailsLogger.Info().Msg("UpdateConfig: background engine update completed")
		}()
	}

	wailsLogger.Info().Msg("UpdateConfig: EXIT")
	return nil
}

// SaveConfig saves to the default location.
// v4.0.8: Also saves API key to token file for persistence.
// The API key is saved separately from config.csv for security (0600 permissions on token file).
func (a *App) SaveConfig() error {
	if a.config == nil {
		return nil
	}

	// Save config.csv (everything except api_key and proxy_password for security)
	configPath := config.GetDefaultConfigPath()
	a.logInfo("config", fmt.Sprintf("Saving config to %s", configPath))
	if err := config.SaveConfigCSV(a.config, configPath); err != nil {
		a.logError("config", fmt.Sprintf("Failed to save config.csv: %v", err))
		return err
	}

	// v4.0.8: Also save API key to token file if set
	// This ensures the API key persists across restarts when user saves from GUI
	if a.config.APIKey != "" {
		tokenPath := config.GetDefaultTokenPath()
		a.logDebug("config", fmt.Sprintf("Saving API key to token file %s", tokenPath))
		if err := config.WriteTokenFile(tokenPath, a.config.APIKey); err != nil {
			a.logError("config", fmt.Sprintf("Failed to save token file: %v", err))
			return fmt.Errorf("failed to save API key: %w", err)
		}
		a.logInfo("config", "API key saved successfully")
	}

	a.logInfo("config", "Config saved successfully")
	return nil
}

// SaveConfigAs saves to a user-specified location (export).
// v4.0.8: Added for exporting config to custom locations.
func (a *App) SaveConfigAs(path string) error {
	if a.config == nil {
		return nil
	}
	if path == "" {
		return nil
	}
	return config.SaveConfigCSV(a.config, path)
}

// GetDefaultConfigPath returns the default config file location.
// v4.0.8: Exposed so UI can show where config will be saved.
func (a *App) GetDefaultConfigPath() string {
	return config.GetDefaultConfigPath()
}

// LoadConfigFromPath loads configuration from a specific path.
func (a *App) LoadConfigFromPath(path string) error {
	cfg, err := config.LoadConfigCSV(path)
	if err != nil {
		return err
	}
	a.config = cfg
	return nil
}

// ConnectionResultDTO contains the result of a connection test.
type ConnectionResultDTO struct {
	Success       bool   `json:"success"`
	Email         string `json:"email,omitempty"`
	FullName      string `json:"fullName,omitempty"`
	WorkspaceID   string `json:"workspaceId,omitempty"`
	WorkspaceName string `json:"workspaceName,omitempty"`
	Error         string `json:"error,omitempty"`
}

// testConnectionMu prevents concurrent TestConnection calls
var testConnectionMu sync.Mutex
var testConnectionInProgress bool

// TestConnection tests API connectivity with a guaranteed 7-second timeout.
// v4.0.8: Uses goroutine with hard select/time.After timeout to guarantee return.
// Also prevents concurrent calls which can cause UI confusion.
func (a *App) TestConnection() ConnectionResultDTO {
	a.logInfo("connection", "Testing API connection...")

	// Prevent concurrent calls
	testConnectionMu.Lock()
	if testConnectionInProgress {
		testConnectionMu.Unlock()
		a.logWarn("connection", "Connection test already in progress")
		return ConnectionResultDTO{
			Success: false,
			Error:   "Connection test already in progress",
		}
	}
	testConnectionInProgress = true
	testConnectionMu.Unlock()

	// Ensure we clear the flag when done
	defer func() {
		testConnectionMu.Lock()
		testConnectionInProgress = false
		testConnectionMu.Unlock()
	}()

	// Quick validation checks - these don't block, do them first
	if a.config == nil {
		a.logError("connection", "No configuration loaded")
		return ConnectionResultDTO{
			Success: false,
			Error:   "No configuration loaded",
		}
	}

	if a.config.APIKey == "" {
		a.logWarn("connection", "API key is empty")
		return ConnectionResultDTO{
			Success: false,
			Error:   "API key is empty - please enter an API key",
		}
	}

	// v4.5.1: Removed API key fragment from log for security (no value in logging partial keys)
	a.logDebug("connection", "Testing API connection...")

	// Copy config values we need - avoid race conditions with concurrent config updates
	// v4.5.9: Added NoProxy so test connection respects bypass rules
	configCopy := &config.Config{
		APIBaseURL:    a.config.APIBaseURL,
		APIKey:        a.config.APIKey,
		ProxyMode:     a.config.ProxyMode,
		ProxyHost:     a.config.ProxyHost,
		ProxyPort:     a.config.ProxyPort,
		ProxyUser:     a.config.ProxyUser,
		ProxyPassword: a.config.ProxyPassword,
		NoProxy:       a.config.NoProxy,
		ProxyWarmup:   false, // CRITICAL: Disable proxy warmup for connection test to avoid blocking
	}

	// Channel to receive result from worker goroutine
	resultChan := make(chan ConnectionResultDTO, 1)

	// Run all potentially blocking work in a goroutine
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()

		apiClient, err := api.NewClient(configCopy)
		if err != nil {
			resultChan <- ConnectionResultDTO{
				Success: false,
				Error:   "Failed to create API client: " + err.Error(),
			}
			return
		}

		profile, err := apiClient.GetUserProfile(ctx)
		if err != nil {
			errMsg := err.Error()
			if ctx.Err() == context.DeadlineExceeded {
				errMsg = "Connection timed out - check your network and API key"
			} else if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "Unauthorized") || strings.Contains(errMsg, "Invalid token") {
				errMsg = "Invalid API key - please check your API key"
			}
			resultChan <- ConnectionResultDTO{
				Success: false,
				Error:   errMsg,
			}
			return
		}

		resultChan <- ConnectionResultDTO{
			Success:       true,
			Email:         profile.Email,
			FullName:      profile.FullName,
			WorkspaceID:   profile.Workspace.ID,
			WorkspaceName: profile.Workspace.Name,
		}
	}()

	// CRITICAL: Hard timeout guarantee - function WILL return within 7 seconds
	select {
	case result := <-resultChan:
		if result.Success {
			a.logInfo("connection", fmt.Sprintf("Connected successfully - %s (%s)", result.Email, result.WorkspaceName))
			// v4.0.8: Clear catalog cache when connection succeeds - user may have switched accounts
			a.ClearCatalogCache()
		} else {
			a.logError("connection", fmt.Sprintf("Connection failed: %s", result.Error))
		}
		return result
	case <-time.After(7 * time.Second):
		a.logError("connection", "Connection timed out after 7 seconds")
		return ConnectionResultDTO{
			Success: false,
			Error:   "Connection timed out after 7 seconds",
		}
	}
}

// SelectDirectory opens a directory dialog.
func (a *App) SelectDirectory(title string) (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
}

// SelectFile opens a file dialog.
func (a *App) SelectFile(title string) (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
}

// SelectMultipleFiles opens a file dialog that allows selecting multiple files.
func (a *App) SelectMultipleFiles(title string) ([]string, error) {
	return runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
}

// SaveFile opens a save file dialog.
func (a *App) SaveFile(title string) (string, error) {
	return runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{Title: title})
}

// =============================================================================
// Auto-Download Configuration
// =============================================================================
// v4.3.1: DEPRECATED - Auto-download config is now unified in daemon.conf
// Use GetDaemonConfig() and SaveDaemonConfig() from daemon_bindings.go instead.
// The old apiconfig file is no longer used.
