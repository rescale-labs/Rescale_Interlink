//go:build !windows

package state

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// systemProcessLiveness asks about a PID with the null signal, which runs every
// permission check and delivers nothing. A process owned by another user answers
// EPERM, and the refusal is itself the evidence that the process is there; only
// ESRCH says the PID names nothing.
func systemProcessLiveness(pid int) (processLiveness, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		// Unreachable on Unix, where FindProcess never fails.
		return livenessUnknown, fmt.Errorf("cannot find the process with PID %d: %w", pid, err)
	}

	switch err := process.Signal(syscall.Signal(0)); {
	case err == nil, errors.Is(err, syscall.EPERM):
		return livenessAlive, nil
	case errors.Is(err, os.ErrProcessDone), errors.Is(err, syscall.ESRCH):
		return livenessDead, nil
	default:
		return livenessUnknown, fmt.Errorf("cannot ask whether PID %d is running: %w", pid, err)
	}
}
