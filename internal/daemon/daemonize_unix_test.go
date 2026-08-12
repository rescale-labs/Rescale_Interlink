//go:build !windows

package daemon

import (
	"os"
	"strconv"
	"testing"
)

// The PID file is the only thing stopping two daemons from polling the same jobs
// into the same folder, so claiming it has to be exclusive — a plain write let
// both racers succeed. Stale or corrupt files must still be recoverable, or a
// crash would lock the daemon out permanently.
func TestWritePIDFileIsExclusive(t *testing.T) {
	// PIDFilePath resolves under the home directory; redirect it so the test
	// cannot touch a real daemon's PID file.
	t.Setenv("HOME", t.TempDir())
	pidPath := PIDFilePath()

	if err := WritePIDFile(); err != nil {
		t.Fatalf("first WritePIDFile failed: %v", err)
	}
	t.Cleanup(RemovePIDFile)

	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("reading PID file: %v", err)
	}
	if got, _ := strconv.Atoi(string(data)); got != os.Getpid() {
		t.Errorf("PID file contains %q, want %d", data, os.Getpid())
	}

	// The holder is this process, which is very much alive.
	if err := WritePIDFile(); err == nil {
		t.Error("second WritePIDFile succeeded; the claim is not exclusive")
	}

	// A corrupt file reads as "no daemon" and must not lock the daemon out.
	if err := os.WriteFile(pidPath, []byte(""), 0o600); err != nil {
		t.Fatalf("truncating PID file: %v", err)
	}
	if err := WritePIDFile(); err != nil {
		t.Errorf("WritePIDFile over a corrupt PID file failed: %v", err)
	}

	RemovePIDFile()
	if err := WritePIDFile(); err != nil {
		t.Errorf("WritePIDFile after RemovePIDFile failed: %v", err)
	}
}
