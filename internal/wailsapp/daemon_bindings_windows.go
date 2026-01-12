//go:build windows

// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
package wailsapp

import (
	"context"
	"fmt"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/service"
)

// DaemonStatusDTO represents the daemon status for the frontend.
type DaemonStatusDTO struct {
	// Running indicates if the daemon process is running
	Running bool `json:"running"`

	// PID is the process ID (0 if not running)
	PID int `json:"pid"`

	// IPCConnected indicates if we can communicate with the daemon via IPC
	IPCConnected bool `json:"ipcConnected"`

	// State is the daemon state ("running", "paused", "stopped", "unknown")
	State string `json:"state"`

	// Version is the daemon version
	Version string `json:"version"`

	// Uptime is how long the daemon has been running (e.g., "5m30s")
	Uptime string `json:"uptime"`

	// LastScan is the time of the last job scan (ISO format, or empty)
	LastScan string `json:"lastScan"`

	// ActiveDownloads is the number of downloads currently in progress
	ActiveDownloads int `json:"activeDownloads"`

	// JobsDownloaded is the total number of jobs downloaded
	JobsDownloaded int `json:"jobsDownloaded"`

	// DownloadFolder is the configured download directory
	DownloadFolder string `json:"downloadFolder"`

	// Error message if status query failed
	Error string `json:"error,omitempty"`

	// ManagedBy indicates if daemon is managed externally ("Windows Service", "", etc.)
	ManagedBy string `json:"managedBy,omitempty"`
}

// GetDaemonStatus returns the current daemon status.
// On Windows, this checks both the Windows Service status and IPC connection.
func (a *App) GetDaemonStatus() DaemonStatusDTO {
	result := DaemonStatusDTO{
		State: "stopped",
	}

	// Check Windows Service status
	svcStatus, err := service.QueryStatus()
	if err == nil {
		switch svcStatus {
		case service.StatusRunning:
			result.Running = true
			result.State = "running"
			result.ManagedBy = "Windows Service"
		case service.StatusPaused:
			result.Running = true
			result.State = "paused"
			result.ManagedBy = "Windows Service"
		case service.StatusStartPending, service.StatusContinuePending:
			result.Running = true
			result.State = "starting"
			result.ManagedBy = "Windows Service"
		case service.StatusStopPending, service.StatusPausePending:
			result.Running = true
			result.State = "stopping"
			result.ManagedBy = "Windows Service"
		}
	}

	// Try to connect via IPC for live status (works for both service and standalone daemon)
	client := ipc.NewClient()
	client.SetTimeout(2 * time.Second)

	ctx := context.Background()
	if status, err := client.GetStatus(ctx); err == nil {
		result.IPCConnected = true
		result.Running = true // If IPC responds, daemon is running
		result.State = status.ServiceState
		result.Version = status.Version
		result.Uptime = status.Uptime
		result.ActiveDownloads = status.ActiveDownloads

		if status.LastScanTime != nil {
			result.LastScan = status.LastScanTime.Format(time.RFC3339)
		}

		// Get user-specific info
		if users, err := client.GetUserList(ctx); err == nil && len(users) > 0 {
			result.JobsDownloaded = users[0].JobsDownloaded
			result.DownloadFolder = users[0].DownloadFolder
		}
	} else if result.Running {
		// Service is running but IPC not responding
		result.Error = "Service is running but IPC not responding"
	}

	return result
}

// StartDaemon starts the daemon via Windows Service.
func (a *App) StartDaemon() error {
	// Check current status
	status, err := service.QueryStatus()
	if err != nil {
		return fmt.Errorf("failed to query service status: %w", err)
	}

	if status == service.StatusRunning {
		return fmt.Errorf("service is already running")
	}

	a.logInfo("Daemon", "Starting Windows Service...")

	if err := service.StartService(); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	// Wait for service to start
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		status, _ := service.QueryStatus()
		if status == service.StatusRunning {
			a.logInfo("Daemon", "Windows Service started successfully")
			return nil
		}
	}

	return fmt.Errorf("service start timeout; check Windows Event Log for details")
}

// StopDaemon stops the daemon via Windows Service.
func (a *App) StopDaemon() error {
	status, err := service.QueryStatus()
	if err != nil {
		return fmt.Errorf("failed to query service status: %w", err)
	}

	if status == service.StatusStopped {
		return nil // Already stopped
	}

	a.logInfo("Daemon", "Stopping Windows Service...")

	if err := service.StopService(); err != nil {
		return fmt.Errorf("failed to stop service: %w", err)
	}

	// Wait for service to stop
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		status, _ := service.QueryStatus()
		if status == service.StatusStopped {
			a.logInfo("Daemon", "Windows Service stopped")
			return nil
		}
	}

	return fmt.Errorf("service stop timeout")
}

// TriggerDaemonScan triggers an immediate job scan via IPC.
func (a *App) TriggerDaemonScan() error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if !client.IsServiceRunning(ctx) {
		return fmt.Errorf("daemon is not running or IPC not available")
	}

	if err := client.TriggerScan(ctx, ""); err != nil {
		return fmt.Errorf("failed to trigger scan: %w", err)
	}

	a.logInfo("Daemon", "Scan triggered")
	return nil
}

// PauseDaemon pauses the daemon's auto-download polling via IPC.
func (a *App) PauseDaemon() error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if !client.IsServiceRunning(ctx) {
		return fmt.Errorf("daemon is not running or IPC not available")
	}

	if err := client.PauseUser(ctx, ""); err != nil {
		return fmt.Errorf("failed to pause daemon: %w", err)
	}

	a.logInfo("Daemon", "Daemon paused")
	return nil
}

// ResumeDaemon resumes the daemon's auto-download polling via IPC.
func (a *App) ResumeDaemon() error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if !client.IsServiceRunning(ctx) {
		return fmt.Errorf("daemon is not running or IPC not available")
	}

	if err := client.ResumeUser(ctx, ""); err != nil {
		return fmt.Errorf("failed to resume daemon: %w", err)
	}

	a.logInfo("Daemon", "Daemon resumed")
	return nil
}

// DaemonConfigDTO represents the daemon configuration for the frontend.
// v4.2.0: Used for reading/writing daemon.conf from the GUI.
type DaemonConfigDTO struct {
	// Daemon core settings
	Enabled             bool   `json:"enabled"`
	DownloadFolder      string `json:"downloadFolder"`
	PollIntervalMinutes int    `json:"pollIntervalMinutes"`
	UseJobNameDir       bool   `json:"useJobNameDir"`
	MaxConcurrent       int    `json:"maxConcurrent"`
	LookbackDays        int    `json:"lookbackDays"`

	// Filter settings
	NamePrefix   string `json:"namePrefix"`
	NameContains string `json:"nameContains"`
	Exclude      string `json:"exclude"` // Comma-separated

	// Eligibility
	CorrectnessTag    string `json:"correctnessTag"`
	AutoDownloadValue string `json:"autoDownloadValue"` // v4.2.1: Configurable (default: "Enable")
	DownloadedTag     string `json:"downloadedTag"`     // v4.2.1: Configurable (default: "autoDownloaded:true")

	// Notifications
	NotificationsEnabled bool `json:"notificationsEnabled"`
	ShowDownloadComplete bool `json:"showDownloadComplete"`
	ShowDownloadFailed   bool `json:"showDownloadFailed"`

	// Config file path (read-only)
	ConfigPath string `json:"configPath"`
}

// GetDaemonConfig returns the current daemon configuration.
// v4.2.0: Reads from daemon.conf instead of apiconfig.
func (a *App) GetDaemonConfig() DaemonConfigDTO {
	result := DaemonConfigDTO{}

	// Get config file path
	path, _ := config.DefaultDaemonConfigPath()
	result.ConfigPath = path

	// Load config
	cfg, err := config.LoadDaemonConfig("")
	if err != nil {
		a.logWarn("Daemon", fmt.Sprintf("Failed to load daemon.conf: %v", err))
		cfg = config.NewDaemonConfig()
	}

	// Map to DTO
	result.Enabled = cfg.Daemon.Enabled
	result.DownloadFolder = cfg.Daemon.DownloadFolder
	result.PollIntervalMinutes = cfg.Daemon.PollIntervalMinutes
	result.UseJobNameDir = cfg.Daemon.UseJobNameDir
	result.MaxConcurrent = cfg.Daemon.MaxConcurrent
	result.LookbackDays = cfg.Daemon.LookbackDays

	result.NamePrefix = cfg.Filters.NamePrefix
	result.NameContains = cfg.Filters.NameContains
	result.Exclude = cfg.Filters.Exclude

	result.CorrectnessTag = cfg.Eligibility.CorrectnessTag
	result.AutoDownloadValue = cfg.Eligibility.AutoDownloadValue
	result.DownloadedTag = cfg.Eligibility.DownloadedTag

	result.NotificationsEnabled = cfg.Notifications.Enabled
	result.ShowDownloadComplete = cfg.Notifications.ShowDownloadComplete
	result.ShowDownloadFailed = cfg.Notifications.ShowDownloadFailed

	return result
}

// SaveDaemonConfig saves daemon configuration to daemon.conf.
// v4.2.0: Writes to daemon.conf.
func (a *App) SaveDaemonConfig(dto DaemonConfigDTO) error {
	// Load existing config to preserve any fields not in DTO
	cfg, err := config.LoadDaemonConfig("")
	if err != nil {
		cfg = config.NewDaemonConfig()
	}

	// Map DTO to config
	cfg.Daemon.Enabled = dto.Enabled
	cfg.Daemon.DownloadFolder = dto.DownloadFolder
	cfg.Daemon.PollIntervalMinutes = dto.PollIntervalMinutes
	cfg.Daemon.UseJobNameDir = dto.UseJobNameDir
	cfg.Daemon.MaxConcurrent = dto.MaxConcurrent
	cfg.Daemon.LookbackDays = dto.LookbackDays

	cfg.Filters.NamePrefix = dto.NamePrefix
	cfg.Filters.NameContains = dto.NameContains
	cfg.Filters.Exclude = dto.Exclude

	cfg.Eligibility.CorrectnessTag = dto.CorrectnessTag
	cfg.Eligibility.AutoDownloadValue = dto.AutoDownloadValue
	cfg.Eligibility.DownloadedTag = dto.DownloadedTag

	cfg.Notifications.Enabled = dto.NotificationsEnabled
	cfg.Notifications.ShowDownloadComplete = dto.ShowDownloadComplete
	cfg.Notifications.ShowDownloadFailed = dto.ShowDownloadFailed

	// Validate before saving
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Save
	if err := config.SaveDaemonConfig(cfg, ""); err != nil {
		return fmt.Errorf("failed to save daemon.conf: %w", err)
	}

	a.logInfo("Daemon", "Configuration saved to daemon.conf")
	return nil
}

// GetDefaultDownloadFolder returns the platform-specific default download folder.
func (a *App) GetDefaultDownloadFolder() string {
	return config.DefaultDownloadFolder()
}

// AutoDownloadValidationDTO represents the result of validating auto-download setup.
// v4.2.1: Added for workspace custom fields validation
type AutoDownloadValidationDTO struct {
	CustomFieldsEnabled      bool     `json:"customFieldsEnabled"`
	HasAutoDownloadField     bool     `json:"hasAutoDownloadField"`
	AutoDownloadFieldType    string   `json:"autoDownloadFieldType"`
	AutoDownloadFieldSection string   `json:"autoDownloadFieldSection"`
	AvailableValues          []string `json:"availableValues"`
	HasAutoDownloadPathField bool     `json:"hasAutoDownloadPathField"`
	Warnings                 []string `json:"warnings"`
	Errors                   []string `json:"errors"`
}

// ValidateAutoDownloadSetup checks if the workspace has the required custom fields.
// v4.2.1: Added for GUI auto-download setup validation
func (a *App) ValidateAutoDownloadSetup() AutoDownloadValidationDTO {
	result := AutoDownloadValidationDTO{
		AvailableValues: []string{},
		Warnings:        []string{},
		Errors:          []string{},
	}

	// Check if we have an engine with API client
	if a.engine == nil {
		result.Errors = append(result.Errors, "Engine not initialized")
		return result
	}

	apiClient := a.engine.API()
	if apiClient == nil {
		result.Errors = append(result.Errors, "API client not available - check API key configuration")
		return result
	}

	// Run validation
	ctx := context.Background()
	validation, err := apiClient.ValidateAutoDownloadSetup(ctx)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("Validation failed: %v", err))
		return result
	}

	// Map to DTO
	result.CustomFieldsEnabled = validation.CustomFieldsEnabled
	result.HasAutoDownloadField = validation.HasAutoDownloadField
	result.AutoDownloadFieldType = validation.AutoDownloadFieldType
	result.AutoDownloadFieldSection = validation.AutoDownloadFieldSection
	result.HasAutoDownloadPathField = validation.HasAutoDownloadPathField

	if validation.AvailableValues != nil {
		result.AvailableValues = validation.AvailableValues
	}
	if validation.Warnings != nil {
		result.Warnings = validation.Warnings
	}
	if validation.Errors != nil {
		result.Errors = validation.Errors
	}

	return result
}

// IsServiceInstalled returns whether the Windows Service is installed.
func (a *App) IsServiceInstalled() bool {
	return service.IsInstalled()
}

// InstallService attempts to install the Windows Service.
// This typically requires Administrator privileges.
func (a *App) InstallService() error {
	if service.IsInstalled() {
		return fmt.Errorf("service is already installed")
	}

	a.logInfo("Daemon", "Installing Windows Service...")

	if err := service.Install(); err != nil {
		return fmt.Errorf("failed to install service (Administrator privileges required): %w", err)
	}

	a.logInfo("Daemon", "Windows Service installed successfully")
	return nil
}
