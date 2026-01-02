//go:build windows

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

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
		Version:         version.Version,
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

// OpenLogs returns the log location path for the user or service.
// v4.0.7 H3: This method should NOT try to open explorer.exe from SYSTEM context,
// as it will silently fail (GUI apps don't display when run as SYSTEM).
// The tray app handles "View Logs" locally via viewLogs() which runs in user context.
// This IPC method is kept for potential future use (e.g., returning the path to the caller).
func (h *ServiceIPCHandler) OpenLogs(userID string) error {
	var logsDir string

	if userID == "service" {
		// Service logs are in the system-wide location or program data
		// v4.0.7 M1: Use environment variable, with standard Windows fallback
		programData := os.Getenv("ProgramData")
		if programData == "" {
			// Standard Windows location if env var not set
			programData = filepath.Join(os.Getenv("SystemDrive"), "ProgramData")
			if programData == "ProgramData" {
				programData = `C:\ProgramData` // Final fallback
			}
		}
		logsDir = filepath.Join(programData, "Rescale", "Interlink", "logs")
	} else {
		// User logs are in their profile
		if userID == "current" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("could not determine user home: %w", err)
			}
			logsDir = filepath.Join(homeDir, ".config", "rescale", "logs")
		} else {
			// v4.0.7 M1: Use USERPROFILE pattern instead of hardcoded C:\Users
			// Look up user's profile path from registry or use standard pattern
			profileRoot := os.Getenv("PUBLIC")
			if profileRoot != "" {
				// PUBLIC is like C:\Users\Public, so parent is C:\Users
				profileRoot = filepath.Dir(profileRoot)
			} else {
				// Fallback to standard Windows users directory
				profileRoot = filepath.Join(os.Getenv("SystemDrive"), "Users")
				if profileRoot == "Users" {
					profileRoot = `C:\Users` // Final fallback
				}
			}
			logsDir = filepath.Join(profileRoot, userID, ".config", "rescale", "logs")
		}
	}

	// Log the path for debugging
	h.logger.Debug().
		Str("userID", userID).
		Str("logsDir", logsDir).
		Msg("OpenLogs request received")

	// v4.0.7 H3: Do NOT try to run explorer.exe from SYSTEM context.
	// It won't show on the user's desktop. The tray app handles this locally.
	// Just ensure the directory exists and return success.
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		h.logger.Warn().Err(err).Str("dir", logsDir).Msg("Failed to create logs directory")
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	return nil
}

// Ensure ServiceIPCHandler implements ipc.ServiceHandler
var _ ipc.ServiceHandler = (*ServiceIPCHandler)(nil)
