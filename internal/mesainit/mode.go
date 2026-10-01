package mesainit

import (
	"os"
	"runtime"
	"slices"
)

// GUIMode reports whether the command line opens the GUI: --gui, or no
// arguments where there is a display to show it on. --cli, and any other
// argument, subcommands and typos alike, run the CLI.
func GUIMode() bool {
	switch {
	case slices.Contains(os.Args, "--cli"):
		return false
	case slices.Contains(os.Args, "--gui"):
		return true
	case len(os.Args) > 1:
		return false
	}
	return runtime.GOOS != "linux" || os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}
