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
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/service"
)

// StartDaemon starts the daemon as a subprocess (no admin required).
// It runs in the user's own session. Blocks the spawn while a service from an
// earlier version is running.
func (a *App) StartDaemon() error {
	// Ensure config.csv and token file are on disk before the subprocess
	// reads them.
	if err := a.ensureAllConfigPersisted(); err != nil {
		return fmt.Errorf("cannot start daemon: %w", err)
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

	// Only what this start's daemon writes to the stderr log says why it
	// stopped: the log is appended to.
	stderrPath := filepath.Join(logsDir, config.DaemonStderrLogName)
	var stderrStart int64
	if info, err := os.Stat(stderrPath); err == nil {
		stderrStart = info.Size()
	}
	if err := daemon.Start(a.loadDaemonConfig()); err != nil {
		a.logError("Daemon", fmt.Sprintf("Subprocess launch failed: %v", err))
		return fmt.Errorf("cannot start daemon: %w", err)
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
	errDetail := ""
	if stderrData, readErr := os.ReadFile(stderrPath); readErr == nil && int64(len(stderrData)) > stderrStart {
		if why := childStderr(string(stderrData[stderrStart:])); why != "" {
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

// StopDaemon stops the daemon via IPC shutdown command, which needs no admin.
func (a *App) StopDaemon() error {
	client := ipc.NewClient()
	client.SetTimeout(5 * time.Second)
	ctx := context.Background()

	// Check if daemon is running
	if !client.IsServiceRunning(ctx) {
		if pid := daemon.IsDaemonRunning(); pid != 0 {
			return fmt.Errorf("daemon process found (PID %d) but IPC not responding. %s", pid, service.EarlierDaemonRunning)
		}
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
