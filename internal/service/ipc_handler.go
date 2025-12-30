//go:build windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/logging"
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
	activeDownloads := 0 // TODO: Add download tracking to daemon
	var lastScanTime *time.Time

	for _, s := range statuses {
		if s.Running {
			activeUsers++
		}
	}

	// Calculate uptime
	uptime := time.Since(h.startTime).Truncate(time.Second).String()

	return &ipc.StatusData{
		ServiceState:    "running",
		Version:         cli.Version,
		LastScanTime:    lastScanTime,
		ActiveDownloads: activeDownloads,
		ActiveUsers:     activeUsers,
		LastError:       "",
		Uptime:          uptime,
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
		} else if !s.Enabled {
			state = "disabled"
		}

		users = append(users, ipc.UserStatus{
			Username:       s.Username,
			SID:            "", // TODO: Add SID to UserDaemonStatus
			State:          state,
			DownloadFolder: s.DownloadFolder,
			LastScanTime:   nil, // TODO: Add last scan time tracking
			JobsDownloaded: 0,   // TODO: Add download counter
			LastError:      "",
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
func (h *ServiceIPCHandler) TriggerScan(userID string) error {
	// For now, trigger a full rescan of all profiles
	// This will restart any daemons with changed configs
	// TODO: Add per-user scan triggering to the daemon
	h.service.TriggerRescan()
	return nil
}

// OpenLogs opens the log location for the user or service.
func (h *ServiceIPCHandler) OpenLogs(userID string) error {
	var logsDir string

	if userID == "service" {
		// Service logs are in the system-wide location or program data
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		logsDir = filepath.Join(programData, "Rescale", "Interlink", "logs")
	} else {
		// User logs are in their profile
		// For "current", resolve to the calling user
		if userID == "current" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("could not determine user home: %w", err)
			}
			logsDir = filepath.Join(homeDir, ".config", "rescale", "logs")
		} else {
			// Resolve user's home directory from their profile
			// This is a simplification - in practice we'd look up their profile path
			logsDir = filepath.Join(`C:\Users`, userID, ".config", "rescale", "logs")
		}
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		h.logger.Warn().Err(err).Str("dir", logsDir).Msg("Failed to create logs directory")
	}

	// Open in Explorer
	// Note: This runs from the service context, so may not show on user's desktop
	// The tray app should handle this locally instead
	cmd := exec.Command("explorer.exe", logsDir)
	return cmd.Start()
}

// Ensure ServiceIPCHandler implements ipc.ServiceHandler
var _ ipc.ServiceHandler = (*ServiceIPCHandler)(nil)
