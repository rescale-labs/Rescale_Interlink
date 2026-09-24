//go:build !windows

package pathutil

import "errors"

// There are no drive letters off Windows, so only tests reach these, and they
// install their own answers. Unanswered, a drive path is refused.
var (
	wnetResolver      = func(string) (string, error) { return "", errors.ErrUnsupported }
	driveTypeResolver = func(string) uint32 { return 0 } // DRIVE_UNKNOWN
)
