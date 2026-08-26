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
	if ServiceDescription == "" {
		t.Error("ServiceDescription should not be empty")
	}
}

func TestGetExecutablePath(t *testing.T) {
	path, err := GetExecutablePath()
	if err != nil {
		t.Errorf("GetExecutablePath() error = %v", err)
		return
	}
	if path == "" {
		t.Error("GetExecutablePath() returned empty path")
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

// =============================================================================
// Multi-User Support Tests (D2.5)
// =============================================================================

func TestGetCurrentUserProfile(t *testing.T) {
	profile, err := GetCurrentUserProfile()
	if err != nil {
		t.Fatalf("GetCurrentUserProfile() error = %v", err)
	}

	// Profile should have basic fields set
	if profile.ProfilePath == "" {
		t.Error("ProfilePath should not be empty")
	}
	if profile.ConfigPath == "" {
		t.Error("ConfigPath should not be empty")
	}
	if profile.StateFilePath == "" {
		t.Error("StateFilePath should not be empty")
	}

	// ConfigPath should be under ProfilePath
	if len(profile.ConfigPath) <= len(profile.ProfilePath) {
		t.Error("ConfigPath should be a path under ProfilePath")
	}

	// StateFilePath should be under ProfilePath
	if len(profile.StateFilePath) <= len(profile.ProfilePath) {
		t.Error("StateFilePath should be a path under ProfilePath")
	}
}

func TestEnumerateUserProfiles(t *testing.T) {
	// On Unix, this should return 0-1 profiles (current user only, if they have config)
	// On Windows, this would return all configured users
	profiles, err := EnumerateUserProfiles()
	if err != nil {
		t.Fatalf("EnumerateUserProfiles() error = %v", err)
	}

	// Should not error even if no profiles are configured
	// (just returns empty list)
	_ = profiles

	// If we got profiles, verify they have required fields
	for _, p := range profiles {
		if p.ProfilePath == "" {
			t.Error("Profile should have ProfilePath set")
		}
		if p.ConfigPath == "" {
			t.Error("Profile should have ConfigPath set")
		}
	}
}
