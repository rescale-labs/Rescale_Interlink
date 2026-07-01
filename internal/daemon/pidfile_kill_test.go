package daemon

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
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
// command other than 'daemon run', the GUI itself, and any other program.
func TestKillDaemon_EndsOnlyThisUsersDaemon(t *testing.T) {
	if mode := os.Getenv("INTERLINK_TEST_KILL_DAEMON"); mode != "" {
		if mode == "ignore SIGTERM" {
			signal.Ignore(syscall.SIGTERM)
		}
		os.Stdout.WriteString("ready\n")
		time.Sleep(time.Minute)
		return
	}

	dir := t.TempDir()
	other := exec.Command("sleep", "60")
	if runtime.GOOS == "windows" {
		other = exec.Command("ping", "-n", "60", "127.0.0.1")
	}
	const notDaemon = "is not an Interlink daemon, so it was not ended"
	for _, tc := range []struct {
		name   string
		cmd    *exec.Cmd
		ended  string // the signal that ended it; "" when it must be left running
		refuse string
	}{
		{"a daemon", startHelper(t, dir, "rescale-int", "run", "daemon", "run", "--ipc"), "terminated", ""},
		{"a daemon that ignores SIGTERM", startHelper(t, dir, "rescale-int", "ignore SIGTERM", "daemon", "run"), "killed", ""},
		{"a transfer", startHelper(t, dir, "rescale-int", "run", "upload", "big.dat"), "", notDaemon},
		{"the GUI", startHelper(t, dir, "rescale-int-gui", "run"), "", notDaemon},
		{"another program", other, "", notDaemon},
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

			err = KillDaemon(tc.cmd.Process.Pid, 2*time.Second)
			if tc.refuse != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refuse) {
					t.Errorf("KillDaemon: %v, want a refusal saying %q", err, tc.refuse)
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
			if gone, err := state.ProcessExited(tc.cmd.Process.Pid); !gone {
				t.Fatalf("KillDaemon returned before the process exited (%v)", err)
			}
			<-exited
			if runtime.GOOS != "windows" && !strings.Contains(tc.cmd.ProcessState.String(), "signal: "+tc.ended) {
				t.Errorf("the process ended with %s, want signal: %s", tc.cmd.ProcessState, tc.ended)
			}
		})
	}
}
