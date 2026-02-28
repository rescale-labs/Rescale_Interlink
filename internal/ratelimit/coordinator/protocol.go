// Package coordinator provides cross-process rate limit coordination.
//
// When multiple Rescale Interlink processes (GUI + daemon + CLI) run concurrently,
// they share the same server-side API quota. The coordinator owns authoritative
// token buckets and arbitrates access so the combined traffic stays under limits.
package coordinator

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

// MessageType identifies the type of coordinator protocol message.
type MessageType string

const (
	// Client → Server request types
	MsgAcquire      MessageType = "Acquire"      // Request a single token
	MsgAcquireLease MessageType = "AcquireLease"  // Request a leased budget for fallback
	MsgDrain        MessageType = "Drain"         // Drain the authoritative bucket (429 received)
	MsgSetCooldown  MessageType = "SetCooldown"   // Set cooldown on authoritative bucket
	MsgHeartbeat    MessageType = "Heartbeat"     // Refresh lease, signal liveness
	MsgGetState     MessageType = "GetState"      // Request coordinator state (for status command)
	MsgShutdown     MessageType = "Shutdown"      // Request graceful shutdown

	// Server → Client response types
	MsgGranted      MessageType = "Granted"      // Token acquired
	MsgWait         MessageType = "Wait"         // No token — client should wait the indicated duration
	MsgLeaseGranted MessageType = "LeaseGranted" // Lease budget granted
	MsgOK           MessageType = "OK"           // Generic success
	MsgError        MessageType = "Error"        // Error response
	MsgStateData    MessageType = "StateData"    // Coordinator state dump
)

// Request is a message from a coordinator client to the server.
type Request struct {
	Type             MessageType     `json:"type"`
	ClientID         string          `json:"client_id"`                    // "pid-{os.Getpid()}"
	Scope            ratelimit.Scope `json:"scope,omitempty"`
	BaseURL          string          `json:"base_url,omitempty"`
	KeyHash          string          `json:"key_hash,omitempty"`           // SHA256[:8] hex of API key
	CooldownDuration time.Duration   `json:"cooldown_duration,omitempty"`
}

// Response is a message from the coordinator server to a client.
type Response struct {
	Type         MessageType `json:"type"`
	Success      bool        `json:"success"`
	Error        string      `json:"error,omitempty"`
	WaitDuration time.Duration `json:"wait_duration,omitempty"`
	Lease        *LeaseGrant `json:"lease,omitempty"`
	State        *StateInfo  `json:"state,omitempty"`
}

// LeaseGrant contains the parameters for a leased token budget.
// When a client has a lease, it can fall back to local rate limiting
// at the granted rate if the coordinator becomes unreachable.
type LeaseGrant struct {
	LeaseID   string          `json:"lease_id"`
	Scope     ratelimit.Scope `json:"scope"`
	Rate      float64         `json:"rate"`       // Tokens/sec for this lease
	Burst     float64         `json:"burst"`      // Burst capacity for this lease
	ExpiresAt time.Time       `json:"expires_at"` // Lease TTL (default 60s)
	RefreshBy time.Time       `json:"refresh_by"` // Send Heartbeat before this (default 30s)
}

// BucketKey identifies an authoritative token bucket on the coordinator.
// Each unique combination of {BaseURL, KeyHash, Scope} gets its own bucket.
type BucketKey struct {
	BaseURL string          `json:"base_url"`
	KeyHash string          `json:"key_hash"`
	Scope   ratelimit.Scope `json:"scope"`
}

// String returns the canonical string representation of a BucketKey.
func (k BucketKey) String() string {
	return fmt.Sprintf("%s|%s|%s", k.BaseURL, k.KeyHash, k.Scope)
}

// BucketKeyFromRequest extracts a BucketKey from a protocol Request.
func BucketKeyFromRequest(req *Request) BucketKey {
	return BucketKey{
		BaseURL: req.BaseURL,
		KeyHash: req.KeyHash,
		Scope:   req.Scope,
	}
}

// StateInfo contains coordinator state for the status command.
type StateInfo struct {
	Uptime        time.Duration          `json:"uptime"`
	ActiveClients int                    `json:"active_clients"`
	ActiveLeases  int                    `json:"active_leases"`
	Buckets       map[string]BucketState `json:"buckets"`
}

// BucketState describes the state of a single authoritative token bucket.
type BucketState struct {
	Scope            ratelimit.Scope `json:"scope"`
	Tokens           float64         `json:"tokens"`
	CooldownRemainMs int64           `json:"cooldown_remain_ms"`
	ActiveClients    int             `json:"active_clients"`
}

// Encode serializes a Request to JSON bytes (without trailing newline).
func (r *Request) Encode() ([]byte, error) {
	return json.Marshal(r)
}

// Encode serializes a Response to JSON bytes (without trailing newline).
func (r *Response) Encode() ([]byte, error) {
	return json.Marshal(r)
}

// DecodeRequest deserializes a Request from JSON bytes.
func DecodeRequest(data []byte) (*Request, error) {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// DecodeResponse deserializes a Response from JSON bytes.
func DecodeResponse(data []byte) (*Response, error) {
	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// NewGrantedResponse creates a response indicating a token was granted.
func NewGrantedResponse() *Response {
	return &Response{Type: MsgGranted, Success: true}
}

// NewWaitResponse creates a response indicating the client should wait.
func NewWaitResponse(d time.Duration) *Response {
	return &Response{Type: MsgWait, Success: true, WaitDuration: d}
}

// NewLeaseGrantedResponse creates a response with a lease grant.
func NewLeaseGrantedResponse(lease *LeaseGrant) *Response {
	return &Response{Type: MsgLeaseGranted, Success: true, Lease: lease}
}

// NewOKResponse creates a generic success response.
func NewOKResponse() *Response {
	return &Response{Type: MsgOK, Success: true}
}

// NewErrorResponse creates an error response.
func NewErrorResponse(errMsg string) *Response {
	return &Response{Type: MsgError, Success: false, Error: errMsg}
}

// NewStateDataResponse creates a response with coordinator state.
func NewStateDataResponse(state *StateInfo) *Response {
	return &Response{Type: MsgStateData, Success: true, State: state}
}
