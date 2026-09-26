//go:build !windows

// Package service provides stub implementations for non-Windows platforms.
package service

import "errors"

// ErrNotSupported is returned when service operations are called on non-Windows platforms.
var ErrNotSupported = errors.New("windows service operations are not supported on this platform")

// RunDisabled does nothing: only Windows has the service, so no SCM started
// this process.
func RunDisabled() bool { return false }

// IsWindowsService always returns false on non-Windows platforms.
func IsWindowsService() (bool, error) {
	return false, nil
}

// IsInstalled always returns false on non-Windows platforms.
func IsInstalled() bool {
	return false
}

// Uninstall is not supported on non-Windows platforms.
func Uninstall() error {
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
