//go:build windows

package ipc

import (
	"syscall"
	"testing"
)

func TestIsPipeInUse_NoPipe(t *testing.T) {
	// Ensure no daemon is running by checking if pipe exists
	// If another test left a daemon running, skip this test
	if IsPipeInUse() {
		t.Skip("Pipe is in use (daemon may be running), cannot test absent pipe scenario")
	}

	// Test should return false for absent pipe
	if IsPipeInUse() {
		t.Error("IsPipeInUse should return false when no pipe exists")
	}
}

func TestWindowsErrorCodes(t *testing.T) {
	// Verify our constants match Windows error codes
	if ERROR_FILE_NOT_FOUND != syscall.Errno(2) {
		t.Errorf("ERROR_FILE_NOT_FOUND mismatch: got %d, want 2", ERROR_FILE_NOT_FOUND)
	}
	if ERROR_PIPE_BUSY != syscall.Errno(231) {
		t.Errorf("ERROR_PIPE_BUSY mismatch: got %d, want 231", ERROR_PIPE_BUSY)
	}
	if ERROR_ACCESS_DENIED != syscall.Errno(5) {
		t.Errorf("ERROR_ACCESS_DENIED mismatch: got %d, want 5", ERROR_ACCESS_DENIED)
	}
}

func TestIsPipeInUse_ConsistentResults(t *testing.T) {
	// Call IsPipeInUse multiple times and verify consistent results
	// This helps catch any state-dependent bugs
	result1 := IsPipeInUse()
	result2 := IsPipeInUse()
	result3 := IsPipeInUse()

	if result1 != result2 || result2 != result3 {
		t.Errorf("IsPipeInUse returned inconsistent results: %v, %v, %v", result1, result2, result3)
	}
}
