package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/transfer"
)

// isolateHome points every system's profile folders at a fresh one, which it
// returns, so a test moves, clears and writes nothing of a real profile's.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, v := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(v, home)
	}
	return home
}

// writeFile writes content to the file at path, making its folder.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// logHook is called with each line the daemon logs, as it logs it.
type logHook func(line string)

func (h logHook) Write(p []byte) (int, error) {
	h(string(p))
	return len(p), nil
}

// fakeFailingJobServer serves a poll's worth of the Rescale API for one
// completed job whose download always fails: its only file has a name the
// daemon refuses, so each attempt is recorded as failed at once, with nothing
// transferred. attempts counts the job file listings, the first call of every
// download attempt.
func fakeFailingJobServer(t *testing.T, jobID string) (string, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch r.URL.Path {
		case "/api/v3/jobs/":
			body = map[string]any{"results": []models.JobResponse{{
				ID: jobID, Name: "job", JobStatus: models.JobStatusContent{Status: "Completed"},
			}}}
		case fmt.Sprintf("/api/v2/jobs/%s/files/", jobID):
			attempts.Add(1)
			body = map[string]any{"results": []models.JobFile{{ID: "f1", Name: "../escape.txt", DecryptedSize: 4}}}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &attempts
}

// A failed job waits 5, 10, 20 and then 30 minutes before each new attempt,
// and after the fifth failed attempt the daemon stops trying. Each wait is
// checked from both sides: a poll a minute short of it leaves the job alone,
// and a poll a minute past it tries again.
func TestPoll_FailedJobWaitsOutItsBackoff(t *testing.T) {
	const jobID = "backoff1"
	url, attempts := fakeFailingJobServer(t, jobID)
	d := newDownloadTestDaemon(t, url, t.TempDir(), nil)
	ctx := context.Background()

	d.poll(ctx)
	for i, wait := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute} {
		failed := int32(i + 1) // attempts made, and failed, so far

		ageLastAttempt(d.state, jobID, wait-time.Minute)
		d.poll(ctx)
		if got := attempts.Load(); got != failed {
			t.Fatalf("after %d failed attempts, a poll %s into the %s wait made attempt %d", failed, wait-time.Minute, wait, got)
		}

		ageLastAttempt(d.state, jobID, wait+time.Minute)
		d.poll(ctx)
		if got := attempts.Load(); got != failed+1 {
			t.Fatalf("after %d failed attempts, a poll past the %s wait left %d attempts in all, want %d", failed, wait, got, failed+1)
		}
	}

	// Five attempts have failed; however long ago that was, there is no sixth.
	ageLastAttempt(d.state, jobID, 24*time.Hour)
	d.poll(ctx)
	if got := attempts.Load(); got != 5 {
		t.Errorf("a poll after the fifth failed attempt tried again: %d attempts, want 5", got)
	}
}

// After its fifth failed attempt a job waits for 'daemon retry', which releases
// it in the state file from another process. The daemon holds its state in
// memory, so the release has to reach it there: the next poll tries again.
func TestPoll_DaemonRetryReleasesAJobThatStoppedRetrying(t *testing.T) {
	const jobID = "gaveup1"
	url, attempts := fakeFailingJobServer(t, jobID)
	d := newDownloadTestDaemon(t, url, t.TempDir(), nil)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		d.state.MarkFailed(jobID, "job", fmt.Errorf("attempt %d failed", i))
	}
	ageLastAttempt(d.state, jobID, time.Hour)
	if err := d.state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	d.poll(ctx)
	if got := attempts.Load(); got != 0 {
		t.Fatalf("a job with five failed attempts was tried again (%d attempts)", got)
	}

	daemonRetry(t, d.cfg.StateFile, jobID)
	d.poll(ctx)
	if got := attempts.Load(); got != 1 {
		t.Fatalf("the poll after 'daemon retry' made %d attempts, want 1", got)
	}
	if got := d.state.AttemptCount(jobID); got != 1 {
		t.Errorf("after the release and one more failure the job has %d failed attempts, want 1", got)
	}
}

// daemonRetry does what 'daemon retry --job-id' does to the state file.
func daemonRetry(t *testing.T, stateFile, jobID string) {
	t.Helper()
	if _, err := NewState(stateFile).Retry(jobID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
}

// A 'daemon retry' run while the daemon records another job's failure keeps
// that failure: the retry marks its job in the latest state file, never in a
// copy read before the daemon saved. The file is read back straight after, with
// no later save of the daemon's to put anything back.
func TestDownloadJob_RetryKeepsAFailureTheDaemonRecordsMeanwhile(t *testing.T) {
	url, _ := fakeFailingJobServer(t, "other")
	d := newDownloadTestDaemon(t, url, t.TempDir(), nil)
	for i := 1; i <= MaxDownloadAttempts; i++ {
		d.state.MarkFailed("held", "Held", fmt.Errorf("attempt %d failed", i))
	}
	if err := d.state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	recorded := stall(t, &stateFileStep, func() error {
		d.downloadJob(context.Background(), &CompletedJob{ID: "other", Name: "Other"})
		return nil
	})
	daemonRetry(t, d.cfg.StateFile, "held")
	<-recorded

	for id, want := range map[string]int{"held": 0, "other": 1} {
		if got := attemptsOnDisk(t, d.cfg.StateFile, id); got != want {
			t.Errorf("on disk, %q has %d failed attempts, want %d", id, got, want)
		}
	}
	if got := d.state.AttemptCount("held"); got != 0 {
		t.Errorf("the daemon did not take the retry in: %d failed attempts, want 0", got)
	}
}

// A 'daemon retry' run while the job's next attempt is under way is not lost:
// if that attempt fails, it is the first failure after the release, not the
// fifth in all, which would stop the daemon trying the job at once.
func TestMarkFailed_CountsFromARetryMadeDuringTheAttempt(t *testing.T) {
	d := newDownloadTestDaemon(t, "", t.TempDir(), nil)
	ctx, job := context.Background(), &CompletedJob{ID: "inflight1", Name: "job"}
	for i := 1; i <= 4; i++ {
		d.markFailed(ctx, job, "", fmt.Errorf("attempt %d failed", i))
	}

	daemonRetry(t, d.cfg.StateFile, job.ID) // while attempt 5 runs
	d.markFailed(ctx, job, "", fmt.Errorf("attempt 5 failed"))
	if got := d.state.AttemptCount(job.ID); got != 1 {
		t.Errorf("the attempt that failed after 'daemon retry' is failed attempt %d, want 1", got)
	}
}

// A download cut short because the daemon is stopping has not failed, and is
// not counted: counting it would hold the job in backoff after the restart, and
// a few restarts during one long download would use up all its attempts. The
// user's cancel, through TransferService.CancelBatch as the Transfers tab and
// IPC make it, is a failed attempt that says the file was cancelled.
func TestDownloadJob_StoppingTheDaemonIsNotAFailedAttempt(t *testing.T) {
	const jobID = "stopping1"
	for _, userCancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("user cancel: %v", userCancel), func(t *testing.T) {
			ctx, stopDaemon := context.WithCancel(context.Background())
			defer stopDaemon()
			var d *Daemon
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case fmt.Sprintf("/api/v2/jobs/%s/files/", jobID):
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"results": []models.JobFile{{ID: "f1", Name: "out1.txt", DecryptedSize: 9}},
					})
				case "/api/v3/files/f1/":
					// The file is downloading when the daemon is told to stop, or the user cancels it.
					once.Do(func() {
						if !userCancel {
							stopDaemon()
						} else if err := d.ts.CancelBatch(d.Queue().GetAllBatchStats()[0].BatchID); err != nil {
							t.Errorf("CancelBatch: %v", err)
						}
					})
					<-r.Context().Done()
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)
			d = newDownloadTestDaemon(t, srv.URL, t.TempDir(), nil)

			done := make(chan DownloadOutcome, 1)
			go func() { done <- d.downloadJob(ctx, &CompletedJob{ID: jobID, Name: "job"}) }()
			select {
			case outcome := <-done:
				if outcome == OutcomeDownloaded {
					t.Fatalf("outcome = %q for a download cut short", outcome)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("downloadJob did not return after the download was cut short")
			}

			entry := d.state.Downloaded[jobID]
			if !userCancel && entry != nil {
				t.Errorf("stopping the daemon mid-download was recorded as a failed attempt: %+v", entry)
			}
			if want := "0 failed + 1 cancelled of 1 file"; userCancel && (entry == nil || entry.Error != want || entry.RetryCount != 1) {
				t.Errorf("the user's cancel mid-download was recorded as %+v, want one failed attempt: %q", entry, want)
			}
		})
	}
}

// While the daemon stops, only what the stop itself caused is left out of the
// count: an error of the attempt's own is still a failed attempt, as is a file
// the dispatch could not take. Here the daemon is told to stop as it refuses a
// file, or fails to make a file's folder: the job's only file, or one before
// b.txt, which the stop then keeps from the queue; the record leads with that
// file, not the stop. A download the stop cuts short reports no statistics, so
// its files decide: one that had failed for a reason of its own makes it a
// failed attempt.
func TestMarkFailed_WhileStoppingCountsAGenuineFailure(t *testing.T) {
	refused := models.JobFile{ID: "f1", Name: "../escape.txt"}
	next := models.JobFile{ID: "f2", Name: "b.txt", DecryptedSize: 1}
	jobs := map[string][]models.JobFile{
		"refused":         {refused},
		"refused-then-b":  {refused, next},
		"nofolder-then-b": {{ID: "f1", Name: "y.txt", RelativePath: "sub/y.txt"}, next},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v2/jobs/"), "/files/")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": jobs[id]})
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	d := newDownloadTestDaemon(t, srv.URL, dir, nil)
	writeFile(t, filepath.Join(ComputeOutputDir(dir, "nofolder-then-b", "job", false), "sub"), "") // where y.txt's folder belongs
	var ctx context.Context
	var stopDaemon context.CancelFunc
	d.logger = logging.NewLoggerWithWriter(logHook(func(line string) {
		if strings.Contains(line, "invalid name") || strings.Contains(line, "Failed to create file directory") {
			stopDaemon()
		}
	}))
	for id := range jobs {
		ctx, stopDaemon = context.WithCancel(context.Background())
		d.downloadJob(ctx, &CompletedJob{ID: id, Name: "job"})
		stopDaemon()
	}

	for batchID, errs := range map[string][]error{
		"cut":   {fmt.Errorf("download: %w", context.Canceled)},
		"mixed": {fmt.Errorf("download: %w", context.Canceled), errors.New("file not found")},
	} {
		for _, err := range errs {
			d.Queue().Fail(d.Queue().TrackTransferWithBatch("f", 1, transfer.TaskTypeDownload, "id", "path", "", batchID, "").ID, err)
		}
		d.markFailed(ctx, &CompletedJob{ID: batchID, Name: "job"}, batchID, context.Canceled)
	}

	d.markFailed(ctx, &CompletedJob{ID: "cancelled", Name: "job"}, "", fmt.Errorf("list files: %w", context.Canceled))
	d.markFailed(ctx, &CompletedJob{ID: "denied", Name: "job"}, "", fmt.Errorf("create folder: %w", fs.ErrPermission))
	for id, want := range map[string]int{"refused": 1, "refused-then-b": 1, "nofolder-then-b": 1, "cancelled": 0, "denied": 1, "cut": 0, "mixed": 1} {
		if got := d.state.AttemptCount(id); got != want {
			t.Errorf("while the daemon stopped, %q was recorded as %d failed attempts, want %d", id, got, want)
		}
	}
	if e, want := d.state.Downloaded["refused-then-b"], "1 of 2 files could not be downloaded: filename cannot contain path separators: ../escape.txt; cancelled"; e == nil || !strings.HasPrefix(e.Error, want) {
		t.Errorf("recorded %+v, want an error beginning %q", e, want)
	}
}

// stopAtMarkFailed is a daemon context that is told to stop as markFailed first
// looks at it, so every earlier look found the daemon running.
type stopAtMarkFailed struct {
	context.Context
	stop context.CancelFunc
}

func (c *stopAtMarkFailed) Err() error {
	if pc, _, _, _ := runtime.Caller(1); strings.HasSuffix(runtime.FuncForPC(pc).Name(), ".markFailed") {
		c.stop()
	}
	return c.Context.Err()
}

// The stop can also come once the attempt has failed, just before markFailed
// looks: a file the dispatch left out still counts the attempt.
func TestMarkFailed_AStopJustBeforeItLooksCountsAGenuineFailure(t *testing.T) {
	const jobID = "nofolder-late"
	payload := []byte("abc")
	files := []models.JobFile{
		{ID: "y", Name: "y.txt", RelativePath: "sub/y.txt", DecryptedSize: 1},
		{ID: "p", Name: "present.txt", DecryptedSize: 3, FileChecksums: sha512Of(t, payload)},
	}
	dir := t.TempDir()
	d := newDownloadTestDaemon(t, fakeJobFilesServer(t, jobID, files, nil).URL, dir, nil)
	outDir := ComputeOutputDir(dir, jobID, "job", false)
	writeFile(t, filepath.Join(outDir, "present.txt"), string(payload))
	writeFile(t, filepath.Join(outDir, "sub"), "") // where y.txt's folder belongs

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	d.downloadJob(&stopAtMarkFailed{ctx, stop}, &CompletedJob{ID: jobID, Name: "job"})
	if got := d.state.AttemptCount(jobID); got != 1 || ctx.Err() == nil {
		t.Errorf("stopped as the failure was recorded (%v): %d failed attempts, want 1", ctx.Err(), got)
	}
}

// Stopping a daemon during its first poll waits for the poll loop that follows
// that poll. Stop used to return at once, saving its final state while the poll
// was still under way, and the loop then started after the daemon had stopped.
// Here Stop starts during the first poll's first save; the stopped scan then
// fails and saves nothing, so the next save must be Stop's, once the loop ends.
func TestStop_DuringTheFirstPollWaitsForThePollLoop(t *testing.T) {
	url, _ := fakeFailingJobServer(t, "job1")
	d := newDownloadTestDaemon(t, url, t.TempDir(), nil)
	var stopping, loopEnded atomic.Bool
	d.logger = logging.NewLoggerWithWriter(logHook(func(line string) {
		if strings.Contains(line, "Poll loop") {
			loopEnded.Store(true)
		}
	}))

	stopped := make(chan struct{})
	lock, step := lockFile, stateFileStep
	t.Cleanup(func() { lockFile, stateFileStep = lock, step })
	lockFile = func(path string) (func(), error) {
		if stopping.Load() && !loopEnded.Load() {
			t.Error("Stop saved its final state while the first poll was still under way")
		}
		return lock(path)
	}
	stateFileStep = func() { // the first poll's first save
		if stopping.CompareAndSwap(false, true) {
			go func() { d.Stop(); close(stopped) }()
			<-d.stopChan // Stop has begun
		}
	}

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-stopped
}
