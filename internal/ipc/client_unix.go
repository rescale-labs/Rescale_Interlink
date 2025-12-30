//go:build !windows

// Package ipc provides inter-process communication stubs for non-Windows platforms.
package ipc

import (
	"context"
	"time"
)

// Client is a stub for non-Windows platforms.
type Client struct{}

// NewClient creates a new IPC client stub.
func NewClient() *Client {
	return &Client{}
}

// SetTimeout is a no-op on non-Windows platforms.
func (c *Client) SetTimeout(timeout time.Duration) {}

// GetStatus returns an error on non-Windows platforms.
func (c *Client) GetStatus(ctx context.Context) (*StatusData, error) {
	return nil, ErrNotSupported
}

// GetUserList returns an error on non-Windows platforms.
func (c *Client) GetUserList(ctx context.Context) ([]UserStatus, error) {
	return nil, ErrNotSupported
}

// PauseUser returns an error on non-Windows platforms.
func (c *Client) PauseUser(ctx context.Context, userID string) error {
	return ErrNotSupported
}

// ResumeUser returns an error on non-Windows platforms.
func (c *Client) ResumeUser(ctx context.Context, userID string) error {
	return ErrNotSupported
}

// TriggerScan returns an error on non-Windows platforms.
func (c *Client) TriggerScan(ctx context.Context, userID string) error {
	return ErrNotSupported
}

// OpenLogs returns an error on non-Windows platforms.
func (c *Client) OpenLogs(ctx context.Context, userID string) error {
	return ErrNotSupported
}

// IsServiceRunning always returns false on non-Windows platforms.
func (c *Client) IsServiceRunning(ctx context.Context) bool {
	return false
}

// Ping returns an error on non-Windows platforms.
func (c *Client) Ping(ctx context.Context) error {
	return ErrNotSupported
}
