//go:build windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"time"
)

// Client connects to the IPC server to send requests.
type Client struct {
	timeout time.Duration
}

// NewClient creates a new IPC client.
func NewClient() *Client {
	return &Client{
		timeout: 5 * time.Second,
	}
}

// connect establishes a connection to the named pipe.
func (c *Client) connect(ctx context.Context) (net.Conn, error) {
	name, err := UserPipeName(pipeBase)
	if err != nil {
		return nil, err
	}
	// Use context with timeout
	dialCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conn, err := DialUserPipe(dialCtx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IPC server: %w", err)
	}

	return conn, nil
}
