// Rescale Interlink Tray Companion - System tray application for Windows.
//
// This is a lightweight tray application that communicates with the
// Rescale Interlink Windows Service via named pipes (IPC).
//
// Build for Windows:
//   GOOS=windows go build -ldflags "-H=windowsgui" ./cmd/rescale-int-tray
//
// Features:
//   - Shows service status in tray icon/tooltip
//   - Menu items: Open GUI, Pause/Resume, Trigger Scan, View Logs, Quit
//   - Communicates with service via IPC (\\.\pipe\rescale-interlink)
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "The tray companion is only supported on Windows")
		os.Exit(1)
	}

	// Windows-specific implementation in main_windows.go
	runTray()
}
