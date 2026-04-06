package compat

import (
	"fmt"

	"github.com/spf13/cobra"
)

// addPlaceholderCommands registers placeholder commands for rescale-cli
// commands that are not yet implemented. Each returns a descriptive error
// that goes through the standard error path (exit code 33).
func addPlaceholderCommands(rootCmd *cobra.Command) {
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
