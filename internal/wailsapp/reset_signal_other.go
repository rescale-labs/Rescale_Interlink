//go:build !linux

package wailsapp

// resetLinuxSignalHandlers is a no-op on non-Linux platforms. See the Linux
// build for the rationale.
func resetLinuxSignalHandlers() {}
