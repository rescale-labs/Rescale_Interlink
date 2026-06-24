// Package service provides Windows Service Control Manager integration.
// On non-Windows platforms, it provides stub implementations that allow
// the daemon to run as a regular process.
package service

// ServiceName is the Windows service name.
const ServiceName = "RescaleInterlink"

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
