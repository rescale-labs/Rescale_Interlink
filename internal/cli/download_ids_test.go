package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// A job ID becomes a folder name ("job_<id>" in jobs watch) and a file ID a
// collision suffix, so an ID from the server that is not an ID could place
// files outside the output directory.
func TestJobDownloadRefusesIDsThatAreNotIDs(t *testing.T) {
	origList, origDownload := listJobFilesFn, downloadFileFn
	t.Cleanup(func() { listJobFilesFn, downloadFileFn = origList, origDownload })

	listed := false
	listJobFilesFn = func(context.Context, *api.Client, string) ([]models.JobFile, error) {
		listed = true
		return []models.JobFile{{ID: "FAKEID", Name: "results.dat"}}, nil
	}
	downloadFileFn = func(_ context.Context, params download.DownloadParams) error {
		return os.WriteFile(params.LocalPath, nil, 0o644)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})

	root := t.TempDir()
	outdir := filepath.Join(root, "watch")
	jobID := "x/../../escaped"
	err := executeJobDownload(context.Background(), jobID, filepath.Join(outdir, "job_"+jobID), 1,
		false, true, false, false, nil, nil, nil, nil, client, GetLogger())
	if err == nil || !strings.Contains(err.Error(), "invalid job ID") {
		t.Errorf("err = %v, want the job ID refused", err)
	}
	if listed {
		t.Error("the job's files were listed under an ID that is not one")
	}
	if _, statErr := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(statErr) {
		t.Errorf("a folder appeared outside the output directory: %v", statErr)
	}

	// Two files of one name: the second gets its ID appended to the path.
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name": "results.dat"}`))
	}))
	defer files.Close()
	filesClient := api.NewClientForTest(&config.Config{APIBaseURL: files.URL, APIKey: "test"})
	byID := filepath.Join(root, "files")
	// Windows resolves ".." by name; elsewhere the component before it has to exist.
	if err := os.MkdirAll(filepath.Join(byID, "results_x"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = executeFileDownload(context.Background(), []string{"FAKEID", "x/../../escaped"}, byID, 1,
		false, false, false, true, filesClient, GetLogger())
	if _, statErr := os.Stat(filepath.Join(root, "escaped.dat")); !os.IsNotExist(statErr) {
		t.Errorf("a file appeared outside the output directory: %v", statErr)
	}

}
