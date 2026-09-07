package pipeline

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/util/tar"
)

// stubUploader stands in for the transfer service: it reports a successful
// upload without touching the network, so the test can control what happens
// after one.
type stubUploader struct {
	mu    sync.Mutex
	calls int
}

func (s *stubUploader) UploadFileSync(ctx context.Context, params SyncUploadParams) (*models.CloudFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return &models.CloudFile{ID: "file-123"}, nil
}

// breakStateWrites makes every later Save fail: the manager writes its CSV to
// "<state file>.tmp" before renaming it into place, and os.Create refuses a
// path a directory already occupies. Failure injection rather than a fake
// manager, because the pipeline holds the concrete *state.Manager the GUI
// shares with it.
func breakStateWrites(t *testing.T, stateFile string) {
	t.Helper()

	if err := os.MkdirAll(stateFile+".tmp", 0o755); err != nil {
		t.Fatalf("block state writes: %v", err)
	}
}

// runUploadStage drives one upload worker over a single item and returns the
// items it handed to job creation.
func runUploadStage(t *testing.T, p *Pipeline, item *workItem) []*workItem {
	t.Helper()

	close(p.feederDone)
	p.uploadQueue <- item
	close(p.uploadQueue)

	var wg sync.WaitGroup
	wg.Add(1)
	go p.uploadWorker(context.Background(), &wg, 0)
	wg.Wait()

	var handed []*workItem
	for queued := range p.jobQueue {
		handed = append(handed, queued)
	}
	return handed
}

// TestUploadCheckpointFailureStopsBeforeDestructiveSteps covers F6's first
// failure sequence: checkpoint writes start failing once the archive is
// uploaded. The upload's file ID never reaches disk, so deleting the archive
// or creating a job from it would leave a restart with nothing to work from.
func TestUploadCheckpointFailureStopsBeforeDestructiveSteps(t *testing.T) {
	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}

	stateFile := filepath.Join(root, "state.csv")
	p := newBatch(t, []models.JobSpec{{JobName: "job_1", Directory: runDir}}, stateFile)
	p.rmTarOnSuccess = true
	uploader := &stubUploader{}
	p.SetSyncUploader(uploader)

	// The archive the tar stage would have produced. Its bytes are never read:
	// the upload is stubbed, and what is under test is what happens after it.
	archive := tar.GenerateTarPath(runDir, p.tempDir, "gzip")
	if err := os.WriteFile(archive, []byte("archive"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	st := p.stateMgr.InitializeState(1, "job_1", runDir)
	st.TarPath = archive
	st.TarStatus = "success"
	if err := p.stateMgr.UpdateState(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	breakStateWrites(t, stateFile)

	handed := runUploadStage(t, p, &workItem{index: 1, jobSpec: p.jobs[0], state: st})

	if uploader.calls != 1 {
		t.Fatalf("uploader called %d times, want 1", uploader.calls)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Errorf("archive deleted although its file ID never reached disk: %v", err)
	}
	if len(handed) != 0 {
		t.Errorf("%d item(s) went on to job creation with an unrecorded upload", len(handed))
	}
	if failed := p.countFailedJobs(); failed != 1 {
		t.Errorf("run counts %d failed job(s), want 1", failed)
	}
}

// TestJobCheckpointFailureStopsBeforeSubmission covers F6's second failure
// sequence: the job is created but its ID does not reach disk. Submitting it
// then starts work whose only record is in memory, and a restart creates and
// submits the job a second time.
func TestJobCheckpointFailureStopsBeforeSubmission(t *testing.T) {
	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}

	var mu sync.Mutex
	var created, submitted int
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/submit/"):
			submitted++
			w.WriteHeader(nethttp.StatusOK)
		default:
			created++
			w.WriteHeader(nethttp.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"job-abc"}`))
		}
	}))
	defer server.Close()

	stateFile := filepath.Join(root, "state.csv")
	spec := models.JobSpec{
		JobName:         "job_1",
		Directory:       runDir,
		AnalysisCode:    "user_included",
		AnalysisVersion: "1.0",
		Command:         "./run.sh",
		CoreType:        "emerald",
		CoresPerSlot:    4,
		Slots:           1,
		WalltimeHours:   1,
		SubmitMode:      "submit",
	}
	p := newBatch(t, []models.JobSpec{spec}, stateFile)
	p.apiClient = api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
	close(p.versionsResolved)

	st := p.stateMgr.InitializeState(1, "job_1", runDir)
	st.TarStatus = "success"
	st.UploadStatus = "success"
	st.FileID = "file-123"
	if err := p.stateMgr.UpdateState(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	breakStateWrites(t, stateFile)

	close(p.feederDone)
	p.jobQueue <- &workItem{index: 1, jobSpec: spec, state: st}
	close(p.jobQueue)

	var wg sync.WaitGroup
	wg.Add(1)
	go p.jobWorker(context.Background(), &wg, 0)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if created != 1 {
		t.Fatalf("job created %d times, want 1", created)
	}
	if submitted != 0 {
		t.Errorf("job submitted although its ID never reached disk")
	}
	if failed := p.countFailedJobs(); failed != 1 {
		t.Errorf("run counts %d failed job(s), want 1", failed)
	}
}

// TestSubmitCheckpointFailureIsReportedAndNotCounted covers F6's last stage.
// Nothing irreversible is left to stop once the job is running, so the failure
// has to be reported instead: the state file still shows the job pending, and a
// resume from it submits the running job a second time. The run must not count
// the job done either, which is what tells the user something needs attention.
func TestSubmitCheckpointFailureIsReportedAndNotCounted(t *testing.T) {
	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}

	var mu sync.Mutex
	var created, submitted int
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/submit/") {
			submitted++
			w.WriteHeader(nethttp.StatusOK)
			return
		}
		created++
		w.WriteHeader(nethttp.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"job-second"}`))
	}))
	defer server.Close()

	stateFile := filepath.Join(root, "state.csv")
	spec := models.JobSpec{
		JobName:         "job_1",
		Directory:       runDir,
		AnalysisCode:    "user_included",
		AnalysisVersion: "1.0",
		Command:         "./run.sh",
		CoreType:        "emerald",
		CoresPerSlot:    4,
		Slots:           1,
		WalltimeHours:   1,
		SubmitMode:      "submit",
	}
	p := newBatch(t, []models.JobSpec{spec}, stateFile)
	p.apiClient = api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

	var logMu sync.Mutex
	var errorLines []string
	p.SetLogCallback(func(level, message, stage, jobName string) {
		if level != "ERROR" {
			return
		}
		logMu.Lock()
		defer logMu.Unlock()
		errorLines = append(errorLines, message)
	})

	// The job exists on the platform already; only its submission is left.
	st := p.stateMgr.InitializeState(1, "job_1", runDir)
	st.TarStatus = "success"
	st.UploadStatus = "success"
	st.FileID = "file-123"
	st.JobID = "job-abc"
	if err := p.stateMgr.UpdateState(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	breakStateWrites(t, stateFile)

	close(p.feederDone)
	p.jobQueue <- &workItem{index: 1, jobSpec: spec, state: st}
	close(p.jobQueue)

	var wg sync.WaitGroup
	wg.Add(1)
	go p.jobWorker(context.Background(), &wg, 0)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if created != 0 {
		t.Errorf("job created %d times, want 0 — it already existed", created)
	}
	if submitted != 1 {
		t.Fatalf("job submitted %d times, want 1", submitted)
	}

	logMu.Lock()
	defer logMu.Unlock()
	named := false
	for _, line := range errorLines {
		if strings.Contains(line, "job-abc") {
			named = true
		}
	}
	if !named {
		t.Errorf("no ERROR line names the submitted job; logged %q", errorLines)
	}
	if p.completedJobs != 0 {
		t.Errorf("the run counted %d job(s) done although the submission was never recorded", p.completedJobs)
	}
	if failed := p.countFailedJobs(); failed != 1 {
		t.Errorf("run counts %d failed job(s), want 1", failed)
	}
}
