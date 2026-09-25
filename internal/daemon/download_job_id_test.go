package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// The job's folder is named after its ID from the server, in either naming
// mode, so an ID that is not one could put the folder, and the job's files,
// outside the download folder. The job fails with the reason, and nothing is
// made anywhere.
func TestDownloadJob_RefusesAJobIDThatIsNotAnID(t *testing.T) {
	for _, tc := range []struct {
		id      string
		useName bool
	}{
		{"x/../../escaped", false},
		{"/../..", true},
	} {
		root := t.TempDir()
		dir := filepath.Join(root, "a", "downloads")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		srv := fakeJobFilesServer(t, tc.id, []models.JobFile{{ID: "f1", Name: "out.txt", RelativePath: "sub/out.txt", DecryptedSize: 1}}, nil)
		d := newDownloadTestDaemon(t, srv.URL, dir, nil)
		d.cfg.UseJobNameDir = tc.useName

		outcome := runDownloadJob(t, d, &CompletedJob{ID: tc.id, Name: "job"}, 20*time.Second)
		if entry := d.state.Downloaded[tc.id]; outcome != OutcomeOutputDirCreateFailed || entry == nil || !strings.HasPrefix(entry.Error, "invalid job ID: ") {
			t.Errorf("%q: outcome %s, state %+v; want the job failed for its ID", tc.id, outcome, entry)
		}
		var made []string
		_ = filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
			if rel, _ := filepath.Rel(root, path); err == nil && rel != "." && rel != "a" && rel != filepath.Join("a", "downloads") {
				made = append(made, rel)
			}
			return nil
		})
		if len(made) > 0 {
			t.Errorf("%q (job name folders %v): made %q", tc.id, tc.useName, made)
		}
	}
}
