//go:build windows

package wailsapp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// v4.7.6: Auto-launch the tray companion app if it exists and isn't already running.
// The tray provides system tray presence for daemon status and quick controls.

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW       = kernel32.NewProc("Process32FirstW")
	procProcess32NextW        = kernel32.NewProc("Process32NextW")
)

const (
	tH32CS_SNAPPROCESS = 0x00000002
	maxPath            = 260
)

type processEntry32W struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [maxPath]uint16
}

// isTrayRunning checks if rescale-int-tray.exe is already running.
func isTrayRunning() bool {
	handle, _, _ := procCreateToolhelp32Snapshot.Call(tH32CS_SNAPPROCESS, 0)
	if handle == uintptr(syscall.InvalidHandle) {
		return false
	}
	defer syscall.CloseHandle(syscall.Handle(handle))

	var entry processEntry32W
	entry.dwSize = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(handle, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return false
	}

	for {
		name := syscall.UTF16ToString(entry.szExeFile[:])
		if strings.EqualFold(name, "rescale-int-tray.exe") {
			return true
		}
		ret, _, _ = procProcess32NextW.Call(handle, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}
	return false
}

// launchTrayIfNeeded launches the tray companion if it exists and isn't already running.
func (a *App) launchTrayIfNeeded() {
	if isTrayRunning() {
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		return
	}

	trayPath := filepath.Join(filepath.Dir(exePath), "rescale-int-tray.exe")
	if _, err := os.Stat(trayPath); os.IsNotExist(err) {
		return // Tray executable not present
	}

	cmd := exec.Command(trayPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000, // CREATE_NO_WINDOW
	}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		a.logWarn("Tray", "Failed to launch tray companion: "+err.Error())
		return
	}

	a.logInfo("Tray", "Tray companion launched")
}
