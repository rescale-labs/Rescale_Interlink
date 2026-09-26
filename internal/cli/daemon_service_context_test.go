package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ratelimit"
)

// A service installed by an earlier version still starts 'rescale-int daemon
// run'. Through the root command and its pre-run hooks, it retires before the
// startup log, the migrations and every profile read, and exits without an
// error. So does a process that cannot tell whether it is a service, unless
// the SCM says it did not start it; only then does the ordinary path run.
//
// The profile makes each step of the ordinary path show: the PID file's place
// holds a folder, so its first read fails; daemon.conf holds a value it
// refuses; a legacy startup log is there for the migrations to rename.
func TestDaemonRunInServiceContextTouchesNoProfile(t *testing.T) {
	const ordinary = "failed to read PID file"
	detectErr := errors.New("FAKE process query failed")
	for _, tc := range []struct {
		name       string
		isService  bool
		detectErr  error
		scmStarted bool
		retired    int    // times the retired service runs
		want       string // the error, or "" for none
	}{
		{"started by the SCM", true, nil, true, 1, ""},
		{"detection fails, the SCM started it", false, detectErr, true, 1, ""},
		{"detection fails, no SCM started it", false, detectErr, false, 1, ordinary},
		{"not a service", false, nil, false, 0, ordinary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for _, env := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
				t.Setenv(env, home)
			}
			t.Setenv("RESCALE_API_KEY", "")
			keepDaemonRunGlobals(t)
			conf, err := config.DefaultDaemonConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			legacy := filepath.Join(config.LogDirectory(), config.LegacyStartupLogName)
			for path, data := range map[string]string{conf: "[daemon]\nmax_concurrent = 50\n", legacy: "FAKE earlier startup log\n"} {
				os.MkdirAll(filepath.Dir(path), 0o700)
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(daemon.PIDFilePath(), 0o700); err != nil {
				t.Fatal(err)
			}
			before := profileTree(t, home)

			origService, origRun, origLog, origBlock, origDaemonize := isWindowsService, runDisabledService, startupLog, shouldBlockSubprocess, daemonize
			retired := 0
			isWindowsService = func() (bool, error) { return tc.isService, tc.detectErr }
			runDisabledService = func() bool { retired++; return tc.scmStarted }
			startupLog = func(string, ...interface{}) { t.Error("the startup log was written") }
			shouldBlockSubprocess = func() (bool, string) { return false, "" }
			daemonize = func([]string) error { t.Error("a daemon was started"); return nil }
			t.Cleanup(func() {
				isWindowsService, runDisabledService, startupLog, shouldBlockSubprocess, daemonize = origService, origRun, origLog, origBlock, origDaemonize
			})

			root := NewRootCmd()
			AddCommands(root)
			_, err = runDaemonCommand(t, root, "daemon", "run")
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("rescale-int daemon run: %v, want %q", err, tc.want)
			}
			if retired != tc.retired {
				t.Errorf("the retired service ran %d times, want %d", retired, tc.retired)
			}
			if after := profileTree(t, home); !maps.Equal(before, after) {
				t.Errorf("the profile changed:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// profileTree lists every file and folder under root with its mode and, for a
// file, a digest of its content.
func profileTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		tree[path] = info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tree[path] += fmt.Sprintf(" %x", sha256.Sum256(data))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
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
