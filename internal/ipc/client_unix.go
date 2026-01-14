//go:build !windows

// Package ipc provides inter-process communication between the daemon
// and the GUI application using Unix domain sockets.
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// GetSocketPath returns the path to the Unix domain socket.
// On Mac/Linux: ~/.config/rescale/interlink.sock
func GetSocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/rescale-interlink.sock"
	}
	return filepath.Join(home, ".config", "rescale", "interlink.sock")
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

// SetTimeout sets the connection timeout.
func (c *Client) SetTimeout(timeout time.Duration) {
	c.timeout = timeout
}

// connect establishes a connection to the Unix socket.
func (c *Client) connect(ctx context.Context) (net.Conn, error) {
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

// sendRequest sends a request and receives a response.
func (c *Client) sendRequest(ctx context.Context, req *Request) (*Response, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Set deadline for the entire operation
	conn.SetDeadline(time.Now().Add(c.timeout))

	// Encode and send request
	data, err := req.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}
	data = append(data, '\n')

	_, err = conn.Write(data)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Read response
	reader := bufio.NewReader(conn)
	respData, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Decode response
	resp, err := DecodeResponse(respData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return resp, nil
}

// GetStatus retrieves the current daemon status.
func (c *Client) GetStatus(ctx context.Context) (*StatusData, error) {
	req := NewRequest(MsgGetStatus)
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("server error: %s", resp.Error)
	}

	return resp.GetStatusData(), nil
}

// GetUserList retrieves the list of user daemon statuses.
// On Unix, this typically returns a single user (the current user).
func (c *Client) GetUserList(ctx context.Context) ([]UserStatus, error) {
	req := NewRequest(MsgGetUserList)
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("server error: %s", resp.Error)
	}

	data := resp.GetUserListData()
	if data == nil {
		return []UserStatus{}, nil
	}
	return data.Users, nil
}

// PauseUser pauses auto-download.
// On Unix single-user mode, userID is ignored.
func (c *Client) PauseUser(ctx context.Context, userID string) error {
	req := NewRequestWithUser(MsgPauseUser, userID)
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("server error: %s", resp.Error)
	}
	return nil
}

// ResumeUser resumes auto-download.
// On Unix single-user mode, userID is ignored.
func (c *Client) ResumeUser(ctx context.Context, userID string) error {
	req := NewRequestWithUser(MsgResumeUser, userID)
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("server error: %s", resp.Error)
	}
	return nil
}

// TriggerScan triggers an immediate job scan.
func (c *Client) TriggerScan(ctx context.Context, userID string) error {
	req := NewRequestWithUser(MsgTriggerScan, userID)
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("server error: %s", resp.Error)
	}
	return nil
}

// OpenLogs opens the log viewer.
func (c *Client) OpenLogs(ctx context.Context, userID string) error {
	req := NewRequestWithUser(MsgOpenLogs, userID)
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("server error: %s", resp.Error)
	}
	return nil
}

// Shutdown sends a shutdown command to the daemon.
// Unlike Windows (where service manager handles shutdown),
// Unix daemons can be shutdown via IPC.
func (c *Client) Shutdown(ctx context.Context) error {
	req := NewRequest(MsgShutdown)
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		// Connection closed is expected after shutdown
		return nil
	}

	if !resp.Success {
		return fmt.Errorf("server error: %s", resp.Error)
	}
	return nil
}

// IsServiceRunning checks if the IPC server (and thus the daemon) is running.
func (c *Client) IsServiceRunning(ctx context.Context) bool {
	_, err := c.GetStatus(ctx)
	return err == nil
}

// Ping checks if the server is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.GetStatus(ctx)
	return err
}

// GetRecentLogs retrieves recent log entries from the daemon.
// v4.3.2: Used for GUI to display daemon activity.
func (c *Client) GetRecentLogs(ctx context.Context, count int) ([]LogEntryData, error) {
	req := NewRequest(MsgGetRecentLogs)
	// Note: count is not sent in current protocol - server uses default
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("server error: %s", resp.Error)
	}

	// Extract log entries from response
	if resp.Data == nil {
		return []LogEntryData{}, nil
	}

	// Handle the data extraction
	switch v := resp.Data.(type) {
	case *RecentLogsData:
		return v.Entries, nil
	case map[string]interface{}:
		// Re-marshal and unmarshal to convert
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var logs RecentLogsData
		if err := json.Unmarshal(data, &logs); err != nil {
			return nil, err
		}
		return logs.Entries, nil
	}
	return []LogEntryData{}, nil
}
