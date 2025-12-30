// Package service provides Windows Service Control Manager integration.
// On non-Windows platforms, it provides stub implementations that allow
// the daemon to run as a regular process.
package service

import (
	"context"

	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/logging"
)

// ServiceName is the Windows service name.
const ServiceName = "RescaleInterlink"

// ServiceDisplayName is the human-readable service name.
const ServiceDisplayName = "Rescale Interlink Auto-Download Service"

// ServiceDescription describes the service.
const ServiceDescription = "Automatically downloads output files from completed Rescale jobs."

// Status represents the current service status.
type Status int

const (
	StatusUnknown Status = iota
	StatusStopped
	StatusStartPending
	StatusStopPending
	StatusRunning
	StatusContinuePending
	StatusPausePending
	StatusPaused
)

// String returns the status name.
func (s Status) String() string {
	switch s {
	case StatusStopped:
		return "Stopped"
	case StatusStartPending:
		return "Start Pending"
	case StatusStopPending:
		return "Stop Pending"
	case StatusRunning:
		return "Running"
	case StatusContinuePending:
		return "Continue Pending"
	case StatusPausePending:
		return "Pause Pending"
	case StatusPaused:
		return "Paused"
	default:
		return "Unknown"
	}
}

// Service wraps the daemon for service management.
// This is the single-user variant, used when running as a regular process.
type Service struct {
	daemon *daemon.Daemon
	logger *logging.Logger
	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a new service wrapper.
func New(d *daemon.Daemon, logger *logging.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		daemon: d,
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start starts the service.
func (s *Service) Start() error {
	return s.daemon.Start(s.ctx)
}

// Stop stops the service.
func (s *Service) Stop() {
	s.cancel()
	s.daemon.Stop()
}

// MultiUserService wraps the MultiUserDaemon for Windows service management.
// This is the preferred variant for the Windows service, supporting multiple user profiles.
type MultiUserService struct {
	daemon *MultiUserDaemon
	logger *logging.Logger
}

// NewMultiUserService creates a new multi-user service wrapper.
func NewMultiUserService(logger *logging.Logger) *MultiUserService {
	return &MultiUserService{
		daemon: NewMultiUserDaemon(logger),
		logger: logger,
	}
}

// Start starts the multi-user service.
func (s *MultiUserService) Start() error {
	return s.daemon.Start()
}

// Stop stops the multi-user service.
func (s *MultiUserService) Stop() {
	s.daemon.Stop()
}

// GetStatus returns the status of all user daemons.
func (s *MultiUserService) GetStatus() []UserDaemonStatus {
	return s.daemon.GetStatus()
}

// RunningCount returns the number of currently running user daemons.
func (s *MultiUserService) RunningCount() int {
	return s.daemon.RunningCount()
}

// TriggerRescan forces an immediate rescan of user profiles.
func (s *MultiUserService) TriggerRescan() {
	s.daemon.TriggerRescan()
}

// PauseUser pauses auto-download for a specific user.
func (s *MultiUserService) PauseUser(identifier string) error {
	return s.daemon.PauseUser(identifier)
}

// ResumeUser resumes auto-download for a specific user.
func (s *MultiUserService) ResumeUser(identifier string) error {
	return s.daemon.ResumeUser(identifier)
}
