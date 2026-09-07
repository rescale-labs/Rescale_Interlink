//go:build !windows

package state

import (
	"errors"
	"os"
	"syscall"
)

// lockGuardFile takes the exclusive advisory lock on an open guard file, waiting
// for whoever holds it. The lock belongs to the open file description rather
// than to the pathname, so the kernel drops it when the holder's last descriptor
// closes — including the close every process gets when it dies, which is what
// stops a taker that crashed mid-reclamation from wedging the next one.
func lockGuardFile(file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EOPNOTSUPP),
			errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.ENOLCK):
			// Filesystems that carry no lock at all: some network and fuse mounts
			// answer this way rather than granting an exclusion they cannot honour.
			return errGuardLockUnsupported
		default:
			return err
		}
	}
}

func unlockGuardFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
