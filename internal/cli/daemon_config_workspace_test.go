package cli

import (
	"strings"
	"testing"
)

// The workspace folder settings are set and shown like the others.
func TestDaemonConfigSetAndShowWorkspaceFolderSettings(t *testing.T) {
	home := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(env, home)
	}
	for _, kv := range [][2]string{{"include_workspace_folders", "true"}, {"flatten_folder_structure", "yes"}} {
		if _, err := runDaemonCommand(t, newDaemonConfigSetCmd(), kv[0], kv[1]); err != nil {
			t.Fatalf("daemon config set %s %s: %v", kv[0], kv[1], err)
		}
	}
	out, err := runDaemonCommand(t, newDaemonConfigShowCmd())
	for _, want := range []string{"include_workspace_folders = true\n", "flatten_folder_structure = true\n"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("daemon config show: %v; want %q in\n%s", err, want, out)
		}
	}
}
