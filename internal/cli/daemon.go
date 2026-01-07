// Package cli provides the daemon CLI commands for rescale-int.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
)

// newDaemonCmd creates the 'daemon' command group.
func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Background service for auto-downloading completed jobs",
		Long: `Background service that polls for completed jobs and automatically
downloads their output files.

The daemon monitors your Rescale jobs and downloads results when they complete.
This is useful for automated workflows where you want results downloaded
without manual intervention.

Examples:
  # Start daemon in foreground (for testing)
  rescale-int daemon run --download-dir ./results

  # Start daemon in background with IPC control (v4.1.0+)
  rescale-int daemon run --download-dir ./results --background --ipc

  # Start daemon with job name filtering
  rescale-int daemon run --download-dir ./results --name-prefix "MyProject"

  # Run once (check and download, then exit)
  rescale-int daemon run --once --download-dir ./results

  # Check status of running daemon (queries via IPC)
  rescale-int daemon status

  # Stop a running daemon (via IPC)
  rescale-int daemon stop

  # List downloaded jobs
  rescale-int daemon list

  # Retry failed downloads
  rescale-int daemon retry --all`,
	}

	cmd.AddCommand(newDaemonRunCmd())
	cmd.AddCommand(newDaemonStatusCmd())
	cmd.AddCommand(newDaemonStopCmd())
	cmd.AddCommand(newDaemonListCmd())
	cmd.AddCommand(newDaemonRetryCmd())

	return cmd
}

// newDaemonRunCmd creates the 'daemon run' command.
func newDaemonRunCmd() *cobra.Command {
	var (
		downloadDir   string
		pollInterval  string
		namePrefix    string
		nameContains  string
		excludeNames  []string
		maxConcurrent int
		stateFile     string
		useJobID      bool
		runOnce       bool
		logFile       string
		background    bool
		enableIPC     bool
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the daemon to auto-download completed jobs",
		Long: `Start the daemon to auto-download completed jobs.

By default, the daemon runs in foreground mode. Use --background to
detach from the terminal and run as a background process.

When --ipc is enabled, the daemon listens for control commands via
Unix socket (Mac/Linux) or named pipe (Windows), allowing other
processes (like the GUI) to query status, pause/resume, and stop
the daemon.

Press Ctrl+C to stop a foreground daemon gracefully.

Examples:
  # Basic usage - foreground mode
  rescale-int daemon run --download-dir /path/to/results

  # Background mode with IPC control (recommended for GUI integration)
  rescale-int daemon run --download-dir /path/to/results --background --ipc

  # Poll every 2 minutes
  rescale-int daemon run --poll-interval 2m

  # Only download jobs with names starting with "Simulation"
  rescale-int daemon run --name-prefix "Simulation"

  # Run once and exit (useful for cron jobs)
  rescale-int daemon run --once`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			// Check if daemon is already running (when background mode requested)
			if background || enableIPC {
				if pid := daemon.IsDaemonRunning(); pid != 0 {
					return fmt.Errorf("daemon is already running (PID %d)", pid)
				}
			}

			// Handle background mode (Unix only)
			if background {
				if runtime.GOOS == "windows" {
					return fmt.Errorf("--background is not supported on Windows; use Windows Service instead")
				}

				// If we're not the daemon child, fork and exit
				if !daemon.IsDaemonChild() {
					// Reconstruct args for the child, but remove --background
					childArgs := []string{"daemon", "run"}
					childArgs = append(childArgs, "--download-dir", downloadDir)
					childArgs = append(childArgs, "--poll-interval", pollInterval)
					if namePrefix != "" {
						childArgs = append(childArgs, "--name-prefix", namePrefix)
					}
					if nameContains != "" {
						childArgs = append(childArgs, "--name-contains", nameContains)
					}
					for _, ex := range excludeNames {
						childArgs = append(childArgs, "--exclude", ex)
					}
					childArgs = append(childArgs, "--max-concurrent", fmt.Sprintf("%d", maxConcurrent))
					childArgs = append(childArgs, "--state-file", stateFile)
					if useJobID {
						childArgs = append(childArgs, "--use-job-id")
					}
					if logFile != "" {
						childArgs = append(childArgs, "--log-file", logFile)
					}
					if enableIPC {
						childArgs = append(childArgs, "--ipc")
					}

					// Daemonize (this will exit the parent)
					return daemon.Daemonize(childArgs)
				}
			}

			// Parse poll interval
			interval, err := time.ParseDuration(pollInterval)
			if err != nil {
				return fmt.Errorf("invalid poll interval %q: %w", pollInterval, err)
			}

			// Validate interval
			if interval < 30*time.Second {
				return fmt.Errorf("poll interval must be at least 30 seconds")
			}
			if interval > 24*time.Hour {
				return fmt.Errorf("poll interval must be at most 24 hours")
			}

			// Validate download directory
			if downloadDir == "" {
				downloadDir = "."
			}
			absDownloadDir, err := resolveAbsolutePath(downloadDir)
			if err != nil {
				return fmt.Errorf("invalid download directory: %w", err)
			}

			// Create download directory if it doesn't exist
			if err := os.MkdirAll(absDownloadDir, 0755); err != nil {
				return fmt.Errorf("failed to create download directory: %w", err)
			}

			// Build daemon config
			daemonCfg := &daemon.Config{
				PollInterval:  interval,
				DownloadDir:   absDownloadDir,
				UseJobNameDir: !useJobID,
				MaxConcurrent: maxConcurrent,
				StateFile:     stateFile,
				LogFile:       logFile,
			}

			// Build filter if any filter options specified
			if namePrefix != "" || nameContains != "" || len(excludeNames) > 0 {
				daemonCfg.Filter = &daemon.JobFilter{
					NamePrefix:   namePrefix,
					NameContains: nameContains,
					ExcludeNames: excludeNames,
				}
			}

			// Load app config
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Create daemon
			d, err := daemon.New(cfg, daemonCfg, logger)
			if err != nil {
				return fmt.Errorf("failed to create daemon: %w", err)
			}

			// Write PID file (for background mode or IPC mode)
			if background || enableIPC {
				if err := daemon.WritePIDFile(); err != nil {
					return fmt.Errorf("failed to write PID file: %w", err)
				}
				defer daemon.RemovePIDFile()
			}

			// Set up signal handling
			ctx, cancel := context.WithCancel(context.Background())
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

			// Shutdown function for IPC handler
			shutdownRequested := make(chan struct{})
			shutdownFunc := func() {
				close(shutdownRequested)
			}

			// Start IPC server if enabled
			var ipcServer *ipc.Server
			if enableIPC && runtime.GOOS != "windows" {
				ipcHandler := daemon.NewIPCHandler(d, shutdownFunc)
				ipcServer = ipc.NewServer(ipcHandler, logger)
				if err := ipcServer.Start(); err != nil {
					return fmt.Errorf("failed to start IPC server: %w", err)
				}
				defer ipcServer.Stop()
				logger.Info().Str("socket", ipcServer.GetSocketPath()).Msg("IPC server listening")
			}

			go func() {
				select {
				case sig := <-sigChan:
					logger.Info().Str("signal", sig.String()).Msg("Received shutdown signal")
				case <-shutdownRequested:
					logger.Info().Msg("Shutdown requested via IPC")
				}
				cancel()
				d.Stop()
			}()

			// Print startup info (only in foreground mode)
			if !daemon.IsDaemonChild() {
				fmt.Println("======================================================================")
				fmt.Println("  RESCALE INTERLINK DAEMON")
				fmt.Println("======================================================================")
				fmt.Printf("Download Directory: %s\n", absDownloadDir)
				fmt.Printf("Poll Interval: %s\n", interval)
				if daemonCfg.Filter != nil {
					if namePrefix != "" {
						fmt.Printf("Name Filter (prefix): %s\n", namePrefix)
					}
					if nameContains != "" {
						fmt.Printf("Name Filter (contains): %s\n", nameContains)
					}
					if len(excludeNames) > 0 {
						fmt.Printf("Excluded Names: %v\n", excludeNames)
					}
				}
				if enableIPC {
					fmt.Printf("IPC: Enabled (%s)\n", ipc.GetSocketPath())
				}
				fmt.Println("----------------------------------------------------------------------")
				if runOnce {
					fmt.Println("Mode: Single poll (--once)")
				} else {
					fmt.Println("Mode: Continuous polling (Ctrl+C to stop)")
				}
				fmt.Println("======================================================================")
				fmt.Println()
			}

			// Run daemon
			if runOnce {
				return d.RunOnce(ctx)
			}

			if err := d.Start(ctx); err != nil {
				return fmt.Errorf("failed to start daemon: %w", err)
			}

			// Wait for shutdown signal
			<-ctx.Done()

			return nil
		},
	}

	cmd.Flags().StringVarP(&downloadDir, "download-dir", "d", ".", "Directory to download job outputs to")
	cmd.Flags().StringVar(&pollInterval, "poll-interval", "5m", "How often to check for completed jobs (e.g., 30s, 5m, 1h)")
	cmd.Flags().StringVar(&namePrefix, "name-prefix", "", "Only download jobs with names starting with this prefix")
	cmd.Flags().StringVar(&nameContains, "name-contains", "", "Only download jobs with names containing this string")
	cmd.Flags().StringArrayVar(&excludeNames, "exclude", nil, "Exclude jobs with names starting with these prefixes")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", 5, "Maximum concurrent file downloads per job")
	cmd.Flags().StringVar(&stateFile, "state-file", daemon.DefaultStateFilePath(), "Path to daemon state file")
	cmd.Flags().BoolVar(&useJobID, "use-job-id", false, "Use job ID instead of job name for output directory names")
	cmd.Flags().BoolVar(&runOnce, "once", false, "Run once and exit (useful for cron jobs)")
	cmd.Flags().StringVar(&logFile, "log-file", "", "Path to log file (empty = stdout)")
	cmd.Flags().BoolVar(&background, "background", false, "Run as a background daemon (Unix only)")
	cmd.Flags().BoolVar(&enableIPC, "ipc", false, "Enable IPC server for remote control (pause/resume/status/stop)")

	return cmd
}

// newDaemonStatusCmd creates the 'daemon status' command.
func newDaemonStatusCmd() *cobra.Command {
	var stateFile string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status (queries running daemon via IPC, or shows state file)",
		Long: `Display the current daemon status.

If a daemon is running with --ipc enabled, this queries the daemon directly
via IPC and shows live status including:
- Running/paused state
- Active downloads
- Last scan time
- Uptime

If no daemon is running (or IPC is not enabled), shows the state file with:
- Number of jobs downloaded
- Number of failed downloads
- Last poll time
- Recent download history`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			// First try to query running daemon via IPC
			if runtime.GOOS != "windows" {
				client := ipc.NewClient()
				client.SetTimeout(2 * time.Second)

				if status, err := client.GetStatus(ctx); err == nil {
					// Daemon is running - show live status
					fmt.Println("======================================================================")
					fmt.Println("  DAEMON STATUS (Live)")
					fmt.Println("======================================================================")
					fmt.Printf("Status: %s\n", status.ServiceState)
					fmt.Printf("Version: %s\n", status.Version)
					fmt.Printf("Uptime: %s\n", status.Uptime)
					fmt.Printf("Active Downloads: %d\n", status.ActiveDownloads)
					if status.LastScanTime != nil {
						fmt.Printf("Last Scan: %s (%s ago)\n",
							status.LastScanTime.Format(time.RFC3339),
							time.Since(*status.LastScanTime).Round(time.Second))
					} else {
						fmt.Println("Last Scan: Never")
					}
					if status.LastError != "" {
						fmt.Printf("Last Error: %s\n", status.LastError)
					}

					// Also show user list
					if users, err := client.GetUserList(ctx); err == nil && len(users) > 0 {
						fmt.Println("----------------------------------------------------------------------")
						fmt.Println("User Status:")
						for _, user := range users {
							fmt.Printf("  %s: %s\n", user.Username, user.State)
							fmt.Printf("    Download Folder: %s\n", user.DownloadFolder)
							fmt.Printf("    Jobs Downloaded: %d\n", user.JobsDownloaded)
						}
					}

					fmt.Println("======================================================================")
					fmt.Println()
					fmt.Println("Use 'rescale-int daemon stop' to stop the daemon.")
					return nil
				}
			}

			// Check PID file
			if pid := daemon.IsDaemonRunning(); pid != 0 {
				fmt.Printf("Daemon process found (PID %d) but IPC not responding.\n", pid)
				fmt.Println("The daemon may be running without --ipc flag.")
				fmt.Println()
			} else {
				fmt.Println("No running daemon detected.")
				fmt.Println()
			}

			// Fall back to state file
			state := daemon.NewState(stateFile)
			if err := state.Load(); err != nil {
				return fmt.Errorf("failed to load state: %w", err)
			}

			fmt.Println("======================================================================")
			fmt.Println("  DAEMON STATE (From state file)")
			fmt.Println("======================================================================")
			fmt.Printf("State File: %s\n", stateFile)
			fmt.Println("----------------------------------------------------------------------")

			lastPoll := state.GetLastPoll()
			if lastPoll.IsZero() {
				fmt.Println("Last Poll: Never")
			} else {
				fmt.Printf("Last Poll: %s (%s ago)\n",
					lastPoll.Format(time.RFC3339),
					time.Since(lastPoll).Round(time.Second))
			}

			fmt.Printf("Downloaded Jobs: %d\n", state.GetDownloadedCount())
			fmt.Printf("Failed Jobs: %d\n", state.GetFailedCount())

			// Show recent downloads
			recent := state.GetRecentDownloads(5)
			if len(recent) > 0 {
				fmt.Println("\nRecent Downloads:")
				for _, job := range recent {
					sizeMB := float64(job.TotalSize) / (1024 * 1024)
					fmt.Printf("  - %s (%s): %d files, %.2f MB\n",
						job.JobName, job.JobID, job.FileCount, sizeMB)
					fmt.Printf("    Downloaded: %s\n", job.DownloadedAt.Format(time.RFC3339))
					fmt.Printf("    Location: %s\n", job.OutputDir)
				}
			}

			// Show failed downloads
			failed := state.GetFailedJobs()
			if len(failed) > 0 {
				fmt.Println("\nFailed Downloads:")
				for _, job := range failed {
					fmt.Printf("  - %s (%s)\n", job.JobName, job.JobID)
					fmt.Printf("    Error: %s\n", job.Error)
				}
				fmt.Printf("\nUse 'rescale-int daemon retry --all' to retry failed downloads.\n")
			}

			fmt.Println("======================================================================")

			return nil
		},
	}

	cmd.Flags().StringVar(&stateFile, "state-file", daemon.DefaultStateFilePath(), "Path to daemon state file")

	return cmd
}

// newDaemonStopCmd creates the 'daemon stop' command.
func newDaemonStopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a running daemon via IPC",
		Long: `Stop a running daemon process.

This sends a shutdown command via IPC to gracefully stop the daemon.
The daemon must have been started with --ipc flag for this to work.

On Windows, use the Windows Service Manager to stop the service instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS == "windows" {
				return fmt.Errorf("use Windows Service Manager to stop the daemon on Windows")
			}

			// Check if daemon is running
			pid := daemon.IsDaemonRunning()
			if pid == 0 {
				fmt.Println("No running daemon detected.")
				return nil
			}

			// Try to stop via IPC
			ctx := context.Background()
			client := ipc.NewClient()
			client.SetTimeout(5 * time.Second)

			// First check if IPC is responding
			if !client.IsServiceRunning(ctx) {
				fmt.Printf("Daemon process found (PID %d) but IPC not responding.\n", pid)
				fmt.Println("The daemon may not have been started with --ipc flag.")
				fmt.Printf("Use 'kill %d' to forcefully terminate it.\n", pid)
				return nil
			}

			fmt.Printf("Stopping daemon (PID %d)...\n", pid)

			if err := client.Shutdown(ctx); err != nil {
				return fmt.Errorf("failed to send shutdown command: %w", err)
			}

			// Wait for daemon to exit
			for i := 0; i < 10; i++ {
				time.Sleep(500 * time.Millisecond)
				if daemon.IsDaemonRunning() == 0 {
					fmt.Println("Daemon stopped successfully.")
					return nil
				}
			}

			fmt.Println("Shutdown command sent. Daemon may still be cleaning up.")
			return nil
		},
	}

	return cmd
}

// newDaemonListCmd creates the 'daemon list' command.
func newDaemonListCmd() *cobra.Command {
	var (
		stateFile  string
		showFailed bool
		limit      int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List downloaded or failed jobs",
		Long: `List jobs that have been downloaded by the daemon.

Use --failed to show failed downloads instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load state
			state := daemon.NewState(stateFile)
			if err := state.Load(); err != nil {
				return fmt.Errorf("failed to load state: %w", err)
			}

			if showFailed {
				failed := state.GetFailedJobs()
				if len(failed) == 0 {
					fmt.Println("No failed downloads.")
					return nil
				}

				fmt.Printf("Failed Downloads (%d):\n\n", len(failed))
				for i, job := range failed {
					fmt.Printf("%d. %s (%s)\n", i+1, job.JobName, job.JobID)
					fmt.Printf("   Error: %s\n", job.Error)
					fmt.Printf("   Time: %s\n\n", job.DownloadedAt.Format(time.RFC3339))
				}
			} else {
				downloads := state.GetRecentDownloads(limit)
				if len(downloads) == 0 {
					fmt.Println("No downloaded jobs.")
					return nil
				}

				fmt.Printf("Downloaded Jobs (%d):\n\n", len(downloads))
				for i, job := range downloads {
					sizeMB := float64(job.TotalSize) / (1024 * 1024)
					fmt.Printf("%d. %s (%s)\n", i+1, job.JobName, job.JobID)
					fmt.Printf("   Files: %d (%.2f MB)\n", job.FileCount, sizeMB)
					fmt.Printf("   Location: %s\n", job.OutputDir)
					fmt.Printf("   Downloaded: %s\n\n", job.DownloadedAt.Format(time.RFC3339))
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&stateFile, "state-file", daemon.DefaultStateFilePath(), "Path to daemon state file")
	cmd.Flags().BoolVar(&showFailed, "failed", false, "Show failed downloads instead of successful ones")
	cmd.Flags().IntVar(&limit, "limit", 0, "Limit number of entries shown (0 = all)")

	return cmd
}

// newDaemonRetryCmd creates the 'daemon retry' command.
func newDaemonRetryCmd() *cobra.Command {
	var (
		stateFile string
		retryAll  bool
		jobIDs    []string
	)

	cmd := &cobra.Command{
		Use:   "retry",
		Short: "Retry failed job downloads",
		Long: `Mark failed jobs for retry on the next poll cycle.

This clears the failed status so the daemon will attempt to download
the job again during its next poll.

Examples:
  # Retry all failed jobs
  rescale-int daemon retry --all

  # Retry specific job
  rescale-int daemon retry --job-id XxYyZz`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !retryAll && len(jobIDs) == 0 {
				return fmt.Errorf("either --all or --job-id must be specified")
			}

			// Load state
			state := daemon.NewState(stateFile)
			if err := state.Load(); err != nil {
				return fmt.Errorf("failed to load state: %w", err)
			}

			if retryAll {
				failed := state.GetFailedJobs()
				if len(failed) == 0 {
					fmt.Println("No failed downloads to retry.")
					return nil
				}

				for _, job := range failed {
					state.ClearFailed(job.JobID)
					fmt.Printf("Marked for retry: %s (%s)\n", job.JobName, job.JobID)
				}

				if err := state.Save(); err != nil {
					return fmt.Errorf("failed to save state: %w", err)
				}

				fmt.Printf("\n%d job(s) marked for retry.\n", len(failed))
			} else {
				for _, jobID := range jobIDs {
					state.ClearFailed(jobID)
					fmt.Printf("Marked for retry: %s\n", jobID)
				}

				if err := state.Save(); err != nil {
					return fmt.Errorf("failed to save state: %w", err)
				}

				fmt.Printf("\n%d job(s) marked for retry.\n", len(jobIDs))
			}

			fmt.Println("\nRun 'rescale-int daemon run --once' to retry immediately,")
			fmt.Println("or wait for the next scheduled poll if daemon is running.")

			return nil
		},
	}

	cmd.Flags().StringVar(&stateFile, "state-file", daemon.DefaultStateFilePath(), "Path to daemon state file")
	cmd.Flags().BoolVar(&retryAll, "all", false, "Retry all failed jobs")
	cmd.Flags().StringArrayVarP(&jobIDs, "job-id", "j", nil, "Job ID to retry (can be specified multiple times)")

	return cmd
}

// resolveAbsolutePath converts a relative path to an absolute path.
func resolveAbsolutePath(path string) (string, error) {
	if path == "" {
		return os.Getwd()
	}

	// Expand ~ to home directory
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = home + path[1:]
	}

	return filepath.Abs(path)
}
