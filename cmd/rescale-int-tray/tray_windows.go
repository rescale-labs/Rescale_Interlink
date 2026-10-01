//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/service"
	"github.com/rescale/rescale-int/internal/version"
)

const (
	// Status refresh interval
	refreshInterval = 5 * time.Second
)

// trayApp manages the system tray application state.
type trayApp struct {
	client *ipc.Client
	comp   *service.Computer
	mu     sync.RWMutex

	// Current derived state from service.Computer. prior is the last
	// computed State, carried across refreshes so the 10s transient-pending
	// timeout fires consistently with the GUI.
	prior       service.State
	lastState   service.State
	lastPresent service.Presentation

	// failure is why the last action failed, shown for failureShown after
	// failedAt; empty once an action succeeds.
	failure  string
	failedAt time.Time

	// Menu items (for dynamic updates)
	mStatus        *systray.MenuItem
	mSetupRequired *systray.MenuItem
	mStartService  *systray.MenuItem
	mPause         *systray.MenuItem
	mResume        *systray.MenuItem
	mTriggerScan   *systray.MenuItem
	mConfigure     *systray.MenuItem
	mOpenGUI       *systray.MenuItem
	mViewLogs      *systray.MenuItem
	mQuit          *systray.MenuItem

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
	app.comp = service.DefaultComputer(app.client)

	// Set initial tray icon and tooltip
	systray.SetIcon(iconData)
	systray.SetTitle("Rescale Interlink")
	systray.SetTooltip("Rescale Interlink - Connecting...")

	// Build menu
	app.mStatus = systray.AddMenuItem("Status: Checking...", "Service status")
	app.mStatus.Disable()

	// Setup guidance (shown when user hasn't configured auto-download)
	app.mSetupRequired = systray.AddMenuItem("Setup Required - Click to Configure", "Open GUI to enable auto-download")
	app.mSetupRequired.Hide() // Hidden by default, shown when needed

	systray.AddSeparator()

	// Auto-download controls. The daemon runs as a subprocess in this user's
	// session, so it inherits the user's drive mappings and credentials.
	app.mStartService = systray.AddMenuItem("Start Auto-Download", "Start auto-download in your session")
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

	// Auto-start the user daemon if auto-download is enabled.
	go app.startupTasks()
}

// startupTasks runs once at tray launch, at login or when the app starts the
// tray: it auto-starts the auto-download daemon when the user has enabled it in
// daemon.conf. The daemon runs as a subprocess in this user's session so it can
// reach the user's mapped/network drives. Start's checks apply, and a refusal
// shows as Start's do; a daemon already running is left alone.
func (a *trayApp) startupTasks() {
	daemonCfg, err := config.LoadDaemonConfig("")
	if err != nil {
		// startService reports it; the log keeps the cause.
		daemon.WriteStartupLog("Tray startup: could not load daemon.conf (%v)", err)
	} else if !daemonCfg.Daemon.Enabled {
		daemon.WriteStartupLog("Tray startup: auto-download disabled in daemon.conf — not starting daemon")
		return
	}

	// At logon a mapped drive can reconnect after the tray starts, so the
	// download folder gets about a minute before Start reports it.
	if daemonCfg != nil && filepath.IsAbs(daemonCfg.Daemon.DownloadFolder) {
		for try := 1; try < folderTries && os.MkdirAll(daemonCfg.Daemon.DownloadFolder, 0755) != nil; try++ {
			daemon.WriteStartupLog("Tray startup: download folder not available yet; trying again in %s", folderWait)
			sleep(folderWait)
		}
	}

	// Don't start a second daemon if one is already running for this user,
	// including one started while startup waited. A pipe another user holds
	// refuses every start, so that one is shown.
	if blocked, reason := shouldBlockSubprocess(); blocked {
		daemon.WriteStartupLog("Tray startup: not starting daemon: %s", reason)
		if reason == service.PipeTaken {
			a.fail(reason)
		}
		return
	}

	daemon.WriteStartupLog("Auto-download enabled — starting daemon on tray launch")
	a.startService()
}

// folderTries is how often startup tries the download folder, folderWait
// apart.
const (
	folderTries = 5
	folderWait  = 15 * time.Second
)

func onExit() {
	if app != nil {
		close(app.done)
	}
}

// refreshLoop periodically refreshes the daemon status.
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

// refreshStatus composes the current service.State via the shared Computer
// and updates the tray's UI from the resulting Presentation. All state
// vocabulary comes from service.Presentation; this function never invents
// its own strings.
func (a *trayApp) refreshStatus() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	a.mu.RLock()
	prior := a.prior
	a.mu.RUnlock()

	st := a.comp.Compute(ctx, prior)
	pres := st.Presentation()

	a.mu.Lock()
	a.prior = st
	a.lastState = st
	a.lastPresent = pres
	a.mu.Unlock()

	a.updateUI()
}

// updateUI renders the tray tooltip, status menu item, and menu-item
// enabled/visible state from service.Presentation. The canonical state
// vocabulary lives entirely in service/state.go; this function is a view.
func (a *trayApp) updateUI() {
	a.mu.RLock()
	st := a.lastState
	pres := a.lastPresent
	tooltip, status := trayText(pres, a.failure, a.failedAt, time.Now())
	a.mu.RUnlock()

	systray.SetTooltip(tooltip)
	a.mStatus.SetTitle(status)

	// Menu item visibility is driven by allowed actions.
	allowed := map[service.Action]bool{}
	for _, a := range pres.AllowedActions {
		allowed[a] = true
	}
	setMenuItem(a.mPause, allowed[service.ActionPause])
	setMenuItem(a.mResume, allowed[service.ActionResume])
	setMenuItem(a.mTriggerScan, allowed[service.ActionTriggerScan])

	// Setup-required shortcut: visible when not yet configured.
	if st.PerUser == service.PerUserNotConfigured {
		a.mSetupRequired.Show()
	} else {
		a.mSetupRequired.Hide()
	}

	setMenuItem(a.mStartService, st.CanStartDaemon())
}

// failureShown is how long the tray shows why an action failed in place of
// the state, unless another action succeeds first.
const failureShown = 30 * time.Second

// trayText is the tooltip and the status line: why the last action failed
// while that is recent, else the state. A tooltip holds 127 characters, so the
// status line carries the whole reason.
func trayText(pres service.Presentation, failure string, failedAt, now time.Time) (tooltip, status string) {
	tooltip, status = pres.TrayTooltip, pres.TrayStatusLine
	if failure != "" && now.Sub(failedAt) < failureShown {
		tooltip, status = failure, "Failed: "+failure
	}
	tooltip = fmt.Sprintf("Rescale Interlink v%s\n%s", version.Version, tooltip)
	if r := []rune(tooltip); len(r) > 127 {
		tooltip = string(r[:124]) + "..."
	}
	return tooltip, status
}

// fail records why an action failed, or with "" that one succeeded, and
// redraws. The redraw takes the lock itself, so it runs once the lock is
// released.
func (a *trayApp) fail(why string) {
	a.mu.Lock()
	a.failure, a.failedAt = why, time.Now()
	a.mu.Unlock()
	redraw(a)
}

// redraw, shouldBlockSubprocess and sleep are variables so a test can run an
// action without a tray, a daemon or the waits.
var (
	redraw                = (*trayApp).updateUI
	shouldBlockSubprocess = service.ShouldBlockSubprocess
	sleep                 = time.Sleep
)

// setMenuItem shows+enables or hides a systray menu item.
func setMenuItem(mi *systray.MenuItem, enabled bool) {
	if mi == nil {
		return
	}
	if enabled {
		mi.Show()
		mi.Enable()
	} else {
		mi.Hide()
	}
}

// handleMenuClicks processes menu item clicks.
func (a *trayApp) handleMenuClicks() {
	for {
		select {
		case <-a.mSetupRequired.ClickedCh:
			a.openGUI()

		case <-a.mStartService.ClickedCh:
			a.startService()

		case <-a.mConfigure.ClickedCh:
			a.openGUI()

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

// startService starts the auto-download daemon, unless shouldBlockSubprocess
// says a daemon of this user, a pipe of its name or a service from an earlier
// version is running.
func (a *trayApp) startService() {
	if blocked, reason := shouldBlockSubprocess(); blocked {
		a.fail(reason)
		return
	}
	daemonCfg, err := config.LoadDaemonConfig("")
	if err != nil {
		a.fail("Configuration error. Open Interlink to configure.")
		return
	}
	if err := daemon.Start(daemonCfg); err != nil {
		// A refusal of the settings says what to change; a process that would
		// not start is worded as the IPC failures are.
		if errors.Is(err, daemon.ErrLaunch) {
			a.fail(translateError(err))
		} else {
			a.fail(err.Error())
		}
		return
	}
	a.fail("")

	// Wait for IPC to come up, then refresh status
	go func() {
		time.Sleep(2 * time.Second)
		a.refreshStatus()
	}()
}

// openGUI launches the main Rescale Interlink GUI.
func (a *trayApp) openGUI() {
	// Find rescale-int-gui.exe in the same directory as the tray app
	exePath, err := os.Executable()
	if err != nil {
		a.fail(fmt.Sprintf("Failed to find executable path: %v", err))
		return
	}

	dir := filepath.Dir(exePath)

	// GUI is a separate binary (Wails-based with embedded frontend)
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
	if err := cmd.Start(); err != nil {
		a.fail(fmt.Sprintf("Failed to launch GUI: %v", err))
	}
}

// triggerScan triggers an immediate job scan via IPC.
func (a *trayApp) triggerScan() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.client.TriggerScan(ctx, "")
	if err != nil {
		a.fail(translateError(err))
	}

	// Refresh status after triggering scan
	time.Sleep(500 * time.Millisecond)
	a.refreshStatus()
}

// pauseAutoDownload pauses auto-download for the current user.
func (a *trayApp) pauseAutoDownload() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.client.PauseUser(ctx, "")
	if err != nil {
		a.fail(translateError(err))
	}

	// Refresh status
	time.Sleep(500 * time.Millisecond)
	a.refreshStatus()
}

// resumeAutoDownload resumes auto-download for the current user.
func (a *trayApp) resumeAutoDownload() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.client.ResumeUser(ctx, "")
	if err != nil {
		a.fail(translateError(err))
	}

	// Refresh status
	time.Sleep(500 * time.Millisecond)
	a.refreshStatus()
}

// viewLogs opens the logs directory in Explorer.
func (a *trayApp) viewLogs() {
	logsDir := config.LogDirectory()

	// Create if doesn't exist
	if err := os.MkdirAll(logsDir, 0700); err != nil {
		a.fail("Failed to create logs directory")
		// Continue anyway - directory might already exist
	}

	if err := exec.Command("explorer.exe", logsDir).Start(); err != nil {
		a.fail("Failed to open logs directory")
	}
}

// translateError maps a raw error from an action (subprocess launch, IPC
// call) to canonical user-facing text. Uses ipc.ErrorCode so
// the tray and the GUI agree on wording, and appends the actionable hint
// when one is defined.
func translateError(err error) string {
	if err == nil {
		return ""
	}
	errStr := err.Error()

	var code ipc.ErrorCode
	switch {
	case strings.Contains(errStr, "pipe\\rescale-interlink") ||
		strings.Contains(errStr, "The system cannot find the file specified"):
		code = ipc.CodeIPCNotResponding
	case strings.Contains(errStr, "CLI not found") || strings.Contains(errStr, "executable path"):
		code = ipc.CodeCLINotFound
	case strings.Contains(errStr, "failed to load daemon.conf"):
		code = ipc.CodeConfigInvalid
	case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded"):
		code = ipc.CodeIPCNotResponding
	case strings.Contains(errStr, "access denied") || strings.Contains(errStr, "Access is denied") ||
		strings.Contains(errStr, "permission"):
		code = ipc.CodePermissionDenied
	case strings.Contains(errStr, "already running"):
		code = ipc.CodeServiceAlreadyRunning
	}

	if code != "" {
		text := ipc.CanonicalText[code]
		if hint := ipc.HintFor(code); hint != "" {
			return text + ". " + hint
		}
		return text
	}

	// No canonical mapping — show a truncated raw error.
	if len(errStr) > 60 {
		return errStr[:57] + "..."
	}
	return errStr
}
