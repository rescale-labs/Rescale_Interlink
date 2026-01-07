//go:build windows

// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"github.com/rescale/rescale-int/internal/ipc"
)

// IPCHandler is a stub on Windows.
// On Windows, the service uses the Windows Service Manager for daemon control,
// and the IPC is handled by the service package, not the daemon package.
type IPCHandler struct{}

// NewIPCHandler is a stub on Windows.
// This function exists for API compatibility but should never be called.
// On Windows, use the service.ServiceIPCHandler instead.
func NewIPCHandler(daemon *Daemon, shutdownFunc func()) *IPCHandler {
	return &IPCHandler{}
}

// The IPCHandler implements ipc.ServiceHandler but all methods are no-ops on Windows.
// This satisfies the compiler but shouldn't actually be used.

func (h *IPCHandler) GetStatus() *ipc.StatusData {
	return &ipc.StatusData{
		ServiceState: "windows-service",
		Version:      "4.1.0",
	}
}

func (h *IPCHandler) GetUserList() []ipc.UserStatus {
	return nil
}

func (h *IPCHandler) PauseUser(userID string) error {
	return nil
}

func (h *IPCHandler) ResumeUser(userID string) error {
	return nil
}

func (h *IPCHandler) TriggerScan(userID string) error {
	return nil
}

func (h *IPCHandler) OpenLogs(userID string) error {
	return nil
}

func (h *IPCHandler) Shutdown() error {
	return nil
}

func (h *IPCHandler) IsPaused() bool {
	return false
}

func (h *IPCHandler) ShouldPoll() bool {
	return true
}
