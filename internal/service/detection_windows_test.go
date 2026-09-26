//go:build windows

package service

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

func TestDetectDaemon_ReturnTypes(t *testing.T) {
	// Test that DetectDaemon returns valid result structure
	result := DetectDaemon()

	// Result should be a valid struct (not cause any panics)
	t.Logf("DetectDaemon result: ServiceMode=%v, SubprocessPID=%d, PipeInUse=%v, Error=%s",
		result.ServiceMode, result.SubprocessPID, result.PipeInUse, result.Error)

	// These are mutually exclusive states (at most one should be true)
	trueCount := 0
	if result.ServiceMode {
		trueCount++
	}
	if result.SubprocessPID > 0 {
		trueCount++
	}
	if result.PipeInUse && !result.ServiceMode && result.SubprocessPID == 0 {
		trueCount++
	}

	// In a clean state (no daemon), all should be false
	// In an active state, exactly one mode should be detected
	// This is just a sanity check - actual state depends on environment
	if trueCount > 1 {
		t.Errorf("DetectDaemon returned multiple active states: ServiceMode=%v, SubprocessPID=%d, PipeInUse=%v",
			result.ServiceMode, result.SubprocessPID, result.PipeInUse)
	}
}

func TestShouldBlockSubprocess_ReturnFormat(t *testing.T) {
	// Test that ShouldBlockSubprocess returns proper format
	blocked, reason := ShouldBlockSubprocess()

	t.Logf("ShouldBlockSubprocess: blocked=%v, reason=%s", blocked, reason)

	// If blocked, reason should not be empty
	if blocked && reason == "" {
		t.Error("ShouldBlockSubprocess returned blocked=true with empty reason")
	}

	// If not blocked, reason should be empty (normal case)
	if !blocked && reason != "" {
		t.Errorf("ShouldBlockSubprocess returned blocked=false with non-empty reason: %s", reason)
	}
}

func TestShouldBlockSubprocess_ConsistentResults(t *testing.T) {
	// Call multiple times and verify consistent results
	blocked1, reason1 := ShouldBlockSubprocess()
	blocked2, reason2 := ShouldBlockSubprocess()

	if blocked1 != blocked2 {
		t.Errorf("ShouldBlockSubprocess returned inconsistent blocked: %v, %v", blocked1, blocked2)
	}

	// Reasons should match (accounting for potential timing differences in error messages)
	if (reason1 == "") != (reason2 == "") {
		t.Errorf("ShouldBlockSubprocess returned inconsistent reason presence: %q, %q", reason1, reason2)
	}
}

func TestDetectDaemon_NoService(t *testing.T) {
	// Only run when we can confirm no service installed
	installed, _ := IsInstalledWithReason()
	if installed {
		t.Skip("Service is installed, cannot test no-service scenario")
	}

	result := DetectDaemon()
	if result.ServiceMode {
		t.Error("DetectDaemon reported ServiceMode=true when no service is installed")
	}
}

func TestIsInstalledWithReason_ReturnsReason(t *testing.T) {
	// Test that IsInstalledWithReason returns proper reason format
	installed, reason := IsInstalledWithReason()

	t.Logf("IsInstalledWithReason: installed=%v, reason=%s", installed, reason)

	// If not installed, reason should explain why (unless access was denied)
	// This is just logging for diagnostic purposes
	if !installed {
		t.Logf("Service not installed, reason: %s", reason)
	}
}

// A service installed by an earlier version blocks a per-user daemon only while
// it runs; installed and stopped, which is all it can be now, it blocks none.
// Whether the service is installed is told apart from not being allowed to ask.
func TestServiceStateDecidesBlocking(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir()) // no per-user daemon's PID file
	orig := scmQuery
	t.Cleanup(func() { scmQuery = orig })
	for _, tc := range []struct {
		name             string
		state            svc.State
		err              error
		installed        bool
		reason, blockMsg string
	}{
		{"not installed", 0, windows.ERROR_SERVICE_DOES_NOT_EXIST, false, "", ""},
		{"installed and stopped", svc.Stopped, nil, true, "", ""},
		{"installed and running", svc.Running, nil, true, "", OldServiceRunning},
		{"not allowed to ask", 0, windows.ERROR_ACCESS_DENIED, false, "denied", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scmQuery = func() (svc.State, error) { return tc.state, tc.err }
			installed, reason := IsInstalledWithReason()
			if installed != tc.installed || (tc.reason == "") != (reason == "") || !strings.Contains(reason, tc.reason) {
				t.Errorf("IsInstalledWithReason = (%v, %q), want (%v, containing %q)", installed, reason, tc.installed, tc.reason)
			}
			blocked, why := ShouldBlockSubprocess()
			if blocked != (tc.blockMsg != "") || !strings.Contains(why, tc.blockMsg) {
				t.Errorf("ShouldBlockSubprocess = (%v, %q), want it to block only for %q", blocked, why, tc.blockMsg)
			}
		})
	}
}

// A daemon from an earlier version listens where this version no longer looks,
// so the app cannot stop it; the refusal says what can: 'daemon stop --force',
// which ends it by its process, or Task Manager.
func TestBlockSubprocess_SaysHowToStopTheRunningDaemon(t *testing.T) {
	blocked, reason := blockSubprocess(ServiceDetectionResult{SubprocessPID: 1234})
	for _, want := range []string{"PID 1234", "'rescale-int daemon stop --force'", "Task Manager"} {
		if !blocked || !strings.Contains(reason, want) {
			t.Errorf("blockSubprocess = %v, %q; want a refusal naming %q", blocked, reason, want)
		}
	}
}
