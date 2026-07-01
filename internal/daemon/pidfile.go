// Package daemon provides background service functionality for auto-downloading completed jobs.
package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rescale/rescale-int/internal/cloud/state"
	"github.com/rescale/rescale-int/internal/reporting"
)

// lockFile takes the exclusive lock that claims of the PID file and rewrites of
// the state file are made under. The system releases it when its holder exits,
// however that happens, so a crash leaves nothing to clear. A variable so a
// test can see a second process wait for it.
var lockFile = lockExclusive

// pidClaimStep runs between writing a claim of the PID file and publishing it,
// where a second daemon starting at the same moment must be kept waiting. Only
// a test sets it.
var pidClaimStep = func() {}

// WritePIDFile claims the daemon PID file for this process.
//
// A claim judges the file already there and replaces it, and two daemons
// starting at once must not both do that: each could take the other's file for
// a stale one, or for none at all while it was still empty. So claims are made
// one at a time under a lock, a file is replaced only when the process it names
// has exited, and the new one is written whole and renamed into place, so it is
// never seen empty.
func WritePIDFile() error {
	if oldPath := oldPIDFilePath(); oldPath != "" {
		os.Remove(oldPath)
	}
	pidPath := PIDFilePath()
	if err := os.MkdirAll(filepath.Dir(pidPath), 0700); err != nil {
		return fmt.Errorf("failed to create PID file directory: %w", err)
	}
	unlock, err := lockFile(pidPath + ".lock")
	if err != nil {
		return fmt.Errorf("failed to lock PID file: %w", err)
	}
	defer unlock()

	if err := CheckPIDFile(); err != nil {
		return err
	}
	tmpPath := pidPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return fmt.Errorf("failed to write PID file: %w", err)
	}
	pidClaimStep()
	if err := os.Rename(tmpPath, pidPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write PID file: %w", err)
	}
	return nil
}

// CheckPIDFile refuses what a claim of the PID file refuses, so a process that
// makes no claim, as the parent of a background daemon does not, can refuse
// first. Only the claim, under its lock, settles the question.
func CheckPIDFile() error {
	// A file that cannot be read is no evidence that no daemon runs, and one
	// naming this very process was left by an earlier process with its PID.
	pid, err := readPIDFile()
	if err != nil {
		return fmt.Errorf("failed to read PID file: %w", err)
	}
	if exited, _ := state.ProcessExited(pid); !exited && pid != os.Getpid() {
		return reporting.UsageError(fmt.Errorf("daemon is already running (PID %d)", pid))
	}
	return nil
}

// RemovePIDFile removes the PID file if it is still this process's claim. No
// other process replaces the claim of one that is running.
func RemovePIDFile() {
	if ReadPIDFile() == os.Getpid() {
		os.Remove(PIDFilePath())
	}
}

// ReadPIDFile reads the PID from the PID file.
// Returns 0 if the file doesn't exist or is invalid.
func ReadPIDFile() int {
	pid, _ := readPIDFile()
	return pid
}

// readPIDFile is ReadPIDFile, but fails for a file that is there and could not
// be read.
func readPIDFile() (int, error) {
	data, err := os.ReadFile(PIDFilePath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return 0, nil // no file, or a corrupt one: it names no daemon
	}

	return pid, nil
}

// IsDaemonRunning checks if a daemon process is already running.
// Returns the PID if running, 0 if not. The process counts as running unless
// the system says it has exited: a probe the system refuses, as it does for
// another user's process, is not evidence that it is gone. The upload lock
// judges its owners with the same probe.
func IsDaemonRunning() int {
	pid := ReadPIDFile()
	if pid == 0 {
		return 0
	}
	if exited, _ := state.ProcessExited(pid); exited {
		return 0
	}
	return pid
}

// KillDaemon ends the daemon process with this PID, for when 'daemon stop'
// cannot shut it down over IPC, and returns once the system says it has exited.
// A PID file can outlive its daemon and the PID be reused, so before each
// signal it checks that the PID names this user's process, started with
// 'daemon run' from the executable a daemon runs as: the CLI, or on macOS and
// Linux the GUI, which runs the daemon itself. It ends nothing else. It asks
// first (SIGTERM, which the daemon handles as a shutdown) and forces the end
// (SIGKILL) only if the process is still there after wait; on Windows both are
// TerminateProcess. A variable so a test can stand in for it.
var KillDaemon = killDaemon

func killDaemon(pid int, wait time.Duration) error {
	if pid <= 0 { // to kill(2), these name groups of processes
		return reporting.UsageError(fmt.Errorf("PID %d names no daemon process", pid))
	}
	exited := func() bool { gone, _ := state.ProcessExited(pid); return gone }
	for _, force := range []bool{false, true} {
		if exited() {
			return nil
		}
		if err := checkDaemonProcess(pid); err != nil {
			return reporting.UsageError(err)
		}
		if err := endProcess(pid, force); err != nil {
			return fmt.Errorf("cannot end PID %d: %w", pid, err)
		}
		for deadline := time.Now().Add(wait); !exited() && time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if exited() {
		return nil
	}
	return reporting.UsageError(fmt.Errorf("PID %d had not exited %s after it was forced to end", pid, wait))
}

// checkDaemonProcess refuses a PID that does not name this user's daemon. The
// arguments are not quoted back, since they can carry an API key.
func checkDaemonProcess(pid int) error {
	image, args, err := processInfo(pid)
	if err != nil {
		return fmt.Errorf("cannot tell whether PID %d is this user's daemon, so it was not ended: %w", pid, err)
	}
	if name := strings.TrimSuffix(strings.ToLower(filepath.Base(image)), ".exe"); name != "rescale-int" && (name != "rescale-int-gui" || runtime.GOOS == "windows") {
		return fmt.Errorf("PID %d is not an Interlink daemon, so it was not ended: it runs %s", pid, image)
	}
	for i := 1; i < len(args); i++ {
		if args[i-1] == "daemon" && args[i] == "run" {
			return nil
		}
	}
	return fmt.Errorf("PID %d is not an Interlink daemon, so it was not ended: it runs %s, not 'daemon run'", pid, image)
}
