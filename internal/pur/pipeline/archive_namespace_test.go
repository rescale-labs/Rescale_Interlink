package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	return newPipelineWith(t, jobs, PipelineOptions{StateFile: stateFile})
}

// newPipelineWith builds a pipeline with one worker per stage over jobs.
func newPipelineWith(t *testing.T, jobs []models.JobSpec, opts PipelineOptions) *Pipeline {
	t.Helper()
	cfg := &config.Config{TarWorkers: 1, UploadWorkers: 1, JobWorkers: 1, TarCompression: "gzip"}
	p, err := NewPipeline(cfg, nil, jobs, opts)
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

// A job with nothing to archive (a sweep over shared file IDs, a submit-existing
// row) has no directory, and Abs("") is the working directory. Staging must not
// follow it to that directory's parent, which is not the run's to write in: "/"
// for a GUI launched from Finder. A batch with no archives stages nothing, even
// a submit-existing one whose rows keep a directory, and a mixed one stages
// beside the inputs it archives. One whose inputs share only a volume root
// stages in a private directory of its own in the user's cache, never in the
// working directory or at the root: the same one for each run of its state
// file, so that a resume can remove an archive an earlier run left, and none
// is left empty by a run that fails early. With no cache location it stages
// in a private temporary directory of its own.
func TestStagingStaysWithTheArchivedInputs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	root := namespaceTestRoot(t)
	shut, work, data := filepath.Join(root, "shut"), filepath.Join(root, "shut", "work"), filepath.Join(root, "data")
	for _, dir := range []string{work, filepath.Join(data, "run1")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.Chmod(shut, 0o500)
	t.Cleanup(func() { os.Chmod(shut, 0o755) })
	t.Chdir(work)
	cache := filepath.Join(root, "cache")
	for _, env := range []string{"HOME", "XDG_CACHE_HOME", "LocalAppData"} {
		t.Setenv(env, cache)
	}
	shared := models.JobSpec{JobName: "shared", ExtraInputFileIDs: "file1"}

	newBatch(t, []models.JobSpec{shared}, filepath.Join(root, "sweep.csv"))
	kept := []models.JobSpec{{JobName: "kept", Directory: filepath.Join(shut, "run1")}}
	if _, err := NewPipeline(&config.Config{}, nil, kept, PipelineOptions{StateFile: filepath.Join(root, "existing.csv"), SkipTarUpload: true}); err != nil {
		t.Errorf("submit-existing over a directory it cannot write beside: %v", err)
	}
	if entries, _ := os.ReadDir(shut); len(entries) != 1 {
		t.Errorf("a batch with nothing to archive left %d entries in %s, want only the working directory", len(entries), shut)
	}
	p := newBatch(t, []models.JobSpec{shared, {JobName: "local", Directory: filepath.Join(data, "run1")}}, filepath.Join(root, "mixed.csv"))
	if got := filepath.Dir(p.tempDir); got != data {
		t.Errorf("a mixed batch stages in %s, want beside its archived inputs in %s", got, data)
	}
	vol := filepath.VolumeName(root) + string(filepath.Separator)
	apart := []models.JobSpec{{JobName: "a", Directory: filepath.Join(vol, "data")}, {JobName: "b", Directory: filepath.Join(vol, "scratch", "run1")}}
	p = newBatch(t, apart, filepath.Join(root, "apart.csv"))
	left := filepath.Join(p.tempDir, "a_deadbeef.tar.gz")
	info, err := os.Stat(p.tempDir)
	if err != nil || !strings.HasPrefix(p.tempDir, cache) || !strings.HasPrefix(filepath.Base(p.tempDir), archiveNamespacePrefix) ||
		runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("a batch sharing only a volume root stages in %s (%v), want a private directory of its own in %s", p.tempDir, err, cache)
	}
	if err := os.WriteFile(left, []byte("tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newBatch(t, apart, filepath.Join(root, "apart.csv")).safeRemoveTar(left, "a"); err != nil {
		t.Errorf("a resumed batch cannot remove the archive its first run left: %v", err)
	}
	planted := filepath.Join(filepath.Dir(p.tempDir), archiveNamespace(filepath.Join(root, "planted.csv")))
	if os.Symlink(data, planted) == nil {
		if _, err := NewPipeline(&config.Config{}, nil, apart, PipelineOptions{StateFile: filepath.Join(root, "planted.csv")}); err == nil {
			t.Error("a batch stages through a link planted where its directory belongs")
		}
	}

	p = newBatch(t, apart, filepath.Join(root, "early.csv"))
	p.commonInputFilesRaw = filepath.Join(root, "missing")
	if err := p.Run(context.Background()); err == nil {
		t.Fatal("a run with a missing shared file succeeded")
	}
	if _, err := os.Stat(p.tempDir); !os.IsNotExist(err) {
		t.Errorf("a run that failed before its workers left %s behind", p.tempDir)
	}

	tmp := filepath.Join(root, "tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	for env, value := range map[string]string{"HOME": "", "XDG_CACHE_HOME": "", "LocalAppData": "", "TMPDIR": tmp, "TMP": tmp} {
		t.Setenv(env, value)
	}
	p = newBatch(t, apart, filepath.Join(root, "nocache.csv"))
	if info, err := os.Stat(p.tempDir); err != nil || filepath.Dir(p.tempDir) != tmp || !strings.HasPrefix(filepath.Base(p.tempDir), archiveNamespacePrefix) ||
		runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("with no cache location a batch stages in %s (%v), want a private directory of its own in %s", p.tempDir, err, tmp)
	}
}
