//go:build !windows

package main

import (
	"fmt"
	"os"
)

// runTray is a stub for non-Windows platforms.
func runTray() {
	fmt.Fprintln(os.Stderr, "The tray companion is only supported on Windows")
	os.Exit(1)
}
