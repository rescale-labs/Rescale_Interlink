//go:build windows

// Package service provides Windows Service Control Manager integration.
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// UserProfile represents a Windows user profile for multi-user auto-download.
type UserProfile struct {
	// SID is the Windows Security Identifier for the user
	SID string

	// Username is the user's account name (may be empty if lookup fails)
	Username string

	// ProfilePath is the full path to the user's profile directory (e.g., C:\Users\john)
	ProfilePath string

	// ConfigPath is the path to the user's daemon.conf file (v4.2.0+)
	ConfigPath string

	// StateFilePath is the path to the user's autodownload state file
	StateFilePath string
}

// SystemProfiles are Windows profile directories that should be excluded from enumeration.
// These are system accounts, not real user accounts.
var SystemProfiles = []string{
	"Public",
	"Default",
	"Default User",
	"All Users",
	"systemprofile",        // SYSTEM account
	"LocalService",         // Local Service account
	"NetworkService",       // Network Service account
	"defaultuser0",         // Windows default user template
	"defaultuser100001",    // Additional system profiles
	".NET v2.0",            // .NET profile
	".NET v2.0 Classic",    // .NET profile
	".NET v4.5",            // .NET profile
	".NET v4.5 Classic",    // .NET profile
}

// EnumerateUserProfiles scans the system for user profiles with valid daemon.conf files.
// It returns profiles for users who have auto-download enabled and properly configured.
//
// The function:
// 1. Reads the ProfileList from the Windows registry
// 2. Filters out system accounts (Public, Default, service accounts, etc.)
// 3. Checks each profile for a valid daemon.conf file with auto-download enabled
//
// This is used by the Windows service to process downloads for all users on the machine.
// v4.2.0: Updated to use daemon.conf instead of apiconfig.
func EnumerateUserProfiles() ([]UserProfile, error) {
	profiles, err := enumerateFromRegistry()
	if err != nil {
		// Fall back to filesystem enumeration if registry fails
		profiles, err = enumerateFromFilesystem()
		if err != nil {
			return nil, fmt.Errorf("failed to enumerate user profiles: %w", err)
		}
	}

	// Filter for profiles with valid daemon.conf
	var validProfiles []UserProfile
	for _, profile := range profiles {
		if profile.ConfigPath == "" {
			continue
		}
		// Check if daemon.conf file exists
		if _, err := os.Stat(profile.ConfigPath); err == nil {
			validProfiles = append(validProfiles, profile)
		}
	}

	return validProfiles, nil
}

// enumerateFromRegistry reads user profiles from the Windows registry.
// This is the preferred method as it's the authoritative source.
func enumerateFromRegistry() ([]UserProfile, error) {
	// Open the ProfileList key in the registry
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList`,
		registry.READ|registry.ENUMERATE_SUB_KEYS,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to open ProfileList registry key: %w", err)
	}
	defer key.Close()

	// Enumerate all subkeys (each subkey is a SID)
	subkeys, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("failed to read profile subkeys: %w", err)
	}

	var profiles []UserProfile
	for _, sid := range subkeys {
		// Skip short SIDs (system accounts like S-1-5-18, S-1-5-19, S-1-5-20)
		if len(sid) < 20 || !strings.HasPrefix(sid, "S-1-5-21-") {
			continue
		}

		// Open the SID subkey to get profile path
		sidKey, err := registry.OpenKey(key, sid, registry.READ)
		if err != nil {
			continue // Skip profiles we can't read
		}

		profilePath, _, err := sidKey.GetStringValue("ProfileImagePath")
		sidKey.Close()
		if err != nil {
			continue // Skip profiles without a path
		}

		// Expand environment variables in path
		profilePath = os.ExpandEnv(profilePath)

		// Check if this is a system profile
		profileName := filepath.Base(profilePath)
		if isSystemProfile(profileName) {
			continue
		}

		// Verify the profile directory exists
		if _, err := os.Stat(profilePath); os.IsNotExist(err) {
			continue
		}

		// Build the profile entry
		// v4.2.0: Use daemon.conf instead of apiconfig
		profile := UserProfile{
			SID:           sid,
			Username:      profileName,
			ProfilePath:   profilePath,
			ConfigPath:    filepath.Join(profilePath, ".config", "rescale", "daemon.conf"),
			StateFilePath: filepath.Join(profilePath, ".config", "rescale", "autodownload_state.json"),
		}

		profiles = append(profiles, profile)
	}

	return profiles, nil
}

// enumerateFromFilesystem scans C:\Users for user profiles.
// This is a fallback if registry access fails.
func enumerateFromFilesystem() ([]UserProfile, error) {
	usersDir := os.Getenv("PUBLIC")
	if usersDir == "" {
		usersDir = `C:\Users`
	} else {
		// PUBLIC is typically C:\Users\Public, so go up one level
		usersDir = filepath.Dir(usersDir)
	}

	entries, err := os.ReadDir(usersDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read users directory: %w", err)
	}

	var profiles []UserProfile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()

		// Skip system profiles
		if isSystemProfile(name) {
			continue
		}

		profilePath := filepath.Join(usersDir, name)

		// Build profile entry (SID unknown in filesystem mode)
		// v4.2.0: Use daemon.conf instead of apiconfig
		profile := UserProfile{
			SID:           "",
			Username:      name,
			ProfilePath:   profilePath,
			ConfigPath:    filepath.Join(profilePath, ".config", "rescale", "daemon.conf"),
			StateFilePath: filepath.Join(profilePath, ".config", "rescale", "autodownload_state.json"),
		}

		profiles = append(profiles, profile)
	}

	return profiles, nil
}

// isSystemProfile checks if a profile name is a system account that should be skipped.
func isSystemProfile(name string) bool {
	nameLower := strings.ToLower(name)
	for _, sys := range SystemProfiles {
		if strings.EqualFold(name, sys) || strings.ToLower(sys) == nameLower {
			return true
		}
	}
	// Also skip profiles starting with a dot (hidden/system)
	if strings.HasPrefix(name, ".") {
		return true
	}
	// Skip TEMP profiles (created during failed profile loads)
	if strings.HasPrefix(strings.ToUpper(name), "TEMP") {
		return true
	}
	return false
}

// GetCurrentUserProfile returns the profile for the currently logged-in user.
// Useful for testing and single-user mode.
func GetCurrentUserProfile() (*UserProfile, error) {
	userProfile := os.Getenv("USERPROFILE")
	if userProfile == "" {
		return nil, fmt.Errorf("USERPROFILE environment variable not set")
	}

	// v4.2.0: Use daemon.conf instead of apiconfig
	return &UserProfile{
		SID:           "", // Could use syscall to get, but not necessary
		Username:      filepath.Base(userProfile),
		ProfilePath:   userProfile,
		ConfigPath:    filepath.Join(userProfile, ".config", "rescale", "daemon.conf"),
		StateFilePath: filepath.Join(userProfile, ".config", "rescale", "autodownload_state.json"),
	}, nil
}
