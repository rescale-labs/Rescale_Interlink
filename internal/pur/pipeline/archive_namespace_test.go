package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// namespaceTestRoot returns a scratch directory with every symlink already
// resolved. macOS hands out /var/... temp directories that resolve to
// /private/var/..., and safeRemoveTar compares a resolved archive path against
// the batch's directory, so an unresolved root would make the comparison fail
// for the wrong reason.
func namespaceTestRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	return resolved
}

// newBatch builds a pipeline the way a CLI run does: over jobs, with a state
// file naming the run.
func newBatch(t *testing.T, jobs []models.JobSpec, stateFile string) *Pipeline {
	t.Helper()

	cfg := &config.Config{
		TarWorkers:     1,
		UploadWorkers:  1,
		JobWorkers:     1,
		TarCompression: "gzip",
	}
	p, err := NewPipeline(cfg, nil, jobs, PipelineOptions{StateFile: stateFile})
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	return p
}

// tarBatch runs one tar worker over the pipeline's own job list and returns the
// archive path each job ended up with, keyed by job name. It mirrors the
// feeder's state handling so a second run over the same state file resumes
// rather than reinitializing.
func tarBatch(t *testing.T, p *Pipeline) map[string]string {
	t.Helper()

	close(p.feederDone)
	for i, job := range p.jobs {
		index := i + 1
		st := p.stateMgr.GetState(index)
		if st == nil {
			st = p.stateMgr.InitializeState(index, job.JobName, job.Directory)
		}
		p.tarQueue <- &workItem{index: index, jobSpec: job, state: st}
	}
	close(p.tarQueue)

	var wg sync.WaitGroup
	wg.Add(1)
	go p.tarWorker(context.Background(), &wg, 0)
	wg.Wait()

	paths := make(map[string]string, len(p.jobs))
	for item := range p.uploadQueue {
		if item.state.TarStatus != "success" {
			t.Fatalf("%s: TarStatus = %q (%s)", item.state.JobName,
				item.state.TarStatus, item.state.ErrorMessage)
		}
		paths[item.state.JobName] = item.state.TarPath
	}
	return paths
}

// TestArchiveNamespace covers the collision two independent batches over one
// file list used to hit: identical archive paths, so one process truncated and
// rewrote the archive the other was uploading, or deleted it after upload
// before the other had opened it.
func TestArchiveNamespace(t *testing.T) {
	t.Run("independent batches over one file list get distinct archives", func(t *testing.T) {
		root := namespaceTestRoot(t)
		deck := filepath.Join(root, "inputs", "case.inp")
		if err := os.MkdirAll(filepath.Dir(deck), 0o755); err != nil {
			t.Fatalf("mkdir inputs: %v", err)
		}
		if err := os.WriteFile(deck, []byte("deck"), 0o644); err != nil {
			t.Fatalf("write deck: %v", err)
		}

		jobs := func() []models.JobSpec {
			return []models.JobSpec{{JobName: "job_1", LocalInputFiles: []string{deck}}}
		}

		first := tarBatch(t, newBatch(t, jobs(), filepath.Join(root, "runA", "state.csv")))
		second := tarBatch(t, newBatch(t, jobs(), filepath.Join(root, "runB", "state.csv")))

		if first["job_1"] == second["job_1"] {
			t.Fatalf("both batches wrote %s, so one can truncate or delete the other's upload",
				first["job_1"])
		}
		for _, path := range []string{first["job_1"], second["job_1"]} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("archive %s missing: %v", path, err)
			}
		}
	})

	t.Run("a resumed batch recomputes the same archive path", func(t *testing.T) {
		root := namespaceTestRoot(t)
		runDir := filepath.Join(root, "Run_1")
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatalf("mkdir run: %v", err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "input.dat"), []byte("in"), 0o644); err != nil {
			t.Fatalf("write input: %v", err)
		}

		stateFile := filepath.Join(root, "state.csv")
		jobs := func() []models.JobSpec {
			return []models.JobSpec{{JobName: "job_1", Directory: runDir}}
		}

		first := tarBatch(t, newBatch(t, jobs(), stateFile))

		// The interrupted run: the archive is gone, so the resumed batch has to
		// recompute its path rather than read a usable one back from state.
		if err := os.Remove(first["job_1"]); err != nil {
			t.Fatalf("remove archive: %v", err)
		}

		resumed := tarBatch(t, newBatch(t, jobs(), stateFile))
		if resumed["job_1"] != first["job_1"] {
			t.Fatalf("resume wrote %s, want the original %s", resumed["job_1"], first["job_1"])
		}
		if _, err := os.Stat(resumed["job_1"]); err != nil {
			t.Errorf("resumed archive %s missing: %v", resumed["job_1"], err)
		}
	})

	t.Run("cleanup removes this batch's archive but not another's", func(t *testing.T) {
		root := namespaceTestRoot(t)
		runDir := filepath.Join(root, "Run_1")
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatalf("mkdir run: %v", err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "input.dat"), []byte("in"), 0o644); err != nil {
			t.Fatalf("write input: %v", err)
		}

		p := newBatch(t, []models.JobSpec{{JobName: "job_1", Directory: runDir}},
			filepath.Join(root, "state.csv"))
		mine := tarBatch(t, p)["job_1"]

		// Another batch's archive, sitting in the jobs' common parent with the
		// name shape the FNV gate accepts. Only the owning batch may delete it.
		foreign := filepath.Join(root, "root_Run_1_deadbeef.tar.gz")
		if err := os.WriteFile(foreign, []byte("theirs"), 0o644); err != nil {
			t.Fatalf("write foreign archive: %v", err)
		}
		if err := p.safeRemoveTar(foreign, "job_1"); err == nil {
			t.Errorf("safeRemoveTar accepted %s, which this batch does not own", foreign)
		}
		if _, err := os.Stat(foreign); err != nil {
			t.Errorf("another batch's archive was deleted: %v", err)
		}

		if err := p.safeRemoveTar(mine, "job_1"); err != nil {
			t.Fatalf("safeRemoveTar refused this batch's own archive %s: %v", mine, err)
		}
		if _, err := os.Stat(mine); !os.IsNotExist(err) {
			t.Errorf("own archive %s still present: %v", mine, err)
		}
	})
}
