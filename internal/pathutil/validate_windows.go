//go:build windows

package pathutil

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/rescale/rescale-int/internal/ipc"
)

// wnetResolver is the injection point for WNetGetUniversalName. Production
// code uses wnetResolveReal; tests can swap in a fake.
var wnetResolver = wnetResolveReal

// universalNameInfoLevel selects the REMOTE_NAME_INFO layout returned by
// WNetGetUniversalNameW. See MSDN: UNIVERSAL_NAME_INFO_LEVEL = 1.
const universalNameInfoLevel = 1

// validateWindowsStrict runs the Windows Service-SYSTEM strictness check:
// drive-letter paths are resolved to UNC via WNetGetUniversalName. A drive
// that cannot be resolved by WNet (ERROR_NOT_CONNECTED / similar) is a
// user-session-only mapping that SYSTEM cannot see; the path is refused.
func validateWindowsStrict(resolved string) PathValidationResult {
	if len(resolved) < 2 || resolved[1] != ':' {
		// Already UNC or a non-drive path; skip the WNet step.
		return probeWritable(resolved)
	}

	unc, err := wnetResolver(resolved)
	if err != nil {
		return PathValidationResult{
			ResolvedPath: resolved,
			ErrorCode:    ipc.CodeDownloadFolderInaccessible,
			Reason: fmt.Sprintf(
				"Drive %s is a user-session mapping and is not reachable from the Windows Service (SYSTEM): %v. Use a UNC path (\\\\server\\share) or a local path instead.",
				resolved[:2], err,
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

// wnetResolveReal calls WNetGetUniversalNameW. Returns the UNC form of a
// mapped-drive path, or a non-nil error when the drive is not a network
// mapping or is not currently connected.
func wnetResolveReal(localPath string) (string, error) {
	modmpr := windows.NewLazySystemDLL("mpr.dll")
	proc := modmpr.NewProc("WNetGetUniversalNameW")

	localPtr, err := syscall.UTF16PtrFromString(localPath)
	if err != nil {
		return "", err
	}

	// Start with a 1 KiB buffer; enlarge on ERROR_MORE_DATA.
	var bufSize uint32 = 1024
	buf := make([]byte, bufSize)

	for attempts := 0; attempts < 3; attempts++ {
		ret, _, _ := proc.Call(
			uintptr(unsafe.Pointer(localPtr)),
			uintptr(universalNameInfoLevel),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&bufSize)),
		)
		switch ret {
		case 0:
			// REMOTE_NAME_INFO: first field is LPWSTR lpUniversalName.
			namePtr := *(**uint16)(unsafe.Pointer(&buf[0]))
			if namePtr == nil {
				return "", fmt.Errorf("WNetGetUniversalName returned nil UNC")
			}
			return windows.UTF16PtrToString(namePtr), nil
		case uintptr(windows.ERROR_MORE_DATA):
			buf = make([]byte, bufSize)
			continue
		default:
			return "", syscall.Errno(ret)
		}
	}
	return "", fmt.Errorf("WNetGetUniversalName exhausted retries")
}
