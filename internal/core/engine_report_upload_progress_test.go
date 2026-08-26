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

// TestReportUploadProgress covers the transient-versus-terminal split the
// runStore polling-merge guard relies on: "in_progress" updates only the
// progress fraction, while terminal statuses persist through UpdateState so the
// pipeline's InputFiles skip branch preserves them.
func TestReportUploadProgress(t *testing.T) {
	tests := []struct {
		name string
		// ensureRow false skips EnsureSingleJobState, leaving the manager empty.
		ensureRow bool
		progress  float64
		status    string
		errMsg    string
		// wantRows is the expected number of state rows afterwards.
		wantRows     int
		wantStatus   string
		wantProgress float64
		wantErrMsg   string
	}{
		{
			name:      "in_progress does not persist a status",
			ensureRow: true, progress: 0.42, status: "in_progress",
			wantRows: 1, wantStatus: "pending", wantProgress: 0.42,
		},
		{
			name:      "success persists the terminal status",
			ensureRow: true, progress: 1.0, status: "success",
			wantRows: 1, wantStatus: "success", wantProgress: 1.0,
		},
		{
			name:      "failed carries the error message",
			ensureRow: true, progress: 0.3, status: "failed", errMsg: "network broken",
			wantRows: 1, wantStatus: "failed", wantProgress: 0.3, wantErrMsg: "network broken",
		},
		{
			// Defensive guard: a terminal report for an unknown job must not
			// panic and must not invent a row.
			name:      "terminal without a row invents nothing",
			ensureRow: false, progress: 1.0, status: "success",
			wantRows: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newProgressTestEngine(t)
			if tt.ensureRow {
				e.EnsureSingleJobState("job1")
			}

			ch := e.eventBus.Subscribe(events.EventStateChange)
			e.ReportUploadProgress("job1", tt.progress, tt.status, tt.errMsg)

			if n := len(e.state.GetAllStates()); n != tt.wantRows {
				t.Fatalf("state rows = %d, want %d", n, tt.wantRows)
			}
			if tt.wantRows == 0 {
				return
			}

			js := e.state.GetState(1)
			if js == nil {
				t.Fatal("state row for index 1 missing after EnsureSingleJobState")
			}
			if js.JobName != "job1" {
				t.Errorf("JobName = %q, want %q", js.JobName, "job1")
			}
			if js.UploadStatus != tt.wantStatus {
				t.Errorf("UploadStatus = %q, want %q", js.UploadStatus, tt.wantStatus)
			}
			if js.UploadProgress < tt.wantProgress-0.01 || js.UploadProgress > tt.wantProgress+0.01 {
				t.Errorf("UploadProgress = %v, want ~%v", js.UploadProgress, tt.wantProgress)
			}
			if js.ErrorMessage != tt.wantErrMsg {
				t.Errorf("ErrorMessage = %q, want %q", js.ErrorMessage, tt.wantErrMsg)
			}

			// Every report publishes one StateChangeEvent carrying the reported
			// (not the persisted) status.
			select {
			case evt := <-ch:
				sce, ok := evt.(*events.StateChangeEvent)
				if !ok {
					t.Fatalf("expected *StateChangeEvent, got %T", evt)
				}
				if sce.EventType != events.EventStateChange {
					t.Errorf("EventType = %v, want EventStateChange (BaseEvent missing?)", sce.EventType)
				}
				if sce.Stage != "upload" || sce.NewStatus != tt.status {
					t.Errorf("event stage/status = %q/%q, want upload/%s", sce.Stage, sce.NewStatus, tt.status)
				}
				if sce.UploadProgress < tt.wantProgress-0.01 || sce.UploadProgress > tt.wantProgress+0.01 {
					t.Errorf("event UploadProgress = %v, want ~%v", sce.UploadProgress, tt.wantProgress)
				}
			case <-time.After(time.Second):
				t.Fatal("no StateChangeEvent received")
			}
		})
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
