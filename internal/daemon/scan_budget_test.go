package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
)

// budgetPlatform serves the user's own completed jobs and those in the
// workspace's shared root, each done, and records which jobs' completion times
// and tags are read; onLookup runs as a completion time is looked up, and
// beforeTags before the nth tag read is answered.
type budgetPlatform struct {
	jobs, shared []string
	onLookup     func()
	beforeTags   func(n int)

	mu               sync.Mutex
	noTree           bool // the workspace folder tree is refused
	failLookups      bool // completion times cannot be read
	lookups, tagRead []string
}

func (p *budgetPlatform) serve(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC().Format(time.RFC3339)
	id := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v3/jobs/"), "/")[0]
	p.mu.Lock()
	noTree, failLookups, ids := p.noTree, p.failLookups, p.jobs
	if strings.HasPrefix(r.URL.Query().Get("q"), "folder:") {
		ids = p.shared
	}
	p.mu.Unlock()
	var body any
	switch {
	case r.URL.Path == "/api/v3/meta/folders/" && noTree:
		w.WriteHeader(http.StatusForbidden)
		return
	case r.URL.Path == "/api/v3/meta/folders/":
		body = map[string]any{"sharedWithWorkspace": map[string]any{"id": "root", "name": "Shared"}}
	case r.URL.Path == "/api/v3/jobs/":
		var jobs []models.JobResponse
		for _, id := range ids {
			jobs = append(jobs, models.JobResponse{ID: id, Name: id, CreatedAt: now, JobStatus: models.JobStatusContent{Status: "Completed"}, Folder: &models.JobFolder{ID: "root"}})
		}
		body = map[string]any{"results": jobs}
	case strings.HasSuffix(r.URL.Path, "/statuses/"):
		p.mu.Lock()
		p.lookups = append(p.lookups, id)
		p.mu.Unlock()
		if p.onLookup != nil {
			p.onLookup()
		}
		if failLookups {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body = map[string]any{"results": []models.JobStatusEntry{{Status: "Completed", StatusDate: now}}}
	case strings.HasSuffix(r.URL.Path, "/tags/"):
		p.mu.Lock()
		p.tagRead = append(p.tagRead, id)
		n := len(p.tagRead)
		p.mu.Unlock()
		if p.beforeTags != nil {
			p.beforeTags(n)
		}
		body = []api.JobTag{{Name: config.DownloadedTag}}
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (p *budgetPlatform) read() (lookups, tagReads []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.lookups), slices.Clone(p.tagRead)
}

// set changes, under the lock, what the platform serves.
func (p *budgetPlatform) set(change func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	change()
}

func (p *budgetPlatform) url(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A scan whose context ends, as the daemon stops or the scan budget runs out,
// ends then, instead of failing and pausing on every job still to look up, and
// counts the jobs it left unchecked.
func TestFindCompletedJobs_EndsWhenItsContextDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &budgetPlatform{onLookup: cancel} // the daemon stops while the first job is looked up
	for i := range 40 {
		p.jobs = append(p.jobs, fmt.Sprintf("job%02d", i))
	}
	client := api.NewClientForTest(&config.Config{APIKey: "test-key", APIBaseURL: p.url(t), ProxyMode: "no-proxy"})
	m := NewMonitorWithEligibility(client, nil, nil, &EligibilityConfig{LookbackDays: 7}, logging.NewLoggerWithWriter(io.Discard))

	start := time.Now()
	result, err := m.FindCompletedJobs(ctx, nil)
	if took := time.Since(start); err != nil || took > 5*time.Second {
		t.Fatalf("FindCompletedJobs returned %v after %s; want it to end at once", err, took.Round(time.Millisecond))
	}
	if n := result.Summary.Unchecked; n < 39 {
		t.Errorf("%d jobs left unchecked, want the 39 or 40 not looked up", n)
	}
}

// A poll that runs out of budget leaves the rest to the next poll, which looks
// no completion time up again and checks first the jobs left unchecked, so
// every job gets its turn however many there are.
func TestPoll_TheNextPollCarriesOnWhereTheBudgetStoppedOne(t *testing.T) {
	orig := scanBudget
	scanBudget = 2 * time.Second
	t.Cleanup(func() { scanBudget = orig })
	p := &budgetPlatform{jobs: []string{"a", "b", "c", "d"}}
	p.beforeTags = func(n int) {
		if n == 2 {
			time.Sleep(scanBudget + time.Second) // the first poll's budget runs out on job b
		}
	}
	elig := &EligibilityConfig{LookbackDays: 7}
	d := newDownloadTestDaemon(t, p.url(t), t.TempDir(), elig)
	d.monitor.SetEligibility(elig)

	d.poll(context.Background())
	if _, _, unchecked := d.state.GetLeftOut(); unchecked != 2 || d.state.GetLastPoll().IsZero() {
		t.Errorf("the poll the budget stopped left %d jobs unchecked, last poll %v; want 2, and the time", unchecked, d.state.GetLastPoll())
	}
	if msg, _ := d.LastScanError(); msg != "" {
		t.Errorf("a poll the budget stopped recorded the scan error %q; it is partial, not failed", msg)
	}
	d.poll(context.Background())
	lookups, tagReads := p.read()
	if want := []string{"a", "b", "c", "d", "a", "b"}; !slices.Equal(tagReads, want) {
		t.Errorf("jobs checked in the order %v, want %v: the first poll stopped after b", tagReads, want)
	}
	if want := []string{"a", "b", "c", "d"}; !slices.Equal(lookups, want) {
		t.Errorf("completion times looked up for %v, want each job once: %v", lookups, want)
	}
}

// A daemon that stops while its scan looks jobs up ends the poll as
// interrupted, as a stop anywhere else in a poll does, not as a failed scan.
func TestPoll_StoppingWhileTheScanLooksJobsUpIsNoScanError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &budgetPlatform{jobs: []string{"a", "b"}, onLookup: cancel}
	elig := &EligibilityConfig{LookbackDays: 7}
	d := newDownloadTestDaemon(t, p.url(t), t.TempDir(), elig)
	d.monitor.SetEligibility(elig)

	d.poll(ctx)
	if msg, _ := d.LastScanError(); msg != "" {
		t.Errorf("stopping recorded the scan error %q", msg)
	}
}

// A poll whose budget runs out while its scan looks jobs up is a partial scan,
// as one that runs out later is: its time is kept as the last poll's, no scan
// error is recorded, and 'daemon status' counts the jobs it left unchecked.
func TestPoll_OutOfBudgetWhileLookingJobsUpIsAPartialScan(t *testing.T) {
	orig := scanBudget
	scanBudget = time.Second
	t.Cleanup(func() { scanBudget = orig })
	p := &budgetPlatform{jobs: []string{"a", "b", "c"}}
	p.onLookup = func() { time.Sleep(scanBudget + 500*time.Millisecond) }
	elig := &EligibilityConfig{LookbackDays: 7}
	d := newDownloadTestDaemon(t, p.url(t), t.TempDir(), elig)
	d.monitor.SetEligibility(elig)

	d.poll(context.Background())
	if msg, _ := d.LastScanError(); msg != "" || d.state.GetLastPoll().IsZero() {
		t.Errorf("scan error %q, last poll %v; want none, and the poll's time", msg, d.state.GetLastPoll())
	}
	if users := NewIPCHandler(d, nil).GetUserList(); len(users) != 1 || users[0].JobsUnchecked != 3 {
		t.Errorf("daemon status gets %+v, want 3 jobs left unchecked", users)
	}
}

// A poll whose workspace folders could not be listed forgets nothing about the
// jobs in them, neither their completion times nor when each was last checked:
// they went unlisted, not away. A job no longer listed at all is forgotten.
func TestPoll_AFailedWorkspaceListingForgetsNothing(t *testing.T) {
	p := &budgetPlatform{jobs: []string{"own"}, shared: []string{"shared"}}
	elig := &EligibilityConfig{LookbackDays: 7, IncludeWorkspaceFolders: true}
	d := newDownloadTestDaemon(t, p.url(t), t.TempDir(), elig)
	d.monitor.SetEligibility(elig)
	known := func() (looked, checked bool) {
		_, looked = d.monitor.completedAt["shared"]
		_, checked = d.lastChecked["shared"]
		return looked, checked
	}
	ctx := context.Background()

	d.poll(ctx)
	p.set(func() { p.noTree = true })
	d.poll(ctx)
	if looked, checked := known(); !looked || !checked {
		t.Errorf("after a failed workspace listing: completion time kept %v, last check kept %v; want both", looked, checked)
	}
	p.set(func() { p.noTree, p.shared = false, nil })
	d.poll(ctx)
	if looked, checked := known(); looked || checked {
		t.Errorf("a job no longer listed: completion time kept %v, last check kept %v; want neither", looked, checked)
	}
	if lookups, _ := p.read(); !slices.Equal(lookups, []string{"shared", "own"}) {
		t.Errorf("completion times looked up for %v, want each job once", lookups)
	}
}

// A completion time that could not be looked up is not asked for again until
// lookupRetryAfter has passed, so jobs whose lookups keep failing cannot spend
// the scan budget at every poll. Each is still scanned, its completion time
// unknown.
func TestFindCompletedJobs_ALookupThatFailedIsNotRepeatedAtOnce(t *testing.T) {
	p := &budgetPlatform{jobs: []string{"a"}, failLookups: true}
	client := api.NewClientForTest(&config.Config{APIKey: "test-key", APIBaseURL: p.url(t), ProxyMode: "no-proxy"})
	m := NewMonitorWithEligibility(client, nil, nil, &EligibilityConfig{LookbackDays: 7}, logging.NewLoggerWithWriter(io.Discard))
	scan := func() {
		t.Helper()
		result, err := m.FindCompletedJobs(context.Background(), nil)
		if err != nil || len(result.Candidates) != 1 || result.Summary.SkipBuckets[ReasonCompletionTimeAPIError] != 1 {
			t.Fatalf("FindCompletedJobs: %v; want the job scanned, its completion time unknown", err)
		}
	}

	scan()
	scan()
	if lookups, _ := p.read(); len(lookups) != 2 {
		t.Errorf("%d lookups in two scans, want the first scan's try and retry only", len(lookups))
	}
	orig := lookupRetryAfter
	lookupRetryAfter = 0
	t.Cleanup(func() { lookupRetryAfter = orig })
	scan()
	if lookups, _ := p.read(); len(lookups) != 4 {
		t.Errorf("%d lookups, want the lookup tried again once lookupRetryAfter had passed", len(lookups))
	}
}
