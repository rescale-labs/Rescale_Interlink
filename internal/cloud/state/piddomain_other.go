//go:build !linux && !darwin && !windows

package state

import (
	"fmt"
	"runtime"
)

// readPIDDomain reports that nothing here names the set of processes a PID
// belongs to. Locks are still created and released; none is ever reclaimed
// automatically, and each abandoned one costs a manual delete — which is the
// conservative direction, and the only honest one on a platform this program
// has no identifier for.
func readPIDDomain() (string, error) {
	return "", fmt.Errorf("no identifier on %s names the set of processes a PID belongs to", runtime.GOOS)
}
