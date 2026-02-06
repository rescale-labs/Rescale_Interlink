//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/elevation"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/pathutil"
	"github.com/rescale/rescale-int/internal/service"
	"github.com/rescale/rescale-int/internal/version"
)

// Windows process creation flag to hide console window.
// v4.3.9: Required for subprocess mode to not show a blank console.
const createNoWindow = 0x08000000

const (
	// Status refresh interval
	refreshInterval = 5 * time.Second
)

// trayApp manages the system tray application state.
type trayApp struct {
	client *ipc.Client
	mu     sync.RWMutex

	// Current status
	serviceRunning   bool
	lastStatus       *ipc.StatusData
	lastError        string
	ipcConnected     bool // v4.5.0: Track IPC availability separately from service running
	serviceInstalled bool // v4.5.1: Track Windows Service installation status
	userConfigured   bool // v4.5.6: Track if user has daemon.conf with enabled=true

	// Menu items (for dynamic updates)
	mStatus            *systray.MenuItem
	mSetupRequired     *systray.MenuItem // v4.5.6: Setup guidance menu item
	mStartService      *systray.MenuItem
	mStartServiceAdmin   *systray.MenuItem // v4.5.1: Start Windows Service (Admin)
	mStopServiceAdmin    *systray.MenuItem // v4.5.1: Stop Windows Service (Admin)
	mInstallServiceAdmin *systray.MenuItem // v4.5.8: Install Windows Service (Admin)
	mUninstallServiceAdmin *systray.MenuItem // v4.5.8: Uninstall Windows Service (Admin)
	mPause             *systray.MenuItem
	mResume            *systray.MenuItem
	mTriggerScan       *systray.MenuItem
	mConfigure         *systray.MenuItem // v4.2.0: Opens GUI for configuration
	mOpenGUI           *systray.MenuItem
	mViewLogs          *systray.MenuItem
	mQuit              *systray.MenuItem

	// Control channels
	done chan struct{}
}

// runTray starts the system tray application.
func runTray() {
	systray.Run(onReady, onExit)
}

var app *trayApp

func onReady() {
	app = &trayApp{
		client: ipc.NewClient(),
		done:   make(chan struct{}),
	}
	app.client.SetTimeout(2 * time.Second)

	// Set initial tray icon and tooltip
	systray.SetIcon(iconData)
	systray.SetTitle("Rescale Interlink")
	systray.SetTooltip("Rescale Interlink - Connecting...")

	// Build menu
	app.mStatus = systray.AddMenuItem("Status: Checking...", "Service status")
	app.mStatus.Disable()

	// v4.5.6: Setup guidance menu item (shown when user hasn't configured auto-download)
	app.mSetupRequired = systray.AddMenuItem("Setup Required - Click to Configure", "Open GUI to enable auto-download")
	app.mSetupRequired.Hide() // Hidden by default, shown when needed

	systray.AddSeparator()

	// v4.5.1: Elevated service controls (when Windows Service installed)
	app.mStartServiceAdmin = systray.AddMenuItem("Start Service (Admin)", "Start Windows Service (requires administrator)")
	app.mStopServiceAdmin = systray.AddMenuItem("Stop Service (Admin)", "Stop Windows Service (requires administrator)")
	app.mStartServiceAdmin.Hide()
	app.mStopServiceAdmin.Hide()

	// v4.5.8: Elevated install/uninstall service controls
	app.mInstallServiceAdmin = systray.AddMenuItem("Install Service (Admin)", "Install Windows Service (requires administrator)")
	app.mUninstallServiceAdmin = systray.AddMenuItem("Uninstall Service (Admin)", "Uninstall Windows Service (requires administrator)")
	app.mInstallServiceAdmin.Hide()
	app.mUninstallServiceAdmin.Hide()

	// Subprocess mode control (when no Windows Service)
	app.mStartService = systray.AddMenuItem("Start Service", "Start the auto-download daemon")
	app.mPause = systray.AddMenuItem("Pause Auto-Download", "Pause auto-download for current user")
	app.mResume = systray.AddMenuItem("Resume Auto-Download", "Resume auto-download for current user")
	app.mTriggerScan = systray.AddMenuItem("Trigger Scan Now", "Trigger an immediate job scan")

	systray.AddSeparator()

	app.mConfigure = systray.AddMenuItem("Configure...", "Open GUI to edit daemon settings")
	app.mOpenGUI = systray.AddMenuItem("Open Interlink", "Open the main GUI application")

	systray.AddSeparator()

	app.mViewLogs = systray.AddMenuItem("View Logs", "Open log files location")

	systray.AddSeparator()

	app.mQuit = systray.AddMenuItem("Quit Tray", "Exit the tray application")

	// Start status refresh goroutine
	go app.refreshLoop()

	// Handle menu clicks
	go app.handleMenuClicks()
}

func onExit() {
	if app != nil {
		close(app.done)
	}
}

// refreshLoop periodically refreshes the service status.
func (a *trayApp) refreshLoop() {
	// Initial refresh
	a.refreshStatus()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.refreshStatus()
		case <-a.done:
			return
		}
	}
}

// refreshStatus fetches current status from the service via IPC.
// v4.5.5: Uses unified DetectDaemon() for consistent service detection.
// v4.5.6: Also tracks user configuration status for "Setup Required" indicator.
func (a *trayApp) refreshStatus() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// v4.5.6: Check if daemon.conf exists and is enabled for current user
	configPath, _ := config.DefaultDaemonConfigPath()
	userConfigured := false
	if _, err := os.Stat(configPath); err == nil {
		if cfg, err := config.LoadDaemonConfig(""); err == nil {
			userConfigured = cfg.Daemon.Enabled
		}
	}

	// v4.5.5: Try IPC first (remains source of truth when available)
	status, err := a.client.GetStatus(ctx)

	a.mu.Lock()
	defer a.mu.Unlock()

	// v4.5.6: Update user configuration status
	a.userConfigured = userConfigured

	if err != nil {
		// IPC failed - use unified detection as fallback
		// v4.5.5: This handles SCM-denied scenarios where IPC may also be slow
		detection := service.DetectDaemon()
		a.serviceInstalled = detection.ServiceMode || service.IsInstalled()
		a.serviceRunning = detection.ServiceMode || detection.SubprocessPID > 0 || detection.PipeInUse
		a.lastError = translateError(err)
		a.lastStatus = nil
		a.ipcConnected = false

		// v4.5.0: Existing fallback to SCM when IPC fails (keep existing logic)
		if a.serviceInstalled {
			if svcStatus, _ := service.QueryStatus(); svcStatus == service.StatusRunning {
				a.serviceRunning = true
				a.lastError = "Service running but IPC not responding"
			}
		}
		a.updateUI()
		return
	}

	// IPC succeeded - use IPC data (source of truth)
	a.serviceRunning = true
	a.lastStatus = status
	a.lastError = ""
	a.ipcConnected = true
	// v4.5.5: Use unified detection for serviceInstalled when IPC works too
	a.serviceInstalled = status.ServiceMode || service.IsInstalled()
	a.updateUI()
}

// updateUI updates the tray icon, tooltip, and menu items based on current state.
// Must be called with a.mu held.
// v4.5.6: Added setup required indicator when user hasn't configured auto-download.
func (a *trayApp) updateUI() {
	// v4.5.1: Handle Windows Service mode vs subprocess mode menu visibility
	// v4.5.8: Added install/uninstall service buttons
	if a.serviceInstalled {
		// Windows Service installed - show admin controls, hide subprocess controls
		a.mStartService.Hide()
		a.mInstallServiceAdmin.Hide() // Already installed

		if a.serviceRunning {
			// Service running - show stop option, hide uninstall (must stop first)
			a.mStartServiceAdmin.Hide()
			a.mStopServiceAdmin.Show()
			a.mStopServiceAdmin.Enable()
			a.mUninstallServiceAdmin.Hide()
		} else {
			// Service stopped - show start and uninstall options
			a.mStartServiceAdmin.Show()
			a.mStartServiceAdmin.Enable()
			a.mStopServiceAdmin.Hide()
			a.mUninstallServiceAdmin.Show()
			a.mUninstallServiceAdmin.Enable()
		}
	} else {
		// No Windows Service - hide admin controls, show subprocess controls and install option
		a.mStartServiceAdmin.Hide()
		a.mStopServiceAdmin.Hide()
		a.mUninstallServiceAdmin.Hide()
		a.mInstallServiceAdmin.Show()
		a.mInstallServiceAdmin.Enable()
	}

	// v4.5.6: Show/hide setup required menu item based on user configuration
	if !a.userConfigured && a.serviceRunning {
		a.mSetupRequired.Show()
	} else {
		a.mSetupRequired.Hide()
	}

	if !a.serviceRunning {
		// v4.4.2: Check if this is first-time setup (no daemon.conf exists)
		configPath, _ := config.DefaultDaemonConfigPath()
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			systray.SetTooltip(fmt.Sprintf("Rescale Interlink v%s\nFirst-time setup: Click 'Configure...' to set download folder", version.Version))
			a.mStatus.SetTitle("Status: Not Configured")
			a.mSetupRequired.Show() // v4.5.6: Show setup required
			if !a.serviceInstalled {
				a.mStartService.Enable()
				a.mStartService.Show()
			}
			a.mPause.Disable()
			a.mResume.Disable()
			a.mTriggerScan.Disable()
			return
		}

		// v4.3.8: Show last error if available (errors were previously stored but never displayed)
		if a.lastError != "" {
			systray.SetTooltip(fmt.Sprintf("Rescale Interlink v%s\nService: Not Running\nError: %s", version.Version, a.lastError))
			a.mStatus.SetTitle(fmt.Sprintf("Error: %s", truncate(a.lastError, 40)))
		} else {
			systray.SetTooltip(fmt.Sprintf("Rescale Interlink v%s\nService: Not Running", version.Version))
			a.mStatus.SetTitle("Status: Service Not Running")
		}
		if !a.serviceInstalled {
			a.mStartService.Enable()
			a.mStartService.Show()
		}
		a.mPause.Disable()
		a.mResume.Disable()
		a.mTriggerScan.Disable()
		return
	}

	// Service is running - hide Start Service button (subprocess mode)
	a.mStartService.Disable()
	a.mStartService.Hide()

	// v4.5.0: Check IPC availability for controls
	if !a.ipcConnected {
		// Service running but IPC unavailable - disable controls
		systray.SetTooltip(fmt.Sprintf("Rescale Interlink v%s\nService: Running (IPC unavailable)", version.Version))
		a.mStatus.SetTitle("Status: Running (IPC unavailable)")
		a.mPause.Disable()
		a.mResume.Disable()
		a.mTriggerScan.Disable()
		return
	}

	// Service is running with IPC available
	if a.lastStatus != nil {
		// v4.5.6: Show user-specific status in tooltip
		var userStatusLine string
		if !a.userConfigured {
			userStatusLine = "\nYour Auto-Download: Setup Required"
		} else {
			userStatusLine = "\nYour Auto-Download: Active"
		}

		tooltip := fmt.Sprintf("Rescale Interlink v%s\nService: %s%s\nActive Users: %d\nActive Downloads: %d",
			version.Version,
			a.lastStatus.ServiceState,
			userStatusLine,
			a.lastStatus.ActiveUsers,
			a.lastStatus.ActiveDownloads,
		)
		if a.lastStatus.LastScanTime != nil {
			tooltip += fmt.Sprintf("\nLast Scan: %s", a.lastStatus.LastScanTime.Format("15:04:05"))
		}
		if a.lastStatus.LastError != "" {
			tooltip += fmt.Sprintf("\nLast Error: %s", truncate(a.lastStatus.LastError, 50))
		}
		systray.SetTooltip(tooltip)

		// v4.5.6: Show user-specific status in menu
		var statusText string
		if !a.userConfigured {
			statusText = "Status: Setup Required"
		} else {
			statusText = fmt.Sprintf("Status: %s | %d users, %d downloads",
				a.lastStatus.ServiceState,
				a.lastStatus.ActiveUsers,
				a.lastStatus.ActiveDownloads,
			)
		}
		a.mStatus.SetTitle(statusText)

		// v4.5.6: Disable controls when user is not configured
		if !a.userConfigured {
			a.mPause.Disable()
			a.mResume.Disable()
			a.mTriggerScan.Disable()
		} else {
			// Enable/disable pause/resume based on state
			a.mPause.Enable()
			a.mResume.Enable()
			a.mTriggerScan.Enable()
		}
	} else {
		// v4.5.6: Show setup required if not configured
		if !a.userConfigured {
			systray.SetTooltip(fmt.Sprintf("Rescale Interlink v%s\nService: Running\nYour Auto-Download: Setup Required", version.Version))
			a.mStatus.SetTitle("Status: Setup Required")
			a.mPause.Disable()
			a.mResume.Disable()
			a.mTriggerScan.Disable()
		} else {
			systray.SetTooltip(fmt.Sprintf("Rescale Interlink v%s\nService: Running", version.Version))
			a.mStatus.SetTitle("Status: Running")
			a.mPause.Enable()
			a.mResume.Enable()
			a.mTriggerScan.Enable()
		}
	}
}

// handleMenuClicks processes menu item clicks.
func (a *trayApp) handleMenuClicks() {
	for {
		select {
		case <-a.mSetupRequired.ClickedCh:
			a.openGUI() // v4.5.6: Open GUI when user clicks "Setup Required"

		case <-a.mStartService.ClickedCh:
			a.startService()

		case <-a.mStartServiceAdmin.ClickedCh:
			a.startServiceElevated() // v4.5.1: UAC elevation

		case <-a.mStopServiceAdmin.ClickedCh:
			a.stopServiceElevated() // v4.5.1: UAC elevation

		case <-a.mConfigure.ClickedCh:
			a.openGUI() // v4.2.0: Configure opens GUI (same action, just more discoverable)

		case <-a.mOpenGUI.ClickedCh:
			a.openGUI()

		case <-a.mTriggerScan.ClickedCh:
			a.triggerScan()

		case <-a.mPause.ClickedCh:
			a.pauseAutoDownload()

		case <-a.mResume.ClickedCh:
			a.resumeAutoDownload()

		case <-a.mInstallServiceAdmin.ClickedCh:
			a.installServiceElevated() // v4.5.8: UAC elevation

		case <-a.mUninstallServiceAdmin.ClickedCh:
			a.uninstallServiceElevated() // v4.5.8: UAC elevation

		case <-a.mViewLogs.ClickedCh:
			a.viewLogs()

		case <-a.mQuit.ClickedCh:
			systray.Quit()
			return

		case <-a.done:
			return
		}
	}
}

// startService starts the auto-download daemon if not already running.
// v4.1.1: Added to allow users to start the daemon from the tray without
// opening the GUI or using the command line.
// v4.2.0: Reads settings from daemon.conf.
// v4.5.0: Blocks subprocess when Windows Service installed.
// v4.5.5: Uses unified detection - only blocks when service is RUNNING.
func (a *trayApp) startService() {
	// v4.5.5: Use unified detection instead of raw IsInstalled()
	if blocked, reason := service.ShouldBlockSubprocess(); blocked {
		a.mu.Lock()
		a.lastError = reason
		a.updateUI()
		a.mu.Unlock()
		return
	}

	// Find rescale-int.exe in the same directory as the tray app
	exePath, err := os.Executable()
	if err != nil {
		a.mu.Lock()
		a.lastError = translateError(fmt.Errorf("executable path: %w", err))
		a.serviceRunning = false // v4.4.3: Ensure UI reflects failed start
		a.updateUI()             // v4.4.3: Force immediate UI update while holding lock
		a.mu.Unlock()
		return
	}

	dir := filepath.Dir(exePath)
	cliPath := filepath.Join(dir, "rescale-int.exe")

	// Check if CLI exists
	if _, err := os.Stat(cliPath); os.IsNotExist(err) {
		a.mu.Lock()
		a.lastError = translateError(fmt.Errorf("CLI not found: rescale-int.exe"))
		a.serviceRunning = false // v4.4.3: Ensure UI reflects failed start
		a.updateUI()             // v4.4.3: Force immediate UI update while holding lock
		a.mu.Unlock()
		return
	}

	// v4.2.0: Load settings from daemon.conf
	// v4.4.2: Pre-flight validation before starting daemon
	daemonCfg, err := config.LoadDaemonConfig("")
	if err != nil {
		a.mu.Lock()
		a.lastError = "Configuration error. Open Interlink to configure."
		a.serviceRunning = false // v4.4.3: Ensure UI reflects failed start
		a.updateUI()             // v4.4.3: Force immediate UI update while holding lock
		a.mu.Unlock()
		return
	}

	downloadDir := daemonCfg.Daemon.DownloadFolder
	if downloadDir == "" {
		downloadDir = config.DefaultDownloadFolder()
	}

	// v4.4.3: Create download folder if it doesn't exist (replaced strict parent check)
	// The daemon also does MkdirAll, but pre-creating here gives better error messages
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		a.mu.Lock()
		a.lastError = fmt.Sprintf("Cannot create download folder: %s", err)
		a.serviceRunning = false // v4.4.3: Ensure UI reflects failed start
		a.updateUI()             // v4.4.3: Force immediate UI update while holding lock
		a.mu.Unlock()
		return
	}

	// v4.4.3: Use shared path resolution logic for consistent behavior
	// This helps when Downloads is a junction to a network drive (e.g., Z:\Downloads on Rescale VMs)
	// The subprocess may not have the same drive mappings as the tray app's session
	if resolved, err := pathutil.ResolveAbsolutePath(downloadDir); err == nil {
		downloadDir = resolved
	}

	pollInterval := fmt.Sprintf("%dm", daemonCfg.Daemon.PollIntervalMinutes)

	// v4.5.8: Add persistent log file for daemon subprocess
	daemonLogPath := filepath.Join(config.LogDirectory(), "daemon.log")

	// Build command arguments
	args := []string{"daemon", "run", "--ipc",
		"--download-dir", downloadDir,
		"--poll-interval", pollInterval,
		"--log-file", daemonLogPath, // v4.5.8: persistent daemon logging
	}

	// Add filter flags if configured
	if daemonCfg.Filters.NamePrefix != "" {
		args = append(args, "--name-prefix", daemonCfg.Filters.NamePrefix)
	}
	if daemonCfg.Filters.NameContains != "" {
		args = append(args, "--name-contains", daemonCfg.Filters.NameContains)
	}
	for _, ex := range daemonCfg.GetExcludePatterns() {
		args = append(args, "--exclude", ex)
	}
	if daemonCfg.Daemon.MaxConcurrent > 0 {
		args = append(args, "--max-concurrent", fmt.Sprintf("%d", daemonCfg.Daemon.MaxConcurrent))
	}

	// v4.3.8: Log startup attempt to help diagnose launch failures
	daemon.WriteStartupLog("=== TRAY STARTUP ATTEMPT ===")
	daemon.WriteStartupLog("CLI path: %s", cliPath)
	daemon.WriteStartupLog("Arguments: %v", args)

	// v4.3.8: Create stderr capture file for subprocess diagnostics
	// v4.4.2: Use centralized log directory
	// v4.5.1: Uses 0700 permissions to restrict log access to owner only
	logsDir := config.LogDirectory()
	if err := os.MkdirAll(logsDir, 0700); err != nil {
		daemon.WriteStartupLog("WARNING: Could not create logs directory: %v", err)
	}
	stderrPath := filepath.Join(logsDir, "daemon-stderr.log")
	stderrFile, stderrErr := os.Create(stderrPath)
	if stderrErr != nil {
		daemon.WriteStartupLog("WARNING: Could not create stderr capture file: %v", stderrErr)
	}

	// Start daemon with IPC enabled
	cmd := exec.Command(cliPath, args...)

	// v4.3.9: Windows process flags for proper subprocess detachment + hidden console
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
	}

	// Detach stdin/stdout, but capture stderr for debugging
	cmd.Stdin = nil
	cmd.Stdout = nil
	if stderrFile != nil {
		cmd.Stderr = stderrFile
	}

	daemon.WriteStartupLog("Calling cmd.Start()...")

	if err := cmd.Start(); err != nil {
		daemon.WriteStartupLog("ERROR: Failed to start service: %v", err)
		if stderrFile != nil {
			stderrFile.Close()
		}
		a.mu.Lock()
		a.lastError = translateError(err)
		a.serviceRunning = false // v4.4.3: Ensure UI reflects failed start
		a.updateUI()             // v4.4.3: Force immediate UI update while holding lock
		a.mu.Unlock()
		return
	}

	daemon.WriteStartupLog("SUCCESS: Started daemon subprocess with PID %d", cmd.Process.Pid)

	// Close stderr file after a delay to capture any immediate errors
	if stderrFile != nil {
		go func() {
			time.Sleep(3 * time.Second)
			stderrFile.Close()
		}()
	}

	// Wait for IPC to come up, then refresh status
	go func() {
		time.Sleep(2 * time.Second)
		a.refreshStatus()
	}()
}

// openGUI launches the main Rescale Interlink GUI.
func (a *trayApp) openGUI() {
	// Find rescale-int-gui.exe in the same directory as the tray app (v4.0.2+)
	exePath, err := os.Executable()
	if err != nil {
		a.mu.Lock()
		a.lastError = fmt.Sprintf("Failed to find executable path: %v", err)
		a.mu.Unlock()
		return
	}

	dir := filepath.Dir(exePath)

	// v4.0.2+: GUI is a separate binary (Wails-based with embedded frontend)
	guiPath := filepath.Join(dir, "rescale-int-gui.exe")

	// Check if it exists
	if _, err := os.Stat(guiPath); os.IsNotExist(err) {
		// Fallback: Try without -gui suffix (older installations)
		guiPath = filepath.Join(dir, "rescale-int.exe")
		if _, err := os.Stat(guiPath); os.IsNotExist(err) {
			// Try just "rescale-int-gui" (might be in PATH)
			guiPath = "rescale-int-gui"
		}
	}

	// Launch GUI
	cmd := exec.Command(guiPath)
	// v4.0.7 M5: Check for launch errors
	if err := cmd.Start(); err != nil {
		a.mu.Lock()
		a.lastError = fmt.Sprintf("Failed to launch GUI: %v", err)
		a.mu.Unlock()
	}
}

// triggerScan triggers an immediate job scan via IPC.
func (a *trayApp) triggerScan() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// v4.0.8: Resolve username on client side before sending to service
	username := getCurrentUsername()
	err := a.client.TriggerScan(ctx, username)
	if err != nil {
		a.mu.Lock()
		a.lastError = translateError(err)
		a.mu.Unlock()
	}

	// Refresh status after triggering scan
	time.Sleep(500 * time.Millisecond)
	a.refreshStatus()
}

// pauseAutoDownload pauses auto-download for the current user.
func (a *trayApp) pauseAutoDownload() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// v4.0.8: Resolve username on client side before sending to service
	username := getCurrentUsername()
	err := a.client.PauseUser(ctx, username)
	if err != nil {
		a.mu.Lock()
		a.lastError = translateError(err)
		a.mu.Unlock()
	}

	// Refresh status
	time.Sleep(500 * time.Millisecond)
	a.refreshStatus()
}

// resumeAutoDownload resumes auto-download for the current user.
func (a *trayApp) resumeAutoDownload() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// v4.0.8: Resolve username on client side before sending to service
	username := getCurrentUsername()
	err := a.client.ResumeUser(ctx, username)
	if err != nil {
		a.mu.Lock()
		a.lastError = translateError(err)
		a.mu.Unlock()
	}

	// Refresh status
	time.Sleep(500 * time.Millisecond)
	a.refreshStatus()
}

// viewLogs opens the logs directory in Explorer.
// v4.0.7: This runs locally in user context (not via IPC to service).
// v4.4.2: Uses centralized LogDirectory() for consistent log location.
func (a *trayApp) viewLogs() {
	// v4.4.2: Use centralized log directory path
	// v4.5.1: Uses 0700 permissions to restrict log access to owner only
	logsDir := config.LogDirectory()

	// Create if doesn't exist
	if err := os.MkdirAll(logsDir, 0700); err != nil {
		a.mu.Lock()
		a.lastError = "Failed to create logs directory"
		a.mu.Unlock()
		// Continue anyway - directory might already exist
	}

	// v4.0.7 M5: Check for launch errors
	if err := exec.Command("explorer.exe", logsDir).Start(); err != nil {
		a.mu.Lock()
		a.lastError = "Failed to open logs directory"
		a.mu.Unlock()
	}
}

// startServiceElevated triggers UAC to start the Windows Service.
// v4.5.1: Uses elevation.StartServiceElevated() which calls "rescale-int service start".
// v4.5.8: Removed IsInstalled() pre-check that blocked elevation on restricted VMs
// where SCM access is denied. The elevated process reports errors properly.
func (a *trayApp) startServiceElevated() {
	// Don't gate on IsInstalled() - SCM may be inaccessible from non-admin context.
	// The elevated "rescale-int service start" will report errors properly.
	daemon.WriteStartupLog("=== TRAY ELEVATED START SERVICE ===")

	if err := elevation.StartServiceElevated(); err != nil {
		daemon.WriteStartupLog("ERROR: UAC elevation failed: %v", err)
		a.mu.Lock()
		a.lastError = translateError(err)
		a.updateUI()
		a.mu.Unlock()
		return
	}

	daemon.WriteStartupLog("SUCCESS: UAC approved, service start command executed")

	// Wait for service to start, then refresh status
	go func() {
		time.Sleep(2 * time.Second)
		a.refreshStatus()
	}()
}

// stopServiceElevated triggers UAC to stop the Windows Service.
// v4.5.1: Uses elevation.StopServiceElevated() which calls "rescale-int service stop".
// v4.5.8: Removed IsInstalled() pre-check that blocked elevation on restricted VMs
// where SCM access is denied. The elevated process reports errors properly.
func (a *trayApp) stopServiceElevated() {
	// Don't gate on IsInstalled() - SCM may be inaccessible from non-admin context.
	// The elevated "rescale-int service stop" will report errors properly.
	daemon.WriteStartupLog("=== TRAY ELEVATED STOP SERVICE ===")

	if err := elevation.StopServiceElevated(); err != nil {
		daemon.WriteStartupLog("ERROR: UAC elevation failed: %v", err)
		a.mu.Lock()
		a.lastError = translateError(err)
		a.updateUI()
		a.mu.Unlock()
		return
	}

	daemon.WriteStartupLog("SUCCESS: UAC approved, service stop command executed")

	// Wait for service to stop, then refresh status
	go func() {
		time.Sleep(2 * time.Second)
		a.refreshStatus()
	}()
}

// installServiceElevated triggers UAC to install the Windows Service.
// v4.5.8: Added for tray parity with GUI InstallServiceElevated().
func (a *trayApp) installServiceElevated() {
	daemon.WriteStartupLog("=== TRAY ELEVATED INSTALL SERVICE ===")

	if err := elevation.InstallServiceElevated(); err != nil {
		daemon.WriteStartupLog("ERROR: UAC elevation failed: %v", err)
		a.mu.Lock()
		a.lastError = translateError(err)
		a.updateUI()
		a.mu.Unlock()
		return
	}

	daemon.WriteStartupLog("SUCCESS: UAC approved, service install command executed")

	// Refresh status after install
	go func() {
		time.Sleep(2 * time.Second)
		a.refreshStatus()
	}()
}

// uninstallServiceElevated triggers UAC to uninstall the Windows Service.
// v4.5.8: Added for tray parity with GUI UninstallServiceElevated().
func (a *trayApp) uninstallServiceElevated() {
	daemon.WriteStartupLog("=== TRAY ELEVATED UNINSTALL SERVICE ===")

	if err := elevation.UninstallServiceElevated(); err != nil {
		daemon.WriteStartupLog("ERROR: UAC elevation failed: %v", err)
		a.mu.Lock()
		a.lastError = translateError(err)
		a.updateUI()
		a.mu.Unlock()
		return
	}

	daemon.WriteStartupLog("SUCCESS: UAC approved, service uninstall command executed")

	// Refresh status after uninstall
	go func() {
		time.Sleep(2 * time.Second)
		a.refreshStatus()
	}()
}

// truncate shortens a string to maxLen, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// translateError converts technical errors to user-friendly messages.
// v4.4.2: Improves UX by showing actionable messages instead of raw Go errors.
func translateError(err error) string {
	if err == nil {
		return ""
	}
	errStr := err.Error()

	switch {
	case strings.Contains(errStr, "pipe\\rescale-interlink") ||
		strings.Contains(errStr, "The system cannot find the file specified"):
		return "Service not running. Click 'Start Service' to begin."
	case strings.Contains(errStr, "CLI not found"):
		return "Interlink CLI not found. Please reinstall."
	case strings.Contains(errStr, "failed to load daemon.conf"):
		return "Configuration not found. Using defaults."
	case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded"):
		return "Service not responding. Try restarting."
	case strings.Contains(errStr, "access denied") || strings.Contains(errStr, "Access is denied") ||
		strings.Contains(errStr, "permission"):
		return "Permission denied. Run as administrator."
	case strings.Contains(errStr, "already running"):
		return "Service is already running."
	case strings.Contains(errStr, "executable path"):
		return "Cannot find Interlink executable."
	default:
		// Keep short errors, truncate long ones intelligently
		if len(errStr) > 60 {
			return errStr[:57] + "..."
		}
		return errStr
	}
}

// getCurrentUsername returns the current Windows username for IPC calls.
// v4.0.8: This resolves the username on the client side (tray app) before sending
// to the service. This is critical because when the service runs as SYSTEM,
// os.Getenv("USERNAME") returns "SYSTEM" instead of the actual user.
//
// The tray app always runs in user context, so we can reliably get the username here.
func getCurrentUsername() string {
	// Try USERNAME environment variable first (most common on Windows)
	if username := os.Getenv("USERNAME"); username != "" {
		return username
	}

	// Fallback: extract from user's home directory
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Base(home)
	}

	// Last resort: return "current" and let the service try to resolve
	return "current"
}
