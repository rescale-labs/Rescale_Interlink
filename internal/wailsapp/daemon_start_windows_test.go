//go:build windows

package wailsapp

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
)

// A daemon that stops as it starts says why in its captured stderr: Start
// reports Cobra's "Error:" line, not the usage text after it, or else the
// last lines. daemon.TestStartSaysWhyItCannotStart covers the refusals.
func TestStartDaemonSaysWhyItCannotStart(t *testing.T) {
	const why = "Error: max_concurrent in daemon.conf must be between 1 and 20, got 50"
	for stderr, want := range map[string]string{
		why + "\r\nUsage:\r\n  rescale-int daemon run [flags]\r\n\r\nFlags:\r\n  -v, --verbose   verbose\r\n": why,
		"one\ntwo\n\nthree\nfour\n": "two | three | four",
	} {
		if got := childStderr(stderr); got != want {
			t.Errorf("childStderr(%q) = %q, want %q", stderr, got, want)
		}
	}
}

// Stop names a daemon whose PID file names a live process that does not
// answer, and how to end it, instead of reporting it stopped.
func TestStopDaemonNamesADaemonItCannotReach(t *testing.T) {
	setIsolatedUserConfigEnv(t) // WritePIDFile also removes an earlier version's PID file
	if ipc.IsPipeInUse() {
		t.Skip("this user's daemon answers on its pipe")
	}
	if err := daemon.WritePIDFile(); err != nil { // this test's process stands in
		t.Fatal(err)
	}
	t.Cleanup(daemon.RemovePIDFile)
	err := (&App{}).StopDaemon()
	if want := fmt.Sprintf("PID %d", os.Getpid()); err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "Task Manager") {
		t.Errorf("StopDaemon: %v, want it to name %s and how to end it", err, want)
	}
}
