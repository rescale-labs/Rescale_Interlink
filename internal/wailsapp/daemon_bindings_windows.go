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

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/elevation"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/pathutil"
	"github.com/rescale/rescale-int/internal/service"
	"github.com/rescale/rescale-int/internal/version"
)

// Windows process creation flag to hide console window.
// Required for subprocess mode to not show a blank console.
const createNoWindow = 0x08000000

// GetDaemonStatus returns the current daemon status, derived from the
// shared service.Computer (see internal/service/state.go). Same
// implementation as the non-Windows variant; the SCM detection that used
// to live here now happens inside service.Compute.
func (a *App) GetDaemonStatus() DaemonStatusDTO {
	comp := a.ensureStateComputer()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	a.stateMu.Lock()
	prior := a.priorState
	a.stateMu.Unlock()

	st := comp.Compute(ctx, prior)

	a.stateMu.Lock()
	a.priorState = st
	a.stateMu.Unlock()

	pres := st.Presentation()
	pid := daemon.IsDaemonRunning()

	configured := st.PerUser != service.PerUserNotConfigured

	userState := "not_configured"
	switch st.PerUser {
	case service.PerUserPending:
		userState = "pending"
	case service.PerUserRunning:
		userState = "running"
	case service.PerUserPaused:
		userState = "paused"
	case service.PerUserError:
		userState = "error"
	}

	legacyState := "stopped"
	switch st.PerUser {
	case service.PerUserRunning:
		legacyState = "running"
	case service.PerUserPaused:
		legacyState = "paused"
	case service.PerUserError:
		legacyState = "error"
	case service.PerUserPending:
		legacyState = "pending"
	}

	managedBy := ""
	if st.ServiceMode {
		managedBy = "Windows Service"
	}

	lastScan := ""
	if st.LastScanTime != nil && !st.LastScanTime.IsZero() {
		lastScan = st.LastScanTime.Format(time.RFC3339)
	}

	lastErrorTime := ""
	if st.LastErrorTime != nil && !st.LastErrorTime.IsZero() {
		lastErrorTime = st.LastErrorTime.Format(time.RFC3339)
	}

	return DaemonStatusDTO{
		Running:         st.IPCConnected || pid != 0,
		PID:             pid,
		IPCConnected:    st.IPCConnected,
		State:           legacyState,
		Version:         version.Version,
		Uptime:          st.Uptime,
		LastScan:        lastScan,
		ActiveDownloads: st.ActiveDownloads,
		JobsDownloaded:  st.JobsDownloaded,
		DownloadFolder:  st.DownloadFolder,
		Error:           st.LastError,
		ErrorCode:       string(st.LastErrorCode),
		LastErrorTime:   lastErrorTime,
		ManagedBy:       managedBy,
		ServiceMode:     st.ServiceMode,
		UserConfigured:  configured,
		UserState:       userState,
		UserStateDetail: pres.GUILongForm,
		UserRegistered:  st.PerUser == service.PerUserRunning || st.PerUser == service.PerUserPaused,
	}
}

// StartDaemon starts the daemon as a subprocess (no admin required).
// It runs in the user's own session. Blocks the spawn while a service from an
// earlier version is running.
func (a *App) StartDaemon() error {
	// Persist config + token before handing off to a different-identity process.
	if err := a.ensureAllConfigPersisted(); err != nil {
		return fmt.Errorf("cannot start daemon: %w", err)
	}

	// Pre-check API key availability
	apiKey := config.ResolveAPIKeyForCurrentUser("")
	if apiKey == "" {
		return fmt.Errorf("cannot start daemon: no API key configured. Set your API key in API Configuration and test the connection first")
	}

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

	// Read daemon-stderr for actual error message instead of generic timeout
	stderrPath := filepath.Join(logsDir, config.DaemonStderrLogName)
	errDetail := ""
	if stderrData, readErr := os.ReadFile(stderrPath); readErr == nil {
		if why := childStderr(string(stderrData)); why != "" {
			errDetail = "; stderr: " + why
		}
	}

	errMsg := fmt.Sprintf("daemon start timeout - IPC not available after 5s%s; check logs at: %s", errDetail, logsDir)
	a.logError("Daemon", errMsg)
	return errors.New(errMsg)
}

// childStderr is why the daemon's captured stderr says it stopped: its last
// "Error:" line, which Cobra prints before the usage text that would fill the
// tail, or else its last 3 non-empty lines.
func childStderr(stderr string) string {
	lines := strings.Split(stderr, "\n")
	var tail []string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "Error:") {
			return line
		}
		if line != "" && len(tail) < 3 {
			tail = append([]string{line}, tail...)
		}
	}
	return strings.Join(tail, " | ")
}

// startDaemonSubprocess launches the daemon as a detached subprocess.
func (a *App) startDaemonSubprocess() error {
	// Load settings from daemon.conf
	daemonCfg, err := config.LoadDaemonConfig("")
	if err != nil {
		a.logWarn("Daemon", fmt.Sprintf("Warning: failed to load daemon.conf: %v (using defaults)", err))
		daemonCfg = config.NewDaemonConfig()
	}
	// The detached daemon would refuse them where no one sees why.
	if err := config.CheckDownloadFolder(daemonCfg.Daemon.DownloadFolder); err != nil {
		return err
	}
	if err := daemon.CheckMaxConcurrent(daemonCfg.Daemon.MaxConcurrent, "max_concurrent in daemon.conf"); err != nil {
		return err
	}

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

	downloadDir := daemonCfg.Daemon.DownloadFolder
	if downloadDir == "" {
		downloadDir = config.DefaultDownloadFolder()
	}

	// Resolve junction points (e.g., Downloads -> Z:\Downloads on Rescale VMs).
	// The subprocess may not have access to the same drive mappings.
	if resolved, err := pathutil.ResolveAbsolutePath(downloadDir); err == nil {
		downloadDir = resolved
	}

	pollInterval := fmt.Sprintf("%dm", daemonCfg.Daemon.PollIntervalMinutes)

	daemonLogPath := filepath.Join(config.LogDirectory(), config.DaemonLogName)

	// Build command arguments
	args := []string{"daemon", "run", "--ipc",
		"--download-dir", downloadDir,
		"--poll-interval", pollInterval,
		"--log-file", daemonLogPath,
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

	daemon.WriteStartupLog("=== GUI STARTUP ATTEMPT ===")
	daemon.WriteStartupLog("CLI path: %s", cliPath)
	daemon.WriteStartupLog("Arguments: %v", args)

	// Create stderr capture file for subprocess diagnostics (0700 to restrict access)
	logsDir := config.LogDirectory()
	if err := os.MkdirAll(logsDir, 0700); err != nil {
		daemon.WriteStartupLog("WARNING: Could not create logs directory: %v", err)
	}
	stderrPath := filepath.Join(logsDir, config.DaemonStderrLogName)
	stderrFile, stderrErr := os.Create(stderrPath)
	if stderrErr != nil {
		daemon.WriteStartupLog("WARNING: Could not create stderr capture file: %v", stderrErr)
	}

	// Start daemon with IPC enabled
	cmd := exec.Command(cliPath, args...)

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
// Uses IPC (works for both subprocess and service modes, no admin required).
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

	// "current" routes to calling user (server infers from caller SID)
	if err := client.TriggerScan(ctx, "current"); err != nil {
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

	// "current" routes to calling user (server infers from caller SID)
	if err := client.PauseUser(ctx, "current"); err != nil {
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

	// "current" routes to calling user (server infers from caller SID)
	if err := client.ResumeUser(ctx, "current"); err != nil {
		return fmt.Errorf("failed to resume daemon: %w", err)
	}

	a.logInfo("Daemon", "Daemon resumed")
	return nil
}

// TriggerProfileRescan asks the user's running daemon to poll for completed
// jobs now. The daemon serves only this user and ignores the scope; the name
// and the "all" scope are kept for the GUI binding and for a service from an
// earlier version, which read "all" as every profile.
func (a *App) TriggerProfileRescan() error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if !client.IsServiceRunning(ctx) {
		return fmt.Errorf("daemon is not running or IPC not available")
	}

	if err := client.TriggerScan(ctx, "all"); err != nil {
		return fmt.Errorf("failed to trigger a poll: %w", err)
	}

	a.logInfo("Daemon", "Poll for completed jobs triggered")
	return nil
}

// ReloadDaemonConfig notifies the running daemon to reload its configuration.
func (a *App) ReloadDaemonConfig() ReloadConfigResultDTO {
	result := ReloadConfigResultDTO{}

	if err := a.ensureAllConfigPersisted(); err != nil {
		result.Error = err.Error()
		return result
	}

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
		// A service from an earlier version applies the reload itself.
		status, statusErr := client.GetStatus(ctx)
		if statusErr == nil && status.ServiceMode {
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

// DaemonConfigDTO represents the daemon configuration for the frontend.
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

	// Eligibility — mode is per-job via custom field, not global
	AutoDownloadTag string `json:"autoDownloadTag"` // Tag for "Conditional" jobs

	// Notifications
	NotificationsEnabled bool `json:"notificationsEnabled"`
	ShowDownloadComplete bool `json:"showDownloadComplete"`
	ShowDownloadFailed   bool `json:"showDownloadFailed"`

	// Config file path (read-only)
	ConfigPath string `json:"configPath"`
}

// SaveDaemonConfig saves daemon configuration to daemon.conf.
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

	cfg.Eligibility.AutoDownloadTag = dto.AutoDownloadTag

	cfg.Notifications.Enabled = dto.NotificationsEnabled
	cfg.Notifications.ShowDownloadComplete = dto.ShowDownloadComplete
	cfg.Notifications.ShowDownloadFailed = dto.ShowDownloadFailed

	// Validate before saving, and before the probe below creates the folder.
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	// Refuse a folder the user's own daemon could not create or write.
	if result := pathutil.ValidateWritablePath(dto.DownloadFolder, pathutil.ConsumerCurrentUser); !result.Reachable {
		return fmt.Errorf("%s: %s",
			ipc.CanonicalText[result.ErrorCode], result.Reason)
	}

	if err := a.ensureAllConfigPersisted(); err != nil {
		return err
	}

	if err := config.SaveDaemonConfig(cfg, ""); err != nil {
		return fmt.Errorf("failed to save daemon.conf: %w", err)
	}

	a.logInfo("Daemon", "Configuration saved to daemon.conf")

	if dto.Enabled {
		client := ipc.NewClient()
		client.SetTimeout(3 * time.Second)
		ctx := context.Background()
		if client.IsServiceRunning(ctx) {
			if err := a.TriggerProfileRescan(); err != nil {
				a.logWarn("Daemon", fmt.Sprintf("Poll after save failed (non-fatal): %v", err))
			}
		}
	}
	return nil
}

// TestAutoDownloadConnection tests API connectivity and folder access for auto-download.
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
				// Check if this is a mount-point / reparse-point error
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

// =============================================================================
// File Logging Settings
// =============================================================================

// =============================================================================
// Daemon Transfer Visibility (Plan 3: Windows variant)
// =============================================================================
//
// The DTO types (DaemonTransferTaskDTO, DaemonBatchStatsDTO, DaemonTransferSnapshotDTO)
// and the daemonReachable helper are defined in daemon_bindings.go with a
// !windows tag and are duplicated here for the Windows build (mutually
// exclusive files). The core IPC-to-DTO marshalling logic matches across
// both platforms.

// GetDaemonTransferSnapshot retrieves a point-in-time view of daemon
// transfers via IPC. Works in both subprocess and service modes.
func (a *App) GetDaemonTransferSnapshot() *DaemonTransferSnapshotDTO {
	client := ipc.NewClient()
	client.SetTimeout(3 * time.Second)
	ctx := context.Background()

	if !a.daemonReachable(ctx, client) {
		return &DaemonTransferSnapshotDTO{}
	}

	data, err := client.GetTransferStatus(ctx)
	if err != nil {
		a.logWarn("daemon", "GetTransferStatus failed: "+err.Error())
		return &DaemonTransferSnapshotDTO{}
	}
	if data == nil {
		return &DaemonTransferSnapshotDTO{}
	}

	out := &DaemonTransferSnapshotDTO{
		Tasks:   make([]DaemonTransferTaskDTO, 0, len(data.Tasks)),
		Batches: make([]DaemonBatchStatsDTO, 0, len(data.Batches)),
	}
	for _, t := range data.Tasks {
		out.Tasks = append(out.Tasks, DaemonTransferTaskDTO{
			ID: t.ID, Type: t.Type, State: t.State, Name: t.Name,
			Source: t.Source, Dest: t.Dest, Size: t.Size,
			Progress: t.Progress, Speed: t.Speed, Error: t.Error,
			SourceLabel: t.SourceLabel, BatchID: t.BatchID, BatchLabel: t.BatchLabel,
			CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, CompletedAt: t.CompletedAt,
		})
	}
	for _, b := range data.Batches {
		out.Batches = append(out.Batches, DaemonBatchStatsDTO{
			BatchID: b.BatchID, BatchLabel: b.BatchLabel, Direction: b.Direction,
			SourceLabel: b.SourceLabel,
			Total:       b.Total, Queued: b.Queued, Active: b.Active,
			Completed: b.Completed, Failed: b.Failed, Cancelled: b.Cancelled,
			TotalBytes: b.TotalBytes, Progress: b.Progress, Speed: b.Speed,
			TotalKnown: b.TotalKnown, StartedAt: b.StartedAt,
		})
	}
	return out
}

// =============================================================================
// Daemon Log Retrieval
// =============================================================================

// GetDaemonLogs retrieves recent log entries from the running daemon via IPC.
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
// Logs Directory Access
// =============================================================================

// OpenLogsDirectory opens the logs folder in the system file explorer.
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

// =============================================================================
// UAC-Elevated Service Control
// =============================================================================

// ServiceStatusDTO represents detailed Windows Service status.
type ServiceStatusDTO struct {
	Installed  bool   `json:"installed"`
	Running    bool   `json:"running"`
	Status     string `json:"status"`     // "Stopped", "Running", "Start Pending", etc.
	SCMBlocked bool   `json:"scmBlocked"` // True if SCM access denied
	SCMError   string `json:"scmError"`   // Error message for debugging
}

// GetServiceStatus returns detailed Windows Service status.
// Falls back to IPC ServiceMode when SCM access is blocked.
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
			// Use ServiceMode flag to detect Windows Service
			if status.ServiceMode {
				return ServiceStatusDTO{
					Installed:  true, // Inferred from IPC ServiceMode flag
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

// UninstallServiceElevated triggers a UAC prompt to remove a Windows Service
// installed by an earlier version. Returns once the command has run; poll
// GetServiceStatus to confirm.
func (a *App) UninstallServiceElevated() ElevatedServiceResultDTO {
	a.logInfo("Service", "Removing Windows Service with UAC elevation...")

	if err := elevation.UninstallServiceElevated(); err != nil {
		a.logError("Service", fmt.Sprintf("UAC elevation failed: %v", err))
		return ElevatedServiceResultDTO{
			Success: false,
			Error:   fmt.Sprintf("Failed to remove service: %v", err),
		}
	}

	a.logInfo("Service", "UAC approved, service uninstall command executed")
	return ElevatedServiceResultDTO{Success: true}
}
