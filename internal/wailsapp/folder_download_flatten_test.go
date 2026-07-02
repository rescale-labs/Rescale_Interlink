package wailsapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/transfer"
)

func folderEntry(kind, id, name string) map[string]any {
	return map[string]any{"type": kind, "item": map[string]any{"id": id, "name": name, "decryptedSize": 4}}
}

// flattenApp is an App with flatten_job_download on whose folder downloads
// list tree from a fake API, one folder ID's pages each. A page waits for its
// delay, keyed "<folder ID>/<page>", so a test sets the order the scan meets
// entries in. Every download itself fails at the fake API.
func flattenApp(t *testing.T, tree map[string][][]map[string]any, delays map[string]time.Duration) (*App, *core.Engine) {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := strings.CutSuffix(r.URL.Path, "/contents/")
		id := path[strings.LastIndex(path, "/")+1:]
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if !ok || page >= len(tree[id]) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		time.Sleep(delays[fmt.Sprintf("%s/%d", id, page)])
		body := map[string]any{"results": tree[id][page]}
		if page+1 < len(tree[id]) {
			body["next"] = fmt.Sprintf("%s%s?page=%d", server.URL, r.URL.Path, page+1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	client := api.NewClientForTest(&config.Config{APIBaseURL: server.URL, APIKey: "test"})
	previous := folderDownloadAPI
	t.Cleanup(func() { folderDownloadAPI = previous })
	folderDownloadAPI = func(*App) *api.Client { return client }
	a, eng := appWithEngine(t)
	a.config = &config.Config{FlattenJobDownload: true}
	eng.TransferService().SetAPIClient(client)
	return a, eng
}

// waitForTasks polls the transfer queue until done accepts it, and returns
// the last snapshot either way.
func waitForTasks(eng *core.Engine, done func([]transfer.TransferTask) bool) []transfer.TransferTask {
	var tasks []transfer.TransferTask
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if tasks = eng.TransferService().GetQueue().GetTasks(); done(tasks) {
			break
		}
	}
	return tasks
}

func describe(tasks []transfer.TransferTask) string {
	var rows []string
	for i := range tasks {
		rows = append(rows, fmt.Sprintf("%s→%s (%s, %v)", tasks[i].Source, tasks[i].Dest, tasks[i].State, tasks[i].Error))
	}
	return strings.Join(rows, "; ")
}

// With flatten_job_download on, a job folder's Input/ and Output/ levels are
// dropped and nothing else changes: an Input or Output folder deeper down, and
// a top-level file that happens to be named Output, keep their paths. Of two
// files that flatten to one local file, ignoring case, Output's is downloaded
// and Input's is a failed row naming it. Refusals still apply at the flattened
// path: a name the scan refused, and a link where the file would land.
func TestFolderDownloadFlattensTheJobSplitOnly(t *testing.T) {
	folder := func(id, name string) map[string]any { return folderEntry("folder", id, name) }
	file := func(id, name string) map[string]any { return folderEntry("file", id, name) }
	a, eng := flattenApp(t, map[string][][]map[string]any{
		"job":     {{folder("in", "Input"), folder("out", "Output"), folder("data", "data")}},
		"in":      {{file("f1", "model.inp"), file("f2", "shared.dat")}},
		"out":     {{folder("run1", "run1"), file("f3", "results.dat"), file("f4", "shared.dat"), file("f5", "linked.dat"), file("f6", "bad."), file("f10", "Model.inp")}},
		"run1":    {{file("f7", "a.dat")}},
		"data":    {{folder("dataout", "Output")}},
		"dataout": {{file("f8", "x.dat")}},
		"plain":   {{file("f9", "Output")}},
	}, nil)

	dest := t.TempDir()
	job := filepath.Join(dest, "JOB")
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(job, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(job, "linked.dat")); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
	for id, name := range map[string]string{"job": "JOB", "plain": "PLAIN"} {
		if result := a.StartFolderDownload(id, name, dest, ""); result.Error != "" {
			t.Fatalf("StartFolderDownload(%s): %s", name, result.Error)
		}
	}

	in := func(parts ...string) string { return filepath.Join(append([]string{dest}, parts...)...) }
	wantStarted := []string{in("JOB", "Model.inp"), in("JOB", "data", "Output", "x.dat"), in("JOB", "results.dat"),
		in("JOB", "run1", "a.dat"), in("JOB", "shared.dat"), in("PLAIN", "Output")}
	wantRefused := []string{"bad.", "f1", "f2", "linked.dat"} // by name, or by file ID for Input's clashing files
	var started, refused []string
	var duplicate string
	waitForTasks(eng, func(tasks []transfer.TransferTask) bool {
		started, refused, duplicate = nil, nil, ""
		for i := range tasks {
			task := &tasks[i]
			switch {
			case !task.StartedAt.IsZero():
				started = append(started, task.Dest)
			case task.State == transfer.TaskFailed && strings.Contains(task.Error.Error(), "same local file"):
				refused = append(refused, task.Source)
				if task.Source == "f2" {
					duplicate = task.Error.Error()
				}
			case task.State == transfer.TaskFailed:
				refused = append(refused, task.Name)
			}
		}
		slices.Sort(started)
		slices.Sort(refused)
		return slices.Equal(started, wantStarted) && slices.Equal(refused, wantRefused)
	})
	if !slices.Equal(started, wantStarted) {
		t.Errorf("transfers to %q, want one each to %q", started, wantStarted)
	}
	if !slices.Equal(refused, wantRefused) {
		t.Errorf("refused rows %q, want %q", refused, wantRefused)
	}
	for _, want := range []string{"same local file", filepath.Join("Input", "shared.dat"), filepath.Join("Output", "shared.dat")} {
		if !strings.Contains(duplicate, want) {
			t.Errorf("Input/shared.dat was refused with %q, want the reason and both remote paths", duplicate)
			break
		}
	}
	for _, split := range []string{in("JOB", "Input"), in("JOB", "Output")} {
		if _, err := os.Lstat(split); !os.IsNotExist(err) {
			t.Errorf("%s exists (err %v); the split folders should not be created", split, err)
		}
	}
	if info, err := os.Stat(in("JOB", "data", "Output")); err != nil || !info.IsDir() {
		t.Errorf("the nested Output folder was not created where it is: %v", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Errorf("the link target now holds %q", got)
	}
}

// Which of two clashing files a flattened download takes does not depend on
// the order the scan lists them in: a file the split does not move keeps its
// path, and Output's file wins over Input's. The other's failed row keeps its
// own split path, so retrying it never writes over the file that won.
func TestFolderDownloadSplitClashIsSettledTheSameWayEveryTime(t *testing.T) {
	folder := func(id, name string) map[string]any { return folderEntry("folder", id, name) }
	file := func(id, name string) map[string]any { return folderEntry("file", id, name) }
	job := map[string][][]map[string]any{
		"root": {{folder("in", "Input"), folder("out", "Output")}},
		"in":   {{file("fin", "shared.dat")}},
		"out":  {{file("fout", "shared.dat")}},
	}
	plain := map[string][][]map[string]any{
		"root": {{folder("out", "Output")}, {file("ftop", "shared.dat")}},
		"out":  {{file("fout", "shared.dat")}},
	}
	slow := 300 * time.Millisecond
	for _, tc := range []struct {
		name          string
		tree          map[string][][]map[string]any
		delays        map[string]time.Duration
		winner, loser string // file IDs
		loserSplit    string
	}{
		{"Input listed first", job, map[string]time.Duration{"out/0": slow}, "fout", "fin", "Input"},
		{"Output listed first", job, map[string]time.Duration{"in/0": slow}, "fout", "fin", "Input"},
		{"a file already at the top, listed last", plain, map[string]time.Duration{"root/1": slow}, "ftop", "fout", "Output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, eng := flattenApp(t, tc.tree, tc.delays)
			dest := t.TempDir()
			if result := a.StartFolderDownload("root", "JOB", dest, ""); result.Error != "" {
				t.Fatalf("StartFolderDownload: %s", result.Error)
			}
			winnerPath := filepath.Join(dest, "JOB", "shared.dat")
			loserPath := filepath.Join(dest, "JOB", tc.loserSplit, "shared.dat")
			find := func(tasks []transfer.TransferTask, source string) *transfer.TransferTask {
				for i := range tasks {
					if tasks[i].Source == source {
						return &tasks[i]
					}
				}
				return nil
			}
			tasks := waitForTasks(eng, func(tasks []transfer.TransferTask) bool {
				winner, loser := find(tasks, tc.winner), find(tasks, tc.loser)
				return winner != nil && !winner.StartedAt.IsZero() && loser != nil && loser.State == transfer.TaskFailed
			})
			winner, loser := find(tasks, tc.winner), find(tasks, tc.loser)
			if winner == nil || loser == nil || winner.Dest != winnerPath || loser.Dest != loserPath ||
				loser.Error == nil || !strings.Contains(loser.Error.Error(), "same local file") {
				t.Fatalf("tasks %s, want file %s sent to %s and file %s a failed row at %s naming the clash", describe(tasks), tc.winner, winnerPath, tc.loser, loserPath)
			}

			if _, err := eng.TransferService().RetryTransfer(loser.ID); err != nil {
				t.Fatalf("RetryTransfer: %v", err)
			}
			tasks = waitForTasks(eng, func(tasks []transfer.TransferTask) bool {
				retried := find(tasks, tc.loser)
				return retried != nil && !retried.StartedAt.IsZero()
			})
			if retried := find(tasks, tc.loser); retried == nil || retried.StartedAt.IsZero() || retried.Dest != loserPath {
				t.Errorf("tasks %s after the retry, want file %s run again at %s", describe(tasks), tc.loser, loserPath)
			}
			for i := range tasks {
				if tasks[i].Dest == winnerPath && tasks[i].Source != tc.winner {
					t.Errorf("file %s was sent to %s, which file %s holds", tasks[i].Source, winnerPath, tc.winner)
				}
			}
		})
	}
}
