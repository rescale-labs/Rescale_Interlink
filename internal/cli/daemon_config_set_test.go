package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/reporting"
)

// 'daemon config set' takes what 'daemon run' takes, max_concurrent 1-20 and an
// absolute download_folder, and a value it refuses is a usage error: no error
// report, and a message the report redaction would not have hidden.
func TestDaemonConfigSet(t *testing.T) {
	home := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(env, home)
	}
	folder := t.TempDir()
	maxConcurrent := func(n int) func(*config.DaemonConfig) bool {
		return func(c *config.DaemonConfig) bool { return c.Daemon.MaxConcurrent == n }
	}
	absolute := func(c *config.DaemonConfig) bool { return filepath.IsAbs(c.Daemon.DownloadFolder) }
	relative := filepath.Join("relative", "dir")
	for _, tc := range []struct {
		key, value string
		refused    string                          // the refusal, or "" when the value is taken
		stored     func(*config.DaemonConfig) bool // what daemon.conf then holds
	}{
		{"max_concurrent", "0", "max_concurrent must be between 1 and 20, got 0", nil},
		{"max_concurrent", "1", "", maxConcurrent(1)},
		{"max_concurrent", "20", "", maxConcurrent(20)},
		{"max_concurrent", "21", "max_concurrent must be between 1 and 20, got 21", nil},
		{"max_concurrent", "many", "invalid integer: many", nil},
		{"download_folder", folder, "", absolute},
		{"download_folder", "~", "", absolute},
		{"download_folder", "~foo", `download_folder must be an absolute path, got "~foo"`, nil},
		{"download_folder", relative, fmt.Sprintf("download_folder must be an absolute path, got %q", relative), nil},
		{"download_folder", "", `download_folder must be an absolute path, got ""`, nil},
		{"poll_interval_minutes", "0", "poll_interval_minutes must be between 1 and 1440", nil},
		{"no_such_setting", "1", `unknown setting "no_such_setting"`, nil},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			_, err := runDaemonCommand(t, newDaemonConfigSetCmd(), tc.key, tc.value)
			if tc.refused == "" {
				if err != nil {
					t.Fatalf("daemon config set %s %q: %v", tc.key, tc.value, err)
				}
				if cfg, err := config.LoadDaemonConfig(""); err != nil || !tc.stored(cfg) {
					t.Errorf("after setting %s %q daemon.conf holds %+v (%v)", tc.key, tc.value, cfg, err)
				}
				return
			}
			if err == nil || !strings.Contains(reporting.RedactError(err.Error()), tc.refused) {
				t.Fatalf("daemon config set %s %q: %v, want an error containing %q", tc.key, tc.value, err, tc.refused)
			}
			if saved := reporting.HandleCLIError(err, "cli", "rescale-int daemon config set", ""); saved != "" {
				t.Errorf("the refusal saved an error report to %s", saved)
			}
		})
	}
}
