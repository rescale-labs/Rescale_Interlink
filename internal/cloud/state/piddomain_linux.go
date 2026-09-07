//go:build linux

package state

import (
	"fmt"
	"os"
	"strings"
)

// machineIDFiles and pidNamespaceLink are where Linux says which machine this
// is and which set of PIDs the kernel is numbering this process among. They are
// variables so a test can stand in for another machine or another namespace.
var (
	machineIDFiles   = []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}
	pidNamespaceLink = "/proc/self/ns/pid"
)

// readPIDDomain names the machine and the PID namespace inside it. The machine
// alone would be wrong: two containers on one host share it, number their
// processes separately, and would read each other's live PIDs as dead. Neither
// half is optional — a process that cannot read its namespace has no way to
// tell itself from a process in another one on the same machine, so it reports
// no domain at all rather than half of one.
func readPIDDomain() (string, error) {
	machine, err := readMachineID()
	if err != nil {
		return "", err
	}
	namespace, err := os.Readlink(pidNamespaceLink)
	if err != nil {
		return "", fmt.Errorf("cannot read the PID namespace at %s: %w", pidNamespaceLink, err)
	}
	if namespace = strings.TrimSpace(namespace); namespace == "" {
		return "", fmt.Errorf("%s names no PID namespace", pidNamespaceLink)
	}
	return machine + " " + namespace, nil
}

// readMachineID reads the first of the machine-id files that holds one. A file
// that holds something else is not an identifier and does not end the search:
// systemd leaves the file empty until an image's first boot commits one and
// writes the literal "uninitialized" into images whose identifier is deferred,
// so any of those strings would put every machine still in that state — every
// container started from one image — into a single domain.
func readMachineID() (string, error) {
	var last error
	for _, path := range machineIDFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			last = err
			continue
		}
		if id := strings.TrimSpace(string(data)); isMachineID(id) {
			return id, nil
		}
		last = fmt.Errorf("%s holds no machine identifier", path)
	}
	return "", fmt.Errorf("cannot read the machine identifier: %w", last)
}

// isMachineID reports whether a string is one, as systemd defines it: exactly 32
// lowercase hexadecimal characters, not all zero. Upper case is rejected rather
// than folded, because a file holding one was not written by systemd and there
// is no reason to believe the rest of it either. "uninitialized", a truncated
// identifier and anything else fail on the same rule.
func isMachineID(id string) bool {
	if len(id) != 32 {
		return false
	}
	zero := true
	for _, character := range id {
		switch {
		case character >= '1' && character <= '9', character >= 'a' && character <= 'f':
			zero = false
		case character == '0':
		default:
			return false
		}
	}
	return !zero
}
