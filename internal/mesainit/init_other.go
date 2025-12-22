//go:build !windows

// Package mesainit provides early Mesa initialization for Windows.
// On non-Windows platforms, this is a no-op.
package mesainit

// init is empty on non-Windows platforms.
// Mesa software rendering is only needed on Windows where GPU
// drivers may not be available in headless/VM environments.
func init() {}
