package compat

import (
	"fmt"

	"github.com/spf13/cobra"
)

// addPlaceholderCommands registers placeholder commands for all rescale-cli
// commands that are not yet implemented. Each returns a descriptive error
// that goes through the standard error path (exit code 33).
func addPlaceholderCommands(rootCmd *cobra.Command) {
	// Core rescale-cli commands
	placeholders := []struct {
		use   string
		short string
	}{
		{"status", "Check job status"},
		{"stop", "Stop a running job"},
		{"delete", "Delete a job"},
		{"submit", "Submit a job"},
		{"upload", "Upload files to a job"},
		{"download-file", "Download job output files"},
		{"sync", "Sync files with a job"},
		{"list-info", "List job information"},
		{"list-files", "List job files"},
		{"check-for-update", "Check for CLI updates"},
	}

	for _, p := range placeholders {
		cmd := newPlaceholderCmd(p.use, p.short)
		rootCmd.AddCommand(cmd)
	}

	// SPUB (software publisher) placeholders — deferred to v5.0.0
	spubCmd := &cobra.Command{
		Use:   "spub",
		Short: "Software publisher commands (not available in compat mode)",
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		Args:               cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("compat command 'spub' is deferred to v5.0.0")
		},
	}
	spubPlaceholders := []string{
		"register", "upload", "validate", "list", "status",
	}
	for _, name := range spubPlaceholders {
		sub := &cobra.Command{
			Use:   name,
			Short: fmt.Sprintf("Software publisher %s (not available in compat mode)", name),
			FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
			Args:               cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return fmt.Errorf("compat command 'spub %s' is deferred to v5.0.0", cmd.Name())
			},
		}
		spubCmd.AddCommand(sub)
	}
	rootCmd.AddCommand(spubCmd)
}

func newPlaceholderCmd(use, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short + " (not yet implemented)",
		// Whitelist unknown flags so existing scripts don't fail on flag errors.
		// Unlike DisableFlagParsing, this preserves persistent flag inheritance
		// (e.g., -q for quiet mode is parsed correctly on the root command).
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		// Accept arbitrary positional args
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("compat command '%s' is not yet implemented", cmd.Name())
		},
	}
	return cmd
}
