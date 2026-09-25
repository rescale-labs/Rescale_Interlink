package daemon

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/services"
	"github.com/rescale/rescale-int/internal/transfer"
)

// fakeJobFilesServer serves just enough of the Rescale API for downloadJob:
// the job file listing, plus a permissive handler for everything else so
// tag/custom-field calls do not hang. tagCalls counts AddJobTag requests.
func fakeJobFilesServer(t *testing.T, jobID string, files []models.JobFile, tagCalls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, fmt.Sprintf("/jobs/%s/files/", jobID)):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"count":   len(files),
				"next":    nil,
				"results": files,
			})
		case strings.HasSuffix(r.URL.Path, fmt.Sprintf("/jobs/%s/tags/", jobID)) && r.Method == http.MethodPost:
			if tagCalls != nil {
				*tagCalls++
			}
			w.WriteHeader(http.StatusCreated)
		default:
			// Anything else (custom fields, file info for a failing download)
			// is a miss. The daemon must treat it as such, not stall.
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newDownloadTestDaemon assembles a Daemon around a test API client pointed at
// a local httptest server. New() cannot be used here: it builds a real API
// client, which rejects non-HTTPS base URLs.
func newDownloadTestDaemon(t *testing.T, baseURL, downloadDir string, elig *EligibilityConfig) *Daemon {
	t.Helper()
	isolateHome(t)
	appCfg := &config.Config{APIKey: "test-key", APIBaseURL: baseURL, ProxyMode: "no-proxy"}
	apiClient := api.NewClientForTest(appCfg)

	daemonCfg := DefaultConfig()
	daemonCfg.StateFile = filepath.Join(t.TempDir(), "state.json")
	daemonCfg.DownloadDir = downloadDir
	daemonCfg.UseJobNameDir = false
	daemonCfg.Eligibility = elig

	logger := logging.NewLogger("daemon-test", nil)
	state := NewState(daemonCfg.StateFile)
	eventBus := events.NewEventBus(0)

	return &Daemon{
		cfg:       daemonCfg,
		appCfg:    appCfg,
		apiClient: apiClient,
		state:     state,
		monitor:   NewMonitor(apiClient, state, nil, logger),
		logger:    logger,
		stopChan:  make(chan struct{}),
		events:    eventBus,
		ts: services.NewTransferService(apiClient, eventBus, services.TransferServiceConfig{
			MaxConcurrent: daemonCfg.MaxConcurrent,
		}),
	}
}

// runDownloadJob calls downloadJob with a hard deadline. A wedged downloadJob
// (the zero-task WaitForBatch spin) leaks a goroutine rather than blocking the
// test run, and reports as a failure instead of a 10-minute hang.
func runDownloadJob(t *testing.T, d *Daemon, job *CompletedJob, budget time.Duration) DownloadOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	done := make(chan DownloadOutcome, 1)
	go func() { done <- d.downloadJob(ctx, job) }()

	select {
	case outcome := <-done:
		return outcome
	case <-time.After(budget):
		t.Fatalf("downloadJob did not return within %s", budget)
		return ""
	}
}

// Every file already on disk means zero requests reach the queue. The batch
// then registers no tasks, its pre-registered metadata is cleaned up, and
// GetBatchStats can never resolve the batch ID again — WaitForBatch used to
// spin on that forever, wedging the poll guard for the daemon's lifetime.
func TestDownloadJob_AllFilesPresentDoesNotHang(t *testing.T) {
	const jobID = "abcdef"
	dir := t.TempDir()

	files := []models.JobFile{
		{ID: "f1", Name: "out1.txt", DecryptedSize: 5},
		{ID: "f2", Name: "out2.txt", DecryptedSize: 3},
	}
	srv := fakeJobFilesServer(t, jobID, files, nil)
	d := newDownloadTestDaemon(t, srv.URL, dir, nil)

	outDir := ComputeOutputDir(dir, jobID, "job", false)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "out1.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "out2.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	outcome := runDownloadJob(t, d, &CompletedJob{ID: jobID, Name: "job"}, 20*time.Second)
	if outcome != OutcomeDownloaded {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeDownloaded)
	}
	entry := d.state.Downloaded[jobID]
	if entry == nil {
		t.Fatal("job not recorded in state")
	}
	if entry.Error != "" {
		t.Errorf("state error = %q, want empty", entry.Error)
	}
	if entry.FileCount != 2 {
		t.Errorf("FileCount = %d, want 2", entry.FileCount)
	}
	if entry.TotalSize != 8 {
		t.Errorf("TotalSize = %d, want 8", entry.TotalSize)
	}
}

// A job whose only files are unusable must be recorded as failed, not
// silently reported as a successful download.
func TestDownloadJob_AllFilesSkippedIsFailure(t *testing.T) {
	const jobID = "ghijkl"
	dir := t.TempDir()

	// Absolute path in Name is rejected by validation.ValidateFilename.
	files := []models.JobFile{{ID: "f1", Name: "../escape.txt", DecryptedSize: 4}}
	srv := fakeJobFilesServer(t, jobID, files, nil)
	d := newDownloadTestDaemon(t, srv.URL, dir, nil)

	outcome := runDownloadJob(t, d, &CompletedJob{ID: jobID, Name: "job"}, 20*time.Second)
	if outcome != OutcomePartialFailure {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomePartialFailure)
	}
	if entry := d.state.Downloaded[jobID]; entry == nil || entry.Error == "" {
		t.Fatalf("expected a recorded failure, got %+v", entry)
	}
}

// A completed job with an empty output set must be tagged. Without the tag it
// passes the tag-first eligibility check on every subsequent poll forever.
func TestDownloadJob_NoFilesAppliesDownloadedTag(t *testing.T) {
	const jobID = "mnopqr"
	dir := t.TempDir()

	tagCalls := 0
	srv := fakeJobFilesServer(t, jobID, nil, &tagCalls)
	d := newDownloadTestDaemon(t, srv.URL, dir, &EligibilityConfig{LookbackDays: 7})

	outcome := runDownloadJob(t, d, &CompletedJob{ID: jobID, Name: "job"}, 20*time.Second)
	if outcome != OutcomeNoFiles {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeNoFiles)
	}
	if tagCalls != 1 {
		t.Errorf("AddJobTag calls = %d, want 1", tagCalls)
	}
	if entry := d.state.Downloaded[jobID]; entry == nil || entry.PendingTagApply {
		t.Errorf("expected a tagged state entry, got %+v", entry)
	}
}

// A download must not inherit the scan's deadline. A large file legitimately
// takes longer than any scan budget, and a download killed part-way leaves a
// partial file that never matches the expected size, so the next poll restarts
// it from zero — forever. The download's context lineage therefore has to come
// from the daemon lifecycle, not from the poll's scan context.
func TestDownloadJob_IgnoresAnExpiredScanBudget(t *testing.T) {
	const jobID = "yzabcd"
	dir := t.TempDir()

	files := []models.JobFile{{ID: "f1", Name: "out1.txt", DecryptedSize: 5}}
	srv := fakeJobFilesServer(t, jobID, files, nil)
	d := newDownloadTestDaemon(t, srv.URL, dir, nil)

	outDir := ComputeOutputDir(dir, jobID, "job", false)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "out1.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Stand in for a scan whose budget has already elapsed. downloadJob must
	// never be handed this context; if it is, the job cannot succeed.
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()

	// The lifecycle context is healthy, which is what poll() now passes down.
	outcome := runDownloadJob(t, d, &CompletedJob{ID: jobID, Name: "job"}, 20*time.Second)
	if outcome != OutcomeDownloaded {
		t.Fatalf("outcome = %q, want %q", outcome, OutcomeDownloaded)
	}

	// And prove the failure mode is real: the same job under the expired
	// context must not succeed, which is why poll() must not pass scanCtx.
	if got := d.downloadJob(expired, &CompletedJob{ID: jobID, Name: "job"}); got == OutcomeDownloaded {
		t.Error("an expired context produced a successful download; the test no longer proves anything")
	}
}

// A batch cancelled before its first task registers leaves nothing for the
// queue to resolve. Waiting on it used to spin for the whole context budget;
// the wait now fails fast, and a user cancel is not reported as a fault.
func TestWaitForRegisteredBatch_FailsFastWhenBatchVanished(t *testing.T) {
	appCfg := &config.Config{APIKey: "test-key", APIBaseURL: "http://127.0.0.1:0", ProxyMode: "no-proxy"}
	ts := services.NewTransferService(api.NewClientForTest(appCfg), events.NewEventBus(0), services.TransferServiceConfig{MaxConcurrent: 2})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err := ts.WaitForRegisteredBatch(ctx, "daemon:gone:1")
	if !errors.Is(err, services.ErrBatchVanished) {
		t.Fatalf("err = %v, want ErrBatchVanished", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s to give up; it should fail on the first tick", elapsed)
	}

	// The tolerant variant must keep its behaviour: before dispatch finishes, a
	// batch that is not registered yet is normal, not an error.
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer shortCancel()
	if _, err := ts.WaitForBatch(shortCtx, "daemon:gone:1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitForBatch err = %v, want DeadlineExceeded (it must keep waiting)", err)
	}
}

// Repeated attempts at the same job produce one Transfers row each. Without the
// attempt number they are indistinguishable, since the label is otherwise
// identical and the start time is dropped with the batch's scan metadata.
func TestDownloadJob_LabelsRepeatAttempts(t *testing.T) {
	const jobID = "efghij"
	dir := t.TempDir()

	files := []models.JobFile{{ID: "f1", Name: "out1.txt", DecryptedSize: 9}}
	srv := fakeJobFilesServer(t, jobID, files, nil)
	d := newDownloadTestDaemon(t, srv.URL, dir, nil)
	job := &CompletedJob{ID: jobID, Name: "job"}

	for i := 0; i < 2; i++ {
		if outcome := runDownloadJob(t, d, job, 60*time.Second); outcome != OutcomePartialFailure {
			t.Fatalf("attempt %d outcome = %q, want %q", i+1, outcome, OutcomePartialFailure)
		}
	}

	labels := make(map[string]struct{})
	tasks := d.Queue().GetTasks()
	for i := range tasks {
		labels[tasks[i].BatchLabel] = struct{}{}
	}
	if _, ok := labels["Auto: job"]; !ok {
		t.Errorf("first attempt should carry the plain label, got %v", labels)
	}
	if _, ok := labels["Auto: job (attempt 2)"]; !ok {
		t.Errorf("second attempt should be labelled as such, got %v", labels)
	}
}

// The shared queue never removes terminal tasks, so a daemon polling for weeks
// would keep one task per downloaded file forever. Finished batches beyond the
// most recent daemonBatchHistoryLimit are retired; the recent ones stay so the
// Transfers tab still shows them.
func TestRetireOldBatchesBoundsTheQueue(t *testing.T) {
	d := newDownloadTestDaemon(t, "http://127.0.0.1:0", t.TempDir(), nil)
	queue := d.Queue()

	total := daemonBatchHistoryLimit + 5
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		batchID := fmt.Sprintf("daemon:job%02d:1", i)
		ids = append(ids, batchID)
		task := queue.TrackTransferWithBatch("f.dat", 1, transfer.TaskTypeDownload,
			"src", "/dest", services.SourceLabelDaemon, batchID, "Auto: job")
		queue.Complete(task.ID)
		d.retireOldBatches(batchID)
	}

	if got := len(queue.GetTasks()); got != daemonBatchHistoryLimit {
		t.Errorf("tasks retained = %d, want %d", got, daemonBatchHistoryLimit)
	}

	live := make(map[string]struct{})
	tasks := queue.GetTasks()
	for i := range tasks {
		live[tasks[i].BatchID] = struct{}{}
	}
	for _, id := range ids[:5] {
		if _, ok := live[id]; ok {
			t.Errorf("batch %s should have been retired", id)
		}
	}
	for _, id := range ids[5:] {
		if _, ok := live[id]; !ok {
			t.Errorf("recent batch %s should still be visible", id)
		}
	}
}

// Each attempt gets its own batch ID. Terminal tasks are never removed from the
// shared queue, so a stable per-job ID made attempt N see attempts 1..N-1's
// failures and the job could never stop being reported as failed.
func TestDownloadJob_RetryDoesNotInheritEarlierFailures(t *testing.T) {
	const jobID = "stuvwx"
	dir := t.TempDir()

	// Not on disk, and file info 404s, so the dispatched download fails fast.
	files := []models.JobFile{{ID: "f1", Name: "out1.txt", DecryptedSize: 9}}
	srv := fakeJobFilesServer(t, jobID, files, nil)
	d := newDownloadTestDaemon(t, srv.URL, dir, nil)

	job := &CompletedJob{ID: jobID, Name: "job"}

	if outcome := runDownloadJob(t, d, job, 60*time.Second); outcome != OutcomePartialFailure {
		t.Fatalf("attempt 1 outcome = %q, want %q", outcome, OutcomePartialFailure)
	}
	firstErr := d.state.Downloaded[jobID].Error
	if !strings.HasPrefix(firstErr, "1 failed") {
		t.Fatalf("attempt 1 error = %q, want it to start with %q", firstErr, "1 failed")
	}

	if outcome := runDownloadJob(t, d, job, 60*time.Second); outcome != OutcomePartialFailure {
		t.Fatalf("attempt 2 outcome = %q, want %q", outcome, OutcomePartialFailure)
	}
	secondErr := d.state.Downloaded[jobID].Error
	if !strings.HasPrefix(secondErr, "1 failed") {
		t.Errorf("attempt 2 error = %q; attempt 1's failure leaked into this attempt's batch stats", secondErr)
	}

	// Two distinct daemon batches, one per attempt.
	seen := make(map[string]struct{})
	tasks := d.Queue().GetTasks()
	for i := range tasks {
		if tasks[i].BatchID != "" {
			seen[tasks[i].BatchID] = struct{}{}
		}
	}
	if len(seen) != 2 {
		t.Errorf("distinct batch IDs = %d, want 2 (%v)", len(seen), seen)
	}
}

// sha512Of returns the checksum the API would carry for these bytes.
func sha512Of(t *testing.T, data []byte) []models.FileChecksum {
	t.Helper()
	sum := sha512.Sum512(data)
	return []models.FileChecksum{{HashFunction: "sha512", FileHash: hex.EncodeToString(sum[:])}}
}

// sameSizeLocalFile is what an interrupted download of "hello" leaves behind:
// the right length, other bytes.
func sameSizeLocalFile(t *testing.T) string {
	t.Helper()
	localPath := filepath.Join(t.TempDir(), "out1.txt")
	if err := os.WriteFile(localPath, []byte("world"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return localPath
}

// alreadyDownloaded matched three exact spellings of SHA-512, so a same-size
// file whose checksum came spelled any other way was adopted by its length.
func TestAlreadyDownloadedChecksAnySpellingOfSHA512(t *testing.T) {
	localPath := sameSizeLocalFile(t)
	remote := sha512Of(t, []byte("hello"))[0].FileHash
	for _, spelling := range []string{"sha512", "SHA-512", "sha-512", "Sha512"} {
		d := &Daemon{logger: logging.NewLoggerWithWriter(new(bytes.Buffer))}
		f := models.JobFile{DecryptedSize: 5, FileChecksums: []models.FileChecksum{{HashFunction: spelling, FileHash: remote}}}
		if d.alreadyDownloaded(localPath, f) {
			t.Errorf("%s: a same-size file with other contents was adopted", spelling)
		}
	}
}

// A file whose checksums include no SHA-512 can only be adopted by its length.
// Downloads warn that such a file was not verified; the daemon said so only at
// debug level. A file with no checksums at all stays quiet, as downloads do.
func TestAlreadyDownloadedWarnsWhenNothingCanVerifyTheFile(t *testing.T) {
	localPath := sameSizeLocalFile(t)
	for _, tc := range []struct {
		checksums []models.FileChecksum
		warn      bool
	}{
		{[]models.FileChecksum{{HashFunction: "md5", FileHash: "abc"}}, true},
		{nil, false},
		{[]models.FileChecksum{{HashFunction: "sha512"}}, false}, // no hash: no checksum
	} {
		var logs bytes.Buffer
		d := &Daemon{logger: logging.NewLoggerWithWriter(&logs)}
		if !d.alreadyDownloaded(localPath, models.JobFile{DecryptedSize: 5, FileChecksums: tc.checksums}) {
			t.Errorf("checksums %v: a same-size file with nothing to check it against was not adopted", tc.checksums)
		}
		if warned := strings.Contains(logs.String(), `"level":"warn"`); warned != tc.warn {
			t.Errorf("checksums %v: warned %v, want %v; logged %s", tc.checksums, warned, tc.warn, logs.String())
		}
	}
}

// The daemon adopted any file whose length matched the remote file's, which is
// exactly what a failed download leaves behind: a pre-allocated or partly
// written file is full-size and holed. Once that file is adopted, no later poll
// ever fetches the real bytes — the job is permanently recorded as downloaded
// with a corrupt file in place. When the remote file carries a checksum, that
// checksum is the test.
func TestDownloadJob_ReDownloadsAnExistingFileThatFailsItsChecksum(t *testing.T) {
	const jobID = "checksum1"
	dir := t.TempDir()

	// The remote file's checksum belongs to different bytes of the same length.
	files := []models.JobFile{{
		ID:            "f1",
		Name:          "out1.txt",
		DecryptedSize: 5,
		FileChecksums: sha512Of(t, []byte("hello")),
	}}
	srv := fakeJobFilesServer(t, jobID, files, nil)
	d := newDownloadTestDaemon(t, srv.URL, dir, nil)

	outDir := ComputeOutputDir(dir, jobID, "job", false)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "out1.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// File info 404s, so the re-download this must trigger fails fast — and a
	// failure is itself the proof that the file was not adopted.
	outcome := runDownloadJob(t, d, &CompletedJob{ID: jobID, Name: "job"}, 60*time.Second)
	if outcome != OutcomePartialFailure {
		t.Fatalf("outcome = %q, want %q: the wrong-content file was adopted instead of re-downloaded",
			outcome, OutcomePartialFailure)
	}
}

// The other half: a file that does hash to the remote checksum is adopted, and
// the next poll must not hash it again. Auto-download polls every few minutes
// over job output directories that can run to many gigabytes, so re-reading
// every adopted file each cycle is not a cost the daemon can carry.
func TestDownloadJob_DoesNotRehashAVerifiedFile(t *testing.T) {
	const jobID = "checksum2"
	dir := t.TempDir()

	payload := []byte("hello")
	files := []models.JobFile{{
		ID:            "f1",
		Name:          "out1.txt",
		DecryptedSize: int64(len(payload)),
		FileChecksums: sha512Of(t, payload),
	}}
	srv := fakeJobFilesServer(t, jobID, files, nil)
	d := newDownloadTestDaemon(t, srv.URL, dir, nil)

	outDir := ComputeOutputDir(dir, jobID, "job", false)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "out1.txt"), payload, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var hashCalls int
	d.hashLocalFile = func(path string) (string, error) {
		hashCalls++
		return encryption.CalculateSHA512(path)
	}

	job := &CompletedJob{ID: jobID, Name: "job"}
	for poll := 1; poll <= 2; poll++ {
		if outcome := runDownloadJob(t, d, job, 20*time.Second); outcome != OutcomeDownloaded {
			t.Fatalf("poll %d outcome = %q, want %q: the verified file was not adopted", poll, outcome, OutcomeDownloaded)
		}
	}

	if hashCalls != 1 {
		t.Errorf("the file was hashed %d times over two polls, want once", hashCalls)
	}
}

// A file whose bytes change after it was verified has to be checked again: the
// cache is a shortcut past the hash, not a substitute for the file.
func TestDownloadJob_RehashesAFileThatChangedAfterVerification(t *testing.T) {
	const jobID = "checksum3"
	dir := t.TempDir()

	payload := []byte("hello")
	files := []models.JobFile{{
		ID:            "f1",
		Name:          "out1.txt",
		DecryptedSize: int64(len(payload)),
		FileChecksums: sha512Of(t, payload),
	}}
	srv := fakeJobFilesServer(t, jobID, files, nil)
	d := newDownloadTestDaemon(t, srv.URL, dir, nil)

	outDir := ComputeOutputDir(dir, jobID, "job", false)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	localPath := filepath.Join(outDir, "out1.txt")
	if err := os.WriteFile(localPath, payload, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	job := &CompletedJob{ID: jobID, Name: "job"}
	if outcome := runDownloadJob(t, d, job, 20*time.Second); outcome != OutcomeDownloaded {
		t.Fatalf("first outcome = %q, want %q", outcome, OutcomeDownloaded)
	}

	// Same length, different bytes, and a modification time the cache can see.
	if err := os.WriteFile(localPath, []byte("world"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := os.Chtimes(localPath, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if outcome := runDownloadJob(t, d, job, 60*time.Second); outcome != OutcomePartialFailure {
		t.Errorf("second outcome = %q, want %q: the cached verification outlived the file it described",
			outcome, OutcomePartialFailure)
	}
}

// A dispatch the batch's cancellation cuts short never queues the rest of the
// job's files, so the job must not be recorded or tagged as downloaded: not when
// every file it had reached was already on disk, not when the files it had
// queued finished before the cancel, and not when the cancel came between
// handing the last file over and its registration, which then never happens,
// even when it was the only one. A user's cancel is a failed attempt that says
// so; a stop is not counted.
func TestDownloadJob_CutShortDispatchIsNotADownload(t *testing.T) {
	const jobID = "cutshort"
	payload := []byte("abc")
	present := models.JobFile{ID: "p", Name: "present.txt", DecryptedSize: 3, FileChecksums: sha512Of(t, payload)}
	queued := models.JobFile{ID: "a", Name: "a.txt", DecryptedSize: 1} // finishes before the cancel
	missing := models.JobFile{ID: "b", Name: "b.txt", DecryptedSize: 1}

	for _, tc := range []struct {
		name  string
		files []models.JobFile
		stop  bool // the daemon stops, where otherwise the user cancels the batch
	}{
		{"nothing queued yet", []models.JobFile{present, missing}, false},
		{"after the queued file finished", []models.JobFile{queued, present, missing}, false},
		{"the daemon stops", []models.JobFile{present, missing}, true},
		{"after the last file was handed over", []models.JobFile{queued, missing}, false},
		{"after the only file was handed over", []models.JobFile{missing}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tagCalls := 0
			fake := fakeJobFilesServer(t, jobID, tc.files, &tagCalls)
			inFlight := make(chan struct{})
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/v3/files/") {
					once.Do(func() { close(inFlight) }) // a.txt's download, running until the cancel
					<-r.Context().Done()
					return
				}
				fake.Config.Handler.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)

			dir := t.TempDir()
			d := newDownloadTestDaemon(t, srv.URL, dir, &EligibilityConfig{LookbackDays: 7})
			outDir := ComputeOutputDir(dir, jobID, "job", false)
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(outDir, present.Name), payload, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			ctx, stopDaemon := context.WithCancel(context.Background())
			defer stopDaemon()
			cancel := func() {
				if tc.files[0].ID == queued.ID {
					<-inFlight
					d.Queue().Complete(d.Queue().GetTasks()[0].ID) // stands in for a.txt finishing
				}
				id := d.Queue().GetAllBatchStats()[0].BatchID
				switch {
				case tc.stop:
					stopDaemon()
				case tc.files[0].ID == missing.ID:
					// No task registers. The queue's cancel alone is the moment a wait
					// finds the batch gone, before CancelBatch adds its row for it.
					_ = d.Queue().CancelBatch(id)
				default:
					_ = d.ts.CancelBatch(id)
				}
			}
			// The cancel comes while the dispatcher checks present.txt, or else
			// once b.txt is handed over, and registration then never reads it.
			d.hashLocalFile = func(path string) (string, error) { cancel(); return encryption.CalculateSHA512(path) }
			start := startDownloadBatch
			t.Cleanup(func() { startDownloadBatch = start })
			startDownloadBatch = func(ts *services.TransferService, ctx context.Context, handed <-chan services.TransferRequest, batchID, label, source string, cancelFn context.CancelFunc) error {
				read := make(chan services.TransferRequest)
				go func() {
					defer close(read)
					for req := range handed {
						if req.Name == missing.Name {
							cancel()
							continue
						}
						read <- req
					}
				}()
				return start(ts, ctx, read, batchID, label, source, cancelFn)
			}

			outcome := d.downloadJob(ctx, &CompletedJob{ID: jobID, Name: "job"})
			entry := d.state.Downloaded[jobID]
			if outcome == OutcomeDownloaded || tagCalls != 0 || entry != nil && (entry.Error == "" || entry.PendingTagApply) {
				t.Fatalf("a cut-short dispatch was taken for a download: outcome %s, state %+v, tag calls %d",
					outcome, entry, tagCalls)
			}
			if tc.stop && entry != nil {
				t.Errorf("a stop that cut the dispatch short was counted as a failed attempt: %+v", entry)
			}
			if !tc.stop && (entry == nil || !strings.HasPrefix(entry.Error, "cancelled before all")) {
				t.Errorf("recorded %+v, want an error saying the batch was cancelled before all files were queued", entry)
			}
		})
	}
}

// A file whose folder cannot be made, or whose name is refused, is neither
// verified nor downloaded, so its job is not downloaded, however many of its
// other files are on disk: the attempt fails, and no tag goes on. The failure
// leads with why, and counts every file of the job, the file left out included.
func TestDownloadJob_AFileItCannotPlaceIsNotADownload(t *testing.T) {
	payload := []byte("abc")
	present := models.JobFile{ID: "p", Name: "present.txt", DecryptedSize: 3, FileChecksums: sha512Of(t, payload)}
	refused := models.JobFile{ID: "r", Name: "../escape.txt", DecryptedSize: 1}
	for _, tc := range []struct {
		jobID string
		files []models.JobFile
		want  string // how the failure begins; nothing is appended to it
	}{
		{"nofolder", []models.JobFile{present, {ID: "y", Name: "y.txt", RelativePath: "sub/y.txt", DecryptedSize: 1}}, "1 of 2 files could not be downloaded: mkdir "},
		{"refused", []models.JobFile{present, refused}, "1 of 2 files could not be downloaded: filename cannot contain path separators: ../escape.txt"},
		{"refused-alone", []models.JobFile{refused}, "1 of 1 file could not be downloaded: filename cannot contain path separators: ../escape.txt"},
	} {
		tagCalls := 0
		srv := fakeJobFilesServer(t, tc.jobID, tc.files, &tagCalls)
		dir := t.TempDir()
		d := newDownloadTestDaemon(t, srv.URL, dir, &EligibilityConfig{LookbackDays: 7})
		outDir := ComputeOutputDir(dir, tc.jobID, "job", false)
		writeFile(t, filepath.Join(outDir, "present.txt"), string(payload))
		writeFile(t, filepath.Join(outDir, "sub"), "") // a file where y.txt's folder belongs

		outcome := runDownloadJob(t, d, &CompletedJob{ID: tc.jobID, Name: "job"}, 20*time.Second)
		if entry := d.state.Downloaded[tc.jobID]; outcome == OutcomeDownloaded || tagCalls != 0 || d.state.AttemptCount(tc.jobID) != 1 ||
			entry.PendingTagApply || !strings.HasPrefix(entry.Error, tc.want) || strings.Contains(entry.Error, ";") {
			t.Errorf("%s: outcome %s, state %+v, tag calls %d: want one failed attempt recorded as %q", tc.jobID, outcome, entry, tagCalls, tc.want)
		}
	}
}
