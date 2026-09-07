//go:build darwin

package state

import (
	"fmt"
	"strings"
	"syscall"
)

// pidDomainSysctl names the boot this process is running in, which is exactly
// the set of processes whose PIDs mean something to each other on macOS: there
// are no PID namespaces, every process is numbered by the one running kernel,
// and that numbering starts again at the next boot. The kernel generates the
// value at every boot, and any process may read it.
//
// It is not a machine identifier, and deliberately so: kern.uuid, which reads
// like one, is the Mach-O UUID of the kernel image, so every Mac booted from
// one macOS build reports it — and two of them sharing a source directory would
// each read the other's live PIDs as dead. Naming the boot costs one thing and
// buys another: a lock left behind by a crash is refused after the reboot that
// follows, and has to be deleted by hand once — while a machine restored from a
// clone, or two machines whose hardware identifiers were copied together, still
// answer to different domains.
const pidDomainSysctl = "kern.bootsessionuuid"

func readPIDDomain() (string, error) {
	uuid, err := syscall.Sysctl(pidDomainSysctl)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", pidDomainSysctl, err)
	}
	if uuid = strings.TrimSpace(uuid); uuid == "" {
		return "", fmt.Errorf("%s names no boot session", pidDomainSysctl)
	}
	return uuid, nil
}
