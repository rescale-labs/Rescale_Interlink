package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/models"
)

// Archived folders and everything under them are left out, folders with the
// same name share a path, and the walk stops at maxWorkspaceFolderDepth. Each
// folder the walk stops at is counted.
func TestBuildFolderPaths(t *testing.T) {
	deep := models.MetaFolder{ID: fmt.Sprint(maxWorkspaceFolderDepth + 1), Name: "L"}
	for level := maxWorkspaceFolderDepth; level >= 1; level-- {
		deep = models.MetaFolder{ID: fmt.Sprint(level), Name: "L", Children: []models.MetaFolder{deep}}
	}
	got := map[string][]string{}
	skipped := buildFolderPaths([]models.MetaFolder{
		{ID: "a", Name: "A", Children: []models.MetaFolder{{ID: "a1", Name: "One"}, {ID: "a2", Name: "Two"}}},
		{ID: "dup", Name: "A"},
		{ID: "old", Name: "Old", IsArchived: true, Children: []models.MetaFolder{{ID: "old1", Name: "Kept?"}}},
		deep,
	}, nil, got, 1)
	if skipped != 2 {
		t.Errorf("%d folders skipped, want 2: the archived one and the first below the depth limit", skipped)
	}

	for id, want := range map[string][]string{"a": {"A"}, "a1": {"A", "One"}, "a2": {"A", "Two"}, "dup": {"A"}} {
		if !slices.Equal(got[id], want) {
			t.Errorf("folder %s: path %q, want %q", id, got[id], want)
		}
	}
	for _, id := range []string{"old", "old1", fmt.Sprint(maxWorkspaceFolderDepth + 1)} {
		if path, ok := got[id]; ok {
			t.Errorf("folder %s mapped to %q; archived folders and folders below the depth limit are left out", id, path)
		}
	}
	if path := got[fmt.Sprint(maxWorkspaceFolderDepth)]; len(path) != maxWorkspaceFolderDepth {
		t.Errorf("the deepest folder within the limit has path %q, want %d levels", path, maxWorkspaceFolderDepth)
	}
}

// workspaceServer serves the user's own jobs, the workspace folder tree and the
// jobs under its shared root, each completed a minute after it was created; a
// nil tree answers the tree request with a refusal.
func workspaceServer(t *testing.T, own, shared []map[string]any, tree any) string {
	t.Helper()
	created := map[string]string{}
	for _, j := range append(slices.Clone(own), shared...) {
		created[fmt.Sprint(j["id"])], _ = j["dateInserted"].(string)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch {
		case strings.HasSuffix(r.URL.Path, "/statuses/"):
			at, _ := time.Parse(time.RFC3339, created[strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v3/jobs/"), "/")[0]])
			body = map[string]any{"results": []models.JobStatusEntry{{Status: "Completed", StatusDate: at.Add(time.Minute).Format(time.RFC3339)}}}
		case r.URL.Path == "/api/v3/meta/folders/" && tree == nil:
			w.WriteHeader(http.StatusForbidden)
			return
		case r.URL.Path == "/api/v3/meta/folders/":
			body = tree
		case r.URL.Path == "/api/v3/jobs/" && r.URL.Query().Get("q") == "folder:root":
			body = map[string]any{"results": shared}
		case r.URL.Path == "/api/v3/jobs/" && r.URL.Query().Get("q") == "":
			body = map[string]any{"results": own}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func completedJob(id, folder string) map[string]any {
	j := map[string]any{"id": id, "name": id, "jobStatus": map[string]string{"content": "Completed"}}
	if folder != "" {
		j["folder"] = map[string]string{"id": folder}
	}
	return j
}

func workspaceMonitor(url string, include bool) *Monitor {
	client := api.NewClientForTest(&config.Config{APIKey: "test-key", APIBaseURL: url, ProxyMode: "no-proxy"})
	return NewMonitorWithEligibility(client, nil, nil, &EligibilityConfig{IncludeWorkspaceFolders: include}, logging.NewLoggerWithWriter(io.Discard))
}

// With workspace folders on, jobs under the shared root join the user's own,
// each with its folder path. A job in both listings is scanned once, with its
// folder, so it lands in the mirror of that folder; a job in an archived folder
// is left out.
func TestFindCompletedJobs_WorkspaceFolders(t *testing.T) {
	tree := map[string]any{"sharedWithWorkspace": map[string]any{"id": "root", "name": "Shared", "children": []any{
		map[string]any{"id": "a", "name": "A"},
		map[string]any{"id": "old", "name": "Old", "isArchived": true},
	}}}
	url := workspaceServer(t,
		[]map[string]any{completedJob("own", ""), completedJob("both", "")},
		[]map[string]any{completedJob("both", "a"), completedJob("inroot", "root"), completedJob("archived", "old")},
		tree)

	for _, include := range []bool{true, false} {
		result, err := workspaceMonitor(url, include).FindCompletedJobs(context.Background(), nil)
		if err != nil || result.WorkspaceErr != nil {
			t.Fatalf("include %v: FindCompletedJobs: %v, workspace %v", include, err, result.WorkspaceErr)
		}
		got := map[string][]string{}
		for _, job := range result.Candidates {
			got[job.ID] = job.FolderPath
		}
		want := map[string][]string{"own": nil, "both": nil}
		if include {
			want = map[string][]string{"both": {"A"}, "inroot": nil, "own": nil}
		}
		if len(got) != len(want) || len(result.Candidates) != len(want) || result.Summary.TotalScanned != len(want) {
			t.Errorf("include %v: candidates %v (%d scanned), want %v once each", include, got, result.Summary.TotalScanned, want)
		}
		for id, path := range want {
			if p, ok := got[id]; !ok || !slices.Equal(p, path) {
				t.Errorf("include %v: job %s has path %q (listed %v), want %q", include, id, p, ok, path)
			}
		}
	}
}

// A workspace folder listing that fails leaves the user's own jobs scanned and
// the failure returned, so the poll can report it.
func TestFindCompletedJobs_FailedWorkspaceListingIsReturned(t *testing.T) {
	url := workspaceServer(t, []map[string]any{completedJob("own", "")}, nil, nil)
	result, err := workspaceMonitor(url, true).FindCompletedJobs(context.Background(), nil)
	if err != nil {
		t.Fatalf("FindCompletedJobs: %v", err)
	}
	if result.WorkspaceErr == nil || len(result.Candidates) != 1 || result.Candidates[0].ID != "own" {
		t.Errorf("workspace error %v, candidates %d; want the failure and the user's own job", result.WorkspaceErr, len(result.Candidates))
	}
}

// A poll counts the workspace folders it skipped, archived or with a name that
// cannot name a folder here, and the jobs the lookback left out, in its summary
// and for 'daemon status'. Flattened, a folder's name is no reason to skip it.
func TestPoll_CountsWhatItLeftOut(t *testing.T) {
	tree := map[string]any{"sharedWithWorkspace": map[string]any{"id": "root", "name": "Shared", "children": []any{
		map[string]any{"id": "a", "name": "A"},
		map[string]any{"id": "old", "name": "Old", "isArchived": true},
		map[string]any{"id": "colon", "name": "Q1: results"},
	}}}
	aged := func(id string, age time.Duration) map[string]any {
		j := completedJob(id, "a")
		j["dateInserted"] = time.Now().Add(-age).UTC().Format(time.RFC3339)
		return j
	}
	// Completed within the lookback window, before it, and created too long
	// before it to have completed within it.
	url := workspaceServer(t, nil, []map[string]any{aged("new", time.Hour), aged("finished", 10*24*time.Hour), aged("ancient", 60*24*time.Hour)}, tree)

	for _, flatten := range []bool{false, true} {
		elig := &EligibilityConfig{LookbackDays: 7, IncludeWorkspaceFolders: true}
		d := newDownloadTestDaemon(t, url, t.TempDir(), elig)
		var log bytes.Buffer
		d.logger = logging.NewLoggerWithWriter(&log)
		d.monitor = NewMonitorWithEligibility(d.apiClient, d.state, nil, elig, d.logger)
		d.monitor.flatten = flatten

		d.poll(context.Background())
		folders := 2
		if flatten {
			folders = 1
		}
		summary := pollSummary(strings.Split(log.String(), "\n"))
		for _, want := range []string{fmt.Sprintf("skipped-folders=%d", folders), "too_old_creation_prefilter=1", "outside_lookback_window=1"} {
			if !strings.Contains(summary, want) {
				t.Errorf("flatten %v: the summary lacks %s: %s", flatten, want, summary)
			}
		}
		if f, l, _ := d.state.GetLeftOut(); f != folders || l != 2 {
			t.Errorf("flatten %v: the state counts %d folders and %d jobs left out, want %d and 2", flatten, f, l, folders)
		}
		if users := NewIPCHandler(d, nil).GetUserList(); len(users) != 1 || users[0].WorkspaceFoldersSkipped != folders || users[0].JobsOutsideLookback != 2 {
			t.Errorf("flatten %v: daemon status gets %+v, want %d folders and 2 jobs left out", flatten, users, folders)
		}
	}
}

// A job whose workspace folder cannot be mirrored, and which has no download
// path of its own, is refused before it is claimed: it fails with the reason,
// and no started tag goes on it, another member's job as it may be.
func TestPoll_RefusesAnUnmirrorableJobBeforeClaimingIt(t *testing.T) {
	shortenClaimSettle(t)
	tree := map[string]any{"sharedWithWorkspace": map[string]any{"id": "root", "name": "Shared", "children": []any{
		map[string]any{"id": "colon", "name": "Q1: results"},
	}}}
	var tagWrites atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch {
		case r.URL.Path == "/api/v3/meta/folders/":
			body = tree
		case r.URL.Path == "/api/v3/jobs/" && r.URL.Query().Get("q") == "folder:root":
			body = map[string]any{"results": []map[string]any{completedJob("shared1", "colon")}}
		case r.URL.Path == "/api/v3/jobs/":
			body = map[string]any{"results": []any{}}
		case strings.HasSuffix(r.URL.Path, "/tags/") && r.Method == http.MethodGet:
			body = []api.JobTag{}
		case strings.HasSuffix(r.URL.Path, "/tags/"):
			tagWrites.Add(1)
			w.WriteHeader(http.StatusAccepted)
			return
		case strings.HasSuffix(r.URL.Path, "/custom-fields/"):
			body = map[string]any{config.AutoDownloadFieldName: map[string]any{"value": "Enabled"}}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	elig := &EligibilityConfig{IncludeWorkspaceFolders: true}
	d := newDownloadTestDaemon(t, srv.URL, t.TempDir(), elig)
	d.monitor.SetEligibility(elig)

	d.poll(context.Background())
	failed := d.state.GetFailedJobs()
	if len(failed) != 1 || !strings.Contains(failed[0].Error, "cannot mirror") {
		t.Errorf("failures %+v; want the job failed, saying its folder cannot be mirrored", failed)
	}
	if n := tagWrites.Load(); n != 0 {
		t.Errorf("%d tag writes; want the job refused before any tag went on it", n)
	}
}

// A poll whose workspace folder listing failed says so where 'daemon status'
// and the app look, instead of reporting a healthy scan.
func TestPoll_RecordsAFailedWorkspaceListing(t *testing.T) {
	url := workspaceServer(t, nil, nil, nil)
	d := newDownloadTestDaemon(t, url, t.TempDir(), &EligibilityConfig{IncludeWorkspaceFolders: true})
	d.monitor = workspaceMonitor(url, true)

	d.poll(context.Background())
	if msg, _ := d.LastScanError(); !strings.Contains(msg, "workspace folders") {
		t.Errorf("LastScanError = %q, want the failed workspace folder listing", msg)
	}
}

// A job in a workspace folder lands in the mirror of that folder under the
// download folder, or straight in it when the structure is flattened.
func TestDownloadJob_MirrorsTheWorkspaceFolder(t *testing.T) {
	for _, flatten := range []bool{false, true} {
		const jobID = "mirror1"
		dir := t.TempDir()
		srv := fakeJobFilesServer(t, jobID, nil, nil)
		d := newDownloadTestDaemon(t, srv.URL, dir, nil)
		d.cfg.FlattenFolderStructure = flatten

		outcome := runDownloadJob(t, d, &CompletedJob{ID: jobID, Name: "job", FolderPath: []string{"Team", "Sub"}}, 20*time.Second)
		want := filepath.Join(dir, "Team", "Sub", "job_"+jobID)
		if flatten {
			want = filepath.Join(dir, "job_"+jobID)
		}
		if info, err := os.Stat(want); outcome != OutcomeNoFiles || err != nil || !info.IsDir() {
			t.Errorf("flatten %v: outcome %s, %s: %v; want the job's folder there", flatten, outcome, want, err)
		}
	}
}

// A folder name from the server that could not name a folder here, or that
// leads out of the download folder, fails the job with the reason, as any
// other refused server name does, instead of landing it somewhere else.
func TestDownloadJob_RefusesAWorkspaceFolderPathItCannotMirror(t *testing.T) {
	for _, tc := range []struct {
		name string
		path []string
		why  string
	}{
		{"dot dot", []string{".."}, "only dots"},
		{"slash", []string{"a/b"}, "path separators"},
		{"backslash", []string{`a\b`}, "path separators"},
		{"colon", []string{"Q1: results"}, "cannot contain"},
		{"device name", []string{"Team", "CON"}, "device name"},
		{"trailing dot", []string{"Team."}, "end in a dot"},
		{"symlinked parent", []string{"Linked", "Sub"}, "escapes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const jobID = "refused1"
			dir, elsewhere := t.TempDir(), t.TempDir()
			if tc.path[0] == "Linked" {
				if err := os.Symlink(elsewhere, filepath.Join(dir, "Linked")); err != nil {
					t.Skipf("cannot make a symbolic link here: %v", err)
				}
			}
			srv := fakeJobFilesServer(t, jobID, nil, nil)
			d := newDownloadTestDaemon(t, srv.URL, dir, nil)

			outcome := runDownloadJob(t, d, &CompletedJob{ID: jobID, Name: "job", FolderPath: tc.path}, 20*time.Second)
			failed := d.state.GetFailedJobs()
			if outcome != OutcomeOutputDirCreateFailed || len(failed) != 1 || !strings.Contains(failed[0].Error, tc.why) {
				t.Fatalf("outcome %s, failures %+v; want the job failed, saying %q", outcome, failed, tc.why)
			}
			for _, root := range []string{dir, elsewhere} {
				if entries, _ := os.ReadDir(root); len(entries) > 1 || (len(entries) == 1 && entries[0].Name() != "Linked") {
					t.Errorf("%s holds %v; nothing should have been created", root, entries)
				}
			}
		})
	}
}

// A job's own "Auto Download Path" still decides where it lands, so a folder
// that cannot be mirrored does not stop it.
func TestDownloadJob_OwnDownloadPathWinsOverAnUnmirrorableFolder(t *testing.T) {
	const jobID = "custom1"
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/jobs/" + jobID + "/custom-fields/":
			_ = json.NewEncoder(w).Encode(map[string]any{config.AutoDownloadPathFieldName: map[string]any{"value": "Mine"}})
		case "/api/v2/jobs/" + jobID + "/files/":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []models.JobFile{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	d := newDownloadTestDaemon(t, srv.URL, dir, &EligibilityConfig{IncludeWorkspaceFolders: true})

	outcome := runDownloadJob(t, d, &CompletedJob{ID: jobID, Name: "job", FolderPath: []string{"CON"}}, 20*time.Second)
	if _, err := os.Stat(filepath.Join(dir, "Mine", "job_"+jobID)); outcome != OutcomeNoFiles || err != nil {
		t.Errorf("outcome %s, %v; want the job in its own download path", outcome, err)
	}
}
