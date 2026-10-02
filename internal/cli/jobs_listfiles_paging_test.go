package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/cloud/download"
)

// jobFileNames names n output files.
func jobFileNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("out_%05d.dat", i)
	}
	return names
}

// jobs listfiles took the endpoint's default page of ten files, so a job with
// more than ten thousand of them stopped at the 1000-page cap after as many
// requests. It asks for the largest page the platform serves.
func TestJobsListFilesAsksForTheLargestPage(t *testing.T) {
	fake := &pagedFake{path: "/api/v2/jobs/JOB1/files/", names: jobFileNames(2500)}
	printed, err := listWith(t, fake, nil, newJobsCmd(), "listfiles", "-j", "JOB1")
	if err != nil || !strings.Contains(printed, "Total: 2500 file(s)") {
		t.Fatalf("jobs listfiles returned %v; printed:\n%.300s", err, printed)
	}
	if sizes := fake.asked("page_size"); strings.Join(sizes, " ") != "1000 1000 1000" {
		t.Errorf("jobs listfiles made %d requests with page sizes %q, want three of 1000", len(sizes), sizes)
	}
}

// jobs download fetches every file of a listing that spans pages, the last
// page's included, each once. The platform here serves ten a page whatever is
// asked, so the three pages each ask for the largest.
func TestJobsDownloadGetsEveryFileOnEveryPage(t *testing.T) {
	fake := &pagedFake{path: "/api/v2/jobs/JOB1/files/", names: jobFileNames(25), max: 10}
	var mu sync.Mutex
	fetched := map[string]int{}
	fetch := func(_ context.Context, p download.DownloadParams) error {
		mu.Lock()
		defer mu.Unlock()
		fetched[p.FileInfo.ID]++
		return nil
	}
	printed, err := listWith(t, fake, fetch, newJobsCmd(), "download", "-j", "JOB1", "--outdir", t.TempDir())
	if err != nil {
		t.Fatalf("jobs download returned %v; printed:\n%s", err, printed)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := range 25 {
		if id := fmt.Sprintf("F%04d", i); fetched[id] != 1 {
			t.Errorf("file %s was downloaded %d times, want once", id, fetched[id])
		}
	}
	if len(fetched) != 25 {
		t.Errorf("downloaded %d distinct files, want 25: %v", len(fetched), fetched)
	}
	if sizes := fake.asked("page_size"); strings.Join(sizes, " ") != "1000 1000 1000" {
		t.Errorf("the listing made %d requests with page sizes %q, want three of 1000", len(sizes), sizes)
	}
}

// A next link already followed can only send the listing round in a loop. Both
// commands that list a job's files stop at the repeat and say how many files
// they had listed; they used to make a thousand requests first and then report
// a page cap.
func TestJobFileListingsStopWhenAPageLinkRepeats(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"jobs listfiles", []string{"listfiles", "-j", "JOB1"}},
		{"jobs download", []string{"download", "-j", "JOB1", "--outdir", t.TempDir()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &pagedFake{path: "/api/v2/jobs/JOB1/files/", names: jobFileNames(25), repeat: true}
			printed, err := listWith(t, fake, nil, newJobsCmd(), tc.args...)
			want := "job files listing stopped: the server repeated a page link (25 listed)"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s returned %v, want %q; printed:\n%s", tc.name, err, want, printed)
			}
			if n := len(fake.asked("page")); n != 1 {
				t.Errorf("%s made %d listing requests, want 1", tc.name, n)
			}
		})
	}
}
