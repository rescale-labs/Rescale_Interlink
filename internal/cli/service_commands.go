// Package cli provides service management CLI commands.
package cli

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/reporting"
	"github.com/rescale/rescale-int/internal/service"
)

// errWindowsOnly refuses the service commands where there is no service.
var errWindowsOnly = reporting.UsageError(errors.New("service management is only supported on Windows"))

// newServiceCmd creates the 'service' command group for Windows service management.
func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Remove or check a Windows service installed by an earlier version",
		Long: `Remove, stop or check a Rescale Interlink Windows service installed by an
earlier version.

Multi-user service mode is not available in this version. Auto-download runs
in each user's session from the Interlink app. A service installed by an
earlier version removes itself the next time Windows starts it; 'service
uninstall' removes it now.

Available commands:
  uninstall  Remove the service
  stop       Stop the service
  status     Show service status

Note: Removing or stopping the service requires administrator privileges.`,
	}

	cmd.AddCommand(newServiceUninstallCmd())
	cmd.AddCommand(newServiceStopCmd())
	cmd.AddCommand(newServiceStatusCmd())
	for _, use := range []string{"install", "install-and-start", "start"} {
		cmd.AddCommand(newRetiredServiceCmd(use))
	}

	return cmd
}

// newRetiredServiceCmd makes a command that used to install or start the
// service. It stays so that a script calling it learns why nothing happens.
func newRetiredServiceCmd(use string) *cobra.Command {
	return &cobra.Command{
		Use:    use,
		Short:  "Not available in this version",
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			return reporting.UsageError(errors.New(service.ModeUnavailable))
		},
	}
}

// newServiceUninstallCmd creates the 'service uninstall' command.
func newServiceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the Windows service",
		Long: `Uninstall the Rescale Interlink auto-download service.

This will stop the service if running and remove it from the system.
Requires administrator privileges.

Example:
  rescale-int service uninstall`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "windows" {
				return errWindowsOnly
			}

			return service.Uninstall()
		},
	}
}

// newServiceStopCmd creates the 'service stop' command.
func newServiceStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the Windows service",
		Long: `Stop the Rescale Interlink auto-download service.

Example:
  rescale-int service stop`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "windows" {
				return errWindowsOnly
			}

			return service.StopService()
		},
	}
}

// newServiceStatusCmd creates the 'service status' command.
func newServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show service status",
		Long: `Show the current status of the Rescale Interlink service.

Example:
  rescale-int service status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "windows" {
				return errWindowsOnly
			}

			status, err := service.QueryStatus()
			if err != nil {
				return fmt.Errorf("failed to query service status: %w", err)
			}

			state := status.String()
			if !service.IsInstalled() {
				state = "Not installed"
			}
			fmt.Printf("Service: %s\n", service.ServiceDisplayName)
			fmt.Printf("Status:  %s\n", state)

			return nil
		},
	}
}
