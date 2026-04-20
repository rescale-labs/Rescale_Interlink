//go:build !windows

// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
package wailsapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/pathutil"
	"github.com/rescale/rescale-int/internal/service"
	"github.com/rescale/rescale-int/internal/version"
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

	// Error carries the canonical English error text when an error state
	// is active. Empty when no error.
	Error string `json:"error,omitempty"`

	// ErrorCode is the stable machine-readable ipc.ErrorCode corresponding
	// to Error. Frontend compares on this, not on Error text.
	ErrorCode string `json:"errorCode,omitempty"`

	// ManagedBy indicates if daemon is managed externally ("Windows Service", "", etc.)
	ManagedBy string `json:"managedBy,omitempty"`

	// ServiceMode indicates if daemon is running as Windows Service (true) or subprocess (false)
	ServiceMode bool `json:"serviceMode"`

	// UserConfigured indicates if this user has daemon.conf with enabled=true
	UserConfigured bool `json:"userConfigured"`

	// UserState is the user-specific state: "not_configured", "pending", "running", "paused", "stopped", "error"
	UserState string `json:"userState"`

	// UserStateDetail is the canonical long-form presentation string for this
	// user's state, suitable for rendering verbatim in the GUI. Same across
	// every surface via service.Presentation.
	UserStateDetail string `json:"userStateDetail,omitempty"`

	// UserRegistered indicates if service has this user registered (daemon.conf was found by service)
	UserRegistered bool `json:"userRegistered"`
}

// GetDaemonStatus returns the current daemon status, derived from the
// shared service.Computer (see internal/service/state.go). This is the
// primary method for the frontend to check daemon state.
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

	// Legacy State field: keep "running"/"paused"/"stopped"/"unknown" vocab
	// for any frontend code still reading it during Plan 1 rollout.
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
		ManagedBy:       managedBy,
		ServiceMode:     st.ServiceMode,
		UserConfigured:  configured,
		UserState:       userState,
		UserStateDetail: pres.GUILongForm,
		UserRegistered:  st.PerUser == service.PerUserRunning || st.PerUser == service.PerUserPaused,
	}
}

// StartDaemon starts the daemon process in background mode with IPC enabled.
// This spawns a new process that survives the GUI closing.
func (a *App) StartDaemon() error {
	// Check if already running
	if pid := daemon.IsDaemonRunning(); pid != 0 {
		return fmt.Errorf("daemon is already running (PID %d)", pid)
	}

	// Ensure config.csv and token file are on disk before the subprocess
	// reads them. Per AUTO_DOWNLOAD_SPEC.md §4.3.
	if err := a.ensureAllConfigPersisted(); err != nil {
		return fmt.Errorf("cannot start daemon: %w", err)
	}

	// Pre-check API key availability before launching daemon
	apiKey := config.ResolveAPIKeyForCurrentUser("")
	if apiKey == "" {
		return fmt.Errorf("cannot start daemon: no API key configured. Set your API key in Connection settings and test the connection first")
	}

	// Get current executable path
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Load daemon config — the daemon run command also reads this file, but we
	// pass explicit flags so they're visible in the logs
	daemonCfg, err := config.LoadDaemonConfig("")
	if err != nil {
		a.logWarn("Daemon", fmt.Sprintf("Failed to load daemon.conf, using defaults: %v", err))
		daemonCfg = config.NewDaemonConfig()
	}

	downloadDir := daemonCfg.Daemon.DownloadFolder
	if downloadDir == "" {
		downloadDir = config.DefaultDownloadFolder()
	}

	// Resolve symlinks to physical paths for consistency
	if resolved, err := filepath.EvalSymlinks(downloadDir); err == nil {
		downloadDir = resolved
	}

	pollInterval := fmt.Sprintf("%dm", daemonCfg.Daemon.PollIntervalMinutes)

	// Build command arguments
	args := []string{
		"daemon", "run",
		"--background",
		"--ipc",
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

	a.logInfo("Daemon", fmt.Sprintf("Starting daemon: %s %v", exePath, args))

	// Start the daemon process
	cmd := exec.Command(exePath, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Don't wait for it - it's a background daemon
	// The parent (GUI) will exit the fork, and the child will continue

	// Wait a moment for daemon to initialize
	time.Sleep(500 * time.Millisecond)

	// Verify it started
	if daemon.IsDaemonRunning() == 0 {
		return fmt.Errorf("daemon process started but is not running; check logs")
	}

	a.logInfo("Daemon", "Daemon started successfully")
	return nil
}

// StopDaemon stops the running daemon process via IPC.
func (a *App) StopDaemon() error {
	pid := daemon.IsDaemonRunning()
	if pid == 0 {
		return nil // Already stopped
	}

	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	// Check if IPC is responding
	if !client.IsServiceRunning(ctx) {
		return fmt.Errorf("daemon process found (PID %d) but IPC not responding; use 'kill %d' to force stop", pid, pid)
	}

	a.logInfo("Daemon", fmt.Sprintf("Stopping daemon (PID %d)...", pid))

	// Send shutdown command
	if err := client.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to send shutdown command: %w", err)
	}

	// Wait for daemon to exit
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if daemon.IsDaemonRunning() == 0 {
			a.logInfo("Daemon", "Daemon stopped successfully")
			return nil
		}
	}

	return fmt.Errorf("shutdown command sent but daemon is still running")
}

// TriggerDaemonScan triggers an immediate job scan.
func (a *App) TriggerDaemonScan() error {
	if daemon.IsDaemonRunning() == 0 {
		return fmt.Errorf("daemon is not running")
	}

	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if err := client.TriggerScan(ctx, ""); err != nil {
		return fmt.Errorf("failed to trigger scan: %w", err)
	}

	a.logInfo("Daemon", "Scan triggered")
	return nil
}

// PauseDaemon pauses the daemon's auto-download polling.
func (a *App) PauseDaemon() error {
	if daemon.IsDaemonRunning() == 0 {
		return fmt.Errorf("daemon is not running")
	}

	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if err := client.PauseUser(ctx, ""); err != nil {
		return fmt.Errorf("failed to pause daemon: %w", err)
	}

	a.logInfo("Daemon", "Daemon paused")
	return nil
}

// ResumeDaemon resumes the daemon's auto-download polling.
func (a *App) ResumeDaemon() error {
	if daemon.IsDaemonRunning() == 0 {
		return fmt.Errorf("daemon is not running")
	}

	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	if err := client.ResumeUser(ctx, ""); err != nil {
		return fmt.Errorf("failed to resume daemon: %w", err)
	}

	a.logInfo("Daemon", "Daemon resumed")
	return nil
}

// TriggerProfileRescan asks the daemon to re-enumerate user profiles.
// Called after saving daemon.conf so the service picks up new users.
// Uses existing TriggerScan("all") path.
func (a *App) TriggerProfileRescan() error {
	if daemon.IsDaemonRunning() == 0 {
		return fmt.Errorf("daemon is not running")
	}

	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)

	ctx := context.Background()

	// "all" triggers a profile rescan rather than a single-user scan
	if err := client.TriggerScan(ctx, "all"); err != nil {
		return fmt.Errorf("failed to trigger profile rescan: %w", err)
	}

	a.logInfo("Daemon", "Profile rescan triggered")
	return nil
}

// ReloadConfigResultDTO represents the result of a config reload request from the frontend.
type ReloadConfigResultDTO struct {
	Applied         bool   `json:"applied"`
	Deferred        bool   `json:"deferred"`
	ActiveDownloads int    `json:"activeDownloads"`
	Error           string `json:"error,omitempty"`
}

// ReloadDaemonConfig notifies the running daemon to reload its configuration.
// In subprocess mode, returns whether restart is needed.
func (a *App) ReloadDaemonConfig() ReloadConfigResultDTO {
	result := ReloadConfigResultDTO{}

	// Persist in-memory config to disk before the daemon reloads it.
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
type PreFlightResultDTO struct {
	APIKeyOK    bool   `json:"apiKeyOk"`
	FolderOK    bool   `json:"folderOk"`
	APIKeyError string `json:"apiKeyError,omitempty"`
	FolderError string `json:"folderError,omitempty"`
}

// ValidateAutoDownloadPreFlight checks prerequisites before enabling auto-download.
// Only checks API key and folder — not service/IPC, since user may configure first.
func (a *App) ValidateAutoDownloadPreFlight(downloadFolder string) PreFlightResultDTO {
	result := PreFlightResultDTO{}

	// Check API key
	apiKey := config.ResolveAPIKeyForCurrentUser("")
	if apiKey != "" {
		result.APIKeyOK = true
	} else {
		result.APIKeyError = ipc.CanonicalText[ipc.CodeNoAPIKey] + ". " + ipc.HintFor(ipc.CodeNoAPIKey)
	}

	// Check download folder
	if downloadFolder == "" {
		downloadFolder = config.DefaultDownloadFolder()
	}
	if downloadFolder != "" {
		if info, err := os.Stat(downloadFolder); err == nil && info.IsDir() {
			result.FolderOK = true
		} else if os.IsNotExist(err) {
			// Will be created on daemon start — OK
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
	AutoDownloadTag string `json:"autoDownloadTag"` // Tag for "Conditional" jobs

	// Notifications
	NotificationsEnabled bool `json:"notificationsEnabled"`
	ShowDownloadComplete bool `json:"showDownloadComplete"`
	ShowDownloadFailed   bool `json:"showDownloadFailed"`

	// Config file path (read-only)
	ConfigPath string `json:"configPath"`
}

// GetDaemonConfig returns the current daemon configuration.
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

	result.AutoDownloadTag = cfg.Eligibility.AutoDownloadTag

	result.NotificationsEnabled = cfg.Notifications.Enabled
	result.ShowDownloadComplete = cfg.Notifications.ShowDownloadComplete
	result.ShowDownloadFailed = cfg.Notifications.ShowDownloadFailed

	return result
}

// SaveDaemonConfig saves daemon configuration to daemon.conf.
func (a *App) SaveDaemonConfig(dto DaemonConfigDTO) error {
	// Refuse save when the download folder is not writable from the
	// current-user identity. The Windows-strict branch is in the
	// _windows.go sibling; this file only builds on macOS/Linux.
	if result := pathutil.ValidateWritablePath(dto.DownloadFolder, pathutil.ConsumerCurrentUser); !result.Reachable {
		return fmt.Errorf("%s: %s",
			ipc.CanonicalText[result.ErrorCode], result.Reason)
	}

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

	// Validate before saving
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Persist config.csv + token alongside daemon.conf so every identity
	// that later reads any of these files sees consistent state.
	if err := a.ensureAllConfigPersisted(); err != nil {
		return err
	}

	// Save daemon.conf.
	if err := config.SaveDaemonConfig(cfg, ""); err != nil {
		return fmt.Errorf("failed to save daemon.conf: %w", err)
	}

	a.logInfo("Daemon", "Configuration saved to daemon.conf")

	// If the daemon is running (subprocess OR Windows Service), trigger a
	// profile rescan so the new state is picked up within seconds instead
	// of waiting for the 5-minute rescan tick. IPC-based check works for
	// both modes; a PID check would miss the service case.
	if dto.Enabled {
		client := ipc.NewClient()
		client.SetTimeout(3 * time.Second)
		ctx := context.Background()
		if client.IsServiceRunning(ctx) {
			if err := a.TriggerProfileRescan(); err != nil {
				a.logWarn("Daemon", fmt.Sprintf("Profile rescan after save failed (non-fatal): %v", err))
			}
		}
	}
	return nil
}

// GetDefaultDownloadFolder returns the platform-specific default download folder.
func (a *App) GetDefaultDownloadFolder() string {
	return config.DefaultDownloadFolder()
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
				testFile := downloadFolder + "/.interlink_test"
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

// =============================================================================
// File Logging Settings
// =============================================================================

// FileLoggingSettingsDTO represents file logging configuration.
type FileLoggingSettingsDTO struct {
	Enabled  bool   `json:"enabled"`
	FilePath string `json:"filePath"`
}

// GetFileLoggingSettings returns the current file logging configuration.
func (a *App) GetFileLoggingSettings() FileLoggingSettingsDTO {
	return FileLoggingSettingsDTO{
		Enabled:  IsFileLoggingEnabled(),
		FilePath: GetLogFilePath(),
	}
}

// SetFileLoggingEnabled enables or disables file logging.
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
func (a *App) GetLogFileLocation() string {
	return GetLogFilePath()
}

// =============================================================================
// Daemon Transfer Visibility (Plan 3: unified with GUI Transfers tab)
// =============================================================================

// DaemonTransferTaskDTO mirrors ipc.TransferTaskInfo for the frontend.
// Rendered in the main Transfers tab with a Daemon badge.
type DaemonTransferTaskDTO struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`
	State       string  `json:"state"`
	Name        string  `json:"name"`
	Source      string  `json:"source"`
	Dest        string  `json:"dest"`
	Size        int64   `json:"size"`
	Progress    float64 `json:"progress"`
	Speed       float64 `json:"speed"`
	Error       string  `json:"error,omitempty"`
	SourceLabel string  `json:"sourceLabel"`
	BatchID     string  `json:"batchId"`
	BatchLabel  string  `json:"batchLabel"`
	CreatedAt   int64   `json:"createdAt"`
	StartedAt   int64   `json:"startedAt,omitempty"`
	CompletedAt int64   `json:"completedAt,omitempty"`
}

// DaemonBatchStatsDTO mirrors ipc.BatchStatsInfo.
type DaemonBatchStatsDTO struct {
	BatchID     string  `json:"batchId"`
	BatchLabel  string  `json:"batchLabel"`
	Direction   string  `json:"direction"`
	SourceLabel string  `json:"sourceLabel"`
	Total       int     `json:"total"`
	Queued      int     `json:"queued"`
	Active      int     `json:"active"`
	Completed   int     `json:"completed"`
	Failed      int     `json:"failed"`
	Cancelled   int     `json:"cancelled"`
	TotalBytes  int64   `json:"totalBytes"`
	Progress    float64 `json:"progress"`
	Speed       float64 `json:"speed"`
	TotalKnown  bool    `json:"totalKnown"`
	StartedAt   int64   `json:"startedAt,omitempty"`
}

// DaemonTransferSnapshotDTO is the unified tasks+batches projection of
// daemon transfers, returned by GetDaemonTransferSnapshot.
type DaemonTransferSnapshotDTO struct {
	Tasks   []DaemonTransferTaskDTO `json:"tasks"`
	Batches []DaemonBatchStatsDTO   `json:"batches"`
}

// daemonReachable reports whether there's a daemon we can talk to via IPC
// — either a subprocess PID (non-service) or the Windows service, when
// applicable. Unified helper used by all Plan 3 daemon bindings so service
// mode no longer gets short-circuited by daemon.IsDaemonRunning()==0.
func (a *App) daemonReachable(ctx context.Context, client *ipc.Client) bool {
	if daemon.IsDaemonRunning() != 0 {
		return true
	}
	return client.IsServiceRunning(ctx)
}

// GetDaemonTransferSnapshot retrieves a point-in-time view of daemon
// transfers (tasks + batches) via IPC. Frontend merges these into the
// main Transfers tab's unified tasks/batches arrays; daemon rows render
// with a Daemon badge.
func (a *App) GetDaemonTransferSnapshot() *DaemonTransferSnapshotDTO {
	client := ipc.NewClient()
	client.SetTimeout(3 * time.Second)
	ctx := context.Background()

	if !a.daemonReachable(ctx, client) {
		return &DaemonTransferSnapshotDTO{}
	}

	data, err := client.GetTransferStatus(ctx)
	if err != nil {
		// Without this log line, an IPC auth regression looks like an
		// empty Transfers tab in the UI. Surface the failure to the file
		// logger + Activity tab so it is diagnosable.
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

// CancelDaemonBatch cancels all non-terminal tasks in a daemon-initiated
// batch. Routed by the frontend based on sourceLabel === 'Daemon'.
func (a *App) CancelDaemonBatch(batchID string) error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)
	ctx := context.Background()
	if !a.daemonReachable(ctx, client) {
		return fmt.Errorf("daemon not reachable")
	}
	return client.CancelDaemonBatch(ctx, "", batchID)
}

// CancelDaemonTransfer cancels a single daemon-initiated task.
func (a *App) CancelDaemonTransfer(taskID string) error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)
	ctx := context.Background()
	if !a.daemonReachable(ctx, client) {
		return fmt.Errorf("daemon not reachable")
	}
	return client.CancelDaemonTransfer(ctx, "", taskID)
}

// RetryFailedInDaemonBatch retries all failed tasks in a daemon batch.
func (a *App) RetryFailedInDaemonBatch(batchID string) error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)
	ctx := context.Background()
	if !a.daemonReachable(ctx, client) {
		return fmt.Errorf("daemon not reachable")
	}
	return client.RetryFailedInDaemonBatch(ctx, "", batchID)
}

// =============================================================================
// Daemon Log Retrieval
// =============================================================================

// DaemonLogEntryDTO represents a log entry from the daemon.
type DaemonLogEntryDTO struct {
	Timestamp string                 `json:"timestamp"`
	Level     string                 `json:"level"`
	Stage     string                 `json:"stage"`
	Message   string                 `json:"message"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// GetDaemonLogs retrieves recent log entries from the running daemon.
func (a *App) GetDaemonLogs(count int) []DaemonLogEntryDTO {
	if daemon.IsDaemonRunning() == 0 {
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
// Uses 0700 permissions to restrict log access to owner only.
func (a *App) OpenLogsDirectory() error {
	logsDir := config.LogDirectory()

	// Ensure directory exists
	if err := os.MkdirAll(logsDir, 0700); err != nil {
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Open in system file browser
	// macOS uses "open", Linux uses "xdg-open"
	var cmd *exec.Cmd
	if _, err := exec.LookPath("open"); err == nil {
		// macOS
		cmd = exec.Command("open", logsDir)
	} else {
		// Linux
		cmd = exec.Command("xdg-open", logsDir)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to open logs directory: %w", err)
	}

	return nil
}

// GetLogsDirectory returns the unified logs directory path.
func (a *App) GetLogsDirectory() string {
	return config.LogDirectory()
}

// =============================================================================
// Service Control Stubs for non-Windows
// =============================================================================

// ElevatedServiceResultDTO represents the result of an elevated service operation.
type ElevatedServiceResultDTO struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ServiceStatusDTO represents detailed Windows Service status.
type ServiceStatusDTO struct {
	Installed  bool   `json:"installed"`
	Running    bool   `json:"running"`
	Status     string `json:"status"`
	SCMBlocked bool   `json:"scmBlocked"` // True if SCM access denied
	SCMError   string `json:"scmError"`
}

// GetServiceStatus returns detailed Windows Service status.
// On non-Windows platforms, always returns "not installed".
func (a *App) GetServiceStatus() ServiceStatusDTO {
	return ServiceStatusDTO{
		Installed: false,
		Running:   false,
		Status:    "Not Available (Windows only)",
	}
}

// StartServiceElevated triggers UAC prompt to start Windows Service.
// On non-Windows platforms, returns error.
func (a *App) StartServiceElevated() ElevatedServiceResultDTO {
	return ElevatedServiceResultDTO{
		Success: false,
		Error:   "Windows Service control is only available on Windows",
	}
}

// StopServiceElevated triggers UAC prompt to stop Windows Service.
// On non-Windows platforms, returns error.
func (a *App) StopServiceElevated() ElevatedServiceResultDTO {
	return ElevatedServiceResultDTO{
		Success: false,
		Error:   "Windows Service control is only available on Windows",
	}
}

// InstallAndStartServiceElevated triggers UAC prompt to install + start Windows Service.
// On non-Windows platforms, returns error.
func (a *App) InstallAndStartServiceElevated() ElevatedServiceResultDTO {
	return ElevatedServiceResultDTO{
		Success: false,
		Error:   "Windows Service control is only available on Windows",
	}
}

