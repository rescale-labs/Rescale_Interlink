package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
)

// doneJobs are jobs of the user's own that a client has downloaded: each
// carries the done tag.
func doneJobs(ids ...string) []*fakeJob {
	var jobs []*fakeJob
	for _, id := range ids {
		jobs = append(jobs, &fakeJob{id: id, tags: []string{config.DownloadedTag}})
	}
	return jobs
}

// A scan whose context ends, as the daemon stops or the scan budget runs out,
// ends then, instead of failing and pausing on every job still to look up, and
// counts the jobs it left unchecked.
func TestFindCompletedJobs_EndsWhenItsContextDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newPlatform(t)
	p.onLookup = func(context.Context) { cancel() } // the daemon stops while the first job is looked up
	for i := range 40 {
		p.jobs = append(p.jobs, &fakeJob{id: fmt.Sprintf("job%02d", i)})
	}
	m := p.monitor(nil, EligibilityConfig{LookbackDays: 7})

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
	p := newPlatform(t, doneJobs("a", "b", "c", "d")...)
	p.beforeRead = func(_ string, n int) {
		if n == 2 {
			time.Sleep(scanBudget + time.Second) // the first poll's budget runs out on job b
		}
	}
	d := p.daemon(t.TempDir(), EligibilityConfig{LookbackDays: 7})

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
	p := newPlatform(t, doneJobs("a", "b")...)
	p.onLookup = func(context.Context) { cancel() }
	d := p.daemon(t.TempDir(), EligibilityConfig{LookbackDays: 7})

	d.poll(ctx)
	if msg, _ := d.LastScanError(); msg != "" {
		t.Errorf("stopping recorded the scan error %q", msg)
	}
}

// controlScans gives each poll a scan with no deadline of its own, which the
// returned endScan ends, as the scan budget would, for the poll under way. A
// lookup cut short this way is cut short however long the poll took to reach it.
func controlScans(t *testing.T) (endScan func()) {
	t.Helper()
	var mu sync.Mutex
	var end context.CancelFunc
	orig := scanContext
	scanContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
		scan, cancel := context.WithCancel(ctx)
		mu.Lock()
		end = cancel
		mu.Unlock()
		return scan, cancel
	}
	t.Cleanup(func() { scanContext = orig })
	return func() {
		mu.Lock()
		defer mu.Unlock()
		end()
	}
}

// cutLookups returns a lookup hook that, while cut says so, ends the poll's
// scan and holds the lookup open until the client gives up on it.
func cutLookups(endScan func(), cut func() bool) func(context.Context) {
	return func(request context.Context) {
		if cut() {
			endScan()
			<-request.Done()
		}
	}
}

// A poll whose budget runs out while its scan looks jobs up is a partial scan,
// as one that runs out later is: its time is kept as the last poll's, no scan
// error is recorded, and 'daemon status' counts the jobs it left unchecked.
// The job whose lookup the budget cut short waits for a later lookup, so the
// next poll gets past it, instead of stopping at it poll after poll, and counts
// it as waiting, not as a failed lookup.
func TestPoll_OutOfBudgetWhileLookingJobsUpIsAPartialScan(t *testing.T) {
	p := newPlatform(t, doneJobs("a", "b", "c")...)
	p.onLookup = cutLookups(controlScans(t), func() bool { return true }) // every lookup runs out of time
	d, logged := p.client("", EligibilityConfig{LookbackDays: 7})

	d.poll(context.Background())
	if msg, _ := d.LastScanError(); msg != "" || d.state.GetLastPoll().IsZero() {
		t.Errorf("scan error %q, last poll %v; want none, and the poll's time", msg, d.state.GetLastPoll())
	}
	if users := NewIPCHandler(d, nil).GetUserList(); len(users) != 1 || users[0].JobsUnchecked != 3 {
		t.Errorf("daemon status gets %+v, want 3 jobs left unchecked", users)
	}
	if c := d.monitor.completedAt["a"]; !c.cut {
		t.Errorf("the first poll recorded a's lookup as %+v, want it cut short", c)
	}
	d.poll(context.Background())
	if lookups, _ := p.read(); !slices.Equal(lookups, []string{"a", "b"}) {
		t.Errorf("completion times looked up for %v over two polls, want a then b: the second poll must get past a", lookups)
	}
	if s := pollSummary(logged()); strings.Contains(s, string(ReasonCompletionTimeAPIError)) || !strings.Contains(s, string(ReasonCompletionTimeDeferred)+"=1") {
		t.Errorf("the second poll's summary does not count a as waiting for its lookup: %s", s)
	}
}

// A job whose completion time lookup ran out of scan time waits for a later
// lookup, and is not let past the lookback window unlooked: the next poll does
// not look it up again, counts it as waiting, and does not download it, though
// it completed before the window. Once lookupRetryAfter has passed, it is
// looked up again, and left out.
func TestPoll_AJobWhoseLookupRanOutOfTimeIsNotLetPastTheLookback(t *testing.T) {
	shortenClaimSettle(t)
	old := time.Now().Add(-10 * 24 * time.Hour) // made and completed before the 7-day window
	p := newPlatform(t, &fakeJob{id: "old1", created: old, completed: old})
	var first atomic.Bool
	first.Store(true)
	p.onLookup = cutLookups(controlScans(t), func() bool { return first.Swap(false) }) // the first lookup runs out of time
	d, logged := p.client("", EligibilityConfig{LookbackDays: 7})
	ctx := context.Background()

	d.poll(ctx)
	if c := d.monitor.completedAt["old1"]; !c.cut {
		t.Fatalf("the first poll recorded old1's lookup as %+v, want it cut short", c)
	}
	d.poll(ctx)
	if lookups, _ := p.read(); len(lookups) != 1 || p.listed() != 0 {
		t.Errorf("the second poll looked old1 up again (lookups %v) or downloaded it (%d file listings); it completed before the lookback window", lookups, p.listed())
	}
	if s := pollSummary(logged()); strings.Contains(s, string(ReasonCompletionTimeAPIError)) || !strings.Contains(s, string(ReasonCompletionTimeDeferred)+"=1") {
		t.Errorf("the second poll's summary does not count old1 as waiting for its lookup: %s", s)
	}
	origRetry := lookupRetryAfter
	lookupRetryAfter = 0
	t.Cleanup(func() { lookupRetryAfter = origRetry })
	d.poll(ctx)
	if lookups, _ := p.read(); !slices.Equal(lookups, []string{"old1", "old1"}) || p.listed() != 0 {
		t.Errorf("lookups %v, %d file listings; want it looked up again, and left out", lookups, p.listed())
	}
}

// A poll whose workspace folders could not be listed forgets nothing about the
// jobs in them, neither their completion times nor when each was last checked:
// they went unlisted, not away. A job no longer listed at all is forgotten.
func TestPoll_AFailedWorkspaceListingForgetsNothing(t *testing.T) {
	tree := map[string]any{"sharedWithWorkspace": map[string]any{"id": "root", "name": "Shared"}}
	shared := &fakeJob{id: "shared", folder: "root", tags: []string{config.DownloadedTag}}
	p := newPlatform(t, append(doneJobs("own"), shared)...)
	p.tree = tree
	d := p.daemon(t.TempDir(), EligibilityConfig{LookbackDays: 7, IncludeWorkspaceFolders: true})
	known := func() (looked, checked bool) {
		_, looked = d.monitor.completedAt["shared"]
		_, checked = d.lastChecked["shared"]
		return looked, checked
	}
	ctx := context.Background()

	d.poll(ctx)
	p.set(func() { p.tree = nil })
	d.poll(ctx)
	if looked, checked := known(); !looked || !checked {
		t.Errorf("after a failed workspace listing: completion time kept %v, last check kept %v; want both", looked, checked)
	}
	p.set(func() { p.tree, p.jobs = tree, p.jobs[:1] })
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
	p := newPlatform(t, &fakeJob{id: "a"})
	p.failLookups = true
	m := p.monitor(nil, EligibilityConfig{LookbackDays: 7})
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
