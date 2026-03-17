//go:build !windows

package coordinator

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// SocketPath returns the path to the coordinator Unix domain socket.
// On Mac/Linux: ~/.config/rescale/ratelimit-coordinator.sock
func SocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/rescale-ratelimit-coordinator.sock"
	}
	return filepath.Join(home, ".config", "rescale", "ratelimit-coordinator.sock")
}

// Listen creates a Unix domain socket listener for the coordinator.
func Listen() (net.Listener, error) {
	sockPath := SocketPath()

	// Ensure socket directory exists
	socketDir := filepath.Dir(sockPath)
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	// Remove any stale socket file
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to remove stale socket: %w", err)
	}

	// Create Unix socket listener
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create coordinator socket: %w", err)
	}

	// Set socket permissions (user only)
	if err := os.Chmod(sockPath, 0600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("failed to set socket permissions: %w", err)
	}

	return listener, nil
}

// CleanupSocket removes the socket file. Called on shutdown.
func CleanupSocket() {
	os.Remove(SocketPath())
}
