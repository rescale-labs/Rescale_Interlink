//go:build !windows

package wailsapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
)

// Saving settings takes what 'daemon run' takes: max_concurrent 1-20 and an
// absolute download folder, enabled or not. A refused save creates nothing: no
// folder where a relative one would resolve, and no daemon.conf.
func TestSaveDaemonConfigTakesWhatDaemonRunTakes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	t.Chdir(cwd)
	conf, err := config.DefaultDaemonConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	for _, tc := range []struct {
		folder  string
		enabled bool
		max     int
		want    string // the refusal, or "" when saved
	}{
		{folder, true, 0, "max_concurrent must be between 1 and 20"},
		{folder, true, 1, ""},
		{folder, true, 20, ""},
		{folder, true, 21, "max_concurrent must be between 1 and 20"},
		{"relative", true, 5, `download_folder must be an absolute path, got "relative"`},
		{"relative", false, 5, `download_folder must be an absolute path, got "relative"`},
	} {
		t.Run(fmt.Sprintf("%s,%v,%d", filepath.Base(tc.folder), tc.enabled, tc.max), func(t *testing.T) {
			os.Remove(conf)
			err := (&App{}).SaveDaemonConfig(DaemonConfigDTO{
				Enabled: tc.enabled, DownloadFolder: tc.folder, PollIntervalMinutes: 5, MaxConcurrent: tc.max, LookbackDays: 7,
			})
			if (err == nil) != (tc.want == "") || err != nil && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SaveDaemonConfig: %v, want %q", err, tc.want)
			}
			if tc.want == "" {
				return
			}
			if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
				t.Errorf("the refused save created %v in the working folder", entries)
			}
			if _, err := os.Stat(conf); !os.IsNotExist(err) {
				t.Errorf("the refused save wrote daemon.conf (stat: %v)", err)
			}
		})
	}
}
