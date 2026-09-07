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

// readMachineID reads the first of the machine-id files that holds one. An
// empty file is not an identifier: systemd creates it empty on first boot of an
// image, and reporting the empty string would put every machine still in that
// state into one domain.
func readMachineID() (string, error) {
	var last error
	for _, path := range machineIDFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			last = err
			continue
		}
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
		last = fmt.Errorf("%s holds no machine identifier", path)
	}
	return "", fmt.Errorf("cannot read the machine identifier: %w", last)
}
