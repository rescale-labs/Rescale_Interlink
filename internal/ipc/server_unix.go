//go:build !windows

// Package ipc provides inter-process communication stubs for non-Windows platforms.
package ipc

import (
	"errors"

	"github.com/rescale/rescale-int/internal/logging"
)

// ErrNotSupported is returned when IPC operations are called on non-Windows platforms.
var ErrNotSupported = errors.New("IPC via named pipes is only supported on Windows")

// ServiceHandler defines the interface for service operations.
type ServiceHandler interface {
	GetStatus() *StatusData
	GetUserList() []UserStatus
	PauseUser(userID string) error
	ResumeUser(userID string) error
	TriggerScan(userID string) error
	OpenLogs(userID string) error
}

// Server is a stub for non-Windows platforms.
type Server struct{}

// NewServer creates a new IPC server stub.
func NewServer(handler ServiceHandler, logger *logging.Logger) *Server {
	return &Server{}
}

// Start returns an error on non-Windows platforms.
func (s *Server) Start() error {
	return ErrNotSupported
}

// Stop is a no-op on non-Windows platforms.
func (s *Server) Stop() {}
