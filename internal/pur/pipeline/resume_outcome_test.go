package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// A resume that creates a job whose creation had failed ends clean: the job is
// created, so the old failure, kept until then, is not its outcome. It kept
// the row failed and failed the run, which is how a batch corrected after a
// refused creation would have looked broken after its resume.
func TestResumeOfAFailedCreationEndsClean(t *testing.T) {
	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(root, "state.csv")
	spec := uploadedJobSpec(runDir) // create-only
	seedReadyToCreate(t, stateFile, filepath.Join(root, "job_1.tar.gz"), spec, "failed",
		"create job failed: status 400: refused")

	var mu sync.Mutex
	var creates int
	server := answeringServer(&creates, &mu)
	defer server.Close()

	_, _, err := runBatch(t, []models.JobSpec{spec}, stateFile, server.URL)
	st := stateOf(t, stateFile, 1)
	if err != nil || creates != 1 || st.JobID != "job-abc" || st.SubmitStatus != "skipped" || st.ErrorMessage != "" {
		t.Errorf("resume returned %v after %d create(s), leaving job %q %s %q; want the job created, "+
			"recorded as created only, and no failure", err, creates, st.JobID, st.SubmitStatus, st.ErrorMessage)
	}
}

// A resume cancelled before it creates that job again leaves the failure and
// its reason as they were: nothing has replaced them yet.
func TestCancelledResumeKeepsAFailedCreationsReason(t *testing.T) {
	root := namespaceTestRoot(t)
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(root, "state.csv")
	spec := uploadedJobSpec(runDir)
	const reason = "create job failed: status 400: refused"
	seedReadyToCreate(t, stateFile, filepath.Join(root, "job_1.tar.gz"), spec, "failed", reason)

	// Version resolution held keeps the job worker from the create request.
	p := newPipelineWith(t, []models.JobSpec{spec}, PipelineOptions{StateFile: stateFile})
	held := &heldResolver{release: make(chan struct{})}
	p.analysisResolver = held
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.Run(ctx)
	close(held.release)
	<-p.versionsResolved
	if st := stateOf(t, stateFile, 1); err != nil || st.SubmitStatus != "failed" || st.ErrorMessage != reason {
		t.Errorf("a resume cancelled before the creation returned %v, leaving %s %q; want the failure kept with its reason",
			err, st.SubmitStatus, st.ErrorMessage)
	}
}

// Run returns once every job's state is written: a cancel stops the workers at
// once, but the feeder may still be routing a job, and a CLI that exits as
// Run returns left that write half done. The feeder is held here until the
// workers have stopped; from then on nothing else can keep Run from returning.
func TestRunWaitsForTheFeeder(t *testing.T) {
	// A job with no inputs at all is refused by the feeder itself, whose log
	// line is where the feeder is held.
	bare := remoteInputJobSpec()
	bare.InputFiles = nil
	p := newPipelineWith(t, []models.JobSpec{bare}, PipelineOptions{StateFile: filepath.Join(t.TempDir(), "state.csv")})
	p.analysisResolver = &mockAnalysisResolver{}
	p.jobWorkers = 0 // the tar and upload workers count themselves out as they stop
	held, release := make(chan struct{}), make(chan struct{})
	p.SetLogCallback(func(_, message, _, _ string) {
		if strings.HasPrefix(message, "REJECTED") {
			close(held)
			<-release
		}
	})
	stopped := func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.activeWorkers["tar_finished"] == p.tarWorkers && p.activeWorkers["upload_finished"] == p.uploadWorkers
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	returned := make(chan error, 1)
	go func() { returned <- p.Run(ctx) }()
	<-held
	for deadline := time.Now().Add(10 * time.Second); !stopped(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("the workers did not stop on the cancel")
		}
	}
	select {
	case <-returned:
		close(release)
		t.Fatal("Run returned while the feeder was still routing a job")
	case <-time.After(time.Second): // only a guard: Run cannot return while the feeder is held
	}
	close(release)
	select {
	case err := <-returned:
		if err != nil {
			t.Errorf("a cancelled run returned %v, want no failure", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return once the feeder was released")
	}
}
