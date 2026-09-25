package cli

import (
	"bufio"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/config"
)

// TestConfigCommands pins the config group's subcommands and each one's name,
// help text, handler and flags.
func TestConfigCommands(t *testing.T) {
	var names []string
	for _, sub := range newConfigCmd().Commands() {
		names = append(names, sub.Name())
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"init", "path", "show", "test"}) {
		t.Errorf("config subcommands = %v, want init, path, show and test", names)
	}

	for _, tc := range []struct {
		cmd   *cobra.Command
		use   string
		flags []string
	}{
		{newConfigPathCmd(), "path", nil},
		{newConfigShowCmd(), "show", nil},
		{newConfigTestCmd(), "test", nil},
		{newConfigInitCmd(), "init", []string{"force"}},
	} {
		if tc.cmd.Use != tc.use || tc.cmd.Short == "" || tc.cmd.RunE == nil {
			t.Errorf("config %s: Use %q, Short %q, RunE set %v", tc.use, tc.cmd.Use, tc.cmd.Short, tc.cmd.RunE != nil)
		}
		for _, name := range tc.flags {
			if tc.cmd.Flags().Lookup(name) == nil {
				t.Errorf("config %s: --%s flag not found", tc.use, name)
			}
		}
	}
}

// TestConfigDefaultPath tests the default config path function
func TestConfigDefaultPath(t *testing.T) {
	path := config.GetDefaultConfigPath()
	if path == "" {
		t.Error("GetDefaultConfigPath() returned empty string")
	}

	// Should be an absolute path (e.g., ~/.config/rescale-int/config.csv)
	if !filepath.IsAbs(path) {
		t.Error("Default config path is not absolute")
	}
}

// TestConfigInitRequiresTerminal verifies that 'config init' fails fast without a
// terminal. It used to discard the read error and re-prompt forever on the
// required API-key field, spinning at 100% CPU and flooding stdout.
func TestConfigInitRequiresTerminal(t *testing.T) {
	if IsTerminal() {
		t.Skip("test needs a non-interactive stdin")
	}

	cmd := newConfigInitCmd()
	done := make(chan error, 1)
	go func() {
		done <- cmd.RunE(cmd, nil)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, errConfigInitNeedsTTY) {
			t.Fatalf("expected errConfigInitNeedsTTY, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("config init did not return without a terminal (infinite prompt loop)")
	}
}

// TestReadPromptLine verifies that a closed stdin is an error rather than an
// empty answer — the difference between exiting and looping forever.
func TestReadPromptLine(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "line with newline", input: "hello\n", want: "hello"},
		{name: "trailing spaces trimmed", input: "  hello  \n", want: "hello"},
		{name: "empty line is a valid answer", input: "\n", want: ""},
		{name: "final line without newline", input: "hello", want: "hello"},
		{name: "closed input", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readPromptLine(bufio.NewReader(strings.NewReader(tt.input)))
			if tt.wantErr != (err != nil) {
				t.Fatalf("readPromptLine() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("readPromptLine() = %q, want %q", got, tt.want)
			}
		})
	}
}
