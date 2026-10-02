package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rescale/rescale-int/internal/constants"
	"github.com/rescale/rescale-int/internal/ratelimit"
)

// A job's file listing that reaches the page cap says how many files it had
// listed, and the warning that it is near the cap reaches the user: it went to
// the standard logger, which the CLI discards unless --verbose is set.
func TestListJobFilesCapCountsFilesAndWarnsTheUser(t *testing.T) {
	var mu sync.Mutex
	var notices []string
	ratelimit.SetGlobalNotifyFunc(func(_, message string) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, message)
	})
	defer ratelimit.SetGlobalNotifyFunc(nil)

	// One file a page and a next page always, each page a new link: a server
	// that ignores page_size, in front of a job too large for the cap.
	var pages atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"results":[{"id":"F%d","name":"out.dat"}],"next":"http://%s/api/v2/jobs/JOB1/files/?page=%d"}`, page, r.Host, max(page, 1)+1)
	}))
	defer server.Close()

	files, err := newTestClient(t, server.URL).ListJobFiles(context.Background(), "JOB1")
	if want := "job files listing incomplete after 1000 pages (1000 listed)"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("ListJobFiles returned %v, want %q", err, want)
	}
	if files != nil {
		t.Errorf("got %d files alongside the error; a partial listing must not be returned", len(files))
	}
	if n := pages.Load(); n != constants.MaxPaginationPages {
		t.Errorf("fetched %d pages, want the %d-page cap", n, constants.MaxPaginationPages)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "Warning: job files listing has read 900 of at most 1000 pages (900 listed so far)"
	if len(notices) != 1 || notices[0] != want {
		t.Errorf("notices = %q, want one: %q", notices, want)
	}
}

// The jobs and run-files listings took the platform's default page of ten, so
// more than ten thousand entries met the page cap after a thousand requests,
// and fewer cost a request for every ten. They ask for large pages now, 200
// jobs or 1000 run files at a time, and still return every entry in order.
func TestJobAndRunFileListingsAskForLargePages(t *testing.T) {
	const n = 2500
	want := make([]string, n)
	for i := range want {
		want[i] = fmt.Sprintf("E%04d", i)
	}
	for _, tc := range []struct {
		path string
		size int // the page size asked for
		list func(c *Client) ([]string, error)
	}{
		{"/api/v3/jobs/", 200, func(c *Client) (ids []string, err error) {
			jobs, err := c.ListJobs(context.Background())
			for _, j := range jobs {
				ids = append(ids, j.ID)
			}
			return ids, err
		}},
		{"/api/v2/jobs/JOB1/runs/1/files/", 1000, func(c *Client) (ids []string, err error) {
			files, err := c.GetRunFiles(context.Background(), "JOB1", "1")
			for _, f := range files {
				ids = append(ids, f.ID)
			}
			return ids, err
		}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var mu sync.Mutex
			var sizes []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					http.NotFound(w, r)
					return
				}
				q := r.URL.Query()
				mu.Lock()
				sizes = append(sizes, q.Get("page_size"))
				mu.Unlock()
				size, page := 10, 1
				if s, err := strconv.Atoi(q.Get("page_size")); err == nil && s > 0 {
					size = min(s, 1000)
				}
				if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
					page = p
				}
				start, end := min((page-1)*size, n), min(page*size, n)
				results := []map[string]string{}
				for _, id := range want[start:end] {
					results = append(results, map[string]string{"id": id})
				}
				body := map[string]any{"count": n, "results": results}
				if end < n {
					q.Set("page", strconv.Itoa(page+1))
					body["next"] = "http://" + r.Host + r.URL.Path + "?" + q.Encode()
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()

			got, err := tc.list(newTestClient(t, server.URL))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				i := 0
				for i < min(len(got), n) && got[i] == want[i] {
					i++
				}
				t.Errorf("listed %d entries, want all %d in order; they differ from entry %d on", len(got), n, i)
			}
			mu.Lock()
			defer mu.Unlock()
			pages := (n + tc.size - 1) / tc.size
			if !slices.Equal(sizes, slices.Repeat([]string{strconv.Itoa(tc.size)}, pages)) {
				t.Errorf("made %d requests with page sizes %q, want %d of %d", len(sizes), sizes, pages, tc.size)
			}
		})
	}
}

// The platform's page count can promise more than the list holds, and a
// listing read every empty page it promised, up to the page cap. Pages are
// numbered over an ordered list, so none can follow an empty one: the listing
// stops there.
func TestListingsStopAtTheFirstEmptyPage(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		page = max(page, 1)
		results := `[]`
		if page == 1 {
			results = `[{"id":"F1","name":"a.dat"},{"id":"F2","name":"b.dat"}]`
		}
		next := "null" // a count of 50,000 promises fifty pages of a thousand
		if page < 50 {
			next = fmt.Sprintf(`"http://%s/api/v2/jobs/JOB1/files/?page=%d&page_size=1000"`, r.Host, page+1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"count":50000,"next":%s,"results":%s}`, next, results)
	}))
	defer server.Close()

	files, err := newTestClient(t, server.URL).ListJobFiles(context.Background(), "JOB1")
	if err != nil || len(files) != 2 {
		t.Fatalf("ListJobFiles returned %d files and %v, want the two", len(files), err)
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("made %d requests, want 2: the second page was empty", n)
	}
}
