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

func TestPresentationNotInstalled(t *testing.T) {
	p := State{Installation: InstallationNotInstalled}.Presentation()
	if !strings.Contains(p.GUILongForm, "Install Service") {
		t.Errorf("GUILongForm should prompt to install: %q", p.GUILongForm)
	}
	if !containsAction(p.AllowedActions, ActionInstallService) {
		t.Errorf("expected ActionInstallService, got %v", p.AllowedActions)
	}
}

func TestPresentationStopped(t *testing.T) {
	p := State{Installation: InstallationStopped}.Presentation()
	if !strings.Contains(p.GUILongForm, "Start Service") {
		t.Errorf("GUILongForm should prompt to start: %q", p.GUILongForm)
	}
	if !containsAction(p.AllowedActions, ActionStartService) {
		t.Errorf("expected ActionStartService, got %v", p.AllowedActions)
	}
}

func TestPresentationRunningNotConfigured(t *testing.T) {
	p := State{Installation: InstallationRunning, PerUser: PerUserNotConfigured}.Presentation()
	if !strings.Contains(p.GUILongForm, "Configure") {
		t.Errorf("GUILongForm should prompt to configure: %q", p.GUILongForm)
	}
	if !containsAction(p.AllowedActions, ActionConfigure) {
		t.Errorf("expected ActionConfigure, got %v", p.AllowedActions)
	}
}

func TestPresentationRunningActive(t *testing.T) {
	s := State{
		Installation:   InstallationRunning,
		PerUser:        PerUserRunning,
		JobsDownloaded: 7,
	}
	p := s.Presentation()
	if !strings.Contains(p.GUILongForm, "Auto-download active") {
		t.Errorf("GUILongForm should say active: %q", p.GUILongForm)
	}
	if !containsAction(p.AllowedActions, ActionPause) {
		t.Errorf("expected ActionPause when running, got %v", p.AllowedActions)
	}
	if !containsAction(p.AllowedActions, ActionTriggerScan) {
		t.Errorf("expected ActionTriggerScan when running")
	}
}

func TestPresentationPaused(t *testing.T) {
	p := State{Installation: InstallationRunning, PerUser: PerUserPaused}.Presentation()
	if !containsAction(p.AllowedActions, ActionResume) {
		t.Errorf("expected ActionResume, got %v", p.AllowedActions)
	}
}

func TestPresentationError_IncludesHint(t *testing.T) {
	s := State{
		Installation:  InstallationRunning,
		PerUser:       PerUserError,
		LastError:     ipc.CanonicalText[ipc.CodeNoAPIKey],
		LastErrorCode: ipc.CodeNoAPIKey,
	}
	p := s.Presentation()
	if !strings.Contains(p.GUILongForm, ipc.CanonicalText[ipc.CodeNoAPIKey]) {
		t.Errorf("GUILongForm should contain canonical text: %q", p.GUILongForm)
	}
	if !strings.Contains(p.GUILongForm, ipc.HintFor(ipc.CodeNoAPIKey)) {
		t.Errorf("GUILongForm should contain hint: %q", p.GUILongForm)
	}
}

// --- Compute tests ---

func TestCompute_NoConfigNoIPC(t *testing.T) {
	c := &Computer{
		Clock:    &fakeClock{t: time.Unix(0, 0)},
		Detector: fakeDetector{},
		IPC:      fakeIPC{statusErr: errors.New("no daemon")},
		Config:   fakeConfig{cfg: newDisabledConfig()},
		Identity: fakeIdentity{},
	}
	s := c.Compute(context.Background(), State{})
	if s.PerUser != PerUserNotConfigured {
		t.Errorf("PerUser = %v, want PerUserNotConfigured", s.PerUser)
	}
	if s.IPCConnected {
		t.Errorf("IPCConnected should be false")
	}
}

func TestCompute_ConfiguredNoIPCStaysPending(t *testing.T) {
	c := &Computer{
		Clock:    &fakeClock{t: time.Unix(1000, 0)},
		Detector: fakeDetector{},
		IPC:      fakeIPC{statusErr: errors.New("no daemon yet")},
		Config:   fakeConfig{cfg: newEnabledConfig()},
		Identity: fakeIdentity{},
	}
	s := c.Compute(context.Background(), State{})
	if s.PerUser != PerUserPending {
		t.Errorf("PerUser = %v, want PerUserPending", s.PerUser)
	}
	if s.PendingSince.IsZero() {
		t.Errorf("PendingSince should be set")
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

func TestCompute_IPCRunningMatchesUserBySID(t *testing.T) {
	c := &Computer{
		Clock:    &fakeClock{t: time.Unix(0, 0)},
		Detector: fakeDetector{},
		IPC: fakeIPC{
			status: &ipc.StatusData{ServiceState: "running", ServiceMode: false},
			users: []ipc.UserStatus{
				{Username: "other", SID: "S-1-0-0-1", State: "paused"},
				{Username: "alice", SID: "S-1-0-0-2", State: "running", JobsDownloaded: 5},
			},
		},
		Config:   fakeConfig{cfg: newEnabledConfig()},
		Identity: fakeIdentity{sid: "S-1-0-0-2", username: "alice"},
	}
	s := c.Compute(context.Background(), State{})
	if s.PerUser != PerUserRunning {
		t.Errorf("PerUser = %v, want PerUserRunning", s.PerUser)
	}
	if s.JobsDownloaded != 5 {
		t.Errorf("JobsDownloaded = %d, want 5", s.JobsDownloaded)
	}
}

func TestCompute_LastErrorReverseLookedUpToCode(t *testing.T) {
	c := &Computer{
		Clock:    &fakeClock{t: time.Unix(0, 0)},
		Detector: fakeDetector{},
		IPC: fakeIPC{
			status: &ipc.StatusData{ServiceState: "running"},
			users: []ipc.UserStatus{
				{Username: "alice", State: "error", LastError: ipc.CanonicalText[ipc.CodeNoAPIKey]},
			},
		},
		Config:   fakeConfig{cfg: newEnabledConfig()},
		Identity: fakeIdentity{username: "alice"},
	}
	s := c.Compute(context.Background(), State{})
	if s.PerUser != PerUserError {
		t.Errorf("PerUser = %v, want PerUserError", s.PerUser)
	}
	if s.LastErrorCode != ipc.CodeNoAPIKey {
		t.Errorf("LastErrorCode = %q, want %q (reverse-looked-up from canonical text)",
			s.LastErrorCode, ipc.CodeNoAPIKey)
	}
}

func TestCompute_ExplicitErrorCodePreferredOverReverseLookup(t *testing.T) {
	// If an IPC peer sets ErrorCode explicitly, Compute trusts that over a
	// reverse-lookup (future-proofs against text changes).
	c := &Computer{
		Clock:    &fakeClock{t: time.Unix(0, 0)},
		Detector: fakeDetector{},
		IPC: fakeIPC{
			status: &ipc.StatusData{ServiceState: "running"},
			users: []ipc.UserStatus{
				{
					Username:  "alice",
					State:     "error",
					LastError: "some evolved wording",
					ErrorCode: ipc.CodeDownloadFolderInaccessible,
				},
			},
		},
		Config:   fakeConfig{cfg: newEnabledConfig()},
		Identity: fakeIdentity{username: "alice"},
	}
	s := c.Compute(context.Background(), State{})
	if s.LastErrorCode != ipc.CodeDownloadFolderInaccessible {
		t.Errorf("LastErrorCode = %q, want explicit CodeDownloadFolderInaccessible", s.LastErrorCode)
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
