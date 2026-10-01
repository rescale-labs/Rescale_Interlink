//go:build windows

package ipc

import (
	"testing"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
)

// capturingHandler implements ServiceHandler and records the user ID of a
// pause, so a test can see whether the server let one through.
type capturingHandler struct {
	lastPauseUserID string
	userList        []UserStatus // configurable user list for filtering tests
}

func (h *capturingHandler) GetStatus() *StatusData {
	return &StatusData{Version: "test"}
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

func (h *capturingHandler) ResumeUser(userID string) error  { return nil }
func (h *capturingHandler) TriggerScan(userID string) error { return nil }

func (h *capturingHandler) Shutdown() error {
	return nil
}

func (h *capturingHandler) GetRecentLogs(userID string, count int) []LogEntryData { return nil }

func (h *capturingHandler) ReloadConfig(userID string) *ReloadConfigData {
	return &ReloadConfigData{Applied: true}
}

func (h *capturingHandler) GetTransferStatus(userID string) (*DaemonTransferSnapshot, error) {
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

// The daemon's owner is served whatever user ID a request carries, the empty
// one every client sends included: the daemon serves only its owner.
func TestOwnerIsServedWithoutAUserID(t *testing.T) {
	handler := &capturingHandler{}
	server := newSubprocessModeServerForTest(handler)
	// NewServer recorded this process's own SID, which a test SID never matches.
	server.ownerSID = testSID
	for _, msg := range []MessageType{MsgPauseUser, MsgResumeUser, MsgTriggerScan, MsgGetRecentLogs} {
		for _, user := range []string{"", "u"} {
			if resp := server.handleRequest(NewRequestWithUser(msg, user), testSID); !resp.Success {
				t.Errorf("%s with user ID %q: %s", msg, user, resp.Error)
			}
		}
	}
}

// GetUserList passes on the daemon's entries as they are.
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

// The pipe's DACL keeps other users out; the owner check stays behind it.
func TestAuthorizeModifyRequest_RefusesAllButTheOwner(t *testing.T) {
	handler := &capturingHandler{}
	server := newSubprocessModeServerForTest(handler)
	server.ownerSID = testSID
	for _, caller := range []string{otherTestSID, ""} {
		if resp := server.handleRequest(NewRequestWithUser(MsgPauseUser, "u"), caller); resp.Success {
			t.Errorf("caller %q paused the daemon of %s", caller, testSID)
		}
	}
	if handler.lastPauseUserID != "" {
		t.Error("the handler ran for a refused caller")
	}
}
