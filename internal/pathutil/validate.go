package pathutil

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rescale/rescale-int/internal/ipc"
)

// PathValidationResult reports whether a path is reachable, and a structured
// error when not.
type PathValidationResult struct {
	// Reachable is true when the path exists (or can be created) and is
	// writable.
	Reachable bool

	// ErrorCode is the canonical ipc.ErrorCode set when Reachable is false;
	// empty when validation succeeded.
	ErrorCode ipc.ErrorCode

	// Reason is the human-readable detail paired with ErrorCode.
	Reason string
}

// ValidateWritablePath checks that this user can create and write path, as
// the user's own daemon will. A mapped drive the user can see is accepted.
//
// Empty paths return Reachable=true; the caller decides whether empty is
// acceptable for its context.
func ValidateWritablePath(path string) PathValidationResult {
	if path == "" {
		return PathValidationResult{Reachable: true}
	}

	resolved, err := ResolveAbsolutePath(path)
	if err != nil {
		return PathValidationResult{
			ErrorCode: ipc.CodeDownloadFolderInaccessible,
			Reason:    fmt.Sprintf("Cannot resolve path: %v", err),
		}
	}

	return probeWritable(resolved)
}

// probeWritable attempts to write and remove a small marker file in the
// target directory, creating the directory if it does not yet exist.
func probeWritable(dir string) PathValidationResult {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return PathValidationResult{
			ErrorCode: ipc.CodeDownloadFolderInaccessible,
			Reason:    fmt.Sprintf("Cannot create folder: %v", err),
		}
	}

	info, err := os.Stat(dir)
	if err != nil {
		return PathValidationResult{
			ErrorCode: ipc.CodeDownloadFolderInaccessible,
			Reason:    fmt.Sprintf("Cannot access folder: %v", err),
		}
	}
	if !info.IsDir() {
		return PathValidationResult{
			ErrorCode: ipc.CodeDownloadFolderInaccessible,
			Reason:    "Path exists but is not a directory",
		}
	}

	marker := filepath.Join(dir, ".interlink_write_test")
	f, err := os.Create(marker)
	if err != nil {
		return PathValidationResult{
			ErrorCode: ipc.CodeDownloadFolderInaccessible,
			Reason:    fmt.Sprintf("Cannot write to folder: %v", err),
		}
	}
	_ = f.Close()
	_ = os.Remove(marker)

	return PathValidationResult{Reachable: true}
}
