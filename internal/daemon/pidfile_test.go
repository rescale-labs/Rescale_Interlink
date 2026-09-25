package daemon

import (
	"os"
	"strconv"
	"testing"
)

// Two daemons starting at once claim the PID file once, however they are timed.
// Here the first, which stands for the parent process, stalls between writing
// its claim and publishing it, where the second used to find an empty file,
// take it for no daemon and replace it, so both ran. The second must wait for
// the first and then find it running.
func TestWritePIDFile_TwoConcurrentStartsClaimOnce(t *testing.T) {
	isolateHome(t)
	second := stall(t, &pidClaimStep, func() error {
		// The first claim, written and not yet published, is the parent's.
		if err := os.WriteFile(PIDFilePath()+".tmp", []byte(strconv.Itoa(os.Getppid())), 0o600); err != nil {
			return err
		}
		return WritePIDFile()
	})
	if err := WritePIDFile(); err != nil {
		t.Fatalf("the first claim: %v", err)
	}
	if err := <-second; err == nil {
		t.Error("the second claim succeeded while the first daemon ran")
	}
	if got := ReadPIDFile(); got != os.Getppid() {
		t.Errorf("the PID file names %d, want the first daemon (%d)", got, os.Getppid())
	}
}

// A claim takes over a PID file that names no running daemon, or a crash would
// lock the daemon out for good: a corrupt one, or one naming this very process,
// which an earlier process with the same PID left, as a restarted container's
// daemon finds. It also clears the PID file of a version that kept it under
// %APPDATA% on Windows.
func TestWritePIDFile_TakesOverAFileNamingNoDaemon(t *testing.T) {
	isolateHome(t)
	legacy := oldPIDFilePath()
	if legacy != "" {
		writeFile(t, legacy, "1")
	}
	for _, stale := range []string{"", "not a PID", strconv.Itoa(os.Getpid())} {
		writeFile(t, PIDFilePath(), stale)
		if err := WritePIDFile(); err != nil {
			t.Errorf("a claim over a PID file holding %q: %v", stale, err)
		}
	}
	if _, err := os.Stat(legacy); legacy != "" && !os.IsNotExist(err) {
		t.Errorf("the claim left the earlier version's PID file %s (stat: %v)", legacy, err)
	}
}
