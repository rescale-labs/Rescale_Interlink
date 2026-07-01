//go:build windows

// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"syscall"
	"unsafe"
)

var (
	singleInstanceKernel32 = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutexW       = singleInstanceKernel32.NewProc("CreateMutexW")
	procCloseHandle        = singleInstanceKernel32.NewProc("CloseHandle")
	procReleaseMutex       = singleInstanceKernel32.NewProc("ReleaseMutex")
)

// daemonMutexName is the global named mutex that guarantees at most one
// auto-download daemon per interactive session. It is created in the Local
// namespace (per-session), matching the daemon's per-user subprocess model.
const daemonMutexName = "RescaleInterlinkDaemon_SingleInstance_v1"

// errorAlreadyExists is the Windows error returned by CreateMutex when the
// named mutex already exists (another daemon holds it).
const errorAlreadyExists = 183

// AcquireSingleInstanceLock attempts to acquire the process-wide daemon lock.
//
// It returns (release, true) when this process is the sole daemon; call
// release on shutdown to free the lock. It returns (nil, false) when another
// daemon already holds the lock, in which case the caller must exit without
// starting a second daemon.
//
// The lock is a Windows named mutex, so acquisition is atomic at the kernel
// level. Unlike PID-file or named-pipe checks (which have a TOCTOU window
// between spawn and initialization), two daemons launched simultaneously
// cannot both succeed here — exactly one CreateMutex call becomes the owner
// and the rest see ERROR_ALREADY_EXISTS.
func AcquireSingleInstanceLock() (release func(), ok bool) {
	namePtr, err := syscall.UTF16PtrFromString(daemonMutexName)
	if err != nil {
		// Can't build the name — fail open so the daemon still runs rather
		// than being permanently unable to start.
		return func() {}, true
	}

	handle, _, callErr := procCreateMutexW.Call(
		0, // lpMutexAttributes
		0, // bInitialOwner (we only need existence detection, not ownership)
		uintptr(unsafe.Pointer(namePtr)),
	)
	if handle == 0 {
		// CreateMutex failed outright — fail open rather than block startup.
		return func() {}, true
	}

	if callErr == syscall.Errno(errorAlreadyExists) {
		// Another daemon owns the lock. Close our handle (it does not grant
		// ownership) and report contention.
		procCloseHandle.Call(handle)
		return nil, false
	}

	release = func() {
		procReleaseMutex.Call(handle)
		procCloseHandle.Call(handle)
	}
	return release, true
}
