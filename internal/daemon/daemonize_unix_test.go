//go:build !windows

package daemon

import (
	"os"
	"testing"
)

// A claim takes nothing over without evidence that no daemon runs. A probe the
// system refuses is no such evidence: kill answers EPERM for a live process of
// another user, and taking that for "gone" deleted a running daemon's PID file,
// so a second daemon started. Nor is a PID file this process cannot read.
func TestWritePIDFile_KeepsAClaimItCannotDisprove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may signal every process and read every file")
	}
	isolateHome(t)
	writeFile(t, PIDFilePath(), "1") // init: running, and not ours

	if got := IsDaemonRunning(); got != 1 {
		t.Errorf("a PID file naming PID 1 reads as daemon %d, want 1", got)
	}
	if err := WritePIDFile(); err == nil {
		t.Error("a daemon claimed the PID file of PID 1, which is running")
	}
	RemovePIDFile() // a daemon's exit removes its own claim only
	if got := ReadPIDFile(); got != 1 {
		t.Errorf("the PID file names %d now, want 1", got)
	}
	if err := os.Chmod(PIDFilePath(), 0); err != nil {
		t.Fatal(err)
	}
	if err := WritePIDFile(); err == nil {
		t.Error("a daemon claimed a PID file it could not read, which names PID 1")
	}
}
