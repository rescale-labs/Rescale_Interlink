//go:build windows

// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
package wailsapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/pathutil"
	"github.com/rescale/rescale-int/internal/service"
	"github.com/rescale/rescale-int/internal/version"
)

// Windows process creation flag to hide console window.
// v4.3.9: Required for subprocess mode to not show a blank console.
const createNoWindow = 0x08000000

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
// v4.3.7: Primary check is IPC (works for both subprocess and service modes).
// SCM queries are skipped by default since they require admin privileges.
// v4.3.1: Version always uses version.Version for consistency.
func (a *App) GetDaemonStatus() DaemonStatusDTO {
	result := DaemonStatusDTO{
		State:   "stopped",
		Version: version.Version, // v4.3.1: Always show current version
	}

	// v4.3.7: Primary method - check via IPC (works without admin)
	// If daemon is running (as subprocess or service), IPC will respond
	client := ipc.NewClient()
	client.SetTimeout(2 * time.Second)

	ctx := context.Background()
	if status, err := client.GetStatus(ctx); err == nil {
		result.IPCConnected = true
		result.Running = true
		result.State = status.ServiceState
		// v4.3.1: Keep showing version.Version, not IPC version (which may be stale)
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

		// v4.3.9: Only set ManagedBy for actual Windows Service mode
		// Leave empty for subprocess mode so GUI shows control buttons (Stop/Pause/Resume)
		if svcStatus, err := service.QueryStatus(); err == nil && svcStatus == service.StatusRunning {
			result.ManagedBy = "Windows Service"
		}
		// Otherwise leave ManagedBy empty - subprocess mode allows GUI control

		return result
	}

	// v4.3.9: IPC not responding - check if Windows Service is installed/running
	// IMPORTANT: Don't set result.Running=true based on SCM alone!
	// IPC is the source of truth for subprocess mode.
	// Only show informational message if Windows Service is detected but IPC fails.
	if service.IsInstalled() {
		if svcStatus, err := service.QueryStatus(); err == nil && svcStatus == service.StatusRunning {
			// Windows Service registered as running but IPC not responding = problem state
			result.ManagedBy = "Windows Service"
			result.Error = "Windows Service running but IPC not responding - try restarting service"
			// Note: result.Running stays false - IPC is the authoritative source
		}
	}
	// For subprocess mode: if IPC fails, daemon is NOT running (default state is correct)

	return result
}

// StartDaemon starts the daemon as a subprocess (no admin required).
// v4.3.7: Uses subprocess mode by default instead of Windows Service (which requires admin).
// v4.3.8: Added startup logging with clear log path in error messages.
// v4.4.2: Uses centralized LogDirectory() for consistent log location.
// Similar to tray's startService() in tray_windows.go.
func (a *App) StartDaemon() error {
	// Check if already running via IPC
	client := ipc.NewClient()
	client.SetTimeout(2 * time.Second)
	ctx := context.Background()

	if client.IsServiceRunning(ctx) {
		return fmt.Errorf("daemon is already running")
	}

	// v4.3.8: Log startup log path for user reference
	// v4.4.2: Use centralized log directory
	logsDir := config.LogDirectory()
	a.logInfo("Daemon", fmt.Sprintf("Starting daemon subprocess (logs: %s)", logsDir))

	// Start daemon as subprocess
	if err := a.startDaemonSubprocess(); err != nil {
		a.logError("Daemon", fmt.Sprintf("Subprocess launch failed: %v", err))
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Wait for IPC to come up with progress logging
	a.logInfo("Daemon", "Waiting for daemon IPC to become available...")
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if client.IsServiceRunning(ctx) {
			a.logInfo("Daemon", "Daemon started successfully and IPC connected")
			return nil
		}
		if i == 4 {
			a.logInfo("Daemon", "Still waiting for daemon IPC (2.5s elapsed)...")
		}
	}

	// v4.3.8: Include log path in timeout error
	errMsg := fmt.Sprintf("daemon start timeout - IPC not available after 5s; check logs at: %s", logsDir)
	a.logError("Daemon", errMsg)
	return fmt.Errorf(errMsg)
}

// startDaemonSubprocess launches the daemon as a detached subprocess.
// v4.3.7: Based on tray's startService() in tray_windows.go:242-321.
// v4.3.8: Added startup logging, stderr capture, and SysProcAttr for proper detachment.
func (a *App) startDaemonSubprocess() error {
	// Find rescale-int.exe in the same directory as the GUI
	exePath, err := os.Executable()
	if err != nil {
		daemon.WriteStartupLog("ERROR: Failed to find executable path: %v", err)
		return fmt.Errorf("failed to find executable path: %w", err)
	}

	dir := filepath.Dir(exePath)
	cliPath := filepath.Join(dir, "rescale-int.exe")

	// Check if CLI exists
	if _, err := os.Stat(cliPath); os.IsNotExist(err) {
		daemon.WriteStartupLog("ERROR: CLI not found: %s", cliPath)
		return fmt.Errorf("CLI not found: %s", cliPath)
	}

	// Load settings from daemon.conf
	daemonCfg, err := config.LoadDaemonConfig("")
	if err != nil {
		a.logWarn("Daemon", fmt.Sprintf("Warning: failed to load daemon.conf: %v (using defaults)", err))
		daemonCfg = config.NewDaemonConfig()
	}

	downloadDir := daemonCfg.Daemon.DownloadFolder
	if downloadDir == "" {
		downloadDir = config.DefaultDownloadFolder()
	}

	// v4.4.3: Use shared path resolution logic for consistent behavior
	// This handles Windows junction points (e.g., Downloads -> Z:\Downloads on Rescale VMs)
	// The subprocess may not have access to the same drive mappings
	if resolved, err := pathutil.ResolveAbsolutePath(downloadDir); err == nil {
		downloadDir = resolved
	}

	pollInterval := fmt.Sprintf("%dm", daemonCfg.Daemon.PollIntervalMinutes)

	// Build command arguments
	args := []string{"daemon", "run", "--ipc",
		"--download-dir", downloadDir,
		"--poll-interval", pollInterval,
	}

	// Add filter flags if configured
	if daemonCfg.Filters.NamePrefix != "" {
		args = append(args, "--name-prefix", daemonCfg.Filters.NamePrefix)
	}
	if daemonCfg.Filters.NameContains != "" {
		args = append(args, "--name-contains", daemonCfg.Filters.NameContains)
	}
	for _, ex := range daemonCfg.GetExcludePatterns() {
		args = append(args, "--exclude", ex)
	}
	if daemonCfg.Daemon.MaxConcurrent > 0 {
		args = append(args, "--max-concurrent", fmt.Sprintf("%d", daemonCfg.Daemon.MaxConcurrent))
	}

	// v4.3.8: Log startup attempt to help diagnose launch failures
	daemon.WriteStartupLog("=== GUI STARTUP ATTEMPT ===")
	daemon.WriteStartupLog("CLI path: %s", cliPath)
	daemon.WriteStartupLog("Arguments: %v", args)

	// v4.3.8: Create stderr capture file for subprocess diagnostics
	// v4.4.2: Use centralized log directory
	logsDir := config.LogDirectory()
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		daemon.WriteStartupLog("WARNING: Could not create logs directory: %v", err)
	}
	stderrPath := filepath.Join(logsDir, "daemon-stderr.log")
	stderrFile, stderrErr := os.Create(stderrPath)
	if stderrErr != nil {
		daemon.WriteStartupLog("WARNING: Could not create stderr capture file: %v", stderrErr)
	}

	// Start daemon with IPC enabled
	cmd := exec.Command(cliPath, args...)

	// v4.3.9: Windows process flags for proper subprocess detachment + hidden console
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
	}

	// Detach stdin/stdout, but capture stderr for debugging
	cmd.Stdin = nil
	cmd.Stdout = nil
	if stderrFile != nil {
		cmd.Stderr = stderrFile
	}

	daemon.WriteStartupLog("Calling cmd.Start()...")

	if err := cmd.Start(); err != nil {
		daemon.WriteStartupLog("ERROR: cmd.Start() failed: %v", err)
		if stderrFile != nil {
			stderrFile.Close()
		}
		return fmt.Errorf("failed to start daemon process: %w", err)
	}

	pid := cmd.Process.Pid
	daemon.WriteStartupLog("SUCCESS: Started daemon subprocess with PID %d", pid)
	a.logInfo("Daemon", fmt.Sprintf("Started daemon subprocess (PID: %d)", pid))

	// Close stderr file after a delay to capture any immediate errors
	if stderrFile != nil {
		go func() {
			time.Sleep(3 * time.Second)
			stderrFile.Close()
		}()
	}

	return nil
}

// StopDaemon stops the daemon via IPC shutdown command.
// v4.3.7: Uses IPC to stop daemon (works for both subprocess and service modes, no admin required).
func (a *App) StopDaemon() error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)
	ctx := context.Background()

	// Check if daemon is running
	if !client.IsServiceRunning(ctx) {
		return nil // Already stopped
	}

	a.logInfo("Daemon", "Stopping daemon via IPC...")

	// Send shutdown command via IPC
	if err := client.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to send shutdown command: %w", err)
	}

	// Wait for daemon to stop
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if !client.IsServiceRunning(ctx) {
			a.logInfo("Daemon", "Daemon stopped successfully")
			return nil
		}
	}

	return fmt.Errorf("daemon stop timeout")
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
// v4.3.0: Simplified - mode is per-job, only AutoDownloadTag configurable.
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
	// v4.3.0: Simplified - mode is now per-job via custom field, not global
	AutoDownloadTag string `json:"autoDownloadTag"` // Tag for "Conditional" jobs

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

	// v4.3.0: Simplified - only AutoDownloadTag is configurable
	result.AutoDownloadTag = cfg.Eligibility.AutoDownloadTag

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

	// v4.3.0: Simplified - only AutoDownloadTag is configurable
	cfg.Eligibility.AutoDownloadTag = dto.AutoDownloadTag

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

// TestAutoDownloadConnection tests API connectivity and folder access for auto-download.
// v4.3.1: Moved from config_bindings.go as part of config unification.
func (a *App) TestAutoDownloadConnection(downloadFolder string) {
	go func() {
		result := struct {
			Success     bool   `json:"success"`
			Email       string `json:"email,omitempty"`
			FolderOK    bool   `json:"folderOk"`
			FolderError string `json:"folderError,omitempty"`
			Error       string `json:"error,omitempty"`
		}{}

		// Test API connection using the main config's API client
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

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
			result.Error = "No API client configured - please test connection in Setup tab first"
			runtime.EventsEmit(a.ctx, "interlink:autodownload_test_result", result)
			return
		}

		// Test folder access
		if downloadFolder != "" {
			info, err := os.Stat(downloadFolder)
			if os.IsNotExist(err) {
				if err := os.MkdirAll(downloadFolder, 0755); err != nil {
					result.FolderError = "Cannot create folder: " + err.Error()
				} else {
					result.FolderOK = true
				}
			} else if err != nil {
				result.FolderError = "Cannot access folder: " + err.Error()
			} else if !info.IsDir() {
				result.FolderError = "Path exists but is not a directory"
			} else {
				testFile := downloadFolder + "\\.interlink_test"
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

	// Get executable path and config path
	execPath, err := service.GetExecutablePath()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Config path is optional - empty string means use defaults
	if err := service.Install(execPath, ""); err != nil {
		return fmt.Errorf("failed to install service (Administrator privileges required): %w", err)
	}

	a.logInfo("Daemon", "Windows Service installed successfully")
	return nil
}

// =============================================================================
// File Logging Settings (v4.3.2)
// =============================================================================

// FileLoggingSettingsDTO represents file logging configuration.
// NOTE: This is defined in daemon_bindings.go for Unix, duplicated here for Windows build.
type FileLoggingSettingsDTO struct {
	Enabled  bool   `json:"enabled"`
	FilePath string `json:"filePath"`
}

// GetFileLoggingSettings returns the current file logging configuration.
// v4.3.2: Cross-platform file logging support.
func (a *App) GetFileLoggingSettings() FileLoggingSettingsDTO {
	return FileLoggingSettingsDTO{
		Enabled:  IsFileLoggingEnabled(),
		FilePath: GetLogFilePath(),
	}
}

// SetFileLoggingEnabled enables or disables file logging.
// v4.3.2: User can toggle file logging from GUI settings.
func (a *App) SetFileLoggingEnabled(enabled bool) error {
	if err := EnableFileLogging(enabled); err != nil {
		return fmt.Errorf("failed to set file logging: %w", err)
	}
	if enabled {
		a.logInfo("Logging", fmt.Sprintf("File logging enabled: %s", GetLogFilePath()))
	} else {
		a.logInfo("Logging", "File logging disabled")
	}
	return nil
}

// GetLogFileLocation returns the current log file path (if logging to file).
// v4.3.2: For displaying log file location in UI.
func (a *App) GetLogFileLocation() string {
	return GetLogFilePath()
}

// =============================================================================
// Daemon Log Retrieval (v4.3.2)
// =============================================================================

// DaemonLogEntryDTO represents a log entry from the daemon.
// NOTE: This is defined in daemon_bindings.go for Unix, duplicated here for Windows build.
type DaemonLogEntryDTO struct {
	Timestamp string                 `json:"timestamp"`
	Level     string                 `json:"level"`
	Stage     string                 `json:"stage"`
	Message   string                 `json:"message"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// GetDaemonLogs retrieves recent log entries from the running daemon.
// v4.3.2: Fetches logs via IPC for display in Activity tab.
func (a *App) GetDaemonLogs(count int) []DaemonLogEntryDTO {
	// Check if service is running
	status := a.GetDaemonStatus()
	if !status.Running {
		return nil
	}

	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()
	logs, err := client.GetRecentLogs(ctx, count)
	if err != nil {
		a.logWarn("Daemon", fmt.Sprintf("Failed to get daemon logs: %v", err))
		return nil
	}

	// Convert to DTO
	result := make([]DaemonLogEntryDTO, len(logs))
	for i, log := range logs {
		result[i] = DaemonLogEntryDTO{
			Timestamp: log.Timestamp,
			Level:     log.Level,
			Stage:     log.Stage,
			Message:   log.Message,
			Fields:    log.Fields,
		}
	}

	return result
}

// =============================================================================
// Logs Directory Access (v4.4.2)
// =============================================================================

// OpenLogsDirectory opens the logs folder in the system file explorer.
// v4.4.2: Added for GUI access to unified log directory.
func (a *App) OpenLogsDirectory() error {
	logsDir := config.LogDirectory()

	// Ensure directory exists
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Open in Explorer on Windows
	if err := exec.Command("explorer.exe", logsDir).Start(); err != nil {
		return fmt.Errorf("failed to open logs directory: %w", err)
	}

	return nil
}

// GetLogsDirectory returns the unified logs directory path.
// v4.4.2: For displaying log location in UI.
func (a *App) GetLogsDirectory() string {
	return config.LogDirectory()
}
