package compat

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/version"
)

func newCheckForUpdateCmd() *cobra.Command {
	var installAvailable bool

	cmd := &cobra.Command{
		Use:   "check-for-update",
		Short: "Check for CLI updates",
		Annotations: map[string]string{
			"skipAuth": "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if installAvailable {
				return fmt.Errorf("'-i' (install available) is not yet implemented in compat mode")
			}

			cc := GetCompatContext(cmd)
			cc.Printf("Current version: %s\n", version.Version)
			cc.Printf("Check for updates at: https://github.com/rescale-labs/Rescale_Interlink/releases\n")
			return nil
		},
	}

	// Deferred flag
	cmd.Flags().BoolVarP(&installAvailable, "install-available", "i", false, "Install available update")
	cmd.Flags().MarkHidden("install-available")

	return cmd
}
