//go:build windows

// Package service provides subprocess daemon detection and legacy Windows
// service cleanup.
package service

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
)

// legacyServiceName is the name of the Windows service installed by older
// Interlink versions. We no longer create it, but we still remove it on
// upgrade so a stale SYSTEM service does not keep running.
const legacyServiceName = "RescaleInterlink"

// debugLog logs only when RESCALE_DEBUG is set (non-production debugging)
func debugLog(format string, args ...interface{}) {
	if os.Getenv("RESCALE_DEBUG") != "" {
		fmt.Printf("[DetectDaemon] "+format+"\n", args...)
	}
}

// ServiceDetectionResult describes the current daemon state.
type ServiceDetectionResult struct {
	SubprocessPID int    // PID if subprocess daemon is running
	PipeInUse     bool   // True if named pipe exists
	Error         string // Error message if detection failed
}

// DetectDaemon determines whether a user-session daemon subprocess is running.
func DetectDaemon() ServiceDetectionResult {
	result := ServiceDetectionResult{}

	if pid := daemon.IsDaemonRunning(); pid != 0 {
		result.SubprocessPID = pid
		debugLog("Result: SubprocessPID=%d", pid)
		return result
	}

	if ipc.IsPipeInUse() {
		result.PipeInUse = true
		result.Error = "Daemon appears to be running but not responding (pipe exists)"
		debugLog("Result: PipeInUse=true")
	}

	debugLog("Result: No daemon detected")
	return result
}

// ShouldBlockSubprocess returns true if a new daemon subprocess should not be
// started because one is already running for this user.
func ShouldBlockSubprocess() (bool, string) {
	d := DetectDaemon()
	if d.SubprocessPID > 0 {
		return true, fmt.Sprintf("Daemon already running (PID %d)", d.SubprocessPID)
	}
	if d.PipeInUse {
		return true, d.Error
	}
	return false, ""
}

// IsLegacyServiceInstalled reports whether the old Windows service is still
// registered with the SCM. Returns false when SCM access is denied (we cannot
// determine, so cleanup is skipped).
func IsLegacyServiceInstalled() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()

	s, err := m.OpenService(legacyServiceName)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// UninstallLegacyService stops and removes the legacy Windows service and
// clears its registry marker. Requires administrator privileges. Safe to call
// when the service does not exist (returns nil).
func UninstallLegacyService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(legacyServiceName)
	if err != nil {
		// Service not installed — nothing to do. Still clear any stale marker.
		clearLegacyRegistryMarker()
		return nil
	}
	defer s.Close()

	if status, qErr := s.Query(); qErr == nil && status.State != svc.Stopped {
		if _, cErr := s.Control(svc.Stop); cErr != nil {
			fmt.Printf("Warning: failed to stop legacy service: %v\n", cErr)
		}
		for i := 0; i < 30; i++ {
			status, qErr = s.Query()
			if qErr != nil || status.State == svc.Stopped {
				break
			}
			time.Sleep(time.Second)
		}
	}

	if err := s.Delete(); err != nil {
		return fmt.Errorf("failed to delete legacy service: %w", err)
	}

	if err := eventlog.Remove(legacyServiceName); err != nil {
		// Non-fatal.
		fmt.Printf("Warning: failed to remove event log source: %v\n", err)
	}

	clearLegacyRegistryMarker()
	return nil
}

// clearLegacyRegistryMarker removes the HKLM ServiceInstalled marker set by
// older Interlink versions.
func clearLegacyRegistryMarker() {
	if regKey, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Rescale\Interlink`, registry.SET_VALUE); err == nil {
		regKey.DeleteValue("ServiceInstalled")
		regKey.Close()
	}
}
