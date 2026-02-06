//go:build windows

// Package elevation provides UAC elevation support for Windows.
// v4.5.1: Used to run service control commands with admin privileges.
package elevation

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	shell32          = syscall.NewLazyDLL("shell32.dll")
	shellExecuteExW  = shell32.NewProc("ShellExecuteExW")
)

// SW_HIDE hides the window
const SW_HIDE = 0

// SHELLEXECUTEINFO structure for ShellExecuteExW
// https://docs.microsoft.com/en-us/windows/win32/api/shellapi/ns-shellapi-shellexecuteinfow
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           uintptr
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       uintptr
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      uintptr
	dwHotKey       uint32
	hIconOrMonitor uintptr
	hProcess       uintptr
}

// SEE_MASK_NOCLOSEPROCESS returns process handle
const SEE_MASK_NOCLOSEPROCESS = 0x00000040

// RunElevated executes a command with UAC elevation.
// Uses ShellExecuteExW with "runas" verb to trigger UAC prompt.
// v4.5.1: Added for GUI/tray to start/stop Windows Service.
func RunElevated(executable string, args string, workingDir string) error {
	verbPtr, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return fmt.Errorf("failed to convert verb: %w", err)
	}

	filePtr, err := syscall.UTF16PtrFromString(executable)
	if err != nil {
		return fmt.Errorf("failed to convert executable path: %w", err)
	}

	paramsPtr, err := syscall.UTF16PtrFromString(args)
	if err != nil {
		return fmt.Errorf("failed to convert parameters: %w", err)
	}

	var dirPtr *uint16
	if workingDir != "" {
		dirPtr, err = syscall.UTF16PtrFromString(workingDir)
		if err != nil {
			return fmt.Errorf("failed to convert directory: %w", err)
		}
	}

	sei := shellExecuteInfo{
		cbSize:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		fMask:        SEE_MASK_NOCLOSEPROCESS,
		lpVerb:       verbPtr,
		lpFile:       filePtr,
		lpParameters: paramsPtr,
		lpDirectory:  dirPtr,
		nShow:        SW_HIDE,
	}

	ret, _, err := shellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if ret == 0 {
		// ShellExecuteExW returns FALSE (0) on failure
		if err != nil && err != syscall.Errno(0) {
			return fmt.Errorf("ShellExecuteExW failed: %w", err)
		}
		return fmt.Errorf("ShellExecuteExW failed with unknown error")
	}

	// If we got a process handle, wait for it to complete and close it
	if sei.hProcess != 0 {
		syscall.WaitForSingleObject(syscall.Handle(sei.hProcess), syscall.INFINITE)
		syscall.CloseHandle(syscall.Handle(sei.hProcess))
	}

	return nil
}

// getCliExecutablePath resolves the CLI executable path.
// v4.5.1: Looks for rescale-int.exe in same directory as current executable.
func getCliExecutablePath() (string, string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("failed to get executable path: %w", err)
	}

	dir := filepath.Dir(exePath)
	cliPath := filepath.Join(dir, "rescale-int.exe")

	// Check if CLI exists in same directory
	if _, err := os.Stat(cliPath); err == nil {
		return cliPath, dir, nil
	}

	// Fallback: use bare name and let Windows search PATH
	// In this case, working directory is current directory
	cwd, _ := os.Getwd()
	return "rescale-int.exe", cwd, nil
}

// StartServiceElevated triggers UAC to run "rescale-int service start".
// v4.5.1: Returns nil on success, error on failure or UAC cancelled.
func StartServiceElevated() error {
	cliPath, workDir, err := getCliExecutablePath()
	if err != nil {
		return fmt.Errorf("failed to locate CLI: %w", err)
	}

	return RunElevated(cliPath, "service start", workDir)
}

// StopServiceElevated triggers UAC to run "rescale-int service stop".
// v4.5.1: Returns nil on success, error on failure or UAC cancelled.
func StopServiceElevated() error {
	cliPath, workDir, err := getCliExecutablePath()
	if err != nil {
		return fmt.Errorf("failed to locate CLI: %w", err)
	}

	return RunElevated(cliPath, "service stop", workDir)
}

// InstallServiceElevated triggers UAC to run "rescale-int service install".
// v4.5.8: Added for GUI/tray to install Windows Service with elevation.
// The elevated CLI process handles SCM registration and sets HKLM registry marker.
func InstallServiceElevated() error {
	cliPath, workDir, err := getCliExecutablePath()
	if err != nil {
		return fmt.Errorf("failed to locate CLI: %w", err)
	}

	return RunElevated(cliPath, "service install", workDir)
}

// UninstallServiceElevated triggers UAC to run "rescale-int service uninstall".
// v4.5.8: Added for GUI/tray to uninstall Windows Service with elevation.
// The elevated CLI process handles SCM removal and clears HKLM registry marker.
func UninstallServiceElevated() error {
	cliPath, workDir, err := getCliExecutablePath()
	if err != nil {
		return fmt.Errorf("failed to locate CLI: %w", err)
	}

	return RunElevated(cliPath, "service uninstall", workDir)
}
