package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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

// With workspace folders on, jobs under the shared root join the user's own,
// each with its folder path. A job in both listings is scanned once, with its
// folder, so it lands in the mirror of that folder; a job in an archived folder
// is left out.
func TestFindCompletedJobs_WorkspaceFolders(t *testing.T) {
	p := newPlatform(t, &fakeJob{id: "own"}, &fakeJob{id: "both"},
		&fakeJob{id: "both", folder: "a"}, &fakeJob{id: "inroot", folder: "root"}, &fakeJob{id: "archived", folder: "old"})
	p.tree = map[string]any{"sharedWithWorkspace": map[string]any{"id": "root", "name": "Shared", "children": []any{
		map[string]any{"id": "a", "name": "A"},
		map[string]any{"id": "old", "name": "Old", "isArchived": true},
	}}}

	for _, include := range []bool{true, false} {
		result, err := p.monitor(nil, EligibilityConfig{IncludeWorkspaceFolders: include}).FindCompletedJobs(context.Background(), nil)
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
	m := newPlatform(t, &fakeJob{id: "own"}).monitor(nil, EligibilityConfig{IncludeWorkspaceFolders: true})
	result, err := m.FindCompletedJobs(context.Background(), nil)
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
	aged := func(id string, age time.Duration) *fakeJob {
		at := time.Now().Add(-age)
		return &fakeJob{id: id, folder: "a", created: at, completed: at.Add(time.Minute), tags: []string{config.DownloadedTag}}
	}
	// Completed within the lookback window, before it, and created too long
	// before it to have completed within it.
	p := newPlatform(t, aged("new", time.Hour), aged("finished", 10*24*time.Hour), aged("ancient", 60*24*time.Hour))
	p.tree = map[string]any{"sharedWithWorkspace": map[string]any{"id": "root", "name": "Shared", "children": []any{
		map[string]any{"id": "a", "name": "A"},
		map[string]any{"id": "old", "name": "Old", "isArchived": true},
		map[string]any{"id": "colon", "name": "Q1: results"},
	}}}

	for _, flatten := range []bool{false, true} {
		d := p.daemon(t.TempDir(), EligibilityConfig{LookbackDays: 7, IncludeWorkspaceFolders: true})
		var log bytes.Buffer
		d.logger = logging.NewLoggerWithWriter(&log)
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
	p := newPlatform(t, &fakeJob{id: "shared1", folder: "colon"})
	p.tree = map[string]any{"sharedWithWorkspace": map[string]any{"id": "root", "name": "Shared", "children": []any{
		map[string]any{"id": "colon", "name": "Q1: results"},
	}}}
	d := p.daemon(t.TempDir(), EligibilityConfig{IncludeWorkspaceFolders: true})

	d.poll(context.Background())
	failed := d.state.GetFailedJobs()
	if len(failed) != 1 || !strings.Contains(failed[0].Error, "cannot mirror") {
		t.Errorf("failures %+v; want the job failed, saying its folder cannot be mirrored", failed)
	}
	if writes := p.writes(); len(writes) != 0 {
		t.Errorf("tag writes %v; want the job refused before any tag went on it", writes)
	}
}

// A poll whose workspace folder listing failed says so where 'daemon status'
// and the app look, instead of reporting a healthy scan.
func TestPoll_RecordsAFailedWorkspaceListing(t *testing.T) {
	d := newPlatform(t).daemon(t.TempDir(), EligibilityConfig{IncludeWorkspaceFolders: true})

	d.poll(context.Background())
	if msg, _ := d.LastScanError(); !strings.Contains(msg, "workspace folders") {
		t.Errorf("LastScanError = %q, want the failed workspace folder listing", msg)
	}
}

// A job in a workspace folder lands in the mirror of that folder under the
// download folder, or straight in it when the structure is flattened.
func TestJobBaseDir_MirrorsTheWorkspaceFolder(t *testing.T) {
	for _, flatten := range []bool{false, true} {
		const jobID = "mirror1"
		dir := t.TempDir()
		d := newPlatform(t, &fakeJob{id: jobID}).daemon(dir, EligibilityConfig{})
		d.cfg.FlattenFolderStructure = flatten
		job := &CompletedJob{ID: jobID, Name: "job", FolderPath: []string{"Team", "Sub"}}

		base, err := d.jobBaseDir(job, "")
		if err != nil {
			t.Fatalf("flatten %v: jobBaseDir: %v", flatten, err)
		}
		outcome := d.downloadJob(context.Background(), job, base)
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
// leads out of the download folder, is refused with the reason, as any other
// refused server name is, instead of landing the job somewhere else; poll
// then fails the job for it before claiming it. Which names cannot name a
// folder is validation.ValidateFilename's to say.
func TestJobBaseDir_RefusesAWorkspaceFolderPathItCannotMirror(t *testing.T) {
	for _, tc := range []struct {
		name string
		path []string
		why  string
	}{
		{"a name that cannot name a folder", []string{"Q1: results"}, "cannot contain"},
		{"symlinked parent", []string{"Linked", "Sub"}, "escapes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.path[0] == "Linked" {
				if err := os.Symlink(t.TempDir(), filepath.Join(dir, "Linked")); err != nil {
					t.Skipf("cannot make a symbolic link here: %v", err)
				}
			}
			d := &Daemon{cfg: &Config{DownloadDir: dir}}
			if _, err := d.jobBaseDir(&CompletedJob{ID: "refused1", Name: "job", FolderPath: tc.path}, ""); err == nil || !strings.Contains(err.Error(), tc.why) {
				t.Errorf("%v; want the folder refused, saying %q", err, tc.why)
			}
		})
	}
}

// Eligibility reads a job's custom fields once, for its Auto Download field
// and for its own download path, where the job then lands: a poll that
// downloads the job reads them once.
func TestPoll_ReadsAJobsCustomFieldsOnce(t *testing.T) {
	shortenClaimSettle(t)
	p := newPlatform(t, &fakeJob{id: "own1", fields: map[string]string{config.AutoDownloadFieldName: "Enabled", config.AutoDownloadPathFieldName: "Mine"}})
	dir := t.TempDir()
	d := p.daemon(dir, EligibilityConfig{})

	d.poll(context.Background())
	var reads int
	p.set(func() { reads = p.fieldReads })
	if _, err := os.Stat(filepath.Join(dir, "Mine", "job_own1")); err != nil || reads != 1 {
		t.Errorf("the job's folder in its own download path: %v; custom fields read %d times, want once", err, reads)
	}
}

// The claim waits seconds before anything is written, in which a folder on
// the way to the job's destination can become a link out of the download
// folder: the destination is checked again after the claim, and the job is
// refused, with nothing written outside the download folder.
func TestPoll_ChecksTheDestinationAgainAfterTheClaim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("making a link needs a privilege Windows does not give every user")
	}
	shortenClaimSettle(t)
	p := newPlatform(t, &fakeJob{id: "own1", fields: map[string]string{config.AutoDownloadFieldName: "Enabled", config.AutoDownloadPathFieldName: "Mine"}})
	dir, elsewhere := t.TempDir(), t.TempDir()
	p.onClaim = func(string) { // the job's own folder becomes a link while the claim settles
		if err := os.Symlink(elsewhere, filepath.Join(dir, "Mine")); err != nil {
			t.Errorf("symlink: %v", err)
		}
	}
	d := p.daemon(dir, EligibilityConfig{})

	d.poll(context.Background())
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("the job was written outside the download folder: %v", entries)
	}
	if failed := d.state.GetFailedJobs(); len(failed) != 1 || !strings.Contains(failed[0].Error, "escapes") {
		t.Errorf("failures %+v; want the job refused for leaving the download folder", failed)
	}
}

// A job's own "Auto Download Path" still decides where it lands, so a folder
// that cannot be mirrored does not stop it.
func TestJobBaseDir_OwnDownloadPathWinsOverAnUnmirrorableFolder(t *testing.T) {
	dir := t.TempDir()
	d := &Daemon{cfg: &Config{DownloadDir: dir}, logger: logging.NewLoggerWithWriter(io.Discard)}
	base, err := d.jobBaseDir(&CompletedJob{ID: "custom1", Name: "job", FolderPath: []string{"CON"}}, "Mine")
	if real, _ := filepath.EvalSymlinks(dir); err != nil || base != filepath.Join(real, "Mine") {
		t.Errorf("base folder %q (%v), want the job's own download path in %s", base, err, real)
	}
}
