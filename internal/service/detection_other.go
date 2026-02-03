//go:build !windows

// Package service provides platform-specific service detection.
// This file provides stub implementations for non-Windows platforms.
package service

// ServiceDetectionResult describes the current service state.
// On non-Windows platforms, this is a stub.
type ServiceDetectionResult struct {
	ServiceMode   bool
	SubprocessPID int
	PipeInUse     bool
	Error         string
}

// DetectDaemon is a stub on non-Windows platforms.
// Windows service detection is not applicable.
func DetectDaemon() ServiceDetectionResult {
	return ServiceDetectionResult{}
}

// ShouldBlockSubprocess is a stub on non-Windows platforms.
// Always returns (false, "") as Windows Service detection is not applicable.
func ShouldBlockSubprocess() (bool, string) {
	return false, ""
}
