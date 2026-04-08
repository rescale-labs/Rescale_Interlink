package compat

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestNewCompatRootCmd_GlobalFlags(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	// Verify global flags are registered
	flags := []struct {
		name      string
		shorthand string
	}{
		{"api-token", "p"},
		{"api-base-url", "X"},
		{"quiet", "q"},
		{"no-prompt", ""},
		{"enableErrorTracking", ""},
	}

	for _, f := range flags {
		pf := rootCmd.PersistentFlags().Lookup(f.name)
		if pf == nil {
			t.Errorf("flag %q not registered", f.name)
			continue
		}
		if f.shorthand != "" && pf.Shorthand != f.shorthand {
			t.Errorf("flag %q shorthand = %q, want %q", f.name, pf.Shorthand, f.shorthand)
		}
	}
}

func TestNewCompatRootCmd_EnableErrorTrackingHidden(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()
	f := rootCmd.PersistentFlags().Lookup("enableErrorTracking")
	if f == nil {
		t.Fatal("enableErrorTracking flag not registered")
	}
	if !f.Hidden {
		t.Error("enableErrorTracking should be hidden")
	}
}

func TestNewCompatRootCmd_FlagParsing(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	// Parse -p TOKEN -q
	rootCmd.SetArgs([]string{"-p", "my-token", "-q", "--version"})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// The flags are bound to the CompatContext created inside NewCompatRootCmd,
	// so we verify by checking the parsed flag values.
	pf := rootCmd.PersistentFlags().Lookup("api-token")
	if pf.Value.String() != "my-token" {
		t.Errorf("api-token = %q, want %q", pf.Value.String(), "my-token")
	}
	qf := rootCmd.PersistentFlags().Lookup("quiet")
	if qf.Value.String() != "true" {
		t.Errorf("quiet = %q, want %q", qf.Value.String(), "true")
	}
}

func TestNewCompatRootCmd_VersionOutput(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	// -v should trigger version output (not verbose)
	var out strings.Builder
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"-v"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() with -v error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "v") {
		t.Errorf("-v output = %q, expected version string", output)
	}
}

func TestNewCompatRootCmd_VersionFlag(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	var out strings.Builder
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"--version"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("Execute() with --version error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "v") {
		t.Errorf("--version output = %q, expected version string", output)
	}
}

func TestNewCompatRootCmd_HelpExitZero(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	rootCmd.SetArgs([]string{"--help"})
	err := rootCmd.Execute()
	if err != nil {
		t.Errorf("--help returned error: %v", err)
	}
}

func TestNewCompatRootCmd_PlaceholderCommands(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	expectedCmds := []string{
		"status", "stop", "delete", "submit", "upload",
		"download-file", "sync", "list-info", "list-files",
		"check-for-update", "spub",
	}

	for _, name := range expectedCmds {
		found := false
		for _, cmd := range rootCmd.Commands() {
			if cmd.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected placeholder command %q not found", name)
		}
	}
}

func TestNewCompatRootCmd_PlaceholderHelp(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	// Subcommand --help should not error (Cobra intercepts --help)
	rootCmd.SetArgs([]string{"status", "--help"})
	err := rootCmd.Execute()
	if err != nil {
		t.Errorf("placeholder --help returned error: %v", err)
	}
}

func TestNewCompatRootCmd_FlagPlacement(t *testing.T) {
	// Verify global flags work when placed before subcommand.
	// We can't fully test execution without a server, but we can test parsing.
	rootCmd, _ := NewCompatRootCmd()

	// Replace the status command with one that succeeds to test flag propagation
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "status" {
			rootCmd.RemoveCommand(cmd)
			break
		}
	}
	testCmd := &cobra.Command{
		Use:  "status",
		RunE: func(cmd *cobra.Command, args []string) error { return nil },
	}
	rootCmd.AddCommand(testCmd)

	// Global flag before subcommand — this tests Cobra's persistent flag inheritance.
	// PersistentPreRunE will fail (no API server), but flag parsing should succeed.
	rootCmd.SetArgs([]string{"-p", "TOKEN", "-q", "status"})

	// We expect an auth error since there's no real API server,
	// but the flags should parse correctly.
	rootCmd.Execute()

	pf := rootCmd.PersistentFlags().Lookup("api-token")
	if pf.Value.String() != "TOKEN" {
		t.Errorf("api-token = %q, want %q", pf.Value.String(), "TOKEN")
	}
}
