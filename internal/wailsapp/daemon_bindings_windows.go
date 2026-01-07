//go:build windows

// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
package wailsapp

import (
	"context"
	"fmt"
	"time"

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
