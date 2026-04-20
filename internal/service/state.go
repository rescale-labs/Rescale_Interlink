package service

import (
	"context"
	"fmt"
	"os/user"
	"runtime"
	"strings"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/version"
)

// PendingTimeout is how long the system can remain in a transient pending
// state before Compute promotes it to Error with CodeTransientTimeout.
// Per AUTO_DOWNLOAD_SPEC §5.5.
const PendingTimeout = 10 * time.Second

// InstallationState describes whether and how a service-level installation
// exists. On macOS, Linux, and Windows portable builds there is no SCM
// concept, so Installation collapses to InstallationSubprocessOnly.
type InstallationState int

const (
	InstallationUnknown InstallationState = iota

	// InstallationSubprocessOnly — no system service is possible on this
	// platform (macOS, Linux, Windows without MSI). A user-session
	// subprocess daemon is the only option.
	InstallationSubprocessOnly

	// InstallationNotInstalled — Windows MSI is present but the service has
	// not been registered with SCM.
	InstallationNotInstalled

	// InstallationStopped — Windows SCM has the service registered but it is
	// currently stopped.
	InstallationStopped

	// InstallationStarting — SCM reports StartPending.
	InstallationStarting

	// InstallationRunning — SCM reports the service is running.
	InstallationRunning

	// InstallationStopping — SCM reports StopPending.
	InstallationStopping
)

// PerUserState describes auto-download's setup and liveness for the current
// user. Orthogonal to InstallationState.
type PerUserState int

const (
	PerUserUnknown PerUserState = iota

	// PerUserNotConfigured — no daemon.conf, or daemon.conf has Enabled=false.
	PerUserNotConfigured

	// PerUserPending — daemon.conf is enabled, but the service has not yet
	// registered this user. Promotes to PerUserError if the pending state
	// persists beyond PendingTimeout.
	PerUserPending

	// PerUserRunning — this user's daemon is registered and polling.
	PerUserRunning

	// PerUserPaused — this user's daemon is registered but polling is paused.
	PerUserPaused

	// PerUserError — this user's daemon is configured but failing.
	PerUserError
)

// Action identifies a user-facing action a surface can expose to the user
// given the current State. Surfaces enable/disable buttons and menu items
// from State.Presentation().AllowedActions.
type Action string

const (
	ActionInstallService   Action = "install_service"
	ActionUninstallService Action = "uninstall_service"
	ActionStartService     Action = "start_service"
	ActionStopService      Action = "stop_service"
	ActionConfigure        Action = "configure"
	ActionOpenGUI          Action = "open_gui"
	ActionPause            Action = "pause"
	ActionResume           Action = "resume"
	ActionTriggerScan      Action = "trigger_scan"
	ActionRetry            Action = "retry"
	ActionOpenLogs         Action = "open_logs"
)

// State is the composed view of the auto-download system's current condition.
// GUI, Tray, and CLI all derive their rendering from State via Presentation.
type State struct {
	Installation InstallationState
	PerUser      PerUserState

	// PendingSince is the time at which PerUser first transitioned to
	// PerUserPending. Used by Compute to enforce PendingTimeout. Zero value
	// means "not in a pending state" (or "pending state started now").
	PendingSince time.Time

	// Liveness / observability fields surfaced by IPC. All optional; any may
	// be zero if IPC is unavailable or the field is not applicable.
	LastError       string
	LastErrorCode   ipc.ErrorCode
	ActiveDownloads int
	JobsDownloaded  int
	LastScanTime    *time.Time
	DownloadFolder  string
	Version         string
	Uptime          string
	IPCConnected    bool
	ServiceMode     bool
}

// Presentation is the canonical per-surface rendering of a State.
type Presentation struct {
	GUILongForm    string
	TrayTooltip    string
	TrayStatusLine string
	CLIStatusLine  string
	AllowedActions []Action
}

// Clock abstracts time.Now for tests.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Detector abstracts DetectDaemon so tests can inject a fixed result.
type Detector interface {
	Detect(ctx context.Context) ServiceDetectionResult
}

type realDetector struct{}

func (realDetector) Detect(ctx context.Context) ServiceDetectionResult {
	return DetectDaemon()
}

// IPCClient abstracts the methods Compute calls on a real *ipc.Client.
type IPCClient interface {
	GetStatus(ctx context.Context) (*ipc.StatusData, error)
	GetUserList(ctx context.Context) ([]ipc.UserStatus, error)
}

// ConfigLoader abstracts loading the current user's daemon.conf.
type ConfigLoader interface {
	LoadUserDaemonConfig() (*config.DaemonConfig, error)
}

type realConfigLoader struct{}

func (realConfigLoader) LoadUserDaemonConfig() (*config.DaemonConfig, error) {
	return config.LoadDaemonConfig("")
}

// UserIdentity abstracts the platform-specific lookup of the current user's
// identity. On Windows, SID is used to match the IPC user list. On
// macOS/Linux only username is needed.
type UserIdentity interface {
	CurrentSID() string
	CurrentUsername() string
}

type realUserIdentity struct{}

func (realUserIdentity) CurrentSID() string {
	if u, err := user.Current(); err == nil {
		// On Windows, Uid is the SID. On Unix, it's a numeric UID; the IPC
		// peer uses SO_PEERCRED so SID matching is unnecessary, but we
		// return what we have for consistency.
		return u.Uid
	}
	return ""
}

func (realUserIdentity) CurrentUsername() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// Computer composes State from injected dependencies. Tests substitute fakes
// for the interfaces; production uses DefaultComputer.
type Computer struct {
	Clock    Clock
	Detector Detector
	IPC      IPCClient
	Config   ConfigLoader
	Identity UserIdentity
}

// DefaultComputer returns a Computer wired with real platform dependencies.
// The caller supplies an ipc.Client (typical: one client per surface).
func DefaultComputer(client IPCClient) *Computer {
	return &Computer{
		Clock:    realClock{},
		Detector: realDetector{},
		IPC:      client,
		Config:   realConfigLoader{},
		Identity: realUserIdentity{},
	}
}

// Compute builds the current State given an optional prior State. The prior
// State carries PendingSince across refreshes so the 10s timeout can fire.
// Fresh/one-shot callers (e.g., CLI daemon status) pass prior = State{} and
// accept that pending renders as pending.
func (c *Computer) Compute(ctx context.Context, prior State) State {
	now := c.Clock.Now()

	s := State{
		Installation: classifyInstallation(c.Detector.Detect(ctx)),
		Version:      version.Version,
	}

	// Per-user configuration state starts from daemon.conf.
	configured := false
	if cfg, err := c.Config.LoadUserDaemonConfig(); err == nil && cfg != nil {
		configured = cfg.Daemon.Enabled
		if cfg.Daemon.DownloadFolder != "" {
			s.DownloadFolder = cfg.Daemon.DownloadFolder
		}
	}

	if !configured {
		s.PerUser = PerUserNotConfigured
	} else {
		// User is configured; default to pending until IPC confirms registration.
		s.PerUser = PerUserPending
	}

	// Query IPC for liveness details and the caller's user entry.
	if status, err := c.IPC.GetStatus(ctx); err == nil && status != nil {
		s.IPCConnected = true
		s.ServiceMode = status.ServiceMode
		s.Uptime = status.Uptime
		s.ActiveDownloads = status.ActiveDownloads
		s.LastScanTime = status.LastScanTime

		// Refine installation state: if IPC responds, a daemon is alive.
		// For non-Windows, stay SubprocessOnly. For Windows, upgrade to
		// InstallationRunning when ServiceMode is true.
		if status.ServiceMode && runtime.GOOS == "windows" {
			s.Installation = InstallationRunning
		}

		if users, err2 := c.IPC.GetUserList(ctx); err2 == nil {
			matched := c.matchUser(users)
			if matched != nil {
				switch matched.State {
				case "running":
					s.PerUser = PerUserRunning
				case "paused":
					s.PerUser = PerUserPaused
				case "error":
					s.PerUser = PerUserError
				case "stopped":
					s.PerUser = PerUserNotConfigured
				}
				s.JobsDownloaded = matched.JobsDownloaded
				if matched.DownloadFolder != "" {
					s.DownloadFolder = matched.DownloadFolder
				}
				if matched.LastError != "" {
					s.LastError = matched.LastError
				}
				if matched.ErrorCode != "" {
					s.LastErrorCode = matched.ErrorCode
				} else if matched.LastError != "" {
					// Backwards compatibility: older servers don't set
					// ErrorCode. Reverse-lookup the canonical text.
					s.LastErrorCode = ipc.CodeFromCanonicalText(matched.LastError)
				}
			}
		}

		if status.LastError != "" && s.LastError == "" {
			s.LastError = status.LastError
			if status.LastErrorCode != "" {
				s.LastErrorCode = status.LastErrorCode
			} else {
				s.LastErrorCode = ipc.CodeFromCanonicalText(status.LastError)
			}
		}
	}

	// Pending-state timeout promotion. Only applies when we are still in
	// PerUserPending after the IPC round-trip.
	if s.PerUser == PerUserPending {
		if prior.PerUser == PerUserPending && !prior.PendingSince.IsZero() {
			s.PendingSince = prior.PendingSince
			if now.Sub(prior.PendingSince) > PendingTimeout {
				s.PerUser = PerUserError
				if s.LastErrorCode == "" {
					s.LastErrorCode = ipc.CodeTransientTimeout
					s.LastError = ipc.CanonicalText[ipc.CodeTransientTimeout]
				}
			}
		} else {
			s.PendingSince = now
		}
	}

	return s
}

// matchUser finds the IPC user entry corresponding to the current process
// identity. Windows matches by SID primarily, with a username fallback;
// Unix matches by username.
func (c *Computer) matchUser(users []ipc.UserStatus) *ipc.UserStatus {
	if len(users) == 0 {
		return nil
	}
	sid := c.Identity.CurrentSID()
	username := c.Identity.CurrentUsername()
	for i := range users {
		u := &users[i]
		if sid != "" && u.SID != "" && strings.EqualFold(u.SID, sid) {
			return u
		}
		if matchesWindowsUsername(u.Username, username) {
			return u
		}
	}
	// Subprocess hardening: single-user daemons return exactly one entry
	// with no SID match by convention. Treat it as "the current user."
	if len(users) == 1 {
		return &users[0]
	}
	return nil
}

// classifyInstallation maps a ServiceDetectionResult to an InstallationState.
func classifyInstallation(d ServiceDetectionResult) InstallationState {
	if runtime.GOOS != "windows" {
		return InstallationSubprocessOnly
	}
	if d.ServiceMode {
		return InstallationRunning
	}
	if d.SubprocessPID > 0 || d.PipeInUse {
		// A subprocess daemon is running (or a stale pipe). For presentation
		// purposes, treat Windows as SubprocessOnly when no SCM service is up.
		return InstallationSubprocessOnly
	}
	if IsInstalled() {
		if st, err := QueryStatus(); err == nil {
			switch st {
			case StatusRunning:
				return InstallationRunning
			case StatusStartPending:
				return InstallationStarting
			case StatusStopPending:
				return InstallationStopping
			case StatusPaused:
				return InstallationStopped
			case StatusStopped:
				return InstallationStopped
			}
		}
		return InstallationStopped
	}
	return InstallationNotInstalled
}

// matchesWindowsUsername compares two Windows username renderings
// case-insensitively, ignoring any DOMAIN\ prefix. Moved from
// internal/wailsapp/daemon_bindings_windows.go so it can be reused from
// state.Compute regardless of platform.
func matchesWindowsUsername(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	strip := func(s string) string {
		if i := strings.LastIndex(s, `\`); i >= 0 {
			s = s[i+1:]
		}
		return strings.ToLower(strings.TrimSpace(s))
	}
	return strip(a) == strip(b)
}

// Presentation returns the canonical rendering of s across all surfaces.
// Pure function of s; safe to call from tests.
func (s State) Presentation() Presentation {
	p := Presentation{AllowedActions: []Action{ActionOpenLogs}}

	switch s.Installation {
	case InstallationNotInstalled:
		p.GUILongForm = "Auto-download is not installed. Click Install Service to set it up."
		p.TrayStatusLine = "Service not installed"
		p.TrayTooltip = "Rescale Interlink: Service not installed"
		p.AllowedActions = append(p.AllowedActions, ActionInstallService, ActionConfigure, ActionOpenGUI)
		p.CLIStatusLine = "Status: service not installed"
		return p
	case InstallationStopped:
		p.GUILongForm = "Windows Service installed but stopped. Click Start Service."
		p.TrayStatusLine = "Service stopped"
		p.TrayTooltip = "Rescale Interlink: Service installed but not running"
		p.AllowedActions = append(p.AllowedActions, ActionStartService, ActionUninstallService, ActionOpenGUI)
		p.CLIStatusLine = "Status: service stopped"
		return p
	case InstallationStarting:
		p.GUILongForm = "Service starting..."
		p.TrayStatusLine = "Service starting"
		p.TrayTooltip = "Rescale Interlink: Service starting"
		p.AllowedActions = append(p.AllowedActions, ActionOpenGUI)
		p.CLIStatusLine = "Status: service starting"
		return p
	case InstallationStopping:
		p.GUILongForm = "Service stopping..."
		p.TrayStatusLine = "Service stopping"
		p.TrayTooltip = "Rescale Interlink: Service stopping"
		p.CLIStatusLine = "Status: service stopping"
		return p
	}

	// Either InstallationRunning (Windows service mode) or
	// InstallationSubprocessOnly (macOS/Linux, or Windows portable/subprocess).
	switch s.PerUser {
	case PerUserNotConfigured:
		p.GUILongForm = "Service running. You are not set up for auto-download. Click Configure to enable for your account."
		p.TrayStatusLine = "Setup required"
		p.TrayTooltip = "Rescale Interlink: Configure to enable auto-download for this user"
		p.AllowedActions = append(p.AllowedActions, ActionConfigure, ActionOpenGUI)
		p.CLIStatusLine = "Status: not configured"
		return p
	case PerUserPending:
		p.GUILongForm = "Activating... the service is picking up your settings. This usually takes a few seconds."
		p.TrayStatusLine = "Activating..."
		p.TrayTooltip = "Rescale Interlink: Activating — waiting for the service to register this user"
		p.AllowedActions = append(p.AllowedActions, ActionOpenGUI, ActionRetry)
		p.CLIStatusLine = "Status: activating"
		return p
	case PerUserRunning:
		scan := "never"
		if s.LastScanTime != nil && !s.LastScanTime.IsZero() {
			scan = fmt.Sprintf("%s ago", roundDuration(time.Since(*s.LastScanTime)))
		}
		p.GUILongForm = fmt.Sprintf("Auto-download active. Last scan: %s. Jobs downloaded: %d.", scan, s.JobsDownloaded)
		p.TrayStatusLine = fmt.Sprintf("Active | %d downloaded | last scan %s", s.JobsDownloaded, scan)
		p.TrayTooltip = fmt.Sprintf("Rescale Interlink: Active\nJobs downloaded: %d\nLast scan: %s", s.JobsDownloaded, scan)
		p.AllowedActions = append(p.AllowedActions, ActionPause, ActionTriggerScan, ActionConfigure, ActionOpenGUI)
		p.CLIStatusLine = fmt.Sprintf("Status: active (last scan %s, %d downloaded)", scan, s.JobsDownloaded)
		return p
	case PerUserPaused:
		p.GUILongForm = "Paused. Click Resume to continue auto-download."
		p.TrayStatusLine = "Paused"
		p.TrayTooltip = "Rescale Interlink: Paused"
		p.AllowedActions = append(p.AllowedActions, ActionResume, ActionConfigure, ActionOpenGUI)
		p.CLIStatusLine = "Status: paused"
		return p
	case PerUserError:
		text := s.LastError
		if text == "" {
			text = "unknown error"
		}
		hint := ""
		if s.LastErrorCode != "" {
			hint = ipc.HintFor(s.LastErrorCode)
		}
		long := "Error: " + text
		if hint != "" {
			long += ". " + hint
		}
		p.GUILongForm = long
		p.TrayStatusLine = "Error: " + truncate(text, 40)
		p.TrayTooltip = "Rescale Interlink: " + text
		p.AllowedActions = append(p.AllowedActions, ActionRetry, ActionConfigure, ActionOpenGUI)
		p.CLIStatusLine = "Status: error — " + text
		return p
	}

	// Unknown fall-through.
	p.GUILongForm = "Auto-download state unknown."
	p.TrayStatusLine = "Unknown"
	p.TrayTooltip = "Rescale Interlink: state unknown"
	p.CLIStatusLine = "Status: unknown"
	return p
}

// truncate shortens s to maxLen characters, appending "..." when truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// roundDuration rounds d to seconds (or minutes for >1m durations) for
// user-readable output.
func roundDuration(d time.Duration) time.Duration {
	if d >= time.Minute {
		return d.Round(time.Minute)
	}
	return d.Round(time.Second)
}
