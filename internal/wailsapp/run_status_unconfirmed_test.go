package wailsapp

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/pur/state"
)

// appWithFinishedRun returns an App whose engine holds a finished run — no run
// context, one job state per given SubmitStatus — which is the state the GUI's
// polling fallback reads when the completion event never arrives.
func appWithFinishedRun(t *testing.T, submitStatuses ...string) *App {
	t.Helper()

	eng, err := core.NewEngine(nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	stateFile := filepath.Join(t.TempDir(), "state.csv")
	if err := eng.StartRun("run_unconfirmed", stateFile, len(submitStatuses)); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	st := eng.GetState()
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
	eng.EndRun()

	return &App{engine: eng}
}

// The polling fallback finalizes the run in the GUI whenever it sees a terminal
// state. Reporting "idle" for unresolved creations is what the run store then
// recorded as a clean completion.
func TestGetRunStatusReportsUnconfirmedCreations(t *testing.T) {
	a := appWithFinishedRun(t, "completed", state.SubmitStatusIndeterminate, "creating")

	status := a.GetRunStatus()

	if status.State != "unconfirmed" {
		t.Errorf("State = %q, want %q", status.State, "unconfirmed")
	}
	if status.SuccessJobs != 1 || status.FailedJobs != 0 {
		t.Errorf("SuccessJobs/FailedJobs = %d/%d, want 1/0", status.SuccessJobs, status.FailedJobs)
	}
}

// A run that really did finish cleanly must be unaffected.
func TestGetRunStatusStillCompletesAnOrdinaryRun(t *testing.T) {
	a := appWithFinishedRun(t, "completed", "success", "skipped")

	status := a.GetRunStatus()

	if status.State != "completed" {
		t.Errorf("State = %q, want %q", status.State, "completed")
	}
}

// The failure text a failed run reports must be a failure's, not the ambiguity
// message an unconfirmed creation carries in the same column.
func TestGetRunStatusReportsAFailuresOwnError(t *testing.T) {
	a := appWithFinishedRun(t, state.SubmitStatusIndeterminate, "failed")
	st := a.engine.GetState()
	failed := st.GetState(2)
	failed.ErrorMessage = "create rejected: analysis code unknown"
	if err := st.UpdateState(failed); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	status := a.GetRunStatus()

	if status.State != "failed" {
		t.Errorf("State = %q, want %q", status.State, "failed")
	}
	if status.Error != "create rejected: analysis code unknown" {
		t.Errorf("Error = %q, want the failed job's own message", status.Error)
	}
}
