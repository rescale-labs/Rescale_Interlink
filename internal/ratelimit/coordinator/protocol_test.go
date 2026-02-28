package coordinator

import (
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

func TestRequestEncodeDecodeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{
			name: "Acquire",
			req: Request{
				Type:     MsgAcquire,
				ClientID: "pid-12345",
				Scope:    ratelimit.ScopeUser,
				BaseURL:  "https://platform.rescale.com",
				KeyHash:  "abcdef01",
			},
		},
		{
			name: "SetCooldown",
			req: Request{
				Type:             MsgSetCooldown,
				ClientID:         "pid-99",
				Scope:            ratelimit.ScopeJobSubmission,
				BaseURL:          "https://platform.rescale.com",
				KeyHash:          "12345678",
				CooldownDuration: 60 * time.Second,
			},
		},
		{
			name: "Heartbeat",
			req: Request{
				Type:     MsgHeartbeat,
				ClientID: "pid-42",
			},
		},
		{
			name: "Shutdown",
			req: Request{
				Type:     MsgShutdown,
				ClientID: "pid-1",
			},
		},
		{
			name: "GetState",
			req: Request{
				Type:     MsgGetState,
				ClientID: "pid-1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.req.Encode()
			if err != nil {
				t.Fatalf("Encode() error: %v", err)
			}

			decoded, err := DecodeRequest(data)
			if err != nil {
				t.Fatalf("DecodeRequest() error: %v", err)
			}

			if decoded.Type != tt.req.Type {
				t.Errorf("Type = %q, want %q", decoded.Type, tt.req.Type)
			}
			if decoded.ClientID != tt.req.ClientID {
				t.Errorf("ClientID = %q, want %q", decoded.ClientID, tt.req.ClientID)
			}
			if decoded.Scope != tt.req.Scope {
				t.Errorf("Scope = %q, want %q", decoded.Scope, tt.req.Scope)
			}
			if decoded.BaseURL != tt.req.BaseURL {
				t.Errorf("BaseURL = %q, want %q", decoded.BaseURL, tt.req.BaseURL)
			}
			if decoded.KeyHash != tt.req.KeyHash {
				t.Errorf("KeyHash = %q, want %q", decoded.KeyHash, tt.req.KeyHash)
			}
			if decoded.CooldownDuration != tt.req.CooldownDuration {
				t.Errorf("CooldownDuration = %v, want %v", decoded.CooldownDuration, tt.req.CooldownDuration)
			}
		})
	}
}

func TestResponseEncodeDecodeRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond) // Truncate for JSON round-trip precision

	tests := []struct {
		name string
		resp Response
	}{
		{
			name: "Granted",
			resp: *NewGrantedResponse(),
		},
		{
			name: "Wait",
			resp: *NewWaitResponse(5 * time.Second),
		},
		{
			name: "LeaseGranted",
			resp: *NewLeaseGrantedResponse(&LeaseGrant{
				LeaseID:   "lease-abc",
				Scope:     ratelimit.ScopeUser,
				Rate:      0.8,
				Burst:     75,
				ExpiresAt: now.Add(60 * time.Second),
				RefreshBy: now.Add(30 * time.Second),
			}),
		},
		{
			name: "OK",
			resp: *NewOKResponse(),
		},
		{
			name: "Error",
			resp: *NewErrorResponse("something went wrong"),
		},
		{
			name: "StateData",
			resp: *NewStateDataResponse(&StateInfo{
				Uptime:        120 * time.Second,
				ActiveClients: 2,
				ActiveLeases:  1,
				Buckets: map[string]BucketState{
					"https://platform.rescale.com|abcdef01|user": {
						Scope:            ratelimit.ScopeUser,
						Tokens:           5.5,
						CooldownRemainMs: 0,
						ActiveClients:    2,
					},
				},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.resp.Encode()
			if err != nil {
				t.Fatalf("Encode() error: %v", err)
			}

			decoded, err := DecodeResponse(data)
			if err != nil {
				t.Fatalf("DecodeResponse() error: %v", err)
			}

			if decoded.Type != tt.resp.Type {
				t.Errorf("Type = %q, want %q", decoded.Type, tt.resp.Type)
			}
			if decoded.Success != tt.resp.Success {
				t.Errorf("Success = %v, want %v", decoded.Success, tt.resp.Success)
			}
			if decoded.Error != tt.resp.Error {
				t.Errorf("Error = %q, want %q", decoded.Error, tt.resp.Error)
			}
			if decoded.WaitDuration != tt.resp.WaitDuration {
				t.Errorf("WaitDuration = %v, want %v", decoded.WaitDuration, tt.resp.WaitDuration)
			}
		})
	}
}

func TestBucketKeyString(t *testing.T) {
	key := BucketKey{
		BaseURL: "https://platform.rescale.com",
		KeyHash: "abcdef01",
		Scope:   ratelimit.ScopeUser,
	}

	expected := "https://platform.rescale.com|abcdef01|user"
	if got := key.String(); got != expected {
		t.Errorf("BucketKey.String() = %q, want %q", got, expected)
	}
}

func TestBucketKeyFromRequest(t *testing.T) {
	req := &Request{
		Type:     MsgAcquire,
		ClientID: "pid-1",
		BaseURL:  "https://platform.rescale.com",
		KeyHash:  "abcdef01",
		Scope:    ratelimit.ScopeJobsUsage,
	}

	key := BucketKeyFromRequest(req)
	if key.BaseURL != req.BaseURL {
		t.Errorf("BaseURL = %q, want %q", key.BaseURL, req.BaseURL)
	}
	if key.KeyHash != req.KeyHash {
		t.Errorf("KeyHash = %q, want %q", key.KeyHash, req.KeyHash)
	}
	if key.Scope != req.Scope {
		t.Errorf("Scope = %q, want %q", key.Scope, req.Scope)
	}
}

func TestDecodeRequestInvalidJSON(t *testing.T) {
	_, err := DecodeRequest([]byte("not json"))
	if err == nil {
		t.Error("DecodeRequest should fail on invalid JSON")
	}
}

func TestDecodeResponseInvalidJSON(t *testing.T) {
	_, err := DecodeResponse([]byte("{broken"))
	if err == nil {
		t.Error("DecodeResponse should fail on invalid JSON")
	}
}

func TestLeaseGrantRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	lease := &LeaseGrant{
		LeaseID:   "lease-xyz",
		Scope:     ratelimit.ScopeJobSubmission,
		Rate:      0.035,
		Burst:     0,
		ExpiresAt: now.Add(60 * time.Second),
		RefreshBy: now.Add(30 * time.Second),
	}

	resp := NewLeaseGrantedResponse(lease)
	data, err := resp.Encode()
	if err != nil {
		t.Fatalf("Encode() error: %v", err)
	}

	decoded, err := DecodeResponse(data)
	if err != nil {
		t.Fatalf("DecodeResponse() error: %v", err)
	}

	if decoded.Lease == nil {
		t.Fatal("decoded lease is nil")
	}
	if decoded.Lease.LeaseID != lease.LeaseID {
		t.Errorf("LeaseID = %q, want %q", decoded.Lease.LeaseID, lease.LeaseID)
	}
	if decoded.Lease.Rate != lease.Rate {
		t.Errorf("Rate = %v, want %v", decoded.Lease.Rate, lease.Rate)
	}
	if decoded.Lease.Burst != lease.Burst {
		t.Errorf("Burst = %v, want %v", decoded.Lease.Burst, lease.Burst)
	}
}
