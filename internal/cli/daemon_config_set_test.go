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
// absolute download_folder, a true/false setting in any usual spelling, and
// says what it stored, as 'daemon config show' then does. A value it refuses is a usage error: no error report,
// and a message the report redaction would not have hidden.
func TestDaemonConfigSet(t *testing.T) {
	home := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(env, home)
	}
	folder := t.TempDir()
	stored := func(get func(*config.DaemonConfig) any, want any) func(*config.DaemonConfig) bool {
		return func(c *config.DaemonConfig) bool { return get(c) == want }
	}
	maxConcurrent := func(c *config.DaemonConfig) any { return c.Daemon.MaxConcurrent }
	workspaceFolders := func(c *config.DaemonConfig) any { return c.Daemon.IncludeWorkspaceFolders }
	flatten := func(c *config.DaemonConfig) any { return c.Daemon.FlattenFolderStructure }
	absolute := func(c *config.DaemonConfig) bool { return filepath.IsAbs(c.Daemon.DownloadFolder) }
	resolved := func(path string) string { p, _ := filepath.EvalSymlinks(path); return p }
	relative := filepath.Join("relative", "dir")
	for _, tc := range []struct {
		key, value string
		want       string                          // the refusal, or, once the value is taken, what the command says
		stored     func(*config.DaemonConfig) bool // what daemon.conf then holds; nil: the value is refused
	}{
		{"max_concurrent", "0", "max_concurrent must be between 1 and 20, got 0", nil},
		{"max_concurrent", "1", "Set max_concurrent = 1\n", stored(maxConcurrent, 1)},
		{"max_concurrent", "20", "Set max_concurrent = 20\n", stored(maxConcurrent, 20)},
		{"max_concurrent", "21", "max_concurrent must be between 1 and 20, got 21", nil},
		{"max_concurrent", "many", "invalid integer: many", nil},
		{"max_concurrent", "007", "Set max_concurrent = 7\n", stored(maxConcurrent, 7)},
		{"download_folder", folder, "Set download_folder = " + resolved(folder) + "\n", absolute},
		{"download_folder", "~", "Set download_folder = " + resolved(home) + "\n", absolute},
		{"download_folder", "~foo", `download_folder must be an absolute path, got "~foo"`, nil},
		{"download_folder", relative, fmt.Sprintf("download_folder must be an absolute path, got %q", relative), nil},
		{"download_folder", "", `download_folder must be an absolute path, got ""`, nil},
		{"poll_interval_minutes", "0", "poll_interval_minutes must be between 1 and 1440", nil},
		{"include_workspace_folders", "True", "Set include_workspace_folders = true\n", stored(workspaceFolders, true)},
		{"include_workspace_folders", "OFF", "Set include_workspace_folders = false\n", stored(workspaceFolders, false)},
		{"include_workspace_folders", "on", "Set include_workspace_folders = true\n", stored(workspaceFolders, true)},
		{"include_workspace_folders", "maybe", `include_workspace_folders must be true or false, got "maybe"`, nil},
		{"flatten_folder_structure", "yes", "Set flatten_folder_structure = true\n", stored(flatten, true)},
		{"no_such_setting", "1", `unknown setting "no_such_setting"`, nil},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			out, err := runDaemonCommand(t, newDaemonConfigSetCmd(), tc.key, tc.value)
			if tc.stored != nil {
				if err != nil || !strings.Contains(out, tc.want) {
					t.Fatalf("daemon config set %s %q: %v, want it taken, saying %q\n%s", tc.key, tc.value, err, tc.want, out)
				}
				if cfg, err := config.LoadDaemonConfig(""); err != nil || !tc.stored(cfg) {
					t.Errorf("after setting %s %q daemon.conf holds %+v (%v)", tc.key, tc.value, cfg, err)
				}
				return
			}
			if err == nil || !strings.Contains(reporting.RedactError(err.Error()), tc.want) {
				t.Fatalf("daemon config set %s %q: %v, want an error containing %q", tc.key, tc.value, err, tc.want)
			}
			if saved := reporting.HandleCLIError(err, "cli", "rescale-int daemon config set", ""); saved != "" {
				t.Errorf("the refusal saved an error report to %s", saved)
			}
		})
	}

	// 'daemon config show' shows the workspace folder settings as the rows
	// above stored them.
	out, err := runDaemonCommand(t, newDaemonConfigShowCmd())
	for _, want := range []string{"include_workspace_folders = true\n", "flatten_folder_structure = true\n"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("daemon config show: %v; want %q in\n%s", err, want, out)
		}
	}
}
