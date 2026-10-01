package daemon

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// shortenClaimSettle lets a claim settle in a fraction of a second: the fake
// API answers at once.
func shortenClaimSettle(t *testing.T) {
	t.Helper()
	orig := claimSettle
	claimSettle = 200 * time.Millisecond
	t.Cleanup(func() { claimSettle = orig })
}

// claimJob is the one job the claim tests share: completed, its Auto Download
// field Enabled, with one file.
const claimJob = "shared1"

// newClaimPlatform serves the claim tests' job, with these tags. Its file is
// on every client's disk when present, and otherwise has a name every client
// refuses. A zero completion time is one that cannot be read.
func newClaimPlatform(t *testing.T, completed time.Time, present bool, tags ...string) *platform {
	file := models.JobFile{ID: "f1", Name: "../escape.txt", DecryptedSize: 1}
	if present {
		file = models.JobFile{ID: "f1", Name: "present.txt", DecryptedSize: 3, FileChecksums: sha512Of(t, []byte("abc"))}
	}
	p := newPlatform(t, &fakeJob{id: claimJob, name: "shared", completed: completed, files: []models.JobFile{file}, tags: tags})
	p.failLookups = completed.IsZero()
	return p
}

// claimant returns client name of the claim tests' platform, with eligibility
// on, and the job's file on its disk when the file is one it can have.
func claimant(p *platform, name string) (*Daemon, func() []string) {
	d, logged := p.client(name, EligibilityConfig{LookbackDays: 7})
	if f := p.jobs[0].files[0]; f.Name == "present.txt" {
		writeFile(p.t, filepath.Join(ComputeOutputDir(d.cfg.DownloadDir, claimJob, "shared", false), f.Name), "abc")
	}
	return d, logged
}

// startedTags returns the started tags among tags.
func startedTags(tags []string) []string {
	return slices.DeleteFunc(slices.Clone(tags), func(s string) bool { return !strings.HasPrefix(s, config.StartedTag) })
}

// savedTags returns the started tags a state holds.
func savedTags(s *State) []string {
	var tags []string
	for id := range s.Started {
		tag, _ := s.StartedTag(id)
		tags = append(tags, tag)
	}
	return tags
}

// otherClaim is a started tag another client put on a job, at.
func otherClaim(at time.Time) string {
	return fmt.Sprintf("%s:0a1b2c3d:%d", config.StartedTag, at.UnixMilli())
}

// pollSummary returns the poll's summary line from what a client logged.
func pollSummary(lines []string) string {
	for _, line := range slices.Backward(lines) {
		if strings.Contains(line, "Poll complete") {
			return line
		}
	}
	return ""
}

// Two clients find the same job free, as their scans reach it together, and
// both put their started tag on it; exactly one downloads it. The other leaves
// it, says why, counts it, and takes its tag off again, so once the job is done
// no started tag is left. That holds too when the earlier claim is the later to
// show: b claims first, but its tag takes a while to go on, and a's goes on at
// once. Each download takes a while, so neither finds the job done instead.
func TestPoll_TwoClientsDownloadAJobOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		aLater time.Duration // how much later than b a claims
		bSlow  time.Duration // how long b's tag takes to go on
	}{
		{"together", 0, 0},
		{"the earlier claim shows later", 20 * time.Millisecond, 120 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortenClaimSettle(t)
			p := newClaimPlatform(t, time.Now().Add(-time.Hour), true)
			p.claimDelay = map[string]time.Duration{"b": tc.bSlow}
			p.listDelay = 3 * claimSettle / 2
			// Neither client's first read of the job's tags is answered until
			// both have asked, so both find the job free.
			both := make(chan struct{})
			var arrived atomic.Int32
			p.beforeRead = func(client string, n int) {
				if n != 1 {
					return
				}
				if arrived.Add(1) == 2 {
					close(both)
				}
				select {
				case <-both:
				case <-time.After(10 * time.Second):
					t.Error("the other client never read the job's tags")
				}
				if client == "a" {
					time.Sleep(tc.aLater)
				}
			}
			a, logA := claimant(p, "a")
			b, logB := claimant(p, "b")

			var wg sync.WaitGroup
			for _, d := range []*Daemon{a, b} {
				wg.Go(func() { d.poll(context.Background()) })
			}
			wg.Wait()

			if n := p.listed(); n != 1 {
				t.Errorf("the job's files were listed %d times; want once, by one client", n)
			}
			winner, loser, loserLog := a, b, logB
			if b.state.GetDownloadedCount() == 1 {
				winner, loser, loserLog = b, a, logA
			}
			if winner.state.GetDownloadedCount() != 1 || loser.state.GetDownloadedCount() != 0 {
				t.Errorf("downloaded: a %d, b %d; want one client each", a.state.GetDownloadedCount(), b.state.GetDownloadedCount())
			}
			if tags := p.tags(claimJob); !slices.Equal(tags, []string{config.DownloadedTag}) {
				t.Errorf("the job's tags end as %v, want only %s", tags, config.DownloadedTag)
			}
			if !slices.ContainsFunc(loserLog(), func(l string) bool {
				return strings.Contains(l, "SKIP: shared") && strings.Contains(l, "another client")
			}) {
				t.Errorf("the client that left the job did not say why:\n%s", strings.Join(loserLog(), ""))
			}
			if n := loser.state.GetHeldElsewhere(); n != 1 {
				t.Errorf("the client that left the job counts %d jobs another client is downloading, want 1", n)
			}
		})
	}
}

// Another client's started tag holds the job: the poll skips it, says so once
// a poll and counts it. Past its lease the tag holds nothing, nor does one
// stamped further ahead than any clock in step would be: the job is taken
// over, and the client taking it takes that tag off, saying so. The bare tag
// earlier versions put on names neither client nor time: it counts from the
// job's completion, and holds nothing once that is a lease ago, or unknown; an
// unknown one's age is unknown too, so it stays.
func TestPoll_AnotherClientsStartedTag(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		tag       string
		completed time.Time
		held      bool
	}{
		{"an hour old", otherClaim(now.Add(-time.Hour)), now.Add(-time.Hour), true},
		{"a minute ahead", otherClaim(now.Add(time.Minute)), now.Add(-time.Hour), true},
		{"past its lease", otherClaim(now.Add(-claimLease - time.Hour)), now.Add(-48 * time.Hour), false},
		{"an hour ahead", otherClaim(now.Add(time.Hour)), now.Add(-time.Hour), false},
		{"bare, on a job completed an hour ago", config.StartedTag, now.Add(-time.Hour), true},
		{"bare, on a job completed two days ago", config.StartedTag, now.Add(-48 * time.Hour), false},
		{"bare, on a job whose completion is unknown", config.StartedTag, time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortenClaimSettle(t)
			p := newClaimPlatform(t, tc.completed, true, tc.tag)
			d, logged := claimant(p, "a")
			ctx := context.Background()

			d.poll(ctx)
			if !tc.held {
				if p.listed() != 1 || d.state.GetDownloadedCount() != 1 {
					t.Fatalf("the job was not taken over: listed %d times, %d downloaded", p.listed(), d.state.GetDownloadedCount())
				}
				want, removals := []string{config.DownloadedTag}, 1
				if tc.completed.IsZero() {
					want, removals = []string{tc.tag, config.DownloadedTag}, 0
				}
				if tags := p.tags(claimJob); !slices.Equal(tags, want) {
					t.Errorf("the job's tags end as %v, want %v", tags, want)
				}
				if n := len(slices.DeleteFunc(logged(), func(l string) bool { return !strings.Contains(l, "hold nothing") })); n != removals {
					t.Errorf("%d lines say started tags that hold nothing were taken off, want %d", n, removals)
				}
				return
			}
			d.poll(ctx)
			if p.listed() != 0 || !slices.Equal(p.tags(claimJob), []string{tc.tag}) {
				t.Errorf("a held job was listed %d times, and its tags are %v", p.listed(), p.tags(claimJob))
			}
			skips := slices.DeleteFunc(logged(), func(l string) bool { return !strings.Contains(l, "SKIP: shared") })
			if len(skips) != 2 || !strings.Contains(skips[0], "another client is downloading it") || !strings.Contains(skips[0], tc.tag) {
				t.Errorf("two polls logged %d skips, want one each naming the tag:\n%s", len(skips), strings.Join(skips, ""))
			}
			if s := pollSummary(logged()); !strings.Contains(s, "logged-skipped=1 (has_started_tag=1)") {
				t.Errorf("summary does not count the held job as a logged skip: %s", s)
			}
			if n := d.state.GetHeldElsewhere(); n != 1 {
				t.Errorf("the state counts %d held jobs, want 1", n)
			}
			if users := NewIPCHandler(d, nil).GetUserList(); len(users) != 1 || users[0].JobsHeldElsewhere != 1 {
				t.Errorf("daemon status gets %+v, want one held job", users)
			}
		})
	}
}

// Having put its started tag on, the client reads the job's tags back once
// every claim made before its own has had time to show. It downloads only if
// its tag is the earliest there, and the job not yet done; and if putting the
// tag on took longer than that, only if its tag is the only one. Otherwise it
// takes its tag off and says why.
func TestPoll_ClaimsTheJobOnlyAsItsEarliestClaim(t *testing.T) {
	now := time.Now()
	add := func(tag string) func(*platform) {
		return func(p *platform) { p.onClaim = func(string) { p.jobs[0].tags = append(p.jobs[0].tags, tag) } }
	}
	for _, tc := range []struct {
		name  string
		setup func(*platform)
		want  SkipReasonCode // ReasonNone: downloaded
	}{
		{"alone", nil, ReasonNone},
		{"an earlier claim shows", add(otherClaim(now.Add(-time.Minute))), ReasonHasStartedTag},
		{"a later claim shows", add(otherClaim(now.Add(time.Minute))), ReasonNone},
		{"the job is done meanwhile", add(config.DownloadedTag), ReasonHasDownloadedTag},
		{"slow to claim, and a later claim shows", func(p *platform) {
			add(otherClaim(now.Add(time.Minute)))(p)
			p.claimDelay = map[string]time.Duration{"a": claimSettle + 100*time.Millisecond}
		}, ReasonHasStartedTag},
		{"slow to claim, alone", func(p *platform) {
			p.claimDelay = map[string]time.Duration{"a": claimSettle + 100*time.Millisecond}
		}, ReasonNone},
		{"the claim does not show", func(p *platform) {
			p.onClaim = func(tag string) {
				p.jobs[0].tags = slices.DeleteFunc(p.jobs[0].tags, func(s string) bool { return s == tag })
			}
		}, ReasonClaimFailed},
		{"the tags cannot be read back", func(p *platform) { p.failReadsFrom = 2 }, ReasonClaimFailed},
		{"the claim is refused", func(p *platform) { p.refuseClaims = true }, ReasonClaimFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortenClaimSettle(t)
			p := newClaimPlatform(t, now.Add(-time.Hour), true)
			if tc.setup != nil {
				tc.setup(p)
			}
			d, logged := claimant(p, "a")
			// The tag goes on only once the state file says it is this
			// client's, so no tag it put on is ever unknown to it.
			var added []string
			claim := p.onClaim
			p.onClaim = func(tag string) {
				if saved := NewState(d.cfg.StateFile); saved.Load() != nil || !slices.Contains(savedTags(saved), tag) {
					t.Errorf("%s went on before the state file held it", tag)
				}
				if claim != nil {
					before := slices.Clone(p.jobs[0].tags)
					claim(tag)
					added = slices.DeleteFunc(slices.Clone(p.jobs[0].tags), func(s string) bool { return slices.Contains(before, s) })
				}
			}

			d.poll(context.Background())
			if got := p.listed() == 1; got != (tc.want == ReasonNone) {
				t.Fatalf("downloaded: %v, want %v", got, tc.want == ReasonNone)
			}
			if own := slices.DeleteFunc(startedTags(p.tags(claimJob)), func(s string) bool { return slices.Contains(added, s) }); len(own) != 0 {
				t.Errorf("the client's started tag is still on the job: %v", own)
			}
			if tc.want != ReasonNone && !strings.Contains(pollSummary(logged()), string(tc.want)+"=1") {
				t.Errorf("summary does not count the job under %s: %s", tc.want, pollSummary(logged()))
			}
		})
	}
}

// A started tag of this client's own that it lost track of, by a crash before
// its state was saved, holds nothing for it: the client downloads the job,
// blames no other client, and takes that tag off.
func TestPoll_ItsOwnLeftoverStartedTagDoesNotHoldTheJob(t *testing.T) {
	shortenClaimSettle(t)
	p := newClaimPlatform(t, time.Now().Add(-time.Hour), true)
	d, logged := claimant(p, "a")
	leftover := startedTag(d.state.ClientID(), time.Now().Add(-time.Hour))
	p.set(func() { p.jobs[0].tags = append(p.jobs[0].tags, leftover) })

	d.poll(context.Background())
	if p.listed() != 1 || d.state.GetDownloadedCount() != 1 {
		t.Errorf("the job was not downloaded: listed %d times, %d downloaded", p.listed(), d.state.GetDownloadedCount())
	}
	if tags := p.tags(claimJob); !slices.Equal(tags, []string{config.DownloadedTag}) {
		t.Errorf("the job's tags end as %v, want only %s", tags, config.DownloadedTag)
	}
	if slices.ContainsFunc(logged(), func(l string) bool { return strings.Contains(l, "another client") }) {
		t.Errorf("the client blamed another client for its own tag:\n%s", strings.Join(logged(), ""))
	}
}

// A claim the state file cannot record is not made: a tag this client put on
// but did not know of would hold the job against it.
func TestPoll_MakesNoClaimItCannotRecord(t *testing.T) {
	shortenClaimSettle(t)
	p := newClaimPlatform(t, time.Now().Add(-time.Hour), true)
	d, logged := claimant(p, "a")
	notADir := filepath.Join(t.TempDir(), "file")
	writeFile(t, notADir, "")
	d.state.filePath = filepath.Join(notADir, "state.json")

	d.poll(context.Background())
	if tags := p.tags(claimJob); len(tags) != 0 || p.listed() != 0 {
		t.Errorf("tags %q put on and %d listings, want none", tags, p.listed())
	}
	if s := pollSummary(logged()); !strings.Contains(s, string(ReasonClaimFailed)+"=1") {
		t.Errorf("summary does not count the job under %s: %s", ReasonClaimFailed, s)
	}
}

// A started tag this client cannot take off, as it cannot once it may no longer
// change the job's tags, is tried until its lease is over, then forgotten, with
// one line to say so: it holds nothing by then.
func TestPoll_GivesUpOnAStartedTagOnceItsLeaseIsOver(t *testing.T) {
	p := newPlatform(t)
	p.failDeletes = math.MaxInt // every removal fails
	d, logged := p.client("", EligibilityConfig{LookbackDays: 7})
	d.state.MarkStarted("gone1", time.Now().Add(-claimLease-time.Minute))
	d.state.MarkStarted("recent1", time.Now().Add(-time.Hour))
	ctx := context.Background()

	d.poll(ctx)
	d.poll(ctx)
	if n := len(p.writes()); n != 3 {
		t.Errorf("%d removals tried, want 3: the old tag's once, the recent one's at each poll", n)
	}
	saved := NewState(d.cfg.StateFile)
	_ = saved.Load()
	if _, ok := saved.StartedTag("gone1"); ok {
		t.Error("the state file still holds the tag past its lease")
	}
	if _, ok := d.state.StartedTag("recent1"); !ok {
		t.Error("the tag still in its lease was forgotten")
	}
	if n := len(slices.DeleteFunc(logged(), func(l string) bool { return !strings.Contains(l, "Gave up") })); n != 1 {
		t.Errorf("%d lines say the client gave up on the tag, want 1", n)
	}
}

// A started tag whose removal fails stays this client's, and the next poll's
// tag retry removes it; the job itself waits out its backoff.
func TestPoll_RetriesARemovalOfItsStartedTagThatFailed(t *testing.T) {
	shortenClaimSettle(t)
	p := newClaimPlatform(t, time.Now().Add(-time.Hour), false)
	p.failDeletes = 1
	d, _ := claimant(p, "a")
	ctx := context.Background()

	d.poll(ctx)
	if started := startedTags(p.tags(claimJob)); len(started) != 1 || d.state.AttemptCount(claimJob) != 1 {
		t.Fatalf("after a failed attempt whose tag removal failed: started tags %v, %d failed attempts; want one of each", started, d.state.AttemptCount(claimJob))
	}
	d.poll(ctx)
	if started := startedTags(p.tags(claimJob)); len(started) != 0 {
		t.Errorf("the next poll left the started tag on: %v", started)
	}
	saved := NewState(d.cfg.StateFile)
	if err := saved.Load(); err != nil || len(saved.Started) != 0 {
		t.Errorf("the state file still holds started tags: %v (%v)", saved.Started, err)
	}
	if n := p.listed(); n != 1 {
		t.Errorf("the job was attempted %d times, want once: it waits out its backoff", n)
	}
}

// The seconds a claim waits for other clients' claims to show are charged to
// the download, not the scan: a backlog of eligible jobs does not run a
// healthy scan past its budget and report it failed.
func TestPoll_ClaimingIsNotChargedToTheScanBudget(t *testing.T) {
	shortenClaimSettle(t)
	orig := scanBudget
	scanBudget = 2*claimSettle - 50*time.Millisecond // less than one claim takes
	t.Cleanup(func() { scanBudget = orig })
	p := newPlatform(t, &fakeJob{id: "job1", name: "one"}, &fakeJob{id: "job2", name: "two"})
	d := p.daemon(t.TempDir(), EligibilityConfig{LookbackDays: 7})

	d.poll(context.Background())
	if msg, _ := d.LastScanError(); msg != "" {
		t.Errorf("the poll recorded a scan error: %s", msg)
	}
	if _, _, unchecked := d.state.GetLeftOut(); unchecked != 0 {
		t.Errorf("the poll ran out of budget, leaving %d jobs unchecked", unchecked)
	}
	for _, id := range []string{"job1", "job2"} {
		if tags := p.tags(id); !slices.Equal(tags, []string{config.DownloadedTag}) {
			t.Errorf("%s's tags are %v, want only %s", id, tags, config.DownloadedTag)
		}
	}
}

// Each poll waits its interval plus up to a tenth of it, at most 30 s, at
// random, so clients started together drift out of step rather than claiming
// the same jobs at the same moment poll after poll.
func TestPollDelay(t *testing.T) {
	for _, tc := range []struct{ interval, most time.Duration }{
		{5 * time.Minute, 30 * time.Second},
		{time.Minute, 6 * time.Second},
	} {
		seen := map[time.Duration]bool{}
		for range 50 {
			d := pollDelay(tc.interval)
			if d < tc.interval || d >= tc.interval+tc.most {
				t.Fatalf("pollDelay(%s) = %s, want from %s to under %s", tc.interval, d, tc.interval, tc.interval+tc.most)
			}
			seen[d] = true
		}
		if len(seen) < 2 {
			t.Errorf("50 delays for %s were all the same: %v", tc.interval, seen)
		}
	}
}
