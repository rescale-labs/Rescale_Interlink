//go:build windows

package service

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// The retired service tells the SCM it is running before its one-time work,
// then returns (false, 0), which svc.Run reports as stopped with exit code 0.
func TestRetiredServiceReportsRunningThenStops(t *testing.T) {
	changes := make(chan svc.Status, 1)
	var worked bool
	specific, code := retiredService(func(running func()) {
		running()
		if len(changes) != 1 {
			t.Error("the work ran before the service reported running")
		}
		worked = true
	}).Execute(nil, nil, changes)
	if specific || code != 0 || !worked {
		t.Errorf("Execute = (%v, %d), worked %v; want (false, 0) after the work", specific, code, worked)
	}
	if st := <-changes; st.State != svc.Running || st.Accepts != 0 {
		t.Errorf("reported %+v, want Running, accepting no controls", st)
	}
}

// With no service to remove, 'service uninstall' succeeds, so the installer
// can run it on any machine.
func TestUninstallWithoutTheServiceSucceeds(t *testing.T) {
	if IsInstalled() {
		t.Skip("a Rescale Interlink service is installed on this machine")
	}
	err := Uninstall()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Skip("removing a service needs administrator rights")
	}
	if err != nil {
		t.Errorf("Uninstall with no service: %v, want nil", err)
	}
}
