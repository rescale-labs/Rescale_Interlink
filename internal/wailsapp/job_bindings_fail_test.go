package wailsapp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/reporting"
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
	a.failSingleJob("job1", errors.New("upload blew up"))

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
	a.failSingleJob("ghost", errors.New("cannot access path"))

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
	a.failSingleJob("job1", errors.New("oops"))

	select {
	case evt := <-ch:
		if _, ok := evt.(*events.CompleteEvent); !ok {
			t.Fatalf("expected *CompleteEvent, got %T", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("no CompleteEvent received")
	}

	// failSingleJob publishes synchronously, so a second event would be queued.
	select {
	case evt := <-ch:
		t.Errorf("unexpected second event: %T", evt)
	default:
	}
}

// An upload that another transfer's lock refused fails the job, but it is the
// user's to act on, so the GUI offers no error report for it.
func TestFailSingleJob_lockRefusalIsNotReported(t *testing.T) {
	a, eng := appWithEngine(t)
	a.reporter = reporting.NewReporter(eng.Events())
	ch := eng.Events().Subscribe(events.EventReportableError)

	refused := fmt.Errorf("S3Storage upload failed: failed to acquire upload lock: %w", state.ErrUploadLocked)
	a.failSingleJob("job1", fmt.Errorf("Upload failed: %w", refused))

	select { // Report publishes synchronously
	case event := <-ch:
		t.Fatalf("reported a lock refusal: %s", event.(*events.ReportableErrorEvent).ErrorMessage)
	default:
	}
}

// A single job whose selected folders hold no file fails, and the refusal is
// the user's to act on, so the GUI offers no error report for it.
func TestStartSingleJob_anEmptySelectionIsNotReported(t *testing.T) {
	setIsolatedUserConfigEnv(t)
	eng, err := core.NewEngine(nil)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{engine: eng, reporter: reporting.NewReporter(eng.Events())}
	ch := eng.Events().Subscribe(events.EventReportableError)

	if _, err := a.StartSingleJob(SingleJobInputDTO{InputMode: "localFiles", LocalFiles: []string{t.TempDir()}, Job: JobSpecDTO{JobName: "job1"}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); eng.IsRunActive(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the run did not end")
		}
	}
	if states := eng.GetState().GetAllStates(); len(states) != 1 || states[0].ErrorMessage != "No files found in the selected paths" {
		t.Fatalf("states %+v, want the job failed for its empty selection", states)
	}
	select {
	case event := <-ch:
		t.Errorf("reported the refusal: %s", event.(*events.ReportableErrorEvent).ErrorMessage)
	default:
	}
}

// A GUI run's state file records each job's error, so its directories are the
// user's alone, as the ones the PUR state manager creates are.
func TestRunStateDirectoryIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not apply on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	generateStateFilePath("run")
	for _, dir := range []string{"", "states"} {
		info, err := os.Stat(filepath.Join(home, ".rescale-int", dir))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("%s has mode %v, want 0700", info.Name(), info.Mode().Perm())
		}
	}
}
