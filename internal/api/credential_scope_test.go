package api

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/ratelimit"
)

// scopeRecorder is a coordinator that grants every token and counts the calls
// it is sent, by scope.
type scopeRecorder struct {
	mu    sync.Mutex
	calls map[string]int
}

func (r *scopeRecorder) note(call string, scope ratelimit.Scope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[call+" "+string(scope)]++
	return nil
}

func (r *scopeRecorder) Acquire(_ context.Context, _, _ string, s ratelimit.Scope) error {
	return r.note("Acquire", s)
}
func (r *scopeRecorder) Drain(_ context.Context, _, _ string, s ratelimit.Scope) error {
	return r.note("Drain", s)
}
func (r *scopeRecorder) SetCooldown(_ context.Context, _, _ string, s ratelimit.Scope, _ time.Duration) error {
	return r.note("SetCooldown", s)
}
func (*scopeRecorder) GetLease(string, string, ratelimit.Scope) *ratelimit.LeaseInfo { return nil }
func (*scopeRecorder) Ping(context.Context) error                                    { return nil }

// The platform counts credential requests apart from the user scope, so a burst
// of them must not spend the tokens every other v3 call needs, and a 429 on one
// must hold back credential requests alone, in this process and at the
// coordinator.
func TestCredentialRequestsHaveTheirOwnScope(t *testing.T) {
	var throttled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if throttled.Load() {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"storageType":"S3Storage","accessKey":"key","secretKey":"SECRET","sessionToken":"token"}`)
	}))
	defer server.Close()

	var mu sync.Mutex
	var notices []string
	ratelimit.SetGlobalNotifyFunc(func(_, message string) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, message)
	})
	defer ratelimit.SetGlobalNotifyFunc(nil)

	for _, coordinated := range []bool{false, true} {
		t.Run(fmt.Sprintf("coordinator=%v", coordinated), func(t *testing.T) {
			// The production limits and registry, one attempt per call: the 429
			// passes through the retry policy and then doRequest, as one that
			// outlives its retries does.
			ratelimit.ResetGlobalStore()
			t.Cleanup(ratelimit.ResetGlobalStore)
			store := ratelimit.GlobalStore()
			recorder := &scopeRecorder{calls: map[string]int{}}
			if coordinated {
				store.SetCoordinatorEnsurer(func() (ratelimit.CoordinatorClient, error) { return recorder, nil })
			}
			client := newTestClient(t, server.URL)
			client.store = store
			policy := &retryPolicy{store: store, baseURL: client.baseURL, apiKey: client.apiKey}
			client.httpClient = newRetryClient(&http.Client{}, policy, 0, time.Millisecond, time.Millisecond).StandardClient()
			user := store.GetLimiter(client.baseURL, client.apiKey, ratelimit.ScopeUser)
			ctx := context.Background()

			for range 20 {
				if _, _, err := client.GetStorageCredentials(ctx, nil); err != nil {
					t.Fatalf("GetStorageCredentials: %v", err)
				}
			}
			if tokens := user.GetCurrentTokens(); tokens < ratelimit.UserScopeBurstCapacity-0.5 {
				t.Errorf("the user scope holds %.1f tokens after 20 credential requests, want its full %d", tokens, ratelimit.UserScopeBurstCapacity)
			}

			throttled.Store(true)
			defer throttled.Store(false)
			if _, _, err := client.GetStorageCredentials(ctx, nil); err == nil {
				t.Fatal("a 429 did not fail the request")
			}
			credentials := store.GetLimiter(client.baseURL, client.apiKey, ratelimit.ScopeCredentialAccess)
			// Levels that seconds of delay cannot cross: the 7s cooldown stays over a
			// second for six more, and a drained bucket refills at 21.25/s while an
			// untouched one holds 150 or 300.
			if cooldown := credentials.CooldownRemaining(); cooldown < time.Second {
				t.Errorf("credential requests cool down for %v after a 429 asking for 7s", cooldown)
			}
			if tokens := credentials.GetCurrentTokens(); tokens >= 100 {
				t.Errorf("credential requests still hold %.1f tokens after a 429", tokens)
			}
			if cooldown := user.CooldownRemaining(); cooldown != 0 {
				t.Errorf("a 429 on a credential request cooled the user scope down for %v", cooldown)
			}

			want := map[string]int{}
			if coordinated {
				want = map[string]int{"Acquire credential-access": 21, "Drain credential-access": 2, "SetCooldown credential-access": 2}
			}
			if !maps.Equal(recorder.calls, want) {
				t.Errorf("the coordinator was sent %v, want %v", recorder.calls, want)
			}

			mu.Lock()
			defer mu.Unlock()
			if last := notices[len(notices)-1]; !strings.Contains(last, "exceeded the credential-access (90000/hour = 25.00/sec) scope limit") {
				t.Errorf("the throttle notice names the wrong scope: %q", last)
			}
		})
	}
}
