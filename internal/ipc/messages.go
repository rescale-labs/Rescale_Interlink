// Package ipc provides inter-process communication between the Windows service
// and the GUI/tray application using named pipes.
package ipc

import (
	"encoding/json"
	"time"
)

// PipeName is the Windows named pipe path for IPC.
const PipeName = `\\.\pipe\rescale-interlink`

// MessageType identifies the type of IPC message.
type MessageType string

const (
	// Request types (client -> server)
	MsgGetStatus         MessageType = "GetStatus"
	MsgPauseUser         MessageType = "PauseUser"
	MsgResumeUser        MessageType = "ResumeUser"
	MsgTriggerScan       MessageType = "TriggerScan"
	MsgOpenLogs          MessageType = "OpenLogs"
	MsgOpenGUI           MessageType = "OpenGUI"
	MsgGetUserList       MessageType = "GetUserList"
	MsgShutdown          MessageType = "Shutdown"
	MsgGetRecentLogs     MessageType = "GetRecentLogs"
	MsgReloadConfig      MessageType = "ReloadConfig"
	MsgGetTransferStatus MessageType = "GetTransferStatus"
	// Plan 3: per-row cancel/retry actions on daemon-initiated transfers.
	// Payload carries BatchID or TaskID via Request extension below.
	MsgCancelDaemonBatch      MessageType = "CancelDaemonBatch"
	MsgCancelDaemonTransfer   MessageType = "CancelDaemonTransfer"
	MsgRetryFailedInDaemonBatch MessageType = "RetryFailedInDaemonBatch"

	// Response types (server -> client)
	MsgStatusResponse         MessageType = "StatusResponse"
	MsgUserListResponse       MessageType = "UserListResponse"
	MsgOK                     MessageType = "OK"
	MsgError                  MessageType = "Error"
	MsgRecentLogs             MessageType = "RecentLogs"
	MsgReloadConfigResponse   MessageType = "ReloadConfigResponse"
	MsgTransferStatusResponse MessageType = "TransferStatusResponse"
)

// Request represents an IPC request from client to server.
type Request struct {
	Type MessageType `json:"type"`
	// UserID is the user identifier (SID or username) for user-specific operations.
	// Use "all" for operations that should affect all users.
	UserID string `json:"user_id,omitempty"`
	// BatchID is carried on CancelDaemonBatch / RetryFailedInDaemonBatch.
	BatchID string `json:"batch_id,omitempty"`
	// TaskID is carried on CancelDaemonTransfer.
	TaskID string `json:"task_id,omitempty"`
}

// Response represents an IPC response from server to client.
type Response struct {
	Type    MessageType `json:"type"`
	Success bool        `json:"success"`
	Error   string      `json:"error,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// StatusData contains the service status information.
type StatusData struct {
	// ServiceState is the overall service state ("running", "paused", "stopped")
	ServiceState string `json:"service_state"`

	// Version is the service version string
	Version string `json:"version"`

	// LastScanTime is when the last job scan completed
	LastScanTime *time.Time `json:"last_scan_time,omitempty"`

	// ActiveDownloads is the number of downloads currently in progress
	ActiveDownloads int `json:"active_downloads"`

	// ActiveUsers is the number of users with running daemons
	ActiveUsers int `json:"active_users"`

	// LastError is the most recent error message (if any), as canonical
	// English text (see CanonicalText in errors.go).
	LastError string `json:"last_error,omitempty"`

	// LastErrorCode is the machine-readable ErrorCode for LastError. Optional
	// for backwards compatibility.
	LastErrorCode ErrorCode `json:"last_error_code,omitempty"`

	// Uptime is how long the service has been running
	Uptime string `json:"uptime,omitempty"`

	// ServiceMode indicates whether the daemon is running as a Windows Service (true)
	// or as a subprocess (false).
	ServiceMode bool `json:"service_mode"`
}

// UserStatus contains status information for a single user's daemon.
type UserStatus struct {
	// Username is the user's account name
	Username string `json:"username"`

	// SID is the user's Security Identifier (Windows)
	SID string `json:"sid,omitempty"`

	// State is the daemon state ("running", "paused", "stopped", "error")
	State string `json:"state"`

	// DownloadFolder is the configured download directory
	DownloadFolder string `json:"download_folder"`

	// LastScanTime is when this user's last scan completed
	LastScanTime *time.Time `json:"last_scan_time,omitempty"`

	// JobsDownloaded is the total count of jobs downloaded for this user
	JobsDownloaded int `json:"jobs_downloaded"`

	// LastError is the most recent error for this user (if any), as canonical
	// English text (see CanonicalText in errors.go).
	LastError string `json:"last_error,omitempty"`

	// ErrorCode is the machine-readable ErrorCode corresponding to LastError.
	// Optional for backwards compatibility: older servers omit it, older
	// clients ignore it. New code should prefer comparing on ErrorCode.
	ErrorCode ErrorCode `json:"error_code,omitempty"`
}

// UserListData contains the list of user statuses.
type UserListData struct {
	Users []UserStatus `json:"users"`
}

// LogEntryData represents a single log entry for IPC streaming.
type LogEntryData struct {
	// Timestamp is the log entry time in RFC3339 format
	Timestamp string `json:"timestamp"`

	// Level is the log level (DEBUG, INFO, WARN, ERROR)
	Level string `json:"level"`

	// Stage identifies the component (Daemon, Monitor, Download, etc.)
	Stage string `json:"stage"`

	// Message is the log message text
	Message string `json:"message"`

	// Fields contains additional structured data
	Fields map[string]interface{} `json:"fields,omitempty"`
}

// RecentLogsData contains a batch of recent log entries.
type RecentLogsData struct {
	Entries []LogEntryData `json:"entries"`
}

// ReloadConfigData contains the result of a config reload request.
type ReloadConfigData struct {
	Applied         bool   `json:"applied"`
	Deferred        bool   `json:"deferred"`
	ActiveDownloads int    `json:"active_downloads"`
	Error           string `json:"error,omitempty"`
}

// TransferTaskInfo is the per-task projection of a daemon transfer task for
// IPC. Mirrors services.TransferTask so the frontend renders daemon rows
// with the same component as GUI rows.
type TransferTaskInfo struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"` // "download"
	State       string  `json:"state"`
	Name        string  `json:"name"`
	Source      string  `json:"source"`
	Dest        string  `json:"dest"`
	Size        int64   `json:"size"`
	Progress    float64 `json:"progress"`
	Speed       float64 `json:"speed"`
	Error       string  `json:"error,omitempty"`
	SourceLabel string  `json:"sourceLabel"` // always "Daemon"
	BatchID     string  `json:"batchId"`
	BatchLabel  string  `json:"batchLabel"`
	CreatedAt   int64   `json:"createdAt"`
	StartedAt   int64   `json:"startedAt,omitempty"`
	CompletedAt int64   `json:"completedAt,omitempty"`
}

// BatchStatsInfo is the per-batch projection for IPC; mirrors the GUI's
// transfer.BatchStats shape. Used by the unified Transfers tab.
type BatchStatsInfo struct {
	BatchID      string  `json:"batchId"`
	BatchLabel   string  `json:"batchLabel"`
	Direction    string  `json:"direction"` // "download"
	SourceLabel  string  `json:"sourceLabel"` // "Daemon"
	Total        int     `json:"total"`
	Queued       int     `json:"queued"`
	Active       int     `json:"active"`
	Completed    int     `json:"completed"`
	Failed       int     `json:"failed"`
	Cancelled    int     `json:"cancelled"`
	TotalBytes   int64   `json:"totalBytes"`
	Progress     float64 `json:"progress"`
	Speed        float64 `json:"speed"`
	TotalKnown   bool    `json:"totalKnown"`
	StartedAt    int64   `json:"startedAt,omitempty"`
}

// DaemonTransferSnapshot is a point-in-time view of the daemon's transfer
// queue, filtered to SourceLabel=Daemon. Replaces the earlier
// TransferStatusData/TransferBatchInfo shape. Frontend merges this into
// its unified tasks/batches arrays so daemon transfers render alongside
// GUI transfers in one list.
type DaemonTransferSnapshot struct {
	Tasks   []TransferTaskInfo `json:"tasks"`
	Batches []BatchStatsInfo   `json:"batches"`
}

// NewRequest creates a new IPC request.
func NewRequest(msgType MessageType) *Request {
	return &Request{Type: msgType}
}

// NewRequestWithUser creates a new IPC request for a specific user.
func NewRequestWithUser(msgType MessageType, userID string) *Request {
	return &Request{Type: msgType, UserID: userID}
}

// NewOKResponse creates a success response.
func NewOKResponse() *Response {
	return &Response{Type: MsgOK, Success: true}
}

// NewErrorResponse creates an error response.
func NewErrorResponse(err string) *Response {
	return &Response{Type: MsgError, Success: false, Error: err}
}

// NewStatusResponse creates a status response.
func NewStatusResponse(status *StatusData) *Response {
	return &Response{Type: MsgStatusResponse, Success: true, Data: status}
}

// NewUserListResponse creates a user list response.
func NewUserListResponse(users []UserStatus) *Response {
	return &Response{Type: MsgUserListResponse, Success: true, Data: &UserListData{Users: users}}
}

// NewRecentLogsResponse creates a recent logs response.
func NewRecentLogsResponse(entries []LogEntryData) *Response {
	return &Response{Type: MsgRecentLogs, Success: true, Data: &RecentLogsData{Entries: entries}}
}

// NewReloadConfigResponse creates a reload config response.
func NewReloadConfigResponse(data *ReloadConfigData) *Response {
	return &Response{Type: MsgReloadConfigResponse, Success: true, Data: data}
}

// NewDaemonTransferSnapshotResponse creates a transfer snapshot response.
func NewDaemonTransferSnapshotResponse(data *DaemonTransferSnapshot) *Response {
	return &Response{Type: MsgTransferStatusResponse, Success: true, Data: data}
}

// GetDaemonTransferSnapshot extracts a DaemonTransferSnapshot from a
// response. Returns nil if the response doesn't contain snapshot data.
func (r *Response) GetDaemonTransferSnapshot() *DaemonTransferSnapshot {
	if r.Data == nil {
		return nil
	}

	switch v := r.Data.(type) {
	case *DaemonTransferSnapshot:
		return v
	case DaemonTransferSnapshot:
		return &v
	case map[string]interface{}:
		data, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var result DaemonTransferSnapshot
		if err := json.Unmarshal(data, &result); err != nil {
			return nil
		}
		return &result
	}
	return nil
}

// Encode serializes a request to JSON.
func (r *Request) Encode() ([]byte, error) {
	return json.Marshal(r)
}

// Encode serializes a response to JSON.
func (r *Response) Encode() ([]byte, error) {
	return json.Marshal(r)
}

// DecodeRequest deserializes a request from JSON.
func DecodeRequest(data []byte) (*Request, error) {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// DecodeResponse deserializes a response from JSON.
func DecodeResponse(data []byte) (*Response, error) {
	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetStatusData extracts StatusData from a response.
// Returns nil if the response doesn't contain status data.
func (r *Response) GetStatusData() *StatusData {
	if r.Data == nil {
		return nil
	}

	// Handle both direct StatusData and map[string]interface{} from JSON
	switch v := r.Data.(type) {
	case *StatusData:
		return v
	case StatusData:
		return &v
	case map[string]interface{}:
		// Re-marshal and unmarshal to convert
		data, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var status StatusData
		if err := json.Unmarshal(data, &status); err != nil {
			return nil
		}
		return &status
	}
	return nil
}

// GetUserListData extracts UserListData from a response.
// Returns nil if the response doesn't contain user list data.
func (r *Response) GetUserListData() *UserListData {
	if r.Data == nil {
		return nil
	}

	switch v := r.Data.(type) {
	case *UserListData:
		return v
	case UserListData:
		return &v
	case map[string]interface{}:
		data, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var userList UserListData
		if err := json.Unmarshal(data, &userList); err != nil {
			return nil
		}
		return &userList
	}
	return nil
}

// GetReloadConfigData extracts ReloadConfigData from a response.
// Returns nil if the response doesn't contain reload config data.
func (r *Response) GetReloadConfigData() *ReloadConfigData {
	if r.Data == nil {
		return nil
	}

	switch v := r.Data.(type) {
	case *ReloadConfigData:
		return v
	case ReloadConfigData:
		return &v
	case map[string]interface{}:
		// Re-marshal and unmarshal to convert
		data, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var result ReloadConfigData
		if err := json.Unmarshal(data, &result); err != nil {
			return nil
		}
		return &result
	}
	return nil
}
