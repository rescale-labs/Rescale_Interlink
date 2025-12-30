package ipc

import (
	"testing"
	"time"
)

func TestNewRequest(t *testing.T) {
	req := NewRequest(MsgGetStatus)
	if req.Type != MsgGetStatus {
		t.Errorf("expected type %q, got %q", MsgGetStatus, req.Type)
	}
	if req.UserID != "" {
		t.Errorf("expected empty user_id, got %q", req.UserID)
	}
}

func TestNewRequestWithUser(t *testing.T) {
	req := NewRequestWithUser(MsgPauseUser, "testuser")
	if req.Type != MsgPauseUser {
		t.Errorf("expected type %q, got %q", MsgPauseUser, req.Type)
	}
	if req.UserID != "testuser" {
		t.Errorf("expected user_id %q, got %q", "testuser", req.UserID)
	}
}

func TestRequestEncodeDecode(t *testing.T) {
	original := NewRequestWithUser(MsgTriggerScan, "user123")

	// Encode
	data, err := original.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	// Decode
	decoded, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest() error = %v", err)
	}

	if decoded.Type != original.Type {
		t.Errorf("Type mismatch: got %q, want %q", decoded.Type, original.Type)
	}
	if decoded.UserID != original.UserID {
		t.Errorf("UserID mismatch: got %q, want %q", decoded.UserID, original.UserID)
	}
}

func TestNewOKResponse(t *testing.T) {
	resp := NewOKResponse()
	if resp.Type != MsgOK {
		t.Errorf("expected type %q, got %q", MsgOK, resp.Type)
	}
	if !resp.Success {
		t.Error("expected Success = true")
	}
	if resp.Error != "" {
		t.Errorf("expected empty error, got %q", resp.Error)
	}
}

func TestNewErrorResponse(t *testing.T) {
	resp := NewErrorResponse("something went wrong")
	if resp.Type != MsgError {
		t.Errorf("expected type %q, got %q", MsgError, resp.Type)
	}
	if resp.Success {
		t.Error("expected Success = false")
	}
	if resp.Error != "something went wrong" {
		t.Errorf("expected error %q, got %q", "something went wrong", resp.Error)
	}
}

func TestNewStatusResponse(t *testing.T) {
	now := time.Now()
	status := &StatusData{
		ServiceState:    "running",
		Version:         "4.0.0",
		LastScanTime:    &now,
		ActiveDownloads: 2,
		ActiveUsers:     3,
		LastError:       "",
		Uptime:          "1h30m",
	}

	resp := NewStatusResponse(status)
	if resp.Type != MsgStatusResponse {
		t.Errorf("expected type %q, got %q", MsgStatusResponse, resp.Type)
	}
	if !resp.Success {
		t.Error("expected Success = true")
	}
}

func TestResponseEncodeDecode(t *testing.T) {
	now := time.Now()
	original := NewStatusResponse(&StatusData{
		ServiceState:    "running",
		Version:         "4.0.0",
		LastScanTime:    &now,
		ActiveDownloads: 1,
		ActiveUsers:     2,
	})

	// Encode
	data, err := original.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	// Decode
	decoded, err := DecodeResponse(data)
	if err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}

	if decoded.Type != original.Type {
		t.Errorf("Type mismatch: got %q, want %q", decoded.Type, original.Type)
	}
	if decoded.Success != original.Success {
		t.Errorf("Success mismatch: got %v, want %v", decoded.Success, original.Success)
	}
}

func TestGetStatusData(t *testing.T) {
	status := &StatusData{
		ServiceState: "running",
		Version:      "4.0.0",
		ActiveUsers:  5,
	}
	resp := NewStatusResponse(status)

	// Encode and decode to simulate real IPC
	data, _ := resp.Encode()
	decoded, _ := DecodeResponse(data)

	// Extract status data
	extracted := decoded.GetStatusData()
	if extracted == nil {
		t.Fatal("GetStatusData() returned nil")
	}

	if extracted.ServiceState != "running" {
		t.Errorf("ServiceState mismatch: got %q", extracted.ServiceState)
	}
	if extracted.Version != "4.0.0" {
		t.Errorf("Version mismatch: got %q", extracted.Version)
	}
	if extracted.ActiveUsers != 5 {
		t.Errorf("ActiveUsers mismatch: got %d", extracted.ActiveUsers)
	}
}

func TestGetUserListData(t *testing.T) {
	users := []UserStatus{
		{Username: "user1", State: "running", DownloadFolder: "/home/user1/downloads"},
		{Username: "user2", State: "paused", DownloadFolder: "/home/user2/downloads"},
	}
	resp := NewUserListResponse(users)

	// Encode and decode
	data, _ := resp.Encode()
	decoded, _ := DecodeResponse(data)

	// Extract user list
	extracted := decoded.GetUserListData()
	if extracted == nil {
		t.Fatal("GetUserListData() returned nil")
	}

	if len(extracted.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(extracted.Users))
	}

	if extracted.Users[0].Username != "user1" {
		t.Errorf("User[0].Username mismatch: got %q", extracted.Users[0].Username)
	}
	if extracted.Users[1].State != "paused" {
		t.Errorf("User[1].State mismatch: got %q", extracted.Users[1].State)
	}
}

func TestMessageTypes(t *testing.T) {
	// Verify message type constants are unique
	types := map[MessageType]bool{
		MsgGetStatus:        true,
		MsgPauseUser:        true,
		MsgResumeUser:       true,
		MsgTriggerScan:      true,
		MsgOpenLogs:         true,
		MsgOpenGUI:          true,
		MsgGetUserList:      true,
		MsgShutdown:         true,
		MsgStatusResponse:   true,
		MsgUserListResponse: true,
		MsgOK:               true,
		MsgError:            true,
	}

	if len(types) != 12 {
		t.Errorf("expected 12 unique message types, got %d", len(types))
	}
}

func TestPipeName(t *testing.T) {
	if PipeName == "" {
		t.Error("PipeName should not be empty")
	}
	if PipeName != `\\.\pipe\rescale-interlink` {
		t.Errorf("unexpected PipeName: %q", PipeName)
	}
}

func TestDecodeInvalidRequest(t *testing.T) {
	_, err := DecodeRequest([]byte("not valid json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestDecodeInvalidResponse(t *testing.T) {
	_, err := DecodeResponse([]byte("not valid json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestStatusData_Fields(t *testing.T) {
	now := time.Now()
	s := StatusData{
		ServiceState:    "running",
		Version:         "4.0.0",
		LastScanTime:    &now,
		ActiveDownloads: 3,
		ActiveUsers:     2,
		LastError:       "test error",
		Uptime:          "2h15m",
	}

	if s.ServiceState != "running" {
		t.Error("ServiceState mismatch")
	}
	if s.ActiveDownloads != 3 {
		t.Error("ActiveDownloads mismatch")
	}
	if s.LastError != "test error" {
		t.Error("LastError mismatch")
	}
}

func TestUserStatus_Fields(t *testing.T) {
	now := time.Now()
	u := UserStatus{
		Username:       "testuser",
		SID:            "S-1-5-21-123",
		State:          "running",
		DownloadFolder: "/downloads",
		LastScanTime:   &now,
		JobsDownloaded: 10,
		LastError:      "",
	}

	if u.Username != "testuser" {
		t.Error("Username mismatch")
	}
	if u.JobsDownloaded != 10 {
		t.Error("JobsDownloaded mismatch")
	}
}
