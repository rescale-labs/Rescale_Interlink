// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
package wailsapp

import (
	"context"
	"fmt"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/pathutil"
)

// DaemonStatusDTO represents the daemon status for the frontend.
type DaemonStatusDTO struct {
	// Running indicates if the daemon process is running
	Running bool `json:"running"`

	// PID is the process ID (0 if not running)
	PID int `json:"pid"`

	// IPCConnected indicates if we can communicate with the daemon via IPC
	IPCConnected bool `json:"ipcConnected"`

	// State is the daemon state ("running", "paused", "stopped", "error", "pending")
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

	// LastErrorTime is when Error was recorded (ISO format, or empty). Lets the
	// GUI show how stale the failure is, which is the only way to tell a scan
	// that just failed from one that failed hours ago and never recovered.
	LastErrorTime string `json:"lastErrorTime,omitempty"`

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

// ReloadConfigResultDTO represents the result of a config reload request from the frontend.
type ReloadConfigResultDTO struct {
	Applied         bool   `json:"applied"`
	Deferred        bool   `json:"deferred"`
	ActiveDownloads int    `json:"activeDownloads"`
	Error           string `json:"error,omitempty"`
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

	// Check download folder. A stat is not enough: an existing directory the
	// daemon cannot write to used to pass pre-flight and then fail on every
	// download. ValidateWritablePath is the same probe SaveDaemonConfig gates
	// on, so pre-flight and save agree.
	if downloadFolder == "" {
		downloadFolder = config.DefaultDownloadFolder()
	}
	if downloadFolder != "" {
		if res := pathutil.ValidateWritablePath(downloadFolder, pathutil.ConsumerCurrentUser); res.Reachable {
			result.FolderOK = true
		} else {
			result.FolderError = fmt.Sprintf("%s: %s",
				ipc.CanonicalText[res.ErrorCode], res.Reason)
		}
	}

	return result
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

// GetDefaultDownloadFolder returns the platform-specific default download folder.
func (a *App) GetDefaultDownloadFolder() string {
	return config.DefaultDownloadFolder()
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

// DaemonLogEntryDTO represents a log entry from the daemon.
type DaemonLogEntryDTO struct {
	Timestamp string                 `json:"timestamp"`
	Level     string                 `json:"level"`
	Stage     string                 `json:"stage"`
	Message   string                 `json:"message"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// ElevatedServiceResultDTO represents the result of an elevated service operation.
type ElevatedServiceResultDTO struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}
