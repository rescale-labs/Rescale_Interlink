//go:build windows

// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// PIDFilePath returns the path to the daemon PID file.
// Uses %LOCALAPPDATA%\Rescale\Interlink\ (consistent with install/logs paths).
func PIDFilePath() string {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return filepath.Join(os.TempDir(), "rescale-daemon.pid")
	}
	return filepath.Join(localAppData, "Rescale", "Interlink", "daemon.pid")
}

// oldPIDFilePath returns the legacy PID file path for migration cleanup.
func oldPIDFilePath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	return filepath.Join(appData, "Rescale", "daemon.pid")
}

// lockExclusive takes an exclusive lock on the file at path, creating it, and
// holds it until unlock is called; see lockFile.
func lockExclusive(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	handle, overlapped := windows.Handle(f.Fd()), new(windows.Overlapped)
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, ^uint32(0), ^uint32(0), overlapped); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		windows.UnlockFileEx(handle, 0, ^uint32(0), ^uint32(0), overlapped)
		f.Close()
	}, nil
}

// Daemonize is not supported on Windows; 'daemon run --background' refuses
// before it gets here.
func Daemonize(args []string) error {
	return fmt.Errorf("--background is not supported on Windows; start auto-download from the Interlink app, or run 'daemon run' without --background")
}

// IsDaemonChild returns true if we're running as the daemon child process.
// On Windows, this is always false (no forking support).
func IsDaemonChild() bool {
	return false
}
