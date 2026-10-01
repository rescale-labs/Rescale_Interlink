//go:build windows

package service

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/rescale/rescale-int/internal/ipc"
)

// A service installed by an earlier version blocks a per-user daemon only while
// it runs; installed and stopped, which is all it can be now, it blocks none.
// Whether the service is installed is told apart from not being allowed to ask.
func TestServiceStateDecidesBlocking(t *testing.T) {
	if ipc.IsPipeInUse() {
		t.Skip("this user's daemon runs: its pipe would block every start")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir()) // no per-user daemon's PID file
	orig := scmQuery
	t.Cleanup(func() { scmQuery = orig })
	for _, tc := range []struct {
		name             string
		state            svc.State
		err              error
		installed        bool
		reason, blockMsg string
	}{
		{"not installed", 0, windows.ERROR_SERVICE_DOES_NOT_EXIST, false, "", ""},
		{"installed and stopped", svc.Stopped, nil, true, "", ""},
		{"installed and running", svc.Running, nil, true, "", OldServiceRunning},
		{"not allowed to ask", 0, windows.ERROR_ACCESS_DENIED, false, "denied", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scmQuery = func() (svc.State, error) { return tc.state, tc.err }
			installed, reason := IsInstalledWithReason()
			if installed != tc.installed || (tc.reason == "") != (reason == "") || !strings.Contains(reason, tc.reason) {
				t.Errorf("IsInstalledWithReason = (%v, %q), want (%v, containing %q)", installed, reason, tc.installed, tc.reason)
			}
			blocked, why := ShouldBlockSubprocess()
			if blocked != (tc.blockMsg != "") || !strings.Contains(why, tc.blockMsg) {
				t.Errorf("ShouldBlockSubprocess = (%v, %q), want it to block only for %q", blocked, why, tc.blockMsg)
			}
		})
	}
}

// A daemon from an earlier version listens where this version no longer looks,
// so the app cannot stop it; the refusal says what can: 'daemon stop --force',
// which ends it by its process, or Task Manager.
func TestBlockSubprocess_SaysHowToStopTheRunningDaemon(t *testing.T) {
	blocked, reason := blockSubprocess(ServiceDetectionResult{SubprocessPID: 1234})
	for _, want := range []string{"PID 1234", "'rescale-int daemon stop --force'", "Task Manager"} {
		if !blocked || !strings.Contains(reason, want) {
			t.Errorf("blockSubprocess = %v, %q; want a refusal naming %q", blocked, reason, want)
		}
	}
}
