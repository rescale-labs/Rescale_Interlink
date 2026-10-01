package ipc

import (
	"reflect"
	"testing"
	"time"
)

func TestMessageConstructors(t *testing.T) {
	status := &StatusData{Version: "4.0.0"}
	for _, tc := range []struct {
		name      string
		got, want any
	}{
		{"NewRequest", NewRequest(MsgGetStatus), &Request{Type: MsgGetStatus}},
		{"NewRequestWithUser", NewRequestWithUser(MsgPauseUser, "testuser"), &Request{Type: MsgPauseUser, UserID: "testuser"}},
		{"NewOKResponse", NewOKResponse(), &Response{Type: MsgOK, Success: true}},
		{"NewErrorResponse", NewErrorResponse("something went wrong"), &Response{Type: MsgError, Error: "something went wrong"}},
		{"NewStatusResponse", NewStatusResponse(status), &Response{Type: MsgStatusResponse, Success: true, Data: status}},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("%s = %+v, want %+v", tc.name, tc.got, tc.want)
		}
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

func TestResponseEncodeDecode(t *testing.T) {
	now := time.Now()
	original := NewStatusResponse(&StatusData{
		Version:         "4.0.0",
		LastScanTime:    &now,
		ActiveDownloads: 1,
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
		Version:         "4.0.0",
		ActiveDownloads: 5,
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

	if extracted.Version != "4.0.0" {
		t.Errorf("Version mismatch: got %q", extracted.Version)
	}
	if extracted.ActiveDownloads != 5 {
		t.Errorf("ActiveDownloads mismatch: got %d", extracted.ActiveDownloads)
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
		MsgGetUserList:      true,
		MsgShutdown:         true,
		MsgStatusResponse:   true,
		MsgUserListResponse: true,
		MsgOK:               true,
		MsgError:            true,
	}

	if len(types) != 10 {
		t.Errorf("expected 10 unique message types, got %d", len(types))
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
