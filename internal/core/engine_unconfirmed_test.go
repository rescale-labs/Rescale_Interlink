package core

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/pur/state"
)

// seedRun starts a run holding one job per given SubmitStatus, each with tar
// and upload already successful — the shape an unconfirmed creation leaves
// behind.
func seedRun(t *testing.T, e *Engine, submitStatuses ...string) {
	t.Helper()

	stateFile := filepath.Join(t.TempDir(), "state.csv")
	if err := e.StartRun("run_unconfirmed", stateFile, len(submitStatuses)); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	st := e.GetState()
	for i, submitStatus := range submitStatuses {
		js := st.InitializeState(i+1, fmt.Sprintf("job_%d", i+1), "")
		js.TarStatus = "completed"
		js.UploadStatus = "completed"
		js.SubmitStatus = submitStatus
		if submitStatus == state.SubmitStatusIndeterminate {
			js.ErrorMessage = "job may exist: the platform accepted the request but the answer was lost"
		}
		if err := st.UpdateState(js); err != nil {
			t.Fatalf("UpdateState: %v", err)
		}
	}
}

// A batch whose creations the platform never confirmed is neither done nor
// merely waiting: counting it as pending is what let the GUI finalize it as a
// clean completion.
func TestGetRunStatsCountsUnconfirmedCreationsApartFromPending(t *testing.T) {
	e, _ := NewEngine(nil)
	seedRun(t, e, "completed", state.SubmitStatusIndeterminate, "creating")

	total, completed, failed, pending, unconfirmed := e.GetRunStats()

	if total != 3 || completed != 1 || failed != 0 {
		t.Errorf("total/completed/failed = %d/%d/%d, want 3/1/0", total, completed, failed)
	}
	if pending != 0 {
		t.Errorf("pending = %d, want 0: unconfirmed creations must not be counted as pending", pending)
	}
	if unconfirmed != 2 {
		t.Errorf("unconfirmed = %d, want 2 (one indeterminate, one still creating)", unconfirmed)
	}
}

// An ordinary batch must keep the counts it had.
func TestGetRunStatsLeavesAnOrdinaryRunUnchanged(t *testing.T) {
	e, _ := NewEngine(nil)
	seedRun(t, e, "completed", "skipped", "failed", "pending")

	total, completed, failed, pending, unconfirmed := e.GetRunStats()

	if total != 4 || completed != 2 || failed != 1 || pending != 1 || unconfirmed != 0 {
		t.Errorf("total/completed/failed/pending/unconfirmed = %d/%d/%d/%d/%d, want 4/2/1/1/0",
			total, completed, failed, pending, unconfirmed)
	}
}

// The completion event is what both frontend finalization paths read. It has to
// account for every job, or a run with unconfirmed creations reads as a
// completion with nothing wrong.
func TestCompleteEventCarriesUnconfirmedCreations(t *testing.T) {
	e, _ := NewEngine(nil)
	seedRun(t, e, state.SubmitStatusIndeterminate, state.SubmitStatusIndeterminate, "creating")

	ch := e.Events().Subscribe(events.EventComplete)
	e.publishComplete(time.Second)

	select {
	case ev := <-ch:
		complete, ok := ev.(*events.CompleteEvent)
		if !ok {
			t.Fatalf("got %T, want *events.CompleteEvent", ev)
		}
		if complete.FailedJobs != 0 {
			t.Errorf("FailedJobs = %d, want 0", complete.FailedJobs)
		}
		if complete.UnconfirmedJobs != 3 {
			t.Errorf("UnconfirmedJobs = %d, want 3", complete.UnconfirmedJobs)
		}
		if complete.SuccessJobs+complete.FailedJobs+complete.UnconfirmedJobs != complete.TotalJobs {
			t.Errorf("completion event reports %d succeeded, %d failed and %d unconfirmed of %d jobs: some are unaccounted for",
				complete.SuccessJobs, complete.FailedJobs, complete.UnconfirmedJobs, complete.TotalJobs)
		}
	case <-time.After(time.Second):
		t.Fatal("no completion event")
	}
}

// A "creating" row that carries a job ID is an ordinary created job: the
// platform named it, so there is nothing for anyone to reconcile. The pipeline
// and the CLI have always read it that way — state.MayAlreadyExist excludes a
// known ID — and a classification here that looked only at the status called
// the same record unconfirmed, telling the user to go looking for a job the run
// can already point at.
func TestGetRunStatsExcludesCreatingRowsWithAKnownJobID(t *testing.T) {
	e, _ := NewEngine(nil)
	seedRun(t, e, state.SubmitStatusCreating, state.SubmitStatusCreating)

	// The first job's creation was answered.
	st := e.GetState()
	js := st.GetState(1)
	js.JobID = "job-abc"
	if err := st.UpdateState(js); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	total, completed, failed, pending, unconfirmed := e.GetRunStats()

	if unconfirmed != 1 {
		t.Errorf("unconfirmed = %d, want 1: only the row with no job ID is one to check the platform for", unconfirmed)
	}
	// The named job is work this run has not finished, which is what the CLI's
	// own resume analysis calls it.
	if total != 2 || completed != 0 || failed != 0 || pending != 1 {
		t.Errorf("total/completed/failed/pending = %d/%d/%d/%d, want 2/0/0/1",
			total, completed, failed, pending)
	}
}
