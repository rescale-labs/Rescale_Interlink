// Package cli provides the daemon CLI commands for rescale-int.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/logging"
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

v4.2.0: The daemon reads settings from ~/.config/rescale/daemon.conf (Unix) or
%APPDATA%\Rescale\Interlink\daemon.conf (Windows). CLI flags override config
file values. Use 'daemon config' commands to manage the config file.

Examples:
  # Start daemon using daemon.conf settings
  rescale-int daemon run

  # Start daemon with CLI flags (override config file)
  rescale-int daemon run --download-dir ./results

  # Start daemon in background with IPC control
  rescale-int daemon run --background --ipc

  # Show/edit daemon configuration
  rescale-int daemon config show
  rescale-int daemon config edit

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
	cmd.AddCommand(newDaemonConfigCmd())

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

v4.2.0: Settings are read from daemon.conf at startup. CLI flags override
config file values. If no config file exists, defaults are used.

By default, the daemon runs in foreground mode. Use --background to
detach from the terminal and run as a background process.

When --ipc is enabled, the daemon listens for control commands via
Unix socket (Mac/Linux) or named pipe (Windows), allowing other
processes (like the GUI) to query status, pause/resume, and stop
the daemon.

Press Ctrl+C to stop a foreground daemon gracefully.

Examples:
  # Start daemon using daemon.conf settings
  rescale-int daemon run

  # Override download directory from CLI
  rescale-int daemon run --download-dir /path/to/results

  # Background mode with IPC control (recommended for GUI integration)
  rescale-int daemon run --background --ipc

  # Poll every 2 minutes (overrides config)
  rescale-int daemon run --poll-interval 2m

  # Run once and exit (useful for cron jobs)
  rescale-int daemon run --once`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// v4.3.8: Early startup logging for debugging Windows subprocess launch issues
			// This writes to a file BEFORE the logger is fully initialized
			if runtime.GOOS == "windows" {
				daemon.WriteStartupLog("=== DAEMON CLI STARTING ===")
				daemon.WriteStartupLog("PID: %d", os.Getpid())
				daemon.WriteStartupLog("Args: %v", os.Args)
				if wd, err := os.Getwd(); err == nil {
					daemon.WriteStartupLog("Working directory: %s", wd)
				}
			}

			// v4.3.2: Create daemon-specific logger with log buffer for IPC streaming
			// The logWriter captures logs for both console output and IPC retrieval
			logWriter := daemon.NewDaemonLogWriter(daemon.DaemonLogConfig{
				Console:    !daemon.IsDaemonChild(), // Console output only in foreground
				BufferSize: 1000,                    // Keep 1000 log entries for IPC
			})
			logger := logging.NewLoggerWithWriter(logWriter)

			// Load daemon config file (v4.2.0+)
			daemonConf, err := config.LoadDaemonConfig("")
			if err != nil {
				logger.Warn().Err(err).Msg("Failed to load daemon.conf, using defaults")
				daemonConf = config.NewDaemonConfig()
			}

			// Apply config file values as defaults, CLI flags override
			// Only override if the flag was actually set by the user
			if !cmd.Flags().Changed("download-dir") && daemonConf.Daemon.DownloadFolder != "" {
				downloadDir = daemonConf.Daemon.DownloadFolder
			}
			if !cmd.Flags().Changed("poll-interval") && daemonConf.Daemon.PollIntervalMinutes > 0 {
				pollInterval = fmt.Sprintf("%dm", daemonConf.Daemon.PollIntervalMinutes)
			}
			if !cmd.Flags().Changed("max-concurrent") && daemonConf.Daemon.MaxConcurrent > 0 {
				maxConcurrent = daemonConf.Daemon.MaxConcurrent
			}
			if !cmd.Flags().Changed("use-job-id") {
				useJobID = !daemonConf.Daemon.UseJobNameDir
			}
			if !cmd.Flags().Changed("name-prefix") && daemonConf.Filters.NamePrefix != "" {
				namePrefix = daemonConf.Filters.NamePrefix
			}
			if !cmd.Flags().Changed("name-contains") && daemonConf.Filters.NameContains != "" {
				nameContains = daemonConf.Filters.NameContains
			}
			if !cmd.Flags().Changed("exclude") && daemonConf.Filters.Exclude != "" {
				excludeNames = daemonConf.GetExcludePatterns()
			}

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
				downloadDir = config.DefaultDownloadFolder()
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

			// v4.3.0: Simplified eligibility - mode is per-job, only tag and lookback configurable
			daemonCfg.Eligibility = &daemon.EligibilityConfig{
				AutoDownloadTag: daemonConf.Eligibility.AutoDownloadTag,
				LookbackDays:    daemonConf.Daemon.LookbackDays,
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
			// v4.3.7: Enable IPC on Windows (previously excluded with runtime.GOOS != "windows")
			// Windows IPC uses named pipes (\\.\pipe\rescale-interlink) which work without admin.
			// This allows GUI/tray to communicate with daemon running as subprocess.
			var ipcServer *ipc.Server
			if enableIPC {
				if runtime.GOOS == "windows" {
					daemon.WriteStartupLog("Starting IPC server...")
				}
				ipcHandler := daemon.NewIPCHandler(d, shutdownFunc)
				// v4.3.2: Connect log buffer for IPC log streaming
				ipcHandler.SetLogBuffer(logWriter.GetBuffer())
				ipcServer = ipc.NewServer(ipcHandler, logger)
				if err := ipcServer.Start(); err != nil {
					if runtime.GOOS == "windows" {
						daemon.WriteStartupLog("ERROR: IPC server failed to start: %v", err)
					}
					return fmt.Errorf("failed to start IPC server: %w", err)
				}
				defer ipcServer.Stop()
				logger.Info().Str("socket", ipcServer.GetSocketPath()).Msg("IPC server listening")

				// v4.3.9: Log successful IPC start but DON'T clear startup log
				// Keeping the log aids debugging - users can see the full startup sequence
				if runtime.GOOS == "windows" {
					daemon.WriteStartupLog("SUCCESS: IPC server started at %s", ipcServer.GetSocketPath())
					// Note: Previously called ClearStartupLog() here, removed in v4.3.9 for better diagnostics
				}
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

	// Set default values - these can be overridden by daemon.conf at runtime
	cmd.Flags().StringVarP(&downloadDir, "download-dir", "d", "", "Directory to download job outputs to (default: from daemon.conf or ~/Downloads/rescale-jobs)")
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

// newDaemonConfigCmd creates the 'daemon config' command group.
func newDaemonConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "View and manage daemon configuration",
		Long: `View and manage the daemon configuration file (daemon.conf).

The daemon configuration file stores settings for the auto-download service:
- Download folder
- Poll interval
- Job name filters
- Notification settings

Location:
  - Unix: ~/.config/rescale/daemon.conf
  - Windows: %APPDATA%\Rescale\Interlink\daemon.conf

Examples:
  # Show current configuration
  rescale-int daemon config show

  # Show config file path
  rescale-int daemon config path

  # Edit config in default editor
  rescale-int daemon config edit

  # Set a specific value
  rescale-int daemon config set download_folder /path/to/downloads
  rescale-int daemon config set poll_interval_minutes 10`,
	}

	cmd.AddCommand(newDaemonConfigShowCmd())
	cmd.AddCommand(newDaemonConfigPathCmd())
	cmd.AddCommand(newDaemonConfigEditCmd())
	cmd.AddCommand(newDaemonConfigSetCmd())
	cmd.AddCommand(newDaemonConfigInitCmd())
	cmd.AddCommand(newDaemonConfigValidateCmd())

	return cmd
}

// newDaemonConfigShowCmd creates the 'daemon config show' command.
func newDaemonConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Display the current daemon configuration",
		Long: `Display the current daemon configuration.

Shows all settings from daemon.conf, or defaults if the file doesn't exist.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDaemonConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			path, _ := config.DefaultDaemonConfigPath()
			if _, err := os.Stat(path); os.IsNotExist(err) {
				fmt.Printf("Config file: %s (not created yet, showing defaults)\n\n", path)
			} else {
				fmt.Printf("Config file: %s\n\n", path)
			}

			fmt.Println("[daemon]")
			fmt.Printf("enabled = %t\n", cfg.Daemon.Enabled)
			fmt.Printf("download_folder = %s\n", cfg.Daemon.DownloadFolder)
			fmt.Printf("poll_interval_minutes = %d\n", cfg.Daemon.PollIntervalMinutes)
			fmt.Printf("use_job_name_dir = %t\n", cfg.Daemon.UseJobNameDir)
			fmt.Printf("max_concurrent = %d\n", cfg.Daemon.MaxConcurrent)
			fmt.Printf("lookback_days = %d\n", cfg.Daemon.LookbackDays)
			fmt.Println()

			fmt.Println("[filters]")
			fmt.Printf("name_prefix = %s\n", cfg.Filters.NamePrefix)
			fmt.Printf("name_contains = %s\n", cfg.Filters.NameContains)
			fmt.Printf("exclude = %s\n", cfg.Filters.Exclude)
			fmt.Println()

			fmt.Println("[eligibility]")
			fmt.Printf("auto_download_tag = %s\n", cfg.Eligibility.AutoDownloadTag)
			fmt.Println()
			fmt.Println("# Note: Mode (Enabled/Conditional/Disabled) is set per-job via the")
			fmt.Println("# 'Auto Download' custom field in Rescale workspace, not here.")
			fmt.Printf("# Downloaded tag (hardcoded): %s\n", config.DownloadedTag)
			fmt.Println()

			fmt.Println("[notifications]")
			fmt.Printf("enabled = %t\n", cfg.Notifications.Enabled)
			fmt.Printf("show_download_complete = %t\n", cfg.Notifications.ShowDownloadComplete)
			fmt.Printf("show_download_failed = %t\n", cfg.Notifications.ShowDownloadFailed)

			return nil
		},
	}
}

// newDaemonConfigPathCmd creates the 'daemon config path' command.
func newDaemonConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Show the daemon configuration file path",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.DefaultDaemonConfigPath()
			if err != nil {
				return fmt.Errorf("failed to determine config path: %w", err)
			}
			fmt.Println(path)
			return nil
		},
	}
}

// newDaemonConfigEditCmd creates the 'daemon config edit' command.
func newDaemonConfigEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the daemon configuration file in your default editor",
		Long: `Open the daemon configuration file in your default editor.

Uses $EDITOR environment variable, or falls back to:
  - Unix: vi, nano
  - Windows: notepad`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.DefaultDaemonConfigPath()
			if err != nil {
				return fmt.Errorf("failed to determine config path: %w", err)
			}

			// Create config with defaults if it doesn't exist
			if _, err := os.Stat(path); os.IsNotExist(err) {
				cfg := config.NewDaemonConfig()
				if err := config.SaveDaemonConfig(cfg, path); err != nil {
					return fmt.Errorf("failed to create config file: %w", err)
				}
				fmt.Printf("Created new config file: %s\n", path)
			}

			// Find editor
			editor := os.Getenv("EDITOR")
			if editor == "" {
				if runtime.GOOS == "windows" {
					editor = "notepad"
				} else {
					// Try common editors
					for _, e := range []string{"vim", "vi", "nano"} {
						if _, err := exec.LookPath(e); err == nil {
							editor = e
							break
						}
					}
					if editor == "" {
						return fmt.Errorf("no editor found; set $EDITOR environment variable")
					}
				}
			}

			// Open editor
			editCmd := exec.Command(editor, path)
			editCmd.Stdin = os.Stdin
			editCmd.Stdout = os.Stdout
			editCmd.Stderr = os.Stderr

			return editCmd.Run()
		},
	}
}

// newDaemonConfigSetCmd creates the 'daemon config set' command.
func newDaemonConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a daemon configuration value",
		Long: `Set a specific configuration value in daemon.conf.

Available keys:
  [daemon]
    enabled                  - true/false
    download_folder          - path to download directory
    poll_interval_minutes    - polling interval (1-1440)
    use_job_name_dir         - true/false
    max_concurrent           - concurrent downloads (1-10)
    lookback_days            - days to look back (1-365)

  [filters]
    name_prefix              - job name prefix filter
    name_contains            - job name contains filter
    exclude                  - comma-separated exclude patterns

  [eligibility]
    auto_download_tag        - tag to check for jobs with "Conditional" mode

  [notifications]
    notifications_enabled    - true/false
    show_download_complete   - true/false
    show_download_failed     - true/false

Note (v4.3.0): Mode (Enabled/Conditional/Disabled) is now set per-job via the
"Auto Download" custom field in your Rescale workspace, not in this config.

Examples:
  rescale-int daemon config set download_folder /path/to/downloads
  rescale-int daemon config set poll_interval_minutes 10
  rescale-int daemon config set auto_download_tag autoDownload
  rescale-int daemon config set exclude "test,debug,scratch"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			value := args[1]

			// Load existing config
			cfg, err := config.LoadDaemonConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Set the value
			switch key {
			// [daemon] section
			case "enabled":
				cfg.Daemon.Enabled = value == "true" || value == "1" || value == "yes"
			case "download_folder":
				absPath, err := resolveAbsolutePath(value)
				if err != nil {
					return fmt.Errorf("invalid path: %w", err)
				}
				cfg.Daemon.DownloadFolder = absPath
			case "poll_interval_minutes":
				var v int
				if _, err := fmt.Sscanf(value, "%d", &v); err != nil {
					return fmt.Errorf("invalid integer: %s", value)
				}
				if v < 1 || v > 1440 {
					return fmt.Errorf("poll_interval_minutes must be between 1 and 1440")
				}
				cfg.Daemon.PollIntervalMinutes = v
			case "use_job_name_dir":
				cfg.Daemon.UseJobNameDir = value == "true" || value == "1" || value == "yes"
			case "max_concurrent":
				var v int
				if _, err := fmt.Sscanf(value, "%d", &v); err != nil {
					return fmt.Errorf("invalid integer: %s", value)
				}
				if v < 1 || v > 10 {
					return fmt.Errorf("max_concurrent must be between 1 and 10")
				}
				cfg.Daemon.MaxConcurrent = v
			case "lookback_days":
				var v int
				if _, err := fmt.Sscanf(value, "%d", &v); err != nil {
					return fmt.Errorf("invalid integer: %s", value)
				}
				if v < 1 || v > 365 {
					return fmt.Errorf("lookback_days must be between 1 and 365")
				}
				cfg.Daemon.LookbackDays = v

			// [filters] section
			case "name_prefix":
				cfg.Filters.NamePrefix = value
			case "name_contains":
				cfg.Filters.NameContains = value
			case "exclude":
				cfg.Filters.Exclude = value

			// [eligibility] section
			// v4.3.0: Simplified - only auto_download_tag is configurable
			case "auto_download_tag":
				cfg.Eligibility.AutoDownloadTag = value
			case "correctness_tag": // backwards compatibility alias
				cfg.Eligibility.AutoDownloadTag = value
				fmt.Println("Note: 'correctness_tag' is deprecated, use 'auto_download_tag' instead")
			case "mode", "auto_download_value", "downloaded_tag":
				fmt.Println("Note: This setting is no longer configurable in v4.3.0+")
				fmt.Println("Mode is now set per-job via the 'Auto Download' custom field in Rescale workspace.")
				return nil

			// [notifications] section
			case "notifications_enabled":
				cfg.Notifications.Enabled = value == "true" || value == "1" || value == "yes"
			case "show_download_complete":
				cfg.Notifications.ShowDownloadComplete = value == "true" || value == "1" || value == "yes"
			case "show_download_failed":
				cfg.Notifications.ShowDownloadFailed = value == "true" || value == "1" || value == "yes"

			default:
				return fmt.Errorf("unknown configuration key: %s", key)
			}

			// Save config
			if err := config.SaveDaemonConfig(cfg, ""); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Printf("Set %s = %s\n", key, value)
			return nil
		},
	}
}

// newDaemonConfigInitCmd creates the 'daemon config init' command.
func newDaemonConfigInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create a new daemon configuration file with default values",
		Long: `Create a new daemon configuration file with default values.

If a config file already exists, this command will not overwrite it.
Use 'daemon config edit' to modify an existing config.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.DefaultDaemonConfigPath()
			if err != nil {
				return fmt.Errorf("failed to determine config path: %w", err)
			}

			// Check if file already exists
			if _, err := os.Stat(path); err == nil {
				fmt.Printf("Config file already exists: %s\n", path)
				fmt.Println("Use 'rescale-int daemon config edit' to modify it.")
				return nil
			}

			// Create with defaults
			cfg := config.NewDaemonConfig()
			if err := config.SaveDaemonConfig(cfg, path); err != nil {
				return fmt.Errorf("failed to create config file: %w", err)
			}

			fmt.Printf("Created config file: %s\n\n", path)
			fmt.Println("Default settings:")
			fmt.Printf("  Download folder: %s\n", cfg.Daemon.DownloadFolder)
			fmt.Printf("  Poll interval: %d minutes\n", cfg.Daemon.PollIntervalMinutes)
			fmt.Printf("  Max concurrent: %d\n", cfg.Daemon.MaxConcurrent)
			fmt.Printf("  Auto-download enabled: %t\n", cfg.Daemon.Enabled)
			fmt.Println()
			fmt.Println("Use 'rescale-int daemon config edit' to customize settings.")
			fmt.Println("Use 'rescale-int daemon config set <key> <value>' to set individual values.")

			return nil
		},
	}
}

// newDaemonConfigValidateCmd creates the 'daemon config validate' command.
// v4.2.1: Added for workspace custom fields validation
func newDaemonConfigValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate auto-download workspace configuration",
		Long: `Validate that your Rescale workspace is properly configured for auto-download.

This command checks:
1. Whether custom fields are enabled for your workspace
2. Whether the required "Auto Download" custom field exists
3. Whether the optional "Auto Download Path" custom field exists

The auto-download daemon requires a custom field named "Auto Download" to be
configured in your Rescale workspace. Jobs with this field set to the configured
value (default: "Enable") will be automatically downloaded when completed.

To create the custom field:
1. Go to Rescale Platform → Workspace Settings → Custom Fields
2. Create a new Job custom field named "Auto Download"
3. Set Type to "Select" (dropdown) or "Text"
4. If using Select, add options like "Enable", "Disable"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load app config for API client
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if cfg.APIKey == "" {
				return fmt.Errorf("no API key configured. Set RESCALE_API_KEY or use --token-file")
			}

			// Create API client
			apiClient, err := api.NewClient(cfg)
			if err != nil {
				return fmt.Errorf("failed to create API client: %w", err)
			}

			fmt.Println("Validating auto-download workspace configuration...")
			fmt.Println()

			// Run validation
			ctx := cmd.Context()
			validation, err := apiClient.ValidateAutoDownloadSetup(ctx)
			if err != nil {
				return fmt.Errorf("validation failed: %w", err)
			}

			// Display results
			fmt.Printf("Custom Fields Enabled: %t\n", validation.CustomFieldsEnabled)
			fmt.Printf("'Auto Download' Field: %t\n", validation.HasAutoDownloadField)

			if validation.HasAutoDownloadField {
				fmt.Printf("  - Type: %s\n", validation.AutoDownloadFieldType)
				fmt.Printf("  - Section: %s\n", validation.AutoDownloadFieldSection)
				if len(validation.AvailableValues) > 0 {
					fmt.Printf("  - Values: %v\n", validation.AvailableValues)
				}
			}

			fmt.Printf("'Auto Download Path' Field: %t (optional)\n", validation.HasAutoDownloadPathField)
			fmt.Println()

			// Show errors
			if len(validation.Errors) > 0 {
				fmt.Println("ERRORS:")
				for _, e := range validation.Errors {
					fmt.Printf("  ✗ %s\n", e)
				}
				fmt.Println()
			}

			// Show warnings
			if len(validation.Warnings) > 0 {
				fmt.Println("WARNINGS:")
				for _, w := range validation.Warnings {
					fmt.Printf("  ⚠ %s\n", w)
				}
				fmt.Println()
			}

			// Summary
			if len(validation.Errors) == 0 && len(validation.Warnings) == 0 {
				fmt.Println("✓ Workspace is properly configured for auto-download.")
			} else if len(validation.Errors) == 0 {
				fmt.Println("✓ Workspace is configured for auto-download (with warnings).")
			} else {
				fmt.Println("✗ Workspace needs configuration before auto-download can work.")
				return fmt.Errorf("validation failed with %d error(s)", len(validation.Errors))
			}

			return nil
		},
	}
}
