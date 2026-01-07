//go:build !windows

// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
package wailsapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
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
// This is the primary method for the frontend to check if the daemon is running
// and get its current state.
func (a *App) GetDaemonStatus() DaemonStatusDTO {
	result := DaemonStatusDTO{
		State: "stopped",
	}

	// Check if daemon process is running via PID file
	pid := daemon.IsDaemonRunning()
	result.PID = pid
	result.Running = pid != 0

	// Try to connect via IPC for live status
	client := ipc.NewClient()
	client.SetTimeout(2 * time.Second)

	ctx := context.Background()
	if status, err := client.GetStatus(ctx); err == nil {
		result.IPCConnected = true
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
	} else if pid != 0 {
		// Process running but IPC not responding
		result.State = "unknown"
		result.Error = "Daemon process found but IPC not responding. It may not have been started with --ipc flag."
	}

	return result
}

// StartDaemon starts the daemon process in background mode with IPC enabled.
// This spawns a new process that survives the GUI closing.
func (a *App) StartDaemon() error {
	// Check if already running
	if pid := daemon.IsDaemonRunning(); pid != 0 {
		return fmt.Errorf("daemon is already running (PID %d)", pid)
	}

	// Get current executable path
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Get download directory and poll interval from auto-download config (APIConfig)
	downloadDir := "."
	pollInterval := "5m"

	// Load auto-download config from APIConfig (separate from main config.Config)
	if apiCfg, err := config.LoadAPIConfig(""); err == nil && apiCfg != nil {
		if apiCfg.AutoDownload.DefaultDownloadFolder != "" {
			downloadDir = apiCfg.AutoDownload.DefaultDownloadFolder
		}
		if apiCfg.AutoDownload.ScanIntervalMinutes > 0 {
			pollInterval = fmt.Sprintf("%dm", apiCfg.AutoDownload.ScanIntervalMinutes)
		}
	}

	// Build command arguments
	args := []string{
		"daemon", "run",
		"--background",
		"--ipc",
		"--download-dir", downloadDir,
		"--poll-interval", pollInterval,
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

