package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestShortcutCommands pins each shortcut's usage, help, handler and flags,
// and that AddShortcuts puts all three on the root command.
func TestShortcutCommands(t *testing.T) {
	for _, sc := range []struct {
		cmd   *cobra.Command
		use   string
		flags []string
	}{
		{newUploadShortcut(), "upload <file> [file...]", []string{"folder-id", "max-concurrent"}},
		{newDownloadShortcut(), "download <file-id> [file-id...]", []string{"outdir", "max-concurrent"}},
		{newLsShortcut(), "ls", []string{"limit"}},
	} {
		if sc.cmd.Use != sc.use || sc.cmd.Short == "" || sc.cmd.Long == "" || sc.cmd.RunE == nil {
			t.Errorf("%s: Use %q, Short %q, Long set %v, RunE set %v", sc.use, sc.cmd.Use, sc.cmd.Short, sc.cmd.Long != "", sc.cmd.RunE != nil)
		}
		for _, name := range sc.flags {
			if sc.cmd.Flags().Lookup(name) == nil {
				t.Errorf("%s: --%s flag not found", sc.use, name)
			}
		}
	}

	rootCmd := NewRootCmd()
	AddShortcuts(rootCmd)
	found := map[string]bool{}
	for _, cmd := range rootCmd.Commands() {
		found[cmd.Name()] = true
	}
	for _, name := range []string{"upload", "download", "ls"} {
		if !found[name] {
			t.Errorf("shortcut %q not found on the root command", name)
		}
	}
}

// isolateCredentials leaves a command under test no key to find: every
// per-user config and token location points at an empty home, the credential
// environment is cleared, and so are the CLI's credential flags. A command
// that gets as far as loading credentials then fails there, before it could
// send a key anywhere.
func isolateCredentials(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA"} {
		t.Setenv(name, home)
	}
	for _, name := range []string{"RESCALE_API_KEY", "RESCALE_API_URL", "RESCALE_CONFIG_FILE"} {
		t.Setenv(name, "")
	}
	origCfgFile, origAPIKey, origTokenFile, origBaseURL := cfgFile, apiKey, tokenFile, apiBaseURL
	cfgFile, apiKey, tokenFile, apiBaseURL = "", "", "", ""
	t.Cleanup(func() { cfgFile, apiKey, tokenFile, apiBaseURL = origCfgFile, origAPIKey, origTokenFile, origBaseURL })
}

// TestShortcutMaxConcurrentRange pins --max-concurrent to 1..20 on both
// transfer shortcuts; the download shortcut once stopped at 10.
func TestShortcutMaxConcurrentRange(t *testing.T) {
	isolateCredentials(t)
	tests := []struct {
		newCmd       func() *cobra.Command
		value        string
		wantRangeErr bool
	}{
		{newDownloadShortcut, "-1", true},
		{newDownloadShortcut, "0", true},
		{newDownloadShortcut, "1", false},
		{newDownloadShortcut, "15", false},
		{newDownloadShortcut, "20", false},
		{newDownloadShortcut, "21", true},
		{newUploadShortcut, "0", true},
		{newUploadShortcut, "20", false},
		{newUploadShortcut, "21", true},
	}

	for _, tt := range tests {
		cmd := tt.newCmd()
		t.Run(cmd.Name()+"="+tt.value, func(t *testing.T) {
			cmd.Flags().Set("max-concurrent", tt.value)
			// RunE validates max-concurrent first; a value it accepts goes on to
			// credential loading, which finds no key.
			want := "API key is required"
			if tt.wantRangeErr {
				want = "must be between"
			}
			if err := cmd.RunE(cmd, []string{"fake-arg"}); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("--max-concurrent %s: error %v, want one saying %q", tt.value, err, want)
			}
		})
	}
}
