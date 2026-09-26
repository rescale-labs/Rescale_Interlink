//go:build windows

// Package service provides Windows Service Control Manager integration.
package service

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// retiredService runs a retired service's work and returns, so that svc.Run
// reports it stopped with exit code 0, which the recovery actions that restart
// a failed service ignore.
type retiredService func(running func())

func (work retiredService) Execute(_ []string, _ <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	work(func() { changes <- svc.Status{State: svc.Running} })
	return false, 0
}

// RunDisabled runs a service installed by an earlier version, touching no user
// profile, and reports whether an SCM started this process. A failure has
// nowhere else to be reported: the service has no console, and an error report
// would be written to a profile.
func RunDisabled() bool {
	log := func(string) {}
	if elog, err := eventlog.Open(ServiceName); err == nil {
		defer elog.Close()
		log = func(msg string) { elog.Info(1, msg) }
	}
	return retire(scmHost{
		dispatch: func(work func(running func())) error { return svc.Run(ServiceName, retiredService(work)) },
		notService: func(err error) bool {
			return errors.Is(err, windows.ERROR_FAILED_SERVICE_CONTROLLER_CONNECT)
		},
		deleteSelf:  deleteSelf,
		clearMarker: clearInstalledMarker,
		log:         log,
	})
}

// deleteSelf marks this service for deletion. The SCM removes it once it has
// stopped and every handle to it is closed, the two opened here included; one
// already marked counts as done.
func deleteSelf() error {
	return withService(windows.DELETE, func(h windows.Handle) error {
		if err := windows.DeleteService(h); !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return err
		}
		return nil
	})
}

// withService opens this service with access, and the SCM with only the right
// to connect, runs fn, and closes both handles before it returns.
func withService(access uint32, fn func(windows.Handle) error) error {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(m)
	name, err := windows.UTF16PtrFromString(ServiceName)
	if err != nil {
		return err
	}
	h, err := windows.OpenService(m, name, access)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(h)
	return fn(h)
}

// clearInstalledMarker removes the HKLM marker that an earlier version's
// 'service install' set for the GUI and tray.
func clearInstalledMarker() {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Rescale\Interlink`, registry.SET_VALUE); err == nil {
		k.DeleteValue("ServiceInstalled")
		k.Close()
	}
}

// IsWindowsService returns true if running as a Windows service.
func IsWindowsService() (bool, error) {
	return svc.IsWindowsService()
}

// scmQuery returns the service's state. It asks only for the query rights
// that the default service permissions give a standard user: mgr.Connect asks
// for full access, which only an elevated administrator gets, so a standard
// user could not see that an old service is installed. A variable so a test
// can stand in for the SCM.
var scmQuery = func() (state svc.State, err error) {
	err = withService(windows.SERVICE_QUERY_STATUS, func(h windows.Handle) error {
		var st windows.SERVICE_STATUS
		err := windows.QueryServiceStatus(h, &st)
		state = svc.State(st.CurrentState)
		return err
	})
	return state, err
}

// IsInstalledWithReason returns (installed, errReason) for better diagnostics.
// The reason is empty when the service is simply not installed.
func IsInstalledWithReason() (bool, string) {
	_, err := scmQuery()
	if err == nil {
		return true, ""
	}
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false, ""
	}
	// DetectDaemon looks for "denied" to fall back to IPC; the OS text may not
	// be English.
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return false, fmt.Sprintf("SCM access denied: %v", err)
	}
	return false, fmt.Sprintf("Service query failed: %v", err)
}

// IsInstalled returns true if the service is installed in the Service Control Manager.
func IsInstalled() bool {
	installed, _ := IsInstalledWithReason()
	return installed
}

// Uninstall removes the service from the Service Control Manager.
func Uninstall() error {
	// Open service manager
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	// Open service
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("service %s not found: %w", ServiceName, err)
	}
	defer s.Close()

	// Stop service if running
	status, err := s.Query()
	if err == nil && status.State != svc.Stopped {
		_, err = s.Control(svc.Stop)
		if err != nil {
			fmt.Printf("Warning: failed to stop service: %v\n", err)
		}
		// Wait for service to stop
		for i := 0; i < 30; i++ {
			status, err = s.Query()
			if err != nil || status.State == svc.Stopped {
				break
			}
			time.Sleep(time.Second)
		}
	}

	// Delete service
	err = s.Delete()
	if err != nil {
		return fmt.Errorf("failed to delete service: %w", err)
	}

	// Remove event log source
	err = eventlog.Remove(ServiceName)
	if err != nil {
		// Non-fatal, just log
		fmt.Printf("Warning: failed to remove event log: %v\n", err)
	}

	clearInstalledMarker()

	fmt.Printf("Service %s uninstalled successfully\n", ServiceName)
	return nil
}

// StopService stops the installed service.
func StopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("failed to open service: %w", err)
	}
	defer s.Close()

	_, err = s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("failed to stop service: %w", err)
	}

	fmt.Printf("Service %s stopped\n", ServiceName)
	return nil
}

// QueryStatus returns the current service status; one that is not installed
// is stopped.
func QueryStatus() (Status, error) {
	st, err := scmQuery()
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return StatusStopped, nil
	}
	if err != nil {
		return StatusUnknown, err
	}
	return svcStateToStatus(st), nil
}

// svcStateToStatus converts Windows service state to our Status type.
func svcStateToStatus(state svc.State) Status {
	switch state {
	case svc.Stopped:
		return StatusStopped
	case svc.StartPending:
		return StatusStartPending
	case svc.StopPending:
		return StatusStopPending
	case svc.Running:
		return StatusRunning
	case svc.ContinuePending:
		return StatusContinuePending
	case svc.PausePending:
		return StatusPausePending
	case svc.Paused:
		return StatusPaused
	default:
		return StatusUnknown
	}
}
