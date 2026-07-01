package daemon

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// processInfo names the executable of this user's process with this PID and
// the arguments it was started with, and fails for another user's process. An
// executable replaced since it started, as an upgrade replaces it, reads with
// " (deleted)" after its path.
func processInfo(pid int) (string, []string, error) {
	dir := fmt.Sprintf("/proc/%d", pid)
	info, err := os.Stat(dir)
	if err != nil {
		return "", nil, err
	}
	if uid := info.Sys().(*syscall.Stat_t).Uid; int(uid) != os.Getuid() {
		return "", nil, fmt.Errorf("it runs as user ID %d", uid)
	}
	path, err := os.Readlink(dir + "/exe")
	if err != nil {
		return "", nil, err
	}
	cmdline, err := os.ReadFile(dir + "/cmdline")
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSuffix(path, " (deleted)"), strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00"), nil
}
