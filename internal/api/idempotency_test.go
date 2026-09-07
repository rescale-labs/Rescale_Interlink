package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// dropAfterRequest accepts the request in full and then closes the connection
// without answering. This is the ambiguous failure: the platform received the
// POST and may already have created the record, but the client is told only
// that the connection went away.
func dropAfterRequest(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "not hijackable", http.StatusInternalServerError)
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}

// newRetryingTestClient builds a client with the production retry wiring —
// which is what decides whether a failed create is sent again — against a test
// server. NewClientForTest deliberately has no retry policy, and NewClient
// refuses a non-platform URL, so the two are combined here.
func newRetryingTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()

	client := NewClientForTest(&config.Config{
		APIBaseURL: serverURL,
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	})
	policy, _ := newRetryTestPolicy(t, 2*time.Second)
	policy.baseURL = client.baseURL
	policy.store = client.store
	client.httpClient = newRetryClient(&http.Client{}, policy,
		apiRetryMax, 5*time.Millisecond, 10*time.Millisecond).StandardClient()
	return client
}

// countingHandler routes by path and counts what each endpoint was asked for.
type countingHandler struct {
	mu     sync.Mutex
	counts map[string]int
	routes map[string]http.HandlerFunc
}

func newCountingHandler() *countingHandler {
	return &countingHandler{counts: map[string]int{}, routes: map[string]http.HandlerFunc{}}
}

func (h *countingHandler) on(key string, fn http.HandlerFunc) {
	h.routes[key] = fn
}

func (h *countingHandler) count(key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.counts[key]
}

func (h *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for key, fn := range h.routes {
		method, path, _ := strings.Cut(key, " ")
		if r.Method != method || !strings.HasPrefix(r.URL.Path, path) {
			continue
		}
		h.mu.Lock()
		h.counts[key]++
		h.mu.Unlock()
		fn(w, r)
		return
	}
	http.Error(w, "unrouted "+r.Method+" "+r.URL.Path, http.StatusNotFound)
}

// TestCreateJobDoesNotRepeatAnAmbiguousRequest covers F17 for job creation. The
// platform has no idempotency key, so a POST that may already have created a
// job must not be sent again: the caller keeps only the last response's ID and
// the earlier jobs are untracked — and chargeable.
func TestCreateJobDoesNotRepeatAnAmbiguousRequest(t *testing.T) {
	handler := newCountingHandler()
	handler.on("POST /api/v3/jobs/", dropAfterRequest)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := newRetryingTestClient(t, server.URL)

	_, err := client.CreateJob(context.Background(), models.JobRequest{Name: "Run_17"})
	if err == nil {
		t.Fatal("CreateJob reported success although the platform never answered")
	}
	if got := handler.count("POST /api/v3/jobs/"); got != 1 {
		t.Errorf("the platform received %d job creations, want 1", got)
	}
	if !errors.Is(err, ErrJobMayExist) {
		t.Errorf("error %q does not report that the job may exist", err)
	}
	if !strings.Contains(err.Error(), "Run_17") {
		t.Errorf("error %q does not name the job the caller has to look for", err)
	}
}

// TestRegisterFileAdoptsTheRecordAnAmbiguousRequestCreated covers F17 for file
// registration: the record exists, so the retry has to find it rather than mint
// a second one describing the same uploaded bytes.
func TestRegisterFileAdoptsTheRecordAnAmbiguousRequestCreated(t *testing.T) {
	handler := newCountingHandler()
	handler.on("POST /api/v3/files/", dropAfterRequest)
	handler.on("GET /api/v3/folders/folder-1/contents/search/", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("search"); got != "Run_17.tar.gz" {
			t.Errorf("search term = %q, want the registered name", got)
		}
		writeJSON(t, w, map[string]any{
			"results": []map[string]any{
				{
					"type": "file",
					"item": map[string]any{
						"id":            "file-existing",
						"name":          "Run_17.tar.gz",
						"decryptedSize": 4096,
						"dateUploaded":  time.Now().UTC().Format(time.RFC3339),
					},
				},
			},
		})
	})
	handler.on("GET /api/v3/files/file-existing/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"id":            "file-existing",
			"name":          "Run_17.tar.gz",
			"decryptedSize": 4096,
		})
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := newRetryingTestClient(t, server.URL)

	file, err := client.RegisterFile(context.Background(), &models.CloudFileRequest{
		Name:            "Run_17.tar.gz",
		CurrentFolderID: "folder-1",
		DecryptedSize:   4096,
		IsUploaded:      true,
	})
	if err != nil {
		t.Fatalf("RegisterFile: %v", err)
	}
	if file.ID != "file-existing" {
		t.Errorf("adopted file ID = %q, want the record the first request created", file.ID)
	}
	if got := handler.count("POST /api/v3/files/"); got != 1 {
		t.Errorf("the platform received %d registrations, want 1", got)
	}
}

// TestRegisterFileRetriesWhenNothingWasCreated is the other half: reconciliation
// found no record, so the request demonstrably did not take effect and sending
// it again is safe.
func TestRegisterFileRetriesWhenNothingWasCreated(t *testing.T) {
	handler := newCountingHandler()
	var posts int
	var mu sync.Mutex
	handler.on("POST /api/v3/files/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		first := posts == 1
		mu.Unlock()
		if first {
			dropAfterRequest(w, r)
			return
		}
		writeJSON(t, w, map[string]any{"id": "file-new", "name": "Run_17.tar.gz"})
	})
	handler.on("GET /api/v3/folders/folder-1/contents/search/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"results": []map[string]any{}})
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := newRetryingTestClient(t, server.URL)

	file, err := client.RegisterFile(context.Background(), &models.CloudFileRequest{
		Name:            "Run_17.tar.gz",
		CurrentFolderID: "folder-1",
		DecryptedSize:   4096,
	})
	if err != nil {
		t.Fatalf("RegisterFile: %v", err)
	}
	if file.ID != "file-new" {
		t.Errorf("file ID = %q, want the record the retry created", file.ID)
	}
	if got := handler.count("POST /api/v3/files/"); got != 2 {
		t.Errorf("the platform received %d registrations, want 2 (one dropped, one retried)", got)
	}
}

// TestClassifyRetriesRequestsThatNeverLeft pins the distinction the fix turns
// on: a connection that was never established carried nothing, so repeating it
// cannot duplicate anything, whatever the request was.
func TestClassifyRetriesRequestsThatNeverLeft(t *testing.T) {
	policy, _ := newRetryTestPolicy(t, 0)
	create := withNonIdempotentCreate(context.Background())

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{
			name: "dial refused on a create",
			ctx:  create,
			err:  &net.OpError{Op: "dial", Err: errors.New("connection refused")},
			want: true,
		},
		{
			name: "DNS failure on a create",
			ctx:  create,
			err:  &net.DNSError{Err: "no such host", Name: "platform.rescale.test"},
			want: true,
		},
		{
			name: "connection dropped after the create was sent",
			ctx:  create,
			err:  io.ErrUnexpectedEOF,
			want: false,
		},
		{
			name: "connection dropped on a request that creates nothing",
			ctx:  context.Background(),
			err:  io.ErrUnexpectedEOF,
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retry, err := policy.classify(tc.ctx, nil, tc.err)
			if err != nil {
				t.Fatalf("classify returned %v alongside its decision", err)
			}
			if retry != tc.want {
				t.Errorf("classify retried = %v, want %v", retry, tc.want)
			}
		})
	}
}

// writeJSON answers with a JSON body, failing the test rather than the request
// if it cannot be encoded.
func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
