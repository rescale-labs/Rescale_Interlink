package daemon

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/cloud/state"
)

// startHelper runs this test binary, copied as name, with args after the flag
// that selects the helper, and waits until it is running in mode.
func startHelper(t *testing.T, dir, name, mode string, args ...string) *exec.Cmd {
	t.Helper()
	path := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if _, err := os.Stat(path); err != nil {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(path, append([]string{"-test.run=^TestKillDaemon_EndsOnlyThisUsersDaemon$"}, args...)...)
	cmd.Env = append(os.Environ(), "INTERLINK_TEST_KILL_DAEMON="+mode)
	return cmd
}

// KillDaemon ends this user's daemon, and returns only once it has exited: here
// this test binary, copied under the CLI's name and run with 'daemon run'. On
// Unix it asks with SIGTERM and forces SIGKILL only when that is ignored. What
// a reused PID can name instead is refused and left running: an Interlink
// command other than 'daemon run', the GUI itself, and any other program; so
// is a daemon whose name or arguments it does not know, as a mistake would end
// another process. A PID file holds for a daemon however it was started, as a
// mistake would start a second one, and for nothing else.
func TestKillDaemon_EndsOnlyThisUsersDaemon(t *testing.T) {
	if mode := os.Getenv("INTERLINK_TEST_KILL_DAEMON"); mode != "" {
		if mode == "ignore SIGTERM" {
			signal.Ignore(syscall.SIGTERM)
		}
		os.Stdout.WriteString("ready\n")
		time.Sleep(time.Minute)
		return
	}

	isolateHome(t)
	dir := t.TempDir()
	other := exec.Command("sleep", "60")
	guiEnded, linkEnded := "terminated", "" // macOS and Linux run the app's daemon from the app
	switch runtime.GOOS {
	case "windows":
		other, guiEnded = exec.Command("ping", "-n", "60", "127.0.0.1"), ""
	case "linux":
		linkEnded = "terminated" // Linux names a process for the file a link names, macOS for the link
	}
	if runtime.GOOS != "windows" { // where making a link needs a privilege: the helper is a copy of that name
		if err := os.Symlink(filepath.Join(dir, "rescale-int"), filepath.Join(dir, "interlink")); err != nil {
			t.Fatal(err)
		}
	}
	const notDaemon = "is not an Interlink daemon, so it was not ended"
	for _, tc := range []struct {
		name  string
		cmd   *exec.Cmd
		held  bool   // whether a PID file naming it holds
		ended string // the signal that ended it; "" when it must be left running
	}{
		{"a daemon", startHelper(t, dir, "rescale-int", "run", "daemon", "run", "--ipc"), true, "terminated"},
		{"a daemon that ignores SIGTERM", startHelper(t, dir, "rescale-int", "ignore SIGTERM", "daemon", "run"), true, "killed"},
		{"the GUI's daemon", startHelper(t, dir, "rescale-int-gui", "run", "daemon", "run"), true, guiEnded},
		{"a renamed daemon", startHelper(t, dir, "rescale-int-4.9.8", "run", "daemon", "run"), true, ""},
		{"a daemon given a flag before run", startHelper(t, dir, "rescale-int", "run", "daemon", "--debug", "run"), true, ""},
		{"a daemon started through a link", startHelper(t, dir, "interlink", "run", "daemon", "run"), true, linkEnded},
		{"a transfer", startHelper(t, dir, "rescale-int", "run", "upload", "big.dat"), false, ""},
		{"the GUI", startHelper(t, dir, "rescale-int-gui", "run"), false, ""},
		{"another program", other, false, ""},
	} {
		if runtime.GOOS == "windows" && tc.ended == "killed" {
			continue // Windows has no SIGTERM to ignore
		}
		t.Run(tc.name, func(t *testing.T) {
			stdout, err := tc.cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.cmd.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			exited := make(chan struct{})
			go func() { tc.cmd.Wait(); close(exited) }()
			t.Cleanup(func() { tc.cmd.Process.Kill(); <-exited })
			if tc.cmd.Env != nil { // wait until the helper is running as asked
				if line, _ := bufio.NewReader(stdout).ReadString('\n'); line != "ready\n" {
					t.Fatalf("the helper said %q", line)
				}
			} else {
				go io.Copy(io.Discard, stdout)
			}

			pid := tc.cmd.Process.Pid
			writeFile(t, PIDFilePath(), strconv.Itoa(pid))
			if err := CheckPIDFile(); (err != nil) != tc.held {
				t.Errorf("CheckPIDFile with the PID file naming %s: %v", tc.name, err)
			}
			if got := IsDaemonRunning(); (got == pid) != tc.held {
				t.Errorf("IsDaemonRunning with the PID file naming %s = %d", tc.name, got)
			}
			if err := WritePIDFile(); (err != nil) != tc.held {
				t.Errorf("WritePIDFile over the PID file naming %s: %v", tc.name, err)
			}
			RemovePIDFile()

			err = KillDaemon(pid, 2*time.Second)
			if tc.ended == "" {
				if err == nil || !strings.Contains(err.Error(), notDaemon) {
					t.Errorf("KillDaemon: %v, want a refusal saying %q", err, notDaemon)
				}
				select {
				case <-exited:
					t.Errorf("KillDaemon ended %s", tc.name)
				case <-time.After(200 * time.Millisecond):
				}
				return
			}
			if err != nil {
				t.Fatalf("KillDaemon: %v", err)
			}
			if gone, err := state.ProcessExited(pid); !gone {
				t.Fatalf("KillDaemon returned before the process exited (%v)", err)
			}
			<-exited
			if runtime.GOOS != "windows" && !strings.Contains(tc.cmd.ProcessState.String(), "signal: "+tc.ended) {
				t.Errorf("the process ended with %s, want signal: %s", tc.cmd.ProcessState, tc.ended)
			}
		})
	}
}
