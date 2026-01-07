//go:build !windows

package ipc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
)

// mockHandler implements ServiceHandler for testing.
type mockHandler struct {
	pauseCalled    bool
	resumeCalled   bool
	scanCalled     bool
	shutdownCalled bool
}

func (h *mockHandler) GetStatus() *StatusData {
	return &StatusData{
		ServiceState:    "running",
		Version:         "test",
		ActiveDownloads: 2,
		ActiveUsers:     1,
	}
}

func (h *mockHandler) GetUserList() []UserStatus {
	return []UserStatus{
		{Username: "testuser", State: "running", JobsDownloaded: 5},
	}
}

func (h *mockHandler) PauseUser(userID string) error {
	h.pauseCalled = true
	return nil
}

func (h *mockHandler) ResumeUser(userID string) error {
	h.resumeCalled = true
	return nil
}

func (h *mockHandler) TriggerScan(userID string) error {
	h.scanCalled = true
	return nil
}

func (h *mockHandler) OpenLogs(userID string) error {
	return nil
}

func (h *mockHandler) Shutdown() error {
	h.shutdownCalled = true
	return nil
}

func TestUnixIPCClientServer(t *testing.T) {
	// Create temp socket path
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Create logger
	eventBus := events.NewEventBus(100)
	logger := logging.NewLogger("cli", eventBus)

	// Create handler
	handler := &mockHandler{}

	// Create server
	server := NewServerWithPath(handler, logger, socketPath)

	// Start server
	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer server.Stop()

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	// Create client
	client := NewClientWithPath(socketPath)
	client.SetTimeout(2 * time.Second)

	ctx := context.Background()

	// Test GetStatus
	t.Run("GetStatus", func(t *testing.T) {
		status, err := client.GetStatus(ctx)
		if err != nil {
			t.Fatalf("GetStatus failed: %v", err)
		}
		if status.ServiceState != "running" {
			t.Errorf("Expected state 'running', got '%s'", status.ServiceState)
		}
		if status.ActiveDownloads != 2 {
			t.Errorf("Expected 2 active downloads, got %d", status.ActiveDownloads)
		}
	})

	// Test GetUserList
	t.Run("GetUserList", func(t *testing.T) {
		users, err := client.GetUserList(ctx)
		if err != nil {
			t.Fatalf("GetUserList failed: %v", err)
		}
		if len(users) != 1 {
			t.Fatalf("Expected 1 user, got %d", len(users))
		}
		if users[0].Username != "testuser" {
			t.Errorf("Expected username 'testuser', got '%s'", users[0].Username)
		}
	})

	// Test PauseUser
	t.Run("PauseUser", func(t *testing.T) {
		err := client.PauseUser(ctx, "testuser")
		if err != nil {
			t.Fatalf("PauseUser failed: %v", err)
		}
		if !handler.pauseCalled {
			t.Error("PauseUser handler not called")
		}
	})

	// Test ResumeUser
	t.Run("ResumeUser", func(t *testing.T) {
		err := client.ResumeUser(ctx, "testuser")
		if err != nil {
			t.Fatalf("ResumeUser failed: %v", err)
		}
		if !handler.resumeCalled {
			t.Error("ResumeUser handler not called")
		}
	})

	// Test TriggerScan
	t.Run("TriggerScan", func(t *testing.T) {
		err := client.TriggerScan(ctx, "testuser")
		if err != nil {
			t.Fatalf("TriggerScan failed: %v", err)
		}
		if !handler.scanCalled {
			t.Error("TriggerScan handler not called")
		}
	})

	// Test IsServiceRunning
	t.Run("IsServiceRunning", func(t *testing.T) {
		if !client.IsServiceRunning(ctx) {
			t.Error("Expected IsServiceRunning to return true")
		}
	})

	// Test Ping
	t.Run("Ping", func(t *testing.T) {
		if err := client.Ping(ctx); err != nil {
			t.Errorf("Ping failed: %v", err)
		}
	})
}

func TestUnixIPCClientNoServer(t *testing.T) {
	// Create temp socket path that doesn't exist
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "nonexistent.sock")

	// Create client
	client := NewClientWithPath(socketPath)
	client.SetTimeout(100 * time.Millisecond)

	ctx := context.Background()

	// Test GetStatus should fail
	_, err := client.GetStatus(ctx)
	if err == nil {
		t.Error("Expected error when server is not running")
	}

	// Test IsServiceRunning should return false
	if client.IsServiceRunning(ctx) {
		t.Error("Expected IsServiceRunning to return false")
	}
}

func TestGetSocketPath(t *testing.T) {
	path := GetSocketPath()
	if path == "" {
		t.Error("GetSocketPath returned empty string")
	}

	// Should be in user's config directory
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "rescale", "interlink.sock")
	if path != expected {
		t.Errorf("Expected socket path '%s', got '%s'", expected, path)
	}
}
