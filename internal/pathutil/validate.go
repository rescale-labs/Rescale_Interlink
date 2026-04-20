package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/rescale/rescale-int/internal/ipc"
)

// PathConsumer identifies the identity that will ultimately read/write a path.
// The consumer affects validation strictness: on Windows, paths consumed by
// the Windows Service (SYSTEM) cannot see per-user drive-letter mappings.
type PathConsumer int

const (
	// ConsumerCurrentUser is the GUI subprocess, CLI, or File Browser — any
	// operation running as the interactive user.
	ConsumerCurrentUser PathConsumer = iota

	// ConsumerWindowsService is a path that will be read by the Windows
	// Service (SYSTEM account). Triggers the strict mapped-drive check.
	ConsumerWindowsService
)

// PathValidationResult reports whether a path is reachable, the resolved
// canonical path, and a structured error when not.
type PathValidationResult struct {
	// Reachable is true when the path exists (or can be created) and is
	// writable from the consuming identity.
	Reachable bool

	// ResolvedPath is the canonical path after symlink/junction resolution,
	// and after mapped-drive → UNC conversion on Windows when applicable.
	ResolvedPath string

	// ErrorCode is the canonical ipc.ErrorCode set when Reachable is false;
	// empty when validation succeeded.
	ErrorCode ipc.ErrorCode

	// Reason is the human-readable detail paired with ErrorCode.
	Reason string

	// WasUNC is true when ResolvedPath was obtained by resolving a mapped
	// drive letter to its underlying UNC path.
	WasUNC bool
}

// ValidateWritablePath checks that path is reachable and writable from the
// identity that will consume it.
//
// On Windows the strict Service-SYSTEM check runs regardless of the supplied
// consumer, because a user may install the service after configuring the
// folder — the current runtime mode is not a reliable gate. On macOS and
// Linux, the consumer argument is honored (there is no mapped-drive concept
// to worry about, so the distinction is only relevant for future extensions).
//
// Empty paths return Reachable=true; the caller decides whether empty is
// acceptable for its context.
func ValidateWritablePath(path string, consumer PathConsumer) PathValidationResult {
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

	// Windows always runs the strict service check; see function doc.
	if runtime.GOOS == "windows" {
		_ = consumer // acknowledge unused arg on Windows
		return validateWindowsStrict(resolved)
	}

	return probeWritable(resolved)
}

// probeWritable attempts to write and remove a small marker file in the
// target directory, creating the directory if it does not yet exist.
func probeWritable(dir string) PathValidationResult {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return PathValidationResult{
			ResolvedPath: dir,
			ErrorCode:    ipc.CodeDownloadFolderInaccessible,
			Reason:       fmt.Sprintf("Cannot create folder: %v", err),
		}
	}

	info, err := os.Stat(dir)
	if err != nil {
		return PathValidationResult{
			ResolvedPath: dir,
			ErrorCode:    ipc.CodeDownloadFolderInaccessible,
			Reason:       fmt.Sprintf("Cannot access folder: %v", err),
		}
	}
	if !info.IsDir() {
		return PathValidationResult{
			ResolvedPath: dir,
			ErrorCode:    ipc.CodeDownloadFolderInaccessible,
			Reason:       "Path exists but is not a directory",
		}
	}

	marker := filepath.Join(dir, ".interlink_write_test")
	f, err := os.Create(marker)
	if err != nil {
		return PathValidationResult{
			ResolvedPath: dir,
			ErrorCode:    ipc.CodeDownloadFolderInaccessible,
			Reason:       fmt.Sprintf("Cannot write to folder: %v", err),
		}
	}
	_ = f.Close()
	_ = os.Remove(marker)

	return PathValidationResult{Reachable: true, ResolvedPath: dir}
}
