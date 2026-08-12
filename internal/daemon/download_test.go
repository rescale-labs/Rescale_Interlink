package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
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
