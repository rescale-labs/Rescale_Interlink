//go:build !windows

// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// PIDFilePath returns the path to the daemon PID file.
func PIDFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/rescale-daemon.pid"
	}
	return filepath.Join(home, ".config", "rescale", "daemon.pid")
}

// WritePIDFile writes the current process's PID to the PID file.
func WritePIDFile() error {
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

// IsDaemonRunning checks if a daemon process is already running.
// Returns the PID if running, 0 if not.
func IsDaemonRunning() int {
	pid := ReadPIDFile()
	if pid == 0 {
		return 0
	}

	// Check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return 0
	}

	// On Unix, FindProcess always succeeds. Use kill(0) to check if it exists.
	err = process.Signal(syscall.Signal(0))
	if err != nil {
		// Process doesn't exist - clean up stale PID file
		RemovePIDFile()
		return 0
	}

	return pid
}

// Daemonize re-executes the current process as a daemon.
// This performs true Unix daemonization:
// 1. Fork via exec (Go doesn't support fork directly)
// 2. The child process calls setsid to create a new session
// 3. Close stdin/stdout/stderr
// 4. Change to root directory (optional)
//
// Returns nil in the parent (after forking), or runs the daemon in the child.
func Daemonize(args []string) error {
	// Check if we're already the daemon (indicated by env var)
	if os.Getenv("RESCALE_DAEMON_CHILD") == "1" {
		// We are the daemon child - continue running
		return nil
	}

	// Get the current executable
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Prepare the command with the same arguments
	cmd := exec.Command(executable, args...)

	// Set environment to indicate this is the daemon child
	cmd.Env = append(os.Environ(), "RESCALE_DAEMON_CHILD=1")

	// Detach from terminal
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	// Set process group for full detachment
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // Create new session (setsid)
	}

	// Start the daemon
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Parent exits successfully
	fmt.Printf("Daemon started with PID %d\n", cmd.Process.Pid)
	os.Exit(0)

	return nil // Never reached
}

// IsDaemonChild returns true if we're running as the daemon child process.
func IsDaemonChild() bool {
	return os.Getenv("RESCALE_DAEMON_CHILD") == "1"
}
