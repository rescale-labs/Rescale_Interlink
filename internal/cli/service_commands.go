// Package cli provides legacy Windows service cleanup commands.
package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/service"
)

// newServiceCmd creates the 'service' command group. Auto-download no longer
// runs as a Windows service — it runs as a subprocess in the logged-in user's
// session (started by the tray/GUI). This group only retains an uninstall
// command so installers and upgrades can remove a service left over from an
// older Interlink version. The group is hidden from help output.
func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "service",
		Short:  "Legacy Windows service cleanup",
		Hidden: true,
	}

	cmd.AddCommand(newServiceUninstallCmd())

	return cmd
}

// newServiceUninstallCmd creates the 'service uninstall' command, used to
// remove a legacy Windows service installed by older Interlink versions.
// Requires administrator privileges. Succeeds quietly when no service exists.
func newServiceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "uninstall",
		Short:  "Remove a legacy Rescale Interlink Windows service",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "windows" {
				return nil
			}
			if !service.IsLegacyServiceInstalled() {
				return nil
			}
			if err := service.UninstallLegacyService(); err != nil {
				return fmt.Errorf("failed to remove legacy service: %w", err)
			}
			fmt.Println("Legacy Rescale Interlink service removed")
			return nil
		},
	}
}
