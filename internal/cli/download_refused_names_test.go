package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud/download"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// A file whose server name cannot be written safely fails alone: the others
// download, and the command says why and exits 1, through the same summary
// when every file is refused. With --file-id and --output the server's name
// builds no path, so it is not held against the file, and it is printed quoted.
func TestDownloadsFailForRefusedNamesAndKeepTheRest(t *testing.T) {
	names := map[string]string{"F1": "a.dat", "F2": "run 10:30.log", "F3": "evil\x1b[31m.log"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models.CloudFile{ID: id, Name: names[id], DecryptedSize: 1})
	}))
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
	origList, origClient := listJobFilesFn, getAPIClientFn
	t.Cleanup(func() { listJobFilesFn, getAPIClientFn = origList, origClient })
	getAPIClientFn = func() (*api.Client, error) { return client, nil }
	var ids []string
	listJobFilesFn = func(context.Context, *api.Client, string) ([]models.JobFile, error) {
		var files []models.JobFile
		for _, id := range ids {
			files = append(files, models.JobFile{ID: id, Name: names[id], DecryptedSize: 1})
		}
		return files, nil
	}
	write := func(_ context.Context, p download.DownloadParams) error {
		return os.WriteFile(p.LocalPath, []byte("x"), 0o644)
	}
	const reason = `filename cannot contain ':': "run 10:30.log"`

	for _, tc := range []struct {
		command string
		run     func(out string) (string, error)
	}{
		{"jobs download", func(out string) (string, error) {
			return runWithCancel(t, newJobsDownloadCmd(), write, "--job-id", "JOB1", "--outdir", out)
		}},
		{"files download", func(out string) (string, error) {
			var err error
			printed := captureStdout(t, func() {
				defer func(dl func(context.Context, download.DownloadParams) error) { downloadFileFn = dl }(downloadFileFn)
				downloadFileFn = write
				err = executeFileDownload(context.Background(), ids, out, 1, false, true, false, false, client, GetLogger())
			})
			return printed, err
		}},
	} {
		for _, ids = range [][]string{{"F1", "F2"}, {"F2"}} {
			out := t.TempDir()
			printed, err := tc.run(out)
			if err == nil || !strings.Contains(err.Error(), reason) || !strings.Contains(printed, "✗ Failed to download 1 file(s)") {
				t.Errorf("%s %v: returned %v after printing\n%s\nwant it to fail F2 for %s and count it", tc.command, ids, err, printed, reason)
			}
			if _, statErr := os.Stat(filepath.Join(out, "a.dat")); (statErr == nil) != (len(ids) == 2) {
				t.Errorf("%s %v: a.dat downloaded: %v", tc.command, ids, statErr == nil)
			}
		}
	}

	target := filepath.Join(t.TempDir(), "evil.log")
	printed, err := runWithCancel(t, newJobsDownloadCmd(), write, "--job-id", "JOB1", "--file-id", "F3", "--output", target)
	if err != nil || !strings.Contains(printed, `Downloading file: "evil\x1b[31m.log"`) || strings.Contains(printed, "\x1b") {
		t.Errorf("jobs download --file-id F3 --output %s: %v after printing\n%q\nwant the download to the path given and the name quoted", target, err, printed)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("--output file not written: %v", err)
	}
}

// Every component of a job file's server path is held to the name rules: one
// that fails them fails that file, which is not written anywhere else instead.
func TestJobsDownloadChecksEveryPathComponent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	origList, origClient := listJobFilesFn, getAPIClientFn
	t.Cleanup(func() { listJobFilesFn, getAPIClientFn = origList, origClient })
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	listJobFilesFn = func(context.Context, *api.Client, string) ([]models.JobFile, error) {
		return []models.JobFile{
			{ID: "F1", Name: "a.dat", RelativePath: "sub/a.dat", DecryptedSize: 1},
			{ID: "F2", Name: "out.log", RelativePath: "run:1/out.log", DecryptedSize: 1},
			{ID: "F3", Name: "x.log", RelativePath: "../x.log", DecryptedSize: 1},
		}, nil
	}
	root := t.TempDir()
	out := filepath.Join(root, "out")
	printed, err := runWithCancel(t, newJobsDownloadCmd(), func(_ context.Context, p download.DownloadParams) error {
		return os.WriteFile(p.LocalPath, []byte("x"), 0o644)
	}, "--job-id", "JOB1", "--outdir", out)
	if err == nil || !strings.Contains(printed, "✗ Failed to download 2 file(s)") ||
		!strings.Contains(printed, `filename cannot contain ':': "run:1"`) || !strings.Contains(printed, `filename cannot be only dots: ".."`) {
		t.Errorf("returned %v after printing\n%s\nwant run:1/out.log and ../x.log refused and counted", err, printed)
	}
	var onDisk []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && path != root {
			rel, _ := filepath.Rel(root, path)
			onDisk = append(onDisk, filepath.ToSlash(rel))
		}
		return nil
	})
	if strings.Join(onDisk, " ") != "out out/sub out/sub/a.dat" {
		t.Errorf("on disk %q, want only out/sub/a.dat", onDisk)
	}
}
