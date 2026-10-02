package pipeline

import (
	"context"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// submitPlatform creates every job as job-abc and answers each submit with
// status, counting the submits. While cancel is set, the next submit runs it
// instead, as a Ctrl-C landing mid-request does, and is held until the client
// gives up on it.
type submitPlatform struct {
	status int

	mu      sync.Mutex
	cancel  context.CancelFunc
	submits int
}

func (f *submitPlatform) start(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_, _ = io.ReadAll(r.Body) // until it is read, the server does not see the client hang up
		switch {
		case isCreate(r):
			w.WriteHeader(nethttp.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"job-abc"}`))
			return
		case !strings.HasSuffix(r.URL.Path, "/submit/"):
			nethttp.Error(w, `{"detail": "refused"}`, nethttp.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.submits++
		cancel := f.cancel
		f.cancel = nil
		f.mu.Unlock()
		if cancel == nil {
			w.WriteHeader(f.status)
			return
		}
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second): // a watchdog: the submit then succeeds, which fails the test
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// seedSubmitDue writes the state of a job to submit whose archive is built and
// uploaded, so a run creates it and submits it.
func seedSubmitDue(t *testing.T) (string, models.JobSpec) {
	t.Helper()
	root := namespaceTestRoot(t)
	spec := uploadedJobSpec(filepath.Join(root, "Run_1"))
	spec.SubmitMode = "submit"
	stateFile := filepath.Join(root, "state.csv")
	seedReadyToCreate(t, stateFile, filepath.Join(root, "job_1.tar.gz"), spec, "pending", "")
	return stateFile, spec
}

// A cancel that cuts off a job's submit says nothing of the job: it stays
// pending, failed nowhere, and the next resume submits it. Recorded as a
// failed submit, a job with an archive of its own was never submitted by a
// resume, and every resume after the cancel failed.
func TestCancelledSubmitStaysPending(t *testing.T) {
	stateFile, spec := seedSubmitDue(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	platform := &submitPlatform{status: nethttp.StatusOK, cancel: cancel}
	url := platform.start(t)

	p := newPipelineWith(t, []models.JobSpec{spec}, PipelineOptions{StateFile: stateFile})
	p.apiClient = api.NewClientForTest(&config.Config{APIBaseURL: url, APIKey: "test"})
	p.analysisResolver = &mockAnalysisResolver{}
	var mu sync.Mutex
	var shown []string
	p.SetStateChangeCallback(func(_, stage, status, _, _ string, _ float64) {
		mu.Lock()
		defer mu.Unlock()
		if stage == "submit" {
			shown = append(shown, status)
		}
	})
	if err := p.Run(ctx); err != nil {
		t.Fatalf("the cancelled run returned %v, want no failure", err)
	}
	mu.Lock()
	submitShown := strings.Join(shown, " ")
	mu.Unlock()
	st := stateOf(t, stateFile, 1)
	finished, _ := p.FinishedJobs()
	if st.JobID != "job-abc" || st.SubmitStatus != "pending" || st.ErrorMessage != "" ||
		ResumeStage(st, spec, false, false) != StageSubmit || len(p.FailedJobs()) != 0 || finished != 0 ||
		submitShown != "in_progress pending" {
		t.Errorf("a cancel during the submit left job %q %s %q, %d failed and %d finished, its submit shown %q; "+
			"want it pending with no error, failed nowhere, not finished, and shown pending again",
			st.JobID, st.SubmitStatus, st.ErrorMessage, len(p.FailedJobs()), finished, submitShown)
	}

	platform.mu.Lock()
	cutOff := platform.submits
	platform.mu.Unlock()
	_, _, err := runBatch(t, []models.JobSpec{spec}, stateFile, url)
	st = stateOf(t, stateFile, 1)
	platform.mu.Lock()
	resubmits := platform.submits - cutOff
	platform.mu.Unlock()
	if err != nil || resubmits != 1 || st.SubmitStatus != "success" {
		t.Errorf("the resume returned %v after %d submit(s), leaving %s; want the job submitted once",
			err, resubmits, st.SubmitStatus)
	}
}

// A submit the platform refuses, with no cancel, is the job's outcome: it is
// recorded as failed, and a resume does not submit it again.
func TestRefusedSubmitStaysFailed(t *testing.T) {
	stateFile, spec := seedSubmitDue(t)
	url := (&submitPlatform{status: nethttp.StatusBadRequest}).start(t)

	_, _, err := runBatch(t, []models.JobSpec{spec}, stateFile, url)
	st := stateOf(t, stateFile, 1)
	if err == nil || st.SubmitStatus != "failed" || !strings.Contains(st.ErrorMessage, "status 400") ||
		ResumeStage(st, spec, false, false) != StageSubmitFailed {
		t.Errorf("a refused submit returned %v, leaving %s %q; want the run failed and the submit recorded as failed",
			err, st.SubmitStatus, st.ErrorMessage)
	}
}
