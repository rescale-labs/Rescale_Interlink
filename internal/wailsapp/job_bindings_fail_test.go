package wailsapp

import (
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
)

// appWithEngine spins up a minimal App wired to a real core.Engine so
// failSingleJob's state-manager and event-bus paths can be observed.
func appWithEngine(t *testing.T) (*App, *core.Engine) {
	t.Helper()
	eng, err := core.NewEngine(nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	// Give the engine a state manager via StartRun (which allocates one).
	if err := eng.StartRun("test-run", "", 1); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return &App{engine: eng}, eng
}

// TestFailSingleJob_updatesExistingRow — when EnsureSingleJobState has
// already created the index-1 row, failSingleJob must update that row
// in-place rather than writing an Index-0 duplicate.
func TestFailSingleJob_updatesExistingRow(t *testing.T) {
	a, eng := appWithEngine(t)

	eng.EnsureSingleJobState("job1")
	a.failSingleJob("job1", "upload blew up")

	all := eng.GetState().GetAllStates()
	if len(all) != 1 {
		t.Fatalf("len(states) = %d, want 1 (no Index 0 duplicate)", len(all))
	}
	js := all[0]
	if js.Index != 1 {
		t.Errorf("Index = %d, want 1", js.Index)
	}
	if js.JobName != "job1" {
		t.Errorf("JobName = %q, want %q", js.JobName, "job1")
	}
	if js.UploadStatus != "failed" || js.SubmitStatus != "failed" {
		t.Errorf("statuses = upload:%q submit:%q, want both failed", js.UploadStatus, js.SubmitStatus)
	}
	if js.ErrorMessage != "upload blew up" {
		t.Errorf("ErrorMessage = %q, want %q", js.ErrorMessage, "upload blew up")
	}
}

// TestFailSingleJob_preExpansionFallbackCreatesIndex1 — when the row
// doesn't exist yet (pre-expansion failure), fall back to creating it at
// Index 1 so polling still surfaces the failure.
func TestFailSingleJob_preExpansionFallbackCreatesIndex1(t *testing.T) {
	a, eng := appWithEngine(t)

	// No EnsureSingleJobState call.
	a.failSingleJob("ghost", "cannot access path")

	all := eng.GetState().GetAllStates()
	if len(all) != 1 || all[0].Index != 1 || all[0].JobName != "ghost" {
		t.Fatalf("got %+v, want one row at Index 1 for ghost", all)
	}
}

// TestFailSingleJob_publishesOneCompleteEvent — exactly one CompleteEvent
// fires per call, regardless of whether the state row existed before.
func TestFailSingleJob_publishesOneCompleteEvent(t *testing.T) {
	a, eng := appWithEngine(t)

	ch := eng.Events().Subscribe(events.EventComplete)
	eng.EnsureSingleJobState("job1")
	a.failSingleJob("job1", "oops")

	select {
	case evt := <-ch:
		if _, ok := evt.(*events.CompleteEvent); !ok {
			t.Fatalf("expected *CompleteEvent, got %T", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("no CompleteEvent received")
	}

	// Drain briefly to confirm no second event.
	select {
	case evt := <-ch:
		t.Errorf("unexpected second event: %T", evt)
	case <-time.After(50 * time.Millisecond):
		// good
	}
}
