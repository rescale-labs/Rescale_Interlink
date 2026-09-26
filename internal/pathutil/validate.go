package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
// On Windows a path the service would read gets the strict SYSTEM check; any
// other path, like one the user's own daemon uses, is probed as the user, so a
// mapped drive the user can see is accepted. Elsewhere every path is probed.
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

	if onWindows && consumer == ConsumerWindowsService {
		return serviceCheck(resolved)
	}

	return probeWritable(resolved)
}

// onWindows and serviceCheck are variables so a test can take the Windows
// branch on any system.
var onWindows, serviceCheck = runtime.GOOS == "windows", validateWindowsStrict

// The drive types GetDriveType reports for local volumes and network drives,
// mirrored as plain numbers because golang.org/x/sys/windows builds on Windows
// alone and validateWindowsStrict is compiled and tested everywhere.
const (
	driveRemovable = 2 // DRIVE_REMOVABLE
	driveFixed     = 3 // DRIVE_FIXED
	driveRemote    = 4 // DRIVE_REMOTE
	driveCDROM     = 5 // DRIVE_CDROM
	driveRAMDisk   = 6 // DRIVE_RAMDISK
)

// validateWindowsStrict runs the Windows Service-SYSTEM strictness check.
// SYSTEM cannot see the drive letters a user maps to network shares, so a path
// on a network drive must resolve to its UNC path via WNetGetUniversalName, or
// it is refused. A local drive is probed as it is: WNetGetUniversalName answers
// ERROR_NOT_CONNECTED for any drive that is not redirected, so asking it about
// a local drive would refuse that drive. Any other drive is refused, since
// nothing says SYSTEM can reach it.
func validateWindowsStrict(resolved string) PathValidationResult {
	if len(resolved) < 2 || resolved[1] != ':' {
		// Already UNC or a non-drive path; skip the WNet step.
		return probeWritable(resolved)
	}

	switch driveTypeResolver(resolved[:2] + `\`) {
	case driveRemovable, driveFixed, driveCDROM, driveRAMDisk:
		return probeWritable(resolved)
	case driveRemote: // resolved through WNet below
	default:
		return PathValidationResult{
			ResolvedPath: resolved,
			ErrorCode:    ipc.CodeDownloadFolderInaccessible,
			Reason: fmt.Sprintf(
				"Windows does not recognize drive %s as a local or network drive. Use a local path or a UNC path (\\\\server\\share) instead.",
				resolved[:2],
			),
		}
	}

	unc, err := wnetResolver(resolved)
	if err != nil {
		return PathValidationResult{
			ResolvedPath: resolved,
			ErrorCode:    ipc.CodeDownloadFolderInaccessible,
			Reason: fmt.Sprintf(
				"Drive %s is a user-session mapping and is not reachable from the Windows Service (SYSTEM): %s. Use a UNC path (\\\\server\\share) or a local path instead.",
				resolved[:2], strings.TrimSuffix(err.Error(), "."),
			),
		}
	}

	// Probe the resolved UNC for writability, but report the user-entered
	// path as the resolved path (spec §13.4 forbids silent rewrite).
	probe := probeWritable(unc)
	if !probe.Reachable {
		probe.ResolvedPath = resolved
		return probe
	}
	return PathValidationResult{
		Reachable:    true,
		ResolvedPath: unc,
		WasUNC:       true,
	}
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
