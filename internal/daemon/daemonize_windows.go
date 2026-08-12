//go:build windows

// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/windows"
)

// PIDFilePath returns the path to the daemon PID file.
// Uses %LOCALAPPDATA%\Rescale\Interlink\ (consistent with install/logs paths).
func PIDFilePath() string {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return filepath.Join(os.TempDir(), "rescale-daemon.pid")
	}
	return filepath.Join(localAppData, "Rescale", "Interlink", "daemon.pid")
}

// oldPIDFilePath returns the legacy PID file path for migration cleanup.
func oldPIDFilePath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	return filepath.Join(appData, "Rescale", "daemon.pid")
}

// WritePIDFile claims the daemon PID file for this process.
// Also cleans up old PID file from legacy path.
//
// The claim is an exclusive create, so two daemons starting at the same time
// cannot both pass the earlier IsDaemonRunning() check and then both write the
// file — the loser gets an error instead of silently becoming a second daemon
// polling the same jobs into the same folder. A PID file left behind by a dead
// process is cleared by IsDaemonRunning(), which callers run first; this
// function also clears it directly so a stale file cannot lock the daemon out.
func WritePIDFile() error {
	// Clean up old PID file if it exists
	if oldPath := oldPIDFilePath(); oldPath != "" {
		os.Remove(oldPath)
	}

	pidPath := PIDFilePath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(pidPath), 0700); err != nil {
		return fmt.Errorf("failed to create PID file directory: %w", err)
	}

	pid := os.Getpid()
	f, err := os.OpenFile(pidPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("failed to write PID file: %w", err)
		}
		if running := IsDaemonRunning(); running != 0 {
			return fmt.Errorf("daemon is already running (PID %d)", running)
		}
		// Also covers an empty or unparseable file, which IsDaemonRunning
		// reports as "not running" without removing.
		os.Remove(pidPath)
		f, err = os.OpenFile(pidPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("failed to write PID file: %w", err)
		}
	}

	if _, err := f.WriteString(strconv.Itoa(pid)); err != nil {
		f.Close()
		os.Remove(pidPath)
		return fmt.Errorf("failed to write PID file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(pidPath)
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	return nil
}

// RemovePIDFile removes the PID file.
func RemovePIDFile() {
	os.Remove(PIDFilePath())
}

// ReadPIDFile reads the PID from the PID file.
// Returns 0 if the file doesn't exist or is invalid.
func ReadPIDFile() int {
	data, err := os.ReadFile(PIDFilePath())
	if err != nil {
		return 0
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return 0
	}

	return pid
}

// IsDaemonRunning checks if a daemon process is already running.
// Returns the PID if running, 0 if not.
func IsDaemonRunning() int {
	pid := ReadPIDFile()
	if pid == 0 {
		return 0
	}

	// os.FindProcess always succeeds on Windows even for non-existent PIDs,
	// so we use Windows-specific API to verify the process is alive.
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Process doesn't exist - clean up stale PID file
		RemovePIDFile()
		return 0
	}
	windows.CloseHandle(handle)

	return pid
}

// Daemonize on Windows is not supported for direct daemon mode.
// Windows uses the Windows Service Manager instead.
func Daemonize(args []string) error {
	return fmt.Errorf("daemonization not supported on Windows - use Windows Service instead")
}

// IsDaemonChild returns true if we're running as the daemon child process.
// On Windows, this is always false (no forking support).
func IsDaemonChild() bool {
	return false
}
