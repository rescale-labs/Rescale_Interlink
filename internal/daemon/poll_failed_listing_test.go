package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A poll whose job listing fails goes on, as the daemon does between polls, but
// records the failure where 'daemon status', the app and 'daemon run --once'
// read it; the next poll that lists the jobs clears it.
func TestRunOnce_RecordsAFailedJobListing(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path != "/api/v3/jobs/": // the credentials a poll warms first, best-effort
			w.WriteHeader(http.StatusNotFound)
		case down.Load():
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "FAKE outage")
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"results": []}`)
		}
	}))
	t.Cleanup(srv.Close)
	d := newDownloadTestDaemon(t, srv.URL, t.TempDir(), EligibilityConfig{})

	if err := d.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if msg, _ := d.LastScanError(); !strings.Contains(msg, "failed to list jobs: list jobs failed: status 503") {
		t.Errorf("after a failed job listing the recorded scan error is %q, want that failure", msg)
	}
	down.Store(false)
	if err := d.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if msg, _ := d.LastScanError(); msg != "" {
		t.Errorf("after a listing that worked the recorded scan error is %q, want none", msg)
	}
}
