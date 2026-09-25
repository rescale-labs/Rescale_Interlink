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
)

// A server name the download cannot write safely fails that file, or that
// folder with everything in it, and the rest of the folder still downloads,
// with or without --continue-on-error. Each refusal is counted as failed and
// printed with its name, location and reason, and the command fails.
func TestFoldersDownloadDirRefusesNamesAndDownloadsTheRest(t *testing.T) {
	entry := func(kind, id, name string) map[string]any {
		return map[string]any{"type": kind, "item": map[string]any{"id": id, "name": name, "decryptedSize": 1}}
	}
	tree := map[string][]map[string]any{
		"root": {entry("folder", "ok", "ok"), entry("folder", "bad", "run:1"), entry("file", "f1", "a.dat"), entry("file", "f2", "results.")},
		"ok":   {entry("file", "f3", "inner.dat"), entry("file", "f4", "NUL.txt")},
		"bad":  {entry("file", "f5", "inner.dat")},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.Split(strings.Trim(r.URL.Path, "/"), "/")[3]
		if id == "bad" {
			t.Error("listed the contents of a refused folder")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": tree[id]})
	}))
	defer server.Close()
	orig := getAPIClientFn
	defer func() { getAPIClientFn = orig }()
	getAPIClientFn = func() (*api.Client, error) {
		return api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"}), nil
	}
	write := func(_ context.Context, p download.DownloadParams) error {
		return os.WriteFile(p.LocalPath, []byte("x"), 0o644)
	}

	for _, flags := range [][]string{{"--merge"}, {"--merge", "--continue-on-error"}} {
		out := t.TempDir()
		var printed string
		var err error
		said := captureStderr(t, func() {
			printed, err = runWithCancel(t, newFoldersCmd(), write, append([]string{"download-dir", "root", "--outdir", out, "--max-concurrent", "1"}, flags...)...)
		})
		if err == nil || !strings.Contains(printed, "Files downloaded:   2\n") || !strings.Contains(printed, "Files failed:       3\n") {
			t.Errorf("%v: returned %v after printing\n%s\nwant 2 downloaded, 3 failed and an error", flags, err, printed)
		}
		for _, reason := range []string{
			`file at the top level not downloaded: filename cannot end in a dot or a space: "results."`,
			`folder at the top level and its contents not downloaded: filename cannot contain ':': "run:1"`,
			`file in "ok" not downloaded: filename is a reserved Windows device name: "NUL.txt"`,
		} {
			if !strings.Contains(said, reason) {
				t.Errorf("%v: printed\n%s\nwant %s", flags, said, reason)
			}
		}
		var onDisk []string
		_ = filepath.WalkDir(out, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(out, path)
				onDisk = append(onDisk, filepath.ToSlash(rel))
			}
			return nil
		})
		if strings.Join(onDisk, " ") != "root/a.dat root/ok/inner.dat" {
			t.Errorf("%v: on disk %q, want root/a.dat and root/ok/inner.dat", flags, onDisk)
		}
	}

	printed, err := runWithCancel(t, newFoldersCmd(), write, "download-dir", "root", "--outdir", t.TempDir(), "--merge", "--dry-run")
	if err != nil || !strings.Contains(printed, `Would fail: file in "ok" not downloaded`) || !strings.Contains(printed, "Would download:   2\n") ||
		!strings.Contains(printed, "Would fail:       3 (the download would exit 1; a dry run exits 0)\n") ||
		!strings.Contains(printed, "Files would fail:   3 (a dry run exits 0)\n") {
		t.Errorf("--dry-run returned %v after printing\n%s\nwant the refusals, 2 files to download and 3 to fail counted, and exit 0", err, printed)
	}
}
