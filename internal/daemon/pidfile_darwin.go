package daemon

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// processInfo names the executable of this user's process with this PID and
// the arguments it was started with, and fails for another user's process.
// kern.procargs2 holds the argument count, the path the process was started
// from, padding, and then the arguments.
func processInfo(pid int) (string, []string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", nil, err
	}
	if uid := info.Eproc.Pcred.P_ruid; int(uid) != os.Getuid() {
		return "", nil, fmt.Errorf("it runs as user ID %d", uid)
	}
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", nil, err
	}
	if len(buf) < 5 {
		return "", nil, errors.New("the system gave no executable path")
	}
	argc := int(binary.NativeEndian.Uint32(buf))
	path, rest, _ := bytes.Cut(buf[4:], []byte{0})
	args := strings.Split(string(bytes.TrimLeft(rest, "\x00")), "\x00")
	if len(args) < argc {
		return "", nil, errors.New("the system gave fewer arguments than it counted")
	}
	return string(path), args[:argc], nil
}
