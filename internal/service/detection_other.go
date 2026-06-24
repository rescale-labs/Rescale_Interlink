//go:build !windows

// Package service provides platform-specific daemon detection.
// This file provides implementations for non-Windows platforms.
package service

import "github.com/rescale/rescale-int/internal/daemon"

// ServiceDetectionResult describes the current daemon state.
type ServiceDetectionResult struct {
	SubprocessPID int
	PipeInUse     bool
	Error         string
}

// DetectDaemon reports whether a user daemon subprocess is running.
func DetectDaemon() ServiceDetectionResult {
	result := ServiceDetectionResult{}
	if pid := daemon.IsDaemonRunning(); pid != 0 {
		result.SubprocessPID = pid
	}
	return result
}

// ShouldBlockSubprocess returns true if a daemon subprocess is already
// running for this user.
func ShouldBlockSubprocess() (bool, string) {
	if d := DetectDaemon(); d.SubprocessPID > 0 {
		return true, "Daemon already running"
	}
	return false, ""
}

// IsLegacyServiceInstalled is always false on non-Windows platforms.
func IsLegacyServiceInstalled() bool { return false }

// UninstallLegacyService is a no-op on non-Windows platforms.
func UninstallLegacyService() error { return nil }
