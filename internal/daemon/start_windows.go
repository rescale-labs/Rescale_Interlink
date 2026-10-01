//go:build windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/rescale/rescale-int/internal/config"
)

// launchDetached starts the daemon in a process group of its own, with no
// console window, appending its stderr to the daemon's stderr log, where a
// daemon that stops at once says why. Appending keeps what the daemon of a
// start at the same moment wrote there.
func launchDetached(cli string, args ...string) error {
	WriteStartupLog("=== STARTUP ATTEMPT (%s) ===", filepath.Base(os.Args[0]))
	WriteStartupLog("CLI path: %s", cli)
	WriteStartupLog("Arguments: %v", args)
	cmd := exec.Command(cli, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
	if stderr, err := os.OpenFile(filepath.Join(config.LogDirectory(), config.DaemonStderrLogName), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600); err == nil {
		defer stderr.Close() // the daemon has a handle of its own
		cmd.Stderr = stderr
	} else {
		WriteStartupLog("WARNING: Could not open stderr capture file: %v", err)
	}
	if err := cmd.Start(); err != nil {
		WriteStartupLog("ERROR: cmd.Start() failed: %v", err)
		return fmt.Errorf("%w: %w", ErrLaunch, err)
	}
	WriteStartupLog("SUCCESS: Started daemon subprocess with PID %d", cmd.Process.Pid)
	cmd.Process.Release()
	return nil
}
