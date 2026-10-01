package daemon

import (
	"strings"
	"testing"
)

// The job's folder is named after its ID from the server, in either naming
// mode, so an ID that is not one could put the folder, and the job's files,
// outside the download folder. Where the job would land is refused with the
// reason, which poll then fails the job for before claiming it.
func TestJobBaseDir_RefusesAJobIDThatIsNotAnID(t *testing.T) {
	d := &Daemon{cfg: &Config{DownloadDir: t.TempDir()}}
	for _, id := range []string{"x/../../escaped", "/../.."} {
		if _, err := d.jobBaseDir(&CompletedJob{ID: id, Name: "job"}, ""); err == nil || !strings.HasPrefix(err.Error(), "invalid job ID: ") {
			t.Errorf("%q: %v, want the job refused for its ID", id, err)
		}
	}
}
