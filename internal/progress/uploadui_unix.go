//go:build !windows
// +build !windows

package progress

import "os"

// enableWindowsANSI is a no-op on non-Windows platforms
// ANSI escape sequences work natively on Unix-like systems
func enableWindowsANSI(f *os.File) {
	// No-op: Unix terminals support ANSI natively
}
