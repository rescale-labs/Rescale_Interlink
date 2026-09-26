//go:build !windows

package coordinator

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testEndpoint returns a socket path of the test's own, under a short temp
// directory: a Unix socket path is limited to about 104 bytes.
func testEndpoint(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "coordinator-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "test.sock")
}

// listenTest listens where the client dials on this platform: a Unix socket.
func listenTest(endpoint string) (net.Listener, error) {
	return net.Listen("unix", endpoint)
}

// Without a home folder the coordinator has no socket or PID file of this
// user's, and none in /tmp, where another user could hold the names.
func TestNoHome_NoSocketOrPIDFile(t *testing.T) {
	if os.Getenv("RESCALE_COORDINATOR_CHILD") != "" {
		t.Skip("this test binary was spawned as a coordinator; it must not spawn another")
	}
	t.Setenv("HOME", "")
	if path, err := SocketPath(); err == nil || path != "" {
		t.Fatalf("SocketPath with no home folder = %q, %v; want a refusal", path, err)
	}
	if path, err := PIDFilePath(); err == nil || path != "" {
		t.Fatalf("PIDFilePath with no home folder = %q, %v; want a refusal", path, err)
	}
	if l, err := Listen(); err == nil {
		l.Close()
		t.Error("Listen with no home folder succeeded")
	}
	if err := WritePIDFile(); err == nil {
		t.Error("WritePIDFile with no home folder succeeded")
	}
	if _, err := EnsureCoordinator(); err == nil || !strings.Contains(err.Error(), "home folder") {
		t.Errorf("EnsureCoordinator with no home folder = %v, want a refusal naming it", err)
	}
}
