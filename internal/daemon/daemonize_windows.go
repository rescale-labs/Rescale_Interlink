//go:build windows

// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

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

// processInfo names the executable of this user's process with this PID and
// the arguments it was started with, and fails for another user's process.
func processInfo(pid int) (string, []string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", nil, err
	}
	defer windows.CloseHandle(process)

	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", nil, err
	}
	defer token.Close()
	owner, err := token.GetTokenUser()
	if err != nil {
		return "", nil, err
	}
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", nil, err
	}
	if !owner.User.Sid.Equals(self.User.Sid) {
		return "", nil, fmt.Errorf("it runs as %s", owner.User.Sid)
	}

	name := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(name))
	if err := windows.QueryFullProcessImageName(process, 0, &name[0], &size); err != nil {
		return "", nil, err
	}

	// The first call says how much room the command line needs; the answer
	// is a UNICODE_STRING whose text follows it in the buffer.
	var need uint32
	_ = windows.NtQueryInformationProcess(process, windows.ProcessCommandLineInformation, nil, 0, &need)
	if need < uint32(unsafe.Sizeof(windows.NTUnicodeString{})) {
		return "", nil, fmt.Errorf("the system gave no command line")
	}
	buf := make([]uint64, (need+7)/8)
	if err := windows.NtQueryInformationProcess(process, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), need, &need); err != nil {
		return "", nil, err
	}
	args, err := windows.DecomposeCommandLine((*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String())
	if err != nil {
		return "", nil, err
	}
	return windows.UTF16ToString(name[:size]), args, nil
}

// endProcess ends the process with this PID. Windows has no request to end a
// windowless process that it would act on, so force makes no difference.
func endProcess(pid int, force bool) error {
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return windows.TerminateProcess(process, 1)
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
