package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/ipc"
)

// isolateStart gives the test a profile of its own, with no API key in it,
// and fails it if Start goes past its checks: off Windows the program Start
// would run is the test binary itself.
func isolateStart(t *testing.T) string {
	t.Helper()
	home := isolateHome(t)
	t.Setenv("RESCALE_API_KEY", "")
	orig := launch
	launch = func(cli string, args ...string) error {
		t.Fatalf("Start went past its checks and ran %s %q", cli, args)
		return nil
	}
	t.Cleanup(func() { launch = orig })
	return home
}

// Start refuses what the detached daemon would refuse where no one sees why,
// before it logs or starts anything; past its checks on Windows, it finds no
// rescale-int.exe beside the test.
func TestStartSaysWhyItCannotStart(t *testing.T) {
	home := isolateStart(t)
	notAFolder := filepath.Join(home, "a-file")
	if err := os.WriteFile(notAFolder, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		folder string
		max    int
		want   string
	}{
		{"rel", 5, `download_folder in daemon.conf must be an absolute path, got "rel"`},
		{home, -1, "max_concurrent in daemon.conf must be between 1 and 20, got -1"},
		{home, 0, "max_concurrent in daemon.conf must be between 1 and 20, got 0"},
		{home, 21, "max_concurrent in daemon.conf must be between 1 and 20, got 21"},
		{filepath.Join(notAFolder, "jobs"), 5, "cannot create download folder"},
		{home, 1, ipc.CanonicalText[ipc.CodeNoAPIKey]},
		{home, 20, ipc.CanonicalText[ipc.CodeNoAPIKey]},
		{"", 5, ipc.CanonicalText[ipc.CodeNoAPIKey]},
	} {
		conf := config.NewDaemonConfig()
		conf.Daemon.DownloadFolder, conf.Daemon.MaxConcurrent = tc.folder, tc.max
		if err := Start(conf); err == nil || !strings.Contains(err.Error(), tc.want) || errors.Is(err, ErrLaunch) {
			t.Errorf("Start with folder %q, max_concurrent %d: %v, want a refusal saying %q", tc.folder, tc.max, err, tc.want)
		}
	}
	if _, err := os.Stat(config.LogDirectory()); !os.IsNotExist(err) {
		t.Errorf("a refused start wrote to %s", config.LogDirectory())
	}
	if runtime.GOOS == "windows" {
		t.Setenv("RESCALE_API_KEY", "FAKE-KEY")
		if err := Start(config.NewDaemonConfig()); !errors.Is(err, ErrLaunch) || !strings.Contains(err.Error(), "CLI not found") {
			t.Errorf("Start with no rescale-int.exe beside it: %v, want it to say so", err)
		}
	}
}

// Start runs 'daemon run --ipc' with the daemon log and nothing from
// daemon.conf, which the daemon reads itself: a setting passed as a flag
// skips the daemon's own checks and defaults, as --poll-interval 0m did.
func TestStartLeavesDaemonConfToTheDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no rescale-int.exe beside the test to start")
	}
	isolateStart(t)
	t.Setenv("RESCALE_API_KEY", "FAKE-KEY")
	var got []string
	orig := launch
	t.Cleanup(func() { launch = orig })
	launch = func(cli string, args ...string) error {
		got = append([]string{cli}, args...)
		return nil
	}
	conf := config.NewDaemonConfig()
	conf.Daemon.PollIntervalMinutes, conf.Filters.NamePrefix = 0, "FAKE"
	if err := Start(conf); err != nil {
		t.Fatalf("Start: %v", err)
	}
	exe, _ := os.Executable()
	if want := []string{exe, "daemon", "run", "--ipc", "--log-file", filepath.Join(config.LogDirectory(), config.DaemonLogName)}; !slices.Equal(got, want) {
		t.Errorf("Start ran %q, want %q", got, want)
	}
	if info, err := os.Stat(config.LogDirectory()); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("log folder: %v, %v; want it made, private", info, err)
	}
}
