package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// TestConcurrentTransitionsDoNotRaceWithCheckpoints reproduces F16 under -race:
// one worker changes a job's fields while another worker's checkpoint
// serializes every job under the manager's lock. The manager's lock cannot
// cover a state object it has handed out, so the write and the checkpoint's
// read of the same field are unsynchronized.
//
// Without -race this is a liveness check; the failure it exists for is the race
// report.
func TestConcurrentTransitionsDoNotRaceWithCheckpoints(t *testing.T) {
	mgr := NewManager(filepath.Join(t.TempDir(), "state.csv"))

	const jobs = 4
	for i := 1; i <= jobs; i++ {
		mgr.InitializeState(i, fmt.Sprintf("job_%d", i), "/runs")
	}

	var wg sync.WaitGroup
	for i := 1; i <= jobs; i++ {
		index := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				// The upload worker's sequence: take the job's state, record
				// what the upload produced, then checkpoint it.
				st := mgr.GetState(index)
				if st == nil {
					t.Errorf("job %d has no state", index)
					return
				}
				st.FileID = fmt.Sprintf("file-%d-%d", index, n)
				st.UploadStatus = "success"
				if err := mgr.UpdateState(st); err != nil {
					t.Errorf("UpdateState: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	// Every job's last transition survived, and the file the checkpoints wrote
	// is readable.
	reloaded := NewManager(mgr.FilePath())
	if err := reloaded.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	for i := 1; i <= jobs; i++ {
		st := reloaded.GetState(i)
		if st == nil {
			t.Fatalf("job %d missing from the reloaded state file", i)
		}
		if st.UploadStatus != "success" {
			t.Errorf("job %d: UploadStatus = %q, want success", i, st.UploadStatus)
		}
	}
}

// TestUpdateStateKeepsLiveUploadProgress guards the one field snapshots would
// otherwise roll back: the pipeline reports progress through the manager while
// holding a snapshot taken before the transfer began, so checkpointing that
// snapshot must not reset what the transfer has reported since.
func TestUpdateStateKeepsLiveUploadProgress(t *testing.T) {
	mgr := NewManager(filepath.Join(t.TempDir(), "state.csv"))
	snapshot := mgr.InitializeState(1, "job_1", "/runs/Run_1")

	mgr.UpdateUploadProgressByName("job_1", 0.6)

	snapshot.UploadStatus = "success"
	if err := mgr.UpdateState(snapshot); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	if got := mgr.GetState(1).UploadProgress; got != 0.6 {
		t.Errorf("UploadProgress = %v, want the 0.6 the transfer reported", got)
	}

	// A caller with something to say about progress still sets it.
	snapshot.UploadProgress = 1
	if err := mgr.UpdateState(snapshot); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if got := mgr.GetState(1).UploadProgress; got != 1 {
		t.Errorf("UploadProgress = %v, want 1", got)
	}
}

// TestHandedOutStateDoesNotReachTheManager pins the other half of F16: a state
// object the manager gave out is the caller's own. Changing it must not alter
// what a checkpoint would write, since the manager cannot lock around a change
// it never sees.
func TestHandedOutStateDoesNotReachTheManager(t *testing.T) {
	mgr := NewManager(filepath.Join(t.TempDir(), "state.csv"))
	mgr.InitializeState(1, "job_1", "/runs/Run_1")

	for name, taken := range map[string]func() *models.JobState{
		"GetState":        func() *models.JobState { return mgr.GetState(1) },
		"GetAllStates":    func() *models.JobState { return mgr.GetAllStates()[0] },
		"InitializeState": func() *models.JobState { return mgr.InitializeState(2, "job_2", "/runs/Run_2") },
	} {
		t.Run(name, func(t *testing.T) {
			st := taken()
			index := st.Index
			st.JobID = "leaked"

			if stored := mgr.GetState(index); stored.JobID != "" {
				t.Errorf("job %d: JobID = %q in the manager, want it unchanged until UpdateState",
					index, stored.JobID)
			}
		})
	}
}

// TestStateWrittenBeforeIndeterminateLoadsUnchanged pins the compatibility
// claim behind SubmitStatusIndeterminate: it is another value in a column that
// already exists, so a state file an earlier binary wrote reads back exactly as
// it did. The fixture is literal for that reason — a file this test constructed
// through the manager would only prove the manager agrees with itself.
func TestStateWrittenBeforeIndeterminateLoadsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.csv")
	fixture := "Index,JobName,Directory,TarPath,TarStatus,FileID,UploadStatus,JobID,SubmitStatus,ExtraFileIDs,ErrorMessage,LastUpdated\n" +
		"1,job_1,/runs/Run_1,/runs/job_1.tar.gz,success,file-1,success,job-abc,success,extra-1,,2026-01-02T03:04:05Z\n" +
		"2,job_2,/runs/Run_2,,pending,,pending,,failed,,tar failed,2026-01-02T03:04:06Z\n"
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	mgr := NewManager(path)
	if err := mgr.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := map[int]models.JobState{
		1: {Index: 1, JobName: "job_1", Directory: "/runs/Run_1", TarPath: "/runs/job_1.tar.gz",
			TarStatus: "success", FileID: "file-1", UploadStatus: "success", JobID: "job-abc",
			SubmitStatus: "success", ExtraFileIDs: "extra-1",
			LastUpdated: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		2: {Index: 2, JobName: "job_2", Directory: "/runs/Run_2", TarStatus: "pending",
			UploadStatus: "pending", SubmitStatus: "failed", ErrorMessage: "tar failed",
			LastUpdated: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
	}
	for index, expected := range want {
		got := mgr.GetState(index)
		if got == nil {
			t.Fatalf("job %d missing after load", index)
		}
		if !got.LastUpdated.Equal(expected.LastUpdated) {
			t.Errorf("job %d LastUpdated = %s, want %s", index, got.LastUpdated, expected.LastUpdated)
		}
		got.LastUpdated = expected.LastUpdated
		if *got != expected {
			t.Errorf("job %d loaded as %+v, want %+v", index, *got, expected)
		}
	}
}

// TestUnconfirmedStatusesSurviveTheStateFile is the other half: both values a
// job's creation can be left at are written and read back like any other,
// without changing the columns a state file has.
func TestUnconfirmedStatusesSurviveTheStateFile(t *testing.T) {
	for _, status := range []string{SubmitStatusIndeterminate, SubmitStatusCreating} {
		t.Run(status, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.csv")
			mgr := NewManager(path)
			st := mgr.InitializeState(1, "job_1", "/runs/Run_1")
			st.TarStatus = "success"
			st.UploadStatus = "success"
			st.SubmitStatus = status
			st.ErrorMessage = "job may have been created"
			if err := mgr.UpdateState(st); err != nil {
				t.Fatalf("UpdateState: %v", err)
			}

			written, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read state: %v", err)
			}
			header := strings.SplitN(string(written), "\n", 2)[0]
			if got := len(strings.Split(header, ",")); got != 12 {
				t.Errorf("state file header has %d columns, want the unchanged 12: %s", got, header)
			}

			reread := NewManager(path)
			if err := reread.Load(); err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := reread.GetState(1)
			if got == nil {
				t.Fatal("job 1 missing after reload")
			}
			if got.SubmitStatus != status {
				t.Errorf("SubmitStatus = %q, want %q", got.SubmitStatus, status)
			}
			if got.ErrorMessage != "job may have been created" {
				t.Errorf("ErrorMessage = %q, want it preserved", got.ErrorMessage)
			}
			if got.JobID != "" {
				t.Errorf("JobID = %q, want empty", got.JobID)
			}
			if !MayAlreadyExist(got) {
				t.Errorf("a job reloaded at %q is not treated as possibly created", status)
			}
		})
	}
}

// TestMayAlreadyExist pins what the pipeline and the CLI both ask of a loaded
// state: a creation that went out, or was recorded as going out, and never came
// back with a job ID. Answering yes to anything else would skip work a resume
// owes; answering no to either would create a job the platform may be running.
func TestMayAlreadyExist(t *testing.T) {
	for name, tc := range map[string]struct {
		st   *models.JobState
		want bool
	}{
		"sent, never answered":  {&models.JobState{SubmitStatus: SubmitStatusIndeterminate}, true},
		"recorded as going out": {&models.JobState{SubmitStatus: SubmitStatusCreating}, true},
		"named by the platform": {&models.JobState{SubmitStatus: SubmitStatusCreating, JobID: "job-abc"}, false},
		"answered late":         {&models.JobState{SubmitStatus: SubmitStatusIndeterminate, JobID: "job-abc"}, false},
		"not attempted":         {&models.JobState{SubmitStatus: "pending"}, false},
		"failed outright":       {&models.JobState{SubmitStatus: "failed"}, false},
		"created and submitted": {&models.JobState{SubmitStatus: "success", JobID: "job-abc"}, false},
		"absent":                {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := MayAlreadyExist(tc.st); got != tc.want {
				t.Errorf("MayAlreadyExist() = %v, want %v", got, tc.want)
			}
		})
	}
}
