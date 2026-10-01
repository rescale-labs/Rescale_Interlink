package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
)

// pur run checks --common-input-files, expanding a folder in it, before the run
// starts: a refusal names the files and comes before any request, even the one
// creating the folder the batch uploads to.
func TestPURRunChecksCommonInputFilesFirst(t *testing.T) {
	usePURConfig(t)
	jobsCSV := writePreflightJobsCSV(t, "yes")
	t.Chdir(t.TempDir()) // the state files land here
	common := t.TempDir()
	mesh := filepath.Join(common, "mesh")
	for _, f := range []string{filepath.Join("mesh", "model.inp"), filepath.Join("mesh", "sub", "grid.dat"), "model.inp"} {
		writeScanDeck(t, common, f)
	}

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, `{"detail": "refused"}`, http.StatusBadRequest)
	}))
	defer server.Close()
	defer func(orig func(*config.Config) (*api.Client, error)) { newPipelineClientFn = orig }(newPipelineClientFn)
	newPipelineClientFn = func(*config.Config) (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}

	// A dry run is checked too: it is how a batch is checked before it is run.
	for _, dryRun := range []string{"--dry-run=false", "--dry-run"} {
		err := runPURCommand(t, newRunCmd(), "--jobs-csv", jobsCSV, "--state", "clash.csv", "--folder", "sweeps/x", dryRun,
			"--common-input-files", mesh+","+filepath.Join(common, "model.inp"))
		if err == nil || requests.Load() != 0 || !strings.Contains(err.Error(), filepath.Join(mesh, "model.inp")) ||
			!strings.Contains(err.Error(), filepath.Join(common, "model.inp")) {
			t.Errorf("%s, two common files named model.inp: error %v after %d request(s), want both named before any request",
				dryRun, err, requests.Load())
		}
	}

	// Without the clash the folder's files are the batch's common files, so the
	// first upload tried is a file in it; the folder itself used to be refused
	// by the upload.
	err := runPURCommand(t, newRunCmd(), "--jobs-csv", jobsCSV, "--state", "folder.csv", "--common-input-files", mesh)
	if want := "failed to upload shared file " + filepath.Join(mesh, "model.inp") + ":"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("a folder of common files: error %v, want it to say %q", err, want)
	}
}
