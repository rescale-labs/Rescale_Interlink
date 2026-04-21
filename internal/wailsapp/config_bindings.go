// Package wailsapp provides configuration-related Wails bindings.
package wailsapp

import (
	"context"
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/config"
	intfips "github.com/rescale/rescale-int/internal/fips"
)

// AppInfoDTO contains application version, FIPS, and platform information.
//
// OS and SessionScopedDaemon describe the runtime's auto-download lifecycle
// posture per spec §2.2/§2.3: on macOS and Linux the daemon is a detached
// subprocess scoped to the user's login session (dies on logout/reboot, not
// on GUI close). The frontend uses SessionScopedDaemon to render an
// informational banner on non-Windows.
type AppInfoDTO struct {
	Version             string           `json:"version"`
	BuildTime           string           `json:"buildTime"`
	FIPSEnabled         bool             `json:"fipsEnabled"`
	FIPSStatus          string           `json:"fipsStatus"`
	OS                  string           `json:"os"`
	SessionScopedDaemon bool             `json:"sessionScopedDaemon"`
	VersionCheck        *VersionCheckDTO `json:"versionCheck,omitempty"`
}

// GetAppInfo returns version, build time, FIPS status, and platform info.
func (a *App) GetAppInfo() AppInfoDTO {
	info := AppInfoDTO{
		Version:             cli.Version,
		BuildTime:           cli.BuildTime,
		FIPSEnabled:         intfips.Enabled,
		FIPSStatus:          cli.FIPSStatus(),
		OS:                  goruntime.GOOS,
		SessionScopedDaemon: goruntime.GOOS == "darwin" || goruntime.GOOS == "linux",
	}

	// Include cached version check if available and not expired
	versionCheckCache.mu.RLock()
	if versionCheckCache.cacheValid {
		cacheTTL := successCacheDuration
		if versionCheckCache.result.Error != "" {
			cacheTTL = errorCacheDuration
		}
		if time.Since(versionCheckCache.lastCheck) < cacheTTL {
			result := versionCheckCache.result
			info.VersionCheck = &result
		}
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
	DetailedLogging   bool   `json:"detailedLogging"`
}

// GetConfig returns the current configuration.
func (a *App) GetConfig() ConfigDTO {
	if a.config == nil {
		return ConfigDTO{}
	}
	// Normalize legacy "gz" to "gzip" for consistent frontend display
	compression := a.config.TarCompression
	if compression == "gz" {
		compression = "gzip"
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
		TarCompression:    compression,
		ValidationPattern: a.config.ValidationPattern,
		RunSubpath:        a.config.RunSubpath,
		MaxRetries:        a.config.MaxRetries,
		DetailedLogging:   a.config.DetailedLogging,
	}
}

// UpdateConfig applies a complete configuration update.
func (a *App) UpdateConfig(cfg ConfigDTO) error {
	wailsLogger.Info().Msg("UpdateConfig: ENTER")
	if a.config == nil {
		wailsLogger.Warn().Msg("UpdateConfig: config is nil, returning")
		return nil
	}

	// Track if API-related settings changed — these affect the API client and require engine update
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

	// Update timing system when DetailedLogging changes
	cloud.SetDetailedLogging(cfg.DetailedLogging)

	// Update engine's API client when API-related settings change.
	// Without this, typing a new API key and clicking "Test Connection" would fail
	// because the engine still had the old API client from startup.
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

	// Save API key to token file so it persists across restarts
	if a.config.APIKey != "" {
		tokenPath := config.GetDefaultTokenPath()
		a.logDebug("config", fmt.Sprintf("Saving API key to token file %s", tokenPath))
		if err := config.WriteTokenFile(tokenPath, a.config.APIKey); err != nil {
			a.logError("config", fmt.Sprintf("Failed to save token file: %v", err))
			return fmt.Errorf("failed to save API key: %w", err)
		}
		a.logInfo("config", "API key saved successfully")
	} else {
		// User cleared the API key — remove persisted token file
		tokenPath := config.GetDefaultTokenPath()
		if tokenPath != "" {
			if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
				a.logError("config", fmt.Sprintf("Failed to remove token file: %v", err))
			} else if err == nil {
				a.logInfo("config", "API key cleared, token file removed")
			}
		}
	}

	a.logInfo("config", "Config saved successfully")
	return nil
}

// SaveConfigAs saves to a user-specified location (export).
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
// Uses goroutine with hard select/time.After to guarantee return even if the
// underlying HTTP request hangs. Prevents concurrent calls which cause UI confusion.
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

	a.logDebug("connection", "Testing API connection...")

	// Copy config values we need - avoid race conditions with concurrent config updates
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
			// Clear catalog cache when connection succeeds - user may have switched accounts
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

// dialogMu serializes all native file/folder dialog calls so we never invoke
// two GTK dialog runs concurrently. Wails's Linux dialog path uses a shared
// unbuffered result channel that deadlocks under overlap. This also lets
// the GVFS / WebKit stack finish one dialog's teardown before the next
// starts. Frontend button-gating is best-effort UX; this is the correctness
// layer.
//
// dialogPanicMessage is the error returned to callers when a panic inside
// the CGo dialog call is recovered. Upstream wailsapp/wails#3965 explains
// why panics here were historically fatal on Linux (WebKit's signal handler
// setup); v2.12.0 + per-call ResetSignalHandlers() mitigates the class, but
// we keep the recovery as a belt-and-suspenders guard.
var dialogMu sync.Mutex

const (
	dialogBusyMessage = "a file dialog is already open"
	appNotReadyError  = "application not ready"
)

// Runtime indirection for tests. Production code uses the real Wails runtime;
// tests swap these to stubs that panic or return fixed values to verify the
// wrapper's mutex, recovery, and error-handling contract.
var (
	openDirectoryDialog      = runtime.OpenDirectoryDialog
	openFileDialog           = runtime.OpenFileDialog
	openMultipleFilesDialog  = runtime.OpenMultipleFilesDialog
	saveFileDialog           = runtime.SaveFileDialog
)

func recoverDialogPanic(binding string, err *error) {
	if r := recover(); r != nil {
		wailsLogger.Error().Interface("panic", r).Str("binding", binding).Msg("recovered panic in dialog binding")
		*err = fmt.Errorf("dialog call failed: %v", r)
	}
}

// SelectDirectory opens a directory dialog.
func (a *App) SelectDirectory(title string) (result string, err error) {
	if !dialogMu.TryLock() {
		return "", fmt.Errorf(dialogBusyMessage)
	}
	defer dialogMu.Unlock()
	defer recoverDialogPanic("SelectDirectory", &err)
	if a.ctx == nil {
		wailsLogger.Error().Str("binding", "SelectDirectory").Msg("dialog binding invoked before context ready")
		return "", fmt.Errorf(appNotReadyError)
	}
	wailsLogger.Debug().Str("binding", "SelectDirectory").Str("title", title).Msg("opening dialog")
	resetLinuxSignalHandlers()
	result, err = openDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
	if err != nil {
		wailsLogger.Error().Err(err).Str("binding", "SelectDirectory").Msg("dialog returned error")
	} else {
		wailsLogger.Debug().Str("binding", "SelectDirectory").Str("result", result).Msg("dialog returned")
	}
	return
}

// SelectFile opens a file dialog.
func (a *App) SelectFile(title string) (result string, err error) {
	if !dialogMu.TryLock() {
		return "", fmt.Errorf(dialogBusyMessage)
	}
	defer dialogMu.Unlock()
	defer recoverDialogPanic("SelectFile", &err)
	if a.ctx == nil {
		wailsLogger.Error().Str("binding", "SelectFile").Msg("dialog binding invoked before context ready")
		return "", fmt.Errorf(appNotReadyError)
	}
	wailsLogger.Debug().Str("binding", "SelectFile").Str("title", title).Msg("opening dialog")
	resetLinuxSignalHandlers()
	result, err = openFileDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
	if err != nil {
		wailsLogger.Error().Err(err).Str("binding", "SelectFile").Msg("dialog returned error")
	} else {
		wailsLogger.Debug().Str("binding", "SelectFile").Str("result", result).Msg("dialog returned")
	}
	return
}

// SelectMultipleFiles opens a file dialog that allows selecting multiple files.
func (a *App) SelectMultipleFiles(title string) (result []string, err error) {
	if !dialogMu.TryLock() {
		return nil, fmt.Errorf(dialogBusyMessage)
	}
	defer dialogMu.Unlock()
	defer recoverDialogPanic("SelectMultipleFiles", &err)
	if a.ctx == nil {
		wailsLogger.Error().Str("binding", "SelectMultipleFiles").Msg("dialog binding invoked before context ready")
		return nil, fmt.Errorf(appNotReadyError)
	}
	wailsLogger.Debug().Str("binding", "SelectMultipleFiles").Str("title", title).Msg("opening dialog")
	resetLinuxSignalHandlers()
	result, err = openMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
	if err != nil {
		wailsLogger.Error().Err(err).Str("binding", "SelectMultipleFiles").Msg("dialog returned error")
	} else {
		wailsLogger.Debug().Str("binding", "SelectMultipleFiles").Int("count", len(result)).Msg("dialog returned")
	}
	return
}

// SaveFile opens a save file dialog.
func (a *App) SaveFile(title string) (result string, err error) {
	if !dialogMu.TryLock() {
		return "", fmt.Errorf(dialogBusyMessage)
	}
	defer dialogMu.Unlock()
	defer recoverDialogPanic("SaveFile", &err)
	if a.ctx == nil {
		wailsLogger.Error().Str("binding", "SaveFile").Msg("dialog binding invoked before context ready")
		return "", fmt.Errorf(appNotReadyError)
	}
	wailsLogger.Debug().Str("binding", "SaveFile").Str("title", title).Msg("opening dialog")
	resetLinuxSignalHandlers()
	result, err = saveFileDialog(a.ctx, runtime.SaveDialogOptions{Title: title})
	if err != nil {
		wailsLogger.Error().Err(err).Str("binding", "SaveFile").Msg("dialog returned error")
	} else {
		wailsLogger.Debug().Str("binding", "SaveFile").Str("result", result).Msg("dialog returned")
	}
	return
}

// =============================================================================
// Auto-Download Configuration
// =============================================================================
// DEPRECATED - Auto-download config is now unified in daemon.conf.
// Use GetDaemonConfig() and SaveDaemonConfig() from daemon_bindings.go instead.
