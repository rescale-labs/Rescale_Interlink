//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/rescale/rescale-int/internal/ipc"
)

const (
	// Status refresh interval
	refreshInterval = 5 * time.Second

	// Version displayed in tooltip
	trayVersion = "4.0.7"
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
	mStatus      *systray.MenuItem
	mPause       *systray.MenuItem
	mResume      *systray.MenuItem
	mTriggerScan *systray.MenuItem
	mOpenGUI     *systray.MenuItem
	mViewLogs    *systray.MenuItem
	mQuit        *systray.MenuItem

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

	app.mOpenGUI = systray.AddMenuItem("Open Interlink", "Open the main GUI application")
	app.mTriggerScan = systray.AddMenuItem("Trigger Scan Now", "Trigger an immediate job scan")

	systray.AddSeparator()

	app.mPause = systray.AddMenuItem("Pause Auto-Download", "Pause auto-download for current user")
	app.mResume = systray.AddMenuItem("Resume Auto-Download", "Resume auto-download for current user")

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
		a.lastError = err.Error()
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
		systray.SetTooltip(fmt.Sprintf("Rescale Interlink v%s\nService: Not Running", trayVersion))
		a.mStatus.SetTitle("Status: Service Not Running")
		a.mPause.Disable()
		a.mResume.Disable()
		a.mTriggerScan.Disable()
		return
	}

	// Service is running
	if a.lastStatus != nil {
		tooltip := fmt.Sprintf("Rescale Interlink v%s\nService: %s\nActive Users: %d\nActive Downloads: %d",
			trayVersion,
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
		systray.SetTooltip(fmt.Sprintf("Rescale Interlink v%s\nService: Running", trayVersion))
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

	// Trigger scan for current user (use "all" for all users)
	err := a.client.TriggerScan(ctx, "current")
	if err != nil {
		a.mu.Lock()
		a.lastError = fmt.Sprintf("Scan failed: %v", err)
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

	err := a.client.PauseUser(ctx, "current")
	if err != nil {
		a.mu.Lock()
		a.lastError = fmt.Sprintf("Pause failed: %v", err)
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

	err := a.client.ResumeUser(ctx, "current")
	if err != nil {
		a.mu.Lock()
		a.lastError = fmt.Sprintf("Resume failed: %v", err)
		a.mu.Unlock()
	}

	// Refresh status
	time.Sleep(500 * time.Millisecond)
	a.refreshStatus()
}

// viewLogs opens the logs directory in Explorer.
// v4.0.7: This runs locally in user context (not via IPC to service).
func (a *trayApp) viewLogs() {
	// Get user's config directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		a.mu.Lock()
		a.lastError = fmt.Sprintf("Failed to get home directory: %v", err)
		a.mu.Unlock()
		return
	}

	logsDir := filepath.Join(homeDir, ".config", "rescale", "logs")

	// Create if doesn't exist
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		a.mu.Lock()
		a.lastError = fmt.Sprintf("Failed to create logs directory: %v", err)
		a.mu.Unlock()
		// Continue anyway - directory might already exist
	}

	// v4.0.7 M5: Check for launch errors
	if err := exec.Command("explorer.exe", logsDir).Start(); err != nil {
		a.mu.Lock()
		a.lastError = fmt.Sprintf("Failed to open logs directory: %v", err)
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
