//go:build !windows

package localfs

import (
	"os"
	"syscall"
)

// dirIdentity uniquely identifies a directory by device+inode.
// Used for symlink ancestry-based cycle detection.
type dirIdentity struct {
	Dev uint64
	Ino uint64
}

// getDirIdentity returns a unique identity for a directory from its FileInfo.
// Returns false if the identity cannot be determined.
func getDirIdentity(info os.FileInfo) (dirIdentity, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return dirIdentity{}, false
	}
	return dirIdentity{Dev: uint64(stat.Dev), Ino: stat.Ino}, true
}
