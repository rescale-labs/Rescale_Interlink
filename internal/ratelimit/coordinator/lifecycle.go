package coordinator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// PIDFilePath returns the path to the coordinator PID file.
func PIDFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/rescale-ratelimit-coordinator.pid"
	}
	return filepath.Join(home, ".config", "rescale", "ratelimit-coordinator.pid")
}

// WritePIDFile writes the current process's PID to the coordinator PID file.
func WritePIDFile() error {
	pidPath := PIDFilePath()
	if err := os.MkdirAll(filepath.Dir(pidPath), 0700); err != nil {
		return fmt.Errorf("failed to create PID file directory: %w", err)
	}
	pid := os.Getpid()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0600); err != nil {
		return fmt.Errorf("failed to write PID file: %w", err)
	}
	return nil
}

// RemovePIDFile removes the coordinator PID file.
func RemovePIDFile() {
	os.Remove(PIDFilePath())
}

// ReadPIDFile reads the PID from the coordinator PID file.
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

// EnsureCoordinator connects to an existing coordinator or starts a new one.
// Returns a connected Client, or an error if the coordinator cannot be reached.
//
// Steps:
//  1. Try connecting to existing coordinator (500ms timeout)
//  2. If connected: return client
//  3. Check PID file — if valid PID exists, wait up to 3s for socket
//  4. If no coordinator: spawn via exec.Command(os.Executable(), "ratelimit-coordinator", "run")
//  5. Wait up to 3s for socket to appear
//  6. Connect and return client, or return error
func EnsureCoordinator() (*Client, error) {
	client := NewClient()

	// Step 1: Try connecting to existing coordinator
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := client.Ping(ctx); err == nil {
		return client, nil
	}

	// Step 2: Check PID file
	pid := ReadPIDFile()
	if pid > 0 && isProcessAlive(pid) {
		// Coordinator process exists but socket not ready yet — wait
		if waitForSocket(3 * time.Second) {
			ctx2, cancel2 := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel2()
			if err := client.Ping(ctx2); err == nil {
				return client, nil
			}
		}
	}

	// Step 3: Spawn new coordinator
	if err := spawnCoordinator(); err != nil {
		return nil, fmt.Errorf("failed to spawn coordinator: %w", err)
	}

	// Step 4: Wait for socket to appear
	if !waitForSocket(3 * time.Second) {
		return nil, fmt.Errorf("coordinator did not start within 3s")
	}

	// Step 5: Connect
	ctx3, cancel3 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel3()
	if err := client.Ping(ctx3); err != nil {
		return nil, fmt.Errorf("coordinator started but not responding: %w", err)
	}

	return client, nil
}

// waitForSocket polls for the coordinator socket to appear.
func waitForSocket(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(SocketPath()); err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// spawnCoordinator and isProcessAlive are implemented in platform-specific files:
// - lifecycle_unix.go
// - lifecycle_windows.go
