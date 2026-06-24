// Package service derives the shared auto-download state vocabulary used by
// the GUI, Tray, and CLI. Auto-download runs as a subprocess in the logged-in
// user's session (started by the tray/GUI); there is no Windows service.
package service

import (
	"context"
	"fmt"
	"os/user"
	"strings"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/version"
)

// PendingTimeout is how long the system can remain in a transient pending
// state before Compute promotes it to Error with CodeTransientTimeout.
const PendingTimeout = 10 * time.Second

// PerUserState describes auto-download's setup and liveness for the current
// user's session daemon.
type PerUserState int

const (
	PerUserUnknown PerUserState = iota

	// PerUserNotConfigured — no daemon.conf, or daemon.conf has Enabled=false.
	PerUserNotConfigured

	// PerUserPending — daemon.conf is enabled, but the daemon subprocess has
	// not yet come up. Promotes to PerUserError if pending persists beyond
	// PendingTimeout.
	PerUserPending

	// PerUserRunning — the user's daemon is running and polling.
	PerUserRunning

	// PerUserPaused — the user's daemon is running but polling is paused.
	PerUserPaused

	// PerUserError — the user's daemon is configured but failing.
	PerUserError
)

// Action identifies a user-facing action a surface can expose given the
// current State. Surfaces enable/disable buttons and menu items from
// State.Presentation().AllowedActions.
type Action string

const (
	ActionStartDaemon Action = "start_daemon"
	ActionConfigure   Action = "configure"
	ActionOpenGUI     Action = "open_gui"
	ActionPause       Action = "pause"
	ActionResume      Action = "resume"
	ActionTriggerScan Action = "trigger_scan"
	ActionRetry       Action = "retry"
	ActionOpenLogs    Action = "open_logs"
)

// State is the composed view of the auto-download system's current condition.
// GUI, Tray, and CLI all derive their rendering from State via Presentation.
type State struct {
	PerUser PerUserState

	// PendingSince is the time at which PerUser first transitioned to
	// PerUserPending. Used by Compute to enforce PendingTimeout.
	PendingSince time.Time

	// Liveness / observability fields surfaced by IPC. All optional.
	LastError       string
	LastErrorCode   ipc.ErrorCode
	ActiveDownloads int
	JobsDownloaded  int
	LastScanTime    *time.Time
	DownloadFolder  string
	Version         string
	Uptime          string
	IPCConnected    bool
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
// identity. On Windows, SID is used to match the IPC user list.
type UserIdentity interface {
	CurrentSID() string
	CurrentUsername() string
}

type realUserIdentity struct{}

func (realUserIdentity) CurrentSID() string {
	if u, err := user.Current(); err == nil {
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
// State carries PendingSince across refreshes so the timeout can fire.
func (c *Computer) Compute(ctx context.Context, prior State) State {
	now := c.Clock.Now()

	s := State{Version: version.Version}

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
		// User is configured; default to pending until IPC confirms the
		// daemon is up.
		s.PerUser = PerUserPending
	}

	// Query IPC for liveness details and the caller's user entry.
	if status, err := c.IPC.GetStatus(ctx); err == nil && status != nil {
		s.IPCConnected = true
		s.Uptime = status.Uptime
		s.ActiveDownloads = status.ActiveDownloads
		s.LastScanTime = status.LastScanTime

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
// identity. Windows matches by SID primarily, with a username fallback.
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

// matchesWindowsUsername compares two Windows username renderings
// case-insensitively, ignoring any DOMAIN\ prefix.
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

	switch s.PerUser {
	case PerUserNotConfigured:
		p.GUILongForm = "Auto-download is not set up. Click Configure to enable it for your account."
		p.TrayStatusLine = "Setup required"
		p.TrayTooltip = "Rescale Interlink: Configure to enable auto-download"
		p.AllowedActions = append(p.AllowedActions, ActionConfigure, ActionOpenGUI)
		p.CLIStatusLine = "Status: not configured"
		return p
	case PerUserPending:
		p.GUILongForm = "Starting... auto-download is picking up your settings. This usually takes a few seconds."
		p.TrayStatusLine = "Starting..."
		p.TrayTooltip = "Rescale Interlink: Starting auto-download"
		p.AllowedActions = append(p.AllowedActions, ActionStartDaemon, ActionOpenGUI, ActionRetry)
		p.CLIStatusLine = "Status: starting"
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
		p.AllowedActions = append(p.AllowedActions, ActionStartDaemon, ActionRetry, ActionConfigure, ActionOpenGUI)
		p.CLIStatusLine = "Status: error — " + text
		return p
	}

	// Unknown fall-through.
	p.GUILongForm = "Auto-download state unknown."
	p.TrayStatusLine = "Unknown"
	p.TrayTooltip = "Rescale Interlink: state unknown"
	p.AllowedActions = append(p.AllowedActions, ActionOpenGUI)
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
