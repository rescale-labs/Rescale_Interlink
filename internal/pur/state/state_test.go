package state

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

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
