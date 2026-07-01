//go:build !windows

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/logging"
)

// refusedShutdown answers as the daemon does, but refuses to shut down.
type refusedShutdown struct{ ipc.ServiceHandler }

func (refusedShutdown) Shutdown() error { return errors.New("FAKE refusal") }

// startRefusingDaemon makes this test process look like a running daemon that
// answers IPC but refuses to shut down.
func startRefusingDaemon(t *testing.T) {
	t.Helper()
	if err := daemon.WritePIDFile(); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	t.Cleanup(daemon.RemovePIDFile)
	logger := logging.NewLoggerWithWriter(io.Discard)
	cfg := daemon.DefaultConfig()
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	d, err := daemon.New(&config.Config{APIKey: "test-key", APIBaseURL: "https://platform.rescale.com"}, cfg, logger)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	srv := ipc.NewServer(refusedShutdown{daemon.NewIPCHandler(d, func() {})}, logger)
	if err := srv.Start(); err != nil {
		t.Fatalf("IPC server: %v", err)
	}
	t.Cleanup(srv.Stop)
}

// With --force, 'daemon stop' ends the daemon's process when IPC does not
// answer, when the daemon refuses the shutdown and when it has not exited in
// time, and reports it stopped once KillDaemon, which returns only after the
// exit, has ended it; a daemon it could not end is a failure. Without --force
// it ends nothing.
func TestDaemonStopForceEndsADaemonThatDoesNotStop(t *testing.T) {
	origWait := daemonStopWait
	daemonStopWait = 300 * time.Millisecond
	t.Cleanup(func() { daemonStopWait = origWait })
	for _, tc := range []struct {
		name, says string // what --force says before it ends the daemon
		start      func(*testing.T)
	}{
		{"IPC does not answer", "but IPC not responding", func(t *testing.T) {
			if err := daemon.WritePIDFile(); err != nil {
				t.Fatalf("WritePIDFile: %v", err)
			}
			t.Cleanup(daemon.RemovePIDFile)
		}},
		{"the shutdown is refused", "Graceful shutdown failed", startRefusingDaemon},
		{"the daemon does not exit", "Graceful shutdown timed out", func(t *testing.T) { startStandInDaemon(t) }},
	} {
		for _, mode := range []struct {
			force bool
			kill  error // what KillDaemon returns
		}{{false, nil}, {true, nil}, {true, errors.New("FAKE failure")}} {
			t.Run(fmt.Sprintf("%s, --force %v, kill error %v", tc.name, mode.force, mode.kill), func(t *testing.T) {
				isolateDaemonHome(t)
				tc.start(t)
				var killed []int
				origKill := daemon.KillDaemon
				daemon.KillDaemon = func(pid int, _ time.Duration) error { killed = append(killed, pid); return mode.kill }
				t.Cleanup(func() { daemon.KillDaemon = origKill })

				var args []string
				if mode.force {
					args = append(args, "--force")
				}
				out, err := runDaemonCommand(t, newDaemonStopCmd(), args...)
				switch {
				case !mode.force:
					if len(killed) != 0 {
						t.Errorf("daemon stop without --force ended PID %v\n%s", killed, out)
					}
				case !slices.Equal(killed, []int{os.Getpid()}) || !strings.Contains(out, tc.says):
					t.Errorf("daemon stop --force ended %v, want the daemon's PID %d after %q: %v\n%s", killed, os.Getpid(), tc.says, err, out)
				case mode.kill == nil && (err != nil || !strings.Contains(out, "Daemon force-stopped.")):
					t.Errorf("daemon stop --force: %v, want the daemon reported force-stopped\n%s", err, out)
				case mode.kill != nil && (err == nil || !strings.Contains(err.Error(), "failed to force-stop daemon: FAKE failure") || strings.Contains(out, "force-stopped")):
					t.Errorf("daemon stop --force that could not end the daemon: %v, want the failure\n%s", err, out)
				}
			})
		}
	}
}
