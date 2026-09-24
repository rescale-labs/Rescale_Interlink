//go:build windows

package coordinator

import (
	"fmt"
	"net"
	"os"
	"testing"

	"github.com/Microsoft/go-winio"
)

// testEndpoint returns a named pipe of the test's own: the Windows client dials
// a pipe, never a Unix socket.
func testEndpoint(t *testing.T) string {
	return fmt.Sprintf(`\\.\pipe\rescale-coordinator-test-%d-%s`, os.Getpid(), t.Name())
}

// listenTest listens where the client dials on this platform: a pipe in the
// message mode Listen uses, which decides the connection type the client gets.
func listenTest(endpoint string) (net.Listener, error) {
	return winio.ListenPipe(endpoint, &winio.PipeConfig{
		MessageMode:      true,
		InputBufferSize:  65536,
		OutputBufferSize: 65536,
	})
}
