//go:build windows

package coordinator

import (
	"context"
	"net"

	"github.com/rescale/rescale-int/internal/ipc"
)

// dial connects to this user's coordinator pipe, and to no pipe another user
// created under its name.
func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	return ipc.DialUserPipe(ctx, c.socketPath)
}
