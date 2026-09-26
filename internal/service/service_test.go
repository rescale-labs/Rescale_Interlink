package service

import (
	"testing"
)

func TestStatusString(t *testing.T) {
	tests := []struct {
		status   Status
		expected string
	}{
		{StatusUnknown, "Unknown"},
		{StatusStopped, "Stopped"},
		{StatusStartPending, "Start Pending"},
		{StatusStopPending, "Stop Pending"},
		{StatusRunning, "Running"},
		{StatusContinuePending, "Continue Pending"},
		{StatusPausePending, "Pause Pending"},
		{StatusPaused, "Paused"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.status.String(); got != tt.expected {
				t.Errorf("Status.String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestServiceConstants(t *testing.T) {
	// Verify constants are set
	if ServiceName == "" {
		t.Error("ServiceName should not be empty")
	}
	if ServiceDisplayName == "" {
		t.Error("ServiceDisplayName should not be empty")
	}
}

func TestIsWindowsService(t *testing.T) {
	// On non-Windows, this should return false, nil
	// On Windows (not running as service), this should return false, nil
	isService, err := IsWindowsService()
	if isService {
		t.Error("IsWindowsService() should return false when not running as service")
	}
	// Error may or may not be nil depending on platform
	_ = err
}
