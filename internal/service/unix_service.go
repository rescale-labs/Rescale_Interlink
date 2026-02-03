//go:build !windows

// Package service provides stub implementations for non-Windows platforms.
package service

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrNotSupported is returned when service operations are called on non-Windows platforms.
var ErrNotSupported = errors.New("windows service operations are not supported on this platform")

// RunAsService is not supported on non-Windows platforms.
// On Unix-like systems, use systemd, launchd, or similar.
func RunAsService(s *Service) error {
	return ErrNotSupported
}

// RunAsMultiUserService is not supported on non-Windows platforms.
// On Unix-like systems, multi-user is not applicable since each user
// runs their own instance via systemd user service or similar.
func RunAsMultiUserService(s *MultiUserService) error {
	return ErrNotSupported
}

// IsWindowsService always returns false on non-Windows platforms.
func IsWindowsService() (bool, error) {
	return false, nil
}

// IsInstalled always returns false on non-Windows platforms.
// v4.3.6: Added for GUI to check service installation status.
func IsInstalled() bool {
	return false
}

// Install is not supported on non-Windows platforms.
func Install(execPath string, configPath string) error {
	return ErrNotSupported
}

// Uninstall is not supported on non-Windows platforms.
func Uninstall() error {
	return ErrNotSupported
}

// StartService is not supported on non-Windows platforms.
func StartService() error {
	return ErrNotSupported
}

// StopService is not supported on non-Windows platforms.
func StopService() error {
	return ErrNotSupported
}

// QueryStatus always returns StatusStopped on non-Windows platforms.
func QueryStatus() (Status, error) {
	return StatusStopped, ErrNotSupported
}

// GetExecutablePath returns the path to the current executable.
func GetExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(exe)
}
