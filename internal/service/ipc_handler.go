//go:build windows

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/version"
)

// ServiceIPCHandler adapts the MultiUserService to the IPC ServiceHandler interface.
// This allows the IPC server to query and control the service.
type ServiceIPCHandler struct {
	service   *MultiUserService
	logger    *logging.Logger
	startTime time.Time
}

// NewServiceIPCHandler creates a new IPC handler for the multi-user service.
func NewServiceIPCHandler(service *MultiUserService, logger *logging.Logger) *ServiceIPCHandler {
	return &ServiceIPCHandler{
		service:   service,
		logger:    logger,
		startTime: time.Now(),
	}
}

// GetStatus returns the current service status in IPC format.
func (h *ServiceIPCHandler) GetStatus() *ipc.StatusData {
	statuses := h.service.GetStatus()

	// Count active users and downloads
	activeUsers := 0
	activeDownloads := 0
	var lastScanTime *time.Time

	for _, s := range statuses {
		if s.Running {
			activeUsers++
			activeDownloads += s.ActiveDownloads

			// Track most recent scan time across all users
			if !s.LastScanTime.IsZero() {
				if lastScanTime == nil || s.LastScanTime.After(*lastScanTime) {
					scanTime := s.LastScanTime
					lastScanTime = &scanTime
				}
			}
		}
	}

	// Calculate uptime
	uptime := time.Since(h.startTime).Truncate(time.Second).String()

	return &ipc.StatusData{
		ServiceState:    "running",
		Version:         version.Version,
		LastScanTime:    lastScanTime,
		ActiveDownloads: activeDownloads,
		ActiveUsers:     activeUsers,
		LastError:       "",
		Uptime:          uptime,
		ServiceMode:     true,
	}
}

// GetUserList returns the list of user daemon statuses in IPC format.
func (h *ServiceIPCHandler) GetUserList() []ipc.UserStatus {
	statuses := h.service.GetStatus()

	users := make([]ipc.UserStatus, 0, len(statuses))
	for _, s := range statuses {
		state := "stopped"
		if s.Running {
			state = "running"
		} else if s.LastError != "" {
			state = "error"
		} else if !s.Enabled {
			state = "disabled"
		}

		var lastScanTime *time.Time
		if !s.LastScanTime.IsZero() {
			t := s.LastScanTime
			lastScanTime = &t
		}

		users = append(users, ipc.UserStatus{
			Username:       s.Username,
			SID:            s.SID,
			State:          state,
			DownloadFolder: s.DownloadFolder,
			LastScanTime:   lastScanTime,
			JobsDownloaded: s.JobsDownloaded,
			LastError:      s.LastError,
			ErrorCode:      s.ErrorCode,
		})
	}

	return users
}

// PauseUser pauses auto-download for a specific user.
func (h *ServiceIPCHandler) PauseUser(userID string) error {
	// Handle "current" as the calling user
	if userID == "current" {
		// Get current user's username
		currentUser := os.Getenv("USERNAME")
		if currentUser == "" {
			return fmt.Errorf("could not determine current user")
		}
		userID = currentUser
	}

	return h.service.PauseUser(userID)
}

// ResumeUser resumes auto-download for a specific user.
func (h *ServiceIPCHandler) ResumeUser(userID string) error {
	// Handle "current" as the calling user
	if userID == "current" {
		currentUser := os.Getenv("USERNAME")
		if currentUser == "" {
			return fmt.Errorf("could not determine current user")
		}
		userID = currentUser
	}

	return h.service.ResumeUser(userID)
}

// TriggerScan triggers an immediate job scan.
// Routes to specific user if SID/username provided; "all" triggers a full rescan.
func (h *ServiceIPCHandler) TriggerScan(userID string) error {
	if userID == "" || userID == "all" {
		// Trigger a full rescan of all profiles
		h.logger.Info().Msg("Scan triggered via IPC for all users")
		h.service.TriggerRescan()
		return nil
	}
	// Trigger scan for specific user only (accepts SID or username)
	h.logger.Info().Str("user_id", userID).Msg("Scan triggered via IPC for user")
	return h.service.TriggerUserScan(userID)
}

// OpenLogs ensures the log directory for the caller (or service) exists.
// The tray app handles the actual "View Logs" open from user context, because
// the service runs as SYSTEM and explorer.exe launched from SYSTEM does not
// surface on the user's desktop.
func (h *ServiceIPCHandler) OpenLogs(userID string) error {
	var logsDir string

	switch userID {
	case "service", "current", "":
		logsDir = config.LogDirectory()
	default:
		profileRoot := os.Getenv("PUBLIC")
		if profileRoot != "" {
			profileRoot = filepath.Dir(profileRoot)
		} else {
			profileRoot = filepath.Join(os.Getenv("SystemDrive"), "Users")
			if profileRoot == "Users" {
				profileRoot = `C:\Users`
			}
		}
		logsDir = config.LogDirectoryForUser(filepath.Join(profileRoot, userID))
	}

	h.logger.Debug().
		Str("userID", userID).
		Str("logsDir", logsDir).
		Msg("OpenLogs request received")

	if err := os.MkdirAll(logsDir, 0700); err != nil {
		h.logger.Warn().Err(err).Str("dir", logsDir).Msg("Failed to create logs directory")
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	return nil
}

// Shutdown gracefully stops the multi-user daemon service.
func (h *ServiceIPCHandler) Shutdown() error {
	h.logger.Info().Msg("IPC shutdown requested")
	h.service.Stop()
	return nil
}

// ReloadConfig handles config reload for service mode.
// Delegates to TriggerRescan() which detects config changes and restarts per-user daemons.
func (h *ServiceIPCHandler) ReloadConfig(userID string) *ipc.ReloadConfigData {
	h.logger.Info().Str("user_id", userID).Msg("Config reload requested via IPC — triggering rescan")
	h.service.TriggerRescan()
	return &ipc.ReloadConfigData{
		Applied: true,
	}
}

// GetTransferStatus returns a snapshot of the per-user daemon's transfer
// queue filtered to SourceLabel=Daemon.
func (h *ServiceIPCHandler) GetTransferStatus(userID string) (*ipc.DaemonTransferSnapshot, error) {
	return h.service.GetUserTransferStatus(userID), nil
}

// CancelDaemonBatch cancels non-terminal tasks in a per-user daemon batch.
func (h *ServiceIPCHandler) CancelDaemonBatch(userID, batchID string) error {
	return h.service.CancelUserDaemonBatch(userID, batchID)
}

// CancelDaemonTransfer cancels one task in a per-user daemon.
func (h *ServiceIPCHandler) CancelDaemonTransfer(userID, taskID string) error {
	return h.service.CancelUserDaemonTransfer(userID, taskID)
}

// RetryFailedInDaemonBatch retries failed tasks in a per-user daemon batch.
func (h *ServiceIPCHandler) RetryFailedInDaemonBatch(userID, batchID string) error {
	return h.service.RetryFailedInUserDaemonBatch(userID, batchID)
}

// GetRecentLogs returns recent log entries from the daemon.
func (h *ServiceIPCHandler) GetRecentLogs(userID string, count int) []ipc.LogEntryData {
	if logs := h.service.GetUserLogs(userID, count); logs != nil {
		return logs
	}
	return []ipc.LogEntryData{}
}

// Ensure ServiceIPCHandler implements ipc.ServiceHandler
var _ ipc.ServiceHandler = (*ServiceIPCHandler)(nil)
