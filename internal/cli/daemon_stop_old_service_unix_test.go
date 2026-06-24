//go:build !windows

package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/service"
)

// While a service from an earlier version runs, 'daemon stop' says how that
// service ends and still stops the user's own daemon, which is what it stops.
func TestDaemonStopStopsTheUsersDaemonWhileAnOldServiceRuns(t *testing.T) {
	isolateDaemonHome(t)
	ipcDown := startStandInDaemon(t)
	origOld, origExited := oldServiceRunning, daemonExited
	oldServiceRunning = func() bool { return true }
	daemonExited = func(int) bool { return true }
	t.Cleanup(func() { oldServiceRunning, daemonExited = origOld, origExited })

	out, err := runDaemonCommand(t, newDaemonStopCmd())
	select {
	case <-ipcDown:
	case <-time.After(5 * time.Second):
		t.Errorf("daemon stop did not ask the user's daemon to shut down")
	}
	if err != nil || !strings.Contains(out, service.OldServiceRunning) || !strings.Contains(out, "Daemon stopped successfully.") {
		t.Errorf("daemon stop: %v; want the note %q and the user's daemon stopped\n%s", err, service.OldServiceRunning, out)
	}
}
