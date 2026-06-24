// Rescale Interlink Tray Companion - System tray application for Windows.
//
// This is a lightweight tray application that starts and controls the
// auto-download daemon. The daemon runs as a subprocess in the logged-in
// user's session (so it inherits the user's mapped/network drives) and is
// controlled via named pipes (IPC).
//
// Build for Windows:
//   GOOS=windows go build -ldflags "-H=windowsgui" ./cmd/rescale-int-tray
//
// Features:
//   - Auto-starts the daemon at login when auto-download is enabled
//   - Shows daemon status in tray icon/tooltip
//   - Menu items: Open GUI, Pause/Resume, Trigger Scan, View Logs, Quit
//   - Communicates with the daemon via IPC (\\.\pipe\rescale-interlink)
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
