//go:build windows

package coordinator

import (
	"net"

	"github.com/rescale/rescale-int/internal/ipc"
)

// pipeBase is the coordinator's pipe; each user's has their SID appended. A
// variable so a test can listen on a pipe of its own.
var pipeBase = "rescale-ratelimit-coordinator"

// SocketPath returns this user's coordinator pipe. Each signed-in user runs
// their own coordinator, as on macOS and Linux.
func SocketPath() (string, error) {
	return ipc.UserPipeName(pipeBase)
}

// Listen creates this user's coordinator pipe; see ipc.ListenUserPipe.
func Listen() (net.Listener, error) {
	name, err := SocketPath()
	if err != nil {
		return nil, err
	}
	return ipc.ListenUserPipe(name, 65536)
}

// CleanupSocket is a no-op on Windows (named pipes are cleaned up automatically).
func CleanupSocket() {}
