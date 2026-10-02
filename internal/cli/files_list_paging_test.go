package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/config"
)

// pagedFake serves one listing endpoint the way the platform pages it: by page
// number, ten entries a page unless page_size asks for more (up to max, 1000
// when unset), a limit parameter ignored, and next naming the following page.
// A search keeps the names that contain every comma- or space-separated term.
// With repeat set, every page names itself as next.
type pagedFake struct {
	path   string
	names  []string // newest first; entry i has ID F%04d
	max    int
	repeat bool

	mu       sync.Mutex
	requests []url.Values
}

func (f *pagedFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != f.path {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	f.mu.Lock()
	f.requests = append(f.requests, q)
	f.mu.Unlock()

	terms := strings.FieldsFunc(strings.ToLower(q.Get("search")), func(c rune) bool { return c == ',' || unicode.IsSpace(c) })
	var hits []map[string]any
	for i, name := range f.names {
		matched := true
		for _, term := range terms {
			matched = matched && strings.Contains(strings.ToLower(name), term)
		}
		if matched {
			hits = append(hits, map[string]any{"id": fmt.Sprintf("F%04d", i), "name": name, "decryptedSize": 1 << 20, "dateUploaded": "2026-01-02T03:04:05Z"})
		}
	}
	size, page, max := 10, 1, f.max
	if max == 0 {
		max = 1000
	}
	if n, err := strconv.Atoi(q.Get("page_size")); err == nil && n > 0 {
		size = min(n, max)
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 0 {
		page = n
	}
	start := min((page-1)*size, len(hits))
	end := min(start+size, len(hits))
	body := map[string]any{"count": len(hits), "results": hits[start:end]}
	if f.repeat {
		body["next"] = "http://" + r.Host + r.URL.RequestURI()
	} else if end < len(hits) {
		q.Set("page", strconv.Itoa(page+1))
		body["next"] = "http://" + r.Host + r.URL.Path + "?" + q.Encode()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// asked lists one parameter of every request the fake answered.
func (f *pagedFake) asked(param string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var values []string
	for _, q := range f.requests {
		values = append(values, q.Get(param))
	}
	return values
}

// listWith runs cmd with args against fake, each file download replaced by
// fetch, and returns what it printed.
func listWith(t *testing.T, fake *pagedFake, fetch func(context.Context, download.DownloadParams) error, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	orig := getAPIClientFn
	t.Cleanup(func() { getAPIClientFn = orig })
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	return runWithCancel(t, cmd, fetch, args...)
}

var listedFileID = regexp.MustCompile(`(?m)^(F\d{4}) `)

// files list sent limit, which the platform ignores, and read one page: it
// listed the newest ten files whatever --limit said, and --search looked only
// at those ten. It now pages through the library, the search run by the
// platform, until it has --limit files, and counts what it filtered over the
// whole library. The platform's search also splits a term at its spaces, so
// the names it returns are checked again against the terms as given.
func TestFilesListPagesThroughTheLibrary(t *testing.T) {
	names := make([]string, 30)
	for i := range names {
		names[i] = fmt.Sprintf("run%02d.log", i)
	}
	for _, i := range []int{3, 11, 17, 22, 26, 29} {
		names[i] = fmt.Sprintf("run%02d.dat", i)
	}
	names[20], names[24], names[28] = "needle-20.bin", "old-NEEDLE-24.bin", "needle-28.bin"
	names[5], names[6], names[7], names[8] = "probe 2.bin", "probe-2x.bin", "2-probe.bin", `say "probe 2".txt`
	first := func(n int) []string {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("F%04d", i)
		}
		return ids
	}
	const note = "(Stopped at --limit %d. Use --limit to change, 0 for all)"

	for _, tc := range []struct {
		name      string
		max       int // the largest page the fake serves; 0 for the platform's 1000
		args      []string
		want      []string
		sizes     []string // page_size of each request
		has, lack []string
	}{
		{"--limit 25", 0, []string{"-n", "25"}, first(25), []string{"25"},
			[]string{"Found 25 file(s):", fmt.Sprintf(note, 25)}, []string{"Filtered:"}},
		{"--limit 25, pages of ten", 10, []string{"-n", "25"}, first(25), []string{"25", "25", "25"},
			[]string{"Found 25 file(s):"}, nil},
		{"--limit 0 lists every file", 0, []string{"-n", "0"}, first(30), []string{"1000"},
			[]string{"Found 30 file(s):"}, []string{"Stopped at --limit"}},
		{"--limit above the largest page", 0, []string{"-n", "5000"}, first(30), []string{"1000"},
			[]string{"Found 30 file(s):"}, []string{"Stopped at --limit"}},
		{"exactly --limit files claim no more", 0, []string{"-n", "30"}, first(30), []string{"30"},
			[]string{"Found 30 file(s):", fmt.Sprintf(note, 30)}, []string{"Showing the first"}},
		{"the search covers the whole library", 0, []string{"--search", "needle", "-n", "50"},
			[]string{"F0020", "F0024", "F0028"}, []string{"1000"},
			[]string{"Found 3 file(s):"}, []string{"Filtered:", "No files found", "Stopped at --limit"}},
		{"a term with a space keeps its space", 0, []string{"--search", "probe 2", "-n", "0"},
			[]string{"F0005", "F0008"}, []string{"1000"},
			[]string{"Filtered: 2 of 4 files match filters", "Found 2 file(s):"}, nil},
		{"every term must match", 0, []string{"--search", "probe,2x", "-n", "0"},
			[]string{"F0006"}, []string{"1000"}, []string{"Found 1 file(s):"}, []string{"Filtered:"}},
		{"quotes are part of a term", 0, []string{"--search", `"probe 2"`, "-n", "0"},
			[]string{"F0008"}, []string{"1000"}, []string{"Found 1 file(s):"}, []string{"Filtered:"}},
		{"a filter counts the whole library", 10, []string{"--include", "*.dat", "-n", "0"},
			[]string{"F0003", "F0011", "F0017", "F0022", "F0026", "F0029"}, []string{"1000", "1000", "1000"},
			[]string{"Filtered: 6 of 30 files match filters", "Found 6 file(s):"}, nil},
		{"--limit 5", 0, []string{"-n", "5"}, first(5), []string{"5"},
			[]string{"Found 5 file(s):", fmt.Sprintf(note, 5)}, nil},
		{"a filter stopped by --limit claims no total", 0, []string{"--include", "*.dat", "-n", "2"},
			[]string{"F0003", "F0011"}, []string{"1000"},
			[]string{"Found 2 file(s):", fmt.Sprintf(note, 2)}, []string{"Filtered:"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &pagedFake{path: "/api/v3/files/", names: names, max: tc.max}
			printed, err := listWith(t, fake, nil, newFilesCmd(), append([]string{"list"}, tc.args...)...)
			if err != nil {
				t.Fatalf("files list %v: %v\n%s", tc.args, err, printed)
			}
			var got []string
			for _, m := range listedFileID.FindAllStringSubmatch(printed, -1) {
				got = append(got, m[1])
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("files list %v listed %v, want %v; printed:\n%s", tc.args, got, tc.want, printed)
			}
			for _, s := range tc.has {
				if !strings.Contains(printed, s) {
					t.Errorf("files list %v did not print %q; printed:\n%s", tc.args, s, printed)
				}
			}
			for _, s := range tc.lack {
				if strings.Contains(printed, s) {
					t.Errorf("files list %v printed %q; printed:\n%s", tc.args, s, printed)
				}
			}
			if sizes := fake.asked("page_size"); strings.Join(sizes, " ") != strings.Join(tc.sizes, " ") {
				t.Errorf("files list %v asked for pages of %q, want %q", tc.args, sizes, tc.sizes)
			}
			if i := slices.Index(tc.args, "--search"); i >= 0 {
				if searched := fake.asked("search"); searched[0] != tc.args[i+1] {
					t.Errorf("the search was not sent to the platform as given: %q", searched)
				}
			}
		})
	}
}
