//go:build !windows

package coordinator

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// spawnCoordinator starts a new coordinator process detached from the current terminal.
func spawnCoordinator() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	cmd := exec.Command(executable, "ratelimit-coordinator", "run")
	cmd.Env = append(os.Environ(), "RESCALE_COORDINATOR_CHILD=1")

	// Detach from terminal
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // Create new session
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start coordinator: %w", err)
	}

	// Release the process so it can run independently
	cmd.Process.Release()
	return nil
}

// isProcessAlive checks if a process with the given PID exists.
func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Use kill(0) to check existence.
	err = process.Signal(syscall.Signal(0))
	return err == nil
}
