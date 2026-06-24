//go:build windows

// Package service provides Windows Service Control Manager integration.
package service

import (
	"fmt"
	"os"

	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
)

// debugLog logs only when RESCALE_DEBUG is set (non-production debugging)
func debugLog(format string, args ...interface{}) {
	if os.Getenv("RESCALE_DEBUG") != "" {
		fmt.Printf("[DetectDaemon] "+format+"\n", args...)
	}
}

// ServiceDetectionResult describes the current service state.
type ServiceDetectionResult struct {
	Installed     bool   // True if a Windows Service is registered, running or not
	ServiceMode   bool   // True if Windows Service is running
	SubprocessPID int    // PID if subprocess daemon is running
	PipeInUse     bool   // True if named pipe exists
	Error         string // Error message if detection failed
}

// DetectDaemon performs multi-layer detection to determine daemon state.
// Should be used by GUI, CLI, and Tray instead of raw IsInstalled() calls.
func DetectDaemon() ServiceDetectionResult {
	result := ServiceDetectionResult{}
	debugLog("Starting detection...")

	// Layer 1: Try SCM (may require admin)
	installed, reason := IsInstalledWithReason()
	debugLog("SCM: installed=%v, reason=%s", installed, reason)
	result.Installed = installed
	if installed {
		// Windows Service is installed - check if running
		if status, err := QueryStatus(); err == nil && status == StatusRunning {
			result.ServiceMode = true
			debugLog("Result: ServiceMode=true (via SCM)")
			return result
		}
	}

	// Layer 2: Check for subprocess via PID file
	if pid := daemon.IsDaemonRunning(); pid != 0 {
		result.SubprocessPID = pid
		debugLog("Result: SubprocessPID=%d", pid)
		return result
	}

	// Layer 3: Check if pipe exists (daemon may be running but slow)
	if ipc.IsPipeInUse() {
		result.PipeInUse = true
		result.Error = "Daemon appears to be running but not responding (pipe exists)"
		debugLog("Result: PipeInUse=true")
	}

	debugLog("Result: No daemon detected")
	return result
}

// ShouldBlockSubprocess returns true if subprocess spawn should be blocked.
// Returns (blocked, reason). Only blocks when service is RUNNING, not just
// installed — this allows subprocess mode when service is installed but stopped.
func ShouldBlockSubprocess() (bool, string) {
	return blockSubprocess(DetectDaemon())
}

// blockSubprocess is ShouldBlockSubprocess for a detection already made.
func blockSubprocess(d ServiceDetectionResult) (bool, string) {
	if d.ServiceMode {
		return true, OldServiceRunning
	}
	if d.SubprocessPID > 0 {
		return true, fmt.Sprintf("Auto-download is already running (PID %d). %s", d.SubprocessPID, EarlierDaemonRunning)
	}
	if d.PipeInUse {
		return true, d.Error
	}

	return false, ""
}
