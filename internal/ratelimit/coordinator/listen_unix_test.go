//go:build !windows

package coordinator

import (
	"net"
	"os"
	"path/filepath"
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
