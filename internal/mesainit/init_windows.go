//go:build windows

// Package mesainit provides early Mesa initialization for Windows.
//
// CRITICAL: This package must be imported BEFORE any Fyne or OpenGL packages
// in main.go. Go runs init() functions in import order, so by importing
// mesainit first, we can preload Mesa's opengl32.dll before Fyne's init()
// triggers Windows to load System32's version.
//
// Usage in main.go:
//
//	import (
//	    _ "github.com/rescale/rescale-int/internal/mesainit"  // MUST BE FIRST!
//	    "github.com/rescale/rescale-int/internal/cli"
//	    "github.com/rescale/rescale-int/internal/gui"
//	)
package mesainit

import (
	"os"
	"strings"

	"github.com/rescale/rescale-int/internal/mesa"
)

func init() {
	// Skip if not GUI mode - no need to load Mesa for CLI operations
	if !isGUIMode() {
		return
	}

	// Allow opting out of software rendering
	if os.Getenv("RESCALE_HARDWARE_RENDER") == "1" {
		return
	}

	// Run early diagnostics if debug mode is enabled
	// This shows what's loaded BEFORE we try anything
	if os.Getenv("RESCALE_MESA_DEBUG") != "" {
		mesa.EarlyDiagnostics()
	}

	// Preload Mesa DLLs BEFORE any Fyne package init() can run.
	// This is our best chance to get Mesa's opengl32.dll loaded before
	// Windows loads System32's version.
	//
	// Note: This may still fail if:
	// - CGO static imports have already loaded opengl32.dll
	// - Another package's init() has loaded it
	// But we have to try.
	_ = mesa.EnsureSoftwareRendering()
}

// isGUIMode checks if the application was launched in GUI mode.
// We check for --gui flag or absence of other command flags.
func isGUIMode() bool {
	for _, arg := range os.Args[1:] {
		// Explicit GUI mode
		if arg == "--gui" || arg == "-gui" {
			return true
		}

		// Mesa doctor also needs OpenGL
		if arg == "--mesa-doctor" {
			return true
		}

		// CLI mode flags - don't need Mesa
		if strings.HasPrefix(arg, "jobs") ||
			strings.HasPrefix(arg, "files") ||
			strings.HasPrefix(arg, "folders") ||
			strings.HasPrefix(arg, "upload") ||
			strings.HasPrefix(arg, "download") ||
			strings.HasPrefix(arg, "hardware") ||
			strings.HasPrefix(arg, "software") ||
			strings.HasPrefix(arg, "pur") ||
			strings.HasPrefix(arg, "config") ||
			arg == "--version" ||
			arg == "-v" ||
			arg == "--help" ||
			arg == "-h" {
			return false
		}
	}

	// Default: If no arguments, we might launch GUI
	// Return true to be safe - we'll load Mesa even if not needed
	return len(os.Args) == 1
}
