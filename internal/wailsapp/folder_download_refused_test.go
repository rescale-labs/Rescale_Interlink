package wailsapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/transfer"
)

// Every entry a folder download refuses is a failed row on the Transfers tab,
// with the reason, and does no transfer work: an entry the scan refused by
// name, the empty name included, and a link where a file belongs, which merge
// mode took for the file already downloaded. Merge mode skips a regular file
// already there only when it is complete: a truncated one, like a missing one,
// is downloaded.
func TestFolderDownloadShowsEveryRefusedEntryAsAFailedRow(t *testing.T) {
	entry := func(kind, id, name string) map[string]any {
		return map[string]any{"type": kind, "item": map[string]any{"id": id, "name": name, "decryptedSize": 4}}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/folders/root/contents/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			entry("folder", "bad", "run:1"), entry("file", "f1", "."), entry("file", "f2", ".."), entry("file", "f3", ""),
			entry("file", "f4", "results."), entry("file", "f5", "linked.dat"), entry("file", "f6", "kept.dat"), entry("file", "f7", "ok.dat"), entry("file", "f8", "short.dat")}})
	}))
	defer server.Close()
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
	defer func(f func(*App) *api.Client) { folderDownloadAPI = f }(folderDownloadAPI)
	folderDownloadAPI = func(*App) *api.Client { return client }
	a, eng := appWithEngine(t)
	eng.TransferService().SetAPIClient(client)

	dest := t.TempDir()
	root := filepath.Join(dest, "FOLDER")
	victim := filepath.Join(t.TempDir(), "victim.txt")
	for path, content := range map[string]string{filepath.Join(root, "kept.dat"): "keep", filepath.Join(root, "short.dat"): "k", victim: "keep"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(victim, filepath.Join(root, "linked.dat")); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
	if result := a.StartFolderDownload("root", "FOLDER", dest, ""); result.Error != "" {
		t.Fatalf("StartFolderDownload: %s", result.Error)
	}

	want := []string{"", ".", "..", "linked.dat", "results.", "run:1"}
	var refused, started []string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		refused, started = nil, nil
		tasks := eng.TransferService().GetQueue().GetTasks()
		for i := range tasks {
			task := &tasks[i]
			reason := "not downloaded: "
			if task.Name == "linked.dat" {
				reason = "is a symbolic link"
			}
			if !task.StartedAt.IsZero() {
				started = append(started, task.Name)
			} else if task.State == transfer.TaskFailed && task.Error != nil && strings.Contains(task.Error.Error(), reason) {
				refused = append(refused, task.Name)
			}
		}
		slices.Sort(started)
		if slices.Sort(refused); slices.Equal(refused, want) && slices.Equal(started, []string{"ok.dat", "short.dat"}) {
			break
		}
	}
	if !slices.Equal(refused, want) || !slices.Equal(started, []string{"ok.dat", "short.dat"}) {
		t.Errorf("refused rows %q and transfers %q, want refused rows %q, each with its reason and never started, and transfers of ok.dat and the truncated short.dat only", refused, started, want)
	}
	if info, err := os.Lstat(filepath.Join(root, "linked.dat")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was not left in place: %v", err)
	}
	for _, path := range []string{victim, filepath.Join(root, "kept.dat")} {
		if got, _ := os.ReadFile(path); string(got) != "keep" {
			t.Errorf("%s now holds %q", path, got)
		}
	}
}
