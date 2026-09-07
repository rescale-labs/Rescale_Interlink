//go:build windows

package state

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code a process that has not exited reports.
// golang.org/x/sys/windows does not export STILL_ACTIVE under that name.
const stillActive = 259

// systemProcessLiveness opens the process just far enough to ask whether it is
// still there. PROCESS_QUERY_LIMITED_INFORMATION asks for the least the system
// can grant, so an elevated process of this login can usually be opened; a
// process whose security descriptor denies even that answers access denied,
// which is read as alive, never as absence. The exit code is then read rather
// than the successful open being taken as the answer: a handle outlives the
// process it names, so a process someone still holds a handle to can be opened
// and be gone. A process that exited with code 259, the value of STILL_ACTIVE,
// reads as alive and is refused, which is the conservative side.
func systemProcessLiveness(pid int) (processLiveness, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		liveness := windowsOpenProcessLiveness(err)
		if liveness == livenessUnknown {
			return liveness, fmt.Errorf("cannot open the process with PID %d: %w", pid, err)
		}
		return liveness, nil
	}
	defer windows.CloseHandle(handle)

	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return livenessUnknown, fmt.Errorf("cannot read the exit code of PID %d: %w", pid, err)
	}
	if code == stillActive {
		return livenessAlive, nil
	}
	return livenessDead, nil
}
