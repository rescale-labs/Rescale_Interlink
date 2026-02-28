package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/ratelimit/coordinator"
)

// newCoordinatorCmd creates the hidden ratelimit-coordinator command group.
func newCoordinatorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "ratelimit-coordinator",
		Short:  "Internal: cross-process rate limit coordinator",
		Hidden: true,
	}

	cmd.AddCommand(newCoordinatorRunCmd())
	cmd.AddCommand(newCoordinatorStatusCmd())

	return cmd
}

// newCoordinatorRunCmd creates the "ratelimit-coordinator run" command.
// This is the entry point for the coordinator server process.
func newCoordinatorRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the rate limit coordinator server",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Write PID file
			if err := coordinator.WritePIDFile(); err != nil {
				return fmt.Errorf("failed to write PID file: %w", err)
			}
			defer coordinator.RemovePIDFile()

			// Create listener
			listener, err := coordinator.Listen()
			if err != nil {
				return fmt.Errorf("failed to create listener: %w", err)
			}
			defer coordinator.CleanupSocket()

			// Create and start server
			srv := coordinator.NewServer()
			srv.Start(listener)

			log.Printf("Rate limit coordinator started (PID %d, socket %s)", os.Getpid(), coordinator.SocketPath())

			// Wait for signal or idle timeout
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

			select {
			case sig := <-sigChan:
				log.Printf("Rate limit coordinator received signal %v, shutting down", sig)
			case <-srv.Done():
				log.Printf("Rate limit coordinator shutting down (idle timeout or shutdown request)")
			}

			signal.Stop(sigChan)
			srv.Stop()
			log.Printf("Rate limit coordinator stopped")
			return nil
		},
	}
}

// newCoordinatorStatusCmd creates the "ratelimit-coordinator status" command.
func newCoordinatorStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show rate limit coordinator status",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := coordinator.NewClient()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			state, err := client.GetState(ctx)
			if err != nil {
				return fmt.Errorf("coordinator not running or unreachable: %w", err)
			}

			fmt.Printf("Rate Limit Coordinator Status\n")
			fmt.Printf("  Uptime:         %s\n", state.Uptime.Truncate(time.Second))
			fmt.Printf("  Active clients: %d\n", state.ActiveClients)
			fmt.Printf("  Active leases:  %d\n", state.ActiveLeases)
			fmt.Printf("  Buckets:        %d\n", len(state.Buckets))

			for key, bucket := range state.Buckets {
				fmt.Printf("\n  Bucket: %s\n", key)
				fmt.Printf("    Tokens:          %.1f\n", bucket.Tokens)
				fmt.Printf("    Cooldown remain: %dms\n", bucket.CooldownRemainMs)
				fmt.Printf("    Active clients:  %d\n", bucket.ActiveClients)
			}

			return nil
		},
	}
}
