package compat

import (
	"fmt"

	"github.com/spf13/cobra"
)

// addPlaceholderCommands registers placeholder commands for rescale-cli
// commands that are not yet implemented. Each returns a descriptive error
// that goes through the standard error path (exit code 33).
func addPlaceholderCommands(rootCmd *cobra.Command) {
	// Commands deferred to Plan 4/5
	placeholders := []struct {
		use   string
		short string
	}{
		{"sync", "Sync files with a job"},
		{"list-files", "List job files"},
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
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		Args:               cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("compat command '%s' is not yet implemented", cmd.Name())
		},
	}
	return cmd
}
