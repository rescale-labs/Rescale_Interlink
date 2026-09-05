package pipeline

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
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

// tarWorkerCase is one scenario for the tar stage: files to create under a fresh
// root, the jobs built over them, and the archive each job that reaches the
// upload stage must hold. Paths in files, Directory and LocalInputFiles are
// relative to that root; a job absent from want must not reach upload at all.
type tarWorkerCase struct {
	name  string
	files []string
	jobs  []models.JobSpec
	want  map[string][]string
}

func TestTarWorker(t *testing.T) {
	tests := []tarWorkerCase{
		{
			// The failure this branch exists to prevent: several file-scan jobs
			// share one PrimaryDir, so before per-file archives they all resolved
			// to a single tarball and raced over it ("upload incomplete: received
			// 1 of 9 parts"), each carrying the whole folder rather than its own
			// two files. unrelated.dat is the file nobody asked for — a directory
			// walk picks it up, an explicit list must not.
			name: "an explicit file list archives only that job's own files",
			files: []string{
				"inputs/case1.inp", "inputs/case1.mesh",
				"inputs/case2.inp", "inputs/case2.mesh",
				"inputs/case3.inp", "inputs/case3.mesh",
				"inputs/unrelated.dat",
			},
			jobs: []models.JobSpec{
				{JobName: "case1", Directory: "inputs", LocalInputFiles: []string{"inputs/case1.inp", "inputs/case1.mesh"}},
				{JobName: "case2", Directory: "inputs", LocalInputFiles: []string{"inputs/case2.inp", "inputs/case2.mesh"}},
				{JobName: "case3", Directory: "inputs", LocalInputFiles: []string{"inputs/case3.inp", "inputs/case3.mesh"}},
			},
			want: map[string][]string{
				"case1": {"case1.inp", "case1.mesh"},
				"case2": {"case2.inp", "case2.mesh"},
				"case3": {"case3.inp", "case3.mesh"},
			},
		},
		{
			// A secondary pattern may reach outside the primary file's folder.
			// Flattening is what makes that work, and it is why those files used
			// to be validated as present and then never uploaded.
			name:  "an explicit file list flattens across directories",
			files: []string{"inputs/case1.inp", "meshes/case1.cfg"},
			jobs: []models.JobSpec{{
				JobName:         "case1",
				Directory:       "inputs",
				LocalInputFiles: []string{"inputs/case1.inp", "meshes/case1.cfg"},
			}},
			want: map[string][]string{"case1": {"case1.cfg", "case1.inp"}},
		},
		{
			// A CSV row may name its files and no directory at all. Such a job
			// still has an archive to build, and the empty Directory must be left
			// alone: resolving it would silently adopt the process working
			// directory, which the job worker would then read as "this job was
			// tarred and uploaded from there".
			name:  "an explicit file list with no directory at all",
			files: []string{"case1.inp"},
			jobs:  []models.JobSpec{{JobName: "case1", LocalInputFiles: []string{"case1.inp"}}},
			want:  map[string][]string{"case1": {"case1.inp"}},
		},
		{
			// Two jobs may legitimately run the same deck with different commands
			// or core counts. Naming the archive by file set alone gave them one
			// path, so one job truncated and rewrote it while the other uploaded.
			name:  "two jobs over one deck get an archive each",
			files: []string{"shared.inp"},
			jobs: []models.JobSpec{
				{JobName: "coarse", Directory: ".", LocalInputFiles: []string{"shared.inp"}},
				{JobName: "fine", Directory: ".", LocalInputFiles: []string{"shared.inp"}},
			},
			want: map[string][]string{"coarse": {"shared.inp"}, "fine": {"shared.inp"}},
		},
		{
			// A job with no file list still archives its whole directory, which is
			// every other PUR mode. The walk keeps the directory prefix, unlike
			// the flattened file-list path.
			name:  "no file list still archives the whole directory",
			files: []string{"Run_1/a.inp", "Run_1/b.dat"},
			jobs:  []models.JobSpec{{JobName: "Run_1", Directory: "Run_1"}},
			want:  map[string][]string{"Run_1": {"Run_1/", "Run_1/a.inp", "Run_1/b.dat"}},
		},
		{
			// A missing file fails only its own job — the healthy one alongside it
			// still reaches upload — and the partial archive is not left behind
			// for the upload stage to find.
			name:  "a missing file fails that job alone",
			files: []string{"case1.inp", "case2.inp"},
			jobs: []models.JobSpec{
				{JobName: "case1", Directory: ".", LocalInputFiles: []string{"case1.inp", "gone.mesh"}},
				{JobName: "case2", Directory: ".", LocalInputFiles: []string{"case2.inp"}},
			},
			want: map[string][]string{"case2": {"case2.inp"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, rel := range tt.files {
				path := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatalf("mkdir for %s: %v", rel, err)
				}
				if err := os.WriteFile(path, []byte(filepath.Base(rel)), 0644); err != nil {
					t.Fatalf("write %s: %v", rel, err)
				}
			}

			jobs := make([]models.JobSpec, len(tt.jobs))
			wantDir := make(map[string]string, len(tt.jobs))
			for i, job := range tt.jobs {
				// An empty Directory stays empty; see the no-directory case.
				if job.Directory != "" {
					job.Directory = filepath.Join(root, job.Directory)
				}
				if len(job.LocalInputFiles) > 0 {
					// Copied rather than rewritten in place, so the table stays
					// reusable across repeated runs of the same case.
					resolved := make([]string, len(job.LocalInputFiles))
					for j, file := range job.LocalInputFiles {
						resolved[j] = filepath.Join(root, file)
					}
					job.LocalInputFiles = resolved
				}
				jobs[i] = job
				wantDir[job.JobName] = job.Directory
			}

			done := runTarWorker(t, &config.Config{TarCompression: "gzip"}, root, jobs)

			if len(done) != len(tt.want) {
				t.Fatalf("%d jobs reached upload, want %d", len(done), len(tt.want))
			}

			seen := make(map[string]string, len(done))
			for _, item := range done {
				name := item.state.JobName
				want, expected := tt.want[name]
				if !expected {
					t.Fatalf("%s reached upload but should not have", name)
				}
				if item.state.TarStatus != "success" {
					t.Fatalf("%s: TarStatus = %q (%s)", name, item.state.TarStatus, item.state.ErrorMessage)
				}
				if other, dup := seen[item.state.TarPath]; dup {
					t.Fatalf("%s and %s resolved to the same archive %s", other, name, item.state.TarPath)
				}
				seen[item.state.TarPath] = name

				if filepath.Dir(item.state.TarPath) != root {
					t.Errorf("%s: archive %s is not in tempDir %s, so safeRemoveTar would refuse it",
						name, item.state.TarPath, root)
				}
				if item.jobSpec.Directory != wantDir[name] {
					t.Errorf("%s: Directory = %q, want it left as %q", name, item.jobSpec.Directory, wantDir[name])
				}
				if !hasLocalArchive(item.jobSpec) {
					t.Errorf("%s: job worker would treat this job as having nothing uploaded", name)
				}
				if got := archiveNames(t, item.state.TarPath); !slices.Equal(got, want) {
					t.Errorf("%s: archive holds %v, want exactly %v", name, got, want)
				}
			}

			// Nothing else was written: a job that failed must leave no partial
			// archive behind for the upload stage to find.
			left, err := filepath.Glob(filepath.Join(root, "*.tar.gz"))
			if err != nil {
				t.Fatalf("glob tempDir: %v", err)
			}
			if len(left) != len(seen) {
				t.Errorf("tempDir holds %v, want only the %d archives that reached upload", left, len(seen))
			}
		})
	}
}
