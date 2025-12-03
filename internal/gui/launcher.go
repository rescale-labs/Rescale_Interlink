// Package gui provides the GUI launcher for rescale-int.
package gui

import (
	"fmt"
	"os"
	"runtime"
)

// Run launches the GUI mode.
func Run(args []string) error {
	// Check for headless environment on Linux
	if runtime.GOOS == "linux" {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return fmt.Errorf("GUI mode requires a display. No display detected.\n" +
				"DISPLAY and WAYLAND_DISPLAY are not set.\n" +
				"Use 'rescale-int' without --gui flag for CLI mode")
		}
	}

	// Parse optional config file from args
	configFile := ""
	for i, arg := range args {
		if (arg == "--config" || arg == "-c") && i+1 < len(args) {
			configFile = args[i+1]
			break
		}
	}

	// Launch the full GUI
	return LaunchGUI(configFile)
}
