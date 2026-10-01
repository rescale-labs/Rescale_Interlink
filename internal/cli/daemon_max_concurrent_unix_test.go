//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/reporting"
)

// 'daemon run' takes max_concurrent 1-20, from its flag or from daemon.conf,
// where only an omitted key means the default, and refuses one from
// daemon.conf before --background starts a daemon, whose refusal no one would
// see. It takes a download_folder from daemon.conf only if it is absolute; its
// own --download-dir is resolved against the working folder, where the user
// can see it. A run it takes reaches the next check, the poll interval, which
// is too short here so that no daemon starts. A refusal is a usage error: no
// error report. The flag's own refusals are in
// TestDaemonRunRefusesBeforeItChangesAnything.
func TestDaemonRunTakesWhatDaemonConfTakes(t *testing.T) {
	const next = "poll interval must be at least 30 seconds"
	const conf = "max_concurrent in daemon.conf must be between 1 and 20, got "
	for _, tc := range []struct {
		name, conf string // conf: daemon.conf's [daemon] section
		args       []string
		want       string
	}{
		{"--max-concurrent 1", "", []string{"--max-concurrent", "1"}, next},
		{"--max-concurrent 20", "", []string{"--max-concurrent", "20"}, next},
		{"max_concurrent = 0", "max_concurrent = 0", nil, conf + "0"},
		{"max_concurrent = 1", "max_concurrent = 1", nil, next},
		{"max_concurrent = 20", "max_concurrent = 20", nil, next},
		{"max_concurrent = 21", "max_concurrent = 21", nil, conf + "21"},
		{"max_concurrent = 50, before --background starts a daemon", "max_concurrent = 50", []string{"--background"}, conf + "50"},
		{"max_concurrent omitted", "poll_interval_minutes = 5", nil, next},
		{"--max-concurrent over max_concurrent = 0", "max_concurrent = 0", []string{"--max-concurrent", "5"}, next},
		{"relative download_folder", "download_folder = rel", nil, `download_folder in daemon.conf must be an absolute path, got "rel"`},
		{"download_folder under ~", "download_folder = ~/dl", nil, `download_folder in daemon.conf must be an absolute path, got "~/dl"`},
		{"relative --download-dir", "download_folder = rel", []string{"--download-dir", "rel"}, next},
		{"--poll-interval soon", "", []string{"--poll-interval", "soon"}, `invalid poll interval "soon"`},
		{"--poll-interval 25h", "", []string{"--poll-interval", "25h"}, "poll interval must be at most 24 hours"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateDaemonHome(t)
			keepDaemonRunGlobals(t)
			t.Chdir(t.TempDir())
			origDaemonize := daemonize
			daemonize = func([]string) error { t.Error("daemon run --background started a daemon"); return nil }
			t.Cleanup(func() { daemonize = origDaemonize })
			path, err := config.DefaultDaemonConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			os.MkdirAll(filepath.Dir(path), 0o700)
			if err := os.WriteFile(path, []byte("[daemon]\n"+tc.conf+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"--poll-interval", "1s", "--state-file", filepath.Join(home, "state.json")}, tc.args...)
			if !slices.Contains(args, "--download-dir") && !strings.Contains(tc.conf, "download_folder") {
				args = append(args, "--download-dir", filepath.Join(home, "downloads"))
			}
			_, err = runDaemonCommand(t, newDaemonRunCmd(), args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("daemon run: %v, want an error containing %q", err, tc.want)
			}
			if saved := reporting.HandleCLIError(err, "cli", "rescale-int daemon run", ""); saved != "" {
				t.Errorf("the refusal saved an error report to %s", saved)
			}
		})
	}
}
