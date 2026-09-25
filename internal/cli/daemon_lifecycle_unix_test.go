//go:build !windows

package cli

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/ratelimit"
	"github.com/rescale/rescale-int/internal/reporting"
)

// isolateDaemonHome gives the test a home directory of its own, so the PID
// file, IPC socket and state it touches are not a real daemon's. It is made
// under the temp root rather than t.TempDir() to keep the socket path within
// the Unix limit of about 104 bytes.
func isolateDaemonHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "daemon-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("RESCALE_API_KEY", "")
	return home
}

// keepDaemonRunGlobals restores what 'daemon run' changes for the whole
// process: the standard logger's output and the rate limit notice hook.
func keepDaemonRunGlobals(t *testing.T) {
	flags, out, notify := log.Flags(), log.Writer(), ratelimit.NotifyFunc()
	t.Cleanup(func() {
		log.SetFlags(flags)
		log.SetOutput(out)
		ratelimit.SetGlobalNotifyFunc(notify)
	})
}

// startStandInDaemon makes this test process look like a running daemon: it
// holds the PID file and answers IPC. Asked to shut down, it closes its IPC
// server at once, as the real daemon does before it exits, and leaves the PID
// file to the test; whether the process has exited is for the test's
// daemonExited to say. The returned channel is closed once IPC is down.
func startStandInDaemon(t *testing.T) <-chan struct{} {
	t.Helper()
	if err := daemon.WritePIDFile(); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	t.Cleanup(daemon.RemovePIDFile)

	// Not a console logger: those read os.Stdout at every write, which
	// runDaemonCommand swaps while the stand-in logs from its IPC server.
	logger := logging.NewLoggerWithWriter(io.Discard)
	cfg := daemon.DefaultConfig()
	cfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	d, err := daemon.New(&config.Config{APIKey: "test-key", APIBaseURL: "https://platform.rescale.com"}, cfg, logger)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}

	ipcDown := make(chan struct{})
	var srv *ipc.Server
	srv = ipc.NewServer(daemon.NewIPCHandler(d, func() {
		srv.Stop()
		close(ipcDown)
	}), logger)
	if err := srv.Start(); err != nil {
		t.Fatalf("IPC server: %v", err)
	}
	t.Cleanup(srv.Stop)
	return ipcDown
}

// 'daemon run' refuses while a daemon runs, and when it cannot read the PID file
// or take its lock, before it changes anything in the profile: its startup
// migrations would rename the legacy log here, and on Windows it would write
// its startup log, making the log folder. It leaves the running daemon's
// PID file alone, and --background starts no daemon, which would refuse the
// file itself and exit after the command had said it started. The poll
// interval is invalid, so a run that skips the check fails on it instead.
func TestDaemonRunRefusesBeforeItChangesAnything(t *testing.T) {
	running := strconv.Itoa(os.Getppid()) // alive, and not this process
	for _, tc := range []struct {
		name, folder string // made where a file belongs, so nothing can open it
		args         []string
		want         string
	}{
		{"while a daemon runs", "", nil, "daemon is already running (PID " + running + ")"},
		{"a PID file it cannot read", "daemon.pid", []string{"--background"}, "failed to read PID file"},
		{"a PID lock it cannot take", "daemon.pid.lock", nil, "failed to lock PID file"},
		{"a --max-concurrent below 1", "", []string{"--max-concurrent", "0"}, "--max-concurrent must be between 1 and 20, got 0"},
		{"a --max-concurrent above 20", "", []string{"--max-concurrent", "21"}, "--max-concurrent must be between 1 and 20, got 21"},
		// what Windows detection refuses a start for
		{"a running Windows Service", "", nil, "cannot start daemon: Windows Service is running. Manage via Services.msc"},
		{"a daemon Windows detection finds", "", nil, "cannot start daemon: Daemon already running (PID 1234)"},
		{"an occupied daemon pipe", "", nil, "cannot start daemon: Daemon appears to be running but not responding (pipe exists)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateDaemonHome(t)
			keepDaemonRunGlobals(t)
			pidPath := daemon.PIDFilePath()
			legacy := filepath.Join(config.LogDirectory(), config.LegacyStartupLogName)
			os.MkdirAll(filepath.Join(filepath.Dir(pidPath), tc.folder), 0o700)
			os.MkdirAll(filepath.Dir(legacy), 0o700)
			if err := os.WriteFile(legacy, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.folder == "" {
				if err := os.WriteFile(pidPath, []byte(running), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			origDaemonize, origLog, origBlock := daemonize, startupLog, shouldBlockSubprocess
			daemonize = func([]string) error { t.Error("daemon run --background started a daemon"); return nil }
			startupLog = func(string, ...interface{}) { t.Error("daemon run wrote its startup log before it refused") }
			shouldBlockSubprocess = func() (bool, string) {
				reason, found := strings.CutPrefix(tc.want, "cannot start daemon: ")
				return found, reason
			}
			t.Cleanup(func() { daemonize, startupLog, shouldBlockSubprocess = origDaemonize, origLog, origBlock })

			_, err := runDaemonCommand(t, newDaemonRunCmd(), append(tc.args, "--poll-interval", "1s", "--download-dir", t.TempDir())...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("daemon run: %v, want an error containing %q", err, tc.want)
			}
			if _, err := os.Stat(legacy); err != nil {
				t.Errorf("daemon run changed the profile before it refused: %v", err)
			}
			if got := daemon.IsDaemonRunning(); tc.folder == "" && strconv.Itoa(got) != running {
				t.Errorf("after the refused start the PID file names %d, want the running daemon %s", got, running)
			}
			if saved := reporting.HandleCLIError(err, "cli", "rescale-int daemon run", ""); tc.folder == "" && saved != "" {
				t.Errorf("the refused start saved an error report to %s", saved)
			}
		})
	}
}

// A max_concurrent in daemon.conf that --max-concurrent would refuse is refused
// by name before a background daemon starts, as in the foreground, and files no
// report: every writer of daemon.conf keeps it in range, so a hand edit put it
// there.
func TestDaemonRunRefusesMaxConcurrentFromDaemonConf(t *testing.T) {
	for _, args := range [][]string{{"--background"}, nil} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			home := isolateDaemonHome(t)
			keepDaemonRunGlobals(t)
			conf, err := config.DefaultDaemonConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			os.MkdirAll(filepath.Dir(conf), 0o700)
			if err := os.WriteFile(conf, []byte("[daemon]\nmax_concurrent = 50\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			origDaemonize := daemonize
			daemonize = func([]string) error { t.Error("daemon run --background started a daemon"); return nil }
			t.Cleanup(func() { daemonize = origDaemonize })

			_, err = runDaemonCommand(t, newDaemonRunCmd(), append(args, "--download-dir", filepath.Join(home, "downloads"), "--state-file", filepath.Join(home, "state.json"))...)
			if want := "max_concurrent in daemon.conf must be between 1 and 20, got 50"; err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("daemon run: %v, want an error containing %q", err, want)
			}
			if saved := reporting.HandleCLIError(err, "cli", "rescale-int daemon run", ""); saved != "" {
				t.Errorf("the refused start saved an error report to %s", saved)
			}
			if _, err := os.Stat(daemon.PIDFilePath()); !os.IsNotExist(err) {
				t.Errorf("the refused start left a PID file (stat: %v)", err)
			}
		})
	}
}

// A foreground 'daemon run' claims the PID file before anything else, even one
// naming this very process, as an earlier process with its PID can leave, and
// removes it when it fails. Its daemon.conf here is a FIFO, whose read blocks
// until the test opens the other end: that holds the command after its claim,
// and after its startup log. The configuration is then empty, so it exits for
// want of an API key.
func TestDaemonRunForegroundHoldsThePIDFileWhileItRuns(t *testing.T) {
	home := isolateDaemonHome(t)
	keepDaemonRunGlobals(t)
	var logged atomic.Bool
	origLog := startupLog
	startupLog = func(string, ...interface{}) { logged.Store(true) }
	t.Cleanup(func() { startupLog = origLog })
	if err := daemon.WritePIDFile(); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	stale, _ := os.Stat(daemon.PIDFilePath())
	fifo, err := config.DefaultDaemonConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := runDaemonCommand(t, newDaemonRunCmd(), "--download-dir", filepath.Join(home, "downloads"), "--state-file", filepath.Join(home, "state.json"))
		done <- err
	}()
	opened := make(chan *os.File, 1)
	go func() {
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			opened <- w
		}
	}()
	select {
	case w := <-opened:
		if claim, err := os.Stat(daemon.PIDFilePath()); err != nil || os.SameFile(stale, claim) {
			t.Errorf("daemon run read its config before claiming the PID file (stat: %v)", err)
		}
		if !logged.Load() {
			t.Error("daemon run read its config before writing its startup log")
		}
		w.Close()
	case err := <-done:
		t.Fatalf("daemon run exited before reading its config: %v", err)
	}

	if err := <-done; err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("daemon run: %v, want it to stop for want of an API key", err)
	}
	if _, err := os.Stat(daemon.PIDFilePath()); !os.IsNotExist(err) {
		t.Errorf("the PID file outlived the daemon (stat: %v)", err)
	}
}

// 'daemon stop' reports the daemon stopped once its process has exited, and
// only then. The daemon releases its PID file just before it exits, and one
// that is killed leaves the file behind, so the file proves nothing either way:
// here its removal and the exit are separate events.
func TestDaemonStopWaitsForTheProcessToExit(t *testing.T) {
	for _, releasesFile := range []bool{true, false} {
		t.Run(fmt.Sprintf("releases its PID file first: %v", releasesFile), func(t *testing.T) {
			isolateDaemonHome(t)
			ipcDown := startStandInDaemon(t)
			var exited atomic.Bool
			var probes atomic.Int32
			origExited := daemonExited
			daemonExited = func(pid int) bool {
				probes.Add(1)
				return pid == os.Getpid() && exited.Load()
			}
			t.Cleanup(func() { daemonExited = origExited })

			type result struct {
				out string
				err error
			}
			done := make(chan result, 1)
			go func() {
				out, err := runDaemonCommand(t, newDaemonStopCmd())
				done <- result{out, err}
			}()
			select {
			case <-ipcDown:
			case <-time.After(30 * time.Second):
				t.Fatal("the stand-in daemon was never asked to shut down")
			}

			if releasesFile {
				daemon.RemovePIDFile()
			}
			deadline := time.Now().Add(30 * time.Second)
			for seen := probes.Load(); probes.Load() < seen+2; time.Sleep(10 * time.Millisecond) {
				select {
				case r := <-done:
					t.Fatalf("daemon stop returned while the daemon process was still running: %v\n%s", r.err, r.out)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("daemon stop never checked whether the daemon process had exited")
				}
			}

			exited.Store(true)
			select {
			case r := <-done:
				if r.err != nil || !strings.Contains(r.out, "Daemon stopped successfully.") {
					t.Errorf("daemon stop: %v\n%s", r.err, r.out)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("daemon stop did not return after the daemon exited")
			}
		})
	}
}

// A daemon still running when the wait runs out is reported plainly, and as a
// failure: the command used to print that the shutdown was sent and exit 0.
// The 'daemon status' it points to must not put that down to a missing --ipc.
func TestDaemonStopSaysSoWhenTheDaemonDoesNotExit(t *testing.T) {
	isolateDaemonHome(t)
	startStandInDaemon(t) // never exits: its PID file stays
	origWait := daemonStopWait
	daemonStopWait = 300 * time.Millisecond
	t.Cleanup(func() { daemonStopWait = origWait })

	out, err := runDaemonCommand(t, newDaemonStopCmd())
	want := fmt.Sprintf("daemon (PID %d) did not exit within 300ms of the shutdown request", os.Getpid())
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("daemon stop: %v, want an error containing %q\n%s", err, want, out)
	}
	if strings.Contains(out, "stopped successfully") {
		t.Errorf("daemon stop claimed success for a daemon that is still running:\n%s", out)
	}
	if saved := reporting.HandleCLIError(err, "cli", "rescale-int daemon stop", ""); saved != "" {
		t.Errorf("the timed-out stop saved an error report to %s", saved)
	}

	if out, err = runDaemonCommand(t, newDaemonStatusCmd()); err != nil || !strings.Contains(out, "stopping") {
		t.Errorf("daemon status for a daemon that is stopping: %v\n%s", err, out)
	}
}

// With no PID file, 'daemon stop' has no process to watch, and IPC going quiet
// comes before the exit, so it says the shutdown was requested and that the
// exit cannot be confirmed, not that the daemon stopped.
func TestDaemonStopWithoutAPIDFileCannotConfirmTheExit(t *testing.T) {
	isolateDaemonHome(t)
	startStandInDaemon(t)
	daemon.RemovePIDFile()

	out, err := runDaemonCommand(t, newDaemonStopCmd())
	if err != nil || !strings.Contains(out, "exit cannot be confirmed") {
		t.Errorf("daemon stop with no PID file: %v\n%s", err, out)
	}
}

// SIGHUP, which a closing terminal sends, stops a daemon that has started the
// way SIGTERM does: it shuts down, returns, and removes its PID file. Its API
// is reached through a proxy on a closed local port, so nothing leaves the
// machine.
func TestDaemonRunStopsOnSIGHUP(t *testing.T) {
	home := isolateDaemonHome(t)
	keepDaemonRunGlobals(t)
	proxied := filepath.Join(home, "config.csv")
	if err := os.WriteFile(proxied, []byte("key,value\nproxy_mode,basic\nproxy_host,127.0.0.1\nproxy_port,9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	origCfgFile, origAPIKey := cfgFile, apiKey
	cfgFile, apiKey = proxied, "test-key"
	t.Cleanup(func() { cfgFile, apiKey = origCfgFile, origAPIKey })
	logFile := filepath.Join(home, "daemon.log")

	done := make(chan error, 1)
	go func() {
		_, err := runDaemonCommand(t, newDaemonRunCmd(), "--download-dir", filepath.Join(home, "downloads"),
			"--state-file", filepath.Join(home, "state.json"), "--log-file", logFile)
		done <- err
	}()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if log, _ := os.ReadFile(logFile); strings.Contains(string(log), "Daemon starting") {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("daemon run returned before it started: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon never started")
		}
	}
	if !signal.Ignored(syscall.SIGPIPE) {
		t.Error("a running daemon does not ignore SIGPIPE, so 'daemon run | tee' dies with its reader before it cleans up")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("SIGHUP: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon run after SIGHUP: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("daemon run did not stop on SIGHUP")
	}
	if log, _ := os.ReadFile(logFile); !strings.Contains(string(log), "Received shutdown signal") {
		t.Errorf("the daemon did not log the signal it stopped on:\n%s", log)
	}
	if _, err := os.Stat(daemon.PIDFilePath()); !os.IsNotExist(err) {
		t.Errorf("the PID file outlived the daemon (stat: %v)", err)
	}
}
