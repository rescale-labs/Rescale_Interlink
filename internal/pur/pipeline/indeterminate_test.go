package pipeline

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	return runBatchOpts(t, jobs, apiURL, PipelineOptions{
		StateFile:             stateFile,
		RecreateIndeterminate: recreate,
	})
}

func runBatchOpts(t *testing.T, jobs []models.JobSpec, apiURL string, opts PipelineOptions) (*Pipeline, []string, error) {
	t.Helper()

	cfg := &config.Config{TarWorkers: 1, UploadWorkers: 1, JobWorkers: 1, TarCompression: "gzip"}
	p, err := NewPipeline(cfg, nil, jobs, opts)
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

// seedReadyToCreate writes the state of a job whose archive is built, uploaded
// and still on disk, so job creation is the only stage left and nothing before
// the create request needs to write state. That is what lets these tests take
// the state file away at exactly the create.
func seedReadyToCreate(t *testing.T, stateFile, tarPath string, spec models.JobSpec, submitStatus, errorMessage string) {
	t.Helper()

	if err := os.WriteFile(tarPath, []byte("archive"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	mgr := state.NewManager(stateFile)
	st := mgr.InitializeState(1, spec.JobName, spec.Directory)
	st.TarStatus = "success"
	st.TarPath = tarPath
	st.UploadStatus = "success"
	st.FileID = "file-123"
	st.SubmitStatus = submitStatus
	st.ErrorMessage = errorMessage
	if err := mgr.UpdateState(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

// answeringServer names every job it is asked to create and counts the
// requests.
func answeringServer(creates *int, mu *sync.Mutex) *httptest.Server {
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
		_, _ = w.Write([]byte(`{"id":"job-abc"}`))
	}))
}

// TestCreateIntentThatCannotBeRecordedSendsNoRequest covers the state file that
// stops taking writes just before job creation. The create request is not
// idempotent, so sending it without a durable record of having sent it leaves a
// job the next resume creates a second time: the run must refuse the request
// instead.
func TestCreateIntentThatCannotBeRecordedSendsNoRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory does not refuse writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}

	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	stateDir := filepath.Join(root, "state")
	for _, dir := range []string{runDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	var mu sync.Mutex
	creates := 0
	server := answeringServer(&creates, &mu)
	defer server.Close()

	stateFile := filepath.Join(stateDir, "state.csv")
	spec := uploadedJobSpec(runDir)
	seedReadyToCreate(t, stateFile, filepath.Join(root, "job_1.tar.gz"), spec, "pending", "")

	// The state directory stops taking writes between the last checkpoint and
	// the create.
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatalf("chmod state dir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(stateDir, 0o700) })

	_, lines, runErr := runBatch(t, []models.JobSpec{spec}, stateFile, server.URL)

	mu.Lock()
	got := creates
	mu.Unlock()
	if got != 0 {
		t.Errorf("platform received %d create request(s), want 0: the run could not record sending one", got)
	}
	if !containsAll(lines, "Creating job", "job_1") {
		t.Fatalf("the run never reached job creation, so this proves nothing; lines: %v", lines)
	}
	if !containsAll(lines, "could not record the job creation before sending it") {
		t.Errorf("no log line says why the request was not sent; lines: %v", lines)
	}
	if runErr == nil {
		t.Error("a run that could not create its job reported success")
	}
}

// TestCreatingLeftBehindByADeathIsNotRecreated covers the process that died
// between handing the create request over and learning its outcome. All the
// state file holds is the intent, which says exactly as much as an
// unanswered request: the platform may be running the job already.
func TestCreatingLeftBehindByADeathIsNotRecreated(t *testing.T) {
	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}

	var mu sync.Mutex
	creates := 0
	server := answeringServer(&creates, &mu)
	defer server.Close()

	stateFile := filepath.Join(root, "state.csv")
	spec := uploadedJobSpec(runDir)
	seedReadyToCreate(t, stateFile, filepath.Join(root, "job_1.tar.gz"), spec, "creating", "")

	resumed, lines, resumeErr := runBatch(t, []models.JobSpec{spec}, stateFile, server.URL)

	mu.Lock()
	got := creates
	mu.Unlock()
	if got != 0 {
		t.Errorf("job created %d time(s) on an ordinary resume, want 0", got)
	}
	if resumeErr == nil || !strings.Contains(resumeErr.Error(), "job_1") {
		t.Errorf("resume verdict = %v, want it to name job_1", resumeErr)
	}
	if !containsAll(lines, "job_1", "--recreate-indeterminate") {
		t.Errorf("no log line names the job and how to create it again; lines: %v", lines)
	}
	if failed := resumed.countFailedJobs(); failed != 0 {
		t.Errorf("resume counts %d plain failure(s), want 0: the job may exist", failed)
	}

	// The user checked the platform, found nothing, and says so. That creates
	// the job once.
	_, _, flagErr := runBatchWith(t, []models.JobSpec{spec}, stateFile, server.URL, true)
	if flagErr != nil {
		t.Errorf("resume with --recreate-indeterminate: %v", flagErr)
	}

	mu.Lock()
	got = creates
	mu.Unlock()
	if got != 1 {
		t.Fatalf("job created %d time(s) in total, want 1", got)
	}
	st := stateOf(t, stateFile, 1)
	if st.JobID != "job-abc" {
		t.Errorf("JobID = %q, want %q", st.JobID, "job-abc")
	}
	if st.SubmitStatus == "creating" || st.SubmitStatus == state.SubmitStatusIndeterminate {
		t.Errorf("SubmitStatus = %q, want the outcome to have replaced the intent", st.SubmitStatus)
	}
}

// TestOrdinaryCreateRecordsTheIntentBeforeTheRequest is the other side of the
// refusal: the ordinary path still ends at the job ID, and the state file
// already carries the creation while the platform holds the request. The
// server reads the file it would be resumed from.
func TestOrdinaryCreateRecordsTheIntentBeforeTheRequest(t *testing.T) {
	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	stateFile := filepath.Join(root, "state.csv")

	var mu sync.Mutex
	creates := 0
	onDisk := models.JobState{}
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if !isCreate(r) {
			w.WriteHeader(nethttp.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		mgr := state.NewManager(stateFile)
		if err := mgr.Load(); err == nil {
			if st := mgr.GetState(1); st != nil {
				mu.Lock()
				onDisk = *st
				mu.Unlock()
			}
		}
		mu.Lock()
		creates++
		mu.Unlock()
		w.WriteHeader(nethttp.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"job-abc"}`))
	}))
	defer server.Close()

	spec := uploadedJobSpec(runDir)
	seedReadyToCreate(t, stateFile, filepath.Join(root, "job_1.tar.gz"), spec, "pending", "")

	_, _, runErr := runBatch(t, []models.JobSpec{spec}, stateFile, server.URL)
	if runErr != nil {
		t.Errorf("ordinary run: %v", runErr)
	}

	mu.Lock()
	got := creates
	seen := onDisk
	mu.Unlock()
	if got != 1 {
		t.Fatalf("job created %d time(s), want 1", got)
	}
	if seen.SubmitStatus != "creating" {
		t.Errorf("while the platform held the request the state file said %q, want %q",
			seen.SubmitStatus, "creating")
	}
	if seen.JobID != "" {
		t.Errorf("JobID = %q before the platform answered, want empty", seen.JobID)
	}

	st := stateOf(t, stateFile, 1)
	if st.JobID != "job-abc" {
		t.Errorf("JobID = %q, want %q", st.JobID, "job-abc")
	}
	if st.SubmitStatus == "creating" {
		t.Errorf("SubmitStatus = %q, want the job ID to have replaced the intent", st.SubmitStatus)
	}
}

// remoteInputJobSpec is a job with nothing of its own to archive: its inputs are
// files already on the platform, as a DOE sweep's or a remote-input single job's
// are. The feeder skips tar and upload for such a job — and checkpoints that
// skip before any worker sees it.
func remoteInputJobSpec() models.JobSpec {
	return models.JobSpec{
		JobName:         "job_1",
		AnalysisCode:    "user_included",
		AnalysisVersion: "1.0",
		Command:         "./run.sh",
		CoreType:        "emerald",
		CoresPerSlot:    4,
		Slots:           1,
		WalltimeHours:   1,
		SubmitMode:      "create_only",
		InputFiles:      []string{"file-remote-1"},
	}
}

// seedUnconfirmedWithoutArchive writes the state left behind by a job that built
// no archive of its own — a DOE sweep, a remote-input single job or a
// submit-existing row — and whose creation was never confirmed.
func seedUnconfirmedWithoutArchive(t *testing.T, stateFile string, spec models.JobSpec, submitStatus, cause string) {
	t.Helper()

	mgr := state.NewManager(stateFile)
	st := mgr.InitializeState(1, spec.JobName, spec.Directory)
	st.TarStatus = "skipped"
	st.UploadStatus = "skipped"
	st.SubmitStatus = submitStatus
	st.ErrorMessage = cause
	if err := mgr.UpdateState(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

// heldResolver keeps every job worker at the first thing it waits for — version
// resolution — so no create intent is recorded while the test is looking at the
// window before it.
type heldResolver struct{ release chan struct{} }

func (r *heldResolver) GetAnalyses(ctx context.Context) ([]models.Analysis, error) {
	<-r.release
	return nil, nil
}

// runInterruptedAtFeederCheckpoint runs a batch and cancels it the moment the
// feeder has checkpointed its job, which is the window a cancel or a death falls
// into before the worker records its create intent. It reports whether that
// checkpoint happened at all, so a test cannot pass because the feeder never got
// there.
func runInterruptedAtFeederCheckpoint(t *testing.T, jobs []models.JobSpec, apiURL string, opts PipelineOptions) bool {
	t.Helper()

	cfg := &config.Config{TarWorkers: 1, UploadWorkers: 1, JobWorkers: 1, TarCompression: "gzip"}
	p, err := NewPipeline(cfg, nil, jobs, opts)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	p.apiClient = api.NewClientForTest(&config.Config{APIBaseURL: apiURL, APIKey: "test"})
	held := &heldResolver{release: make(chan struct{})}
	p.analysisResolver = held

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	checkpointed := false
	p.SetStateChangeCallback(func(jobName, stage, newStatus, jobID, errorMessage string, uploadProgress float64) {
		if stage != "tar" || newStatus != "skipped" {
			return
		}
		mu.Lock()
		checkpointed = true
		mu.Unlock()
		cancel()
	})

	if err := p.Run(ctx); err != nil {
		t.Fatalf("cancelled run reported %v, want no verdict", err)
	}
	close(held.release)
	<-p.versionsResolved

	mu.Lock()
	defer mu.Unlock()
	return checkpointed
}

// TestRecreationKeepsTheUnconfirmedRecordUntilTheWorkerRecordsItsIntent covers
// the two routes whose feeder checkpoints sit between the recreation
// authorization and the worker's create intent: a job with no archive of its own
// (DOE, remote inputs) and submit-existing.
//
// --recreate-indeterminate is this run's permission to create the job again, not
// a new fact about the platform. Until the worker has recorded that it is
// sending the request, what is on disk is still the creation nobody has
// reconciled, and an interruption in between has to leave it that way: an
// ordinary "pending" there is one the next resume creates from with no flag at
// all.
func TestRecreationKeepsTheUnconfirmedRecordUntilTheWorkerRecordsItsIntent(t *testing.T) {
	const cause = "job may have been created: the platform took the request and the answer was lost"

	for _, tc := range []struct {
		name          string
		skipTarUpload bool
	}{
		{name: "remote inputs", skipTarUpload: false},
		{name: "submit existing", skipTarUpload: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := namespaceTestRoot(t)
			work := filepath.Join(root, "work")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatalf("mkdir work: %v", err)
			}
			// The job has no directory of its own, so the batch sites its
			// archive directory on the process working directory.
			t.Chdir(work)

			var mu sync.Mutex
			creates := 0
			server := answeringServer(&creates, &mu)
			defer server.Close()

			stateFile := filepath.Join(root, "state.csv")
			spec := remoteInputJobSpec()
			seedUnconfirmedWithoutArchive(t, stateFile, spec, state.SubmitStatusIndeterminate, cause)

			opts := PipelineOptions{
				StateFile:             stateFile,
				SkipTarUpload:         tc.skipTarUpload,
				RecreateIndeterminate: true,
			}
			if !runInterruptedAtFeederCheckpoint(t, []models.JobSpec{spec}, server.URL, opts) {
				t.Fatal("the feeder never checkpointed this job, so this proves nothing")
			}

			st := stateOf(t, stateFile, 1)
			if st.SubmitStatus != state.SubmitStatusIndeterminate {
				t.Errorf("after the feeder's checkpoint the state file says SubmitStatus = %q, want %q: "+
					"nothing had been sent yet", st.SubmitStatus, state.SubmitStatusIndeterminate)
			}
			if st.JobID != "" {
				t.Errorf("JobID = %q, want empty", st.JobID)
			}
			if st.ErrorMessage != cause {
				t.Errorf("ErrorMessage = %q, want the unconfirmed creation's own %q", st.ErrorMessage, cause)
			}

			// What that record is for: the next resume, without the flag,
			// leaves the job alone and says so.
			resumeOpts := PipelineOptions{StateFile: stateFile, SkipTarUpload: tc.skipTarUpload}
			resumed, lines, resumeErr := runBatchOpts(t, []models.JobSpec{spec}, server.URL, resumeOpts)

			mu.Lock()
			got := creates
			mu.Unlock()
			if got != 0 {
				t.Errorf("job created %d time(s) on an unflagged resume, want 0", got)
			}
			if resumeErr == nil || !strings.Contains(resumeErr.Error(), "job_1") {
				t.Errorf("resume verdict = %v, want it to name job_1", resumeErr)
			}
			if !containsAll(lines, "job_1", "--recreate-indeterminate") {
				t.Errorf("no log line names the job and how to create it again; lines: %v", lines)
			}
			if failed := resumed.countFailedJobs(); failed != 0 {
				t.Errorf("resume counts %d plain failure(s), want 0: the job may exist", failed)
			}

			// And the flag still does its job when the run is left alone: one
			// creation, and the outcome replaces the unconfirmed record.
			if _, _, err := runBatchOpts(t, []models.JobSpec{spec}, server.URL, opts); err != nil {
				t.Errorf("resume with --recreate-indeterminate: %v", err)
			}

			mu.Lock()
			got = creates
			mu.Unlock()
			if got != 1 {
				t.Fatalf("job created %d time(s) in total, want 1", got)
			}
			done := stateOf(t, stateFile, 1)
			if done.JobID != "job-abc" {
				t.Errorf("JobID = %q, want %q", done.JobID, "job-abc")
			}
			if state.MayAlreadyExist(done) {
				t.Errorf("SubmitStatus = %q, want the created job's own status", done.SubmitStatus)
			}
			if done.ErrorMessage != "" {
				t.Errorf("ErrorMessage = %q, want it cleared with the ambiguity", done.ErrorMessage)
			}
		})
	}
}

// TestRecreationThatCannotBuildItsRequestKeepsTheUnconfirmedRecord covers the
// other write that precedes the create intent: the job spec no longer builds a
// request, because the row was edited between the two runs. Nothing is sent, so
// the earlier creation is still the one to check the platform for — recording an
// ordinary failure over it is a record the next resume retries with no flag.
func TestRecreationThatCannotBuildItsRequestKeepsTheUnconfirmedRecord(t *testing.T) {
	const cause = "job may have been created: the platform took the request and the answer was lost"

	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}

	var mu sync.Mutex
	creates := 0
	server := answeringServer(&creates, &mu)
	defer server.Close()

	stateFile := filepath.Join(root, "state.csv")
	spec := uploadedJobSpec(runDir)
	seedReadyToCreate(t, stateFile, filepath.Join(root, "job_1.tar.gz"), spec,
		state.SubmitStatusIndeterminate, cause)

	// The edit: license settings the request builder rejects.
	spec.LicenseSettings = "{not json"

	p, lines, runErr := runBatchWith(t, []models.JobSpec{spec}, stateFile, server.URL, true)

	mu.Lock()
	got := creates
	mu.Unlock()
	if got != 0 {
		t.Fatalf("platform received %d create request(s), want 0", got)
	}
	if !containsAll(lines, "Failed to build request") {
		t.Fatalf("the run never reached the request builder, so this proves nothing; lines: %v", lines)
	}

	st := stateOf(t, stateFile, 1)
	if st.SubmitStatus != state.SubmitStatusIndeterminate {
		t.Errorf("SubmitStatus = %q, want it still %q: nothing was sent, so nothing was resolved",
			st.SubmitStatus, state.SubmitStatusIndeterminate)
	}
	if st.ErrorMessage != cause {
		t.Errorf("ErrorMessage = %q, want the unconfirmed creation's own %q", st.ErrorMessage, cause)
	}
	if failed := p.countFailedJobs(); failed != 0 {
		t.Errorf("run counts %d plain failure(s), want 0: the job may exist", failed)
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "job_1") {
		t.Errorf("run verdict = %v, want it to name job_1", runErr)
	}
}

// TestRecreationRejectedForNoInputsKeepsTheUnconfirmedRecord covers the feeder's
// own pre-request write: the job's inputs were edited away between the two runs,
// so checkJobHasInputs rejects it before any intent or request. Recording a
// plain failure there replaces the unconfirmed creation with a record the next
// resume creates from with no flag at all — restoring the inputs would then
// create the job the user was told to check the platform for.
func TestRecreationRejectedForNoInputsKeepsTheUnconfirmedRecord(t *testing.T) {
	const cause = "job may have been created: the platform took the request and the answer was lost"

	for _, unconfirmed := range []string{state.SubmitStatusCreating, state.SubmitStatusIndeterminate} {
		t.Run(unconfirmed, func(t *testing.T) {
			root := namespaceTestRoot(t)
			work := filepath.Join(root, "work")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatalf("mkdir work: %v", err)
			}
			// The job has no directory of its own, so the batch sites its
			// archive directory on the process working directory.
			t.Chdir(work)

			var mu sync.Mutex
			creates := 0
			server := answeringServer(&creates, &mu)
			defer server.Close()

			stateFile := filepath.Join(root, "state.csv")
			spec := remoteInputJobSpec()
			seedUnconfirmedWithoutArchive(t, stateFile, spec, unconfirmed, cause)

			// The edit: the row's remote input file IDs are gone, so the job
			// would be created carrying nothing.
			stripped := spec
			stripped.InputFiles = nil

			p, lines, runErr := runBatchWith(t, []models.JobSpec{stripped}, stateFile, server.URL, true)

			mu.Lock()
			got := creates
			mu.Unlock()
			if got != 0 {
				t.Fatalf("platform received %d create request(s), want 0", got)
			}
			if !containsAll(lines, "REJECTED", "no input file IDs") {
				t.Fatalf("the feeder never rejected the job, so this proves nothing; lines: %v", lines)
			}

			st := stateOf(t, stateFile, 1)
			if st.SubmitStatus != unconfirmed {
				t.Errorf("SubmitStatus = %q, want it still %q: nothing was sent, so nothing was resolved",
					st.SubmitStatus, unconfirmed)
			}
			if st.JobID != "" {
				t.Errorf("JobID = %q, want empty", st.JobID)
			}
			if st.ErrorMessage != cause {
				t.Errorf("ErrorMessage = %q, want the unconfirmed creation's own %q", st.ErrorMessage, cause)
			}
			if failed := p.countFailedJobs(); failed != 0 {
				t.Errorf("run counts %d plain failure(s), want 0: the job may exist", failed)
			}
			if runErr == nil || !strings.Contains(runErr.Error(), "job_1") {
				t.Errorf("run verdict = %v, want it to name job_1", runErr)
			}

			// What that record is for: the inputs come back, and the next resume
			// without the flag still leaves the job alone and says so.
			resumed, resumeLines, resumeErr := runBatch(t, []models.JobSpec{spec}, stateFile, server.URL)

			mu.Lock()
			got = creates
			mu.Unlock()
			if got != 0 {
				t.Errorf("job created %d time(s) on an unflagged resume, want 0", got)
			}
			if resumeErr == nil || !strings.Contains(resumeErr.Error(), "job_1") {
				t.Errorf("resume verdict = %v, want it to name job_1", resumeErr)
			}
			if !containsAll(resumeLines, "job_1", "--recreate-indeterminate") {
				t.Errorf("no log line names the job and how to create it again; lines: %v", resumeLines)
			}
			if failed := resumed.countFailedJobs(); failed != 0 {
				t.Errorf("resume counts %d plain failure(s), want 0: the job may exist", failed)
			}

			// And with the inputs restored the flag still creates it once.
			if _, _, err := runBatchWith(t, []models.JobSpec{spec}, stateFile, server.URL, true); err != nil {
				t.Errorf("resume with --recreate-indeterminate: %v", err)
			}

			mu.Lock()
			got = creates
			mu.Unlock()
			if got != 1 {
				t.Fatalf("job created %d time(s) in total, want 1", got)
			}
			done := stateOf(t, stateFile, 1)
			if done.JobID != "job-abc" {
				t.Errorf("JobID = %q, want %q", done.JobID, "job-abc")
			}
			if state.MayAlreadyExist(done) {
				t.Errorf("SubmitStatus = %q, want the created job's own status", done.SubmitStatus)
			}
			if done.ErrorMessage != "" {
				t.Errorf("ErrorMessage = %q, want it cleared with the ambiguity", done.ErrorMessage)
			}
		})
	}
}

// TestOrdinaryJobWithNoInputsStillFails is the control for the same rejection
// outside a recreation: nothing may exist on the platform, so the job fails on
// disk and in the count exactly as it did before.
func TestOrdinaryJobWithNoInputsStillFails(t *testing.T) {
	root := namespaceTestRoot(t)
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	t.Chdir(work)

	var mu sync.Mutex
	creates := 0
	server := answeringServer(&creates, &mu)
	defer server.Close()

	stateFile := filepath.Join(root, "state.csv")
	spec := remoteInputJobSpec()
	spec.InputFiles = nil

	p, lines, runErr := runBatch(t, []models.JobSpec{spec}, stateFile, server.URL)

	mu.Lock()
	got := creates
	mu.Unlock()
	if got != 0 {
		t.Fatalf("platform received %d create request(s), want 0", got)
	}
	if !containsAll(lines, "REJECTED", "no input file IDs") {
		t.Fatalf("the feeder never rejected the job, so this proves nothing; lines: %v", lines)
	}

	st := stateOf(t, stateFile, 1)
	if st.SubmitStatus != "failed" {
		t.Errorf("SubmitStatus = %q, want %q", st.SubmitStatus, "failed")
	}
	if !strings.Contains(st.ErrorMessage, "no inputs at all") {
		t.Errorf("ErrorMessage = %q, want the rejection's own reason", st.ErrorMessage)
	}
	if failed := p.countFailedJobs(); failed != 1 {
		t.Errorf("run counts %d failure(s), want 1", failed)
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "1 of 1 job(s) failed") {
		t.Errorf("run verdict = %v, want it to report the failure", runErr)
	}
}
