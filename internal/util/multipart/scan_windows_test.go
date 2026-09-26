//go:build windows

package multipart

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// A bare drive ("C:") names that drive's current directory, and "C:proj" a
// folder under it. os.DirFS reads the bare drive as the drive's root instead, so
// before roots were resolved the scan matched in one folder and reported
// another. The run folder's name is unique, so the drive's root cannot hold it.
func TestScanDirectories_DriveRelativeRoots(t *testing.T) {
	base := resolvedTempDir(t)
	run := fmt.Sprintf("Run_%d", time.Now().UnixNano())
	mkdirs(t, base, []string{run, filepath.Join("proj", run)}, nil)
	t.Chdir(base)
	vol := filepath.VolumeName(base)

	for _, tt := range []struct{ root, want string }{
		{vol, filepath.Join(base, run)},
		{vol + "proj", filepath.Join(base, "proj", run)},
	} {
		for _, opts := range []ScanOpts{{SingleDir: tt.root}, {PartDirs: []string{tt.root}}} {
			opts.Pattern, opts.BaseJobName, opts.StartIndex = run, "job", 1
			results, err := ScanDirectories(opts)
			if err != nil || len(results) != 1 || results[0].Directory != tt.want {
				t.Errorf("root %q, multi-part %v: %+v, %v; want %s",
					tt.root, len(opts.PartDirs) > 0, results, err, tt.want)
			}
		}
	}
}
