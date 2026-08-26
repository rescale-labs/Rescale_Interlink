package service

import (
	"context"
	"errors"
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
	usersErr  error
}

func (f fakeIPC) GetStatus(ctx context.Context) (*ipc.StatusData, error) {
	return f.status, f.statusErr
}
func (f fakeIPC) GetUserList(ctx context.Context) ([]ipc.UserStatus, error) {
	return f.users, f.usersErr
}

type fakeConfig struct {
	cfg *config.DaemonConfig
	err error
}

func (f fakeConfig) LoadUserDaemonConfig() (*config.DaemonConfig, error) { return f.cfg, f.err }

type fakeIdentity struct {
	sid, username string
}

func (f fakeIdentity) CurrentSID() string      { return f.sid }
func (f fakeIdentity) CurrentUsername() string { return f.username }

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
		name        string
		state       State
		wantPhrases []string
		wantActions []Action
	}{
		{
			name:        "not installed",
			state:       State{Installation: InstallationNotInstalled},
			wantPhrases: []string{"Install Service"},
			wantActions: []Action{ActionInstallService},
		},
		{
			name:        "stopped",
			state:       State{Installation: InstallationStopped},
			wantPhrases: []string{"Start Service"},
			wantActions: []Action{ActionStartService},
		},
		{
			name:        "running but not configured",
			state:       State{Installation: InstallationRunning, PerUser: PerUserNotConfigured},
			wantPhrases: []string{"Configure"},
			wantActions: []Action{ActionConfigure},
		},
		{
			name:        "running and active",
			state:       State{Installation: InstallationRunning, PerUser: PerUserRunning, JobsDownloaded: 7},
			wantPhrases: []string{"Auto-download active"},
			wantActions: []Action{ActionPause, ActionTriggerScan},
		},
		{
			name:        "paused",
			state:       State{Installation: InstallationRunning, PerUser: PerUserPaused},
			wantActions: []Action{ActionResume},
		},
		{
			// An error cell must carry both the canonical text and its hint, so
			// the user is told what to do about it.
			name: "error carries canonical text and hint",
			state: State{
				Installation:  InstallationRunning,
				PerUser:       PerUserError,
				LastError:     ipc.CanonicalText[ipc.CodeNoAPIKey],
				LastErrorCode: ipc.CodeNoAPIKey,
			},
			wantPhrases: []string{ipc.CanonicalText[ipc.CodeNoAPIKey], ipc.HintFor(ipc.CodeNoAPIKey)},
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
				if !containsAction(p.AllowedActions, action) {
					t.Errorf("expected action %v, got %v", action, p.AllowedActions)
				}
			}
		})
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
			// The matching user is found by SID, not by list position.
			name: "running user matched by SID",
			now:  time.Unix(0, 0),
			ipc: fakeIPC{
				status: &ipc.StatusData{ServiceState: "running", ServiceMode: false},
				users: []ipc.UserStatus{
					{Username: "other", SID: "S-1-0-0-1", State: "paused"},
					{Username: "alice", SID: "S-1-0-0-2", State: "running", JobsDownloaded: 5},
				},
			},
			cfg:      newEnabledConfig(),
			identity: fakeIdentity{sid: "S-1-0-0-2", username: "alice"},
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
			name: "error text is reverse-looked-up to a code",
			now:  time.Unix(0, 0),
			ipc: fakeIPC{
				status: &ipc.StatusData{ServiceState: "running"},
				users: []ipc.UserStatus{
					{Username: "alice", State: "error", LastError: ipc.CanonicalText[ipc.CodeNoAPIKey]},
				},
			},
			cfg:      newEnabledConfig(),
			identity: fakeIdentity{username: "alice"},
			validate: func(t *testing.T, s State) {
				if s.PerUser != PerUserError {
					t.Errorf("PerUser = %v, want PerUserError", s.PerUser)
				}
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
				status: &ipc.StatusData{ServiceState: "running"},
				users: []ipc.UserStatus{{
					Username:  "alice",
					State:     "error",
					LastError: "some evolved wording",
					ErrorCode: ipc.CodeDownloadFolderInaccessible,
				}},
			},
			cfg:      newEnabledConfig(),
			identity: fakeIdentity{username: "alice"},
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
					ServiceState:  "running",
					LastError:     ipc.CanonicalText[ipc.CodeScanFailed] + ": list jobs failed: 503",
					LastErrorCode: ipc.CodeScanFailed,
					LastErrorTime: &errAt,
				},
				users: []ipc.UserStatus{{Username: "alice", State: "running"}},
			},
			cfg:      newEnabledConfig(),
			identity: fakeIdentity{username: "alice"},
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

// --- matchesWindowsUsername ---

func TestMatchesWindowsUsername(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"alice", "alice", true},
		{"Alice", "alice", true},
		{"DOMAIN\\alice", "alice", true},
		{"alice", "BOB", false},
		{"", "alice", false},
		{"alice", "", false},
	}
	for _, tc := range cases {
		if got := matchesWindowsUsername(tc.a, tc.b); got != tc.want {
			t.Errorf("matchesWindowsUsername(%q,%q)=%v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// helper

func containsAction(actions []Action, want Action) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}
