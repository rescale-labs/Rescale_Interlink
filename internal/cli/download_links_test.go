package cli

import (
	"context"
	"encoding/json"
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

// A link where a download belongs is refused, and it and what it points at
// are left alone, whatever the conflict mode: --overwrite and the replacement
// of an incomplete file used to remove the link, --skip took a link of the
// right size for the finished file, and --resume removed it.
func TestDownloadsRefuseALinkAtTheDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"type": "file", "item": map[string]any{"id": "F1", "name": "results.dat", "decryptedSize": 4}}}})
	}))
	defer server.Close()
	origList, origClient := listJobFilesFn, getAPIClientFn
	t.Cleanup(func() { listJobFilesFn, getAPIClientFn = origList, origClient })
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	listJobFilesFn = func(context.Context, *api.Client, string) ([]models.JobFile, error) {
		return []models.JobFile{{ID: "F1", Name: "results.dat", DecryptedSize: 4}}, nil
	}
	write := func(_ context.Context, p download.DownloadParams) error {
		return os.WriteFile(p.LocalPath, []byte("new!"), 0o644)
	}

	for _, tc := range []struct {
		name, target string // target: what the link points at
		folder       bool   // folders download-dir, else jobs download
		flag         string
	}{
		{"jobs download --overwrite", "same", false, "--overwrite"},
		{"jobs download --skip, a link of the right size", "same", false, "--skip"},
		{"jobs download --skip, a link of the wrong size", "longer", false, "--skip"},
		{"jobs download --resume", "longer", false, "--resume"},
		{"folders download-dir --merge, a link of the right size", "same", true, "--merge"},
		{"folders download-dir --merge, a link of the wrong size", "longer", true, "--merge"},
		{"folders download-dir --overwrite", "same", true, "--overwrite"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			out := filepath.Join(root, "out")
			dest := filepath.Join(out, "results.dat")
			args := []string{"--job-id", "JOB1", "--outdir", out, tc.flag}
			cmd := newJobsDownloadCmd()
			if tc.folder {
				dest = filepath.Join(out, "FOLDER1", "results.dat")
				args = []string{"download-dir", "FOLDER1", "--outdir", out, "--max-concurrent", "1", tc.flag}
				cmd = newFoldersCmd()
			}
			victim := filepath.Join(root, "victim.txt")
			if err := os.WriteFile(victim, []byte(tc.target), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, dest); err != nil {
				t.Skipf("cannot make a symbolic link here: %v", err)
			}
			before, err := os.Stat(victim)
			if err != nil {
				t.Fatal(err)
			}

			var printed string
			said := captureStderr(t, func() { printed, err = runWithCancel(t, cmd, write, args...) })
			if err == nil || !strings.Contains(err.Error()+printed+said, "is a symbolic link") {
				t.Errorf("returned %v after printing\n%s%s\nwant the link refused", err, printed, said)
			}
			if info, lerr := os.Lstat(dest); lerr != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link was not left in place: %v", lerr)
			}
			if got, _ := os.ReadFile(victim); string(got) != tc.target {
				t.Errorf("the link's target now holds %q", got)
			}
			if info, _ := os.Stat(victim); info.Mode() != before.Mode() {
				t.Errorf("the link's target mode is now %v, was %v", info.Mode(), before.Mode())
			}
		})
	}
}

// --resume removed <file>.encrypted whenever no resume record vouched for it,
// though only a v0 download writes that name: a user's own
// "results.dat.encrypted" was lost. It is left alone.
func TestJobsDownloadResumeLeavesASiblingEncryptedFileAlone(t *testing.T) {
	origList, origClient := listJobFilesFn, getAPIClientFn
	t.Cleanup(func() { listJobFilesFn, getAPIClientFn = origList, origClient })
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: "http://127.0.0.1:1", APIKey: "test"}), nil
	}
	listJobFilesFn = func(context.Context, *api.Client, string) ([]models.JobFile, error) {
		return []models.JobFile{{ID: "F1", Name: "results.dat", DecryptedSize: 4}}, nil
	}
	out := t.TempDir()
	dest := filepath.Join(out, "results.dat")
	for path, content := range map[string]string{dest: "short", dest + ".encrypted": "mine"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runWithCancel(t, newJobsDownloadCmd(), func(_ context.Context, p download.DownloadParams) error {
		return os.WriteFile(p.LocalPath, []byte("new!"), 0o644)
	}, "--job-id", "JOB1", "--outdir", out, "--resume"); err != nil {
		t.Fatalf("jobs download --resume: %v", err)
	}
	if got, _ := os.ReadFile(dest + ".encrypted"); string(got) != "mine" {
		t.Errorf("results.dat.encrypted now holds %q, want it left alone", got)
	}
}
