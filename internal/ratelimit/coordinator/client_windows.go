//go:build windows

package coordinator

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// dial creates a connection to the coordinator via Windows named pipe.
func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	return winio.DialPipeContext(ctx, c.socketPath)
}
