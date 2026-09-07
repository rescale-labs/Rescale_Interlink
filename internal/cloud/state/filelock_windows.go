//go:build windows

package state

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// guardLockLength is how much of the guard file the lock covers. The file holds
// nothing; one byte is enough to name a region, and every holder names the same
// one so that they exclude each other.
const guardLockLength = 1

// lockGuardFile takes the exclusive lock on an open guard file if it is free,
// and reports errGuardBusy rather than waiting when it is not: the caller bounds
// the wait itself, which LockFileEx has no form for. Windows attaches the lock
// to the file handle, so closing it releases the lock — including the close the
// kernel performs for a process that died, which is what stops an acquirer that
// crashed mid-acquisition from wedging the next one.
func lockGuardFile(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, guardLockLength, 0, &overlapped)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errGuardBusy
	}
	if errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) {
		// Volumes that carry no byte-range lock, which some network redirectors
		// answer this way rather than granting an exclusion they cannot honour.
		return errGuardLockUnsupported
	}
	return err
}

func unlockGuardFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, guardLockLength, 0, &overlapped)
}
