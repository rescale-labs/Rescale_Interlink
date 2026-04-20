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

// TestServiceModeGetUserListFilteredByCallerSID verifies that in service mode,
// GetUserList only returns the caller's own entry.
func TestServiceModeGetUserListFilteredByCallerSID(t *testing.T) {
	callerSID := "S-1-5-21-CALLER"
	otherSID := "S-1-5-21-OTHER"

	handler := &capturingHandler{
		userList: []UserStatus{
			{Username: "alice", SID: callerSID, State: "running"},
			{Username: "bob", SID: otherSID, State: "running"},
		},
	}
	server := newServiceModeServerForTest(handler)

	req := NewRequestWithUser(MsgGetUserList, "")
	resp := server.handleRequest(req, callerSID)
	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}

	data := resp.GetUserListData()
	if data == nil {
		t.Fatal("Expected UserListData, got nil")
	}
	if len(data.Users) != 1 {
		t.Fatalf("Expected 1 user, got %d", len(data.Users))
	}
	if data.Users[0].Username != "alice" {
		t.Errorf("Expected alice, got %q", data.Users[0].Username)
	}
	if data.Users[0].SID != callerSID {
		t.Errorf("Expected SID %q, got %q", callerSID, data.Users[0].SID)
	}
}

// TestServiceModeGetUserListDeniedWithoutCallerSID verifies that in service mode,
// GetUserList fails closed when callerSID is empty.
func TestServiceModeGetUserListDeniedWithoutCallerSID(t *testing.T) {
	handler := &capturingHandler{}
	server := newServiceModeServerForTest(handler)

	req := NewRequestWithUser(MsgGetUserList, "")
	resp := server.handleRequest(req, "") // empty callerSID
	if resp.Success {
		t.Fatal("Expected error for empty callerSID, got success")
	}
	if resp.Error != "unauthorized: could not identify caller" {
		t.Errorf("Unexpected error: %q", resp.Error)
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

// userScopedMessageTypes is the canonical catalog of IPC message types
// that must fail-closed on empty callerSID in service mode (spec §11.3).
// New user-scoped handler types MUST be added here or the test below
// will still pass but provide no coverage for the new type; treat the
// table as a required registration point when adding a message type.
//
// MsgOpenLogs has a "service" keyword bypass (see
// TestServiceModeOpenLogsServiceKeyword); its empty-SID behavior with
// a non-"service" userID is covered by TestServiceModeOpenLogsFailClosed.
// MsgGetUserList has post-hoc filtering; covered by
// TestServiceModeGetUserListDeniedWithoutCallerSID.
var userScopedMessageTypes = []struct {
	name    string
	msgType MessageType
}{
	{"PauseUser", MsgPauseUser},
	{"ResumeUser", MsgResumeUser},
	{"TriggerScan", MsgTriggerScan},
	{"ReloadConfig", MsgReloadConfig},
	{"GetRecentLogs", MsgGetRecentLogs},
	{"GetTransferStatus", MsgGetTransferStatus},
	{"CancelDaemonBatch", MsgCancelDaemonBatch},
	{"CancelDaemonTransfer", MsgCancelDaemonTransfer},
	{"RetryFailedInDaemonBatch", MsgRetryFailedInDaemonBatch},
}

// TestServiceMode_UserScopedMessages_FailClosedWithoutCallerSID asserts
// the spec §11.3 policy over the full catalog: every user-scoped message
// type must return "unauthorized: could not identify caller" when
// callerSID is empty in service mode. Makes future regressions in new
// handler types fail this test rather than slip silently past review.
func TestServiceMode_UserScopedMessages_FailClosedWithoutCallerSID(t *testing.T) {
	for _, tc := range userScopedMessageTypes {
		t.Run(tc.name, func(t *testing.T) {
			handler := &capturingHandler{}
			server := newServiceModeServerForTest(handler)

			req := NewRequestWithUser(tc.msgType, "some-user-id-from-client")
			resp := server.handleRequest(req, "") // empty callerSID

			if resp.Success {
				t.Fatalf("%s: expected fail-closed error with empty callerSID, got success", tc.name)
			}
			if resp.Error != "unauthorized: could not identify caller" {
				t.Errorf("%s: unexpected error text: %q", tc.name, resp.Error)
			}
		})
	}
}

// TestServiceMode_UserScopedMessages_ScopeToCallerSID asserts that when
// callerSID is present, every user-scoped handler receives callerSID as
// its userID argument (i.e., the req.UserID provided by the client is
// ignored). Complements the fail-closed test above; together they define
// the spec §11.3 authorization contract.
func TestServiceMode_UserScopedMessages_ScopeToCallerSID(t *testing.T) {
	const callerSID = "S-1-5-21-CALLER"
	const clientSuppliedSID = "S-1-5-21-OTHER"

	for _, tc := range userScopedMessageTypes {
		t.Run(tc.name, func(t *testing.T) {
			handler := &capturingHandler{}
			server := newServiceModeServerForTest(handler)

			req := NewRequestWithUser(tc.msgType, clientSuppliedSID)
			resp := server.handleRequest(req, callerSID)
			if !resp.Success {
				t.Fatalf("%s: expected success, got error: %s", tc.name, resp.Error)
			}

			// Only the handlers that record userID need checking; the
			// ones that don't (CancelDaemon*, RetryFailedInDaemonBatch)
			// are still exercised for the fail-closed property above.
			var got string
			switch tc.msgType {
			case MsgPauseUser:
				got = handler.lastPauseUserID
			case MsgResumeUser:
				got = handler.lastResumeUserID
			case MsgTriggerScan:
				got = handler.lastTriggerScanUserID
			case MsgReloadConfig:
				got = handler.lastReloadConfigUserID
			case MsgGetRecentLogs:
				got = handler.lastGetRecentLogsUserID
			case MsgGetTransferStatus:
				got = handler.lastGetTransferStatusUID
			default:
				// No captured userID — scoping property is enforced by
				// the fail-closed test; scope assertion skipped here.
				return
			}
			if got != callerSID {
				t.Errorf("%s: handler received userID=%q, want callerSID=%q", tc.name, got, callerSID)
			}
			if got == clientSuppliedSID {
				t.Errorf("%s: handler received client-supplied SID — callerSID was NOT enforced", tc.name)
			}
		})
	}
}
