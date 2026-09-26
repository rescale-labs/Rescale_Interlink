// Package service provides Windows Service Control Manager integration.
// On non-Windows platforms, it provides stub implementations that allow
// the daemon to run as a regular process.
package service

// ServiceName is the Windows service name.
const ServiceName = "RescaleInterlink"

// ServiceDisplayName is the human-readable service name.
const ServiceDisplayName = "Rescale Interlink Auto-Download Service"

// ModeUnavailable says why the service can no longer be installed or started,
// and where auto-download runs instead.
const ModeUnavailable = "Multi-user service mode is not available in this version. Auto-download runs in each user's session from the Interlink app."

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
