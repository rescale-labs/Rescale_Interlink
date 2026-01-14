//go:build windows

// Package ipc provides inter-process communication between the Windows service
// and the GUI/tray application using named pipes.
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
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

// SetTimeout sets the connection timeout.
func (c *Client) SetTimeout(timeout time.Duration) {
	c.timeout = timeout
}

// connect establishes a connection to the named pipe.
func (c *Client) connect(ctx context.Context) (net.Conn, error) {
	// Use context with timeout
	dialCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conn, err := winio.DialPipeContext(dialCtx, PipeName)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IPC server: %w", err)
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

// GetStatus retrieves the current service status.
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

// PauseUser pauses auto-download for a specific user.
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

// ResumeUser resumes auto-download for a specific user.
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
// Pass "all" for userID to scan all users, or a specific user ID.
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
// Pass "service" for service logs, or a user ID for user logs.
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

// IsServiceRunning checks if the IPC server (and thus the service) is running.
func (c *Client) IsServiceRunning(ctx context.Context) bool {
	_, err := c.GetStatus(ctx)
	return err == nil
}

// Ping checks if the server is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.GetStatus(ctx)
	return err
}

// Shutdown sends a shutdown command to the daemon.
// Note: On Windows, service shutdown is typically handled via SCM,
// but this method is provided for API parity with Unix.
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

// GetSocketPath returns the named pipe path (for API compatibility with Unix).
// On Windows, this returns the named pipe path rather than a socket path.
func GetSocketPath() string {
	return PipeName
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
