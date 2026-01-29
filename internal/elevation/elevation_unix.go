//go:build !windows

// Package elevation provides UAC elevation support for Windows.
// v4.5.1: On non-Windows platforms, these functions return errors.
package elevation

import (
	"errors"
)

// ErrNotSupported is returned when elevation is attempted on non-Windows platforms.
var ErrNotSupported = errors.New("UAC elevation is only supported on Windows")

// RunElevated is not supported on non-Windows platforms.
func RunElevated(executable string, args string, workingDir string) error {
	return ErrNotSupported
}

// StartServiceElevated is not supported on non-Windows platforms.
func StartServiceElevated() error {
	return ErrNotSupported
}

// StopServiceElevated is not supported on non-Windows platforms.
func StopServiceElevated() error {
	return ErrNotSupported
}
