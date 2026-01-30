//go:build windows

// Package service provides Windows Service Control Manager integration.
package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

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
	ServiceMode   bool   // True if Windows Service is running
	SubprocessPID int    // PID if subprocess daemon is running
	PipeInUse     bool   // True if named pipe exists
	Error         string // Error message if detection failed
}

// DetectDaemon performs multi-layer detection to determine daemon state.
// Should be used by GUI, CLI, and Tray instead of raw IsInstalled() calls.
// v4.5.5: Handles SCM access denied by falling back to IPC and pipe detection.
func DetectDaemon() ServiceDetectionResult {
	result := ServiceDetectionResult{}
	debugLog("Starting detection...")

	// Layer 1: Try SCM (may require admin)
	installed, reason := IsInstalledWithReason()
	debugLog("SCM: installed=%v, reason=%s", installed, reason)
	if installed {
		// Windows Service is installed - check if running
		if status, err := QueryStatus(); err == nil && status == StatusRunning {
			result.ServiceMode = true
			debugLog("Result: ServiceMode=true (via SCM)")
			return result
		}
	}

	// Layer 2: If SCM access denied, check via IPC
	if reason != "" && strings.Contains(strings.ToLower(reason), "denied") {
		debugLog("SCM denied, trying IPC fallback...")
		client := ipc.NewClient()
		client.SetTimeout(5 * time.Second)
		ctx := context.Background()
		if status, err := client.GetStatus(ctx); err == nil {
			if status.ServiceMode {
				result.ServiceMode = true
				debugLog("Result: ServiceMode=true (via IPC)")
				return result
			}
		}
	}

	// Layer 3: Check for subprocess via PID file
	if pid := daemon.IsDaemonRunning(); pid != 0 {
		result.SubprocessPID = pid
		debugLog("Result: SubprocessPID=%d", pid)
		return result
	}

	// Layer 4: Check if pipe exists (daemon may be running but slow)
	if ipc.IsPipeInUse() {
		result.PipeInUse = true
		result.Error = "Daemon appears to be running but not responding (pipe exists)"
		debugLog("Result: PipeInUse=true")
	}

	debugLog("Result: No daemon detected")
	return result
}

// ShouldBlockSubprocess returns true if subprocess spawn should be blocked.
// Returns (blocked, reason).
// v4.5.5: Only blocks when service is RUNNING, not just installed.
// This allows subprocess mode when service is installed but stopped.
func ShouldBlockSubprocess() (bool, string) {
	d := DetectDaemon()
	if d.ServiceMode {
		return true, "Windows Service is running. Manage via Services.msc"
	}
	if d.SubprocessPID > 0 {
		return true, fmt.Sprintf("Daemon already running (PID %d)", d.SubprocessPID)
	}
	if d.PipeInUse {
		return true, d.Error
	}

	// v4.5.5: Check if service is installed but stopped - warn but don't block
	if installed, _ := IsInstalledWithReason(); installed {
		if status, err := QueryStatus(); err == nil && status != StatusRunning {
			// Service is installed but not running - allow subprocess but log warning
			debugLog("Warning: Service installed but stopped (status=%s). Allowing subprocess, but service may start later.", status.String())
			daemon.WriteStartupLog("WARNING: Windows Service is installed but stopped. Subprocess allowed, but service may start later and cause conflicts.")
		}
	}

	return false, ""
}
