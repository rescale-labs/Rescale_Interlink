//go:build windows

// Package service provides Windows Service Control Manager integration.
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/rescale/rescale-int/internal/ipc"
	"github.com/rescale/rescale-int/internal/logging"
)

// windowsService implements the svc.Handler interface for single-user mode.
type windowsService struct {
	service *Service
	elog    *eventlog.Log
}

// multiUserWindowsService implements svc.Handler for multi-user mode.
type multiUserWindowsService struct {
	service    *MultiUserService
	elog       *eventlog.Log
	ipcServer  *ipc.Server
	ipcHandler *ServiceIPCHandler
	logger     *logging.Logger
}

// Execute implements svc.Handler.Execute.
// This is called by the Windows Service Control Manager.
func (ws *windowsService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (svcSpecificEC bool, exitCode uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPauseAndContinue

	// Report start pending
	changes <- svc.Status{State: svc.StartPending}

	// Start the daemon
	if err := ws.service.Start(); err != nil {
		ws.elog.Error(1, fmt.Sprintf("Failed to start service: %v", err))
		changes <- svc.Status{State: svc.StopPending}
		return false, 1
	}

	// Report running
	ws.elog.Info(1, "Service started successfully")
	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	// Service loop - handle control requests
	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus

		case svc.Stop, svc.Shutdown:
			ws.elog.Info(1, "Service stop requested")
			changes <- svc.Status{State: svc.StopPending}
			ws.service.Stop()
			changes <- svc.Status{State: svc.Stopped}
			return false, 0

		case svc.Pause:
			ws.elog.Info(1, "Service pause requested")
			changes <- svc.Status{State: svc.PausePending}
			// For now, we stop polling on pause
			ws.service.Stop()
			changes <- svc.Status{State: svc.Paused, Accepts: cmdsAccepted}

		case svc.Continue:
			ws.elog.Info(1, "Service continue requested")
			changes <- svc.Status{State: svc.ContinuePending}
			if err := ws.service.Start(); err != nil {
				ws.elog.Error(1, fmt.Sprintf("Failed to continue service: %v", err))
			}
			changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

		default:
			ws.elog.Warning(1, fmt.Sprintf("Unexpected control request: %d", c.Cmd))
		}
	}

	return false, 0
}

// RunAsService starts the service under SCM control (single-user mode).
// This should be called when running as a Windows service in single-user mode.
func RunAsService(s *Service) error {
	// Open event log
	elog, err := eventlog.Open(ServiceName)
	if err != nil {
		return fmt.Errorf("failed to open event log: %w", err)
	}
	defer elog.Close()

	elog.Info(1, "Starting service (single-user mode)")

	ws := &windowsService{
		service: s,
		elog:    elog,
	}

	// Run the service
	err = svc.Run(ServiceName, ws)
	if err != nil {
		elog.Error(1, fmt.Sprintf("Service run failed: %v", err))
		return fmt.Errorf("failed to run service: %w", err)
	}

	return nil
}

// Execute implements svc.Handler.Execute for multi-user mode.
func (ws *multiUserWindowsService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (svcSpecificEC bool, exitCode uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPauseAndContinue

	// Report start pending
	changes <- svc.Status{State: svc.StartPending}

	// Start the multi-user daemon
	if err := ws.service.Start(); err != nil {
		ws.elog.Error(1, fmt.Sprintf("Failed to start multi-user service: %v", err))
		changes <- svc.Status{State: svc.StopPending}
		return false, 1
	}

	// Start the IPC server for tray/GUI communication
	if ws.ipcServer != nil {
		if err := ws.ipcServer.Start(); err != nil {
			ws.elog.Warning(1, fmt.Sprintf("Failed to start IPC server (tray communication unavailable): %v", err))
			// Non-fatal - service can still run without IPC
		} else {
			ws.elog.Info(1, "IPC server started for tray/GUI communication")
		}
	}

	// Report running
	ws.elog.Info(1, fmt.Sprintf("Multi-user service started successfully (%d users)", ws.service.RunningCount()))
	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	// Service loop - handle control requests
	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus

		case svc.Stop, svc.Shutdown:
			ws.elog.Info(1, "Multi-user service stop requested")
			changes <- svc.Status{State: svc.StopPending}
			// Stop IPC server first
			if ws.ipcServer != nil {
				ws.ipcServer.Stop()
			}
			ws.service.Stop()
			changes <- svc.Status{State: svc.Stopped}
			return false, 0

		case svc.Pause:
			ws.elog.Info(1, "Multi-user service pause requested")
			changes <- svc.Status{State: svc.PausePending}
			// Keep IPC server running during pause so tray can still communicate
			ws.service.Stop()
			changes <- svc.Status{State: svc.Paused, Accepts: cmdsAccepted}

		case svc.Continue:
			ws.elog.Info(1, "Multi-user service continue requested")
			changes <- svc.Status{State: svc.ContinuePending}
			if err := ws.service.Start(); err != nil {
				ws.elog.Error(1, fmt.Sprintf("Failed to continue multi-user service: %v", err))
			}
			ws.elog.Info(1, fmt.Sprintf("Multi-user service resumed (%d users)", ws.service.RunningCount()))
			changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

		default:
			ws.elog.Warning(1, fmt.Sprintf("Unexpected control request: %d", c.Cmd))
		}
	}

	return false, 0
}

// RunAsMultiUserService starts the service under SCM control (multi-user mode).
// This is the recommended mode for Windows services that need to support multiple user profiles.
func RunAsMultiUserService(s *MultiUserService) error {
	// Create logger for the service
	logger := logging.NewLogger("service", nil)

	// Open event log
	elog, err := eventlog.Open(ServiceName)
	if err != nil {
		return fmt.Errorf("failed to open event log: %w", err)
	}
	defer elog.Close()

	elog.Info(1, "Starting service (multi-user mode)")

	// Create IPC handler and server
	// v4.5.0: Use service mode server for multi-user mode
	// This relaxes owner-based auth since user-scoped routing handles isolation
	ipcHandler := NewServiceIPCHandler(s, logger)
	ipcServer := ipc.NewServiceModeServer(ipcHandler, logger)

	ws := &multiUserWindowsService{
		service:    s,
		elog:       elog,
		ipcServer:  ipcServer,
		ipcHandler: ipcHandler,
		logger:     logger,
	}

	// Run the service
	err = svc.Run(ServiceName, ws)
	if err != nil {
		elog.Error(1, fmt.Sprintf("Multi-user service run failed: %v", err))
		return fmt.Errorf("failed to run multi-user service: %w", err)
	}

	return nil
}

// IsWindowsService returns true if running as a Windows service.
func IsWindowsService() (bool, error) {
	return svc.IsWindowsService()
}

// IsInstalled returns true if the service is installed in the Service Control Manager.
// v4.3.6: Added for GUI to check service installation status.
func IsInstalled() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// Install installs the service with the Service Control Manager.
func Install(execPath string, configPath string) error {
	// Open service manager
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	// Check if service already exists
	s, err := m.OpenService(ServiceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", ServiceName)
	}

	// Build service arguments
	args := []string{"daemon", "run"}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}

	// Create service
	s, err = m.CreateService(ServiceName, execPath, mgr.Config{
		DisplayName: ServiceDisplayName,
		Description: ServiceDescription,
		StartType:   mgr.StartAutomatic,
	}, args...)
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}
	defer s.Close()

	// Set recovery actions (restart on failure)
	err = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400) // Reset failure count after 1 day
	if err != nil {
		// Non-fatal, just log
		fmt.Printf("Warning: failed to set recovery actions: %v\n", err)
	}

	// Create event log source
	err = eventlog.InstallAsEventCreate(ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info)
	if err != nil {
		// Non-fatal, just log
		fmt.Printf("Warning: failed to install event log: %v\n", err)
	}

	fmt.Printf("Service %s installed successfully\n", ServiceName)
	return nil
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

	fmt.Printf("Service %s uninstalled successfully\n", ServiceName)
	return nil
}

// Start starts the installed service.
func StartService() error {
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

	err = s.Start()
	if err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	fmt.Printf("Service %s started\n", ServiceName)
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

// QueryStatus returns the current service status.
func QueryStatus() (Status, error) {
	m, err := mgr.Connect()
	if err != nil {
		return StatusUnknown, fmt.Errorf("failed to connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return StatusStopped, nil // Service not installed = stopped
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return StatusUnknown, fmt.Errorf("failed to query service status: %w", err)
	}

	return svcStateToStatus(status.State), nil
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

// GetExecutablePath returns the path to the current executable.
func GetExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}
	return filepath.Abs(exe)
}
