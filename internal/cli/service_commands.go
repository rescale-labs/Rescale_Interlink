// Package cli provides service management CLI commands.
package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/service"
)

// newServiceCmd creates the 'service' command group for Windows service management.
func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Windows service management commands",
		Long: `Manage the Rescale Interlink Windows service.

The service automatically downloads output files from completed Rescale jobs
in the background. It runs as a Windows service and can be started automatically
at system boot.

Available commands:
  install    Install the service
  uninstall  Uninstall the service
  start      Start the service
  stop       Stop the service
  status     Show service status

Note: Service management requires administrator privileges.`,
	}

	cmd.AddCommand(newServiceInstallCmd())
	cmd.AddCommand(newServiceUninstallCmd())
	cmd.AddCommand(newServiceStartCmd())
	cmd.AddCommand(newServiceStopCmd())
	cmd.AddCommand(newServiceStatusCmd())
	cmd.AddCommand(newServiceInstallAndStartCmd())

	return cmd
}

// newServiceInstallCmd creates the 'service install' command.
func newServiceInstallCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the Windows service",
		Long: `Install the Rescale Interlink auto-download service.

The service will be configured to start automatically at system boot.
Requires administrator privileges.

Example:
  rescale-int service install
  rescale-int service install --config C:\path\to\config.csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "windows" {
				return fmt.Errorf("service installation is only supported on Windows")
			}

			execPath, err := service.GetExecutablePath()
			if err != nil {
				return fmt.Errorf("failed to get executable path: %w", err)
			}

			return service.Install(execPath, configPath)
		},
	}

	cmd.Flags().StringVar(&configPath, "config", "", "Path to configuration file (optional)")

	return cmd
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
				return fmt.Errorf("service management is only supported on Windows")
			}

			return service.Uninstall()
		},
	}
}

// newServiceStartCmd creates the 'service start' command.
func newServiceStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the Windows service",
		Long: `Start the Rescale Interlink auto-download service.

The service must be installed first using 'service install'.
Requires administrator privileges.

Example:
  rescale-int service start`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "windows" {
				return fmt.Errorf("service management is only supported on Windows")
			}

			return service.StartService()
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
				return fmt.Errorf("service management is only supported on Windows")
			}

			return service.StopService()
		},
	}
}

// newServiceInstallAndStartCmd creates the 'service install-and-start' command.
// v4.7.6: Combined idempotent install + start for single UAC prompt.
func newServiceInstallAndStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install-and-start",
		Short: "Install and start the Windows service (idempotent)",
		Long: `Install (if not already installed) and start the Rescale Interlink service.

This is an idempotent operation: if the service is already installed, the install
step is skipped. If the service is already running, the start step is skipped.
Requires administrator privileges.

Example:
  rescale-int service install-and-start`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "windows" {
				return fmt.Errorf("service management is only supported on Windows")
			}

			// Idempotent install: skip if already installed
			if !service.IsInstalled() {
				execPath, err := service.GetExecutablePath()
				if err != nil {
					return fmt.Errorf("failed to get executable path: %w", err)
				}
				if err := service.Install(execPath, ""); err != nil {
					return fmt.Errorf("install failed: %w", err)
				}
				fmt.Println("Service installed successfully")
			} else {
				fmt.Println("Service already installed, skipping install")
			}

			// Idempotent start: skip if already running
			status, err := service.QueryStatus()
			if err == nil && status == service.StatusRunning {
				fmt.Println("Service already running")
				return nil
			}

			if err := service.StartService(); err != nil {
				return fmt.Errorf("start failed: %w", err)
			}
			fmt.Println("Service started successfully")
			return nil
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
				return fmt.Errorf("service management is only supported on Windows")
			}

			status, err := service.QueryStatus()
			if err != nil {
				return fmt.Errorf("failed to query service status: %w", err)
			}

			fmt.Printf("Service: %s\n", service.ServiceDisplayName)
			fmt.Printf("Status:  %s\n", status.String())

			return nil
		},
	}
}
