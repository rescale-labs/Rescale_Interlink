//go:build windows

package resources

import (
	"syscall"
	"unsafe"

	"github.com/rescale/rescale-int/internal/constants"
)

// getAvailableMemory returns available system memory in bytes (Windows)
func getAvailableMemory() uint64 {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	globalMemoryStatusEx := kernel32.NewProc("GlobalMemoryStatusEx")

	type memoryStatusEx struct {
		dwLength                uint32
		dwMemoryLoad            uint32
		ullTotalPhys            uint64
		ullAvailPhys            uint64
		ullTotalPageFile        uint64
		ullAvailPageFile        uint64
		ullTotalVirtual         uint64
		ullAvailVirtual         uint64
		ullAvailExtendedVirtual uint64
	}

	var memInfo memoryStatusEx
	memInfo.dwLength = uint32(unsafe.Sizeof(memInfo))

	ret, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&memInfo)))
	if ret == 0 {
		// Fallback to conservative estimate if we can't get memory info
		return 2 * 1024 * 1024 * 1024 // 2GB
	}

	availableBytes := memInfo.ullAvailPhys

	// Ensure we have a reasonable minimum
	if availableBytes < constants.MinSystemMemory {
		availableBytes = constants.MinSystemMemory
	}

	return availableBytes
}
