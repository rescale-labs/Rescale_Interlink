//go:build windows

package localfs

import "os"

// dirIdentity uniquely identifies a directory.
// On Windows, inode-based identification is not available through os.FileInfo.
type dirIdentity struct {
	Dev uint64
	Ino uint64
}

// getDirIdentity returns a unique identity for a directory.
// On Windows, this always returns false — symlink following falls back to
// not following (safe default). Windows symlinks require admin privileges
// and are rare in practice.
func getDirIdentity(_ os.FileInfo) (dirIdentity, bool) {
	return dirIdentity{}, false
}
