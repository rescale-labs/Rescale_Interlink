//go:build !windows

// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// oldPIDFilePath returns where an earlier version kept the PID file: nowhere
// else, on Unix.
func oldPIDFilePath() string { return "" }

// lockExclusive takes an exclusive lock on the file at path, creating it, and
// holds it until unlock is called; see lockFile.
func lockExclusive(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
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
