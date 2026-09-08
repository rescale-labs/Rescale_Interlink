package cli

import (
	"io"
	"os"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud"
)

// --timing was documented and acted on by main(), which set RESCALE_TIMING and
// left the token in os.Args, so cobra then refused the whole command with
// "unknown flag: --timing". Parsing it is the first half of the fix.
func TestTimingFlagParses(t *testing.T) {
	for _, args := range [][]string{
		{"files", "upload", "--help", "--timing"},
		{"--timing", "--version"},
		{"pur", "run", "--timing", "--help"},
	} {
		t.Run(args[0], func(t *testing.T) {
			rootCmd := NewRootCmd()
			AddCommands(rootCmd)
			rootCmd.SetArgs(args)
			rootCmd.SetOut(io.Discard)
			rootCmd.SetErr(io.Discard)
			rootCmd.SilenceUsage = true

			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("%v: %v", args, err)
			}
		})
	}
}

// Parsing it is not enough: it has to turn timing on, which is what the
// os.Args scan in main() did. The flag binds to the package variable and the
// root hook hands that to applyTimingFlag; both halves are tested without
// running the hook, which also replaces the logger and rate-limit callbacks.
func TestTimingFlagBindsAndEnablesTiming(t *testing.T) {
	t.Setenv("RESCALE_TIMING", "")

	rootCmd := NewRootCmd()
	if err := rootCmd.PersistentFlags().Parse([]string{"--timing"}); err != nil {
		t.Fatalf("parse --timing: %v", err)
	}
	if !timing {
		t.Fatal("--timing did not set the flag variable the root hook reads")
	}

	applyTimingFlag(timing)
	// The environment variable, not just an in-process flag: it is what the
	// timing check reads and what a child process inherits.
	if got := os.Getenv("RESCALE_TIMING"); got != "1" {
		t.Errorf("RESCALE_TIMING = %q after --timing, want \"1\"", got)
	}
	if !cloud.TimingEnabled() {
		t.Error("--timing did not enable timing output")
	}
}

// The environment variable is the documented way to enable timing and keeps
// working on its own.
func TestRescaleTimingEnvEnablesTiming(t *testing.T) {
	t.Setenv("RESCALE_TIMING", "1")

	if !cloud.TimingEnabled() {
		t.Error("RESCALE_TIMING=1 no longer enables timing output")
	}
}

// Without the flag nothing is turned on: --timing must not become the default.
func TestNoTimingFlagLeavesTimingOff(t *testing.T) {
	t.Setenv("RESCALE_TIMING", "")

	rootCmd := NewRootCmd()
	if err := rootCmd.PersistentFlags().Parse([]string{}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	applyTimingFlag(timing)
	if got := os.Getenv("RESCALE_TIMING"); got != "" {
		t.Errorf("RESCALE_TIMING = %q without --timing, want it left alone", got)
	}
}
