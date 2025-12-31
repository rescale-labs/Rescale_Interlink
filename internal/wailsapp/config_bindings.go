// Package wailsapp provides configuration-related Wails bindings.
package wailsapp

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/config"
)

// AppInfoDTO contains application version and status information.
type AppInfoDTO struct {
	Version     string `json:"version"`
	BuildTime   string `json:"buildTime"`
	FIPSEnabled bool   `json:"fipsEnabled"`
	FIPSStatus  string `json:"fipsStatus"`
}

// GetAppInfo returns version, build time, and FIPS status.
func (a *App) GetAppInfo() AppInfoDTO {
	return AppInfoDTO{
		Version:     cli.Version,
		BuildTime:   cli.BuildTime,
		FIPSEnabled: cli.FIPSStatus() != "",
		FIPSStatus:  cli.FIPSStatus(),
	}
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
	if a.config == nil {
		return nil
	}

	// v4.0.1: Track if API-related settings changed
	// These affect the API client and require engine update
	apiSettingsChanged := a.config.APIKey != cfg.APIKey ||
		a.config.APIBaseURL != cfg.APIBaseURL ||
		a.config.TenantURL != cfg.TenantURL ||
		a.config.ProxyMode != cfg.ProxyMode ||
		a.config.ProxyHost != cfg.ProxyHost ||
		a.config.ProxyPort != cfg.ProxyPort ||
		a.config.ProxyUser != cfg.ProxyUser ||
		a.config.ProxyPassword != cfg.ProxyPassword

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

	// v4.0.0: Update timing system when DetailedLogging changes
	cloud.SetDetailedLogging(cfg.DetailedLogging)

	// v4.0.1: Update engine's API client when API-related settings change
	// This fixes the bug where typing a new API key and clicking "Test Connection"
	// would fail because the engine still had the old API client from startup.
	if apiSettingsChanged && a.engine != nil {
		// Run in background to avoid blocking UI during proxy warmup
		go func() {
			if err := a.engine.UpdateConfig(a.config); err != nil {
				wailsLogger.Error().Err(err).Msg("Failed to update engine config")
			}
		}()
	}

	return nil
}

// SaveConfig saves to the default location.
func (a *App) SaveConfig() error {
	if a.config == nil {
		return nil
	}
	return config.SaveConfigCSV(a.config, config.GetDefaultConfigPath())
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

// TestConnection tests API connectivity asynchronously.
func (a *App) TestConnection() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if a.engine == nil || a.engine.API() == nil {
			runtime.EventsEmit(a.ctx, "interlink:connection_result", ConnectionResultDTO{
				Success: false,
				Error:   "No API client configured",
			})
			return
		}

		profile, err := a.engine.API().GetUserProfile(ctx)
		if err != nil {
			runtime.EventsEmit(a.ctx, "interlink:connection_result", ConnectionResultDTO{
				Success: false,
				Error:   err.Error(),
			})
			return
		}

		runtime.EventsEmit(a.ctx, "interlink:connection_result", ConnectionResultDTO{
			Success:       true,
			Email:         profile.Email,
			FullName:      profile.FullName,
			WorkspaceID:   profile.Workspace.ID,
			WorkspaceName: profile.Workspace.Name,
		})
	}()
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
// Auto-Download Configuration (v4.0.0)
// =============================================================================

// AutoDownloadConfigDTO is the JSON-safe structure for auto-download settings.
// This represents the configuration contract between GUI and the Windows Service.
type AutoDownloadConfigDTO struct {
	// Rescale connection settings
	PlatformURL string `json:"platformUrl"`
	APIKey      string `json:"apiKey"`

	// Auto-download settings
	Enabled               bool   `json:"enabled"`
	CorrectnessTag        string `json:"correctnessTag"`
	DefaultDownloadFolder string `json:"defaultDownloadFolder"`
	ScanIntervalMinutes   int    `json:"scanIntervalMinutes"`
	LookbackDays          int    `json:"lookbackDays"`
}

// AutoDownloadStatusDTO contains the current auto-download service status.
type AutoDownloadStatusDTO struct {
	ConfigExists  bool   `json:"configExists"`
	Enabled       bool   `json:"enabled"`
	IsValid       bool   `json:"isValid"`
	ValidationMsg string `json:"validationMsg,omitempty"`
}

// GetAutoDownloadConfig loads the current auto-download configuration.
func (a *App) GetAutoDownloadConfig() AutoDownloadConfigDTO {
	cfg, err := config.LoadAPIConfig("")
	if err != nil || cfg == nil {
		// Return empty defaults
		return AutoDownloadConfigDTO{
			PlatformURL:         "https://platform.rescale.com",
			CorrectnessTag:      "isCorrect:true",
			ScanIntervalMinutes: 10,
			LookbackDays:        7,
		}
	}

	return AutoDownloadConfigDTO{
		PlatformURL:           cfg.PlatformURL,
		APIKey:                cfg.APIKey,
		Enabled:               cfg.AutoDownload.Enabled,
		CorrectnessTag:        cfg.AutoDownload.CorrectnessTag,
		DefaultDownloadFolder: cfg.AutoDownload.DefaultDownloadFolder,
		ScanIntervalMinutes:   cfg.AutoDownload.ScanIntervalMinutes,
		LookbackDays:          cfg.AutoDownload.LookbackDays,
	}
}

// SaveAutoDownloadConfig saves the auto-download configuration.
// Returns an error if validation fails or save fails.
func (a *App) SaveAutoDownloadConfig(dto AutoDownloadConfigDTO) error {
	cfg := &config.APIConfig{
		PlatformURL: dto.PlatformURL,
		APIKey:      dto.APIKey,
		AutoDownload: config.AutoDownloadConfig{
			Enabled:               dto.Enabled,
			CorrectnessTag:        dto.CorrectnessTag,
			DefaultDownloadFolder: dto.DefaultDownloadFolder,
			ScanIntervalMinutes:   dto.ScanIntervalMinutes,
			LookbackDays:          dto.LookbackDays,
		},
	}

	// Validate before saving
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Save to default location
	return config.SaveAPIConfig(cfg, "")
}

// GetAutoDownloadStatus returns the current status of the auto-download configuration.
func (a *App) GetAutoDownloadStatus() AutoDownloadStatusDTO {
	path, err := config.DefaultAPIConfigPath()
	if err != nil {
		return AutoDownloadStatusDTO{
			ConfigExists:  false,
			IsValid:       false,
			ValidationMsg: "Cannot determine config path: " + err.Error(),
		}
	}

	cfg, err := config.LoadAPIConfig(path)
	if err != nil {
		return AutoDownloadStatusDTO{
			ConfigExists:  false,
			IsValid:       false,
			ValidationMsg: "Failed to load config: " + err.Error(),
		}
	}

	// Check if config file actually exists (LoadAPIConfig returns defaults if not)
	configExists := false
	if _, statErr := os.Stat(path); statErr == nil {
		configExists = true
	}

	validationErr := cfg.Validate()
	isValid := validationErr == nil

	msg := ""
	if !isValid && validationErr != nil {
		msg = validationErr.Error()
	} else if cfg.AutoDownload.Enabled {
		msg = "Auto-download is enabled"
	} else {
		msg = "Auto-download is disabled"
	}

	return AutoDownloadStatusDTO{
		ConfigExists:  configExists,
		Enabled:       cfg.AutoDownload.Enabled,
		IsValid:       isValid,
		ValidationMsg: msg,
	}
}

// TestAutoDownloadConnection tests the connection and folder access for auto-download.
// This validates that the configured API key works and the download folder is accessible.
func (a *App) TestAutoDownloadConnection(dto AutoDownloadConfigDTO) {
	go func() {
		result := struct {
			Success      bool   `json:"success"`
			Email        string `json:"email,omitempty"`
			FolderOK     bool   `json:"folderOk"`
			FolderError  string `json:"folderError,omitempty"`
			Error        string `json:"error,omitempty"`
		}{}

		// Test API connection
		if dto.APIKey == "" {
			result.Error = "API key is required"
			runtime.EventsEmit(a.ctx, "interlink:autodownload_test_result", result)
			return
		}

		// Create temporary API client to test connection
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Use the engine's API if available and same URL, otherwise create new client
		if a.engine != nil && a.engine.API() != nil {
			profile, err := a.engine.API().GetUserProfile(ctx)
			if err != nil {
				result.Error = "API connection failed: " + err.Error()
				runtime.EventsEmit(a.ctx, "interlink:autodownload_test_result", result)
				return
			}
			result.Success = true
			result.Email = profile.Email
		} else {
			result.Error = "No API client available for testing"
			runtime.EventsEmit(a.ctx, "interlink:autodownload_test_result", result)
			return
		}

		// Test folder access
		if dto.DefaultDownloadFolder != "" {
			// Check if folder exists or can be created
			info, err := os.Stat(dto.DefaultDownloadFolder)
			if os.IsNotExist(err) {
				// Try to create it
				if err := os.MkdirAll(dto.DefaultDownloadFolder, 0755); err != nil {
					result.FolderError = "Cannot create folder: " + err.Error()
				} else {
					result.FolderOK = true
					// Clean up if we just created it and it's empty
				}
			} else if err != nil {
				result.FolderError = "Cannot access folder: " + err.Error()
			} else if !info.IsDir() {
				result.FolderError = "Path exists but is not a directory"
			} else {
				// Folder exists, test write access
				testFile := dto.DefaultDownloadFolder + "/.interlink_test"
				f, err := os.Create(testFile)
				if err != nil {
					result.FolderError = "Cannot write to folder: " + err.Error()
				} else {
					f.Close()
					os.Remove(testFile)
					result.FolderOK = true
				}
			}
		}

		runtime.EventsEmit(a.ctx, "interlink:autodownload_test_result", result)
	}()
}
