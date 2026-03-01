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
}

func (h *capturingHandler) GetStatus() *StatusData {
	return &StatusData{ServiceState: "running", Version: "test"}
}

func (h *capturingHandler) GetUserList() []UserStatus {
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

func (h *capturingHandler) GetTransferStatus(userID string) (*TransferStatusData, error) {
	h.lastGetTransferStatusUID = userID
	return &TransferStatusData{}, nil
}

func newServiceModeServerForTest(handler ServiceHandler) *Server {
	eventBus := events.NewEventBus(100)
	logger := logging.NewLogger("test", eventBus)
	s := NewServer(handler, logger)
	s.serviceMode = true
	return s
}

func newSubprocessModeServerForTest(handler ServiceHandler) *Server {
	eventBus := events.NewEventBus(100)
	logger := logging.NewLogger("test", eventBus)
	return NewServer(handler, logger)
}

// TestServiceModeMutatingOpsUseCallerSID verifies that in service mode,
// PauseUser/ResumeUser/TriggerScan/ReloadConfig always use callerSID
// regardless of what the client sent in req.UserID.
func TestServiceModeMutatingOpsUseCallerSID(t *testing.T) {
	handler := &capturingHandler{}
	server := newServiceModeServerForTest(handler)

	callerSID := "S-1-5-21-CALLER"
	clientSuppliedSID := "S-1-5-21-OTHER-USER"

	tests := []struct {
		name     string
		msgType  MessageType
		getUID   func() string
	}{
		{"PauseUser", MsgPauseUser, func() string { return handler.lastPauseUserID }},
		{"ResumeUser", MsgResumeUser, func() string { return handler.lastResumeUserID }},
		{"TriggerScan", MsgTriggerScan, func() string { return handler.lastTriggerScanUserID }},
		{"ReloadConfig", MsgReloadConfig, func() string { return handler.lastReloadConfigUserID }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := NewRequestWithUser(tt.msgType, clientSuppliedSID)
			resp := server.handleRequest(req, callerSID)
			if !resp.Success {
				t.Fatalf("Expected success, got error: %s", resp.Error)
			}
			got := tt.getUID()
			if got != callerSID {
				t.Errorf("Handler received userID=%q, want callerSID=%q", got, callerSID)
			}
			if got == clientSuppliedSID {
				t.Errorf("Handler received client-supplied SID %q — callerSID was NOT enforced", clientSuppliedSID)
			}
		})
	}
}

// TestServiceModeReadOpsUseCallerSID verifies that in service mode,
// GetRecentLogs and GetTransferStatus always use callerSID.
func TestServiceModeReadOpsUseCallerSID(t *testing.T) {
	handler := &capturingHandler{}
	server := newServiceModeServerForTest(handler)

	callerSID := "S-1-5-21-CALLER"
	clientSuppliedSID := "S-1-5-21-OTHER-USER"

	tests := []struct {
		name    string
		msgType MessageType
		getUID  func() string
	}{
		{"GetRecentLogs", MsgGetRecentLogs, func() string { return handler.lastGetRecentLogsUserID }},
		{"GetTransferStatus", MsgGetTransferStatus, func() string { return handler.lastGetTransferStatusUID }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := NewRequestWithUser(tt.msgType, clientSuppliedSID)
			resp := server.handleRequest(req, callerSID)
			if !resp.Success {
				t.Fatalf("Expected success, got error: %s", resp.Error)
			}
			got := tt.getUID()
			if got != callerSID {
				t.Errorf("Handler received userID=%q, want callerSID=%q", got, callerSID)
			}
		})
	}
}

// TestServiceModeOpenLogsServiceKeyword verifies that the "service" keyword
// is still accepted in service mode (returns service-level logs).
func TestServiceModeOpenLogsServiceKeyword(t *testing.T) {
	handler := &capturingHandler{}
	server := newServiceModeServerForTest(handler)

	req := NewRequestWithUser(MsgOpenLogs, "service")
	resp := server.handleRequest(req, "S-1-5-21-CALLER")
	if !resp.Success {
		t.Fatalf("Expected success for service keyword, got error: %s", resp.Error)
	}
	// "service" keyword should pass through (not overridden by callerSID)
	if handler.lastOpenLogsUserID != "service" {
		t.Errorf("Handler received userID=%q, want 'service'", handler.lastOpenLogsUserID)
	}
}

// TestServiceModeOpenLogsFailClosed verifies that when callerSID is empty
// and userID is not "service", the request is denied rather than falling
// through to "service" logs.
func TestServiceModeOpenLogsFailClosed(t *testing.T) {
	handler := &capturingHandler{}
	server := newServiceModeServerForTest(handler)

	req := NewRequestWithUser(MsgOpenLogs, "some-user-id")
	resp := server.handleRequest(req, "") // empty callerSID
	if resp.Success {
		t.Fatal("Expected error for empty callerSID with non-service userID, got success")
	}
	if resp.Error != "unauthorized: could not identify caller" {
		t.Errorf("Unexpected error: %q", resp.Error)
	}
}

// TestSubprocessModeUnchanged verifies that non-service-mode behavior is
// unaffected — the client-supplied userID is used directly.
func TestSubprocessModeUnchanged(t *testing.T) {
	handler := &capturingHandler{}
	server := newSubprocessModeServerForTest(handler)

	clientUserID := "user-from-client"
	callerSID := "S-1-5-21-SOME-SID"

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
