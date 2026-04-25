package core

import (
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/pur/state"
)

// newProgressTestEngine returns an Engine backed by a real state.Manager
// and EventBus. No api.Client, so nothing here hits the network.
func newProgressTestEngine(t *testing.T) *Engine {
	t.Helper()
	e := &Engine{
		eventBus:      events.NewEventBus(100),
		state:         state.NewManager(""),
		publishEvents: true,
	}
	return e
}

// TestReportUploadProgress_inProgressUpdatesTransientOnly — first and
// subsequent "in_progress" calls must not change UploadStatus (stays at
// the initialized "pending"); only UploadProgress is updated. This is the
// contract the runStore polling-merge guard relies on.
func TestReportUploadProgress_inProgressUpdatesTransientOnly(t *testing.T) {
	e := newProgressTestEngine(t)
	e.EnsureSingleJobState("job1")

	ch := e.eventBus.Subscribe(events.EventStateChange)
	e.ReportUploadProgress("job1", 0.42, "in_progress", "")

	js := e.state.GetState(1)
	if js == nil {
		t.Fatal("state row for index 1 missing after EnsureSingleJobState")
	}
	if js.JobName != "job1" {
		t.Errorf("JobName = %q, want %q", js.JobName, "job1")
	}
	if js.UploadStatus != "pending" {
		t.Errorf("UploadStatus = %q, want %q (in_progress must NOT persist)", js.UploadStatus, "pending")
	}
	if js.UploadProgress < 0.41 || js.UploadProgress > 0.43 {
		t.Errorf("UploadProgress = %v, want ~0.42", js.UploadProgress)
	}

	select {
	case evt := <-ch:
		sce, ok := evt.(*events.StateChangeEvent)
		if !ok {
			t.Fatalf("expected *StateChangeEvent, got %T", evt)
		}
		if sce.EventType != events.EventStateChange {
			t.Errorf("EventType = %v, want EventStateChange (BaseEvent missing?)", sce.EventType)
		}
		if sce.Stage != "upload" || sce.NewStatus != "in_progress" {
			t.Errorf("event stage/status = %q/%q, want upload/in_progress", sce.Stage, sce.NewStatus)
		}
		if sce.UploadProgress < 0.41 || sce.UploadProgress > 0.43 {
			t.Errorf("event UploadProgress = %v, want ~0.42", sce.UploadProgress)
		}
	case <-time.After(time.Second):
		t.Fatal("no StateChangeEvent received")
	}
}

// TestReportUploadProgress_successWritesTerminalStatus — terminal
// "success" persists UploadStatus via UpdateState so the pipeline
// InputFiles skip branch (guarded by nextSkipStatus) preserves it.
func TestReportUploadProgress_successWritesTerminalStatus(t *testing.T) {
	e := newProgressTestEngine(t)
	e.EnsureSingleJobState("job1")

	e.ReportUploadProgress("job1", 1.0, "success", "")

	js := e.state.GetState(1)
	if js == nil || js.UploadStatus != "success" {
		t.Errorf("UploadStatus = %v, want %q", js, "success")
	}
	if js.UploadProgress != 1.0 {
		t.Errorf("UploadProgress = %v, want 1.0", js.UploadProgress)
	}
}

// TestReportUploadProgress_failedCarriesErrorMessage — terminal "failed"
// persists status + ErrorMessage.
func TestReportUploadProgress_failedCarriesErrorMessage(t *testing.T) {
	e := newProgressTestEngine(t)
	e.EnsureSingleJobState("job1")

	e.ReportUploadProgress("job1", 0.3, "failed", "network broken")

	js := e.state.GetState(1)
	if js == nil || js.UploadStatus != "failed" {
		t.Fatalf("UploadStatus = %v, want %q", js, "failed")
	}
	if js.ErrorMessage != "network broken" {
		t.Errorf("ErrorMessage = %q, want %q", js.ErrorMessage, "network broken")
	}
}

// TestEnsureSingleJobState_isIdempotent — two calls produce exactly one
// row at Index 1.
func TestEnsureSingleJobState_isIdempotent(t *testing.T) {
	e := newProgressTestEngine(t)

	e.EnsureSingleJobState("job1")
	e.EnsureSingleJobState("job1")

	all := e.state.GetAllStates()
	if len(all) != 1 {
		t.Fatalf("len(GetAllStates) = %d, want 1", len(all))
	}
	if all[0].Index != 1 || all[0].JobName != "job1" {
		t.Errorf("row = {Index:%d, JobName:%q}, want {Index:1, JobName:\"job1\"}", all[0].Index, all[0].JobName)
	}
}

// TestReportUploadProgress_terminalWithoutRowLogsWarning — calling
// terminal status without a pre-existing row must not panic and must not
// invent a row. Defensive guard against programming errors.
func TestReportUploadProgress_terminalWithoutRowLogsWarning(t *testing.T) {
	e := newProgressTestEngine(t)

	// No EnsureSingleJobState call — state manager is empty.
	e.ReportUploadProgress("ghost", 1.0, "success", "")

	if n := len(e.state.GetAllStates()); n != 0 {
		t.Errorf("state.GetAllStates len = %d, want 0 (terminal without row must not invent one)", n)
	}
}
