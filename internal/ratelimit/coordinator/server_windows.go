//go:build windows

package coordinator

import (
	"net"

	"github.com/Microsoft/go-winio"
)

// PipeName is the Windows named pipe path for the rate limit coordinator.
const PipeName = `\\.\pipe\rescale-ratelimit-coordinator`

// SocketPath returns the coordinator's communication endpoint path.
// On Windows, this is a named pipe path.
func SocketPath() string {
	return PipeName
}

// Listen creates a Windows named pipe listener for the coordinator.
func Listen() (net.Listener, error) {
	cfg := &winio.PipeConfig{
		// Allow authenticated users
		SecurityDescriptor: "D:P(A;;GA;;;AU)",
		MessageMode:        true,
		InputBufferSize:    65536,
		OutputBufferSize:   65536,
	}

	return winio.ListenPipe(PipeName, cfg)
}

// CleanupSocket is a no-op on Windows (named pipes are cleaned up automatically).
func CleanupSocket() {}
