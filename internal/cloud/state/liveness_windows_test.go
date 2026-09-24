package state

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// TestProbeProcessLiveness_AnExitedChildStillHeldOpenIsDead probes a process
// that has exited while a handle to it is still open, as a lock owner's can be.
// OpenProcess then succeeds, so only the exit code liveness_windows.go reads can
// say the owner is gone.
func TestProbeProcessLiveness_AnExitedChildStillHeldOpenIsDead(t *testing.T) {
	// The test binary, told to run no tests, exits at once.
	child := exec.Command(os.Args[0], "-test.run=^$")
	if err := child.Start(); err != nil {
		t.Fatalf("start a child: %v", err)
	}
	pid := child.Process.Pid
	held, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatalf("open the child: %v", err)
	}
	defer windows.CloseHandle(held)
	if err := child.Wait(); err != nil {
		t.Fatalf("the child failed: %v", err)
	}

	again, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatalf("the exited child cannot be opened, so the probe would never read its exit code: %v", err)
	}
	windows.CloseHandle(again)

	if liveness, err := probeProcessLiveness(pid); liveness != livenessDead {
		t.Errorf("PID %d, exited and still held open, reads as %s (%v), want dead", pid, livenessName(liveness), err)
	}
}
