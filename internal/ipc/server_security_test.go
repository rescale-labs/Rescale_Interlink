//go:build windows

package ipc

import (
	"testing"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
)

// capturingHandler implements ServiceHandler and captures the userID argument
// passed to each handler method, so tests can verify the server correctly
// enforces caller scoping before delegation.
type capturingHandler struct {
	lastPauseUserID          string
	lastResumeUserID         string
	lastTriggerScanUserID    string
	lastReloadConfigUserID   string
	lastOpenLogsUserID       string
	lastGetRecentLogsUserID  string
	lastGetTransferStatusUID string
	userList                 []UserStatus // configurable user list for filtering tests
}

func (h *capturingHandler) GetStatus() *StatusData {
	return &StatusData{ServiceState: "running", Version: "test"}
}

func (h *capturingHandler) GetUserList() []UserStatus {
	if len(h.userList) > 0 {
		return h.userList
	}
	return []UserStatus{{Username: "testuser", State: "running"}}
}

func (h *capturingHandler) PauseUser(userID string) error {
	h.lastPauseUserID = userID
	return nil
}

func (h *capturingHandler) ResumeUser(userID string) error {
	h.lastResumeUserID = userID
	return nil
}

func (h *capturingHandler) TriggerScan(userID string) error {
	h.lastTriggerScanUserID = userID
	return nil
}

func (h *capturingHandler) OpenLogs(userID string) error {
	h.lastOpenLogsUserID = userID
	return nil
}

func (h *capturingHandler) Shutdown() error {
	return nil
}

func (h *capturingHandler) GetRecentLogs(userID string, count int) []LogEntryData {
	h.lastGetRecentLogsUserID = userID
	return nil
}

func (h *capturingHandler) ReloadConfig(userID string) *ReloadConfigData {
	h.lastReloadConfigUserID = userID
	return &ReloadConfigData{Applied: true}
}

func (h *capturingHandler) GetTransferStatus(userID string) (*DaemonTransferSnapshot, error) {
	h.lastGetTransferStatusUID = userID
	return &DaemonTransferSnapshot{}, nil
}

func (h *capturingHandler) CancelDaemonBatch(userID, batchID string) error   { return nil }
func (h *capturingHandler) CancelDaemonTransfer(userID, taskID string) error { return nil }
func (h *capturingHandler) RetryFailedInDaemonBatch(userID, batchID string) error {
	return nil
}

func newSubprocessModeServerForTest(handler ServiceHandler) *Server {
	eventBus := events.NewEventBus(100)
	logger := logging.NewLogger("test", eventBus)
	return NewServer(handler, logger)
}

// TestSubprocessModeUnchanged verifies that non-service-mode behavior is
// unaffected — the client-supplied userID is used directly.
func TestSubprocessModeUnchanged(t *testing.T) {
	handler := &capturingHandler{}
	server := newSubprocessModeServerForTest(handler)

	clientUserID := "user-from-client"
	callerSID := "S-1-5-21-SOME-SID"
	// Subprocess mode lets only the daemon's owner pause it; NewServer recorded
	// this process's own SID, which a test SID never matches.
	server.ownerSID = callerSID

	// In subprocess mode, the client-supplied userID should be used directly
	req := NewRequestWithUser(MsgPauseUser, clientUserID)
	resp := server.handleRequest(req, callerSID)
	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}
	if handler.lastPauseUserID != clientUserID {
		t.Errorf("Handler received userID=%q, want client-supplied %q", handler.lastPauseUserID, clientUserID)
	}

	// GetRecentLogs should also use client-supplied userID in subprocess mode
	req = NewRequestWithUser(MsgGetRecentLogs, clientUserID)
	resp = server.handleRequest(req, callerSID)
	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}
	if handler.lastGetRecentLogsUserID != clientUserID {
		t.Errorf("Handler received userID=%q, want client-supplied %q", handler.lastGetRecentLogsUserID, clientUserID)
	}
}

// TestSubprocessModeGetUserListUnfiltered verifies that in non-service mode,
// GetUserList returns all users unfiltered.
func TestSubprocessModeGetUserListUnfiltered(t *testing.T) {
	handler := &capturingHandler{
		userList: []UserStatus{
			{Username: "alice", SID: "S-1-5-21-ALICE", State: "running"},
			{Username: "bob", SID: "S-1-5-21-BOB", State: "running"},
		},
	}
	server := newSubprocessModeServerForTest(handler)

	req := NewRequestWithUser(MsgGetUserList, "")
	resp := server.handleRequest(req, "S-1-5-21-ALICE")
	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	data := resp.GetUserListData()
	if data == nil {
		t.Fatal("Expected UserListData, got nil")
	}
	if len(data.Users) != 2 {
		t.Errorf("Expected 2 users (unfiltered), got %d", len(data.Users))
	}
}
