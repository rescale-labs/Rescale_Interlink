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
	"github.com/rescale/rescale-int/internal/pur/state"
)

// uploadedJobSpec is a job whose archive a previous run already built and
// uploaded, so job creation is the only stage left. It is create-only, which
// keeps submission out of what these tests count.
func uploadedJobSpec(runDir string) models.JobSpec {
	return models.JobSpec{
		JobName:         "job_1",
		Directory:       runDir,
		AnalysisCode:    "user_included",
		AnalysisVersion: "1.0",
		Command:         "./run.sh",
		CoreType:        "emerald",
		CoresPerSlot:    4,
		Slots:           1,
		WalltimeHours:   1,
		SubmitMode:      "create_only",
	}
}

// isCreate reports the job-creation POST, which shares its path prefix with
// submission and tagging.
func isCreate(r *nethttp.Request) bool {
	return r.Method == nethttp.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs/")
}

// seedUploaded writes the state file a run leaves behind once tar and upload
// have succeeded and nothing else has happened yet.
func seedUploaded(t *testing.T, stateFile string, spec models.JobSpec) {
	t.Helper()

	mgr := state.NewManager(stateFile)
	st := mgr.InitializeState(1, spec.JobName, spec.Directory)
	st.TarStatus = "success"
	st.UploadStatus = "success"
	st.FileID = "file-123"
	if err := mgr.UpdateState(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

// stateOf reads one job's state back off disk, which is what a resume sees.
func stateOf(t *testing.T, stateFile string, index int) *models.JobState {
	t.Helper()

	mgr := state.NewManager(stateFile)
	if err := mgr.Load(); err != nil {
		t.Fatalf("load state: %v", err)
	}
	st := mgr.GetState(index)
	if st == nil {
		t.Fatalf("no state recorded for job %d", index)
	}
	return st
}

// runBatch runs a whole pipeline over one state file the way the CLI does: a
// fresh Pipeline that reads back whatever the previous run recorded. It returns
// the pipeline, every log line it emitted, and the run's own verdict.
func runBatch(t *testing.T, jobs []models.JobSpec, stateFile, apiURL string) (*Pipeline, []string, error) {
	t.Helper()
	return runBatchWith(t, jobs, stateFile, apiURL, false)
}

func runBatchWith(t *testing.T, jobs []models.JobSpec, stateFile, apiURL string, recreate bool) (*Pipeline, []string, error) {
	t.Helper()

	cfg := &config.Config{TarWorkers: 1, UploadWorkers: 1, JobWorkers: 1, TarCompression: "gzip"}
	p, err := NewPipeline(cfg, nil, jobs, PipelineOptions{
		StateFile:             stateFile,
		RecreateIndeterminate: recreate,
	})
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	p.apiClient = api.NewClientForTest(&config.Config{APIBaseURL: apiURL, APIKey: "test"})
	p.analysisResolver = &mockAnalysisResolver{}

	var mu sync.Mutex
	var lines []string
	p.SetLogCallback(func(level, message, stage, jobName string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, level+" "+message)
	})

	runErr := p.Run(context.Background())

	mu.Lock()
	defer mu.Unlock()
	return p, append([]string(nil), lines...), runErr
}

// lostAnswerServer accepts every job creation and loses the answer naming the
// job, which is one of the sequences api.CreateJob reports as ErrJobMayExist.
// It counts the creation requests it received.
func lostAnswerServer(creates *int, mu *sync.Mutex) *httptest.Server {
	return httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if !isCreate(r) {
			w.WriteHeader(nethttp.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		mu.Lock()
		*creates++
		mu.Unlock()
		w.WriteHeader(nethttp.StatusCreated)
		_, _ = w.Write([]byte(`{"id":`))
	}))
}

func containsAll(lines []string, want ...string) bool {
	for _, line := range lines {
		matched := true
		for _, w := range want {
			if !strings.Contains(line, w) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// TestIndeterminateCreateIsRecordedAndNotRepeatedOnResume covers the creation
// whose outcome the platform never confirmed: the request was accepted and the
// answer naming the job did not survive. Recording an ordinary failure with an
// empty job ID left the resume free to create the job a second time, which the
// platform runs and bills.
func TestIndeterminateCreateIsRecordedAndNotRepeatedOnResume(t *testing.T) {
	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}

	var mu sync.Mutex
	creates := 0
	server := lostAnswerServer(&creates, &mu)
	defer server.Close()

	stateFile := filepath.Join(root, "state.csv")
	spec := uploadedJobSpec(runDir)
	seedUploaded(t, stateFile, spec)

	first, _, firstErr := runBatch(t, []models.JobSpec{spec}, stateFile, server.URL)
	if firstErr == nil {
		t.Error("a run holding a job that may exist reported success")
	}

	st := stateOf(t, stateFile, 1)
	if st.JobID != "" {
		t.Errorf("JobID = %q, want empty: the platform never named the job", st.JobID)
	}
	if st.SubmitStatus != "indeterminate" {
		t.Errorf("SubmitStatus = %q, want %q", st.SubmitStatus, "indeterminate")
	}
	if !strings.Contains(st.ErrorMessage, "job_1") {
		t.Errorf("ErrorMessage = %q, want the job name in it", st.ErrorMessage)
	}
	if failed := first.countFailedJobs(); failed != 0 {
		t.Errorf("run counts %d plain failure(s), want 0: the job may exist", failed)
	}

	// The resume: nothing has reconciled the job, so it must not be created
	// again, and the run has to say which job needs checking and how to proceed.
	resumed, lines, resumeErr := runBatch(t, []models.JobSpec{spec}, stateFile, server.URL)

	mu.Lock()
	got := creates
	mu.Unlock()
	if got != 1 {
		t.Fatalf("job created %d times across the run and its resume, want 1", got)
	}
	if resumeErr == nil || !strings.Contains(resumeErr.Error(), "job_1") {
		t.Errorf("resume verdict = %v, want it to name job_1", resumeErr)
	}
	if !containsAll(lines, "job_1", "--recreate-indeterminate") {
		t.Errorf("no log line names the job and how to create it again; lines: %v", lines)
	}
	if failed := resumed.countFailedJobs(); failed != 0 {
		t.Errorf("resume counts %d plain failure(s), want 0", failed)
	}
	if again := stateOf(t, stateFile, 1); again.SubmitStatus != "indeterminate" {
		t.Errorf("after the resume SubmitStatus = %q, want it still %q", again.SubmitStatus, "indeterminate")
	}
}

// TestRecreateIndeterminateCreatesTheJobExactlyOnce covers the reconciliation
// the user does by hand: they looked, the job is not there, and they say so.
// The flag is the only thing that creates it again, and it creates it once.
func TestRecreateIndeterminateCreatesTheJobExactlyOnce(t *testing.T) {
	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}

	var mu sync.Mutex
	creates := 0
	answer := []byte(`{"id":`) // the lost answer, until the test replaces it
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if !isCreate(r) {
			w.WriteHeader(nethttp.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		mu.Lock()
		creates++
		body := answer
		mu.Unlock()
		w.WriteHeader(nethttp.StatusCreated)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	stateFile := filepath.Join(root, "state.csv")
	spec := uploadedJobSpec(runDir)
	seedUploaded(t, stateFile, spec)

	runBatch(t, []models.JobSpec{spec}, stateFile, server.URL)
	if st := stateOf(t, stateFile, 1); st.SubmitStatus != "indeterminate" {
		t.Fatalf("SubmitStatus = %q, want %q", st.SubmitStatus, "indeterminate")
	}

	// The platform answers properly from here on, as it would once whatever
	// broke has passed.
	mu.Lock()
	answer = []byte(`{"id":"job-abc"}`)
	mu.Unlock()

	_, lines, runErr := runBatchWith(t, []models.JobSpec{spec}, stateFile, server.URL, true)
	if runErr != nil {
		t.Errorf("resume with --recreate-indeterminate: %v", runErr)
	}

	mu.Lock()
	got := creates
	mu.Unlock()
	if got != 2 {
		t.Fatalf("job created %d times in total, want 2: one lost answer and one recreation", got)
	}
	if !containsAll(lines, "job_1", "--recreate-indeterminate") {
		t.Errorf("no log line says which job was created again; lines: %v", lines)
	}

	st := stateOf(t, stateFile, 1)
	if st.JobID != "job-abc" {
		t.Errorf("JobID = %q, want %q", st.JobID, "job-abc")
	}
	if st.SubmitStatus == "indeterminate" {
		t.Errorf("SubmitStatus is still %q after the job was created", st.SubmitStatus)
	}
	if st.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q, want it cleared with the ambiguity", st.ErrorMessage)
	}
}

// TestIndeterminateJobsAreCountedApartFromFailures pins the two counts the
// end-of-run summary reports: a failure is work to retry, an unconfirmed
// creation is work someone has to look at. Folding either into the other loses
// the distinction the state file exists to keep.
func TestIndeterminateJobsAreCountedApartFromFailures(t *testing.T) {
	mgr := state.NewManager(filepath.Join(t.TempDir(), "state.csv"))
	for _, st := range []*models.JobState{
		{Index: 1, JobName: "job_failed", TarStatus: "success", UploadStatus: "success",
			SubmitStatus: "failed", ErrorMessage: "create job failed: status 500"},
		{Index: 2, JobName: "job_unconfirmed", TarStatus: "success", UploadStatus: "success",
			SubmitStatus: state.SubmitStatusIndeterminate, ErrorMessage: "job may have been created"},
		{Index: 3, JobName: "job_done", TarStatus: "success", UploadStatus: "success",
			JobID: "job-abc", SubmitStatus: "success"},
	} {
		if err := mgr.UpdateState(st); err != nil {
			t.Fatalf("seed state: %v", err)
		}
	}

	p := &Pipeline{stateMgr: mgr, totalJobs: 3}
	if got := p.countFailedJobs(); got != 1 {
		t.Errorf("countFailedJobs() = %d, want 1: only job_failed failed", got)
	}
	got := p.indeterminateJobNames()
	if len(got) != 1 || got[0] != "job_unconfirmed" {
		t.Errorf("indeterminateJobNames() = %v, want [job_unconfirmed]", got)
	}
}
