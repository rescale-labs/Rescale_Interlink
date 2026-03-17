//go:build !windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
)

// shortSocketPath creates a short-lived temp directory for Unix sockets.
// macOS has a 104-byte limit on socket paths, so t.TempDir() paths can be too long.
func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

// TestIPCMessageSizeLimit verifies that messages exceeding the 1MB limit
// are rejected with an error rather than causing OOM.
func TestIPCMessageSizeLimit(t *testing.T) {
	socketPath := shortSocketPath(t, "s.sock")

	eventBus := events.NewEventBus(100)
	logger := logging.NewLogger("cli", eventBus)

	handler := &mockHandler{}
	server := NewServerWithPath(handler, logger, socketPath)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Stop()

	time.Sleep(50 * time.Millisecond)

	// Connect directly and send an oversized message (>1MB without newline)
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Send a message larger than 1MB without a newline delimiter
	oversized := make([]byte, maxIPCMessageSize+1024)
	for i := range oversized {
		oversized[i] = 'A' // Fill with non-newline bytes
	}
	_, err = conn.Write(oversized)
	if err != nil {
		// Write may fail if server closes connection early — that's acceptable
		t.Logf("Write returned error (acceptable): %v", err)
		return
	}

	// Read response — server should send an error response before closing
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		// Connection closed — acceptable behavior for oversized messages
		t.Logf("Read returned error (acceptable): %v", err)
		return
	}

	response := string(buf[:n])
	if !strings.Contains(response, "exceeds maximum size") {
		t.Errorf("Expected 'exceeds maximum size' error, got: %s", response)
	}
}

// TestIPCNormalMessageAccepted verifies that normal-sized messages are
// still processed correctly after adding the bounded read.
func TestIPCNormalMessageAccepted(t *testing.T) {
	socketPath := shortSocketPath(t, "n.sock")

	eventBus := events.NewEventBus(100)
	logger := logging.NewLogger("cli", eventBus)

	handler := &mockHandler{}
	server := NewServerWithPath(handler, logger, socketPath)

	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Stop()

	time.Sleep(50 * time.Millisecond)

	// Use the normal client to send a GetStatus request
	client := NewClientWithPath(socketPath)
	client.SetTimeout(2 * time.Second)

	ctx := context.Background()
	status, err := client.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus failed after bounded read change: %v", err)
	}
	if status.ServiceState != "running" {
		t.Errorf("Expected state 'running', got '%s'", status.ServiceState)
	}

	// Test with a raw request to verify scanner handles valid JSON
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Send a valid JSON request (small, well under 1MB)
	req := fmt.Sprintf(`{"type":"get_status","user_id":""}` + "\n")
	_, err = conn.Write([]byte(req))
	if err != nil {
		t.Fatalf("Failed to write request: %v", err)
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	response := string(buf[:n])
	if strings.Contains(response, "error") && strings.Contains(response, "maximum size") {
		t.Errorf("Normal message was rejected as oversized: %s", response)
	}
}
