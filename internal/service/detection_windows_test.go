//go:build windows

package service

import (
	"testing"
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
