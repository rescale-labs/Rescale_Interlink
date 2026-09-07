//go:build linux

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withMachineIDFiles and withPIDNamespaceLink stand in for what Linux would say
// about a different machine or a different namespace.
func withMachineIDFiles(t *testing.T, paths ...string) {
	t.Helper()
	previous := machineIDFiles
	machineIDFiles = paths
	t.Cleanup(func() { machineIDFiles = previous })
}

func withPIDNamespaceLink(t *testing.T, target string) {
	t.Helper()
	link := filepath.Join(t.TempDir(), "pid")
	if target != "" {
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("plant the namespace link: %v", err)
		}
	}
	previous := pidNamespaceLink
	pidNamespaceLink = link
	t.Cleanup(func() { pidNamespaceLink = previous })
}

func writeMachineID(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "machine-id")
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write a machine identifier: %v", err)
	}
	return path
}

// TestReadPIDDomain_TellsTwoNamespacesOfOneMachineApart is the containers case.
// Two containers on one host read one machine identifier and mount one source
// tree, and number their processes independently — so the machine alone would
// put them in one domain and let each read the other's live PIDs as dead.
func TestReadPIDDomain_TellsTwoNamespacesOfOneMachineApart(t *testing.T) {
	machine := writeMachineID(t, "1cd67aa9d1b04b8f9a2c0e5f6b7d8e90\n")
	withMachineIDFiles(t, machine)

	withPIDNamespaceLink(t, "pid:[4026531836]")
	host, err := readPIDDomain()
	if err != nil {
		t.Fatalf("readPIDDomain failed: %v", err)
	}

	withPIDNamespaceLink(t, "pid:[4026532210]")
	container, err := readPIDDomain()
	if err != nil {
		t.Fatalf("readPIDDomain failed in the second namespace: %v", err)
	}

	if container == host {
		t.Errorf("two PID namespaces of one machine report one domain %q", host)
	}
	if !strings.Contains(host, "1cd67aa9d1b04b8f9a2c0e5f6b7d8e90") {
		t.Errorf("the domain %q does not carry the machine identifier; two machines could then answer to one namespace number", host)
	}
}

// TestReadPIDDomain_WithoutTheParts pins the conservative direction. Half a
// domain is worse than none: a machine identifier without a namespace is a
// string every container on that host would report, and reclamation would then
// cross exactly the boundary this is for.
func TestReadPIDDomain_WithoutTheParts(t *testing.T) {
	t.Run("no readable namespace", func(t *testing.T) {
		withMachineIDFiles(t, writeMachineID(t, "1cd67aa9d1b04b8f9a2c0e5f6b7d8e90"))
		withPIDNamespaceLink(t, "")

		if domain, err := readPIDDomain(); err == nil {
			t.Fatalf("reported domain %q with no namespace to name", domain)
		}
	})

	t.Run("no machine identifier anywhere", func(t *testing.T) {
		withMachineIDFiles(t, filepath.Join(t.TempDir(), "absent"))
		withPIDNamespaceLink(t, "pid:[4026531836]")

		if domain, err := readPIDDomain(); err == nil {
			t.Fatalf("reported domain %q with no machine to name", domain)
		}
	})

	t.Run("a machine identifier holding nothing", func(t *testing.T) {
		// systemd leaves the file empty until an image's first boot has
		// committed one; every such machine would otherwise share a domain.
		withMachineIDFiles(t, writeMachineID(t, "\n"))
		withPIDNamespaceLink(t, "pid:[4026531836]")

		if domain, err := readPIDDomain(); err == nil {
			t.Fatalf("reported domain %q from a machine-id file holding nothing", domain)
		}
	})
}

// TestReadPIDDomain_FallsBackToTheSecondMachineIDFile covers the systems that
// keep the identifier only where D-Bus put it.
func TestReadPIDDomain_FallsBackToTheSecondMachineIDFile(t *testing.T) {
	dbus := writeMachineID(t, "8f0142bc5e3a47d1b6c9a0f2e4d7c531")
	withMachineIDFiles(t, filepath.Join(t.TempDir(), "absent"), dbus)
	withPIDNamespaceLink(t, "pid:[4026531836]")

	domain, err := readPIDDomain()
	if err != nil {
		t.Fatalf("readPIDDomain failed with only the fallback file: %v", err)
	}
	if !strings.Contains(domain, "8f0142bc5e3a47d1b6c9a0f2e4d7c531") {
		t.Errorf("the domain %q does not carry the identifier the fallback file holds", domain)
	}
}
