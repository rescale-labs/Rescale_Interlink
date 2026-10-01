//go:build !windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

var errNoHome = errors.New("cannot place this user's socket without their home folder")

// UserSocketPath is this user's socket file under ~/.config/rescale: the
// daemon's or the rate-limit coordinator's. Without a home folder there is
// none; a shared path, such as one in /tmp, could be another user's.
func UserSocketPath(file string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w", errNoHome, err)
	}
	return filepath.Join(home, ".config", "rescale", file), nil
}

// CurrentUserSID returns "": only Windows users have a SID.
func CurrentUserSID() (string, error) { return "", nil }

// GetSocketPath returns the daemon's socket, ~/.config/rescale/interlink.sock,
// or "" when this user's home folder is unknown.
func GetSocketPath() string {
	path, _ := UserSocketPath("interlink.sock")
	return path
}

// Client connects to the IPC server via Unix domain socket.
type Client struct {
	timeout    time.Duration
	socketPath string
}

// NewClient creates a new IPC client.
func NewClient() *Client {
	return &Client{
		timeout:    5 * time.Second,
		socketPath: GetSocketPath(),
	}
}

// NewClientWithPath creates a new IPC client with a custom socket path.
func NewClientWithPath(socketPath string) *Client {
	return &Client{
		timeout:    5 * time.Second,
		socketPath: socketPath,
	}
}

// connect establishes a connection to the Unix socket.
func (c *Client) connect(ctx context.Context) (net.Conn, error) {
	if c.socketPath == "" {
		return nil, errNoHome
	}
	// Create a dialer with timeout
	dialer := net.Dialer{
		Timeout: c.timeout,
	}

	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IPC server at %s: %w", c.socketPath, err)
	}

	return conn, nil
}
