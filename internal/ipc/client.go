package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SetTimeout sets the connection timeout.
func (c *Client) SetTimeout(timeout time.Duration) {
	c.timeout = timeout
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

// GetUserList retrieves the daemon's user entry: a daemon serves one user.
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

// PauseUser pauses auto-download. The daemon ignores userID.
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

// ResumeUser resumes auto-download. The daemon ignores userID.
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

// TriggerScan triggers an immediate job scan. The daemon ignores userID.
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

// Shutdown sends a shutdown command to the daemon.
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

// GetRecentLogs retrieves recent log entries from the daemon.
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

// GetTransferStatus retrieves a snapshot of the daemon's transfer queue.
func (c *Client) GetTransferStatus(ctx context.Context) (*DaemonTransferSnapshot, error) {
	req := NewRequest(MsgGetTransferStatus)
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("server error: %s", resp.Error)
	}

	data := resp.GetDaemonTransferSnapshot()
	if data == nil {
		return &DaemonTransferSnapshot{}, nil
	}
	return data, nil
}

// CancelDaemonBatch asks the daemon to cancel all non-terminal tasks in a
// specific batch. The daemon ignores userID.
func (c *Client) CancelDaemonBatch(ctx context.Context, userID, batchID string) error {
	req := NewRequestWithUser(MsgCancelDaemonBatch, userID)
	req.BatchID = batchID
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("server error: %s", resp.Error)
	}
	return nil
}

// CancelDaemonTransfer asks the daemon to cancel a single task.
func (c *Client) CancelDaemonTransfer(ctx context.Context, userID, taskID string) error {
	req := NewRequestWithUser(MsgCancelDaemonTransfer, userID)
	req.TaskID = taskID
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("server error: %s", resp.Error)
	}
	return nil
}

// RetryFailedInDaemonBatch asks the daemon to retry all failed tasks in a batch.
func (c *Client) RetryFailedInDaemonBatch(ctx context.Context, userID, batchID string) error {
	req := NewRequestWithUser(MsgRetryFailedInDaemonBatch, userID)
	req.BatchID = batchID
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("server error: %s", resp.Error)
	}
	return nil
}

// ReloadConfig sends a config reload request to the daemon.
func (c *Client) ReloadConfig(ctx context.Context) (*ReloadConfigData, error) {
	req := NewRequest(MsgReloadConfig)
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("server error: %s", resp.Error)
	}

	return resp.GetReloadConfigData(), nil
}
