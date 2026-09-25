//go:build !windows

package wailsapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/daemon"
)

// Start says why the daemon cannot start instead of spawning one that refuses:
// a PID file it cannot read is no evidence that no daemon runs, and 'daemon run'
// refuses a max_concurrent out of its range.
func TestStartDaemonSaysWhyItCannotStart(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func() error
	}{
		{"a PID file it cannot read", "failed to read PID file", func() error {
			return os.MkdirAll(daemon.PIDFilePath(), 0o700) // a folder where the file belongs
		}},
		{"a max_concurrent daemon run refuses", "max_concurrent in daemon.conf must be between 1 and 20, got 50", func() error {
			conf, err := config.DefaultDaemonConfigPath()
			if err == nil {
				os.MkdirAll(filepath.Dir(conf), 0o700)
				err = os.WriteFile(conf, []byte("[daemon]\nmax_concurrent = 50\n"), 0o600)
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("RESCALE_API_KEY", "")
			if err := tc.setup(); err != nil {
				t.Fatal(err)
			}
			if err := (&App{}).StartDaemon(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("StartDaemon: %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
