// Package cli provides legacy Windows service cleanup commands.
package cli

import (
	"errors"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/reporting"
	"github.com/rescale/rescale-int/internal/service"
)

// errWindowsOnly refuses the service commands where there is no service.
var errWindowsOnly = reporting.UsageError(errors.New("service management is only supported on Windows"))

// errServiceRemoved refuses, on every OS, the service commands earlier
// versions had: a script or installer still calling one must fail, not get
// the group's help and succeed.
var errServiceRemoved = reporting.UsageError(errors.New("service mode was removed: auto-download now runs from the " +
	"Interlink app or with 'rescale-int daemon run'; only 'rescale-int service uninstall' remains, which removes a " +
	"Windows service from an earlier version"))

// newServiceCmd creates the 'service' command group. Auto-download no longer
// runs as a Windows service — it runs as a subprocess in the logged-in user's
// session (started by the tray/GUI). This group only retains an uninstall
// command so installers and upgrades can remove a service left over from an
// older Interlink version. The group is hidden from help output; its usage
// line names the one command it has.
func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "service uninstall",
		Short:  "Legacy Windows service cleanup",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return errServiceRemoved
			}
			return cmd.Help()
		},
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
				return errWindowsOnly
			}
			return service.Uninstall()
		},
	}
}
