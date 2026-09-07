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

	return newRetryingTestClientWithTimeout(t, serverURL, 0)
}

// newRetryingTestClientWithTimeout is newRetryingTestClient with the
// whole-request ceiling production applies (constants.HTTPClientTimeout, set on
// the client ConfigureHTTPClient builds) shortened enough to fire during a test.
func newRetryingTestClientWithTimeout(t *testing.T, serverURL string, timeout time.Duration) *Client {
	t.Helper()

	client := NewClientForTest(&config.Config{
		APIBaseURL: serverURL,
		APIKey:     "test-key",
		ProxyMode:  "no-proxy",
	})
	policy, _ := newRetryTestPolicy(t, 2*time.Second)
	policy.baseURL = client.baseURL
	policy.store = client.store
	client.httpClient = newRetryClient(&http.Client{Timeout: timeout}, policy,
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
			"results": []map[string]any{listedFile("file-existing", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz")},
		})
	})
	handler.on("GET /api/v3/files/file-existing/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, storedRecord("file-existing", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := newRetryingTestClient(t, server.URL)

	file, err := client.RegisterFile(context.Background(),
		storedAt("Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
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

	file, err := client.RegisterFile(context.Background(),
		storedAt("Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
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

// storedAt builds the registration the upload path sends: a name and a size,
// and the storage identity of the object the bytes actually landed on.
func storedAt(name string, size int64, objectPath string) *models.CloudFileRequest {
	return &models.CloudFileRequest{
		Name:            name,
		CurrentFolderID: "folder-1",
		DecryptedSize:   size,
		IsUploaded:      true,
		PathParts:       models.CloudFilePathParts{Container: "bucket-1", Path: objectPath},
		Storage:         models.CloudFileStorage{ID: "storage-1", StorageType: "S3Storage"},
	}
}

// listedFile is one entry of a folder-contents search response.
func listedFile(id, name string, size int64, objectPath string) map[string]any {
	return map[string]any{
		"type": "file",
		"item": map[string]any{
			"id":            id,
			"name":          name,
			"decryptedSize": size,
			"dateUploaded":  time.Now().UTC().Format(time.RFC3339),
			"pathParts":     map[string]any{"container": "bucket-1", "path": objectPath},
			"storage":       map[string]any{"id": "storage-1", "storageType": "S3Storage"},
		},
	}
}

// storedRecord is what GET /files/<id>/ answers with.
func storedRecord(id, name string, size int64, objectPath string) map[string]any {
	return map[string]any{
		"id":            id,
		"name":          name,
		"decryptedSize": size,
		"pathParts":     map[string]any{"container": "bucket-1", "path": objectPath},
		"storage":       map[string]any{"id": "storage-1", "storageType": "S3Storage"},
	}
}

// TestRegisterFileAdoptsOnlyTheRecordOfItsOwnObject covers N1. The platform
// allows same-named files, so two uploads of different content can share a name
// and a size in one folder. Only the storage path says which record describes
// the bytes this registration is for; adopting the other one hands the caller a
// file ID whose contents are someone else's, which PUR then checkpoints and
// whose archive it removes.
func TestRegisterFileAdoptsOnlyTheRecordOfItsOwnObject(t *testing.T) {
	handler := newCountingHandler()
	handler.on("POST /api/v3/files/", dropAfterRequest)
	handler.on("GET /api/v3/folders/folder-1/contents/search/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"results": []map[string]any{
				listedFile("file-other", "Run_17.tar.gz", 4096, "uploads/other.tar.gz"),
				listedFile("file-mine", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz"),
			},
		})
	})
	handler.on("GET /api/v3/files/file-other/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, storedRecord("file-other", "Run_17.tar.gz", 4096, "uploads/other.tar.gz"))
	})
	handler.on("GET /api/v3/files/file-mine/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, storedRecord("file-mine", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := newRetryingTestClient(t, server.URL)

	file, err := client.RegisterFile(context.Background(),
		storedAt("Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	if err != nil {
		t.Fatalf("RegisterFile: %v", err)
	}
	if file.ID != "file-mine" {
		t.Errorf("adopted file ID = %q, want the record for this upload's own object", file.ID)
	}
	if got := handler.count("POST /api/v3/files/"); got != 1 {
		t.Errorf("the platform received %d registrations, want 1", got)
	}
}

// TestRegisterFileRefusesTwoIndistinguishableRecords is the other half of N1:
// where the lookup cannot tell which record is this upload's, guessing is what
// produced the wrong-ID adoption. The caller is told instead, and nothing is
// registered a second time.
func TestRegisterFileRefusesTwoIndistinguishableRecords(t *testing.T) {
	handler := newCountingHandler()
	handler.on("POST /api/v3/files/", dropAfterRequest)
	handler.on("GET /api/v3/folders/folder-1/contents/search/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"results": []map[string]any{
				listedFile("file-a", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz"),
				listedFile("file-b", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz"),
			},
		})
	})
	handler.on("GET /api/v3/files/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, storedRecord("file-a", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := newRetryingTestClient(t, server.URL)

	_, err := client.RegisterFile(context.Background(),
		storedAt("Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	if err == nil {
		t.Fatal("RegisterFile adopted one of two records it cannot tell apart")
	}
	if !strings.Contains(err.Error(), "file-a") || !strings.Contains(err.Error(), "file-b") {
		t.Errorf("error %q does not name both records the user has to reconcile", err)
	}
	if got := handler.count("POST /api/v3/files/"); got != 1 {
		t.Errorf("the platform received %d registrations, want 1", got)
	}
}

// TestRegisterFileFollowsSearchPagination covers N2's first half: one page of
// results is not proof that no record was created. A match on page two used to
// be missed, and the retry then registered the same bytes twice.
func TestRegisterFileFollowsSearchPagination(t *testing.T) {
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
		writeJSON(t, w, map[string]any{"id": "file-duplicate", "name": "Run_17.tar.gz"})
	})
	handler.on("GET /api/v3/folders/folder-1/contents/search/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			writeJSON(t, w, map[string]any{
				"results": []map[string]any{listedFile("file-mine", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz")},
			})
			return
		}
		next := "https://platform.rescale.test/api/v3/folders/folder-1/contents/search/?search=Run_17.tar.gz&page=2"
		writeJSON(t, w, map[string]any{
			"next": next,
			// A same-named file of another size: real enough to fill page one,
			// and never a candidate.
			"results": []map[string]any{listedFile("file-older", "Run_17.tar.gz", 512, "uploads/older.tar.gz")},
		})
	})
	handler.on("GET /api/v3/files/file-mine/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, storedRecord("file-mine", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := newRetryingTestClient(t, server.URL)

	file, err := client.RegisterFile(context.Background(),
		storedAt("Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	if err != nil {
		t.Fatalf("RegisterFile: %v", err)
	}
	if file.ID != "file-mine" {
		t.Errorf("adopted file ID = %q, want the record found on page two", file.ID)
	}
	if got := handler.count("POST /api/v3/files/"); got != 1 {
		t.Errorf("the platform received %d registrations, want 1", got)
	}
}

// TestRegisterFileReconcilesATruncatedSuccess covers N2's body-read half: the
// platform answered 2xx, so the record exists, but the answer naming it did not
// arrive intact. Treating that as a plain failure loses the file ID; registering
// again mints a second record for the same bytes.
func TestRegisterFileReconcilesATruncatedSuccess(t *testing.T) {
	handler := newCountingHandler()
	handler.on("POST /api/v3/files/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"file-mi`))
	})
	handler.on("GET /api/v3/folders/folder-1/contents/search/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"results": []map[string]any{listedFile("file-mine", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz")},
		})
	})
	handler.on("GET /api/v3/files/file-mine/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, storedRecord("file-mine", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := newRetryingTestClient(t, server.URL)

	file, err := client.RegisterFile(context.Background(),
		storedAt("Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	if err != nil {
		t.Fatalf("RegisterFile: %v", err)
	}
	if file.ID != "file-mine" {
		t.Errorf("adopted file ID = %q, want the record the truncated answer described", file.ID)
	}
	if got := handler.count("POST /api/v3/files/"); got != 1 {
		t.Errorf("the platform received %d registrations, want 1", got)
	}
}

// TestRegisterFileDoesNotRepeatAnAcceptedRegistration is the same 2xx with a
// lookup that finds nothing. The platform accepted the registration, so sending
// it again would leave two records for one upload: the caller is told the ID is
// lost instead.
func TestRegisterFileDoesNotRepeatAnAcceptedRegistration(t *testing.T) {
	handler := newCountingHandler()
	handler.on("POST /api/v3/files/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"file-mi`))
	})
	handler.on("GET /api/v3/folders/folder-1/contents/search/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"results": []map[string]any{}})
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := newRetryingTestClient(t, server.URL)

	if _, err := client.RegisterFile(context.Background(),
		storedAt("Run_17.tar.gz", 4096, "uploads/mine.tar.gz")); err == nil {
		t.Fatal("RegisterFile reported success although it never learned the file ID")
	}
	if got := handler.count("POST /api/v3/files/"); got != 1 {
		t.Errorf("the platform received %d registrations, want 1", got)
	}
}

// TestRegisterFileReconcilesARequestTimeout covers N2's deadline half. The API
// client carries its own whole-request ceiling (constants.HTTPClientTimeout),
// and net/http reports its expiry as context.DeadlineExceeded — indistinguishable
// by errors.Is from a deadline the caller set. One fired after the request was
// delivered and is as ambiguous as a dropped connection.
func TestRegisterFileReconcilesARequestTimeout(t *testing.T) {
	handler := newCountingHandler()
	release := make(chan struct{})
	handler.on("POST /api/v3/files/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-release
	})
	handler.on("GET /api/v3/folders/folder-1/contents/search/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"results": []map[string]any{listedFile("file-mine", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz")},
		})
	})
	handler.on("GET /api/v3/files/file-mine/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, storedRecord("file-mine", "Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	})
	server := httptest.NewServer(handler)
	// Cleanups run last-registered first, so the stalled handler is let go
	// before the server is asked to wait for it.
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	client := newRetryingTestClientWithTimeout(t, server.URL, 50*time.Millisecond)

	// The caller's own context stays alive throughout: the deadline that fired
	// is the client's, inside the request path.
	file, err := client.RegisterFile(context.Background(),
		storedAt("Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	if err != nil {
		t.Fatalf("RegisterFile: %v", err)
	}
	if file.ID != "file-mine" {
		t.Errorf("adopted file ID = %q, want the record the timed-out request created", file.ID)
	}
	if got := handler.count("GET /api/v3/folders/folder-1/contents/search/"); got == 0 {
		t.Error("a timed-out registration was not reconciled at all")
	}
}

// TestRegisterFileReportsTheCallersOwnDeadline is the boundary of the previous
// test: when the caller's context is what expired, there is nothing to reconcile
// against — the lookup would use the same dead context — and the caller is told
// so directly.
func TestRegisterFileReportsTheCallersOwnDeadline(t *testing.T) {
	handler := newCountingHandler()
	release := make(chan struct{})
	handler.on("POST /api/v3/files/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-release
	})
	handler.on("GET /api/v3/folders/folder-1/contents/search/", func(w http.ResponseWriter, r *http.Request) {
		t.Error("the lookup ran on the caller's expired context")
		writeJSON(t, w, map[string]any{"results": []map[string]any{}})
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	client := newRetryingTestClient(t, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := client.RegisterFile(ctx, storedAt("Run_17.tar.gz", 4096, "uploads/mine.tar.gz")); err == nil {
		t.Fatal("RegisterFile reported success although the caller's deadline expired")
	}
}

// TestCreateJobReportsATruncatedSuccess covers N2 for job creation: a 2xx the
// client cannot parse means the job exists and its ID is lost. Reported as a
// plain decode failure, the caller would create and pay for a second one.
func TestCreateJobReportsATruncatedSuccess(t *testing.T) {
	handler := newCountingHandler()
	handler.on("POST /api/v3/jobs/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"job-a`))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client := newRetryingTestClient(t, server.URL)

	_, err := client.CreateJob(context.Background(), models.JobRequest{Name: "Run_17"})
	if err == nil {
		t.Fatal("CreateJob reported success although it never learned the job ID")
	}
	if !errors.Is(err, ErrJobMayExist) {
		t.Errorf("error %q does not report that the job may exist", err)
	}
	if !strings.Contains(err.Error(), "Run_17") {
		t.Errorf("error %q does not name the job the caller has to look for", err)
	}
	if got := handler.count("POST /api/v3/jobs/"); got != 1 {
		t.Errorf("the platform received %d job creations, want 1", got)
	}
}

// stallAfterRequest accepts the request in full, announces that the platform
// now holds it, and never answers. It is the delivered-then-abandoned case: the
// platform may act on the request at any moment, and the caller walks away
// without ever learning whether it did.
func stallAfterRequest(arrived chan<- struct{}, release <-chan struct{}) http.HandlerFunc {
	var once sync.Once
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(arrived) })
		<-release
	}
}

// TestRegisterFileReportsAPossibleRecordWhenTheCallerCancels covers D7 for
// registration. The POST was delivered; the caller then cancelled while waiting
// for the answer. Reported as a plain cancellation, a resumed run registers the
// same uploaded bytes a second time, so the caller has to be told the record may
// already exist — without a lookup, which would run on the context that just died.
func TestRegisterFileReportsAPossibleRecordWhenTheCallerCancels(t *testing.T) {
	handler := newCountingHandler()
	arrived := make(chan struct{})
	release := make(chan struct{})
	handler.on("POST /api/v3/files/", stallAfterRequest(arrived, release))
	handler.on("GET /api/v3/folders/folder-1/contents/search/", func(w http.ResponseWriter, r *http.Request) {
		t.Error("the lookup ran on the caller's cancelled context")
		writeJSON(t, w, map[string]any{"results": []map[string]any{}})
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	client := newRetryingTestClient(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-arrived
		cancel()
	}()

	_, err := client.RegisterFile(ctx, storedAt("Run_17.tar.gz", 4096, "uploads/mine.tar.gz"))
	if err == nil {
		t.Fatal("RegisterFile reported success although the caller cancelled it")
	}
	if !strings.Contains(err.Error(), "could not be confirmed") {
		t.Errorf("error %q does not report that the registration may have taken effect", err)
	}
	if !strings.Contains(err.Error(), "Run_17.tar.gz") {
		t.Errorf("error %q does not name the file whose record may exist", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %q no longer reports that the caller cancelled the call", err)
	}
	if got := handler.count("POST /api/v3/files/"); got != 1 {
		t.Errorf("the platform received %d registrations, want 1", got)
	}
}

// TestCreateJobReportsAPossibleJobWhenTheCallerCancels is D7 for job creation.
// A job the platform may have created and started charging for must not be
// reported as a plain cancellation: the caller has to look before creating it
// again.
func TestCreateJobReportsAPossibleJobWhenTheCallerCancels(t *testing.T) {
	handler := newCountingHandler()
	arrived := make(chan struct{})
	release := make(chan struct{})
	handler.on("POST /api/v3/jobs/", stallAfterRequest(arrived, release))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	client := newRetryingTestClient(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-arrived
		cancel()
	}()

	_, err := client.CreateJob(ctx, models.JobRequest{Name: "Run_17"})
	if err == nil {
		t.Fatal("CreateJob reported success although the caller cancelled it")
	}
	if !errors.Is(err, ErrJobMayExist) {
		t.Errorf("error %q does not report that the job may exist", err)
	}
	if !strings.Contains(err.Error(), "Run_17") {
		t.Errorf("error %q does not name the job the caller has to look for", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %q no longer reports that the caller cancelled the call", err)
	}
	if got := handler.count("POST /api/v3/jobs/"); got != 1 {
		t.Errorf("the platform received %d job creations, want 1", got)
	}
}

// TestCreateJobDoesNotClaimAJobItNeverSent is the boundary of the two tests
// above: a request that never left carries nothing, so telling the user to go
// looking for a job would be a false alarm. Covers both proofs — a context that
// was already dead when the call started, and a dial that never connected.
func TestCreateJobDoesNotClaimAJobItNeverSent(t *testing.T) {
	handler := newCountingHandler()
	handler.on("POST /api/v3/jobs/", func(w http.ResponseWriter, r *http.Request) {
		t.Error("a call on a dead context reached the platform")
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	t.Run("the caller's context was already cancelled", func(t *testing.T) {
		client := newRetryingTestClient(t, server.URL)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := client.CreateJob(ctx, models.JobRequest{Name: "Run_17"})
		if err == nil {
			t.Fatal("CreateJob reported success on a cancelled context")
		}
		if errors.Is(err, ErrJobMayExist) {
			t.Errorf("error %q claims a job may exist for a request that was never sent", err)
		}
	})

	t.Run("the dial never connected", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve a port: %v", err)
		}
		addr := listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatalf("close the listener: %v", err)
		}

		client := newRetryingTestClient(t, "http://"+addr)

		_, err = client.CreateJob(context.Background(), models.JobRequest{Name: "Run_17"})
		if err == nil {
			t.Fatal("CreateJob reported success although nothing was listening")
		}
		if errors.Is(err, ErrJobMayExist) {
			t.Errorf("error %q claims a job may exist for a dial that never connected", err)
		}
	})
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
