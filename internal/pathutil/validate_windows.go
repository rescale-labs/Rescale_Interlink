//go:build windows

package pathutil

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// wnetResolver and driveTypeResolver are the injection points for
// WNetGetUniversalName and GetDriveType. Production code uses the real calls;
// tests swap in fakes.
var (
	wnetResolver      = wnetResolveReal
	driveTypeResolver = driveTypeReal
)

// universalNameInfoLevel selects the REMOTE_NAME_INFO layout returned by
// WNetGetUniversalNameW. See MSDN: UNIVERSAL_NAME_INFO_LEVEL = 1.
const universalNameInfoLevel = 1

// driveTypeReal calls GetDriveTypeW for a drive root such as `C:\`.
func driveTypeReal(root string) uint32 {
	rootPtr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return windows.DRIVE_UNKNOWN
	}
	return windows.GetDriveType(rootPtr)
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
