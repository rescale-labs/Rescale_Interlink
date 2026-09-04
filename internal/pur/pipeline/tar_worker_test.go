package pipeline

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/state"
)

// runTarWorker drives one tarWorker over the given jobs and returns the work
// items it passed on to the upload stage. Built as a literal, like the other
// tests in this package, because tar is the one stage that needs no API client.
func runTarWorker(t *testing.T, cfg *config.Config, tempDir string, jobs []models.JobSpec) []*workItem {
	t.Helper()

	stateMgr := state.NewManager(filepath.Join(t.TempDir(), "state.csv"))

	p := &Pipeline{
		cfg:           cfg,
		stateMgr:      stateMgr,
		jobs:          jobs,
		tempDir:       tempDir,
		tarWorkers:    1,
		tarQueue:      make(chan *workItem, len(jobs)),
		uploadQueue:   make(chan *workItem, len(jobs)),
		feederDone:    make(chan struct{}),
		activeWorkers: make(map[string]int),
	}
	close(p.feederDone)

	for i, job := range jobs {
		p.tarQueue <- &workItem{
			index:   i,
			jobSpec: job,
			state:   stateMgr.InitializeState(i, job.JobName, job.Directory),
		}
	}
	close(p.tarQueue)

	var wg sync.WaitGroup
	wg.Add(1)
	go p.tarWorker(context.Background(), &wg, 0)
	wg.Wait()

	var done []*workItem
	for item := range p.uploadQueue {
		done = append(done, item)
	}
	return done
}

// archiveNames lists the entry names in a gzipped tarball.
func archiveNames(t *testing.T, path string) []string {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()

	var names []string
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		names = append(names, header.Name)
	}
	sort.Strings(names)
	return names
}

// The failure this branch exists to prevent: several file-scan jobs share one
// PrimaryDir, so before per-file archives they all resolved to a single tarball
// and raced over it ("upload incomplete: received 1 of 9 parts"), each one
// carrying the whole folder rather than its own two files.
func TestTarWorker_ExplicitFileListArchivesOnlyThatJobsFiles(t *testing.T) {
	root := t.TempDir()
	inputs := filepath.Join(root, "inputs")
	if err := os.MkdirAll(inputs, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var jobs []models.JobSpec
	for _, name := range []string{"case1", "case2", "case3"} {
		deck := filepath.Join(inputs, name+".inp")
		mesh := filepath.Join(inputs, name+".mesh")
		for _, path := range []string{deck, mesh} {
			if err := os.WriteFile(path, []byte(filepath.Base(path)), 0644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		}
		jobs = append(jobs, models.JobSpec{
			JobName:         name,
			Directory:       inputs,
			LocalInputFiles: []string{deck, mesh},
		})
	}

	// A file nobody asked for: it lives in Directory, so a directory walk would
	// pick it up and every job's archive would carry it.
	if err := os.WriteFile(filepath.Join(inputs, "unrelated.dat"), []byte("x"), 0644); err != nil {
		t.Fatalf("write unrelated: %v", err)
	}

	cfg := &config.Config{TarCompression: "gzip"}
	done := runTarWorker(t, cfg, root, jobs)

	if len(done) != len(jobs) {
		t.Fatalf("%d jobs reached upload, want %d", len(done), len(jobs))
	}

	seen := make(map[string]string, len(done))
	for _, item := range done {
		if item.state.TarStatus != "success" {
			t.Fatalf("%s: TarStatus = %q (%s)", item.state.JobName, item.state.TarStatus, item.state.ErrorMessage)
		}
		if other, dup := seen[item.state.TarPath]; dup {
			t.Fatalf("%s and %s resolved to the same archive %s", other, item.state.JobName, item.state.TarPath)
		}
		seen[item.state.TarPath] = item.state.JobName

		if filepath.Dir(item.state.TarPath) != root {
			t.Errorf("%s: archive %s is not in tempDir %s, so safeRemoveTar would refuse it",
				item.state.JobName, item.state.TarPath, root)
		}

		want := []string{item.state.JobName + ".inp", item.state.JobName + ".mesh"}
		got := archiveNames(t, item.state.TarPath)
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("%s: archive holds %v, want exactly %v", item.state.JobName, got, want)
		}
	}
}

// A secondary pattern may reach outside the primary file's folder. Flattening is
// what makes that work, and it is why those files used to be validated as
// present and then never uploaded.
func TestTarWorker_ExplicitFileListFlattensAcrossDirectories(t *testing.T) {
	root := t.TempDir()
	inputs := filepath.Join(root, "inputs")
	meshes := filepath.Join(root, "meshes")
	for _, dir := range []string{inputs, meshes} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	deck := filepath.Join(inputs, "case1.inp")
	shared := filepath.Join(meshes, "case1.cfg")
	for _, path := range []string{deck, shared} {
		if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	cfg := &config.Config{TarCompression: "gzip"}
	done := runTarWorker(t, cfg, root, []models.JobSpec{{
		JobName:         "case1",
		Directory:       inputs,
		LocalInputFiles: []string{deck, shared},
	}})

	if len(done) != 1 {
		t.Fatalf("%d jobs reached upload, want 1", len(done))
	}
	if done[0].state.TarStatus != "success" {
		t.Fatalf("TarStatus = %q (%s)", done[0].state.TarStatus, done[0].state.ErrorMessage)
	}

	got := archiveNames(t, done[0].state.TarPath)
	want := []string{"case1.cfg", "case1.inp"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("archive holds %v, want %v flattened to the working directory", got, want)
	}
}

// A job with no file list still archives its whole directory, which is every
// other PUR mode.
func TestTarWorker_WithoutFileListStillArchivesDirectory(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "Run_1")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"a.inp", "b.dat"} {
		if err := os.WriteFile(filepath.Join(runDir, name), []byte("x"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	cfg := &config.Config{TarCompression: "gzip"}
	done := runTarWorker(t, cfg, root, []models.JobSpec{{
		JobName:   "Run_1",
		Directory: runDir,
	}})

	if len(done) != 1 {
		t.Fatalf("%d jobs reached upload, want 1", len(done))
	}
	if done[0].state.TarStatus != "success" {
		t.Fatalf("TarStatus = %q (%s)", done[0].state.TarStatus, done[0].state.ErrorMessage)
	}

	names := archiveNames(t, done[0].state.TarPath)
	if len(names) == 0 {
		t.Fatal("directory archive is empty")
	}
	// The walk keeps the directory prefix, unlike the flattened file-list path.
	for _, name := range names {
		if filepath.Base(name) == name {
			t.Errorf("entry %q lost its directory prefix; the walk path should not flatten", name)
		}
	}
}

// A missing file fails only its own job, and the partial archive is not left
// behind for the upload stage to find.
func TestTarWorker_MissingFileFailsOneJob(t *testing.T) {
	root := t.TempDir()
	present := filepath.Join(root, "case1.inp")
	if err := os.WriteFile(present, []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := &config.Config{TarCompression: "gzip"}
	done := runTarWorker(t, cfg, root, []models.JobSpec{{
		JobName:         "case1",
		Directory:       root,
		LocalInputFiles: []string{present, filepath.Join(root, "gone.mesh")},
	}})

	if len(done) != 0 {
		t.Fatalf("%d jobs reached upload, want 0", len(done))
	}
}
