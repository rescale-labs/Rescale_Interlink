package state

import (
	"errors"
	"syscall"
)

// processLiveness is what the operating system was willing to say about a PID.
// Three answers, not two: a probe that is refused has not established that the
// process is gone, and a lock is only ever reclaimed on positive evidence of
// absence.
type processLiveness int

const (
	// livenessUnknown is the zero value deliberately: a probe that says nothing
	// at all refuses the reclamation rather than authorising it.
	livenessUnknown processLiveness = iota
	livenessAlive
	livenessDead
)

// probeProcessLiveness asks this system about a PID. It is a variable so a test
// can decide the answer: a lock's owner has to be a process the test cannot
// create or kill portably. The error explains an unknown and is nil otherwise.
var probeProcessLiveness = func(pid int) (processLiveness, error) {
	if pid <= 0 {
		return livenessDead, nil
	}
	return systemProcessLiveness(pid)
}

// The two answers OpenProcess gives that decide the question, mirrored as plain
// numbers because golang.org/x/sys/windows builds on Windows alone and this
// mapping is compiled and tested everywhere.
const (
	windowsAccessDenied     = syscall.Errno(5)  // ERROR_ACCESS_DENIED
	windowsInvalidParameter = syscall.Errno(87) // ERROR_INVALID_PARAMETER
)

// windowsOpenProcessLiveness reads what OpenProcess answered about a PID.
// Access denied is evidence that the process is there: Windows checks the
// requested rights against the process's own security descriptor, so a live
// process of another login, an elevated one or a protected one answers exactly
// that, and reading it as absence is what clears a running upload's lock. A PID
// nothing is using answers invalid parameter. Anything else has not answered.
func windowsOpenProcessLiveness(err error) processLiveness {
	switch {
	case errors.Is(err, windowsAccessDenied):
		return livenessAlive
	case errors.Is(err, windowsInvalidParameter):
		return livenessDead
	default:
		return livenessUnknown
	}
}
