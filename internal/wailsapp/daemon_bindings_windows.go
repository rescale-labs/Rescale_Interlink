//go:build windows

// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
package wailsapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/elevation"
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

	// ServiceMode indicates if daemon is running as Windows Service (true) or subprocess (false)
	// v4.5.2: Added for GUI to detect mode via IPC when SCM is inaccessible
	ServiceMode bool `json:"serviceMode"`

	// v4.5.6: User-specific status fields
	// UserConfigured indicates if this user has daemon.conf with enabled=true
	UserConfigured bool `json:"userConfigured"`

	// UserState is the user-specific state: "not_configured", "pending", "running", "paused", "stopped"
	UserState string `json:"userState"`

	// UserRegistered indicates if service has this user registered (daemon.conf was found by service)
	UserRegistered bool `json:"userRegistered"`
}

// GetDaemonStatus returns the current daemon status.
// v4.3.7: Primary check is IPC (works for both subprocess and service modes).
// SCM queries are skipped by default since they require admin privileges.
// v4.3.1: Version always uses version.Version for consistency.
// v4.5.6: Added user-specific status fields (UserConfigured, UserState, UserRegistered).
func (a *App) GetDaemonStatus() DaemonStatusDTO {
	result := DaemonStatusDTO{
		State:     "stopped",
		UserState: "not_configured", // v4.5.6: Default user state
		Version:   version.Version,  // v4.3.1: Always show current version
	}

	// v4.5.6: Check if daemon.conf exists and is enabled for current user
	configPath, _ := config.DefaultDaemonConfigPath()
	if _, err := os.Stat(configPath); err == nil {
		// Config file exists - check if enabled
		if cfg, err := config.LoadDaemonConfig(""); err == nil {
			result.UserConfigured = cfg.Daemon.Enabled
		}
	}

	// v4.3.7: Primary method - check via IPC (works without admin)
	// If daemon is running (as subprocess or service), IPC will respond
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()
	if status, err := client.GetStatus(ctx); err == nil {
		result.IPCConnected = true
		result.Running = true
		result.State = status.ServiceState
		// v4.3.1: Keep showing version.Version, not IPC version (which may be stale)
		result.Uptime = status.Uptime
		result.ActiveDownloads = status.ActiveDownloads
		// v4.5.2: Propagate ServiceMode from IPC status
		result.ServiceMode = status.ServiceMode

		if status.LastScanTime != nil {
			result.LastScan = status.LastScanTime.Format(time.RFC3339)
		}

		// v4.5.5: Get CURRENT user's status, not first user
		// v4.5.6: Also track if user is registered with service
		currentSID := getCurrentUserSID()
		currentUsername := os.Getenv("USERNAME")
		if users, err := client.GetUserList(ctx); err == nil {
			for _, user := range users {
				if user.SID == currentSID || matchesWindowsUsername(user.Username, currentUsername) {
					result.JobsDownloaded = user.JobsDownloaded
					result.DownloadFolder = user.DownloadFolder
					result.State = user.State // v4.5.5: Use USER's state, not service state
					result.UserRegistered = true
					result.UserState = user.State // v4.5.6: User-specific state
					// v4.7.6: Propagate real error from daemon (e.g., "No API key configured")
					if user.LastError != "" {
						result.Error = user.LastError
					}
					break
				}
			}
			// Subprocess hardening: in single-user mode, if exactly 1 user returned
			// and no SID/username match was found, treat it as the current user.
			// This prevents format drift from causing permanent "Activating..." state.
			if !result.UserRegistered && !status.ServiceMode && len(users) == 1 {
				result.JobsDownloaded = users[0].JobsDownloaded
				result.DownloadFolder = users[0].DownloadFolder
				result.State = users[0].State
				result.UserRegistered = true
				result.UserState = users[0].State
			}
			// Fallback to first user if current user not found
			if result.DownloadFolder == "" && len(users) > 0 {
				result.JobsDownloaded = users[0].JobsDownloaded
				result.DownloadFolder = users[0].DownloadFolder
			}
		}

		// v4.5.6: Determine user state based on configuration + registration
		if !result.UserConfigured {
			result.UserState = "not_configured"
		} else if !result.UserRegistered {
			result.UserState = "pending" // Config exists but not yet picked up by service
		}
		// Otherwise use the state from IPC (running/paused/stopped)

		// v4.3.9: Only set ManagedBy for actual Windows Service mode
		// v4.5.2: Use IPC ServiceMode flag as primary indicator
		if status.ServiceMode {
			result.ManagedBy = "Windows Service"
		}
		// Otherwise leave ManagedBy empty - subprocess mode allows GUI control

		return result
	}

	// v4.5.0: Better status when service running but IPC fails
	// Check Windows Service status for better error messaging
	if service.IsInstalled() {
		if svcStatus, err := service.QueryStatus(); err == nil {
			if svcStatus == service.StatusRunning {
				// Windows Service is running but IPC not responding
				result.ManagedBy = "Windows Service"
				result.Running = true       // Service IS running
				result.State = "running"
				result.IPCConnected = false // v4.5.0: Explicit IPC status
				result.Error = "Service running but IPC not responding - may be initializing"
				// v4.5.6: User state when service is running but IPC fails
				if result.UserConfigured {
					result.UserState = "pending" // Config exists, service running, but can't confirm registration
				}
			} else if svcStatus == service.StatusStopped {
				// Windows Service installed but stopped
				result.ManagedBy = "Windows Service"
				result.Running = false
				result.State = "stopped"
				result.Error = "Windows Service installed but stopped. Start via Services.msc."
			}
		}
	}
	// For subprocess mode: if IPC fails, daemon is NOT running (default state is correct)

	return result
}

// StartDaemon starts the daemon as a subprocess (no admin required).
// v4.3.7: Uses subprocess mode by default instead of Windows Service (which requires admin).
// v4.3.8: Added startup logging with clear log path in error messages.
// v4.4.2: Uses centralized LogDirectory() for consistent log location.
// v4.5.0: Blocks subprocess spawn if Windows Service is installed.
// v4.5.5: Uses unified detection instead of raw IsInstalled() - only blocks when running.
// Similar to tray's startService() in tray_windows.go.
func (a *App) StartDaemon() error {
	// v4.7.6: Ensure token file exists before daemon starts (Phase 0b)
	if err := a.ensureTokenPersisted(); err != nil {
		a.logWarn("Daemon", fmt.Sprintf("Token persistence warning: %v", err))
	}

	// v4.7.6: Pre-check API key availability (Phase 3a)
	apiKey := config.ResolveAPIKeyForCurrentUser("")
	if apiKey == "" {
		return fmt.Errorf("cannot start daemon: no API key configured. Set your API key in Connection settings and test the connection first")
	}

	// v4.5.5: Use unified detection instead of raw IsInstalled()
	if blocked, reason := service.ShouldBlockSubprocess(); blocked {
		return errors.New(reason)
	}

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

	// v4.7.6: Read daemon-stderr for actual error message instead of generic timeout
	stderrPath := filepath.Join(logsDir, "daemon-stderr.log")
	errDetail := ""
	if stderrData, readErr := os.ReadFile(stderrPath); readErr == nil && len(stderrData) > 0 {
		lines := strings.Split(strings.TrimSpace(string(stderrData)), "\n")
		// Take last 3 non-empty lines
		var lastLines []string
		for i := len(lines) - 1; i >= 0 && len(lastLines) < 3; i-- {
			line := strings.TrimSpace(lines[i])
			if line != "" {
				lastLines = append([]string{line}, lastLines...)
			}
		}
		if len(lastLines) > 0 {
			errDetail = "; stderr: " + strings.Join(lastLines, " | ")
		}
	}

	errMsg := fmt.Sprintf("daemon start timeout - IPC not available after 5s%s; check logs at: %s", errDetail, logsDir)
	a.logError("Daemon", errMsg)
	return errors.New(errMsg)
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

	// v4.5.8: Add persistent log file for daemon subprocess
	daemonLogPath := filepath.Join(config.LogDirectory(), "daemon.log")

	// Build command arguments
	args := []string{"daemon", "run", "--ipc",
		"--download-dir", downloadDir,
		"--poll-interval", pollInterval,
		"--log-file", daemonLogPath, // v4.5.8: persistent daemon logging
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
	// v4.5.1: Uses 0700 permissions to restrict log access to owner only
	logsDir := config.LogDirectory()
	if err := os.MkdirAll(logsDir, 0700); err != nil {
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
// v4.5.5: Uses "current" user ID to route to current user in service mode.
func (a *App) TriggerDaemonScan() error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if !client.IsServiceRunning(ctx) {
		return fmt.Errorf("daemon is not running or IPC not available")
	}

	// v4.5.5: Use "current" to route to current user (server infers from caller SID)
	if err := client.TriggerScan(ctx, "current"); err != nil {
		return fmt.Errorf("failed to trigger scan: %w", err)
	}

	a.logInfo("Daemon", "Scan triggered")
	return nil
}

// PauseDaemon pauses the daemon's auto-download polling via IPC.
// v4.5.5: Uses "current" user ID to route to current user in service mode.
func (a *App) PauseDaemon() error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if !client.IsServiceRunning(ctx) {
		return fmt.Errorf("daemon is not running or IPC not available")
	}

	// v4.5.5: Use "current" to route to current user (server infers from caller SID)
	if err := client.PauseUser(ctx, "current"); err != nil {
		return fmt.Errorf("failed to pause daemon: %w", err)
	}

	a.logInfo("Daemon", "Daemon paused")
	return nil
}

// ResumeDaemon resumes the daemon's auto-download polling via IPC.
// v4.5.5: Uses "current" user ID to route to current user in service mode.
func (a *App) ResumeDaemon() error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if !client.IsServiceRunning(ctx) {
		return fmt.Errorf("daemon is not running or IPC not available")
	}

	// v4.5.5: Use "current" to route to current user (server infers from caller SID)
	if err := client.ResumeUser(ctx, "current"); err != nil {
		return fmt.Errorf("failed to resume daemon: %w", err)
	}

	a.logInfo("Daemon", "Daemon resumed")
	return nil
}

// TriggerProfileRescan asks the daemon to re-enumerate user profiles.
// v4.5.6: Called after saving daemon.conf so service picks up new users.
// Uses existing TriggerScan("all") path - no new IPC message type needed.
func (a *App) TriggerProfileRescan() error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if !client.IsServiceRunning(ctx) {
		return fmt.Errorf("daemon is not running or IPC not available")
	}

	// v4.5.6: Use "all" to trigger profile rescan (existing behavior)
	if err := client.TriggerScan(ctx, "all"); err != nil {
		return fmt.Errorf("failed to trigger profile rescan: %w", err)
	}

	a.logInfo("Daemon", "Profile rescan triggered")
	return nil
}

// ReloadConfigResultDTO represents the result of a config reload request from the frontend.
// v4.7.6: Used for GUI to know if config was applied or deferred.
type ReloadConfigResultDTO struct {
	Applied         bool   `json:"applied"`
	Deferred        bool   `json:"deferred"`
	ActiveDownloads int    `json:"activeDownloads"`
	Error           string `json:"error,omitempty"`
}

// ReloadDaemonConfig notifies the running daemon to reload its configuration.
// v4.7.6: Called after SaveDaemonConfig to propagate changes.
func (a *App) ReloadDaemonConfig() ReloadConfigResultDTO {
	result := ReloadConfigResultDTO{}

	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)
	ctx := context.Background()

	if !client.IsServiceRunning(ctx) {
		result.Error = "daemon not running"
		return result
	}

	data, err := client.ReloadConfig(ctx)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	if data.Applied {
		// Check if service mode — in service mode, TriggerRescan handles everything
		status, statusErr := client.GetStatus(ctx)
		if statusErr == nil && status.ServiceMode {
			// Service mode: TriggerRescan already applied
			result.Applied = true
			a.logInfo("Daemon", "Config reload applied via service rescan")
			return result
		}

		// Subprocess mode: stop and restart for config to take effect
		a.logInfo("Daemon", "Config reload accepted — restarting daemon for new config")
		if err := a.StopDaemon(); err != nil {
			result.Error = fmt.Sprintf("failed to stop daemon for restart: %v", err)
			return result
		}
		time.Sleep(500 * time.Millisecond)
		if err := a.StartDaemon(); err != nil {
			result.Error = fmt.Sprintf("daemon stopped but failed to restart: %v", err)
			return result
		}
		result.Applied = true
	} else if data.Deferred {
		result.Deferred = true
		result.ActiveDownloads = data.ActiveDownloads
	}

	return result
}

// PreFlightResultDTO represents the result of auto-download pre-flight checks.
// v4.7.6: Used by GUI before enabling auto-download.
type PreFlightResultDTO struct {
	APIKeyOK    bool   `json:"apiKeyOk"`
	FolderOK    bool   `json:"folderOk"`
	APIKeyError string `json:"apiKeyError,omitempty"`
	FolderError string `json:"folderError,omitempty"`
}

// ValidateAutoDownloadPreFlight checks prerequisites before enabling auto-download.
// v4.7.6: Only checks API key and folder (not service/IPC — user may configure first).
func (a *App) ValidateAutoDownloadPreFlight(downloadFolder string) PreFlightResultDTO {
	result := PreFlightResultDTO{}

	// Check API key
	apiKey := config.ResolveAPIKeyForCurrentUser("")
	if apiKey != "" {
		result.APIKeyOK = true
	} else {
		result.APIKeyError = "No API key configured. Set your API key in Connection settings and test the connection first."
	}

	// Check download folder
	if downloadFolder == "" {
		downloadFolder = config.DefaultDownloadFolder()
	}
	if downloadFolder != "" {
		if info, err := os.Stat(downloadFolder); err == nil && info.IsDir() {
			result.FolderOK = true
		} else if os.IsNotExist(err) {
			result.FolderOK = true
		} else if err != nil {
			result.FolderError = fmt.Sprintf("Cannot access folder: %v", err)
		} else {
			result.FolderError = "Path exists but is not a directory"
		}
	}

	return result
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
		// v4.5.8: Resolve junction points before testing folder access
		if downloadFolder != "" {
			if resolvedFolder, resolveErr := pathutil.ResolveAbsolutePath(downloadFolder); resolveErr == nil && resolvedFolder != "" {
				downloadFolder = resolvedFolder
			}
			info, err := os.Stat(downloadFolder)
			if os.IsNotExist(err) {
				if err := os.MkdirAll(downloadFolder, 0755); err != nil {
					result.FolderError = "Cannot create folder: " + err.Error()
				} else {
					result.FolderOK = true
				}
			} else if err != nil {
				// v4.5.8: Check if this is a mount-point / reparse-point error
				errStr := err.Error()
				if strings.Contains(errStr, "mount point") || strings.Contains(errStr, "reparse point") ||
					strings.Contains(errStr, "untrusted") {
					result.FolderError = fmt.Sprintf("Cannot access folder (may be a junction to an inaccessible drive): %v", err)
				} else {
					result.FolderError = "Cannot access folder: " + err.Error()
				}
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

// InstallService attempts to install the Windows Service (non-elevated, legacy).
// Deprecated: Use InstallServiceElevated() which triggers UAC for reliable installation.
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

// InstallServiceElevated triggers UAC prompt to install Windows Service.
// v4.5.8: Replaces non-elevated InstallService() as the primary install path.
// The elevated CLI process handles SCM registration and sets HKLM registry marker.
func (a *App) InstallServiceElevated() ElevatedServiceResultDTO {
	a.logInfo("Service", "Installing Windows Service with UAC elevation...")

	if err := elevation.InstallServiceElevated(); err != nil {
		a.logError("Service", fmt.Sprintf("UAC elevation failed: %v", err))
		return ElevatedServiceResultDTO{
			Success: false,
			Error:   fmt.Sprintf("Failed to install service: %v", err),
		}
	}

	a.logInfo("Service", "UAC approved, service install command executed")
	return ElevatedServiceResultDTO{Success: true}
}

// UninstallServiceElevated triggers UAC prompt to uninstall Windows Service.
// v4.5.8: Added for GUI to cleanly remove the service before MSI uninstall.
// The elevated CLI process handles SCM removal and clears HKLM registry marker.
func (a *App) UninstallServiceElevated() ElevatedServiceResultDTO {
	a.logInfo("Service", "Uninstalling Windows Service with UAC elevation...")

	if err := elevation.UninstallServiceElevated(); err != nil {
		a.logError("Service", fmt.Sprintf("UAC elevation failed: %v", err))
		return ElevatedServiceResultDTO{
			Success: false,
			Error:   fmt.Sprintf("Failed to uninstall service: %v", err),
		}
	}

	a.logInfo("Service", "UAC approved, service uninstall command executed")
	return ElevatedServiceResultDTO{Success: true}
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
// v4.5.1: Uses 0700 permissions to restrict log access to owner only.
func (a *App) OpenLogsDirectory() error {
	logsDir := config.LogDirectory()

	// Ensure directory exists
	if err := os.MkdirAll(logsDir, 0700); err != nil {
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

// =============================================================================
// UAC-Elevated Service Control (v4.5.1)
// =============================================================================

// ElevatedServiceResultDTO represents the result of an elevated service operation.
// v4.5.1: Used for GUI to communicate UAC elevation results.
type ElevatedServiceResultDTO struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ServiceStatusDTO represents detailed Windows Service status.
// v4.5.1: Used for GUI to show service status independently of IPC.
// v4.5.2: Added SCMBlocked/SCMError for IPC fallback when SCM access is denied.
type ServiceStatusDTO struct {
	Installed  bool   `json:"installed"`
	Running    bool   `json:"running"`
	Status     string `json:"status"`     // "Stopped", "Running", "Start Pending", etc.
	SCMBlocked bool   `json:"scmBlocked"` // v4.5.2: True if SCM access denied
	SCMError   string `json:"scmError"`   // v4.5.2: Error message for debugging
}

// GetServiceStatus returns detailed Windows Service status.
// v4.5.1: Always derives Installed explicitly from service.IsInstalled().
// v4.5.2: Falls back to IPC ServiceMode when SCM access is blocked.
// NOTE: Do NOT infer installed from QueryStatus() because it returns "Stopped"
// even when the service is not installed.
func (a *App) GetServiceStatus() ServiceStatusDTO {
	installed, scmError := service.IsInstalledWithReason()

	if !installed && scmError != "" {
		// SCM blocked - check if IPC says we're in service mode
		client := ipc.NewClient()
		client.SetTimeout(2 * time.Second)
		ctx := context.Background()
		if status, err := client.GetStatus(ctx); err == nil {
			// Use ServiceMode (added in v4.5.2) to detect Windows Service
			if status.ServiceMode {
				return ServiceStatusDTO{
					Installed:  true,  // Inferred from IPC ServiceMode flag
					Running:    status.ServiceState == "running",
					Status:     "Running (via IPC)",
					SCMBlocked: true,
					SCMError:   scmError,
				}
			}
		}
		// Neither SCM nor IPC worked (or IPC is subprocess mode)
		return ServiceStatusDTO{
			Installed:  false,
			Running:    false,
			Status:     "Unknown",
			SCMBlocked: true,
			SCMError:   scmError,
		}
	}

	if !installed {
		return ServiceStatusDTO{
			Installed: false,
			Running:   false,
			Status:    "Not Installed",
		}
	}

	status, err := service.QueryStatus()
	if err != nil {
		return ServiceStatusDTO{
			Installed: true,
			Running:   false,
			Status:    "Unknown",
		}
	}

	return ServiceStatusDTO{
		Installed: true,
		Running:   status == service.StatusRunning,
		Status:    status.String(),
	}
}

// StartServiceElevated triggers UAC prompt to start Windows Service.
// v4.5.1: Returns immediately after UAC approved (poll GetServiceStatus to confirm).
// v4.5.8: Removed IsInstalled() pre-check that blocked elevation on restricted VMs
// where SCM access is denied. The elevated process reports errors properly.
func (a *App) StartServiceElevated() ElevatedServiceResultDTO {
	// Don't gate on IsInstalled() - SCM may be inaccessible from non-admin context.
	// The elevated "rescale-int service start" will report errors properly.
	a.logInfo("Service", "Starting Windows Service with UAC elevation...")

	// Trigger UAC elevation
	if err := elevation.StartServiceElevated(); err != nil {
		a.logError("Service", fmt.Sprintf("UAC elevation failed: %v", err))
		return ElevatedServiceResultDTO{
			Success: false,
			Error:   fmt.Sprintf("Failed to start service: %v", err),
		}
	}

	a.logInfo("Service", "UAC approved, service start command executed")
	return ElevatedServiceResultDTO{Success: true}
}

// StopServiceElevated triggers UAC prompt to stop Windows Service.
// v4.5.1: Returns immediately after UAC approved (poll GetServiceStatus to confirm).
// v4.5.8: Removed IsInstalled() pre-check that blocked elevation on restricted VMs
// where SCM access is denied. The elevated process reports errors properly.
func (a *App) StopServiceElevated() ElevatedServiceResultDTO {
	// Don't gate on IsInstalled() - SCM may be inaccessible from non-admin context.
	// The elevated "rescale-int service stop" will report errors properly.
	a.logInfo("Service", "Stopping Windows Service with UAC elevation...")

	// Trigger UAC elevation
	if err := elevation.StopServiceElevated(); err != nil {
		a.logError("Service", fmt.Sprintf("UAC elevation failed: %v", err))
		return ElevatedServiceResultDTO{
			Success: false,
			Error:   fmt.Sprintf("Failed to stop service: %v", err),
		}
	}

	a.logInfo("Service", "UAC approved, service stop command executed")
	return ElevatedServiceResultDTO{Success: true}
}

// InstallAndStartServiceElevated triggers UAC prompt to install + start Windows Service.
// v4.7.6: Combined operation — single UAC prompt for both install and start.
func (a *App) InstallAndStartServiceElevated() ElevatedServiceResultDTO {
	a.logInfo("Service", "Installing and starting Windows Service with UAC elevation...")

	if err := elevation.InstallAndStartServiceElevated(); err != nil {
		a.logError("Service", fmt.Sprintf("UAC elevation failed: %v", err))
		return ElevatedServiceResultDTO{
			Success: false,
			Error:   fmt.Sprintf("Failed to install and start service: %v", err),
		}
	}

	a.logInfo("Service", "UAC approved, install-and-start command executed")
	return ElevatedServiceResultDTO{Success: true}
}

// matchesWindowsUsername compares usernames handling Windows format differences.
// user.Current().Username returns "DOMAIN\user", os.Getenv("USERNAME") returns "user".
// Also handles user@domain (UPN) format for domain-joined machines.
func matchesWindowsUsername(ipcUsername, guiUsername string) bool {
	if strings.EqualFold(ipcUsername, guiUsername) {
		return true
	}
	// Handle DOMAIN\user format
	if parts := strings.SplitN(ipcUsername, `\`, 2); len(parts) == 2 {
		if strings.EqualFold(parts[1], guiUsername) {
			return true
		}
	}
	// Handle user@domain (UPN) format
	if parts := strings.SplitN(ipcUsername, "@", 2); len(parts) == 2 {
		if strings.EqualFold(parts[0], guiUsername) {
			return true
		}
	}
	return false
}

// getCurrentUserSID returns the SID of the current process owner.
// v4.5.5: Used for per-user status routing in service mode.
func getCurrentUserSID() string {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return ""
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return ""
	}
	return user.User.Sid.String()
}
