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

// StartDaemon starts the daemon process in background mode with IPC enabled.
// This spawns a new process that survives the GUI closing.
func (a *App) StartDaemon() error {
	// What 'daemon run' would refuse, refused here with its reason: a daemon
	// that is running, or a PID file that cannot be read.
	if err := daemon.CheckPIDFile(); err != nil {
		return err
	}

	// Ensure config.csv and token file are on disk before the subprocess
	// reads them.
	if err := a.ensureAllConfigPersisted(); err != nil {
		return fmt.Errorf("cannot start daemon: %w", err)
	}

	a.logInfo("Daemon", "Starting daemon")
	if err := daemon.Start(a.loadDaemonConfig()); err != nil {
		return fmt.Errorf("cannot start daemon: %w", err)
	}

	// Wait a moment for daemon to initialize
	time.Sleep(500 * time.Millisecond)

	// Verify it started
	if daemon.IsDaemonRunning() == 0 {
		return fmt.Errorf("daemon process started but is not running; see its log in %s", config.LogDirectory())
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
