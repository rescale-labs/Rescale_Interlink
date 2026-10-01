//go:build !windows

package daemon

import (
	"fmt"
	"os/exec"
)

// launchDetached runs 'daemon run --background', whose process starts the
// daemon, detached, and exits. Waiting for it leaves no zombie behind.
func launchDetached(cli string, args ...string) error {
	if err := exec.Command(cli, append(args, "--background")...).Run(); err != nil {
		return fmt.Errorf("%w: %w", ErrLaunch, err)
	}
	return nil
}
