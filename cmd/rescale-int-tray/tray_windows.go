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
	"github.com/rescale/rescale-int/internal/ipc"
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
	serviceRunning bool
	lastStatus     *ipc.StatusData
	lastError      string

	// Menu items (for dynamic updates)
	mStatus       *systray.MenuItem
	mStartService *systray.MenuItem
	mPause        *systray.MenuItem
	mResume       *systray.MenuItem
	mTriggerScan  *systray.MenuItem
	mConfigure    *systray.MenuItem // v4.2.0: Opens GUI for configuration
	mOpenGUI      *systray.MenuItem
	mViewLogs     *systray.MenuItem
	mQuit         *systray.MenuItem

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

	systray.AddSeparator()

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
func (a *trayApp) refreshStatus() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	status, err := a.client.GetStatus(ctx)

	a.mu.Lock()
	defer a.mu.Unlock()

	if err != nil {
		a.serviceRunning = false
		a.lastError = translateError(err)
		a.lastStatus = nil
		a.updateUI()
		return
	}

	a.serviceRunning = true
	a.lastStatus = status
	a.lastError = ""
	a.updateUI()
}

// updateUI updates the tray icon, tooltip, and menu items based on current state.
// Must be called with a.mu held.
func (a *trayApp) updateUI() {
	if !a.serviceRunning {
		// v4.4.2: Check if this is first-time setup (no daemon.conf exists)
		configPath, _ := config.DefaultDaemonConfigPath()
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			systray.SetTooltip(fmt.Sprintf("Rescale Interlink v%s\nFirst-time setup: Click 'Configure...' to set download folder", version.Version))
			a.mStatus.SetTitle("Status: Not Configured")
			a.mStartService.Enable()
			a.mStartService.Show()
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
		a.mStartService.Enable()
		a.mStartService.Show()
		a.mPause.Disable()
		a.mResume.Disable()
		a.mTriggerScan.Disable()
		return
	}

	// Service is running - hide Start Service button
	a.mStartService.Disable()
	a.mStartService.Hide()

	// Service is running
	if a.lastStatus != nil {
		tooltip := fmt.Sprintf("Rescale Interlink v%s\nService: %s\nActive Users: %d\nActive Downloads: %d",
			version.Version,
			a.lastStatus.ServiceState,
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

		statusText := fmt.Sprintf("Status: %s | %d users, %d downloads",
			a.lastStatus.ServiceState,
			a.lastStatus.ActiveUsers,
			a.lastStatus.ActiveDownloads,
		)
		a.mStatus.SetTitle(statusText)

		// Enable/disable pause/resume based on state
		// For now, enable both and let the server handle state
		a.mPause.Enable()
		a.mResume.Enable()
		a.mTriggerScan.Enable()
	} else {
		systray.SetTooltip(fmt.Sprintf("Rescale Interlink v%s\nService: Running", version.Version))
		a.mStatus.SetTitle("Status: Running")
		a.mPause.Enable()
		a.mResume.Enable()
		a.mTriggerScan.Enable()
	}
}

// handleMenuClicks processes menu item clicks.
func (a *trayApp) handleMenuClicks() {
	for {
		select {
		case <-a.mStartService.ClickedCh:
			a.startService()

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
func (a *trayApp) startService() {
	// Find rescale-int.exe in the same directory as the tray app
	exePath, err := os.Executable()
	if err != nil {
		a.mu.Lock()
		a.lastError = translateError(fmt.Errorf("executable path: %w", err))
		a.mu.Unlock()
		return
	}

	dir := filepath.Dir(exePath)
	cliPath := filepath.Join(dir, "rescale-int.exe")

	// Check if CLI exists
	if _, err := os.Stat(cliPath); os.IsNotExist(err) {
		a.mu.Lock()
		a.lastError = translateError(fmt.Errorf("CLI not found: rescale-int.exe"))
		a.mu.Unlock()
		return
	}

	// v4.2.0: Load settings from daemon.conf
	// v4.4.2: Pre-flight validation before starting daemon
	daemonCfg, err := config.LoadDaemonConfig("")
	if err != nil {
		a.mu.Lock()
		a.lastError = "Configuration error. Open Interlink to configure."
		a.mu.Unlock()
		return
	}

	downloadDir := daemonCfg.Daemon.DownloadFolder
	if downloadDir == "" {
		downloadDir = config.DefaultDownloadFolder()
	}

	// v4.4.2: Validate download folder parent exists or can be created
	parent := filepath.Dir(downloadDir)
	if _, err := os.Stat(parent); os.IsNotExist(err) {
		a.mu.Lock()
		a.lastError = fmt.Sprintf("Download folder parent doesn't exist: %s", parent)
		a.mu.Unlock()
		return
	}

	// v4.4.2: Resolve junctions/symlinks to physical paths before passing to daemon
	// This helps when Downloads is a junction to a network drive (e.g., Z:\Downloads on Rescale VMs)
	// The subprocess may not have the same drive mappings as the tray app's session
	if resolved, err := filepath.EvalSymlinks(downloadDir); err == nil {
		downloadDir = resolved
	}

	pollInterval := fmt.Sprintf("%dm", daemonCfg.Daemon.PollIntervalMinutes)

	// Build command arguments
	args := []string{"daemon", "run", "--ipc",
		"--download-dir", downloadDir,
		"--poll-interval", pollInterval,
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
	logsDir := config.LogDirectory()
	if err := os.MkdirAll(logsDir, 0755); err != nil {
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
	logsDir := config.LogDirectory()

	// Create if doesn't exist
	if err := os.MkdirAll(logsDir, 0755); err != nil {
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
