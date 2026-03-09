//go:build windows

package platform

import (
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
)

var procSetThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

func inhibitSleep(_ string) (func(), error) {
	// Windows requires SetThreadExecutionState on a locked OS thread.
	// We spawn a dedicated goroutine that locks to its thread and blocks
	// until the release channel is signaled.
	done := make(chan struct{})
	ready := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		// Set ES_CONTINUOUS | ES_SYSTEM_REQUIRED to prevent sleep
		ret, _, _ := procSetThreadExecutionState.Call(uintptr(esContinuous | esSystemRequired))
		if ret == 0 {
			ready <- windows.GetLastError()
			return
		}
		ready <- nil

		// Block until release is called
		<-done

		// Clear: restore to ES_CONTINUOUS only
		procSetThreadExecutionState.Call(uintptr(esContinuous))
	}()

	if err := <-ready; err != nil {
		return func() {}, err
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			close(done)
		})
	}
	return release, nil
}
