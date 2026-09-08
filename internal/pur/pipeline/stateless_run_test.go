package pipeline

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// runStatelessBatch runs a whole pipeline with no state file, the way
// `pur run --jobs-csv jobs.csv` does when the user gives no --state. The
// uploader is stubbed and job creation goes to a local test server, so the run
// reaches nothing outside the process.
func runStatelessBatch(t *testing.T, jobs []models.JobSpec, apiURL string) (*Pipeline, error) {
	t.Helper()

	cfg := &config.Config{TarWorkers: 1, UploadWorkers: 1, JobWorkers: 1, TarCompression: "gzip"}
	p, err := NewPipeline(cfg, nil, jobs, PipelineOptions{StateFile: ""})
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	p.apiClient = api.NewClientForTest(&config.Config{APIBaseURL: apiURL, APIKey: "test"})
	p.analysisResolver = &mockAnalysisResolver{}
	p.SetSyncUploader(&stubUploader{})

	return p, p.Run(context.Background())
}

// TestStatelessRunCompletes is the regression a run without --state hit: the
// state manager had an empty path to persist to, so every checkpoint failed,
// and the hardening that stops a job whose state could not be recorded failed
// each one at the first checkpoint after its upload. The batch archived and
// uploaded everything and then reported every job failed.
//
// Nothing about the checkpoint ordering changes here — the run simply has no
// file to write, so its record is the in-memory map, and the checkpoints
// succeed against it.
func TestStatelessRunCompletes(t *testing.T) {
	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "input.dat"), []byte("in"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	// The directory the empty state path used to resolve against, so a run that
	// still wrote there is caught rather than merely reported.
	work := t.TempDir()
	t.Chdir(work)

	var mu sync.Mutex
	var created int
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if !isCreate(r) {
			w.WriteHeader(nethttp.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		mu.Lock()
		created++
		mu.Unlock()
		w.WriteHeader(nethttp.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"job-abc"}`))
	}))
	defer server.Close()

	// Create-only: what is under test is the tar → upload → create sequence and
	// the checkpoints between its stages.
	p, runErr := runStatelessBatch(t, []models.JobSpec{uploadedJobSpec(runDir)}, server.URL)
	if runErr != nil {
		t.Fatalf("run without --state failed: %v", runErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if created != 1 {
		t.Errorf("job created %d times, want 1", created)
	}
	if failed := p.countFailedJobs(); failed != 0 {
		t.Errorf("run counts %d failed job(s), want 0", failed)
	}
	if p.completedJobs != 1 {
		t.Errorf("run counted %d job(s) done, want 1", p.completedJobs)
	}

	st := p.stateMgr.GetState(1)
	if st == nil {
		t.Fatal("job 1 has no state")
	}
	if st.TarStatus != "success" || st.UploadStatus != "success" {
		t.Errorf("TarStatus = %q, UploadStatus = %q, want both success (%s)",
			st.TarStatus, st.UploadStatus, st.ErrorMessage)
	}
	if st.JobID != "job-abc" {
		t.Errorf("JobID = %q, want the created job", st.JobID)
	}
	if st.SubmitStatus != "skipped" {
		t.Errorf("SubmitStatus = %q, want skipped for a create-only job (%s)",
			st.SubmitStatus, st.ErrorMessage)
	}

	// The run has no file to write, so it writes none: the empty path must not
	// resolve to a temp file in whatever directory the user started from.
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatalf("read working directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("a run with no state file wrote %v into the working directory", names)
	}
}

// TestStatelessRunsGetDistinctArchives keeps the seed archiveNamespace falls
// back on when there is no state file to hash: FilePath() still answers "" for
// an in-memory manager, so the pid-and-clock seed is still what names the
// directory, and two stateless batches over one file list do not write one
// archive.
func TestStatelessRunsGetDistinctArchives(t *testing.T) {
	root := namespaceTestRoot(t)
	deck := filepath.Join(root, "inputs", "case.inp")
	if err := os.MkdirAll(filepath.Dir(deck), 0o755); err != nil {
		t.Fatalf("mkdir inputs: %v", err)
	}
	if err := os.WriteFile(deck, []byte("deck"), 0o644); err != nil {
		t.Fatalf("write deck: %v", err)
	}

	jobs := func() []models.JobSpec {
		return []models.JobSpec{{JobName: "job_1", LocalInputFiles: []string{deck}}}
	}

	first := newBatch(t, jobs(), "")
	if got := first.stateMgr.FilePath(); got != "" {
		t.Fatalf("FilePath() = %q, want empty: the namespace seed depends on it", got)
	}

	firstPaths := tarBatch(t, first)
	secondPaths := tarBatch(t, newBatch(t, jobs(), ""))

	if firstPaths["job_1"] == secondPaths["job_1"] {
		t.Fatalf("both stateless batches wrote %s, so one can truncate or delete the other's upload",
			firstPaths["job_1"])
	}
	for _, path := range []string{firstPaths["job_1"], secondPaths["job_1"]} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("archive %s missing: %v", path, err)
		}
	}
}
