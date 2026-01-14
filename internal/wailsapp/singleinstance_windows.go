// +build windows

// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
// This file implements single-instance enforcement on Windows using named mutexes.
package wailsapp

import (
	"syscall"
	"unsafe"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	user32          = syscall.NewLazyDLL("user32.dll")
	createMutex     = kernel32.NewProc("CreateMutexW")
	findWindow      = user32.NewProc("FindWindowW")
	setForeground   = user32.NewProc("SetForegroundWindow")
	showWindow      = user32.NewProc("ShowWindow")
	isIconic        = user32.NewProc("IsIconic")
)

const (
	// Mutex name for single-instance enforcement
	mutexName = "RescaleInterlinkGUI_SingleInstance_v4"

	// Error code when mutex already exists
	ERROR_ALREADY_EXISTS = 183

	// ShowWindow commands
	SW_RESTORE = 9
)

// singleInstanceMutex holds the mutex handle (kept alive for process lifetime)
var singleInstanceMutex uintptr

// EnsureSingleInstance checks if another instance is already running.
// Returns true if this is the first instance, false if another instance exists.
// On Windows, if another instance exists, it will be brought to foreground.
func EnsureSingleInstance() bool {
	mutexNamePtr, _ := syscall.UTF16PtrFromString(mutexName)

	// Try to create a named mutex
	handle, _, err := createMutex.Call(
		0,                          // lpMutexAttributes
		0,                          // bInitialOwner
		uintptr(unsafe.Pointer(mutexNamePtr)),
	)

	if handle == 0 {
		return false // Failed to create mutex
	}

	// Check if mutex already existed
	if err == syscall.Errno(ERROR_ALREADY_EXISTS) {
		// Another instance is running - try to bring it to foreground
		bringExistingToForeground()
		return false
	}

	// Keep the mutex handle alive for the process lifetime
	singleInstanceMutex = handle
	return true
}

// bringExistingToForeground attempts to find and activate the existing window.
func bringExistingToForeground() {
	// Try to find the existing window by its title
	windowTitle, _ := syscall.UTF16PtrFromString("Rescale Interlink")
	hwnd, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(windowTitle)))

	if hwnd != 0 {
		// Check if window is minimized
		iconic, _, _ := isIconic.Call(hwnd)
		if iconic != 0 {
			// Restore the window if minimized
			showWindow.Call(hwnd, SW_RESTORE)
		}
		// Bring to foreground
		setForeground.Call(hwnd)
	}
}
