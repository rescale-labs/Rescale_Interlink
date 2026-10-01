package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/ipc"
)

// --- test fakes ---

type fakeClock struct{ t time.Time }

func (f *fakeClock) Now() time.Time { return f.t }

type fakeDetector struct{ result ServiceDetectionResult }

func (f fakeDetector) Detect(ctx context.Context) ServiceDetectionResult { return f.result }

type fakeIPC struct {
	status    *ipc.StatusData
	statusErr error
	users     []ipc.UserStatus
}

func (f fakeIPC) GetStatus(ctx context.Context) (*ipc.StatusData, error) {
	return f.status, f.statusErr
}
func (f fakeIPC) GetUserList(ctx context.Context) ([]ipc.UserStatus, error) {
	return f.users, nil
}

type fakeConfig struct {
	cfg *config.DaemonConfig
}

func (f fakeConfig) LoadUserDaemonConfig() (*config.DaemonConfig, error) { return f.cfg, nil }

type fakeIdentity struct{ sid string }

func (f fakeIdentity) CurrentSID() string { return f.sid }

func newEnabledConfig() *config.DaemonConfig {
	return &config.DaemonConfig{
		Daemon: config.DaemonCoreConfig{
			Enabled:        true,
			DownloadFolder: "/tmp/downloads",
		},
	}
}

func newDisabledConfig() *config.DaemonConfig {
	return &config.DaemonConfig{Daemon: config.DaemonCoreConfig{Enabled: false}}
}

// --- Presentation matrix tests ---

// TestPresentationMatrixCoverage walks every combination of
// InstallationState × PerUserState (that Compute can produce) and asserts
// Presentation returns non-empty strings and at least one allowed action
// per cell. Acts as a coverage smoke test — real wording is checked by the
// cell-specific tests below.
func TestPresentationMatrixCoverage(t *testing.T) {
	installs := []InstallationState{
		InstallationNotInstalled,
		InstallationStopped,
		InstallationStarting,
		InstallationStopping,
		InstallationRunning,
		InstallationSubprocessOnly,
	}
	peruser := []PerUserState{
		PerUserNotConfigured,
		PerUserPending,
		PerUserRunning,
		PerUserPaused,
		PerUserError,
	}

	for _, inst := range installs {
		for _, pu := range peruser {
			s := State{Installation: inst, PerUser: pu, LastError: "test error", LastErrorCode: ipc.CodeNoAPIKey}
			p := s.Presentation()
			if p.GUILongForm == "" {
				t.Errorf("empty GUILongForm for (%v,%v)", inst, pu)
			}
			if p.TrayStatusLine == "" {
				t.Errorf("empty TrayStatusLine for (%v,%v)", inst, pu)
			}
			if p.TrayTooltip == "" {
				t.Errorf("empty TrayTooltip for (%v,%v)", inst, pu)
			}
			if p.CLIStatusLine == "" {
				t.Errorf("empty CLIStatusLine for (%v,%v)", inst, pu)
			}
			if len(p.AllowedActions) == 0 {
				t.Errorf("no AllowedActions for (%v,%v)", inst, pu)
			}
		}
	}
}

// TestPresentationCells pins the wording and allowed actions for the cells a
// user actually sees; the matrix test above only checks each cell is populated.
func TestPresentationCells(t *testing.T) {
	tests := []struct {
		name         string
		state        State
		wantPhrases  []string
		wantActions  []Action
		exactActions []Action
	}{
		{
			// No service is the usual state now: the user's own daemon does the
			// work, so the cell is the per-user one, with nothing to install.
			name:         "not installed",
			state:        State{Installation: InstallationNotInstalled, PerUser: PerUserNotConfigured},
			wantPhrases:  []string{"Configure"},
			exactActions: []Action{ActionOpenLogs, ActionConfigure, ActionOpenGUI},
		},
		{
			// A stopped service from an earlier version changes nothing for
			// the user's own daemon.
			name:         "stopped",
			state:        State{Installation: InstallationStopped, PerUser: PerUserRunning},
			wantPhrases:  []string{"Auto-download active"},
			exactActions: []Action{ActionOpenLogs, ActionPause, ActionTriggerScan, ActionConfigure, ActionOpenGUI},
		},
		{
			name:        "running and active",
			state:       State{Installation: InstallationSubprocessOnly, PerUser: PerUserRunning, JobsDownloaded: 7},
			wantPhrases: []string{"Auto-download active"},
			wantActions: []Action{ActionPause, ActionTriggerScan},
		},
		{
			name:        "paused",
			state:       State{Installation: InstallationSubprocessOnly, PerUser: PerUserPaused},
			wantActions: []Action{ActionResume},
		},
		{
			// An error cell must carry both the canonical text and its hint, so
			// the user is told what to do about it.
			name: "error carries canonical text and hint",
			state: State{
				Installation:  InstallationSubprocessOnly,
				PerUser:       PerUserError,
				LastError:     ipc.CanonicalText[ipc.CodeNoAPIKey],
				LastErrorCode: ipc.CodeNoAPIKey,
			},
			wantPhrases: []string{ipc.CanonicalText[ipc.CodeNoAPIKey], ipc.HintFor(ipc.CodeNoAPIKey), "API Configuration"},
		},
		{
			name:        "a daemon that does not answer",
			state:       State{Installation: InstallationSubprocessOnly, PerUser: PerUserError, LastError: ipc.CanonicalText[ipc.CodeIPCNotResponding], LastErrorCode: ipc.CodeIPCNotResponding},
			wantPhrases: []string{"auto-download"},
		},
		{
			name:        "a daemon that takes too long",
			state:       State{Installation: InstallationSubprocessOnly, PerUser: PerUserError, LastError: ipc.CanonicalText[ipc.CodeTransientTimeout], LastErrorCode: ipc.CodeTransientTimeout},
			wantPhrases: []string{"Auto-download"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.state.Presentation()
			for _, phrase := range tt.wantPhrases {
				if !strings.Contains(p.GUILongForm, phrase) {
					t.Errorf("GUILongForm = %q, want it to contain %q", p.GUILongForm, phrase)
				}
			}
			for _, action := range tt.wantActions {
				if !slices.Contains(p.AllowedActions, action) {
					t.Errorf("expected action %v, got %v", action, p.AllowedActions)
				}
			}
			if tt.exactActions != nil && !slices.Equal(p.AllowedActions, tt.exactActions) {
				t.Errorf("actions = %v, want %v", p.AllowedActions, tt.exactActions)
			}
			if strings.Contains(strings.ToLower(p.GUILongForm+p.TrayTooltip), "service") {
				t.Errorf("per-user wording names a service: %q / %q", p.GUILongForm, p.TrayTooltip)
			}
		})
	}
}

// While a service from an earlier version runs, the user's own daemon cannot
// start and nothing here can reach the service, so every surface says how it
// ends, whatever the user's settings, instead of waiting for a daemon.
func TestPresentationWhileAnEarlierServiceRuns(t *testing.T) {
	for _, pu := range []PerUserState{PerUserNotConfigured, PerUserPending, PerUserRunning, PerUserPaused, PerUserError} {
		p := State{Installation: InstallationRunning, PerUser: pu}.Presentation()
		for name, text := range map[string]string{"GUI": p.GUILongForm, "tray": p.TrayTooltip, "CLI": p.CLIStatusLine} {
			if !strings.Contains(text, OldServiceRunning) {
				t.Errorf("%v: %s says %q, want %q", pu, name, text, OldServiceRunning)
			}
		}
		if want := []Action{ActionOpenLogs, ActionOpenGUI}; !slices.Equal(p.AllowedActions, want) {
			t.Errorf("%v: actions = %v, want %v", pu, p.AllowedActions, want)
		}
	}
	if strings.Contains(OldServiceRunning, "Remove Old Service") || !strings.Contains(OldServiceRunning, "'rescale-int service uninstall'") {
		t.Errorf("OldServiceRunning = %q, want it to name the command that removes the service", OldServiceRunning)
	}
}

// The user's own daemon can be started whenever none answers and no service
// from an earlier version is running, installed or not.
func TestCanStartDaemon(t *testing.T) {
	for _, tc := range []struct {
		state State
		want  bool
	}{
		{State{Installation: InstallationSubprocessOnly}, true},
		{State{Installation: InstallationNotInstalled}, true},
		{State{Installation: InstallationStopped}, true},
		{State{Installation: InstallationSubprocessOnly, IPCConnected: true}, false},
		{State{Installation: InstallationRunning}, false},
		{State{Installation: InstallationStarting}, false},
		{State{Installation: InstallationStopping}, false},
		{State{Installation: InstallationUnknown}, false},
	} {
		if got := tc.state.CanStartDaemon(); got != tc.want {
			t.Errorf("CanStartDaemon(%+v) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// --- Compute tests ---

// TestCompute walks the single-shot state derivations: what Compute reports for
// a given clock, daemon IPC reply, config and identity.
func TestCompute(t *testing.T) {
	errAt := time.Unix(1700000000, 0)

	tests := []struct {
		name     string
		now      time.Time
		ipc      fakeIPC
		cfg      *config.DaemonConfig
		identity fakeIdentity
		validate func(t *testing.T, s State)
	}{
		{
			name: "no config and no daemon",
			now:  time.Unix(0, 0),
			ipc:  fakeIPC{statusErr: errors.New("no daemon")},
			cfg:  newDisabledConfig(),
			validate: func(t *testing.T, s State) {
				if s.PerUser != PerUserNotConfigured {
					t.Errorf("PerUser = %v, want PerUserNotConfigured", s.PerUser)
				}
				if s.IPCConnected {
					t.Error("IPCConnected should be false")
				}
			},
		},
		{
			name: "configured but daemon not up yet stays pending",
			now:  time.Unix(1000, 0),
			ipc:  fakeIPC{statusErr: errors.New("no daemon yet")},
			cfg:  newEnabledConfig(),
			validate: func(t *testing.T, s State) {
				if s.PerUser != PerUserPending {
					t.Errorf("PerUser = %v, want PerUserPending", s.PerUser)
				}
				if s.PendingSince.IsZero() {
					t.Error("PendingSince should be set")
				}
			},
		},
		{
			name: "running user matched by SID",
			now:  time.Unix(0, 0),
			ipc: fakeIPC{
				status: &ipc.StatusData{},
				users:  []ipc.UserStatus{{Username: "alice", SID: "S-1-0-0-2", State: "running", JobsDownloaded: 5}},
			},
			cfg:      newEnabledConfig(),
			identity: fakeIdentity{sid: "S-1-0-0-2"},
			validate: func(t *testing.T, s State) {
				if s.PerUser != PerUserRunning {
					t.Errorf("PerUser = %v, want PerUserRunning", s.PerUser)
				}
				if s.JobsDownloaded != 5 {
					t.Errorf("JobsDownloaded = %d, want 5", s.JobsDownloaded)
				}
			},
		},
		{
			// A daemon of an earlier version can send the text without its code.
			name: "error text is reverse-looked-up to a code",
			now:  time.Unix(0, 0),
			ipc: fakeIPC{
				status: &ipc.StatusData{LastError: ipc.CanonicalText[ipc.CodeNoAPIKey]},
				users:  []ipc.UserStatus{{Username: "alice", State: "running"}},
			},
			cfg: newEnabledConfig(),
			validate: func(t *testing.T, s State) {
				if s.LastErrorCode != ipc.CodeNoAPIKey {
					t.Errorf("LastErrorCode = %q, want %q (reverse-looked-up from canonical text)",
						s.LastErrorCode, ipc.CodeNoAPIKey)
				}
			},
		},
		{
			// An explicit ErrorCode from the peer outranks a reverse-lookup, so
			// evolving the wording cannot change the code.
			name: "explicit error code beats reverse lookup",
			now:  time.Unix(0, 0),
			ipc: fakeIPC{
				status: &ipc.StatusData{
					LastError:     ipc.CanonicalText[ipc.CodeNoAPIKey],
					LastErrorCode: ipc.CodeDownloadFolderInaccessible,
				},
				users: []ipc.UserStatus{{Username: "alice", State: "running"}},
			},
			cfg: newEnabledConfig(),
			validate: func(t *testing.T, s State) {
				if s.LastErrorCode != ipc.CodeDownloadFolderInaccessible {
					t.Errorf("LastErrorCode = %q, want explicit CodeDownloadFolderInaccessible", s.LastErrorCode)
				}
			},
		},
		{
			// A daemon that is up but whose last scan failed must still surface
			// the failure. The per-user entry carries no error then, so it has to
			// come from the service-level status, timestamp included — otherwise
			// the only symptom is a last-scan time that quietly stops advancing.
			name: "scan failure from service status is surfaced",
			now:  time.Unix(1700000060, 0),
			ipc: fakeIPC{
				status: &ipc.StatusData{
					LastError:     ipc.CanonicalText[ipc.CodeScanFailed] + ": list jobs failed: 503",
					LastErrorCode: ipc.CodeScanFailed,
					LastErrorTime: &errAt,
				},
				users: []ipc.UserStatus{{Username: "alice", State: "running"}},
			},
			cfg: newEnabledConfig(),
			validate: func(t *testing.T, s State) {
				if s.PerUser != PerUserRunning {
					t.Errorf("PerUser = %v, want PerUserRunning (a failed scan is not a dead daemon)", s.PerUser)
				}
				if s.LastErrorCode != ipc.CodeScanFailed {
					t.Errorf("LastErrorCode = %q, want %q", s.LastErrorCode, ipc.CodeScanFailed)
				}
				if !strings.Contains(s.LastError, "list jobs failed: 503") {
					t.Errorf("LastError = %q, want it to carry the scan error detail", s.LastError)
				}
				if s.LastErrorTime == nil || !s.LastErrorTime.Equal(errAt) {
					t.Errorf("LastErrorTime = %v, want %v", s.LastErrorTime, errAt)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Computer{
				Clock:    &fakeClock{t: tt.now},
				Detector: fakeDetector{},
				IPC:      tt.ipc,
				Config:   fakeConfig{cfg: tt.cfg},
				Identity: tt.identity,
			}
			tt.validate(t, c.Compute(context.Background(), State{}))
		})
	}
}

func TestCompute_PendingTimeoutPromotesToError(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := &Computer{
		Clock:    clk,
		Detector: fakeDetector{},
		IPC:      fakeIPC{statusErr: errors.New("still pending")},
		Config:   fakeConfig{cfg: newEnabledConfig()},
		Identity: fakeIdentity{},
	}

	// First call: enters pending at t=1000.
	first := c.Compute(context.Background(), State{})
	if first.PerUser != PerUserPending {
		t.Fatalf("first call PerUser = %v, want PerUserPending", first.PerUser)
	}

	// Advance 9s — still pending.
	clk.t = time.Unix(1009, 0)
	second := c.Compute(context.Background(), first)
	if second.PerUser != PerUserPending {
		t.Errorf("after 9s PerUser = %v, want PerUserPending", second.PerUser)
	}
	if !second.PendingSince.Equal(first.PendingSince) {
		t.Errorf("PendingSince should be preserved across refreshes")
	}

	// Advance past 10s — promoted to error with CodeTransientTimeout.
	clk.t = time.Unix(1011, 0)
	third := c.Compute(context.Background(), second)
	if third.PerUser != PerUserError {
		t.Errorf("after 11s PerUser = %v, want PerUserError", third.PerUser)
	}
	if third.LastErrorCode != ipc.CodeTransientTimeout {
		t.Errorf("LastErrorCode = %v, want CodeTransientTimeout", third.LastErrorCode)
	}
}

// TestMatchUser pins which IPC entry is the caller's: a daemon serves one user
// and reports one entry, which is ours unless it names another SID.
func TestMatchUser(t *testing.T) {
	const mine, theirs = "S-1-5-21-1000000001-1000000002-1000000003-1001", "S-1-5-21-1000000001-1000000002-1000000003-1002"
	me := fakeIdentity{sid: mine}
	tests := []struct {
		name     string
		identity fakeIdentity
		users    []ipc.UserStatus
		want     bool
	}{
		{"our SID", me, []ipc.UserStatus{{Username: "jdoe", SID: mine}}, true},
		{"our SID in another case", me, []ipc.UserStatus{{SID: strings.ToLower(mine)}}, true},
		{"no SID, whatever the name, as on macOS and Linux", me, []ipc.UserStatus{{Username: "someone"}}, true},
		{"we have no SID", fakeIdentity{}, []ipc.UserStatus{{SID: theirs}}, true},
		{"another SID", me, []ipc.UserStatus{{Username: "jdoe", SID: theirs}}, false},
		{"no entries", me, nil, false},
		{"more than one entry", me, []ipc.UserStatus{{SID: mine}, {SID: mine}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (&Computer{Identity: tt.identity}).matchUser(tt.users)
			if (got != nil) != tt.want || got != nil && got != &tt.users[0] {
				t.Fatalf("matchUser = %+v, want the entry: %v", got, tt.want)
			}
		})
	}
}
