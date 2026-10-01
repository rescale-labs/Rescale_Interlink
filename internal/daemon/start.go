package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/ipc"
)

// ErrLaunch marks a daemon process that could not be started, as against a
// start refused over the user's settings, so that each surface can word the
// two its own way.
var ErrLaunch = errors.New("failed to start daemon process")

var errNoAPIKey = errors.New(ipc.CanonicalText[ipc.CodeNoAPIKey] + ". " + ipc.HintFor(ipc.CodeNoAPIKey))

// launch starts the daemon process; see launchDetached. A variable so a test
// can see what would run without running it.
var launch = launchDetached

// Start starts this user's auto-download daemon, 'daemon run --ipc' logging to
// the daemon log, for the app and the tray. The daemon reads daemon.conf
// itself; conf is that file as the caller loaded it. Start first refuses what
// the detached daemon would refuse where no one sees why: a download_folder or
// max_concurrent that 'daemon run' does not take, a download folder it cannot
// create, and no API key.
func Start(conf *config.DaemonConfig) error {
	if err := config.CheckDownloadFolder(conf.Daemon.DownloadFolder); err != nil {
		return err
	}
	if err := CheckMaxConcurrent(conf.Daemon.MaxConcurrent, "max_concurrent in daemon.conf"); err != nil {
		return err
	}
	folder := conf.Daemon.DownloadFolder
	if folder == "" {
		folder = config.DefaultDownloadFolder()
	}
	if err := os.MkdirAll(folder, 0755); err != nil {
		return fmt.Errorf("cannot create download folder: %w", err)
	}
	if config.ResolveAPIKey("") == "" {
		return errNoAPIKey
	}
	cli, err := cliPath()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLaunch, err)
	}
	logs := config.LogDirectory()
	if err := os.MkdirAll(logs, 0700); err != nil {
		WriteStartupLog("WARNING: Could not create logs directory: %v", err)
	}
	return launch(cli, "daemon", "run", "--ipc", "--log-file", filepath.Join(logs, config.DaemonLogName))
}

// cliPath is the rescale-int that runs the daemon: on Windows the one beside
// this program, since the app and the tray are programs of their own, and
// elsewhere this program itself.
func cliPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("executable path: %w", err)
	}
	if runtime.GOOS != "windows" {
		return exe, nil
	}
	cli := filepath.Join(filepath.Dir(exe), "rescale-int.exe")
	if _, err := os.Stat(cli); os.IsNotExist(err) {
		return "", fmt.Errorf("CLI not found: %s", cli)
	} else if err != nil {
		return "", err // access denied reads as such, not as a missing CLI
	}
	return cli, nil
}
