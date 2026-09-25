package compat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
)

// sync names each job's folder "rescale_job_<id>", so a job ID from the server
// that is not an ID could place that folder outside the output directory.
func TestCompatJobDownloadRefusesAJobIDThatIsNotAnID(t *testing.T) {
	listed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/files") {
			listed = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

	err := compatDownloadByJobID(context.Background(), "x/../../escaped",
		compatDownloadOpts{OutputDir: t.TempDir()}, client, &CompatContext{Quiet: true})
	if err == nil || !strings.Contains(err.Error(), "invalid job ID") {
		t.Errorf("err = %v, want the job ID refused", err)
	}
	if listed {
		t.Error("the job's files were listed under an ID that is not one")
	}
}

// download-file -fid checks the server's name only where it builds the local
// path: with -o naming a file it is not used, and was refused all the same.
func TestCompatFileIDDownloadChecksTheNameOnlyWhereItIsUsed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "FAKEID", "name": "run 10:30.log"}`))
	}))
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

	dir := t.TempDir()
	for out, want := range map[string]bool{filepath.Join(dir, "run-1030.log"): false, dir: true} {
		err := compatDownloadByFileID(context.Background(), "FAKEID", out, client, &CompatContext{Quiet: true})
		if refused := err != nil && strings.Contains(err.Error(), "invalid filename"); refused != want {
			t.Errorf("-o %s: err = %v, want refused = %v", out, err, want)
		}
	}
}

// A job file whose server path has a component the name rules refuse fails
// before any folder of that path is made.
func TestCompatJobDownloadChecksEveryPathComponent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": [{"id": "FAKEID", "name": "out.log", "relativePath": "run:1/out.log", "decryptedSize": 1}]}`))
	}))
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

	out := t.TempDir()
	err := compatDownloadByJobID(context.Background(), "JOB1", compatDownloadOpts{OutputDir: out}, client, &CompatContext{Quiet: true})
	if err == nil || !strings.Contains(err.Error(), `"run:1"`) {
		t.Errorf("err = %v, want the path refused", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "run:1")); !os.IsNotExist(statErr) {
		t.Errorf("a folder was made for the refused path: %v", statErr)
	}
}

// A link where a download belongs is refused and left alone: one of the right
// size was skipped as the finished file, and one of the wrong size removed.
func TestCompatJobDownloadRefusesALinkAtTheDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": [{"id": "FAKEID", "name": "results.dat", "decryptedSize": 4}]}`))
	}))
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

	for _, target := range []string{"same", "longer"} {
		root := t.TempDir()
		victim, dest := filepath.Join(root, "victim.txt"), filepath.Join(root, "results.dat")
		if err := os.WriteFile(victim, []byte(target), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, dest); err != nil {
			t.Skipf("cannot make a symbolic link here: %v", err)
		}
		err := compatDownloadByJobID(context.Background(), "JOB1", compatDownloadOpts{OutputDir: root}, client, &CompatContext{Quiet: true})
		if err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
			t.Errorf("%s: err = %v, want the link refused", target, err)
		}
		if info, lerr := os.Lstat(dest); lerr != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s: the link was not left in place: %v", target, lerr)
		}
		if got, _ := os.ReadFile(victim); string(got) != target {
			t.Errorf("%s: the link's target now holds %q", target, got)
		}
	}
}

// Each refused file's reason is printed: the batch returned only the first.
func TestCompatJobDownloadPrintsEveryRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": [{"id": "F1", "name": "a:b"}, {"id": "F2", "name": "c|d"}]}`))
	}))
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

	stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer func(orig *os.File) { os.Stderr = orig }(os.Stderr)
	os.Stderr = stderr
	err = compatDownloadByJobID(context.Background(), "JOB1", compatDownloadOpts{OutputDir: t.TempDir()}, client, &CompatContext{Quiet: true})
	said, _ := os.ReadFile(stderr.Name())
	for _, name := range []string{`"a:b"`, `"c|d"`} {
		if err == nil || !strings.Contains(err.Error()+string(said), name) {
			t.Errorf("returned %v after printing %q, want the refusal of %s", err, said, name)
		}
	}
}
