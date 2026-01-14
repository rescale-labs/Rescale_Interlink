// +build !windows

// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
// This file provides a no-op implementation of single-instance checking
// for non-Windows platforms (macOS, Linux).
package wailsapp

// EnsureSingleInstance on non-Windows platforms always returns true
// since single-instance enforcement is only implemented for Windows.
// macOS handles this naturally via app bundles, and Linux users
// typically manage window focus themselves.
func EnsureSingleInstance() bool {
	return true
}
