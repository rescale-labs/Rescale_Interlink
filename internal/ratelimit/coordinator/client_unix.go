//go:build !windows

package coordinator

import (
	"context"
	"net"
)

// dial creates a connection to the coordinator via Unix domain socket.
func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	dialer := net.Dialer{Timeout: c.timeout}
	return dialer.DialContext(ctx, "unix", c.socketPath)
}
