package service

import (
	"errors"
	"slices"
	"testing"
)

// A service installed by an earlier version, when Windows starts it, reports
// running, marks its registration for deletion, clears the installed marker,
// logs one line and stops. A failed deletion is logged with how to remove the
// service, and the service still stops normally. Only a dispatch that no SCM
// answered means the process is not a service.
func TestRetire(t *testing.T) {
	errNotService, errDenied := errors.New("FAKE no service controller"), errors.New("FAKE access is denied")
	const failed = "log: Multi-user service mode is not available in this version, and this service could not remove itself: FAKE access is denied. " +
		"Remove it from the Interlink app or with 'rescale-int service uninstall' as administrator."
	for _, tc := range []struct {
		name                   string
		dispatchErr, deleteErr error
		want                   []string
		service                bool
	}{
		{"removes itself", nil, nil, []string{"register", "running", "delete", "marker", "log: " + RemovedMessage, "stopped"}, true},
		{"cannot remove itself", nil, errDenied, []string{"register", "running", "delete", failed, "stopped"}, true},
		{"not started by an SCM", errNotService, nil, nil, false},
		{"SCM dispatch fails otherwise", errors.New("FAKE dispatch failed"), nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			service := retire(scmHost{
				dispatch: func(work func(running func())) error {
					if tc.dispatchErr != nil {
						return tc.dispatchErr
					}
					got = append(got, "register")
					work(func() { got = append(got, "running") })
					got = append(got, "stopped")
					return nil
				},
				notService:  func(err error) bool { return err == errNotService },
				deleteSelf:  func() error { got = append(got, "delete"); return tc.deleteErr },
				clearMarker: func() { got = append(got, "marker") },
				log:         func(msg string) { got = append(got, "log: "+msg) },
			})
			if !slices.Equal(got, tc.want) || service != tc.service {
				t.Errorf("retire = %v, steps\n  %q\nwant %v, steps\n  %q", service, got, tc.service, tc.want)
			}
		})
	}
}
