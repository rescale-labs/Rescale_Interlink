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

// WritePIDFile writes the current process's PID to the PID file.
// Also cleans up old PID file from legacy path.
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

	// Write PID
	pid := os.Getpid()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0600); err != nil {
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

// stillActiveExitCode is the value GetExitCodeProcess returns while a process
// is still running (STILL_ACTIVE / STATUS_PENDING = 259).
const stillActiveExitCode = 259

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
	defer windows.CloseHandle(handle)

	// OpenProcess succeeds even for a TERMINATED process as long as any handle
	// to it remains open — and the GUI/tray that spawned the daemon keep its
	// process handle open (they never Wait()). So a dead daemon's PID object
	// lingers and OpenProcess would falsely report it alive, blocking restarts.
	// Confirm liveness via the exit code: STILL_ACTIVE means running.
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		// Can't determine state — be conservative and treat as not running so
		// a new daemon can start; clean up the stale PID file.
		RemovePIDFile()
		return 0
	}
	if exitCode != stillActiveExitCode {
		// Process has exited - clean up stale PID file.
		RemovePIDFile()
		return 0
	}

	return pid
}

// KillDaemon forcefully terminates the running daemon process recorded in the
// PID file. It is the last-resort fallback for callers (e.g. the uninstaller)
// that cannot rely on a graceful IPC shutdown — the daemon is a detached,
// windowless subprocess, so Windows Restart Manager cannot close it and its
// executable stays locked during uninstall.
//
// Returns nil when no daemon is running (nothing to kill) or the process was
// terminated. The PID file is removed on success.
func KillDaemon() error {
	pid := IsDaemonRunning()
	if pid == 0 {
		return nil // Not running (IsDaemonRunning already cleaned any stale PID file)
	}

	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("failed to open daemon process %d for termination: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	if err := windows.TerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("failed to terminate daemon process %d: %w", pid, err)
	}

	RemovePIDFile()
	return nil
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
