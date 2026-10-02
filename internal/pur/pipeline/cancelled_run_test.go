package pipeline

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/state"
)

// A run the user cancels is not reported as a failure, and its end-of-run line
// says it was cancelled, not completed, counting the batch's finished jobs the
// way the CLI does: one a previous run finished counts.
func TestCancelledRunSaysSo(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.csv")
	jobs := []models.JobSpec{remoteInputJobSpec(), remoteInputJobSpec()}
	jobs[1].JobName = "job_2"
	mgr := state.NewManager(stateFile)
	st := mgr.InitializeState(1, jobs[0].JobName, "")
	st.TarStatus, st.UploadStatus, st.JobID, st.SubmitStatus = "skipped", "skipped", "job-1", "skipped"
	if err := mgr.UpdateState(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		nethttp.Error(w, `{"detail": "refused"}`, nethttp.StatusBadRequest)
	}))
	defer server.Close()
	p := newPipelineWith(t, jobs, PipelineOptions{StateFile: stateFile})
	p.apiClient = api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
	p.analysisResolver = &mockAnalysisResolver{}
	var mu sync.Mutex
	var lines []string
	p.SetLogCallback(func(level, message, _, _ string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, level+" "+message)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Run(ctx); err != nil {
		t.Errorf("a cancelled run returned %v, want no failure", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !containsAll(lines, "INFO Pipeline cancelled: 1/2 jobs finished in ") || containsAll(lines, "Pipeline completed") {
		t.Errorf("a cancelled run logged\n%s\nwant it to say it was cancelled, with 1 of 2 jobs finished",
			strings.Join(lines, "\n"))
	}
	if finished, total := p.FinishedJobs(); finished != 1 || total != 2 {
		t.Errorf("FinishedJobs = %d, %d; want 1, 2", finished, total)
	}
}
