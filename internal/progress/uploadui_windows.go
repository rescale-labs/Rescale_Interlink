//go:build windows
// +build windows

package progress

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableWindowsANSI enables Virtual Terminal processing on Windows terminals
// This allows ANSI escape sequences (colors, cursor movement) to work properly
func enableWindowsANSI(f *os.File) {
	handle := windows.Handle(f.Fd())
	var mode uint32

	// Get current console mode
	if err := windows.GetConsoleMode(handle, &mode); err == nil {
		// Enable Virtual Terminal Processing (0x0004)
		const ENABLE_VIRTUAL_TERMINAL_PROCESSING = 0x0004
		_ = windows.SetConsoleMode(handle, mode|ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}
