//go:build !windows

// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockFilePath returns the path to the daemon single-instance lock file. It
// sits next to the PID file so both live under the same per-user directory.
func lockFilePath() string {
	return PIDFilePath() + ".lock"
}

// AcquireSingleInstanceLock attempts to acquire the process-wide daemon lock.
//
// It returns (release, true) when this process is the sole daemon; call
// release on shutdown to free the lock. It returns (nil, false) when another
// daemon already holds the lock, in which case the caller must exit without
// starting a second daemon.
//
// The lock is an exclusive, non-blocking flock on a lock file. The kernel
// releases it automatically if the process dies, so a crashed daemon never
// leaves a permanently stuck lock (unlike a stale PID file). Acquisition is
// atomic, closing the TOCTOU window that PID-file checks leave open between a
// daemon's spawn and its initialization.
func AcquireSingleInstanceLock() (release func(), ok bool) {
	path := lockFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		// Can't create the lock dir — fail open rather than block startup.
		return func() {}, true
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		// Can't open the lock file — fail open.
		return func() {}, true
	}

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		// Another daemon holds the lock.
		f.Close()
		return nil, false
	}

	release = func() {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}
	return release, true
}
