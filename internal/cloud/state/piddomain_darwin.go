//go:build darwin

package state

import (
	"fmt"
	"strings"
	"syscall"
)

// pidDomainSysctl names the running kernel, which is exactly the set of
// processes whose PIDs mean something to each other on macOS: there are no PID
// namespaces, and every process is numbered by that one kernel.
//
// It is generated per boot, not per machine. That costs one thing and buys
// another: a lock left behind by a crash is refused after the reboot that
// follows, and has to be deleted by hand once — while a machine restored from a
// clone, or two machines whose hardware identifiers were copied together, still
// answer to different domains.
const pidDomainSysctl = "kern.uuid"

func readPIDDomain() (string, error) {
	uuid, err := syscall.Sysctl(pidDomainSysctl)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", pidDomainSysctl, err)
	}
	if uuid = strings.TrimSpace(uuid); uuid == "" {
		return "", fmt.Errorf("%s names no kernel", pidDomainSysctl)
	}
	return uuid, nil
}
