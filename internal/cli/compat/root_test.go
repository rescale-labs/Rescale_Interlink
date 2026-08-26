package compat

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestNewCompatRootCmd_GlobalFlags(t *testing.T) {
	tests := []struct {
		name          string
		wantShorthand string
		wantHidden    bool
	}{
		{name: "api-token", wantShorthand: "p"},
		{name: "api-base-url", wantShorthand: "X"},
		{name: "quiet", wantShorthand: "q"},
		{name: "no-prompt"},
		{name: "enableErrorTracking", wantHidden: true},
	}

	rootCmd, _ := NewCompatRootCmd()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pf := rootCmd.PersistentFlags().Lookup(tt.name)
			if pf == nil {
				t.Fatalf("flag %q not registered", tt.name)
			}
			if pf.Shorthand != tt.wantShorthand {
				t.Errorf("flag %q shorthand = %q, want %q", tt.name, pf.Shorthand, tt.wantShorthand)
			}
			if pf.Hidden != tt.wantHidden {
				t.Errorf("flag %q hidden = %v, want %v", tt.name, pf.Hidden, tt.wantHidden)
			}
		})
	}
}

// TestNewCompatRootCmd_Execute covers the invocations that must succeed without
// credentials: version output, help, and global flag parsing.
func TestNewCompatRootCmd_Execute(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantOut   string            // output must contain this
		wantFlags map[string]string // flag name -> parsed value
	}{
		{
			name:      "global flags parse before --version",
			args:      []string{"-p", "my-token", "-q", "--version"},
			wantFlags: map[string]string{"api-token": "my-token", "quiet": "true"},
		},
		// -v is version, not verbose.
		{name: "-v prints the version", args: []string{"-v"}, wantOut: "v"},
		{name: "--version prints the version", args: []string{"--version"}, wantOut: "v"},
		{name: "--help exits zero", args: []string{"--help"}},
		// Cobra intercepts --help before the subcommand's auth hook.
		{name: "subcommand --help exits zero", args: []string{"status", "--help"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootCmd, _ := NewCompatRootCmd()
			var out strings.Builder
			rootCmd.SetOut(&out)
			rootCmd.SetArgs(tt.args)

			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("Execute(%v) error = %v", tt.args, err)
			}
			if tt.wantOut != "" && !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("output = %q, want it to contain %q", out.String(), tt.wantOut)
			}
			for name, want := range tt.wantFlags {
				pf := rootCmd.PersistentFlags().Lookup(name)
				if pf == nil {
					t.Fatalf("flag %q not registered", name)
				}
				if pf.Value.String() != want {
					t.Errorf("flag %q = %q, want %q", name, pf.Value.String(), want)
				}
			}
		})
	}
}

// TestNewCompatRootCmd_FlagPlacement verifies Cobra's persistent-flag
// inheritance: a global flag placed before the subcommand still binds.
func TestNewCompatRootCmd_FlagPlacement(t *testing.T) {
	rootCmd, _ := NewCompatRootCmd()

	// Swap in a status command that succeeds, so only flag binding is under test.
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "status" {
			rootCmd.RemoveCommand(cmd)
			break
		}
	}
	rootCmd.AddCommand(&cobra.Command{
		Use:  "status",
		RunE: func(cmd *cobra.Command, args []string) error { return nil },
	})

	rootCmd.SetArgs([]string{"-p", "TOKEN", "-q", "status"})
	// PersistentPreRunE may fail without an API server; the flags still parse.
	_ = rootCmd.Execute()

	pf := rootCmd.PersistentFlags().Lookup("api-token")
	if pf.Value.String() != "TOKEN" {
		t.Errorf("api-token = %q, want %q", pf.Value.String(), "TOKEN")
	}
}
