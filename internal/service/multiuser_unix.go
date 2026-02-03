//go:build !windows

// Package service provides stub implementations for non-Windows platforms.
package service

import (
	"os"
	"path/filepath"

	"github.com/rescale/rescale-int/internal/config"
)

// UserProfile represents a user profile for multi-user auto-download.
// On non-Windows platforms, this only returns the current user.
type UserProfile struct {
	// SID is the user identifier (on Unix, this is the UID as string)
	SID string

	// Username is the user's account name
	Username string

	// ProfilePath is the full path to the user's home directory
	ProfilePath string

	// ConfigPath is the path to the user's daemon.conf file (v4.2.0+)
	ConfigPath string

	// StateFilePath is the path to the user's autodownload state file
	StateFilePath string
}

// SystemProfiles is empty on Unix - we only return the current user anyway.
var SystemProfiles = []string{}

// EnumerateUserProfiles returns only the current user on non-Windows platforms.
// Multi-user enumeration is a Windows-specific feature for the Windows Service.
// On Unix-like systems, daemon mode runs per-user (e.g., via systemd user service).
func EnumerateUserProfiles() ([]UserProfile, error) {
	profile, err := GetCurrentUserProfile()
	if err != nil {
		return nil, err
	}

	// Only return the profile if it has a config file
	if _, err := os.Stat(profile.ConfigPath); os.IsNotExist(err) {
		return []UserProfile{}, nil
	}

	return []UserProfile{*profile}, nil
}

// GetCurrentUserProfile returns the profile for the current user.
func GetCurrentUserProfile() (*UserProfile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	// Try to get username from environment
	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("LOGNAME")
	}
	if username == "" {
		username = filepath.Base(home)
	}

	// Get UID as a string identifier
	uid := os.Getenv("UID")
	if uid == "" {
		// Could use syscall.Getuid() but it's fine to leave empty
		uid = ""
	}

	// v4.4.3: Use config helpers for consistent paths across platforms
	return &UserProfile{
		SID:           uid,
		Username:      username,
		ProfilePath:   home,
		ConfigPath:    filepath.Join(home, ".config", "rescale", "daemon.conf"), // v4.2.0: daemon.conf instead of apiconfig
		StateFilePath: config.StateFilePathForUser(home),
	}, nil
}

// ResolveSIDToUsername is a no-op on Unix platforms.
// Windows SIDs don't exist on Unix, so this always returns empty string.
// v4.5.3: Added stub for cross-platform compatibility.
func ResolveSIDToUsername(_ string) string {
	return ""
}

// ResolveUsernameToSID is a no-op on Unix platforms.
// Windows SIDs don't exist on Unix, so this always returns empty string.
// v4.5.3: Added stub for cross-platform compatibility.
func ResolveUsernameToSID(_ string) string {
	return ""
}
